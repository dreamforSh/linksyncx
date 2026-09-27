package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/clashsub"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"go.uber.org/zap"
)

// ClashService is the Clash proxy pool facade: subscriptions, nodes, exit
// probing, pause reconciliation and the account binding guard.
type ClashService struct {
	cfg              *config.Config
	repo             ClashRepository
	proxyRepo        ProxyRepository
	settings         *SettingService
	encryptor        SecretEncryptor
	runtime          ClashRuntime
	notifier         ClashRuntimeNotifier
	prober           ProxyExitInfoProber
	latencyCache     ProxyLatencyCache
	tempUnschedCache TempUnschedCache

	refreshMu    sync.Mutex
	refreshLocks map[int64]*sync.Mutex
	guardMu      sync.Mutex

	directMu   sync.Mutex
	directIP   string
	directIPAt time.Time

	// onStructuralChange lets the manager resync the local core immediately.
	onStructuralChange func()
	// localTrafficLive reads this instance's throughput when no notifier
	// shares the snapshots of every instance.
	localTrafficLive func() *ClashTrafficLive
}

// NewClashService wires the Clash pool facade. runtime is nil when the pool is disabled.
func NewClashService(
	cfg *config.Config,
	repo ClashRepository,
	proxyRepo ProxyRepository,
	settings *SettingService,
	encryptor SecretEncryptor,
	runtime ClashRuntime,
	notifier ClashRuntimeNotifier,
	prober ProxyExitInfoProber,
	latencyCache ProxyLatencyCache,
	tempUnschedCache TempUnschedCache,
) *ClashService {
	return &ClashService{
		cfg:              cfg,
		repo:             repo,
		proxyRepo:        proxyRepo,
		settings:         settings,
		encryptor:        encryptor,
		runtime:          runtime,
		notifier:         notifier,
		prober:           prober,
		latencyCache:     latencyCache,
		tempUnschedCache: tempUnschedCache,
		refreshLocks:     make(map[int64]*sync.Mutex),
	}
}

func (s *ClashService) log() *zap.Logger {
	return logger.L().With(zap.String("component", "service.clash"))
}

// Mode reports clash_pool.mode.
func (s *ClashService) Mode() string {
	if s == nil || s.cfg == nil {
		return config.ClashPoolModeDisabled
	}
	return s.cfg.ClashPool.Mode
}

// Enabled reports whether a core is configured.
func (s *ClashService) Enabled() bool {
	return s != nil && s.Mode() != config.ClashPoolModeDisabled && s.runtime != nil
}

func (s *ClashService) requireEnabled() error {
	if !s.Enabled() {
		return ErrClashPoolDisabled
	}
	return nil
}

func (s *ClashService) poolSettings(ctx context.Context) *ClashPoolSettings {
	settings, err := s.settings.GetClashPoolSettings(ctx)
	if err != nil || settings == nil {
		return defaultClashPoolSettings()
	}
	return settings
}

// structuralChange fans a configuration change out to every instance.
func (s *ClashService) structuralChange(ctx context.Context) {
	if s.onStructuralChange != nil {
		s.onStructuralChange()
	}
	if s.notifier != nil {
		if err := s.notifier.NotifyConfigChanged(ctx); err != nil {
			s.log().Warn("clash config change notify failed", zap.Error(err))
		}
	}
}

// ClashProfileInput creates or updates a subscription. Nil pointers keep the
// current value on update; an empty URL keeps the stored one.
type ClashProfileInput struct {
	Name                   *string
	URL                    *string
	UserAgent              *string
	Enabled                *bool
	RefreshIntervalMinutes *int
	IncludePattern         *string
	ExcludePattern         *string
	FetchProxyID           *int64
	ClearFetchProxy        bool
	Notes                  *string
}

// ClashProfileSummary is a profile with node statistics.
type ClashProfileSummary struct {
	ClashProfile
	Stats ClashProfileStats
	// Traffic is measured locally, unlike the provider-reported usage.
	Traffic ClashProfileTraffic
}

