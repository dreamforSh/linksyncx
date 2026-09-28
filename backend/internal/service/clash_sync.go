package service

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clashsub"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// ClashDropProtectionError reports a refresh skipped because the subscription
// suddenly returned far fewer nodes (expired plan, panel outage).
type ClashDropProtectionError struct {
	Before int
	After  int
}

func (e *ClashDropProtectionError) Error() string {
	return fmt.Sprintf("subscription node count dropped from %d to %d; refresh skipped (use force to apply)", e.Before, e.After)
}

// ClashSyncOptions tunes BuildClashNodeSyncPlan.
type ClashSyncOptions struct {
	ProfileName       string
	Include           *regexp.Regexp
	Exclude           *regexp.Regexp
	AllowPrivateNodes bool
	// DropProtectionPercent aborts when the usable node count shrinks by more
	// than this percentage; 0 disables the guard. Force bypasses it.
	DropProtectionPercent int
	Force                 bool
}

// ClashSyncStats summarizes a planned refresh.
type ClashSyncStats struct {
	Parsed   int
	Filtered int
	Private  int
}

const clashDropProtectionMinNodes = 3

// BuildClashNodeSyncPlan matches freshly parsed nodes against stored ones.
// Matching prefers an identical configuration, then the same name, then the
// same (type, server, port); unmatched stored nodes become missing. Matched
// nodes keep their listener port and managed proxy, so account bindings
// survive renames and credential rotations.
func BuildClashNodeSyncPlan(existing []ClashNode, parsed []clashsub.Node, opts ClashSyncOptions) (*ClashNodeSyncPlan, *ClashSyncStats, error) {
	stats := &ClashSyncStats{Parsed: len(parsed)}
	candidates := make([]clashsub.Node, 0, len(parsed))
	for _, node := range parsed {
		if opts.Include != nil && !opts.Include.MatchString(node.Name) {
			stats.Filtered++
			continue
		}
		if opts.Exclude != nil && opts.Exclude.MatchString(node.Name) {
			stats.Filtered++
			continue
		}
		candidates = append(candidates, node)
	}

	activeBefore := 0
	for i := range existing {
		if existing[i].Status == ClashNodeStatusActive {
			activeBefore++
		}
	}
	if !opts.Force && activeBefore > 0 {
		after := len(candidates)
		if after == 0 {
			return nil, stats, &ClashDropProtectionError{Before: activeBefore, After: after}
		}
		if opts.DropProtectionPercent > 0 && activeBefore >= clashDropProtectionMinNodes &&
			after*100 < activeBefore*(100-opts.DropProtectionPercent) {
			return nil, stats, &ClashDropProtectionError{Before: activeBefore, After: after}
		}
	}

	byHash := make(map[string][]int, len(existing))
	byName := make(map[string][]int, len(existing))
	byEndpoint := make(map[string][]int, len(existing))
	for i := range existing {
		node := &existing[i]
		byHash[node.ConfigHash] = append(byHash[node.ConfigHash], i)
		byName[node.Name] = append(byName[node.Name], i)
		byEndpoint[clashEndpointKey(node.Type, node.Server, node.ServerPort)] = append(byEndpoint[clashEndpointKey(node.Type, node.Server, node.ServerPort)], i)
	}
	matched := make([]bool, len(existing))
	take := func(indexes []int) (int, bool) {
		for _, idx := range indexes {
			if !matched[idx] {
				matched[idx] = true
				return idx, true
			}
		}
		return 0, false
	}

	plan := &ClashNodeSyncPlan{}
	for _, node := range candidates {
		hash := clashsub.ConfigHash(node.Config)
		status, reason := clashNodeScreen(node, opts.AllowPrivateNodes)
		if status == ClashNodeStatusInvalid {
			stats.Private++
		}
		proxyName := clashManagedProxyName(opts.ProfileName, node.Name)

		idx, found := take(byHash[hash])
		configChanged := false
		if !found {
			if idx, found = take(byName[node.Name]); !found {
				idx, found = take(byEndpoint[clashEndpointKey(node.Type, node.Server, node.Port)])
			}
			configChanged = found
		}
		if !found {
			plan.Inserts = append(plan.Inserts, ClashSyncNewNode{
				Name:         node.Name,
				Type:         node.Type,
				Server:       node.Server,
				ServerPort:   node.Port,
				Config:       node.Config,
				ConfigHash:   hash,
				Status:       status,
				StatusReason: reason,
				ProxyName:    proxyName,
			})
			continue
		}

		prev := &existing[idx]
		nextStatus, nextReason := status, reason
		switch {
		case status == ClashNodeStatusInvalid:
			// Screening failures always win (a hidden node stays hidden, and
			// invalid keeps it offline just the same).
		case prev.Hidden:
			// Hidden nodes stay offline whatever the subscription says, even
			// when a previously invalid configuration changes.
			nextStatus, nextReason = ClashNodeStatusDisabled, ClashNodeReasonHiddenByAdmin
			if prev.Status == ClashNodeStatusDisabled && prev.StatusReason != "" {
				nextReason = prev.StatusReason
			}
		case prev.Status == ClashNodeStatusDisabled:
			nextStatus, nextReason = ClashNodeStatusDisabled, prev.StatusReason
		case prev.Status == ClashNodeStatusInvalid && !configChanged:
			// The core rejected this exact configuration before; keep it out
			// until the subscription changes it.
			nextStatus, nextReason = ClashNodeStatusInvalid, prev.StatusReason
		}
		plan.Updates = append(plan.Updates, ClashSyncUpdate{
			NodeID:        prev.ID,
			Name:          node.Name,
			Type:          node.Type,
			Server:        node.Server,
			ServerPort:    node.Port,
			Config:        node.Config,
			ConfigHash:    hash,
			ConfigChanged: configChanged,
			Status:        nextStatus,
			StatusReason:  nextReason,
			ProxyName:     proxyName,
		})
	}
	for i := range existing {
		if matched[i] {
			continue
		}
		if existing[i].Status == ClashNodeStatusMissing || existing[i].Status == ClashNodeStatusDisabled {
			continue
		}
		plan.Missing = append(plan.Missing, existing[i].ID)
	}
	return plan, stats, nil
}

