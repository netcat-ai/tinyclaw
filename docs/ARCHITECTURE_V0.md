# tinyclaw Architecture v0

## 1. 目标与边界

### 1.1 项目目标
tinyclaw 是一个面向企业微信会话的 AI Agent Runtime：
- 员工在企业微信私聊/群聊中与 Agent 交互。
- 每个会话（私聊/群聊）映射到独立 sandbox。
- 主服务负责消息拉取、标准化、会话路由、sandbox 唤醒与回发。
- sandbox 内 agent 负责模型推理、工具执行和会话内工作目录管理。

### 1.2 v0 非目标
- 不训练基础模型，不自建模型服务。
- 不做复杂多 agent 协作。
- 不做跨租户共享记忆。
- 不在 v0 引入 warm pool / snapshot / hibernate 自动恢复。

## 2. 架构总览

```text
+---------------------------+       +----------------------------------+
| WeCom Session Archive API |-----> | Ingress Service (clawman)        |
| WeCom Send API            | <-----| - decrypt / normalize            |
+---------------------------+       | - manage sandbox via Go SDK      |
                                    | - invoke /agent via SDK Run      |
                                    | - persist messages/outbox        |
                                    +----------------+-----------------+
                                                     |
                                   +-----------------v------------------+
                                   | PostgreSQL                           |
                                   | - ingest_cursors                     |
                                   | - messages                           |
                                   | - outbox_deliveries                  |
                                   +-----------------+------------------+
                                                     |
                                   +-----------------v------------------+
                                   | Egress Consumer                     |
                                   | - poll outbox_deliveries            |
                                   | - WorkTool / WeCom send             |
                                   | - retry / failed                    |
                                   +-------------------------------------+

                                                     |
                                                     v
                              +-------------------------------------------+
                              | agent-sandbox extensions                  |
                              | SandboxTemplate + SandboxClaim            |
                              +-------------------+-----------------------+
                                                  |
                                                  v
                              +-------------------------------------------+
                              | Sandbox Pod per room                      |
                              | - tinyclaw agent HTTP server              |
                              | - /healthz                                |
                              | - /agent                                  |
                              | - /execute /upload /download /list /exists|
                              | - claude_agent_sdk / echo runtime         |
                              +-------------------------------------------+
```

## 3. room 与隔离模型

### 3.1 room_id 规则

```text
room_id = {roomid_or_from}
```

- 群聊：`room_id = roomid`
- 私聊：`room_id = from`
- `tenant_id` 与 `chat_type` 作为独立字段保留，用于审计、统计和权限判断。

### 3.2 隔离策略
- 控制面隔离：每个活跃 `room_id` 在当前进程内持有一个对应的 SDK sandbox handle。
- 运行时隔离：每个 room 一个 sandbox pod。
- 文件系统隔离：每个 sandbox 拥有独立 `/workspace`。
- 网络隔离：由 agent-sandbox 模板和网络策略控制，默认按最小权限配置。

## 4. 控制面设计

### 4.1 官方资源模型
- `SandboxTemplate`
  - 描述统一的 agent 镜像、端口、探针和运行时环境。
- `SandboxClaim`
  - 由官方 Go SDK 在 `Open()` 时创建并等待 ready。
  - 当前 SDK 不支持自定义 claim 名称，claim identity 由 SDK 内部生成并保存在进程内 handle。
### 4.2 生命周期约定

```text
room_id -> process-local SDK client
SDK client.Open() -> create SandboxClaim -> wait Sandbox ready
```

说明：
- 当前版的 room 复用范围是单进程生命周期。
- 如果主服务重启，官方 Go SDK 当前不会自动找回旧 claim；后续需评估稳定复用策略。

## 5. 数据面设计

### 5.1 Ingress 流程
1. `clawman` 每 3 秒执行一次 archive pull，当前固定调用 `GetChatData(seq, 100)`。
2. 对每条 archive item 执行解密、JSON 解析与字段校验。
3. 若消息发送方满足 `from == WECOM_BOT_ID`，直接跳过，不进入后续详情解析、sandbox 调用和落库流程。
4. 根据 `room_id = {roomid_or_from}` 标准化会话标识，先解析发送者身份并拿到昵称，再写入一条 inbound `messages(status=received)`。
5. 私聊消息默认直接触发处理；群聊消息仅在命中 `WECOM_GROUP_TRIGGER_MENTIONS` 的 `@` 提及，或命中 `WECOM_GROUP_TRIGGER_KEYWORDS` 时才触发处理。
6. 未触发的群聊消息仅推进 archive cursor，不调用 sandbox；它们继续保留在 PostgreSQL，等待后续触发消息一并带入上下文。
7. 触发处理时，先按 `tenant_id + room_id` 读取所有 `status=received` 的 inbound 消息，作为当前 room 尚未处理的上下文窗口。
8. 触发阶段只补充解析群详情；发送方身份与昵称已经在入库前解析，并在进程内使用短期 TTL cache 降低企业微信详情接口压力。群详情接口按发送者类型选择：外部联系人走客户群接口，员工走内部群接口。
9. 通过官方 Go SDK `Open()` 确保该 room 的 sandbox session ready；进程内 room session cache 遇到 `ErrOrphanedClaim` 时会先 `Close()` 再重建。
10. 通过官方 Go SDK `Run()` 在 sandbox 内部桥接调用本机 `POST /agent`。
11. agent 成功返回后，在同一事务中写入 outbound `messages`、写入 `outbox_deliveries`，并把本轮参与处理的 inbound 消息标记为 `completed`。
12. PostgreSQL 事务成功后，才更新 `ingest_cursors.wecom_archive.cursor`。
13. egress consumer 轮询 outbox 并统一回发企业微信。

