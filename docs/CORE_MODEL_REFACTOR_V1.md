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
dispatch_state bigint not null default 0
created_at timestamptz not null

unique(room_id, source_message_id)
check(dispatch_state in (0, 1) or dispatch_state >= 1000)
```

`dispatch_state` 约定：

```text
0       waiting/context candidate
1       skipped
>=1000  real invocation id by convention
```

`dispatch_state` 不设外键。这是有意的应用层约定，见 [ADR-0001](./adr/0001-message-invocation-state-sentinel.md)。

### invocations

```text
id bigint primary key
room_id bigint not null references rooms(id)
status text not null
trigger_message_id bigint null references messages(id)
input_snapshot jsonb not null
output_snapshot jsonb null
created_at timestamptz not null
started_at timestamptz null
completed_at timestamptz null

check(id >= 1000)
check(status in ('queued', 'running', 'completed', 'failed', 'cancelled'))
```

Invocation id 从 `1000` 开始，避免与 `messages.dispatch_state` sentinel 冲突。

`input_snapshot` 记录 Invocation 创建时的初始结构化输入快照，包括初始 prompt、被选入的 messages、memory、附件和其他上下文。它不是从 `messages` 重新推导的缓存，而是本次 Invocation 的可复盘初始输入事实。Invocation active 期间追加的 Message 通过 `messages.dispatch_state = invocation.id` 关联，不回写这个初始快照。

`output_snapshot` 记录 agent 执行结果；第一版可只保存 final output 或失败提示。

Active Invocation：

```text
queued
running
```

Terminal Invocation：

```text
completed
failed
cancelled
```

数据库应保证同一 Room 只有一个 active Invocation：

```sql
CREATE UNIQUE INDEX uniq_active_invocation_per_room
ON invocations (room_id)
WHERE status IN ('queued', 'running');
```

`failed` 是终态；第一版不自动重试 failed Invocation。
Invocation 失败时，clawman 创建一条失败提示 Delivery，后续用户重新触发会创建新的 Invocation。

### deliveries

```text
id bigint primary key
seq bigint not null generated always as identity
room_id bigint not null references rooms(id)
invocation_id bigint not null references invocations(id)
payload jsonb not null
status text not null
created_at timestamptz not null
acked_at timestamptz null

check(status in ('pending', 'acked', 'failed'))
```

`deliveries.seq` 是 clawman 内部全局递增 outbox offset。Adapter 按 channel 过滤拉取时，seq 有空洞是正常现象。

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
6. 根据 dispatch 规则创建或追加 Invocation。

## 5. Dispatch 规则

Message 初始写入时：

```text
dispatch_state = 0
```

如果平台层判断该消息永远不应进入 agent invocation：

```text
dispatch_state = 1
```

未触发不等于 skipped。未触发但可作为上下文的消息保持 `dispatch_state = 0`。

当 Room 没有 active Invocation，且当前 Message 触发执行：

1. 创建 Invocation，状态为 `queued`。
2. 将同一 Room 中 `dispatch_state = 0` 的 Messages 绑定到该 Invocation：

```sql
UPDATE messages
SET dispatch_state = $invocation_id
WHERE room_id = $room_id
  AND dispatch_state = 0;
```

当 Room 已有 active Invocation：

1. 新 Message 如果不是 skipped，直接设置：

```text
dispatch_state = active_invocation.id
```

2. 第一版只做追加，不做中断。

## 6. Invocation 与 Delivery

Invocation 状态：

```text
queued -> running -> completed
queued -> failed
running -> failed
queued/running -> cancelled
```

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
GET /api/deliveries?channel=wecom&seq=123
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
- sandbox lifecycle / claim / runtime connection
- Tool Runtime Backend：后续优先按“clawman 持有 agent loop，sandbox 只执行工具调用”设计
- durable workflow engine：第一版先用 `invocations.status`、`input_snapshot`、`output_snapshot` 和 `dispatch_state` 承载恢复语义
- memory capability
- explicit send capability
- 同一 Room 多个独立 Agent Session
- channel adapter 自身 cursor / offset / reconnect state
