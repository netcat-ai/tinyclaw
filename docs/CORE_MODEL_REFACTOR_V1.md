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
- 接收标准化 inbound message
- 自动创建 Room
- 保存 Message
- 应用 Trigger Policy
- 创建和推进 Invocation
- 生成 Delivery outbox
- 提供 adapter-facing HTTP API

## 2. 核心概念

### Room

`Room` 是 TinyClaw 内部会话容器。第一版中，一个外部 `Channel Room` 严格映射到一个 `Room`。

`rooms.channel` 和 `rooms.channel_room_id` 第一版不可变；如果外部绑定变化，创建新的 Room。

### Message

`Message` 是 TinyClaw 内部入站事实，属于一个 Room。

Message 不再使用企业微信 archive `seq` 作为身份。第三方平台消息身份由 adapter 提供 `source_message_id`，用于幂等插入。

### Invocation

`Invocation` 表示一次 agent 执行。第一版中每个 Room 只有一个隐式默认 Agent Session，因此 Invocation 直接挂在 Room 上。

同一 Room 同一时间只允许一个 active Invocation。

### Delivery

`Delivery` 是 Invocation 产生的外发项。Invocation 完成只表示 agent 执行完成，不要求 Delivery 已发送成功。

第一版 agent final output 非空时，clawman 自动创建一条 Delivery；final output 为空时，Invocation 可完成且不产生 Delivery。

## 3. 表模型

### rooms

```text
id bigint primary key
tenant_id text not null
channel text not null
channel_room_id text not null
channel_room_type text not null
display_name text null
trigger_policy jsonb null
created_at timestamptz not null
updated_at timestamptz not null

unique(tenant_id, channel, channel_room_id)
```

第一版 `tenant_id = default`。后续可由 API token 绑定 tenant。

`trigger_policy = null` 表示使用 channel default trigger rule。

### messages

```text
id bigint primary key
room_id bigint not null references rooms(id)
source_message_id text not null
sender_id text not null
sender_name text null
payload jsonb not null
message_time timestamptz not null
skipped boolean not null default false
created_at timestamptz not null

unique(room_id, source_message_id)
```

Message 是 append-only 入站事实，不记录属于哪个 Invocation。`skipped = true` 表示该消息永远不应进入 agent 上下文。

### invocations

```text
id bigint primary key
room_id bigint not null references rooms(id)
status smallint not null
trigger_message_id bigint null references messages(id)
start_message_id bigint null references messages(id)
last_seen_message_id bigint null references messages(id)
error_detail text null
created_at timestamptz not null
started_at timestamptz null
completed_at timestamptz null
```

Invocation id 从 `1000` 开始，作为内部执行记录的可读约定。

`start_message_id` 是 executor 开始执行时看到的 room 消息高水位。初始 prompt 读取 `id <= start_message_id` 且 `skipped = false` 的 messages。

`last_seen_message_id` 是 running agent 读取新消息的游标。agent 可以显式读取 `id > last_seen_message_id` 的 messages，读取成功后推进该游标。

第一版不存 `input_snapshot` / `output_snapshot`。失败原因记录在 `error_detail`；真实外发内容记录在 `deliveries.payload`。

Active Invocation：

```text
0 queued
1 running
```

Terminal Invocation：

```text
2 completed
3 failed
4 cancelled
```

第一版暂不在数据库层限制同一 Room 的 active Invocation 数量；执行侧先按应用逻辑选择最新 active Invocation。

`failed` 是终态；第一版不自动重试 failed Invocation。
Invocation 失败时，clawman 创建一条失败提示 Delivery，后续用户重新触发会创建新的 Invocation。

### deliveries

```text
id bigint primary key
room_id bigint not null references rooms(id)
invocation_id bigint not null references invocations(id)
payload jsonb not null
status smallint not null
created_at timestamptz not null
acked_at timestamptz null
```

