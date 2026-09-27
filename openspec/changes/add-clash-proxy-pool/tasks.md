## 1. 内核与解析

- [x] 1.1 引入 `github.com/metacubex/mihomo v1.19.31`，新增 `internal/clashcore`（进程内引擎：Open/Apply/Validate/DelayTest/HasProxy/CloseInboundConnections/Close，日志桥接限流，`store-selected` 关闭避免占用 cache.db）
- [x] 1.2 新增 `internal/pkg/clashsub`：Clash YAML / Base64 YAML / 节点链接（复用 mihomo `convert.ConvertsV2Ray`）解析，类型白名单、剔除 interface-name/routing-mark、跳过 dialer-proxy，`ConfigHash`、`ParseUserInfo`、`ClassifyServer`
- [x] 1.3 新增 `internal/clashruntime`：embedded 与 external（REST）两种 `service.ClashRuntime` 实现，`New(cfg)` 按 `clash_pool.mode` 选择

## 2. 数据与配置

- [x] 2.1 迁移 `246_clash_proxy_pool.sql`（`proxies.source`、`clash_profiles`、`clash_nodes`）与迁移文本测试
- [x] 2.2 ent proxy schema 增加不可变 `source` 字段并重新生成；`service.Proxy.Source`、`IsClashManaged()`
- [x] 2.3 代理仓储：`ListActive*` 排除托管代理，`ListWithFilters*` 增加 source 参数（同步 5 个测试桩）；管理接口 `source` 查询参数（默认 manual）；DTO 返回 `source`、托管代理不下发凭据
- [x] 2.4 配置节 `clash_pool`（默认值、校验、`EffectiveListenerHost/ListenAddress`），`deploy/config.example.yaml` 与 compose 注释
- [x] 2.5 DB 设置 `clash_pool_settings`（`ClashPoolSettings` 默认值与范围校验）

## 3. 服务与仓储

- [x] 3.1 `clash_repo.go`：订阅 CRUD、节点视图（含绑定账号）、`ApplyNodeSync`（advisory lock 分配端口 + 创建托管代理 + 配置变化时失效计费探针快照）、渲染节点、回收、rehost、Clash 暂停的施加/条件清除（批量 outbox）
- [x] 3.2 `clash_notify.go`：Redis 配置变更广播与实例状态
- [x] 3.3 `clash_fetch.go`：SSRF 策略、重定向逐跳校验、体积/超时限制、URL 脱敏
- [x] 3.4 `clash_sync.go`：匹配、状态规则、骤降保护、私网节点筛查、托管代理命名
- [x] 3.5 `clash_render.go`：确定性渲染、REJECT 占位、漂移标记、`proxy N:` 下标解析
- [x] 3.6 `clash_service.go` / `clash_probe.go`：订阅 CRUD/刷新/试解析、节点启停与出口确认、延迟探测（迟滞 + 全局失败保护）、出口探测（变化策略、本机出口比对、平台可达性）、暂停对齐、出口列表、冲突检测、告警指标、出口守卫
- [x] 3.7 `clash_manager.go`：启动（HTTP 监听前）、每实例同步循环（去抖、漂移检测、坏节点隔离、listener 自检）、leader 周期（刷新、探测、对齐、回收）

## 4. 账号链路与旁路

- [x] 4.1 出口守卫挂载 CreateAccount / UpdateAccount / BulkUpdateAccounts；DuplicateAccount 副本不继承 Clash 出口；绑定变化后触发对齐
- [x] 4.2 托管代理只读（更新/删除/批量删除）、备用代理不得为托管代理、手动代理不得占用 Clash 端口段、到期回退解析跳过托管代理
- [x] 4.3 CRS 同步保留 Clash 绑定；token 刷新候选排除 `[clash-exit]` 暂停
- [x] 4.4 账号导出排除托管代理并附出口提示，导入时以暂停状态创建；导入结果计数

## 5. 接口、告警与装配

- [x] 5.1 `clash_handler.go` 与 `/api/v1/admin/clash/*` 路由；审计动作名与请求体省略名单
- [x] 5.2 4 个告警指标（白名单 + 评估器 + `SetClashMetrics`）
- [x] 5.3 wire 装配（`ProvideClashService` 挂载守卫、`ProvideClashManager`、`clashruntime.New`）、`Application.ClashManager`、`main.go` 启动顺序、`provideCleanup`、`wire_gen.go` 与 `wire_gen_test.go`

## 6. 前端

- [x] 6.1 API 模块与类型，清理未引用的旧订阅类型
- [x] 6.2 Clash 订阅页（内核状态、订阅表、表单与试解析、节点面板、设置）、路由与菜单
- [x] 6.3 ProxySelector「Clash 出口」分组、占用/可用性/平台提示、批量测试并发上限
- [x] 6.4 账号页出口数据加载、代理列徽章、多账号创建拦截、导入导出提示
- [x] 6.5 `[clash-exit]` 状态展示、代理页来源筛选与只读、告警指标选项、i18n、测试
- [x] 6.6 订阅统计补充 `bound_accounts`（去重、不含影子账号），统计卡与停用确认改用账号数；节点状态原因本地化

## 7. 文档与验证

- [x] 7.1 `docs/CLASH_PROXY_POOL.md`、OpenSpec 变更文档
- [x] 7.2 后端单测、lint、集成测试编译
- [x] 7.3 前端测试、类型检查、i18n 检查、截图
