# TinyClaw Core Model Refactor v1

> 状态：目标模型草案。本文只记录已确认的 core model，不包含 clawman 内部 sandbox 模块重设计。

## 1. 边界

TinyClaw core 不再直接持有企业微信、微信、斗鱼等第三方平台协议逻辑。

第三方平台由外挂 Channel Adapter 负责：
- 拉取或接收平台消息
- 持有平台凭据、cursor、offset、重连状态
- 标准化 inbound message 后调用 clawman
- 轮询 clawman deliveries 并执行真实外发

clawman 负责：
- 接收已注册 Room 的标准化 Message
- 注册和更新 Room
- 保存 Message
- 应用 Trigger Policy
- 扫描 Agent Session 并运行 agent
- 在一次 Agent Run 中按需运行 Subagents
- 生成 Delivery outbox
- 提供 adapter-facing HTTP API

## 2. 核心概念

### Room

`Room` 是 TinyClaw 的共享协作空间，承载一条 append-only Message 时间线，并作为 agent runtime scope。第一版中，一个外部 `Channel Room` 可以映射到一个 `Room`。

`rooms.channel` 和 `rooms.channel_room_id` 第一版不可变；如果外部绑定变化，创建新的 Room。

Room 必须先注册，后续 Message 才能写入。Room 不再作为 Message 入站的副作用自动创建。

Room 的 `channel` 决定消息写入口和 Delivery consumer。`channel = local` 时由 TinyClaw local client 写入 Messages 并消费 Deliveries；第三方 channel 由对应 Channel Adapter 写入 Messages 并消费 Deliveries。

Room 重新注册时允许更新 `display_name`、`outbound_alias`，以及该 Room 默认 Agent Session 的 `enabled`、`trigger_policy`。`channel`、`channel_room_id`、`channel_room_type` 是 Room 的外部身份，不允许通过更新改变。

### Message

`Message` 是已经通过某个 channel 进入 Room 的原始 append-only 事实，可来自本地用户、agent、系统或外部 Channel。

Message 不再使用企业微信 archive `seq` 作为身份。第三方平台消息身份由 adapter 提供 `source_message_id`，用于幂等插入。

Message 不保存全局 `skipped` 状态。是否触发 agent、是否进入某次 Agent Run 上下文，由 Trigger Policy、Command handler 和 Agent Session 的读取策略决定。

### Agent

`Agent` 是可配置的 agent 定义，可被用户在 Room 中寻址，也可在一次 Agent Run 内作为 Subagent 执行。第一版 Agent 定义可通过 API/UI 编辑，但不保留版本或运行快照；历史排障依赖结构化日志。

### Agent Session

`Agent Session` 表示一个 Room 内默认 orchestrator 的长期运行状态，保存启用状态、触发规则、处理边界、runner continuation 和执行锁。

第一版每个 Room 恰好有一个 Agent Session。用户可寻址的多个 Agent 作为 run-scoped Subagents 执行，不为每个 Agent 创建长期 Agent Session。

### Subagent

`Subagent` 是一次 Agent Run 内部临时执行的 Agent。Subagent 可以通过本次 Agent Run 的 memory search capability 读取 Room Memory，但不拥有 Room 身份、进度游标或 Room Memory 写入权。

### Delivery

`Delivery` 是 agent run 或 Command 产生的 channel-bound outbound intent。agent run 完成只表示 agent 执行完成，不要求 Delivery 已发送成功，也不表示目标 channel 已出现该消息。

第一版 agent final output 非空时，clawman 自动创建一条 Delivery；final output 为空时，只推进 Agent Session 的处理边界，不产生 Delivery。Delivery ack 不要求同步写回 Message；consumer 可主动写回 `source_kind = agent` 的 Message，也可等待平台 self echo 后由 adapter 写入。

### Room Memory

`Room Memory` 是归属于 Room 的长期记忆，不归属于 Agent Session、runner session 或 sandbox。

Agent Session 在 Agent Run 中可以通过 Clawman 颁发的短期 memory capability token 主动执行 Memory Search。Memory Search 只返回当前 Agent Run 所属 Room 的 active Memory Items，不接受 agent 传入的 `room_id`。

第一版 Memory Item 类型只包含 `fact`、`preference`、`todo`。普通更正通过 `stale` 或 `closed` 状态表达，不物理删除。

## 3. 表模型

### rooms

