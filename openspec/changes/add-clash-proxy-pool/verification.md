## 验证环境

- 本机：Windows 11，Go 1.27.0（模块内自动切换工具链），Node 22；无 PostgreSQL / Redis / Docker。
- 集成测试（`-tags=integration`）本机只做编译检查，需在 CI 执行。

## 后端

| 项 | 命令 | 结果 |
|---|---|---|
| 编译 | `go build ./...` | 通过 |
| 进程内内核冒烟 | `go test -tags=unit ./internal/clashcore/` | 通过：经节点转发、listener 认证拒绝、REJECT 失败即断、延迟测试、坏节点 `proxy N:` 报错且旧配置继续服务、热重载保留已建立连接、端口冲突经日志桥接可见、按入口关闭连接 |
| 运行时适配 | `go test -tags=unit ./internal/clashruntime/` | 通过：外部 sidecar REST（应用/400 定位/标记检测/延迟/关闭连接/鉴权失败）；嵌入式运行时加载 `service.RenderClashConfig` 产出的配置后真实转发、占位拒绝、SOCKS5 自检识别真实 mihomo 监听、拒绝节点下标映射 |
| 订阅解析 | `go test -tags=unit ./internal/pkg/clashsub/` | 通过：Clash YAML、Base64 YAML、裸列表、ss/trojan/vless/hysteria2/vmess 链接（明文与 Base64）、HTML/providers/空/无效、节点上限、指纹、userinfo、地址分类 |
| 服务层 | `go test -tags=unit -run "Clash\|ProbeClashListener" ./internal/service/` | 24 个用例通过：渲染确定性与 fail-closed、同步匹配/状态规则/私网筛查/骤降保护、出口守卫（占用、同出口 IP 跨节点、影子与自身排除、批量、中转、未探测、不可用、上限可调）、暂停对齐（施加、续期窗口、恢复清除、不动外部暂停、出口变化与确认、订阅禁用）、延迟迟滞与全局失败保护、出口策略（pause/accept、未绑定跟随、本机出口判无效、失败标 stale）、刷新全流程（加密、脱敏、改名保端口、消失、骤降跳过与强制、重复拒绝）、加密密钥要求、SSRF 策略、试解析、坏节点隔离、漂移重推、listener 自检、到期选择、告警指标、设置校验 |
| 全量单测 | `go test -tags=unit ./...` | 除 `TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort` 外全部通过；该用例在未改动的基线（4b594a349）上同样失败。`TestFilterGrokFreeQuotaAccountsOnlyBlocksExplicitFreeOAuth` 在一次全量运行中偶发失败，单独运行与复跑均通过 |
| 集成测试编译 | `go vet -tags=integration ./internal/...` | 通过（含新增 `clash_repo_integration_test.go`，并修正旧签名调用） |
| Lint | `golangci-lint run ./...`（v2.13，CI 模式） | 0 issues |
| wire | `wire`（go1.27 构建） | 重新生成 `wire_gen.go`，`cmd/server` 测试通过 |
| 体积 | `go build -trimpath -ldflags "-s -w" ./cmd/server` | 约 114MB → 154MB（内核编入） |

## 前端

| 项 | 命令 | 结果 |
|---|---|---|
| 全量单测 | `npx vitest run` | 350 个文件 / 2685 个用例全部通过（改动前 340 / 2574），无未处理错误。修复了 `AccountsView.selectAllResults.spec.ts` 中筛选切换后未等待重新加载即结束的既有竞态（全量运行时偶发未处理拒绝） |
| i18n 完整性 | `npx vitest run src/i18n/__tests__/localeKeyCompleteness.spec.ts` | 通过 |
| 类型检查 | `npx vue-tsc --noEmit` | 无错误 |
| Lint | `npx eslint --max-warnings 0 <52 个改动/新增文件>` | 0 错误 0 警告 |
| 截图（mock 环境） | `node .claude/mock-api/shoot.mjs` | `.claude/shots/clash/`：Clash 页亮/暗/英文/关闭模式、节点面板、编辑账号代理选择器（亮/暗）、代理页 Clash 筛选、账号列表（Clash 代理列与出口不可用状态）、新建订阅试解析、池设置、删除仍被使用提示、暂停详情；`.claude/shots/clash-final/` 为补充统计口径与原因本地化后的复拍 |

新增用例覆盖：Clash API（路径、参数、超时、分批）、代理选择器 Clash 分组（置灰规则含当前账号自占用、平台提示、选中标签、未传出口时不渲染）、Clash 页渲染与交互、代理页来源筛选与只读、账号页出口加载与代理列、新建弹窗批量拦截、`[clash-exit]` 状态与暂停弹窗、原因本地化与并发工具。

## 未在本机验证

- 仓储集成测试（需 PostgreSQL/Redis，交 CI）。
- 真实机场订阅与真实上游请求的端到端链路（需部署环境）。建议上线前按 `docs/CLASH_PROXY_POOL.md` 走一遍：导入订阅 → 出口探测 → 指定账号出口 → 发请求核对出口 IP → 禁用节点观察暂停与告警 → 启用后自动恢复。
