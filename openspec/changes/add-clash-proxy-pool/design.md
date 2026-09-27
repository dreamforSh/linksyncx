## Context

动机见 proposal.md。与方案相关的现状：

- 所有出站统一经 `Proxy.URL()` → `proxyurl.Parse`（http/https/socks5/socks5h，不限制 127.0.0.1）→ `proxyutil.ConfigureTransportProxy`；utls 指纹、OAuth、WebSocket 等全部复用。
- 调度不看代理状态；`Account.Proxy` 在调度快照（Redis）中整体缓存，含 host/port/密码。代理被软删后 `ProxyID!=nil && Proxy==nil` 会静默直连。
- 临时不可调度（`SetTempUnschedulable`）自带 scheduler_outbox 与快照同步，但会被清除错误、账号测试成功、token 刷新成功等路径无条件清除。
- OpenAI 传输错误中 `connection refused` / SOCKS 认证失败 / 407 视为持久故障，账号临时下线 10 分钟。

## Goals / Non-Goals

**Goals:**
- 一次部署带齐内核；网关出站链路零改动。
- 任何情况下绑定 Clash 出口的账号都不直连。
- 出口 IP 不在管理员不知情时漂移（默认暂停待确认）。
- 多实例一致：各实例本地内核配置相同，探测与暂停由单实例执行。

**Non-Goals:**
- 不做批量/自动分配出口，不做分组出口池（仅手动指定）。
- 不做按账号流量统计。
- 不支持 `proxy-providers` 引用型订阅与 `dialer-proxy` 链式节点。

## Decisions

### D1：mihomo 以库方式编译进进程
`internal/clashcore` 封装 `executor.ParseWithBytes` / `ApplyConfig`、`tunnel.Proxies()[name].URLTest`、`statistic.DefaultManager`，日志经订阅桥接到 zap（限流，仅 warn/error）。mihomo 全局状态决定进程内单例。只有 `cmd/server` 通过 `clashruntime.New` 引用它，service 层只依赖 `ClashRuntime` 接口，service 单测不编译 mihomo。外部 sidecar 模式用 REST（`PUT /configs?force=true` payload，已核实走 `executor.ApplyConfig`，不重建控制端口）。

### D2：节点物化为托管代理，listener 直指节点
每个节点一条 `source='clash'` 的 proxies 记录与一个 listener（`type: mixed`，`proxy: n-<id>`，`users` 为随机凭据）。托管代理 host/port/凭据创建后永不变化（Redis 快照缓存了它们）；端口只在托管代理删除后回收（回收要求无账号引用）。模式切换导致 listener host 变化时统一 rehost 并对绑定账号投 `account_bulk_changed`。

### D3：fail-closed 渲染
渲染按 node id 排序、确定性输出；`rules: [MATCH,REJECT]`；活节点 listener 指向自身出站，不可用但仍被引用的节点渲染为 `proxy: REJECT` 占位；漂移检测标记为只含 REJECT 的 select 组 `__sub2api_cfg_<hash16>`。内核拒绝配置时按 `proxy N:` 下标定位节点标 invalid 后重试。

### D4：只在结构变化时热重载
`ApplyConfig` 会短暂挂起 tunnel，因此只有渲染哈希变化或标记缺失时才应用；健康状态不参与渲染；变更通知去抖 2 秒；每实例 60 秒兜底对齐。应用后以 SOCKS5 用户名密码握手自检每个 listener（mihomo 只记日志不报错的端口冲突、以及被外部免认证代理占用的端口都能识别）。

### D5：暂停复用临时不可调度，电平触发对齐
不在 `IsSchedulable` 增加代理判断（分桶快照 meta 不含 Proxy，改动面大且旧快照 fail-open）。leader 每 30 秒按「应有状态」对齐：应暂停的写 `[clash-exit] <节点>: <原因>`，TTL 30 分钟、剩余不足一半续期（长于其他子系统的 10 分钟写入，避免 reason 被覆盖）；不应暂停且 reason 带前缀的清除（条件清除在 clash repository，批量投 outbox，不给 AccountRepository 加方法）。token 刷新候选排除带前缀的暂停。

### D6：出口守卫只在绑定变化时校验
出口键 = 出口 IP（跨订阅统一去重），未探测时退化为节点；占用排除影子账号与自身。只有新 proxy_id 与旧值不同且为托管代理时检查可用性、是否已探测与占用上限；自定义中转无论是否变化都拒绝。批量编辑禁止设置托管代理；复制账号时副本不继承（副本本就不可调度）。进程内互斥覆盖检查到落库之间，多实例竞态由冲突指标兜底。

### D7：订阅同步匹配与保护
匹配顺序：配置哈希（去 name）→ 同名 → (type, server, port)，匹配到的节点保留端口与托管代理。未匹配 → missing（disabled 保持）。返回 0 个或骤降超过 50% 时跳过（可强制）。节点类型白名单剔除 direct/dns/reject；回环/链路本地地址始终拒绝，内网地址可配置放行；出口 IP 与本机直连出口相同的节点标 invalid。

### D8：订阅拉取的 SSRF 策略
默认拦截内网/回环/云元数据（拨号时逐 IP 校验防 DNS rebinding，重定向逐跳校验）；`allow_private_hosts` 放行内网与本机（自建 subconverter），云元数据与链路本地始终拦截；经指定手动代理拉取时只做 URL 层校验。

## Risks / Trade-offs

- mihomo 协程 panic 会带崩进程 → 依赖容器/systemd 拉起；启动时 HTTP 监听前完成首轮配置加载。
- 依赖体积增大（服务端二进制约 +30MB）。
- ip-api 免费额度按来源 IP 计 → 探测限速；失败只标 stale，不影响健康。
- 多实例到节点的可达性差异 → 以 leader 探测为准，文档说明。
