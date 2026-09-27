package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// Clash node lifecycle states.
const (
	ClashNodeStatusActive   = "active"
	ClashNodeStatusMissing  = "missing"
	ClashNodeStatusDisabled = "disabled"
	ClashNodeStatusInvalid  = "invalid"
)

// Clash node health states (latency probing through the core).
const (
	ClashHealthUnknown   = "unknown"
	ClashHealthHealthy   = "healthy"
	ClashHealthUnhealthy = "unhealthy"
)

// Clash exit states (egress IP probing through the node listener).
const (
	ClashExitUnknown = "unknown"
	ClashExitOK      = "ok"
	ClashExitStale   = "stale"
	// ClashExitChanged means a new egress IP was observed and awaits admin acceptance.
	ClashExitChanged = "changed"
)

// Clash subscription refresh outcomes.
const (
	ClashRefreshNever   = "never"
	ClashRefreshOK      = "ok"
	ClashRefreshError   = "error"
	ClashRefreshSkipped = "skipped"
)

// ClashPauseReasonPrefix marks temp-unschedulable reasons owned by the Clash
// pool. Only reasons carrying this prefix are cleared automatically.
const ClashPauseReasonPrefix = "[clash-exit]"

// Clash error codes surfaced to the admin API.
const (
	ClashErrCodeExitOccupied       = "CLASH_EXIT_OCCUPIED"
	ClashErrCodeExitUnavailable    = "CLASH_EXIT_UNAVAILABLE"
	ClashErrCodeExitUnprobed       = "CLASH_EXIT_UNPROBED"
	ClashErrCodeRelayUnsupported   = "CLASH_EXIT_RELAY_UNSUPPORTED"
	ClashErrCodeBulkUnsupported    = "CLASH_EXIT_BULK_UNSUPPORTED"
	ClashErrCodeManagedReadonly    = "CLASH_MANAGED_PROXY_READONLY"
	ClashErrCodePortReserved       = "CLASH_PROXY_PORT_RESERVED"
	ClashErrCodeEncryptionRequired = "CLASH_ENCRYPTION_KEY_REQUIRED"
	ClashErrCodeProfileInUse       = "CLASH_PROFILE_IN_USE"
	ClashErrCodeProfileInvalid     = "CLASH_PROFILE_INVALID"
	ClashErrCodeProfileNotFound    = "CLASH_PROFILE_NOT_FOUND"
	ClashErrCodeNodeNotFound       = "CLASH_NODE_NOT_FOUND"
	ClashErrCodeFetchFailed        = "CLASH_FETCH_FAILED"
	ClashErrCodeRuntimeUnavailable = "CLASH_RUNTIME_UNAVAILABLE"
	ClashErrCodeDisabled           = "CLASH_POOL_DISABLED"
)

var (
	ErrClashProfileNotFound = infraerrors.NotFound(ClashErrCodeProfileNotFound, "clash profile not found")
	ErrClashNodeNotFound    = infraerrors.NotFound(ClashErrCodeNodeNotFound, "clash node not found")
	ErrClashManagedReadonly = infraerrors.Conflict(ClashErrCodeManagedReadonly,
		"this proxy is managed by a Clash subscription; change it from the Clash page")
	ErrClashPoolDisabled = infraerrors.Conflict(ClashErrCodeDisabled, "clash proxy pool is disabled")
)