// ClashRefreshResult reports one refresh attempt.
type ClashRefreshResult struct {
	ProfileID int64              `json:"profile_id"`
	Status    string             `json:"status"`
	Error     string             `json:"error,omitempty"`
	Format    string             `json:"format,omitempty"`
	Parsed    int                `json:"parsed"`
	Filtered  int                `json:"filtered"`
	Private   int                `json:"private"`
	Inserted  int                `json:"inserted"`
	Updated   int                `json:"updated"`
	Missing   int                `json:"missing"`
	Skipped   []clashsub.Skipped `json:"skipped,omitempty"`
}

// ClashPreviewNode is one node shown by a dry-run parse.
type ClashPreviewNode struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Excluded bool   `json:"excluded"`
}

// ClashPreviewResult is a dry-run parse of a subscription.
type ClashPreviewResult struct {
	Format    string             `json:"format"`
	NodeCount int                `json:"node_count"`
	Usable    int                `json:"usable"`
	Nodes     []ClashPreviewNode `json:"nodes"`
	Skipped   []clashsub.Skipped `json:"skipped,omitempty"`
	UserInfo  *ClashUserInfo     `json:"user_info,omitempty"`
}

const (
	clashMaxPreviewNodes  = 50
	clashMaxSkippedReport = 20
	clashProfileNameLimit = 100
	clashUserAgentLimit   = 200
	clashNotesLimit       = 2000
)

func (s *ClashService) ListProfiles(ctx context.Context) ([]ClashProfileSummary, error) {
	profiles, err := s.repo.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}
	stats, err := s.repo.ListProfileStats(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ClashProfileSummary, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, ClashProfileSummary{ClashProfile: profile, Stats: stats[profile.ID]})
	}
	s.attachProfileTraffic(ctx, out)
	return out, nil
}

func (s *ClashService) GetProfile(ctx context.Context, id int64) (*ClashProfileSummary, error) {
	profile, err := s.repo.GetProfile(ctx, id)
	if err != nil {
		return nil, err
	}
	stats, err := s.repo.ListProfileStats(ctx)
	if err != nil {
		return nil, err
	}
	summaries := []ClashProfileSummary{{ClashProfile: *profile, Stats: stats[id]}}
	s.attachProfileTraffic(ctx, summaries)
	return &summaries[0], nil
}

func (s *ClashService) requireEncryption() error {
	if s.encryptor == nil || s.cfg == nil || !s.cfg.Totp.EncryptionKeyConfigured {
		return infraerrors.BadRequest(ClashErrCodeEncryptionRequired,
			"a fixed encryption key (TOTP_ENCRYPTION_KEY) is required to store subscription URLs")
	}
	return nil
}

func (s *ClashService) validateProfile(ctx context.Context, p *ClashProfile, rawURL string) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || utf8.RuneCountInString(p.Name) > clashProfileNameLimit {
		return infraerrors.BadRequest(ClashErrCodeProfileInvalid, "name is required and must be at most 100 characters")
	}
	p.UserAgent = strings.TrimSpace(p.UserAgent)
	if len(p.UserAgent) > clashUserAgentLimit {
		return infraerrors.BadRequest(ClashErrCodeProfileInvalid, "user_agent is too long")
	}
	if p.RefreshIntervalMinutes != 0 && (p.RefreshIntervalMinutes < 10 || p.RefreshIntervalMinutes > 10080) {
		return infraerrors.BadRequest(ClashErrCodeProfileInvalid, "refresh_interval_minutes must be 0 (manual) or between 10 and 10080")
	}
	if utf8.RuneCountInString(p.Notes) > clashNotesLimit {
		return infraerrors.BadRequest(ClashErrCodeProfileInvalid, "notes is too long")
	}
	if _, err := compileClashPattern("include_pattern", p.IncludePattern); err != nil {
		return err
	}
	if _, err := compileClashPattern("exclude_pattern", p.ExcludePattern); err != nil {
		return err
	}
	if rawURL != "" {
		if err := s.fetchPolicy().validateURL(mustParseURL(rawURL)); err != nil {
			return err
		}
	}
	if p.FetchProxyID != nil {
		proxy, err := s.proxyRepo.GetByID(ctx, *p.FetchProxyID)
		if err != nil {
			return infraerrors.BadRequest(ClashErrCodeProfileInvalid, "fetch proxy not found")
		}
		if proxy.IsClashManaged() {
			return infraerrors.BadRequest(ClashErrCodeProfileInvalid, "fetch proxy must be a manual proxy")
		}
	}
	if exists, err := s.repo.ExistsProfileName(ctx, p.Name, p.ID); err != nil {
		return err
	} else if exists {
		return infraerrors.Conflict(ClashErrCodeProfileInvalid, "a subscription with this name already exists")
	}
	return nil
}

