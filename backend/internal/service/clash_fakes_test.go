//go:build unit

package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

type fakeClashAccount struct {
	ID       int64
	Name     string
	Platform string
	Parent   *int64
	ProxyID  *int64
	Reason   string
	Until    *time.Time
}

// fakeClashRepo is an in-memory ClashRepository for service tests.
type fakeClashRepo struct {
	mu        sync.Mutex
	profiles  map[int64]*ClashProfile
	deleted   map[int64]bool
	nodes     map[int64]*ClashNode
	proxies   map[int64]*Proxy
	accounts  map[int64]*fakeClashAccount
	nextID    int64
	refreshes []ClashRefreshRecord
	// traffic holds daily buckets per node and day; trafficErr fails flushes.
	traffic    map[int64]map[string]*ClashTrafficDay
	trafficErr error
}

func newFakeClashRepo() *fakeClashRepo {
	return &fakeClashRepo{
		profiles: map[int64]*ClashProfile{},
		deleted:  map[int64]bool{},
		nodes:    map[int64]*ClashNode{},
		proxies:  map[int64]*Proxy{},
		accounts: map[int64]*fakeClashAccount{},
		nextID:   100,
		traffic:  map[int64]map[string]*ClashTrafficDay{},
	}
}

func (r *fakeClashRepo) id() int64 { r.nextID++; return r.nextID }

func (r *fakeClashRepo) addProfile(name string, enabled bool) *ClashProfile {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := &ClashProfile{ID: r.id(), Name: name, Enabled: enabled, LastRefreshStatus: ClashRefreshNever}
	r.profiles[p.ID] = p
	return p
}

// addNode creates a node with its managed proxy.
func (r *fakeClashRepo) addNode(profileID int64, name, exitIP string, mutate ...func(*ClashNode)) *ClashNode {
	r.mu.Lock()
	defer r.mu.Unlock()
	proxy := &Proxy{ID: r.id(), Name: "Clash·" + name, Protocol: "socks5", Host: "127.0.0.1", Port: 20000 + len(r.nodes),
		Username: "u", Password: "p", Status: StatusActive, Source: ProxySourceClash}
	r.proxies[proxy.ID] = proxy
	n := &ClashNode{ID: r.id(), ProfileID: profileID, Name: name, Type: "trojan", Server: name + ".example.com", ServerPort: 443,
		Config: map[string]any{"name": name, "type": "trojan", "server": name + ".example.com", "port": 443, "password": "pw"},
		Status: ClashNodeStatusActive, HealthStatus: ClashHealthHealthy, ExitIP: exitIP, ExitStatus: ClashExitOK,
		ListenPort: proxy.Port, ProxyID: proxy.ID}
	if exitIP == "" {
		n.ExitStatus = ClashExitUnknown
	}
	for _, m := range mutate {
		m(n)
	}
	r.nodes[n.ID] = n
	return n
}

func (r *fakeClashRepo) addManualProxy() *Proxy {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := &Proxy{ID: r.id(), Name: "manual", Protocol: "http", Host: "10.0.0.1", Port: 8080, Status: StatusActive, Source: ProxySourceManual}
	r.proxies[p.ID] = p
	return p
}

func (r *fakeClashRepo) bind(name string, proxyID int64, parent *int64) *fakeClashAccount {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.id()
	pid := proxyID
	acc := &fakeClashAccount{ID: id, Name: name, Platform: PlatformAnthropic, Parent: parent, ProxyID: &pid}
	r.accounts[id] = acc
	return acc
}

func (r *fakeClashRepo) account(id int64) fakeClashAccount {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.accounts[id]
}

func (r *fakeClashRepo) node(id int64) ClashNode {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.nodes[id]
}

func (r *fakeClashRepo) CreateProfile(_ context.Context, p *ClashProfile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p.ID = r.id()
	p.LastRefreshStatus = ClashRefreshNever
	p.CreatedAt, p.UpdatedAt = time.Now(), time.Now()
	clone := *p
	r.profiles[p.ID] = &clone
	return nil
}

