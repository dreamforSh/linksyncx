// Package clashsub decodes Clash/mihomo subscription payloads into sanitized
// proxy mappings. It accepts Clash YAML, base64-wrapped Clash YAML and
// V2Ray-style share links (plain or base64), the latter through mihomo's own
// converter so every scheme mihomo understands is supported.
package clashsub

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/metacubex/mihomo/common/convert"
	"gopkg.in/yaml.v3"
)

// Format identifies how a subscription payload was encoded.
type Format string

const (
	FormatClashYAML  Format = "clash_yaml"
	FormatBase64YAML Format = "base64_yaml"
	FormatURIList    Format = "uri_list"
)

// DefaultMaxNodes caps how many nodes one subscription may yield.
const DefaultMaxNodes = 2000

const maxNameRunes = 128

var (
	ErrEmpty         = errors.New("subscription is empty")
	ErrHTMLPage      = errors.New("subscription returned an HTML page (login, captcha or error page); check the URL and User-Agent")
	ErrNoProxies     = errors.New("subscription contains no usable proxies")
	ErrProvidersOnly = errors.New("subscription only references proxy-providers, which are not supported")
	ErrTooManyNodes  = errors.New("subscription exceeds the node limit")
)

// allowedTypes lists outbound types that tunnel to a remote server. direct,
// dns, reject and group-like types are excluded: a tampered subscription must
// never make an account egress from this server's own address.
var allowedTypes = map[string]struct{}{
	"ss": {}, "ssr": {}, "vmess": {}, "vless": {}, "trojan": {},
	"hysteria": {}, "hysteria2": {}, "tuic": {}, "anytls": {}, "snell": {},
	"mieru": {}, "wireguard": {}, "ssh": {}, "socks5": {}, "http": {},
}

// strippedKeys bind a node to local interfaces or routing tables.
var strippedKeys = []string{"interface-name", "routing-mark"}

// Node is one sanitized proxy. Config is the mihomo proxy mapping including
// name/type/server/port.
type Node struct {
	Name   string
	Type   string
	Server string
	Port   int
	Config map[string]any
}

// Skipped describes a proxy entry that was dropped during parsing.
type Skipped struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Result is the outcome of Parse.
type Result struct {
	Format  Format
	Nodes   []Node
	Skipped []Skipped
}

// Options tunes Parse.
type Options struct {
	// MaxNodes limits the number of accepted nodes; 0 means DefaultMaxNodes.
	MaxNodes int
}

// Parse decodes a subscription payload.
func Parse(body []byte, opts Options) (*Result, error) {
	data := bytes.TrimSpace(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
	if len(data) == 0 {
		return nil, ErrEmpty
	}
	if looksLikeHTML(data) {
		return nil, ErrHTMLPage
	}

	var (
		mappings []map[string]any
		format   Format
	)
	if found, ok, err := parseClashYAML(data); ok {
		if err != nil {
			return nil, err
		}
		mappings, format = found, FormatClashYAML
	} else if decoded, decErr := decodeBase64(data); decErr == nil {
		if found, ok, err := parseClashYAML(decoded); ok {
			if err != nil {
				return nil, err
			}
			mappings, format = found, FormatBase64YAML
		}
	}
	if format == "" {
		converted, err := convert.ConvertsV2Ray(data)
		if err != nil || len(converted) == 0 {
			return nil, ErrNoProxies
		}
		mappings, format = converted, FormatURIList
	}

	maxNodes := opts.MaxNodes
	if maxNodes <= 0 {
		maxNodes = DefaultMaxNodes
	}
	result := &Result{Format: format}
	for _, mapping := range mappings {
		node, reason := sanitize(mapping)
		if reason != "" {
			result.Skipped = append(result.Skipped, Skipped{Name: truncateRunes(asString(mapping["name"]), maxNameRunes), Reason: reason})
			continue
		}
		result.Nodes = append(result.Nodes, node)
	}
	if len(result.Nodes) == 0 {
		return nil, ErrNoProxies
	}
	if len(result.Nodes) > maxNodes {
		return nil, fmt.Errorf("%w (%d > %d)", ErrTooManyNodes, len(result.Nodes), maxNodes)
	}
	return result, nil
}

func looksLikeHTML(data []byte) bool {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	lower := strings.ToLower(string(head))
	return strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") ||
		strings.Contains(lower, "<head>") || strings.Contains(lower, "<body")
}

// parseClashYAML reports ok=true when data is a Clash document: a mapping with
// proxies (or only proxy-providers), or a bare list of proxy mappings.
func parseClashYAML(data []byte) ([]map[string]any, bool, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, nil
	}
	var list []any
	switch typed := normalize(doc).(type) {
	case map[string]any:
		raw, hasProxies := typed["proxies"]
		if !hasProxies || raw == nil {
			if _, hasProviders := typed["proxy-providers"]; hasProviders {
				return nil, true, ErrProvidersOnly
			}
			return nil, false, nil
		}
		items, ok := raw.([]any)
		if !ok {
			return nil, true, ErrNoProxies
		}
		list = items
	case []any:
		if len(typed) == 0 {
			return nil, false, nil
		}
		if first, ok := typed[0].(map[string]any); !ok || first["type"] == nil {
			return nil, false, nil
		}
		list = typed
	default:
		return nil, false, nil
	}
	mappings := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if mapping, ok := item.(map[string]any); ok {
			mappings = append(mappings, mapping)
		}
	}
	return mappings, true, nil
}

