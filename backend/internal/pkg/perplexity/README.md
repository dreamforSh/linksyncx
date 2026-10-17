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

### 思考内容（reasoning_content）

推理模型（`pplx_reasoning`、`*_thinking` 等）会下发计划块 `plan_block.goals`，其
`description` 是模型自述的思考目标。网关把这些目标（去重、清洗、限长）作为
`reasoning_content` 下发，先于最终答案：流式按出现顺序增量发送，非流式聚合到
`message.reasoning_content`。这是"思考"的呈现，不是答案正文，也不是客户端可执行工具；
由 `include_search_progress` 一并控制（与检索进度同一开关），结构载荷/代码不外泄。

### 推理模型的流式终止

推理模型不发独立的 `end_of_stream` 事件，而用 `final_sse_message: true` 标记最终消息；
其遗留 `text` 字段是步骤【数组】而非对象。网关把 `final_sse_message` 视为等价终态，并
接受数组/不可解析的遗留 `text`（跳过而非整条判错），因此推理模型的流不再被误判为
`malformed`/`incomplete`，答案也不会在最终事件处被截断。

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

## 渠道监控

原生 Perplexity 监控关联一个 cookie 账号，不接受 endpoint、API key、通用请求模板、
自定义头或 body。三种模式都复用账号的 Cookie、代理和 TLS 配置：

- `quota`：默认。只读取会话、credits 与 rate limits，不调用模型。
- `probe` / `quota_probe`：必须显式选择。对账号支持的模型发送算术探活，消耗订阅额度。
  回答正确但上游型号声明未匹配，或结果未能按当前凭据保存时，状态为降级而不是正常。

配额只展示已验证的 Pro / Research 绝对余量。未知结构、过期或失败分段不会显示为健康，
也不编造总额度或百分比。兼容网关的 API key 探活仍使用 OpenAI provider。

## 函数工具模拟（function tool call）

网页 Ask 端点**没有原生函数调用**，网关用提示词协议模拟客户端 function 工具：

- 把客户端 `tools`（function 型）渲染为 `query_str` 头部的文本协议说明，要求模型以
  `<tool_call>{"name":...,"arguments":{...}}</tool_call>` 作答；`assistant.tool_calls` /
  `role=tool` 历史折叠为 `<tool_call>`/`<tool_result>` 文本（见 `tools.go`）。
- 存在可调用工具时，请求强制 `search_focus=writing`**关闭联网搜索**，让 `query_str`
  退化为纯 prompt（否则工具协议会被当成检索查询）。因此**同一请求里函数工具与原生
  联网搜索互斥**，工具优先。
- 上游回答开头出现 `<tool_call>` 才按工具解析，还原为 Chat Completions `tool_calls`
  （`finish_reason=tool_calls`），再由既有 lifter 自动转为 Responses `function_call` /
  Anthropic `tool_use`。**解析失败整段按普通文本下发，绝不伪造调用。**
- 三条入站协议（CC / Responses / Messages）到上游发送汇聚点已归一为 CC 形态，工具解析
  统一在此完成；入站校验层只做放行，不再拒绝 function 工具与工具历史。
- 启用模拟的响应带 `X-Perplexity-Tool-Emulation: active` 头。

能力边界（实测）：**遵从率由底座模型主导。** 强指令模型（如 `claude55opus`、`gpt54`、
`claude46sonnet`）下 auto / required / 多工具 / 嵌套参数均可用；较弱模型（如 `pplx_pro`）
只有 auto 稳定，`tool_choice=required`/具名可能不兑现。故 function 工具场景建议映射到强
底座模型。

- `tool_choice=auto`：模型可自行决定；不调用时按普通文本 + `finish_reason=stop` 返回。
- `tool_choice=required`/具名：要求必须调用（指定）工具。网关同步判定满足性——**模型
  未产出满足约束的调用时返回 400**（三协议、流式/非流式统一，在任何响应提交前返回），
  绝不伪造调用、也不把无关文本当成功。
- 旧式 `functions`/`function_call` 不支持；MCP / 自定义 / 内置服务端工具历史不支持。

## 验证边界

本轮使用假上游、内存 HTTP、竞态检测和 PGlite PostgreSQL 语义检查。
浏览器预览使用隔离假 API，不会调用真实 Perplexity。
这些验证不证明任何真实账号的档位、Cookie 生命周期或模型权益。
函数工具模拟的遵从率依赖所选底座模型，不是上游原生函数调用；
`search_focus=writing` 关搜索与遵从率由 `live_search_focus_probe_test.go`（需真实 cookie）实测。
