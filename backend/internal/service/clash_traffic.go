package service

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"go.uber.org/zap"
)

const (
	// clashTrafficSampleInterval bounds what a closing connection loses: the
	// core forgets a connection's counters on close, so only bytes moved after
	// the last sample go uncounted.
	clashTrafficSampleInterval = 3 * time.Second
	clashTrafficFlushInterval  = 30 * time.Second
	// clashTrafficLiveTTL drops throughput published by a sampler that stopped.
	clashTrafficLiveTTL = 20 * time.Second
	// clashTrafficTrendDays is the per-node daily trend shown on the admin page.
	clashTrafficTrendDays = 7
	// clashTrafficMaxPending caps buckets kept across failed flushes.
	clashTrafficMaxPending = 50000
	// clashTrafficOwnerCheck is how often a shared-core sampler confirms (or
	// tries to take) the sampling lock.
	clashTrafficOwnerCheck = 30 * time.Second
	clashTrafficLockKey    = "clash_pool:traffic_sampler"
	clashTrafficDateLayout = "2006-01-02"
)

// Node listing orders.
const (
	ClashNodeSortName         = "name"
	ClashNodeSortLatency      = "latency"
	ClashNodeSortTrafficToday = "traffic_today"
	ClashNodeSortTrafficTotal = "traffic_total"
)

// NormalizeClashNodeSort maps unknown orders to the default ("").
func NormalizeClashNodeSort(sort string) string {
	switch sort {
	case ClashNodeSortName, ClashNodeSortLatency, ClashNodeSortTrafficToday, ClashNodeSortTrafficTotal:
		return sort
	default:
		return ""
	}
}

// ClashTrafficIncrement adds bytes to one node's daily bucket.
type ClashTrafficIncrement struct {
	NodeID int64
	// Date is the bucket day (YYYY-MM-DD) in the configured timezone.
	Date     string
	Upload   int64
	Download int64
}

// ClashTrafficDay is one daily bucket.
type ClashTrafficDay struct {
	Date     string
	Upload   int64
	Download int64
}

// ClashNodeTrafficTotals is the stored traffic of one node.
type ClashNodeTrafficTotals struct {
	Upload    int64
	Download  int64
	UpdatedAt *time.Time
	// Days holds the buckets on or after the requested day, oldest first.
	Days []ClashTrafficDay
}

// ClashNodeLiveTraffic is a node's throughput over the latest sample window.
type ClashNodeLiveTraffic struct {
	UploadRate   int64 `json:"up"`
	DownloadRate int64 `json:"down"`
	Connections  int   `json:"conns"`
}

// ClashTrafficLive is one sampler's latest throughput, per node.
type ClashTrafficLive struct {
	InstanceID string                         `json:"instance_id"`
	UpdatedAt  time.Time                      `json:"updated_at"`
	Nodes      map[int64]ClashNodeLiveTraffic `json:"nodes"`
}

// ClashNodeTraffic is the traffic summary attached to node listings.
type ClashNodeTraffic struct {
	Upload        int64
	Download      int64
	TodayUpload   int64
	TodayDownload int64
	// Daily covers the trend window, oldest first, one entry per day.
	Daily     []ClashTrafficDay
	UpdatedAt *time.Time
	ClashNodeLiveTraffic
}

// ClashProfileTraffic sums the measured traffic of a profile's nodes.
type ClashProfileTraffic struct {
	Upload        int64
	Download      int64
	TodayUpload   int64
	TodayDownload int64
}

func clashTrafficDate(t time.Time) string {
	return t.In(timezone.Location()).Format(clashTrafficDateLayout)
}

// clashTrafficTrendDates lists the trend window ending today, oldest first.
func clashTrafficTrendDates(now time.Time) []string {
	start := timezone.StartOfDay(now)
	dates := make([]string, clashTrafficTrendDays)
	for i := range dates {
		dates[i] = start.AddDate(0, 0, i-(clashTrafficTrendDays-1)).Format(clashTrafficDateLayout)
	}
	return dates
}

type clashTrafficSeen struct {
	nodeID   int64
	upload   int64
	download int64
}

type clashTrafficKey struct {
	nodeID int64
	date   string
}

