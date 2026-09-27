## Purpose

让管理员导入 Clash 订阅作为代理池，并为每个上游账号手动指定独立出口；节点失效时相关账号自动暂停、恢复时自动恢复，任何情况下都不直连。

## ADDED Requirements

### Requirement: 订阅管理与解析
系统 SHALL 支持多个 Clash 订阅的增删改、启停、手动与定时刷新，并能解析 Clash YAML、Base64 包裹的 YAML 以及 Base64 或明文的节点链接。订阅链接 MUST 加密存储，接口 MUST 只返回脱敏地址。

#### Scenario: 未配置固定加密密钥
- **WHEN** 管理员在未配置 `TOTP_ENCRYPTION_KEY` 时创建订阅
- **THEN** 系统 MUST 拒绝保存并返回 `CLASH_ENCRYPTION_KEY_REQUIRED`

#### Scenario: 信息节点过滤
- **WHEN** 订阅包含名称含「剩余流量」「到期」等的信息节点且未自定义排除正则
- **THEN** 这些节点 MUST 不被导入

#### Scenario: 订阅节点数骤降
- **WHEN** 刷新得到 0 个可用节点，或可用节点数比当前活跃节点下降超过设定比例
- **THEN** 系统 MUST 跳过本次同步并记录为 `skipped`，除非管理员强制刷新

#### Scenario: 私网订阅地址
- **WHEN** 订阅地址解析到内网或回环地址且未开启 `allow_private_hosts`
- **THEN** 系统 MUST 拒绝拉取；云元数据地址 MUST 始终被拒绝

### Requirement: 节点物化与稳定标识
每个节点 MUST 对应一条 `source='clash'` 的托管代理与一个内核 listener。刷新时按配置哈希、名称、(类型, 服务器, 端口) 依次匹配已有节点，匹配到的节点 MUST 保留监听端口与托管代理。

#### Scenario: 节点改名或轮换密码
- **WHEN** 订阅中某节点改名或更换密码
- **THEN** 该节点 MUST 保留原端口与托管代理，绑定账号无需重新指定

#### Scenario: 节点从订阅中消失
- **WHEN** 已有节点不在新订阅中
- **THEN** 节点 MUST 标记为 missing；仍被账号引用时其 listener MUST 渲染为 REJECT 占位

### Requirement: 永不直连
内核配置 MUST 不包含 DIRECT 出口；绑定 Clash 出口的账号在节点不可用、订阅删除、功能关闭等任何情况下 MUST NOT 直连上游。

#### Scenario: 被引用的托管代理
- **WHEN** 托管代理仍被账号引用
- **THEN** 系统 MUST NOT 删除该代理，其端口 MUST NOT 分配给其他节点

#### Scenario: 托管代理只读
- **WHEN** 管理员通过代理管理接口编辑、删除或启停托管代理
- **THEN** 系统 MUST 返回 `CLASH_MANAGED_PROXY_READONLY`

### Requirement: 独立出口
系统 MUST 按出口 IP 统计占用（影子账号不计），每个出口的账号数 MUST NOT 超过设置的上限（默认 1）。仅在绑定关系变化到 Clash 出口时校验。

#### Scenario: 出口已被占用
- **WHEN** 管理员把账号绑定到已有其他账号使用、且达到上限的出口（包括落地同一出口 IP 的另一节点）
- **THEN** 系统 MUST 返回 409 `CLASH_EXIT_OCCUPIED` 并列出占用账号

#### Scenario: 出口未探测或不可用
- **WHEN** 目标节点出口 IP 未探测（且未放开）或节点不可用
- **THEN** 系统 MUST 分别返回 `CLASH_EXIT_UNPROBED` 或 `CLASH_EXIT_UNAVAILABLE`

#### Scenario: 已绑定账号的其他编辑
- **WHEN** 账号已绑定在一个 missing 节点上，管理员只修改名称等其他字段
- **THEN** 系统 MUST 允许保存

#### Scenario: 自定义中转与批量编辑
- **WHEN** 开启自定义中转 base URL 的账号绑定 Clash 出口，或批量编辑把 Clash 出口设给多个账号
- **THEN** 系统 MUST 分别返回 `CLASH_EXIT_RELAY_UNSUPPORTED` 与 `CLASH_EXIT_BULK_UNSUPPORTED`

### Requirement: 失效暂停与自动恢复
节点不可用（连续探测失败、missing、disabled、invalid、订阅禁用或删除、出口 IP 变化待确认）时，绑定账号 MUST 被置为临时不可调度，原因以 `[clash-exit]` 开头；节点恢复后 MUST 自动解除；系统 MUST NOT 清除不带该前缀的其他暂停。

#### Scenario: 节点连续探测失败
- **WHEN** 绑定节点连续失败次数达到阈值
- **THEN** 绑定账号（含影子账号）MUST 被暂停；节点连续成功次数达到恢复阈值后 MUST 自动解除

#### Scenario: 本机网络故障
- **WHEN** 单轮检测中至少 80% 的节点同时失败（样本不少于 5）
- **THEN** 系统 MUST NOT 将节点标为不健康

#### Scenario: 出口 IP 原地变化
- **WHEN** 已绑定节点探测到新的出口 IP 且策略为 pause
- **THEN** 系统 MUST 保留原出口 IP、记录待确认的新 IP 并暂停绑定账号，直到管理员确认

#### Scenario: 暂停被手动清除
- **WHEN** 管理员在节点仍不可用时手动清除账号的临时不可调度
- **THEN** 系统 MUST 在下个对齐周期重新施加暂停

### Requirement: 内核运行与同步
系统 MUST 在 HTTP 开始监听前启动内核并加载配置；每个实例 MUST 按数据库状态同步本地内核，仅在配置变化或漂移时热重载；内核拒绝的节点 MUST 被标记为 invalid 且不影响其他节点。

#### Scenario: 单个坏节点
- **WHEN** 内核因某节点配置错误拒绝整份配置
- **THEN** 系统 MUST 将该节点标为 invalid 并重新应用其余配置

#### Scenario: 端口被占用
- **WHEN** 某 listener 端口被其他程序占用
- **THEN** 自检 MUST 发现并将该节点标为 invalid，绑定账号随之暂停