func (s *ClashService) CreateProfile(ctx context.Context, in ClashProfileInput) (*ClashProfileSummary, *ClashRefreshResult, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, nil, err
	}
	if err := s.requireEncryption(); err != nil {
		return nil, nil, err
	}
	rawURL := strings.TrimSpace(derefString(in.URL))
	if rawURL == "" {
		return nil, nil, infraerrors.BadRequest(ClashErrCodeProfileInvalid, "url is required")
	}
	settings := s.poolSettings(ctx)
	profile := &ClashProfile{
		Name:                   derefString(in.Name),
		UserAgent:              derefString(in.UserAgent),
		Enabled:                true,
		RefreshIntervalMinutes: 360,
		IncludePattern:         strings.TrimSpace(derefString(in.IncludePattern)),
		ExcludePattern:         DefaultClashExcludePattern,
		Notes:                  strings.TrimSpace(derefString(in.Notes)),
		FetchProxyID:           in.FetchProxyID,
	}
	if profile.UserAgent == "" {
		profile.UserAgent = settings.DefaultUserAgent
	}
	if in.Enabled != nil {
		profile.Enabled = *in.Enabled
	}
	if in.RefreshIntervalMinutes != nil {
		profile.RefreshIntervalMinutes = *in.RefreshIntervalMinutes
	}
	if in.ExcludePattern != nil {
		profile.ExcludePattern = strings.TrimSpace(*in.ExcludePattern)
	}
	if err := s.validateProfile(ctx, profile, rawURL); err != nil {
		return nil, nil, err
	}
	if err := s.setProfileURL(ctx, profile, rawURL); err != nil {
		return nil, nil, err
	}
	if err := s.repo.CreateProfile(ctx, profile); err != nil {
		return nil, nil, err
	}
	var refresh *ClashRefreshResult
	if profile.Enabled {
		refresh, _ = s.RefreshProfile(ctx, profile.ID, false)
	}
	summary, err := s.GetProfile(ctx, profile.ID)
	return summary, refresh, err
}

func (s *ClashService) setProfileURL(ctx context.Context, profile *ClashProfile, rawURL string) error {
	fingerprint := clashURLFingerprint(rawURL)
	if exists, err := s.repo.ExistsProfileURL(ctx, fingerprint, profile.ID); err != nil {
		return err
	} else if exists {
		return infraerrors.Conflict(ClashErrCodeProfileInvalid, "this subscription URL is already imported")
	}
	encrypted, err := s.encryptor.Encrypt(rawURL)
	if err != nil {
		return fmt.Errorf("encrypt subscription url: %w", err)
	}
	profile.URLEncrypted = encrypted
	profile.URLFingerprint = fingerprint
	profile.URLMasked = maskClashURL(rawURL)
	return nil
}

func (s *ClashService) UpdateProfile(ctx context.Context, id int64, in ClashProfileInput) (*ClashProfileSummary, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	profile, err := s.repo.GetProfile(ctx, id)
	if err != nil {
		return nil, err
	}
	wasEnabled := profile.Enabled
	if in.Name != nil {
		profile.Name = *in.Name
	}
	if in.UserAgent != nil {
		profile.UserAgent = *in.UserAgent
	}
	if in.Enabled != nil {
		profile.Enabled = *in.Enabled
	}
	if in.RefreshIntervalMinutes != nil {
		profile.RefreshIntervalMinutes = *in.RefreshIntervalMinutes
	}
	if in.IncludePattern != nil {
		profile.IncludePattern = strings.TrimSpace(*in.IncludePattern)
	}
	if in.ExcludePattern != nil {
		profile.ExcludePattern = strings.TrimSpace(*in.ExcludePattern)
	}
	if in.Notes != nil {
		profile.Notes = strings.TrimSpace(*in.Notes)
	}
	if in.ClearFetchProxy {
		profile.FetchProxyID = nil
	} else if in.FetchProxyID != nil {
		profile.FetchProxyID = in.FetchProxyID
	}
	if profile.UserAgent == "" {
		profile.UserAgent = s.poolSettings(ctx).DefaultUserAgent
	}
	rawURL := strings.TrimSpace(derefString(in.URL))
	if err := s.validateProfile(ctx, profile, rawURL); err != nil {
		return nil, err
	}
	if rawURL != "" {
		if err := s.requireEncryption(); err != nil {
			return nil, err
		}
		if err := s.setProfileURL(ctx, profile, rawURL); err != nil {
			return nil, err
		}
	}
	if err := s.repo.UpdateProfile(ctx, profile); err != nil {
		return nil, err
	}
	if wasEnabled != profile.Enabled {
		s.structuralChange(ctx)
		if err := s.ReconcilePauses(ctx); err != nil {
			s.log().Warn("reconcile clash pauses failed", zap.Error(err))
		}
	}
	return s.GetProfile(ctx, id)
}