func (r *fakeClashRepo) UpdateProfile(_ context.Context, p *ClashProfile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.profiles[p.ID]; !ok || r.deleted[p.ID] {
		return ErrClashProfileNotFound
	}
	clone := *p
	r.profiles[p.ID] = &clone
	return nil
}

func (r *fakeClashRepo) GetProfile(_ context.Context, id int64) (*ClashProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.profiles[id]
	if !ok || r.deleted[id] {
		return nil, ErrClashProfileNotFound
	}
	clone := *p
	return &clone, nil
}

func (r *fakeClashRepo) ListProfiles(_ context.Context) ([]ClashProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ClashProfile, 0)
	for id, p := range r.profiles {
		if !r.deleted[id] {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *fakeClashRepo) ListProfileStats(_ context.Context) (map[int64]ClashProfileStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[int64]ClashProfileStats{}
	for _, n := range r.nodes {
		s := out[n.ProfileID]
		s.Total++
		if n.Status == ClashNodeStatusActive {
			s.Active++
		}
		if r.boundLocked(n.ProxyID) {
			s.Bound++
		}
		for _, acc := range r.accounts {
			if acc.Parent == nil && acc.ProxyID != nil && *acc.ProxyID == n.ProxyID {
				s.BoundAccounts++
			}
		}
		out[n.ProfileID] = s
	}
	return out, nil
}

func (r *fakeClashRepo) SoftDeleteProfile(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.profiles[id]; !ok {
		return ErrClashProfileNotFound
	}
	r.deleted[id] = true
	return nil
}

func (r *fakeClashRepo) RecordRefresh(_ context.Context, id int64, rec ClashRefreshRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshes = append(r.refreshes, rec)
	if p, ok := r.profiles[id]; ok {
		at := rec.At
		p.LastRefreshAt, p.LastRefreshStatus, p.LastRefreshError = &at, rec.Status, rec.Error
	}
	return nil
}

func (r *fakeClashRepo) ExistsProfileName(_ context.Context, name string, excludeID int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, p := range r.profiles {
		if !r.deleted[id] && id != excludeID && p.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func (r *fakeClashRepo) ExistsProfileURL(_ context.Context, fp string, excludeID int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, p := range r.profiles {
		if !r.deleted[id] && id != excludeID && p.URLFingerprint == fp {
			return true, nil
		}
	}
	return false, nil
}

func (r *fakeClashRepo) ListNodesByProfile(_ context.Context, profileID int64) ([]ClashNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ClashNode, 0)
	for _, n := range r.nodes {
		if n.ProfileID == profileID {
			out = append(out, *n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *fakeClashRepo) viewsLocked() []ClashNodeView {
	views := make([]ClashNodeView, 0, len(r.nodes))
	for _, n := range r.nodes {
		p := r.profiles[n.ProfileID]
		view := ClashNodeView{ClashNode: *n, ProfileName: p.Name, ProfileEnabled: p.Enabled, ProfileDeleted: r.deleted[p.ID]}
		for _, acc := range r.accounts {
			if acc.ProxyID != nil && *acc.ProxyID == n.ProxyID {
				view.Accounts = append(view.Accounts, ClashBoundAccount{ID: acc.ID, Name: acc.Name, Platform: acc.Platform, ParentAccountID: acc.Parent})
			}
		}
		sort.Slice(view.Accounts, func(i, j int) bool { return view.Accounts[i].ID < view.Accounts[j].ID })
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	return views
}

func (r *fakeClashRepo) filteredViewsLocked(filter ClashNodeFilter) []ClashNodeView {
	out := make([]ClashNodeView, 0)
	for _, v := range r.viewsLocked() {
		if filter.ProfileID != nil && v.ProfileID != *filter.ProfileID {
			continue
		}
		if filter.Status != "" && v.Status != filter.Status {
			continue
		}
		if filter.Search != "" && !strings.Contains(v.Name, filter.Search) {
			continue
		}
		if filter.LiveOnly && !clashNodeLive(&v) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func (r *fakeClashRepo) ListNodeViews(_ context.Context, filter ClashNodeFilter, params pagination.PaginationParams) ([]ClashNodeView, *pagination.PaginationResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.filteredViewsLocked(filter)
	return out, &pagination.PaginationResult{Total: int64(len(out)), Page: 1, PageSize: len(out)}, nil
}

func (r *fakeClashRepo) ListNodeIDs(_ context.Context, filter ClashNodeFilter) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]int64, 0)
	for _, v := range r.filteredViewsLocked(filter) {
		ids = append(ids, v.ID)
	}
	return ids, nil
}

func (r *fakeClashRepo) AddNodeTraffic(_ context.Context, incs []ClashTrafficIncrement, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.trafficErr != nil {
		return r.trafficErr
	}
	for _, inc := range incs {
		if _, ok := r.nodes[inc.NodeID]; !ok {
			continue
		}
		days := r.traffic[inc.NodeID]
		if days == nil {
			days = map[string]*ClashTrafficDay{}
			r.traffic[inc.NodeID] = days
		}
		day := days[inc.Date]
		if day == nil {
			day = &ClashTrafficDay{Date: inc.Date}
			days[inc.Date] = day
		}
		day.Upload += inc.Upload
		day.Download += inc.Download
	}
	return nil
}

func (r *fakeClashRepo) trafficDay(nodeID int64, date string) ClashTrafficDay {
	r.mu.Lock()
	defer r.mu.Unlock()
	if day := r.traffic[nodeID][date]; day != nil {
		return *day
	}
	return ClashTrafficDay{Date: date}
}

func (r *fakeClashRepo) ListNodeTraffic(_ context.Context, nodeIDs []int64, since string) (map[int64]*ClashNodeTrafficTotals, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[int64]*ClashNodeTrafficTotals{}
	for _, id := range nodeIDs {
		days := r.traffic[id]
		if len(days) == 0 {
			continue
		}
		totals := &ClashNodeTrafficTotals{}
		for _, day := range days {
			totals.Upload += day.Upload
			totals.Download += day.Download
			if day.Date >= since {
				totals.Days = append(totals.Days, *day)
			}
		}
		sort.Slice(totals.Days, func(i, j int) bool { return totals.Days[i].Date < totals.Days[j].Date })
		out[id] = totals
	}
	return out, nil
}

func (r *fakeClashRepo) ListProfileTraffic(_ context.Context, today string) (map[int64]ClashProfileTraffic, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[int64]ClashProfileTraffic{}
	for id, days := range r.traffic {
		node := r.nodes[id]
		if node == nil {
			continue
		}
		sum := out[node.ProfileID]
		for _, day := range days {
			sum.Upload += day.Upload
			sum.Download += day.Download
			if day.Date == today {
				sum.TodayUpload += day.Upload
				sum.TodayDownload += day.Download
			}
		}
		out[node.ProfileID] = sum
	}
	return out, nil
}

func (r *fakeClashRepo) ListAllNodeViews(_ context.Context) ([]ClashNodeView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.viewsLocked(), nil
}

func (r *fakeClashRepo) GetNodeView(_ context.Context, id int64) (*ClashNodeView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.viewsLocked() {
		if v.ID == id {
			view := v
			return &view, nil
		}
	}
	return nil, ErrClashNodeNotFound
}

func (r *fakeClashRepo) GetNodeViewByProxyID(_ context.Context, proxyID int64) (*ClashNodeView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.viewsLocked() {
		if v.ProxyID == proxyID {
			view := v
			return &view, nil
		}
	}
	return nil, ErrClashNodeNotFound
}

func (r *fakeClashRepo) ApplyNodeSync(_ context.Context, profileID int64, plan *ClashNodeSyncPlan, ports ClashPortRange, spec ClashManagedProxySpecFactory) (*ClashNodeSyncResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res := &ClashNodeSyncResult{}
	used := map[int]bool{}
	for _, n := range r.nodes {
		used[n.ListenPort] = true
	}
	for _, insert := range plan.Inserts {
		port := ports.Start
		for used[port] {
			port++
		}
		if port > ports.End {
			return nil, errors.New("no free port")
		}
		used[port] = true
		s := spec()
		proxy := &Proxy{ID: r.id(), Name: insert.ProxyName, Protocol: "socks5", Host: s.Host, Port: port,
			Username: s.Username, Password: s.Password, Status: StatusActive, Source: ProxySourceClash}
		r.proxies[proxy.ID] = proxy
		n := &ClashNode{ID: r.id(), ProfileID: profileID, Name: insert.Name, Type: insert.Type, Server: insert.Server,
			ServerPort: insert.ServerPort, Config: insert.Config, ConfigHash: insert.ConfigHash, Status: insert.Status,
			StatusReason: insert.StatusReason, ListenPort: port, ProxyID: proxy.ID, HealthStatus: ClashHealthUnknown, ExitStatus: ClashExitUnknown}
		r.nodes[n.ID] = n
		res.Inserted++
		res.Structural = true
	}
	for _, u := range plan.Updates {
		n := r.nodes[u.NodeID]
		if u.ConfigChanged || n.Status != u.Status {
			res.Structural = true
		}
		n.Name, n.Type, n.Server, n.ServerPort, n.Config, n.ConfigHash = u.Name, u.Type, u.Server, u.ServerPort, u.Config, u.ConfigHash
		n.Status, n.StatusReason = u.Status, u.StatusReason
		if u.ConfigChanged && n.ExitStatus == ClashExitOK {
			n.ExitStatus = ClashExitStale
		}
		r.proxies[n.ProxyID].Name = u.ProxyName
		res.Updated++
	}
	for _, id := range plan.Missing {
		n := r.nodes[id]
		if n.Status == ClashNodeStatusActive || n.Status == ClashNodeStatusInvalid {
			n.Status, n.StatusReason = ClashNodeStatusMissing, "removed from subscription"
			res.Missing++
			res.Structural = true
		}
	}
	return res, nil
}

func (r *fakeClashRepo) SetNodeStatus(_ context.Context, id int64, status, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return ErrClashNodeNotFound
	}
	n.Status, n.StatusReason = status, reason
	return nil
}

func (r *fakeClashRepo) MarkProfileNodes(_ context.Context, profileID int64, status, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.nodes {
		if n.ProfileID == profileID {
			n.Status, n.StatusReason = status, reason
		}
	}
	return nil
}

func (r *fakeClashRepo) UpdateNodeHealth(_ context.Context, updates []ClashNodeHealthUpdate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range updates {
		n := r.nodes[u.NodeID]
		n.HealthStatus, n.ConsecutiveFailures, n.ConsecutiveSuccesses = u.HealthStatus, u.ConsecutiveFailures, u.ConsecutiveSuccesses
		if u.LatencyMs != nil {
			n.LatencyMs = u.LatencyMs
		}
		checked := u.CheckedAt
		n.LastCheckedAt, n.LastCheckError = &checked, u.Error
	}
	return nil
}

func (r *fakeClashRepo) UpdateNodeExit(_ context.Context, u ClashNodeExitUpdate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.nodes[u.NodeID]
	n.ExitIP, n.ExitCountry, n.ExitCountryCode, n.ExitRegion, n.ExitCity = u.ExitIP, u.Country, u.CountryCode, u.Region, u.City
	n.ExitStatus, n.ExitPendingIP = u.ExitStatus, u.PendingIP
	if u.ChangedAt != nil {
		n.ExitChangedAt = u.ChangedAt
	}
	checked := u.CheckedAt
	n.ExitCheckedAt = &checked
	if u.PlatformChecks != nil {
		n.PlatformChecks = *u.PlatformChecks
	}
	return nil
}

func (r *fakeClashRepo) AcceptNodeExit(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.nodes[id]
	if n == nil || n.ExitStatus != ClashExitChanged || n.ExitPendingIP == "" {
		return ErrClashNodeNotFound
	}
	n.ExitIP, n.ExitPendingIP, n.ExitStatus = n.ExitPendingIP, "", ClashExitStale
	return nil
}

func (r *fakeClashRepo) boundLocked(proxyID int64) bool {
	for _, acc := range r.accounts {
		if acc.ProxyID != nil && *acc.ProxyID == proxyID {
			return true
		}
	}
	return false
}

func (r *fakeClashRepo) ReclaimNodes(_ context.Context, before time.Time) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []int64
	for id, n := range r.nodes {
		if (n.Status == ClashNodeStatusMissing || r.deleted[n.ProfileID]) && !r.boundLocked(n.ProxyID) {
			delete(r.nodes, id)
			out = append(out, n.ProxyID)
		}
	}
	return out, nil
}

func (r *fakeClashRepo) ListRenderNodes(_ context.Context) ([]ClashRenderNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ClashRenderNode, 0)
	for _, n := range r.nodes {
		p := r.profiles[n.ProfileID]
		proxy := r.proxies[n.ProxyID]
		node := ClashRenderNode{NodeID: n.ID, ListenPort: n.ListenPort, Username: proxy.Username, Password: proxy.Password}
		live := n.Status == ClashNodeStatusActive && p.Enabled && !r.deleted[p.ID]
		switch {
		case live:
			node.Config = n.Config
		case r.boundLocked(n.ProxyID):
		default:
			continue
		}
		out = append(out, node)
	}
	return out, nil
}

func (r *fakeClashRepo) RehostManagedProxies(_ context.Context, host string) ([]int64, error) {
	return nil, nil
}

func (r *fakeClashRepo) PauseAccounts(_ context.Context, reasons map[int64]string, until, renewBefore time.Time) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var changed []int64
	for id, reason := range reasons {
		acc := r.accounts[id]
		if acc == nil {
			continue
		}
		if acc.Until == nil || acc.Until.Before(renewBefore) || (strings.HasPrefix(acc.Reason, ClashPauseReasonPrefix) && acc.Reason != reason) {
			u := until
			acc.Until, acc.Reason = &u, reason
			changed = append(changed, id)
		}
	}
	return changed, nil
}

func (r *fakeClashRepo) ClearPauses(_ context.Context, ids []int64) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var cleared []int64
	for _, id := range ids {
		acc := r.accounts[id]
		if acc != nil && strings.HasPrefix(acc.Reason, ClashPauseReasonPrefix) {
			acc.Until, acc.Reason = nil, ""
			cleared = append(cleared, id)
		}
	}
	return cleared, nil
}