```text
id bigint primary key
tenant_id text not null
channel text not null
channel_room_id text not null
channel_room_type text not null
display_name text null
outbound_alias text not null
created_at timestamptz not null
updated_at timestamptz not null

unique(tenant_id, channel, channel_room_id)
```

第一版 `tenant_id = default`。后续可由 API token 绑定 tenant。

`outbound_alias` 是 Channel Adapter 执行真实外发时用于定位目标会话的名称。WeCom adapter 能从接口获取 name 时，默认使用该 name 作为 `display_name` 和 `outbound_alias`。

### messages

```text
id bigint primary key
room_id bigint not null references rooms(id)
source_message_id text not null
source_kind text not null
source_ref text null
sender_kind text not null
sender_id text not null
sender_name text null
payload jsonb not null
message_time timestamptz not null
created_at timestamptz not null

unique(room_id, source_message_id)
```

Message 是已进入 Room channel 的 append-only 原始事实。`source_kind` 表达来源类型，例如 `local`、`external`、`agent`、`system`；`sender_kind` 表达发言主体类型，例如 `person`、`agent`、`external`、`system`。

### agent_sessions

```text
id bigint primary key
room_id bigint not null references rooms(id)
enabled boolean not null
trigger_policy jsonb null
pending_trigger_message_id bigint null references messages(id)
caught_up_message_id bigint not null
codex_session_id text null
lock_owner text null
lock_expires_at timestamptz null
created_at timestamptz not null
updated_at timestamptz not null

unique(room_id)
```

`pending_trigger_message_id` 是该 Agent Session 当前需要追上的最新触发边界。入站 Message 命中该 Agent Session 的 Trigger Policy 时，更新该字段。

`caught_up_message_id` 是该 Agent Session 已经追到的 Room Message 边界。它不表示边界前每条 Message 都逐条进入过 prompt，只表示该 Agent Session 的长期上下文已追到该 Room 时间线位置。

`codex_session_id` 是当前 Codex CLI runner 的 continuation id。当前实现只有 Codex CLI runner，所以该 id 直接归属于 Agent Session，不引入通用 provider/session 表。该字段不是 Room Memory；它只用于让下一次 Agent Run 通过 `codex exec resume` 复用同一个 Codex CLI thread。

`pending_trigger_message_id` 同时是触发信号和本次追赶边界。Agent Run 可以读取最近窗口和按需 Memory Search，而不要求把 `caught_up_message_id < id <= pending_trigger_message_id` 的所有 Messages 全量放入 prompt。

`lock_owner` 和 `lock_expires_at` 是执行 loop 的短租约，用于限制同一 Agent Session 同一时间只有一个 agent run。

Agent execution loop 扫描 `pending_trigger_message_id > caught_up_message_id` 且未被有效 lock 持有的 Agent Session，抢占 lock 后开始 agent run。入站 Message 写入路径只负责保存 Message 和更新匹配 Agent Session 的 `pending_trigger_message_id`，不直接启动 agent run。

一次 Agent Run 的追赶边界为 `caught_up_message_id < message.id <= pending_trigger_message_id`。Runner 实际上下文应使用 bounded recent messages、Room Memory 和按需工具调用，而不是无界重放全部历史。

Agent Run 成功或失败后都推进 `caught_up_message_id` 到本次 `pending_trigger_message_id` 并释放 lock。失败时写日志并创建失败提示 Delivery；失败不自动重试。

### agents

```text
id bigint primary key
key text not null unique
display_name text not null
description text null
prompt text not null
allowed_tools jsonb not null
enabled boolean not null
created_at timestamptz not null
updated_at timestamptz not null
```

Agent definitions are mutable in the first version. Updating an Agent overwrites the current definition; TinyClaw does not keep `agent_versions`, prompt snapshots, or subagent run-step snapshots in the first version.

Agent Run logs should include `agent_run_id`, `room_id`, `agent_session_id`, selected subagent keys, selected subagent `agent_id`s, tool calls, and memory search counts for troubleshooting.

### deliveries

```text
id bigint primary key
room_id bigint not null references rooms(id)
agent_session_id bigint not null references agent_sessions(id)
source_message_from_id bigint not null
source_message_to_id bigint not null
payload jsonb not null
status smallint not null
created_at timestamptz not null
acked_at timestamptz null
```

Adapter 使用 `deliveries.id` 作为轮询游标。

Delivery 不冗余保存 `channel` / `channel_room_id`；外发时通过 `room_id` join `rooms`。