func decodeBase64(data []byte) ([]byte, error) {
	compact := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, string(data))
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := enc.DecodeString(compact); err == nil && utf8.Valid(decoded) {
			return bytes.TrimSpace(decoded), nil
		}
	}
	return nil, errors.New("not base64")
}

// normalize converts YAML maps with non-string keys into map[string]any so the
// result round-trips through JSON.
func normalize(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			typed[key] = normalize(item)
		}
		return typed
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[fmt.Sprint(key)] = normalize(item)
		}
		return out
	case []any:
		for i, item := range typed {
			typed[i] = normalize(item)
		}
		return typed
	default:
		return value
	}
}

func sanitize(mapping map[string]any) (Node, string) {
	mapping, _ = normalize(mapping).(map[string]any)
	typ := strings.ToLower(strings.TrimSpace(asString(mapping["type"])))
	if _, ok := allowedTypes[typ]; !ok {
		return Node{}, fmt.Sprintf("unsupported type %q", typ)
	}
	server := strings.TrimSpace(asString(mapping["server"]))
	if server == "" {
		return Node{}, "missing server"
	}
	port, portOK := asInt(mapping["port"])
	if !portOK || port < 0 || port > 65535 || (port == 0 && asString(mapping["ports"]) == "") {
		return Node{}, "invalid port"
	}
	if _, chained := mapping["dialer-proxy"]; chained {
		return Node{}, "dialer-proxy chains are not supported"
	}
	for _, key := range strippedKeys {
		delete(mapping, key)
	}
	name := truncateRunes(strings.TrimSpace(asString(mapping["name"])), maxNameRunes)
	if name == "" {
		name = fmt.Sprintf("%s-%s:%d", typ, server, port)
	}
	mapping["name"] = name
	mapping["type"] = typ
	mapping["server"] = server
	return Node{Name: name, Type: typ, Server: server, Port: port, Config: mapping}, ""
}

// ConfigHash fingerprints a node configuration independent of its display
// name. encoding/json sorts map keys, so the digest is order independent.
func ConfigHash(config map[string]any) string {
	clone := make(map[string]any, len(config))
	for key, value := range config {
		if key == "name" {
			continue
		}
		clone[key] = value
	}
	raw, err := json.Marshal(clone)
	if err != nil {
		raw = []byte(fmt.Sprint(clone))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ServerScope classifies a node server address.
type ServerScope string

const (
	ScopePublic    ServerScope = "public"
	ScopePrivate   ServerScope = "private"
	ScopeLoopback  ServerScope = "loopback"
	ScopeLinkLocal ServerScope = "link_local"
)

// ClassifyServer inspects literal IPs (and localhost). Hostnames are reported
// as public because they are resolved by the core at dial time.
func ClassifyServer(server string) ServerScope {
	host := strings.Trim(strings.TrimSpace(server), "[]")
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return ScopeLoopback
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return ScopePublic
	}
	addr = addr.Unmap()
	switch {
	case addr.IsLoopback() || addr.IsUnspecified():
		return ScopeLoopback
	case addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast():
		return ScopeLinkLocal
	case addr.IsPrivate() || isCGNAT(addr):
		return ScopePrivate
	default:
		return ScopePublic
	}
}

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

func isCGNAT(addr netip.Addr) bool { return addr.Is4() && cgnatPrefix.Contains(addr) }

// UserInfo is the parsed subscription-userinfo response header.
type UserInfo struct {
	Upload   int64
	Download int64
	Total    int64
	Expire   *time.Time
}

// ParseUserInfo parses "upload=1; download=2; total=3; expire=1700000000".
func ParseUserInfo(header string) (UserInfo, bool) {
	var info UserInfo
	found := false
	for _, part := range strings.Split(header, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		number, ok := parseNumber(value)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "upload":
			info.Upload, found = number, true
		case "download":
			info.Download, found = number, true
		case "total":
			info.Total, found = number, true
		case "expire":
			found = true
			if number > 0 {
				expire := time.Unix(number, 0).UTC()
				info.Expire = &expire
			}
		}
	}
	return info, found
}

func parseNumber(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return n, true
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return int64(f), true
	}
	return 0, false
}

func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func asInt(value any) (int, bool) {
	switch typed := value.(type) {
	case nil:
		return 0, true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float64:
		return int(typed), typed == float64(int(typed))
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, true
		}
		n, err := strconv.Atoi(trimmed)
		return n, err == nil
	default:
		return 0, false
	}
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
