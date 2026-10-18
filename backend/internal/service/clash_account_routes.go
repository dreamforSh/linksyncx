package service

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// Per-account listener credentials.
//
// A Clash exit may serve several accounts. Every node listener accepts its
// shared credentials plus one username per bound account, so the core tags each
// connection with the account that opened it (metadata.inboundUser) and the
// traffic sampler can split node traffic by account. The password stays the
// node's: the listener only binds the local/internal pool address, and the
// username is an attribution tag, not a separate secret.
//
// An account only switches to its own username after this instance has
// confirmed the running core configuration contains it (clashAccountRoutes).
// Until then — and for any caller without an account — the shared credentials
// keep working, so a binding change never makes a request fail with 407.

const clashAccountUserSeparator = "~a"

// ClashAccountProxyUsername is the listener username that tags an account.
func ClashAccountProxyUsername(base string, accountID int64) string {
	return base + clashAccountUserSeparator + strconv.FormatInt(accountID, 10)
}

// ParseClashAccountProxyUsername returns the account tagged by a listener
// username, or false for the shared node credentials.
func ParseClashAccountProxyUsername(user string) (int64, bool) {
	idx := strings.LastIndex(user, clashAccountUserSeparator)
	if idx < 0 {
		return 0, false
	}
	raw := user[idx+len(clashAccountUserSeparator):]
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, false
	}
	return id, true
}

// clashAccountRouteTable records, per Clash proxy, the accounts whose tagged
// username the running core configuration accepts.
type clashAccountRouteTable struct {
	mu     sync.RWMutex
	routes map[int64]map[int64]struct{}
}

var clashAccountRoutes = &clashAccountRouteTable{}

func (t *clashAccountRouteTable) active(proxyID, accountID int64) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.routes[proxyID][accountID]
	return ok
}

// set replaces the table once a configuration is confirmed running.
func (t *clashAccountRouteTable) set(routes map[int64]map[int64]struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.routes = routes
}

// retain drops routes missing from next before applying it: a username about
// to disappear from the core must stop being used first, while new usernames
// wait for set after the apply succeeded.
func (t *clashAccountRouteTable) retain(next map[int64]map[int64]struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	kept := make(map[int64]map[int64]struct{}, len(t.routes))
	for proxyID, accounts := range t.routes {
		allowed := next[proxyID]
		for accountID := range accounts {
			if _, ok := allowed[accountID]; !ok {
				continue
			}
			if kept[proxyID] == nil {
				kept[proxyID] = make(map[int64]struct{})
			}
			kept[proxyID][accountID] = struct{}{}
		}
	}
	t.routes = kept
}

// clashRenderAccountRoutes collects the tagged usernames a render contains.
func clashRenderAccountRoutes(nodes []ClashRenderNode) map[int64]map[int64]struct{} {
	out := make(map[int64]map[int64]struct{})
	for _, node := range nodes {
		if node.ProxyID <= 0 || node.Username == "" || len(node.AccountIDs) == 0 {
			continue
		}
		accounts := make(map[int64]struct{}, len(node.AccountIDs))
		for _, id := range node.AccountIDs {
			accounts[id] = struct{}{}
		}
		out[node.ProxyID] = accounts
	}
	return out
}

// clashAccountProxyURL returns the account-tagged URL of a Clash exit, or ""
// when the account must keep using the shared credentials.
func clashAccountProxyURL(p *Proxy, accountID int64) string {
	if !p.IsClashManaged() || accountID <= 0 || p.Username == "" || p.Password == "" {
		return ""
	}
	if !clashAccountRoutes.active(p.ID, accountID) {
		return ""
	}
	u := &url.URL{
		Scheme: p.Protocol,
		Host:   net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
		User:   url.UserPassword(ClashAccountProxyUsername(p.Username, accountID), p.Password),
	}
	return u.String()
}