Delivery 消费成功后，consumer ack Delivery。若真实外发失败，Delivery 可失败或重试，但不会污染 Room 的 Message 时间线。Delivery 与后续 Message 的关联由 `source_message_id`、平台消息 id 或结构化日志 best-effort 追踪，不在 Delivery 核心表中保存强关联字段。

`source_message_from_id` 和 `source_message_to_id` 记录产生该 Delivery 的 Message 闭区间，语义等同于 `source_message_from_id <= message.id AND message.id <= source_message_to_id`。普通 Agent Run 使用本次处理窗口的第一条 Message 和触发边界；Command Delivery 使用命令 Message 自身作为单点闭区间，即 `source_message_from_id = source_message_to_id = command_message.id`。

### memory_items

```text
id bigint primary key
room_id bigint not null references rooms(id)
type text not null
key text not null
content text not null
status text not null
source_message_from_id bigint not null
source_message_to_id bigint not null
created_by_agent_session_id bigint null references agent_sessions(id)
updated_by_agent_session_id bigint null references agent_sessions(id)
created_at timestamptz not null
updated_at timestamptz not null

unique(room_id, type, key)
```

第一版 `type` 只允许 `fact`、`preference`、`todo`。默认 Memory Search 只返回 `status = active`。

### memory_write_jobs

```text
id bigint primary key
room_id bigint not null references rooms(id)
agent_session_id bigint not null references agent_sessions(id)
agent_id bigint not null references agents(id)
source_message_from_id bigint not null
source_message_to_id bigint not null
operation_key text not null unique
op text not null
type text not null
key text not null
content text not null
status text not null
attempts integer not null
last_error text null
created_at timestamptz not null
updated_at timestamptz not null
```

Agent Run Result 中的 Memory Write Proposals 不直接修改 Room Memory，而是先写入 `memory_write_jobs`。后台 worker 异步应用 pending jobs，并做有限重试。

### memory_change_audit

记录 Memory Write Job 对 Room Memory 的 durable change audit。第一版不把 Memory Search 写入 durable audit；search 只使用结构化日志。

### api_clients

```text
id bigint primary key
client_id text not null unique
client_secret_hash text not null
name text not null
enabled boolean not null
permissions jsonb not null
created_at timestamptz not null
updated_at timestamptz not null
```

`api_clients` 表示可调用 Clawman HTTP API 的外部或管理客户端。第一版使用 HTTP Basic authentication：`Authorization: Basic base64(client_id:client_secret)`。Clawman 只保存 secret hash，不保存明文 secret。

迁移期保留 legacy `Authorization: Bearer <CLAWMAN_API_TOKEN>` 供现有 Channel Adapter 使用；新的 Control Plane API 只使用 API Client authentication。

第一版权限是简单能力包，不引入角色继承、token 签发、refresh token 或细粒度权限矩阵。初始权限只包含：

- `adapter`: may call adapter-facing APIs for Room registration, Message ingestion, Delivery polling, and Delivery ack.
- `admin`: may call `/admin/api/*` for control-plane reads and limited operator writes.

Control Plane UI client 通常只授予 `admin`。Channel Adapter client 通常只授予 `adapter`。后续如果需要更精细的职责分离，再把能力包拆成领域动作权限。

第一版不把 API Client 绑定到 channel、bot 或 Room scope；如果需要 channel-level、Room-level 或 operation-level 隔离，后续单独设计 scope 和细粒度 permission 语义。

为避免初始部署没有可登录 client，第一版默认创建一个 admin client。默认 `client_id` 为 `admin`，`client_secret` 读取 `CLAWMAN_ADMIN_SECRET`；若未配置 secret，则禁用默认 admin client 和 `/admin/api/*`。服务启动时只在该 client 不存在时创建，不覆盖已有 secret hash 或权限。

## 4. Room And Message APIs

Adapter 先注册或更新 Room，再向已注册 Room 写入标准化 Message。旧 `POST /api/inbound` 不保留；Room 生命周期和 Message 生命周期分开。

```http
POST /api/rooms
Authorization: Basic base64(client_id:client_secret)
```

按 `(tenant_id, channel, channel_room_id)` 幂等注册或更新 Room：

```json
{
  "channel": "wecom",
  "channel_room_id": "room-123",
  "channel_room_type": "group",
  "display_name": "测试 AI",
  "outbound_alias": "测试 AI",
  "agent_enabled": true
}
```

`outbound_alias` 为空时，不应把 Room 注册为可运行 Room。

Message 写入接口：

```http
POST /api/messages
Authorization: Basic base64(client_id:client_secret)
```

