package service

import "time"

// ResolveProxyFallbackTarget 计算一个过期代理 start 应把账号改投到哪里。
// 返回 (targetID, change)：
//   - change=false：不改动账号（mode=none，或链路成环/无解的兜底），账号保持绑定已过期的代理（fail-closed）
//   - change=true：改投到备用代理 targetID（此时 targetID 恒非 nil）
//
// 过期回退永远不会把账号改投为直连：分配了代理的账号绝不能静默转为直连（泄漏出口 IP）。
// 历史数据中的 "direct" 已由迁移 251 改为 none；此处遇到任何未知取值也一律按 none 处理。
//
// byID 是「全部代理」的快照（id -> Proxy），now 为判定基准时间。
func ResolveProxyFallbackTarget(start Proxy, byID map[int64]Proxy, now time.Time) (*int64, bool) {
	if start.FallbackMode != FallbackModeProxy {
		return nil, false
	}
	visited := map[int64]struct{}{start.ID: {}}
	curID := start.BackupProxyID
	for {
		if curID == nil {
			return nil, false
		}
		if _, seen := visited[*curID]; seen {
			return nil, false
		}
		p, ok := byID[*curID]
		if !ok {
			return nil, false
		}
		// Clash exits are exclusive; never fail over onto one.
		if p.IsClashManaged() {
			return nil, false
		}
		// Disabled nodes may define a fallback, but cannot be selected as targets.
		if p.IsActive() && !p.IsExpired(now) {
			id := p.ID
			return &id, true
		}
		visited[*curID] = struct{}{}
		if p.FallbackMode != FallbackModeProxy {
			return nil, false
		}
		curID = p.BackupProxyID
	}
}