func (r *fakeClashRepo) ListPausedAccounts(_ context.Context) (map[int64]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[int64]string{}
	for id, acc := range r.accounts {
		if strings.HasPrefix(acc.Reason, ClashPauseReasonPrefix) && acc.Until != nil && acc.Until.After(time.Now()) {
			out[id] = acc.Reason
		}
	}
	return out, nil
}

// fakeClashProxyRepo serves proxies from the fake Clash repo.
type fakeClashProxyRepo struct {
	proxyRepoStub
	repo *fakeClashRepo
}

func (p *fakeClashProxyRepo) GetByID(_ context.Context, id int64) (*Proxy, error) {
	p.repo.mu.Lock()
	defer p.repo.mu.Unlock()
	proxy, ok := p.repo.proxies[id]
	if !ok {
		return nil, ErrProxyNotFound
	}
	clone := *proxy
	return &clone, nil
}

func (p *fakeClashProxyRepo) ListByIDs(_ context.Context, ids []int64) ([]Proxy, error) {
	p.repo.mu.Lock()
	defer p.repo.mu.Unlock()
	out := make([]Proxy, 0, len(ids))
	for _, id := range ids {
		if proxy, ok := p.repo.proxies[id]; ok {
			out = append(out, *proxy)
		}
	}
	return out, nil
}