// clashTrafficSampler turns successive connection snapshots of one core into
// per-node daily increments and throughput.
type clashTrafficSampler struct {
	// since separates connections this sampler owns from older ones: a
	// connection opened before it is only counted from its first observed
	// counters, because its earlier bytes predate sampling or were counted by
	// the previous owner of a shared core.
	since   time.Time
	seen    map[string]clashTrafficSeen
	pending map[clashTrafficKey]*ClashTrafficIncrement
	lastAt  time.Time
	live    map[int64]ClashNodeLiveTraffic
}

func newClashTrafficSampler(now time.Time) *clashTrafficSampler {
	s := &clashTrafficSampler{pending: make(map[clashTrafficKey]*ClashTrafficIncrement)}
	s.reset(now)
	return s
}

// reset forgets connection baselines; pending increments are kept.
func (s *clashTrafficSampler) reset(now time.Time) {
	s.since = now
	s.seen = make(map[string]clashTrafficSeen)
	s.lastAt = time.Time{}
	s.live = nil
}

// observe folds one snapshot into pending increments and live throughput.
func (s *clashTrafficSampler) observe(conns []ClashConnection, now time.Time) {
	date := clashTrafficDate(now)
	present := make(map[string]struct{}, len(conns))
	window := make(map[int64]*ClashNodeLiveTraffic)
	for _, conn := range conns {
		nodeID, ok := ParseClashListenerName(conn.Inbound)
		if !ok || conn.ID == "" {
			continue
		}
		present[conn.ID] = struct{}{}
		var baseUp, baseDown int64
		if prev, known := s.seen[conn.ID]; known && prev.nodeID == nodeID {
			baseUp, baseDown = prev.upload, prev.download
		} else if conn.Start.IsZero() || conn.Start.Before(s.since) {
			baseUp, baseDown = conn.Upload, conn.Download
		}
		s.seen[conn.ID] = clashTrafficSeen{nodeID: nodeID, upload: conn.Upload, download: conn.Download}
		up, down := clashNonNegative(conn.Upload-baseUp), clashNonNegative(conn.Download-baseDown)

		entry := window[nodeID]
		if entry == nil {
			entry = &ClashNodeLiveTraffic{}
			window[nodeID] = entry
		}
		entry.Connections++
		entry.UploadRate += up
		entry.DownloadRate += down
		if up == 0 && down == 0 {
			continue
		}
		key := clashTrafficKey{nodeID: nodeID, date: date}
		inc := s.pending[key]
		if inc == nil {
			if len(s.pending) >= clashTrafficMaxPending {
				continue
			}
			inc = &ClashTrafficIncrement{NodeID: nodeID, Date: date}
			s.pending[key] = inc
		}
		inc.Upload += up
		inc.Download += down
	}
	for id := range s.seen {
		if _, ok := present[id]; !ok {
			delete(s.seen, id)
		}
	}

	// Rates need a previous sample: the first window may hold bytes moved
	// long before it.
	elapsed := 0.0
	if !s.lastAt.IsZero() {
		elapsed = now.Sub(s.lastAt).Seconds()
	}
	live := make(map[int64]ClashNodeLiveTraffic, len(window))
	for nodeID, entry := range window {
		rate := ClashNodeLiveTraffic{Connections: entry.Connections}
		if elapsed > 0 {
			rate.UploadRate = int64(float64(entry.UploadRate) / elapsed)
			rate.DownloadRate = int64(float64(entry.DownloadRate) / elapsed)
		}
		live[nodeID] = rate
	}
	s.live = live
	s.lastAt = now
}

func clashNonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// drain returns and clears the pending increments, ordered by node and day
// so concurrent flushes from several instances lock rows in the same order.
func (s *clashTrafficSampler) drain() []ClashTrafficIncrement {
	out := make([]ClashTrafficIncrement, 0, len(s.pending))
	for _, inc := range s.pending {
		out = append(out, *inc)
	}
	s.pending = make(map[clashTrafficKey]*ClashTrafficIncrement)
	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		return out[i].Date < out[j].Date
	})
	return out
}

