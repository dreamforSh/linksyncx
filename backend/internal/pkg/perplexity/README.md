# Perplexity 网页接入与模型巡检

本模块使用 Perplexity 网页会话 Cookie，不是官方 Sonar API。
协议参考 [pplx-proxy 固定版本](https://github.com/jamie950315/pplx-proxy/tree/5e40b654f72339a9fd1044ff9298a2107385471f)，许可保留在仓库 `third_party/pplx-proxy/LICENSE`。
模型目录、订阅提示、一次请求成功和实际型号匹配是不同的证据，不能相互替代。

## 显式的真实模型测试

管理端账号测试提供两种 Perplexity 模式：

- `default`：仅校验 Cookie 会话和配额快照，不调用模型。旧客户端、旧计划保持此行为。
- `perplexity_model`：直接调用选中模型，消耗上游账号额度。必须明确选择模式及模型。

账号测试接口为 `POST /api/v1/admin/accounts/:id/test`，例如：

```json
{
  "model_id": "my-gpt",
  "mode": "perplexity_model"
}
```

这是管理端测试，不作为普通用户 API 请求计费；仍会消耗上游订阅额度。
烟测默认要求上游仅回复 `OK`，不保证固定 token 数。单次期限为 30 秒，
自定义提示词最多 1024 字符且仍受账号查询长度设置约束。
Research/Computer 等长任务可能超时；超时不代表该型号无权益。

SSE 的 `perplexity_probe` 事件返回结构化记录，包括请求型号、发送的 preference/mode、
上游声明、匹配状态、耗时、观察时间、错误分类及保存状态。
`verified` 仅表示上游声明匹配；`substituted`、`mixed`、`unknown` 不记为型号已确认。
凭据或代理在途中变化时标记 `stale`。保存失败也会在界面提示。

最新记录按 preference/mode 保存到账号 Extra 的 `perplexity_model_health`，最多 128 项，
不在账号 Extra 保存回答正文。写入匹配完整凭据、代理和观察版本，并与调度 outbox 原子提交。
只有明确的模型不支持错误会暂避对应型号五分钟；认证、Cloudflare、429、网络或格式错误
不会据此停用其他模型或整个账号。新探测成功可更新对应模型状态，不能解除手动停用。

## 定时巡检

复用现有“定时测试计划”，每个计划选择模型与 cron 周期。
新增 `test_mode` 字段，默认 `default`；不会自动建立付费模型扫描计划。
真实模型模式必须显式选择，并关闭账号级 `auto_recover`：模型健康会独立维护。

```json
{
  "account_id": 72,
  "model_id": "my-gpt",
  "test_mode": "perplexity_model",
  "cron_expression": "7 9 * * *",
  "enabled": true,
  "auto_recover": false
}
```

计划执行具有跨实例互斥、锁后到期/版本重读、运行结束时的条件更新及关闭取消。
协调后端不可用时不执行真实模型计划。成功答复但型号未确认的结果以 `warning` 保存，
结构化信息位于结果的 `metadata.perplexity_probe`。
Perplexity 会话/模型恢复均不使用其他平台的泛化账号恢复逻辑。

## Thinking 自动选型

三个入站协议均在类型转换前读取 Thinking 意图，例如 Chat Completions 的
`thinking: true`、Responses 的 `reasoning.effort`、Messages 的 `thinking.type`。
先应用账号映射，再选已确认的同家族变体。已知对应项包括
`gpt54 → gpt54_thinking` 和 `claude46sonnet → claude46sonnetthinking`；
其他型号只使用当前凭据身份绑定、24 小时内目录中实际存在的对应变体，不猜版本或换家族。
显式映射的 Thinking 型号不能由请求偷偷改成基础型号。

选变体不等于兑现 high/low 等强度或预算。
无法兑现的强度以 `*.strength` 出现在 `X-Perplexity-Ignored-Parameters`；严格模式拒绝它们。
单独的 Thinking 开关可生效，明确的 token 预算仍拒绝。
最终选择用于发送、模型健康终检、续聊隔离和实际型号观测。

## 查询配置

创建/编辑表单和 API 支持：

- `keep_system_prompt`：默认 `false`，避免样板指令污染搜索；设为 `true` 会把指令一起发送。
- `max_query_chars`：默认 90000，允许 1000–96000 的整数；0 关闭网关限长，界面会警告。

超限时丢弃较老历史，最终问题完整保留；最终问题本身超限则拒绝。
这不是无损上下文存储，也不是 Responses 的 `previous_response_id` 功能。

## MCP

使用绑定 Perplexity 分组的 API Key，向 `POST /mcp` 发送 JSON-RPC。
提供 ask、search、reason、research、models 五个工具；reason 会显式启用已确认的推理/Thinking 选择。
search/reason 支持当前 Key 允许的模型名称覆盖。搜索配置包括 sources、language、timezone、
`is_incognito`；未知或非法参数明确拒绝。
`perplexity_models` 复用当前 Key 的 `/v1/models` 清单规则，不是静态目录，也不保证实测权益。
工具执行仅通过同 Key 的网关链路，不额外走一条绕过计费或调度的调用路径。

保持 JSON 响应兼容；客户端接受 `text/event-stream` 时可收到 SSE 消息。
有 `_meta.progressToken` 时发送单调递增的 `notifications/progress`，不伪造总量百分比，
只发布白名单检索状态，不公开内部查询载荷、代码或思维链。
`notifications/cancelled` 按 Key、分组和请求 ID 隔离取消在途调用。
HTTP 断开也会停止本次网关连接，但不承诺上游任务停止计费或退款。

服务为无持久会话的现代 Streamable HTTP 实现：不提供旧式 `/sse` 双端点、重连重放或批量 RPC。
`GET/DELETE /mcp` 返回 405。通知取消仅在当前实例生效，多实例需粘性路由；没有分布式 MCP 会话声明。
浏览器 Origin 必须显式匹配 CORS 具体来源或 `server.frontend_url`；通配 CORS 与不可信 Host
不能作为 Origin 许可。无 Origin 的 CLI 客户端不受此浏览器限制。
内部 HTTP 固定为本机网关，禁用环境代理与重定向，不将 API Key 发往第三方。

协议依据：[Streamable HTTP](https://modelcontextprotocol.io/specification/2025-03-26/basic/transports)、
[进度通知](https://modelcontextprotocol.io/specification/2025-03-26/basic/utilities/progress)。

## 验证边界

本轮使用假上游、内存 HTTP、竞态检测和 PGlite PostgreSQL 语义检查。
浏览器预览使用隔离假 API，不会调用真实 Perplexity。
这些验证不证明任何真实账号的档位、Cookie 生命周期或模型权益。