// fakeClashRuntime records applied payloads and scripts delay results.
type fakeClashRuntime struct {
	mu        sync.Mutex
	ready     bool
	applied   [][]byte
	applyErrs []error
	proxies   map[string]bool
	delays    map[string]error
	flaky     map[string]int
	attempts  map[string]int
	// conns is the connection table returned by Connections.
	conns    []ClashConnection
	connsErr error
}

func newFakeClashRuntime() *fakeClashRuntime {
	return &fakeClashRuntime{ready: true, proxies: map[string]bool{}, delays: map[string]error{}, flaky: map[string]int{}, attempts: map[string]int{}}
}

func (f *fakeClashRuntime) Mode() string                { return "embedded" }
func (f *fakeClashRuntime) Start(context.Context) error { return nil }
func (f *fakeClashRuntime) Stop(context.Context) error  { return nil }
func (f *fakeClashRuntime) Version() string             { return "test" }
func (f *fakeClashRuntime) Ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready
}

func (f *fakeClashRuntime) Apply(_ context.Context, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.applyErrs) > 0 {
		err := f.applyErrs[0]
		f.applyErrs = f.applyErrs[1:]
		if err != nil {
			return err
		}
	}
	f.applied = append(f.applied, payload)
	f.proxies = map[string]bool{}
	for _, line := range strings.Split(string(payload), "\n") {
		if idx := strings.Index(line, "name: "); idx >= 0 {
			f.proxies[strings.TrimSpace(line[idx+6:])] = true
		}
	}
	return nil
}