// restore puts back increments whose flush failed.
func (s *clashTrafficSampler) restore(incs []ClashTrafficIncrement) {
	for _, inc := range incs {
		key := clashTrafficKey{nodeID: inc.NodeID, date: inc.Date}
		existing := s.pending[key]
		if existing == nil {
			if len(s.pending) >= clashTrafficMaxPending {
				continue
			}
			copied := inc
			s.pending[key] = &copied
			continue
		}
		existing.Upload += inc.Upload
		existing.Download += inc.Download
	}
}

func (s *clashTrafficSampler) liveSnapshot() map[int64]ClashNodeLiveTraffic {
	out := make(map[int64]ClashNodeLiveTraffic, len(s.live))
	for id, rate := range s.live {
		out[id] = rate
	}
	return out
}

// clashTrafficOwnership decides whether this instance samples its core. An
// embedded core only carries its own instance's connections, so every
// instance samples. An external sidecar may be shared by several instances;
// one of them samples it while holding a session-level advisory lock, so its
// connections are not counted once per instance.
type clashTrafficOwnership struct {
	shared    bool
	db        *sql.DB
	lockID    int64
	conn      *sql.Conn
	checkedAt time.Time
}

func newClashTrafficOwnership(mode string, db *sql.DB) *clashTrafficOwnership {
	return &clashTrafficOwnership{
		shared: mode == config.ClashPoolModeExternal,
		db:     db,
		lockID: hashAdvisoryLockID(clashTrafficLockKey),
	}
}

// hold reports whether this instance samples now, and whether it just became
// the sampler (its connection baselines must be reset).
func (o *clashTrafficOwnership) hold(ctx context.Context, now time.Time) (owned, acquired bool) {
	if !o.shared || o.db == nil {
		return true, false
	}
	if now.Sub(o.checkedAt) < clashTrafficOwnerCheck {
		return o.conn != nil, false
	}
	o.checkedAt = now
	if o.conn != nil {
		if err := o.conn.PingContext(ctx); err == nil {
			return true, false
		}
		// The session (and with it the lock) is gone.
		o.release()
	}
	conn, err := o.db.Conn(ctx)
	if err != nil {
		return false, false
	}
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", o.lockID).Scan(&got); err != nil || !got {
		_ = conn.Close()
		return false, false
	}
	o.conn = conn
	return true, true
}

func (o *clashTrafficOwnership) release() {
	if o.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = o.conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", o.lockID)
	_ = o.conn.Close()
	o.conn = nil
}

// trafficLoop samples the local core's connections and records per-node
// traffic. Every instance runs it; see clashTrafficOwnership for shared cores.
func (m *ClashManager) trafficLoop(ctx context.Context) {
	defer m.wg.Done()
	sampler := newClashTrafficSampler(time.Now())
	owner := newClashTrafficOwnership(m.runtime.Mode(), m.db)
	defer owner.release()
	ticker := time.NewTicker(clashTrafficSampleInterval)
	defer ticker.Stop()
	lastFlush := time.Now()
	owned := false
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			m.flushTraffic(flushCtx, sampler)
			cancel()
			return
		case <-ticker.C:
		}
		now := time.Now()
		holds, acquired := owner.hold(ctx, now)
		if !holds {
			if owned {
				m.flushTraffic(ctx, sampler)
				m.setLocalTrafficLive(nil)
			}
			owned = false
			continue
		}
		if acquired {
			sampler.reset(now)
		}
		owned = true
		m.sampleTraffic(ctx, sampler, now)
		if now.Sub(lastFlush) >= clashTrafficFlushInterval {
			m.flushTraffic(ctx, sampler)
			lastFlush = now
		}
	}
}

func (m *ClashManager) sampleTraffic(ctx context.Context, sampler *clashTrafficSampler, now time.Time) {
	sampleCtx, cancel := context.WithTimeout(ctx, clashTrafficSampleInterval)
	defer cancel()
	conns, err := m.runtime.Connections(sampleCtx)
	if err != nil {
		// Core restarting or unreachable: keep the baselines, the next sample
		// resumes where this one would have.
		return
	}
	sampler.observe(conns, now)
	live := ClashTrafficLive{InstanceID: m.instanceID, UpdatedAt: now.UTC(), Nodes: sampler.liveSnapshot()}
	m.setLocalTrafficLive(&live)
	if m.notifier != nil {
		if err := m.notifier.PublishTrafficLive(sampleCtx, live, clashTrafficLiveTTL); err != nil {
			m.log().Debug("publish clash traffic failed", zap.Error(err))
		}
	}
}