// DeleteProfile removes a subscription. Nodes still backing accounts stay as
// REJECT placeholders (bound accounts are paused) until they are unbound.
func (s *ClashService) DeleteProfile(ctx context.Context, id int64, force bool) error {
	profile, err := s.repo.GetProfile(ctx, id)
	if err != nil {
		return err
	}
	nodes, err := s.repo.ListAllNodeViews(ctx)
	if err != nil {
		return err
	}
	var bound []string
	for i := range nodes {
		if nodes[i].ProfileID != profile.ID {
			continue
		}
		for _, acc := range nodes[i].Accounts {
			bound = append(bound, acc.Name)
		}
	}
	if len(bound) > 0 && !force {
		return infraerrors.Conflict(ClashErrCodeProfileInUse,
			fmt.Sprintf("%d account(s) still use exits of this subscription; rebind them first or delete with force", len(bound))).
			WithMetadata(map[string]string{"accounts": strings.Join(limitStrings(bound, 20), ", ")})
	}
	if err := s.repo.SoftDeleteProfile(ctx, id); err != nil {
		return err
	}
	if err := s.repo.MarkProfileNodes(ctx, id, ClashNodeStatusMissing, "subscription deleted"); err != nil {
		return err
	}
	s.structuralChange(ctx)
	return s.ReconcilePauses(ctx)
}

func limitStrings(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return append(values[:limit:limit], "…")
}

func (s *ClashService) fetchPolicy() clashFetchPolicy {
	sub := config.ClashPoolSubscriptionConfig{MaxBodyBytes: 10 << 20, FetchTimeoutSeconds: 30}
	if s.cfg != nil {
		sub = s.cfg.ClashPool.Subscription
	}
	return clashFetchPolicy{
		AllowPrivateHosts: sub.AllowPrivateHosts,
		Timeout:           time.Duration(sub.FetchTimeoutSeconds) * time.Second,
		MaxBodyBytes:      sub.MaxBodyBytes,
	}
}

func (s *ClashService) maxNodesPerProfile() int {
	if s.cfg == nil || s.cfg.ClashPool.Subscription.MaxNodesPerProfile <= 0 {
		return clashsub.DefaultMaxNodes
	}
	return s.cfg.ClashPool.Subscription.MaxNodesPerProfile
}

func (s *ClashService) fetchAndParse(ctx context.Context, rawURL, userAgent string, fetchProxyID *int64) (*clashsub.Result, *ClashUserInfo, error) {
	var via *Proxy
	if fetchProxyID != nil {
		proxy, err := s.proxyRepo.GetByID(ctx, *fetchProxyID)
		if err != nil {
			return nil, nil, fmt.Errorf("fetch proxy %d: %w", *fetchProxyID, err)
		}
		via = proxy
	}
	fetched, err := fetchClashSubscription(ctx, s.fetchPolicy(), rawURL, userAgent, via)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := clashsub.Parse(fetched.Body, clashsub.Options{MaxNodes: s.maxNodesPerProfile()})
	if err != nil {
		return nil, fetched.UserInfo, err
	}
	return parsed, fetched.UserInfo, nil
}