最小请求：

```json
{
  "room_id": 123,
  "source_message_id": "external-msg-123",
  "sender_id": "user-1",
  "sender_name": "Alice",
  "message_time": "2026-05-19T10:00:00Z",
  "payload": {
    "type": "text",
    "text": "hello"
  }
}
```

clawman 行为：

1. 使用 `tenant_id = default`。
2. `POST /api/rooms` 按 `(tenant_id, channel, channel_room_id)` upsert Room。
3. `POST /api/messages` 只接受已存在的 `room_id`；找不到时返回 `404 room_not_found`。
4. 按 `(room_id, source_message_id)` 幂等插入 Message。
5. 如果 Message 已存在，返回已有结果，不重复触发。
6. 对 Room 的 Agent Session 应用 Trigger Policy；为空时使用 channel default rule。
7. 命中 trigger 时更新该 Agent Session 的 `pending_trigger_message_id`。

Duplicate Message never triggers side effects. Only a newly inserted Message may update Agent Session trigger boundaries, enqueue or start command handling, or produce Deliveries.

Message API success only means the inbound Message was accepted or deduplicated by TinyClaw. Downstream Agent Run or Command execution errors are reported asynchronously through Deliveries, not as synchronous `POST /api/messages` failures.

## 5. Message 与 Agent Session 规则

Message 入库后不再改所属关系。未触发消息仍保留在 room message log 中，可在后续触发时作为上下文。

Message 本身不记录全局 agent 可见性。命令、第三方只读镜像和其它非对话消息是否进入 Agent Run 上下文，由 Command handler、Trigger Policy 和 runner 上下文选择决定。

当 Message 命中某个 Agent Session 的 Trigger Policy：

1. 不创建 run 记录。
2. 更新该 Agent Session 的 `pending_trigger_message_id = message.id`。
3. execution loop 后续发现 `pending_trigger_message_id > caught_up_message_id` 后开始运行。

当同一 Agent Session 正在运行：

1. 新 Message 只追加到 room message log。
2. 如果新 Message 命中 trigger，只更新 `pending_trigger_message_id`。
3. 当前 run 完成后，下一轮 loop 会追到新的 `pending_trigger_message_id`。

这让“运行期间的新消息”成为下一次窗口，而不是当前 agent run 的隐式输入。

## 6. Agent Run 与 Delivery

Agent run 由 clawman 进程内 execution loop 启动，不设计外部 claim API。execution loop 抢占 Agent Session lock，读取 bounded recent messages 和 Room Memory，并调用 agent runner 追到 `pending_trigger_message_id`。

当前实现提供 `CodexRunner`：设置 `AGENT_RUNNER=codex` 后，execution module 会调用本机 `codex exec`，并把 runner 输出解析成 Agent Run Result。

第一次运行时，CodexRunner 使用 fresh `codex exec`，从 `thread.started.thread_id` 事件中提取 Codex CLI thread id 并保存到 `agent_sessions.codex_session_id`。同一 Agent Session 的后续运行使用 `codex exec resume <codex_session_id> -` 继续该 Codex CLI thread。若保存的 thread id 已失效，runner 会丢弃该 id 并 fresh 启动一次。

Fresh run 使用 `--output-schema` 约束 Agent Run Result。`codex exec resume` 当前不支持 `--output-schema`，因此 runner prompt 必须自描述 Agent Run Result JSON 形状。

Headless runner 默认通过 `CODEX_DISABLED_FEATURES=apps,tool_suggest,plugins` 关闭 Codex CLI 的交互式 app/plugin 发现路径，避免生产 pod 在不需要这些能力时阻塞在外部 connector 刷新上。当前 K8s 部署还通过 `hostAliases` 固定 `api.openai.com` 与 `chatgpt.com`，因为现有集群 DNS 会把这两个域名解析到错误 IP。

Agent Run Result 包含：

- user-visible final output
- Memory Write Proposals

Codex run 会收到 memory search endpoint 和短期 capability token。Codex 可以在运行中主动调用 Memory Search；Clawman 从 token 绑定 Room，不信任 agent 传入的 `room_id`。Memory Search backend 或 endpoint 短暂失败时，runner 将失败作为带 `error` 字段的 search result 回传给下一轮 Codex，不直接让 Agent Run 失败。

Agent final output 处理：

```text
non-empty output -> create delivery(status=pending)
empty output     -> no delivery
failure          -> create failure delivery(status=pending)
```