func (m *ClashManager) flushTraffic(ctx context.Context, sampler *clashTrafficSampler) {
	incs := sampler.drain()
	if len(incs) == 0 {
		return
	}
	if err := m.svc.repo.AddNodeTraffic(ctx, incs, time.Now().UTC()); err != nil {
		sampler.restore(incs)
		m.log().Warn("record clash node traffic failed", zap.Error(err))
	}
}

func (m *ClashManager) setLocalTrafficLive(live *ClashTrafficLive) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trafficLive = live
}

func (m *ClashManager) localTrafficLive() *ClashTrafficLive {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.trafficLive == nil {
		return nil
	}
	live := *m.trafficLive
	return &live
}

// attachNodeTraffic fills the traffic summary of listed nodes: stored daily
// buckets plus the latest throughput reported by every sampling instance.
func (s *ClashService) attachNodeTraffic(ctx context.Context, views []ClashNodeView) {
	if len(views) == 0 {
		return
	}
	now := time.Now()
	dates := clashTrafficTrendDates(now)
	ids := make([]int64, 0, len(views))
	for i := range views {
		ids = append(ids, views[i].ID)
	}
	totals, err := s.repo.ListNodeTraffic(ctx, ids, dates[0])
	if err != nil {
		s.log().Warn("load clash node traffic failed", zap.Error(err))
	}
	live := s.liveTraffic(ctx, now)
	index := make(map[string]int, len(dates))
	for i, date := range dates {
		index[date] = i
	}
	for i := range views {
		traffic := ClashNodeTraffic{Daily: make([]ClashTrafficDay, len(dates))}
		for j, date := range dates {
			traffic.Daily[j].Date = date
		}
		if stored := totals[views[i].ID]; stored != nil {
			traffic.Upload, traffic.Download, traffic.UpdatedAt = stored.Upload, stored.Download, stored.UpdatedAt
			for _, day := range stored.Days {
				if j, ok := index[day.Date]; ok {
					traffic.Daily[j].Upload, traffic.Daily[j].Download = day.Upload, day.Download
				}
			}
		}
		today := traffic.Daily[len(traffic.Daily)-1]
		traffic.TodayUpload, traffic.TodayDownload = today.Upload, today.Download
		traffic.ClashNodeLiveTraffic = live[views[i].ID]
		views[i].Traffic = traffic
	}
}

// liveTraffic sums the fresh throughput snapshots of every sampler.
func (s *ClashService) liveTraffic(ctx context.Context, now time.Time) map[int64]ClashNodeLiveTraffic {
	var snapshots []ClashTrafficLive
	if s.notifier != nil {
		list, err := s.notifier.ListTrafficLive(ctx)
		if err != nil {
			s.log().Debug("load clash traffic snapshots failed", zap.Error(err))
		}
		snapshots = list
	}
	if len(snapshots) == 0 && s.localTrafficLive != nil {
		if local := s.localTrafficLive(); local != nil {
			snapshots = []ClashTrafficLive{*local}
		}
	}
	out := make(map[int64]ClashNodeLiveTraffic)
	for _, snapshot := range snapshots {
		if now.Sub(snapshot.UpdatedAt) > clashTrafficLiveTTL {
			continue
		}
		for id, rate := range snapshot.Nodes {
			sum := out[id]
			sum.UploadRate += rate.UploadRate
			sum.DownloadRate += rate.DownloadRate
			sum.Connections += rate.Connections
			out[id] = sum
		}
	}
	return out
}

// attachProfileTraffic adds the measured traffic of each profile's nodes.
func (s *ClashService) attachProfileTraffic(ctx context.Context, summaries []ClashProfileSummary) {
	if len(summaries) == 0 {
		return
	}
	traffic, err := s.repo.ListProfileTraffic(ctx, clashTrafficDate(time.Now()))
	if err != nil {
		s.log().Warn("load clash profile traffic failed", zap.Error(err))
		return
	}
	for i := range summaries {
		summaries[i].Traffic = traffic[summaries[i].ID]
	}
}
