## Why

出口代理目前只能在「IP管理」逐条手工录入 http/https/socks5，账号通过 `accounts.proxy_id` 绑定。机场或自建 Clash 订阅中的 ss/vmess/vless/trojan/hysteria2/tuic 等节点 Go 进程无法直接使用；想做到「一号一出口」只能另搭本地代理再逐条录入，节点增减、失效和出口 IP 变化都无人感知，账号可能长期走在失效或与他人共享的出口上。

## What Changes

- 新增 Clash 订阅管理：多订阅增删改、启停、手动/定时刷新、试解析；支持 Clash YAML、Base64 包裹 YAML、Base64/明文节点链接；解析 `subscription-userinfo` 展示流量与到期。订阅链接 AES-GCM 加密存储（要求固定 `TOTP_ENCRYPTION_KEY`），接口只返回脱敏地址，审计日志不记录请求体。
- mihomo 内核以 Go 库方式编译进 sub2api（`clash_pool.mode=embedded`，一次部署带齐内核）；可切换为外部 sidecar（`external`，REST 推送配置）或关闭。
- 每个节点物化为一条 `proxies.source='clash'` 的托管代理（`socks5://随机凭据@127.0.0.1:<端口>`），内核为每个节点开一个 listener 直指该节点。账号沿用 `proxy_id` 绑定，网关出站链路零改动。配置中不存在 DIRECT 出口，失效节点改为 REJECT 占位，绝不退化为直连。
- 健康检测（内核延迟探测，带迟滞与全局失败保护）、出口 IP 探测（复用 `ProxyExitInfoProber`，限速）、AI 平台可达性检测（复用代理质量检测目标，仅提示）。
- 独立出口守卫：按出口 IP 去重，每个出口默认 1 个账号（可调）；挂在账号创建/编辑/批量编辑/复制入口；自定义中转账号禁止使用 Clash 出口。
- 节点不可用（连续探测失败、订阅中消失、禁用、无效、出口 IP 原地变化待确认）→ 绑定账号以 `[clash-exit]` 原因临时不可调度；恢复后自动解除；token 刷新跳过这些账号。
- 托管代理在「IP管理」中只读；手动代理不得占用 Clash 端口段、不得把 Clash 出口设为备用代理；CRS 同步不覆盖 Clash 绑定；账号导出不含托管代理，导入时相应账号以暂停状态创建。
- 新增 4 个运维告警指标；前端新增 Clash 订阅页、代理选择器「Clash 出口」分组与占用提示、代理页来源筛选、状态徽章。

## Capabilities

### New Capabilities
- `clash-proxy-pool`：订阅与节点管理、内核运行时、健康与出口探测、独立出口守卫、失效暂停与恢复、告警。

### Modified Capabilities
<!-- openspec/specs 目前为空，没有既有能力需要修改。 -->

## Impact

- **数据库**：迁移 `246_clash_proxy_pool.sql`：`proxies.source` 列；新表 `clash_profiles`、`clash_nodes`。ent 仅 proxy schema 增加 `source`。
- **后端**：新包 `internal/clashcore`（进程内 mihomo）、`internal/clashruntime`（embedded/external 适配）、`internal/pkg/clashsub`（订阅解析，复用 mihomo 转换器）；service 层 `clash_*.go`；repository `clash_repo.go`、`clash_notify.go`；admin handler `clash_handler.go` 与 `/api/v1/admin/clash/*` 路由；配置节 `clash_pool`；`main.go` 在 HTTP 监听前启动内核。
- **依赖**：新增 `github.com/metacubex/mihomo v1.19.31`（私有服务，按需求不考虑许可兼容）；testify 等少量间接依赖小版本升级。
- **管理端 API**：新增 Clash 接口；`/admin/proxies` 增加 `source` 过滤、返回 `source`；账号数据导入导出增加字段。
- **前端**：新页面、代理选择器、账号/代理/告警相关页面与 i18n。
