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
- 生成 Delivery outbox
- 提供 adapter-facing HTTP API

## 2. 核心概念

### Room

`Room` 是 TinyClaw 内部会话容器。第一版中，一个外部 `Channel Room` 严格映射到一个 `Room`。

`rooms.channel` 和 `rooms.channel_room_id` 第一版不可变；如果外部绑定变化，创建新的 Room。

Room 必须先注册，后续 Message 才能写入。Room 不再作为 Message 入站的副作用自动创建。

Room 重新注册时允许更新 `display_name`、`outbound_alias`，并更新请求指定的 Agent Session 的 `agent_enabled`、`trigger_policy`。`channel`、`channel_room_id`、`channel_room_type` 是 Room 的外部身份，不允许通过更新改变。

### Message

`Message` 是 TinyClaw 内部入站事实，属于一个 Room。

Message 不再使用企业微信 archive `seq` 作为身份。第三方平台消息身份由 adapter 提供 `source_message_id`，用于幂等插入。

### Agent Session

`Agent Session` 表示一个 agent 在一个 Room 内的长期状态。同一个 Room 可以有多个 Agent Session，每个 Agent Session 独立保存 agent 配置、触发规则和处理进度。

### Delivery

`Delivery` 是 agent run 产生的外发项。agent run 完成只表示 agent 执行完成，不要求 Delivery 已发送成功。

第一版 agent final output 非空时，clawman 自动创建一条 Delivery；final output 为空时，只推进 Agent Session 的处理边界，不产生 Delivery。

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
sender_id text not null
sender_name text null
payload jsonb not null
message_time timestamptz not null
skipped boolean not null default false
created_at timestamptz not null

unique(room_id, source_message_id)
```

Message 是 append-only 入站事实。`skipped = true` 表示该消息永远不应进入 agent 上下文。

### agent_sessions

```text
id bigint primary key
room_id bigint not null references rooms(id)
agent_key text not null
enabled boolean not null
trigger_policy jsonb null
trigger_message_id bigint null references messages(id)
last_processed_message_id bigint not null
lock_owner text null
lock_expires_at timestamptz null
created_at timestamptz not null
updated_at timestamptz not null

unique(room_id, agent_key)
```

`trigger_message_id` 是该 Agent Session 当前需要处理的最新触发消息。入站 Message 命中该 Agent Session 的 Trigger Policy 时，更新该字段。

`last_processed_message_id` 是该 Agent Session 已成功处理到的 Room Message 边界。不同 Agent Session 可以独立处理同一个 Room 的消息流。

`trigger_message_id` 同时是触发信号和本次处理窗口右边界。Agent run 读取 `id > agent_sessions.last_processed_message_id AND id <= agent_sessions.trigger_message_id` 且 `skipped = false` 的 messages。

`lock_owner` 和 `lock_expires_at` 是执行 loop 的短租约，用于限制同一 Agent Session 同一时间只有一个 agent run。

Agent execution loop 扫描 `trigger_message_id > last_processed_message_id` 且未被有效 lock 持有的 Agent Session，抢占 lock 后开始 agent run。入站 Message 写入路径只负责保存 Message 和更新匹配 Agent Session 的 `trigger_message_id`，不直接启动 agent run。

一次 agent run 的处理窗口包含 `last_processed_message_id < message.id <= trigger_message_id` 内所有非 skipped Messages，而不是只包含触发消息。

agent run 成功或失败后都推进 `last_processed_message_id` 到本次窗口的 `trigger_message_id` 并释放 lock。失败时写日志并创建失败提示 Delivery；失败不自动重试。

### deliveries

```text
id bigint primary key
room_id bigint not null references rooms(id)
agent_session_id bigint not null references agent_sessions(id)
source_message_after_id bigint not null
source_message_until_id bigint not null
payload jsonb not null
status smallint not null
created_at timestamptz not null
acked_at timestamptz null
```

Adapter 使用 `deliveries.id` 作为轮询游标。

Delivery 不冗余保存 `channel` / `channel_room_id`；外发时通过 `room_id` join `rooms`。

`source_message_after_id` 和 `source_message_until_id` 记录产生该 Delivery 的 Message 窗口，语义等同于 `message.id > source_message_after_id AND message.id <= source_message_until_id`。

## 4. Room And Message APIs

Adapter 先注册或更新 Room，再向已注册 Room 写入标准化 Message。旧 `POST /api/inbound` 不保留；Room 生命周期和 Message 生命周期分开。

```http
POST /api/rooms
Authorization: Bearer <CLAWMAN_API_TOKEN>
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
Authorization: Bearer <CLAWMAN_API_TOKEN>
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
6. 对 Room 下 enabled 的 Agent Sessions 分别应用 Trigger Policy；为空时使用 channel default rule。
7. 命中 trigger 时更新对应 Agent Session 的 `trigger_message_id`。

## 5. Message 与 Agent Session 规则

Message 入库后不再改所属关系。未触发消息仍保留在 room message log 中，可在后续触发时作为上下文。

如果平台层判断该消息永远不应进入 agent 上下文，写入 `skipped = true`。

当 Message 命中某个 Agent Session 的 Trigger Policy：

1. 不创建 run 记录。
2. 更新该 Agent Session 的 `trigger_message_id = message.id`。
3. execution loop 后续发现 `trigger_message_id > last_processed_message_id` 后开始运行。

当同一 Agent Session 正在运行：

1. 新 Message 只追加到 room message log。
2. 如果新 Message 命中 trigger，只更新 `trigger_message_id`。
3. 当前 run 完成后，下一轮 loop 会处理 `last_processed_message_id < id <= trigger_message_id` 的剩余窗口。

这让“运行期间的新消息”成为下一次窗口，而不是当前 agent run 的隐式输入。

## 6. Agent Run 与 Delivery

Agent run 由 clawman 进程内 execution loop 启动，不设计外部 claim API。execution loop 抢占 Agent Session lock，读取 `last_processed_message_id < id <= trigger_message_id` 的初始上下文，并调用 agent runner。

当前实现提供 `CodexRunner`：设置 `AGENT_RUNNER=codex` 后，execution module 会调用本机 `codex exec`，并把 runner final output 写成 Delivery。

Agent final output 处理：

```text
non-empty output -> create delivery(status=pending)
empty output     -> no delivery
failure          -> create failure delivery(status=pending)
```

Agent run 成功或失败后都推进 `agent_sessions.last_processed_message_id` 到本次窗口的 `trigger_message_id`，并释放 lock。成功且 final output 非空时创建 Delivery；失败时写日志并创建失败提示 Delivery。失败不自动重试，后续新触发消息会创建新的处理窗口。

## 7. Delivery Pull API

Adapter 轮询 clawman deliveries，并自行负责真实外发和短期重试。

MobileClaw 当前按该接口轮询 `deliveries`。2026-05-20 真机验证中，`wecom` channel 已完成 `delivery -> 企业微信发送 -> ack`；`wechat` channel 因目标设备微信无障碍节点树为空，暂未完成自动发送。

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
- durable workflow engine：第一版先用 Agent Session trigger/checkpoint/lock 和 deliveries 承载恢复语义
- memory capability
- explicit send capability
- channel adapter 自身 cursor / offset / reconnect state