func (f *fakeClashRuntime) HasProxy(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.proxies[name], nil
}

func (f *fakeClashRuntime) DelayTest(_ context.Context, name, _ string, _ time.Duration) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attempts == nil {
		f.attempts = map[string]int{}
	}
	f.attempts[name]++
	if f.flaky != nil && f.flaky[name] > 0 {
		f.flaky[name]--
		return 0, errors.New("timeout")
	}
	if err := f.delays[name]; err != nil {
		return 0, err
	}
	return 42 * time.Millisecond, nil
}

func (f *fakeClashRuntime) CloseInboundConnections(context.Context, []string) (int, error) {
	return 0, nil
}

func (f *fakeClashRuntime) Connections(context.Context) ([]ClashConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connsErr != nil {
		return nil, f.connsErr
	}
	return append([]ClashConnection(nil), f.conns...), nil
}

func (f *fakeClashRuntime) setConnections(conns ...ClashConnection) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns = conns
}

func (f *fakeClashRuntime) appliedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applied)
}

// fakeExitProber maps proxy URLs (by port) to egress IPs; "" is the direct route.
type fakeExitProber struct {
	mu       sync.Mutex
	directIP string
	byPort   map[string]string
	// bare ports answer like the fallback service: the IP without a location.
	bare  map[string]bool
	calls int
}

func (f *fakeExitProber) ProbeProxy(_ context.Context, proxyURL string) (*ProxyExitInfo, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if proxyURL == "" {
		return &ProxyExitInfo{IP: f.directIP}, 1, nil
	}
	for port, ip := range f.byPort {
		if strings.HasSuffix(proxyURL, ":"+port) {
			if ip == "" {
				return nil, 0, errors.New("probe failed")
			}
			if f.bare[port] {
				return &ProxyExitInfo{IP: ip}, 10, nil
			}
			return &ProxyExitInfo{IP: ip, Country: "US", CountryCode: "US"}, 10, nil
		}
	}
	return nil, 0, errors.New("unknown proxy")
}

type fakeClashEncryptor struct{}

func (fakeClashEncryptor) Encrypt(plain string) (string, error) { return "enc:" + plain, nil }
func (fakeClashEncryptor) Decrypt(cipher string) (string, error) {
	return strings.TrimPrefix(cipher, "enc:"), nil
}