### 5.2 Router 调用契约
当前主服务不再直接从业务层拼接 router 请求头，而是让官方 Go SDK 通过 direct-url 模式连接 `sandbox-router`，并通过 `/execute` 在 sandbox 内部调用：

```text
POST http://127.0.0.1:{AGENT_SERVER_PORT}/agent
```

请求体最小集：

```json
{
  "query": "用户消息正文",
  "msgid": "wecom_msg_abc",
  "room_id": "chat_123",
  "tenant_id": "corp_id",
  "chat_type": "group"
}
```

返回体最小集：

```json
{
  "stdout": "agent reply",
  "stderr": "",
  "exit_code": 0
}
```

### 5.3 PostgreSQL 职责
v0 中主服务只依赖最小 PostgreSQL 事实源：
- `ingest_cursors`：企业微信拉取游标
- `messages`：入站事实、待处理状态、出站消息
- `outbox_deliveries`：待发送、重试中、已发送、失败的回发任务

补充语义：
- inbound `messages` 会先以 `status=received` 写入；当该 room 发生一次有效触发并成功产出回复后，这批 inbound 会被更新为 `status=completed`。
- 群聊未触发的消息保持 `status=received`，用于后续触发时补齐上下文。
- `ingest_cursors` 只在当前 archive item 的入库与必要的状态迁移完成后推进。
- 因此，只要消息在 sandbox 打开、agent 执行或事务提交前失败，后续轮询仍可基于已持久化的 `received` 消息继续重放，而不会丢失上下文。

## 6. Agent Runtime 设计

### 6.1 运行模式
- `echo`
  - 用于联调和基础测试。
- `claude_agent_sdk`
  - 用于真实 Claude 执行。

### 6.2 入口契约
- HTTP:
  - `GET /healthz`
  - `POST /agent`
  - `POST /execute`
  - `POST /upload`
  - `GET /download/{path}`
  - `GET /list/{path}`
  - `GET /exists/{path}`
- 运行目录：
  - `AGENT_WORKDIR`
  - `AGENT_TMPDIR`
- Claude 凭据：
  - `ANTHROPIC_API_KEY` 或 `CLAUDE_CODE_OAUTH_TOKEN`
- Claude system prompt：
  - `CLAUDE_SYSTEM_PROMPT_APPEND`

### 6.3 进程模型

```text
PID 1: tini / entrypoint
  └─ node dist/main.js
```

约束：
- 不在镜像内运行 supervisor。
- 失败由 sandbox / K8s 拉起。
- 空闲时保持 HTTP server 存活，由控制面决定是否回收。

## 7. 主服务职责

### 7.1 Ingress
- 拉取企业微信消息。
- 解密与标准化。
- 解析用户和群详情并缓存。
- 调用 sandbox。

### 7.2 Sandbox Orchestrator
- 通过官方 Go SDK 创建 room 级 sandbox session。
- 通过 SDK `Open()` 等待 claim ready。
- 不单独维护 room registry 表。
- 当前用进程内 room session cache 复用活跃 sandbox。

### 7.3 Egress
- 轮询 `outbox_deliveries`。
- 统一回发企业微信。
- 失败重试，超过阈值后标记 `failed`。

当前实现细节：
- 轮询间隔：500ms
- 发送租约：1 分钟
- 最大重试次数：5
- 领取状态：`pending` / `retry` / `sending`
- 成功后标记：`sent`

## 8. 失败处理
- `SandboxClaim` 创建失败：
  - 当前触发消息已经写入 inbound，但主服务不推进 `ingest_cursors.cursor`；下一轮会复用同一批 `received` 消息重试。
- sandbox 未 ready：
  - 视为当前触发消息处理失败，不推进 cursor。
- sandbox 返回 5xx：
  - 当前触发消息保留重试机会，不推进 cursor。
- 企业微信详情解析失败：
  - 当前消息已在 inbound 落库，但不会推进 cursor。
- bot 自发消息：
  - 在 ingress 侧直接跳过，也不会推进 cursor；后续 archive 若再次拉到该条消息，仍会再次命中 skip 分支。
- egress 回发失败：
  - 保留在 `outbox_deliveries` 中重试，超过阈值后标记 `failed`。

现网含义：
- 如果 `clawman` 长时间停机，或者某个阶段持续失败，`wecom_archive` cursor 会停在旧位置。
- 服务恢复后，会从旧 cursor 继续回放历史 archive 消息；如果这些旧消息当时已经写入 inbound 但尚未完成处理，就会复用同一批 `received` 消息重试，而不是重复插入 inbound。

## 9. 可观测性
核心指标建议：
- `sandbox_claim_ready_latency_ms`
- `sandbox_invoke_latency_ms`
- `sandbox_http_error_rate`
- `reply_e2e_ms`
- `egress_retry_count`

## 10. 结论
v0 方案的重点已经从“Redis 驱动 sandbox 自拉”切换为“官方 agent-sandbox Go SDK + router/direct-url + PostgreSQL 最小事实源”：
- 控制面更贴近官方演进方向。
- 模板复用能力更强。
- sandbox 不再依赖 per-room Redis 凭据和 consumer group。
- 主服务职责更清晰，后续接 warm pool、snapshot、router gateway 都更顺。