Agent Run 成功不会直接追加 agent Message。Message 只在 channel consumer 或后续平台回流通过 `POST /api/messages` 写入后出现。若 consumer 主动写回，建议使用 `source_message_id = "delivery:<delivery_id>"` 保证幂等；若等待外部平台 self echo，则由 Channel Adapter 使用平台消息 id 写入并按需关联 Delivery。

第一版 `/draw <prompt>` 作为 Clawman-owned Command 处理，不进入 Codex Agent Run，也不更新 Agent Session trigger boundary。Command 是有效 Room Message，但由 Command handler 消费。

第一版 Draw Command 采用轻量执行模型：`POST /api/messages` 只有在插入新 Message 且命中 `/draw` 时，才启动一个 in-process background goroutine 执行生图；重复 Message 不启动副作用。该版本不提供 crash recovery，clawman 重启会丢失正在执行的 `/draw`。

Draw Command 默认启用；显式设置 `DRAW_COMMAND_ENABLED=false` 时禁用。Image provider 默认使用 `IMAGE_PROVIDER_BASE_URL=https://code.v4.chat` 和 `IMAGE_PROVIDER_MODEL=gpt-image-2`。`IMAGE_PROVIDER_API_KEY` 优先；为空时第一版可从 `CODEX_AUTH_JSON.OPENAI_API_KEY` 兼容读取。

Clawman 直接调用 image provider 生成 Generated Media，并产生三条 Delivery：

```text
command_progress text -> "正在画图..."
command_output text   -> "图片已生成：<generated_media_id>"
command_output image  -> references <generated_media_id>
```

Generated Media 只用于当前 Room 的短期外发，不作为长期 Artifact。第一版不新增 `generated_media` 表；media metadata 持久化在现有 `deliveries.payload` 中。Delivery list 不内嵌图片 bytes；Clawman 将图片 bytes 写入 S3-compatible object storage，image Delivery 携带 24h presigned S3 URL：

```json
{
  "kind": "command_output",
  "type": "image",
  "media_id": "gm_20260522_7f3a9c",
  "media_url": "https://...",
  "media_url_kind": "presigned_s3",
  "mime_type": "image/png",
  "expires_at": "2026-05-23T03:00:00Z"
}
```

Channel Adapter 直接通过 `media_url` 下载 bytes 后执行真实平台发送。第一版不提供 Clawman media download proxy。

S3 object 清理由 bucket lifecycle 负责；Clawman 不跟踪 object lifecycle，也不支持 URL 过期后的重新签发。

WeCom 微盘可作为 WeCom Channel Adapter 的可选发送或归档策略，但不作为 Clawman Core 的 Generated Media 存储。

Memory Write Proposals 处理：

```text
valid proposal   -> enqueue memory_write_job(status=pending)
invalid proposal -> enqueue memory_write_job(status=rejected)
pending job      -> background worker applies Room Memory change
```

Memory Write Job 失败不阻止 Delivery 创建。

Rejected 和最终 failed 的 Memory Write Jobs 都会写 durable audit，并输出结构化日志，方便 operator 从运行日志和审计记录两侧定位失败 proposal。

Agent run 成功或失败后都推进 `agent_sessions.caught_up_message_id` 到本次 `pending_trigger_message_id`，并释放 lock。成功且 final output 非空时创建 Delivery，但不直接写 agent Message；失败时写日志并创建失败提示 Delivery。失败不自动重试，后续新触发消息会创建新的处理窗口。

## 7. Delivery Pull API

Adapter 轮询 clawman pending deliveries，并自行负责真实外发和短期重试。

MobileClaw 当前按该接口轮询 `deliveries`，可在一次请求中同时领取多个 channel 的 pending delivery。2026-05-20 真机验证中，`wecom` channel 已完成 `delivery -> 企业微信发送 -> ack`；`wechat` channel 需要在目标设备微信无障碍节点树为空时走坐标 fallback。

```http
GET /api/deliveries?channels=wecom,wechat
Authorization: Basic base64(client_id:client_secret)
```

clawman 根据 `deliveries.room_id -> rooms.channel` 过滤返回 pending delivery；客户端不维护本地游标。

成功发送后：

```http
POST /api/deliveries/{id}/ack
Authorization: Basic base64(client_id:client_secret)
```

ack 后 Delivery 保留，只更新状态，后续 pending 轮询不再返回。

第一版由 adapter 负责发送失败后的短期重试；clawman 不主动重试 failed Delivery。

