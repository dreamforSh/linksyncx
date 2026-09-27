package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	clashProxyNamePrefix    = "n-"
	clashListenerNamePrefix = "l-"
	clashMarkerPrefix       = "__sub2api_cfg_"
	clashRejectProxy        = "REJECT"
)

// ClashProxyName is the core-side name of a node's outbound.
func ClashProxyName(nodeID int64) string { return clashProxyNamePrefix + strconv.FormatInt(nodeID, 10) }

// ClashListenerName is the core-side name of a node's inbound listener.
func ClashListenerName(nodeID int64) string {
	return clashListenerNamePrefix + strconv.FormatInt(nodeID, 10)
}

// ClashRenderOptions tunes RenderClashConfig.
type ClashRenderOptions struct {
	ListenAddress string
}

// ClashRenderedConfig is a complete core configuration.
type ClashRenderedConfig struct {
	Payload []byte
	// Hash fingerprints the configuration content; MarkerName embeds it so a
	// running core can be checked for drift with a single lookup.
	Hash       string
	MarkerName string
	// ProxyNodeIDs maps the index of each rendered proxy to its node, which is
	// how core parse errors ("proxy N: ...") are traced back to a node.
	ProxyNodeIDs []int64
	Listeners    int
}

type mihomoProfileSection struct {
	StoreSelected bool `yaml:"store-selected"`
	StoreFakeIP   bool `yaml:"store-fake-ip"`
}

type mihomoUser struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type mihomoListener struct {
	Name   string       `yaml:"name"`
	Type   string       `yaml:"type"`
	Listen string       `yaml:"listen"`
	Port   int          `yaml:"port"`
	Proxy  string       `yaml:"proxy"`
	UDP    bool         `yaml:"udp"`
	Users  []mihomoUser `yaml:"users"`
}

type mihomoDocument struct {
	Mode            string               `yaml:"mode"`
	LogLevel        string               `yaml:"log-level"`
	AllowLan        bool                 `yaml:"allow-lan"`
	IPv6            bool                 `yaml:"ipv6"`
	UnifiedDelay    bool                 `yaml:"unified-delay"`
	TCPConcurrent   bool                 `yaml:"tcp-concurrent"`
	GeoAutoUpdate   bool                 `yaml:"geo-auto-update"`
	FindProcessMode string               `yaml:"find-process-mode"`
	Profile         mihomoProfileSection `yaml:"profile"`
	Proxies         []map[string]any     `yaml:"proxies"`
	ProxyGroups     []map[string]any     `yaml:"proxy-groups"`
	Listeners       []mihomoListener     `yaml:"listeners"`
	Rules           []string             `yaml:"rules"`
}

// RenderClashConfig renders the full core configuration. Output is
// deterministic for equal input. The configuration never contains a DIRECT
// outbound: listeners route to their node or to REJECT, and anything else
// falls through to MATCH,REJECT.
func RenderClashConfig(nodes []ClashRenderNode, opts ClashRenderOptions) (*ClashRenderedConfig, error) {
	sorted := make([]ClashRenderNode, len(nodes))
	copy(sorted, nodes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].NodeID < sorted[j].NodeID })

	listen := strings.TrimSpace(opts.ListenAddress)
	if listen == "" {
		listen = "127.0.0.1"
	}

	doc := mihomoDocument{
		Mode:     "rule",
		LogLevel: "warning",
		IPv6:     true,
		// Time the second request on the already open connection so the
		// reported delay excludes the proxy handshake.
		UnifiedDelay:    true,
		TCPConcurrent:   true,
		FindProcessMode: "off",
		Proxies:         make([]map[string]any, 0, len(sorted)),
		Listeners:       make([]mihomoListener, 0, len(sorted)),
		Rules:           []string{"MATCH," + clashRejectProxy},
	}
	proxyNodeIDs := make([]int64, 0, len(sorted))
	seenPorts := make(map[int]int64, len(sorted))
	for _, node := range sorted {
		if node.ListenPort <= 0 || node.ListenPort > 65535 {
			return nil, fmt.Errorf("clash node %d has invalid listen port %d", node.NodeID, node.ListenPort)
		}
		if other, dup := seenPorts[node.ListenPort]; dup {
			return nil, fmt.Errorf("clash nodes %d and %d share listen port %d", other, node.NodeID, node.ListenPort)
		}
		seenPorts[node.ListenPort] = node.NodeID

		target := clashRejectProxy
		if node.Config != nil {
			proxy := cloneClashConfig(node.Config)
			proxy["name"] = ClashProxyName(node.NodeID)
			doc.Proxies = append(doc.Proxies, proxy)
			proxyNodeIDs = append(proxyNodeIDs, node.NodeID)
			target = ClashProxyName(node.NodeID)
		}
		doc.Listeners = append(doc.Listeners, mihomoListener{
			Name:   ClashListenerName(node.NodeID),
			Type:   "mixed",
			Listen: listen,
			Port:   node.ListenPort,
			Proxy:  target,
			Users:  []mihomoUser{{Username: node.Username, Password: node.Password}},
		})
	}

	body, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("render clash config: %w", err)
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	marker := clashMarkerPrefix + hash[:16]
	doc.ProxyGroups = []map[string]any{{"name": marker, "type": "select", "proxies": []string{clashRejectProxy}}}
	payload, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("render clash config: %w", err)
	}
	return &ClashRenderedConfig{
		Payload:      payload,
		Hash:         hash,
		MarkerName:   marker,
		ProxyNodeIDs: proxyNodeIDs,
		Listeners:    len(doc.Listeners),
	}, nil
}

func cloneClashConfig(config map[string]any) map[string]any {
	out := make(map[string]any, len(config))
	for key, value := range config {
		out[key] = value
	}
	return out
}

var clashProxyIndexErrorPattern = regexp.MustCompile(`\bproxy (\d+):`)

// ParseClashProxyIndexError extracts N from core errors shaped "proxy N: ...".
func ParseClashProxyIndexError(message string) (int, bool) {
	match := clashProxyIndexErrorPattern.FindStringSubmatch(message)
	if len(match) != 2 {
		return 0, false
	}
	index, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return index, true
}