func clashEndpointKey(typ, server string, port int) string {
	return strings.ToLower(typ) + "|" + strings.ToLower(strings.TrimSpace(server)) + "|" + fmt.Sprint(port)
}

// clashNodeScreen rejects nodes pointing at this host or its private network:
// the core would dial them from inside the deployment.
func clashNodeScreen(node clashsub.Node, allowPrivate bool) (string, string) {
	switch clashsub.ClassifyServer(node.Server) {
	case clashsub.ScopeLoopback, clashsub.ScopeLinkLocal:
		return ClashNodeStatusInvalid, "server is a loopback or link-local address"
	case clashsub.ScopePrivate:
		if !allowPrivate {
			return ClashNodeStatusInvalid, "server is a private network address"
		}
	}
	return ClashNodeStatusActive, ""
}

const clashManagedProxyNameLimit = 100

func clashManagedProxyName(profileName, nodeName string) string {
	name := "Clash·" + strings.TrimSpace(profileName) + "·" + strings.TrimSpace(nodeName)
	if utf8.RuneCountInString(name) <= clashManagedProxyNameLimit {
		return name
	}
	return string([]rune(name)[:clashManagedProxyNameLimit])
}

// compileClashPattern validates an include/exclude expression.
func compileClashPattern(field, pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, nil
	}
	if len(pattern) > 1000 {
		return nil, infraerrors.BadRequest(ClashErrCodeProfileInvalid, field+" is too long")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, infraerrors.BadRequest(ClashErrCodeProfileInvalid, fmt.Sprintf("%s is not a valid regular expression: %v", field, err))
	}
	return re, nil
}

// DefaultClashExcludePattern filters informational pseudo-nodes that panels
// insert to show remaining traffic, expiry or their website.
const DefaultClashExcludePattern = `(?i)(剩余|到期|过期|流量|官网|套餐|重置|expire|traffic|website|remaining)`