// PreviewProfile fetches and parses a subscription without saving it.
func (s *ClashService) PreviewProfile(ctx context.Context, in ClashProfileInput) (*ClashPreviewResult, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	rawURL := strings.TrimSpace(derefString(in.URL))
	if rawURL == "" {
		return nil, infraerrors.BadRequest(ClashErrCodeProfileInvalid, "url is required")
	}
	include, err := compileClashPattern("include_pattern", derefString(in.IncludePattern))
	if err != nil {
		return nil, err
	}
	excludeSource := DefaultClashExcludePattern
	if in.ExcludePattern != nil {
		excludeSource = *in.ExcludePattern
	}
	exclude, err := compileClashPattern("exclude_pattern", excludeSource)
	if err != nil {
		return nil, err
	}
	userAgent := strings.TrimSpace(derefString(in.UserAgent))
	if userAgent == "" {
		userAgent = s.poolSettings(ctx).DefaultUserAgent
	}
	parsed, info, err := s.fetchAndParse(ctx, rawURL, userAgent, in.FetchProxyID)
	if err != nil {
		return nil, infraerrors.BadRequest(ClashErrCodeFetchFailed, err.Error())
	}
	result := &ClashPreviewResult{Format: string(parsed.Format), NodeCount: len(parsed.Nodes), UserInfo: info}
	for _, node := range parsed.Nodes {
		excluded := (include != nil && !include.MatchString(node.Name)) || (exclude != nil && exclude.MatchString(node.Name))
		if !excluded {
			result.Usable++
		}
		if len(result.Nodes) < clashMaxPreviewNodes {
			result.Nodes = append(result.Nodes, ClashPreviewNode{Name: node.Name, Type: node.Type, Server: node.Server, Port: node.Port, Excluded: excluded})
		}
	}
	result.Skipped = limitSkipped(parsed.Skipped)
	return result, nil
}

func limitSkipped(skipped []clashsub.Skipped) []clashsub.Skipped {
	if len(skipped) > clashMaxSkippedReport {
		return skipped[:clashMaxSkippedReport]
	}
	return skipped
}

func (s *ClashService) profileLock(id int64) *sync.Mutex {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	lock, ok := s.refreshLocks[id]
	if !ok {
		lock = &sync.Mutex{}
		s.refreshLocks[id] = lock
	}
	return lock
}

// RefreshProfile fetches a subscription and synchronizes its nodes.
func (s *ClashService) RefreshProfile(ctx context.Context, id int64, force bool) (*ClashRefreshResult, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	lock := s.profileLock(id)
	lock.Lock()
	defer lock.Unlock()

	profile, err := s.repo.GetProfile(ctx, id)
	if err != nil {
		return nil, err
	}
	result := &ClashRefreshResult{ProfileID: id}
	record := ClashRefreshRecord{At: time.Now().UTC()}
	fail := func(status string, err error) (*ClashRefreshResult, error) {
		result.Status, result.Error = status, err.Error()
		record.Status, record.Error = status, err.Error()
		if recErr := s.repo.RecordRefresh(ctx, id, record); recErr != nil {
			s.log().Warn("record clash refresh failed", zap.Int64("profile_id", id), zap.Error(recErr))
		}
		return result, nil
	}

	rawURL, err := s.encryptor.Decrypt(profile.URLEncrypted)
	if err != nil {
		return fail(ClashRefreshError, errors.New("stored subscription URL cannot be decrypted; re-enter the URL (encryption key changed?)"))
	}
	include, err := compileClashPattern("include_pattern", profile.IncludePattern)
	if err != nil {
		return fail(ClashRefreshError, err)
	}
	exclude, err := compileClashPattern("exclude_pattern", profile.ExcludePattern)
	if err != nil {
		return fail(ClashRefreshError, err)
	}
	parsed, info, err := s.fetchAndParse(ctx, rawURL, profile.UserAgent, profile.FetchProxyID)
	record.UserInfo = info
	if err != nil {
		return fail(ClashRefreshError, err)
	}
	result.Format, record.Format = string(parsed.Format), string(parsed.Format)
	result.Skipped = limitSkipped(parsed.Skipped)

	existing, err := s.repo.ListNodesByProfile(ctx, id)
	if err != nil {
		return nil, err
	}
	settings := s.poolSettings(ctx)
	plan, stats, err := BuildClashNodeSyncPlan(existing, parsed.Nodes, ClashSyncOptions{
		ProfileName:           profile.Name,
		Include:               include,
		Exclude:               exclude,
		AllowPrivateNodes:     s.cfg != nil && s.cfg.ClashPool.Subscription.AllowPrivateNodes,
		DropProtectionPercent: settings.DropProtectionPercent,
		Force:                 force,
	})
	if stats != nil {
		result.Parsed, result.Filtered, result.Private = stats.Parsed, stats.Filtered, stats.Private
	}
	if err != nil {
		var drop *ClashDropProtectionError
		if errors.As(err, &drop) {
			return fail(ClashRefreshSkipped, err)
		}
		return fail(ClashRefreshError, err)
	}

	host := s.cfg.ClashPool.EffectiveListenerHost()
	synced, err := s.repo.ApplyNodeSync(ctx, id, plan, ClashPortRange{
		Start: s.cfg.ClashPool.PortRangeStart,
		End:   s.cfg.ClashPool.PortRangeEnd,
	}, func() ClashManagedProxySpec {
		return ClashManagedProxySpec{Host: host, Username: "sub2api-" + randomClashToken(6), Password: randomClashToken(16)}
	})
	if err != nil {
		return fail(ClashRefreshError, err)
	}
	result.Inserted, result.Updated, result.Missing = synced.Inserted, synced.Updated, synced.Missing
	result.Status, record.Status = ClashRefreshOK, ClashRefreshOK
	if err := s.repo.RecordRefresh(ctx, id, record); err != nil {
		s.log().Warn("record clash refresh failed", zap.Int64("profile_id", id), zap.Error(err))
	}
	if synced.Structural {
		s.structuralChange(ctx)
	}
	return result, nil
}

func randomClashToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(buf)
}

// ListNodes returns a page of nodes with their bindings and traffic.
func (s *ClashService) ListNodes(ctx context.Context, filter ClashNodeFilter, params pagination.PaginationParams) ([]ClashNodeView, *pagination.PaginationResult, error) {
	filter = s.prepareNodeFilter(filter)
	views, result, err := s.repo.ListNodeViews(ctx, filter, params)
	if err != nil {
		return nil, nil, err
	}
	s.attachNodeTraffic(ctx, views)
	return views, result, nil
}

// ListNodeIDs returns every node matching filter, in listing order, so the
// admin page can test all filtered nodes rather than one page.
func (s *ClashService) ListNodeIDs(ctx context.Context, filter ClashNodeFilter) ([]int64, error) {
	return s.repo.ListNodeIDs(ctx, s.prepareNodeFilter(filter))
}

func (s *ClashService) prepareNodeFilter(filter ClashNodeFilter) ClashNodeFilter {
	filter.Sort = NormalizeClashNodeSort(filter.Sort)
	if filter.Sort == ClashNodeSortTrafficToday {
		filter.TrafficDate = clashTrafficDate(time.Now())
	}
	return filter
}

// SetNodeEnabled toggles an admin disable. Disabling immediately drops the
// node's established connections; bound accounts are paused on reconcile.
func (s *ClashService) SetNodeEnabled(ctx context.Context, nodeID int64, enabled bool) error {
	view, err := s.repo.GetNodeView(ctx, nodeID)
	if err != nil {
		return err
	}
	switch {
	case enabled && view.Status == ClashNodeStatusDisabled:
		if err := s.repo.SetNodeStatus(ctx, nodeID, ClashNodeStatusActive, ""); err != nil {
			return err
		}
	case !enabled && view.Status != ClashNodeStatusDisabled:
		if err := s.repo.SetNodeStatus(ctx, nodeID, ClashNodeStatusDisabled, "disabled by admin"); err != nil {
			return err
		}
	default:
		return nil
	}
	s.structuralChange(ctx)
	if !enabled && s.runtime != nil {
		if _, err := s.runtime.CloseInboundConnections(ctx, []string{ClashListenerName(nodeID)}); err != nil {
			s.log().Warn("close clash node connections failed", zap.Int64("node_id", nodeID), zap.Error(err))
		}
	}
	return s.ReconcilePauses(ctx)
}

// AcceptNodeExit confirms an in-place egress change.
func (s *ClashService) AcceptNodeExit(ctx context.Context, nodeID int64) error {
	if err := s.repo.AcceptNodeExit(ctx, nodeID); err != nil {
		return err
	}
	return s.ReconcilePauses(ctx)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