Adapter 使用 `deliveries.id` 作为轮询游标。

Delivery 不冗余保存 `channel` / `channel_room_id`；外发时通过 `room_id` join `rooms`。

## 4. Inbound API

Adapter 调用 clawman 写入标准化消息。

```http
POST /api/inbound
Authorization: Bearer <CLAWMAN_API_TOKEN>
```

最小请求：

```json
{
  "channel": "wecom",
  "channel_room_id": "room-123",
  "channel_room_type": "group",
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
2. 按 `(tenant_id, channel, channel_room_id)` upsert Room。
3. 按 `(room_id, source_message_id)` 幂等插入 Message。
4. 如果 Message 已存在，返回已有结果，不重复触发。
5. 应用 Room Trigger Policy；为空时使用 channel default rule。
6. 根据 trigger 规则创建 Invocation，或让 active Invocation 后续通过消息游标读取新消息。

## 5. Message 与 Invocation 规则

Message 入库后不再改所属关系。未触发消息仍保留在 room message log 中，可在后续触发时作为上下文。

如果平台层判断该消息永远不应进入 agent invocation，写入 `skipped = true`。

当 Room 没有 active Invocation，且当前 Message 触发执行：

1. 创建 Invocation，状态为 `queued`。
2. 不更新历史 Message。
3. scheduler start 时设置 `start_message_id = last_seen_message_id = 当前 room 最新非 skipped message id`。
4. 初始 prompt 读取 `id <= start_message_id` 的非 skipped messages。

当 Room 已有 active Invocation：

1. 新 Message 只追加到 room message log。
2. 不创建新的 Invocation。
3. running agent 如需查看用户补充消息，显式读取 `id > last_seen_message_id` 的非 skipped messages，并推进 `last_seen_message_id`。

这让“运行中看到新消息”成为 agent 行为，而不是 storage 层隐式改写执行输入。

## 6. Invocation 与 Delivery

Invocation 状态：

```text
queued -> running -> completed
queued -> failed
running -> failed
queued/running -> cancelled
```

Invocation 创建后由 clawman 进程内 scheduler 启动执行，不设计外部 claim API。scheduler 将 `queued` 推进到 `running`，调用当前配置的 agent runner；runner 成功时完成 Invocation，失败时标记 failed 并生成失败 Delivery。

当前代码保留 `POST /api/invocations/{id}/complete` 和 `POST /api/invocations/{id}/fail` 作为 Core Model 状态推进接口；主流程优先进程内调用同一层 storage 方法。

Agent final output 处理：

```text
non-empty output -> create delivery(status=pending)
empty output     -> no delivery
failure          -> create failure delivery(status=pending)
```

`invocation.completed` 不等待 Delivery 发送成功。

## 7. Delivery Pull API

Adapter 轮询 clawman deliveries，并自行负责真实外发和短期重试。

```http
GET /api/deliveries?channel=wecom&id=123
Authorization: Bearer <CLAWMAN_API_TOKEN>
```

clawman 根据 `deliveries.room_id -> rooms.channel` 过滤返回。

成功发送后：

```http
POST /api/deliveries/{id}/ack
Authorization: Bearer <CLAWMAN_API_TOKEN>
```

ack 后 Delivery 保留，只更新状态。

第一版由 adapter 负责发送失败后的短期重试；clawman 不主动重试 failed Delivery。

## 8. 暂不覆盖

以下内容单独设计，不写入本文：

- clawman 内部 sandbox 模块
- sandbox lifecycle / runtime connection
- Tool Runtime Backend：后续优先按“clawman 持有 agent loop，sandbox 只执行工具调用”设计
- durable workflow engine：第一版先用 `invocations.status`、`start_message_id`、`last_seen_message_id` 和 deliveries 承载恢复语义
- memory capability
- explicit send capability
- 同一 Room 多个独立 Agent Session
- channel adapter 自身 cursor / offset / reconnect state