// ClashProfile is one Clash subscription.
type ClashProfile struct {
	ID                     int64
	Name                   string
	URLEncrypted           string
	URLFingerprint         string
	URLMasked              string
	UserAgent              string
	Enabled                bool
	RefreshIntervalMinutes int
	IncludePattern         string
	ExcludePattern         string
	FetchProxyID           *int64
	Notes                  string
	LastRefreshAt          *time.Time
	LastRefreshStatus      string
	LastRefreshError       string
	LastFormat             string
	UploadBytes            int64
	DownloadBytes          int64
	TotalBytes             int64
	ExpireAt               *time.Time
	NodeCount              int
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// ClashProfileStats aggregates node states per profile.
type ClashProfileStats struct {
	Total     int
	Active    int
	Healthy   int
	Unhealthy int
	Missing   int
	Invalid   int
	Disabled  int
	// Bound counts nodes backing at least one account.
	Bound int
	// BoundAccounts counts distinct non-shadow accounts using the profile's nodes.
	BoundAccounts int
}

// ClashPlatformChecks stores AI platform reachability observed through an exit.
type ClashPlatformChecks struct {
	CheckedAt *time.Time        `json:"checked_at,omitempty"`
	Results   map[string]string `json:"results,omitempty"`
}

// ClashNode is one proxy node of a profile, materialized as a managed proxy.
type ClashNode struct {
	ID                   int64
	ProfileID            int64
	Name                 string
	Type                 string
	Server               string
	ServerPort           int
	Config               map[string]any
	ConfigHash           string
	Status               string
	StatusReason         string
	MissingSince         *time.Time
	ListenPort           int
	ProxyID              int64
	HealthStatus         string
	LatencyMs            *int
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	LastCheckedAt        *time.Time
	LastCheckError       string
	ExitIP               string
	ExitCountry          string
	ExitCountryCode      string
	ExitRegion           string
	ExitCity             string
	ExitStatus           string
	ExitCheckedAt        *time.Time
	ExitPendingIP        string
	ExitChangedAt        *time.Time
	PlatformChecks       ClashPlatformChecks
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// ClashBoundAccount is an account bound to a node's managed proxy.
type ClashBoundAccount struct {
	ID       int64
	Name     string
	Platform string
	// ParentAccountID is set for credential shadows, which inherit their
	// parent's proxy and never count as separate exit occupants.
	ParentAccountID *int64
}

// ClashNodeView joins a node with its profile and bound accounts.
type ClashNodeView struct {
	ClashNode
	ProfileName    string
	ProfileEnabled bool
	ProfileDeleted bool
	Accounts       []ClashBoundAccount
	// Traffic is only filled by node listings (ClashService.ListNodes).
	Traffic ClashNodeTraffic
}

// Available reports whether traffic may be routed through the node.
func (v *ClashNodeView) Available() bool {
	return v.unavailableReason() == ""
}

// UnavailableReason explains why traffic may not use the node ("" when available).
func (v *ClashNodeView) UnavailableReason() string { return v.unavailableReason() }

func (v *ClashNodeView) unavailableReason() string {
	switch {
	case v.ProfileDeleted:
		return "subscription deleted"
	case !v.ProfileEnabled:
		return "subscription disabled"
	case v.Status == ClashNodeStatusMissing:
		return "node removed from subscription"
	case v.Status == ClashNodeStatusDisabled:
		return "node disabled"
	case v.Status == ClashNodeStatusInvalid:
		reason := strings.TrimSpace(v.StatusReason)
		if reason == "" {
			reason = "invalid configuration"
		}
		return "node invalid: " + reason
	case v.HealthStatus == ClashHealthUnhealthy:
		return "health check failing"
	case v.ExitStatus == ClashExitChanged:
		return "exit IP changed to " + v.ExitPendingIP + ", awaiting confirmation"
	default:
		return ""
	}
}

// ExitKey identifies the egress shared by nodes. Nodes with a known exit IP
// share a key with every node landing on the same address.
func (n *ClashNode) ExitKey() string {
	if ip := strings.TrimSpace(n.ExitIP); ip != "" {
		return "ip:" + ip
	}
	return "node:" + strconv.FormatInt(n.ID, 10)
}

// ClashNodeFilter narrows node listings.
type ClashNodeFilter struct {
	ProfileID *int64
	Status    string
	Health    string
	Bound     *bool
	Search    string
	// LiveOnly keeps nodes that can carry traffic (and be probed): active
	// nodes of enabled, undeleted profiles.
	LiveOnly bool
	// Sort is one of the ClashNodeSort* orders ("" = profile, then id).
	Sort string
	// TrafficDate is the bucket day for ClashNodeSortTrafficToday.
	TrafficDate string
}

// ClashRefreshRecord is persisted after every refresh attempt.
type ClashRefreshRecord struct {
	At        time.Time
	Status    string
	Error     string
	Format    string
	UserInfo  *ClashUserInfo
	NodeCount *int
}

// ClashUserInfo mirrors the subscription-userinfo header.
type ClashUserInfo struct {
	Upload   int64      `json:"upload"`
	Download int64      `json:"download"`
	Total    int64      `json:"total"`
	Expire   *time.Time `json:"expire"`
}

// ClashNodeHealthUpdate is one latency probe outcome.
type ClashNodeHealthUpdate struct {
	NodeID               int64
	HealthStatus         string
	LatencyMs            *int
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	Error                string
	CheckedAt            time.Time
}

// ClashNodeExitUpdate is one egress probe outcome.
type ClashNodeExitUpdate struct {
	NodeID         int64
	ExitIP         string
	Country        string
	CountryCode    string
	Region         string
	City           string
	ExitStatus     string
	PendingIP      string
	ChangedAt      *time.Time
	CheckedAt      time.Time
	PlatformChecks *ClashPlatformChecks
}

// ClashRenderNode is the per-node input of the core configuration.
type ClashRenderNode struct {
	NodeID     int64
	ListenPort int
	Username   string
	Password   string
	// Config is nil for placeholders: nodes that must not carry traffic but
	// still back an account binding render as REJECT listeners.
	Config map[string]any
}

// ClashPortRange bounds listener port allocation.
type ClashPortRange struct {
	Start int
	End   int
}

// ClashSyncNewNode is a node to insert during a refresh.
type ClashSyncNewNode struct {
	Name       string
	Type       string
	Server     string
	ServerPort int
	Config     map[string]any
	ConfigHash string
	// Status is active unless the node was rejected (e.g. private server).
	Status       string
	StatusReason string
	ProxyName    string
}

// ClashSyncUpdate rewrites an existing node matched during a refresh.
type ClashSyncUpdate struct {
	NodeID        int64
	Name          string
	Type          string
	Server        string
	ServerPort    int
	Config        map[string]any
	ConfigHash    string
	ConfigChanged bool
	Status        string
	StatusReason  string
	ProxyName     string
}

// ClashNodeSyncPlan is the diff between stored nodes and a fresh subscription.
type ClashNodeSyncPlan struct {
	Inserts []ClashSyncNewNode
	Updates []ClashSyncUpdate
	// Missing lists node IDs no longer present in the subscription.
	Missing []int64
}

// ClashNodeSyncResult reports what ApplyNodeSync changed.
type ClashNodeSyncResult struct {
	Inserted int
	Updated  int
	Missing  int
	// Structural is true when the core configuration must be re-rendered.
	Structural bool
}

// ClashManagedProxySpec describes a managed proxy row to create.
type ClashManagedProxySpec struct {
	Host     string
	Username string
	Password string
}

// ClashRepository persists subscriptions, nodes and Clash pauses.
type ClashRepository interface {
	CreateProfile(ctx context.Context, profile *ClashProfile) error
	UpdateProfile(ctx context.Context, profile *ClashProfile) error
	GetProfile(ctx context.Context, id int64) (*ClashProfile, error)
	ListProfiles(ctx context.Context) ([]ClashProfile, error)
	ListProfileStats(ctx context.Context) (map[int64]ClashProfileStats, error)
	SoftDeleteProfile(ctx context.Context, id int64) error
	RecordRefresh(ctx context.Context, profileID int64, record ClashRefreshRecord) error
	ExistsProfileName(ctx context.Context, name string, excludeID int64) (bool, error)
	ExistsProfileURL(ctx context.Context, fingerprint string, excludeID int64) (bool, error)

	ListNodesByProfile(ctx context.Context, profileID int64) ([]ClashNode, error)
	ListNodeViews(ctx context.Context, filter ClashNodeFilter, params pagination.PaginationParams) ([]ClashNodeView, *pagination.PaginationResult, error)
	ListNodeIDs(ctx context.Context, filter ClashNodeFilter) ([]int64, error)
	ListAllNodeViews(ctx context.Context) ([]ClashNodeView, error)
	GetNodeView(ctx context.Context, id int64) (*ClashNodeView, error)
	GetNodeViewByProxyID(ctx context.Context, proxyID int64) (*ClashNodeView, error)

	// ApplyNodeSync inserts, updates and marks nodes missing in one
	// transaction, allocating listener ports and managed proxies for inserts.
	ApplyNodeSync(ctx context.Context, profileID int64, plan *ClashNodeSyncPlan, ports ClashPortRange, proxy ClashManagedProxySpecFactory) (*ClashNodeSyncResult, error)
	SetNodeStatus(ctx context.Context, nodeID int64, status, reason string) error
	MarkProfileNodes(ctx context.Context, profileID int64, status, reason string) error
	UpdateNodeHealth(ctx context.Context, updates []ClashNodeHealthUpdate) error
	UpdateNodeExit(ctx context.Context, update ClashNodeExitUpdate) error
	AcceptNodeExit(ctx context.Context, nodeID int64) error
	ReclaimNodes(ctx context.Context, missingBefore time.Time) ([]int64, error)
	ListRenderNodes(ctx context.Context) ([]ClashRenderNode, error)
	RehostManagedProxies(ctx context.Context, host string) ([]int64, error)

	// PauseAccounts sets a Clash pause on the given accounts unless a longer
	// pause (from any source) is already in effect past renewBefore. It
	// returns the accounts whose row changed.
	PauseAccounts(ctx context.Context, reasons map[int64]string, until, renewBefore time.Time) ([]int64, error)
	// ClearPauses clears Clash-owned pauses only (reason prefix match).
	ClearPauses(ctx context.Context, accountIDs []int64) ([]int64, error)
	ListPausedAccounts(ctx context.Context) (map[int64]string, error)

	// AddNodeTraffic adds sampled bytes to daily buckets in one statement;
	// increments of nodes deleted meanwhile are dropped.
	AddNodeTraffic(ctx context.Context, increments []ClashTrafficIncrement, at time.Time) error
	// ListNodeTraffic returns all-time totals plus the buckets from since
	// (YYYY-MM-DD) on; nodes without traffic are absent.
	ListNodeTraffic(ctx context.Context, nodeIDs []int64, since string) (map[int64]*ClashNodeTrafficTotals, error)
	ListProfileTraffic(ctx context.Context, today string) (map[int64]ClashProfileTraffic, error)
}

// ClashManagedProxySpecFactory creates listener credentials for new nodes.
type ClashManagedProxySpecFactory func() ClashManagedProxySpec

// ClashRuntimeNotifier fans configuration changes out to every instance and
// keeps per-instance runtime status for the admin page.
type ClashRuntimeNotifier interface {
	NotifyConfigChanged(ctx context.Context) error
	SubscribeConfigChanged(ctx context.Context, handler func()) error
	PublishInstanceStatus(ctx context.Context, status ClashInstanceStatus, ttl time.Duration) error
	ListInstanceStatuses(ctx context.Context) ([]ClashInstanceStatus, error)
	PublishTrafficLive(ctx context.Context, live ClashTrafficLive, ttl time.Duration) error
	ListTrafficLive(ctx context.Context) ([]ClashTrafficLive, error)
}

// ClashInstanceStatus is one instance's view of its local core.
type ClashInstanceStatus struct {
	InstanceID    string    `json:"instance_id"`
	Mode          string    `json:"mode"`
	Ready         bool      `json:"ready"`
	Version       string    `json:"version"`
	ConfigHash    string    `json:"config_hash"`
	Listeners     int       `json:"listeners"`
	ListenerFails int       `json:"listener_failures"`
	LastAppliedAt time.Time `json:"last_applied_at"`
	LastError     string    `json:"last_error"`
	UpdatedAt     time.Time `json:"updated_at"`
}