Ack 表示 consumer 已成功处理该 outbound intent，例如第三方发送接口调用成功或 local UI 已接受展示。Ack 不要求已经写回 Message；Delivery-to-Message 关联是可选、best-effort 的 channel 策略。

## 8. Control Plane UI

第一版 Control Plane UI 作为独立前端工程放在同仓库内，源码位于 `web/control/`。前端使用 Vue 3 + Composition API、UnoCSS、pnpm。构建产物可由 clawman 同域服务在 `/admin/` 下提供，但源码和构建链保持独立，后续复杂化时可以单独部署。

Control Plane UI 使用独立的 `/admin/api/*` 管理接口，不复用 adapter-facing API 的 URL 形状。第一版鉴权使用同一套 API Client 模型，由 operator 在前端输入 `client_id` 和 `client_secret`；不引入用户系统、角色系统或长期浏览器会话。

Room list 是运维索引页，第一版聚合展示：

- `room.id`
- `channel`
- `channel_room_type`
- `display_name`
- `outbound_alias`
- default `agent_enabled`
- `pending_trigger_message_id`
- `caught_up_message_id`
- `pending_delivery_count`
- `last_message_time`
- `updated_at`

`codex_session_id` 仅在 Room Detail 的 debug 区显示，不进入 Room list。

第一版控制面以 Room Timeline 为主视图，而不是只提供 Message 表。Room Timeline 用于把同一个 Room 内的 Message、Agent Session 处理边界和 Delivery 放在一起排障：

```text
Rooms
  -> Room Timeline
     - Messages
     - Agent Session trigger and processed boundaries
     - Deliveries and ack state
  -> Room Settings
     - display_name
     - outbound_alias
     - agent_enabled
     - trigger_policy
  -> Room Memory
     - fact / preference / todo items
     - active / inactive / all filters
```

Room Timeline 第一版默认查询最近 N 条 Room 事件：

```http
GET /admin/api/rooms/{id}/timeline?limit=100
Authorization: Basic base64(client_id:client_secret)
```

向前翻页使用 `before_message_id`：

```http
GET /admin/api/rooms/{id}/timeline?limit=100&before_message_id=12345
Authorization: Basic base64(client_id:client_secret)
```

Timeline 按 Room Message ID 组织主轴；Delivery 通过 `source_message_from_id` / `source_message_to_id` 归入对应 Message 窗口。无法归入窗口的 Delivery 单独显示在当前页面末尾。第一版不优先提供按时间分页，避免 operator 在消息乱序或平台时间不可靠时看到不稳定结果。

第一版允许有限写操作：

- register/update Room
- enable/disable the default Agent Session
- update `display_name`, `outbound_alias`, and `trigger_policy`
- ack Delivery
- inject Message

第一版不提供删除 Room、删除 Message、直接修改 Message、直接修改 Room Memory。Room 是 Message、Delivery、Room Memory 和 Agent Session 的归属边界；删除会破坏历史排障和审计。需要停止处理时，优先停用 Agent Session；需要隐藏历史时，后续再单独设计 archive 语义。

Control Plane UI 可提供 `Inject Message` 调试操作。该操作写入的是 TinyClaw 入站 Message，不是向外部 Channel Room 发送消息，因此不得命名为 `send`。第一版由 admin API 生成 `source_message_id = admin:<uuid>`，operator 填写 `sender_id`、`sender_name` 和 text payload。默认允许触发 Agent Session，也可显式选择 `suppress_agent_trigger`。注入的 Message 必须进入 Room Timeline，作为可审计的 Room history。

Control Plane UI 第一版提供只读 Room Memory 查看能力。Room Memory tab 支持按 `status=active|inactive|all` 和 `type=fact|preference|todo` 过滤，展示 `key`、`content`、`status`、`source_message_from_id`、`source_message_to_id` 和 `updated_at`。第一版不允许新增、修改、关闭或删除 Memory Item，避免绕过 Memory Write Proposal 和 Memory Write Job 的治理链路。

## 9. 暂不覆盖

以下内容单独设计，不写入本文：

- clawman 内部 sandbox 模块
- sandbox lifecycle / runtime connection
- Tool Runtime Backend：后续优先按“clawman 持有 agent loop，sandbox 只执行工具调用”设计
- durable workflow engine：第一版先用 Agent Session trigger/checkpoint/lock 和 deliveries 承载恢复语义
- vector / hybrid memory ranking
- explicit send capability
- channel adapter 自身 cursor / offset / reconnect state
