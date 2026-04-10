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
- 不让 sandbox 直接持有企业微信等外部渠道协议状态。

## 2. 架构总览

```text
+---------------------------+       +----------------------------------+
| WeCom Session Archive API |-----> | Ingress Service (clawman)        |
| Android WeCom Sender App  |<----->| - persist archive into messages  |
+---------------------------+       | - scan pending rooms             |
                                    | - manage SandboxClaim lifecycle  |
                                    | - gRPC message gateway server    |
                                    | - persist jobs outbox            |
                                    | - control API / metrics          |
                                    +----------------+-----------------+
                                                     |
                                                     v
                                    +----------------------------------+
                                    | PostgreSQL                       |
                                    | - messages                       |
                                    | - rooms                          |
                                    | - jobs                           |
                                    | - wecom_app_clients              |
                                    +----------------------------------+

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
                              | - tinyclaw agent /healthz                 |
                              | - gRPC client bridge                      |
                              | - claude_agent_sdk / echo runtime         |
                              | - workspace                               |
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
- 控制面隔离：每个 `room_id` 对应一个确定性的 `SandboxClaim.name`。
- 运行时隔离：每个 room 一个 sandbox pod。
- 文件系统隔离：每个 sandbox 拥有独立 `/workspace`。
- 网络隔离：由 `SandboxTemplate` 和平台网络策略控制，默认按最小权限配置。

## 4. 控制面设计

### 4.1 官方资源模型
- `SandboxTemplate`
  - 描述统一 agent 镜像、端口、探针和运行时环境。
- `SandboxClaim`
  - 由 `clawman` 直接创建或复用。
  - 当前 claim 名称由 `room_id` 的稳定哈希推导，claim 上写入 `tinyclaw/room-id` 注解。

### 4.2 生命周期约定

```text
room_id -> deterministic SandboxClaim.name
Ensure claim exists -> wait Ready=True -> wait sandbox gRPC connect
```

说明：
- 当前版的 room 级复用仍依赖单进程内 `room_id -> claimName` cache。
- 因为 claim 名称稳定，服务异常退出后理论上可重新连接已有 claim；但当前 graceful shutdown 会主动删除本进程创建过的 claims。

## 5. 数据面设计

### 5.1 Ingress 与 dispatch 流程
1. `clawman` 运行两个独立协程：ingest worker 每 3 秒执行一次 archive pull，dispatch worker 每秒扫描一次 `messages(status=pending)`。
2. ingest worker 先查询当前 tenant 的 `MAX(messages.seq)`，再调用 `GetChatData(seq, 100)`；`messages.seq` 是唯一拉取 checkpoint。
3. 对每条 archive item 按 `seq` 升序执行解密、JSON 解析与字段校验，并把完整解密结果写入 `messages.payload`。
4. 所有 archive item 都必须先落库：
   - bot/self、冷启动窗口外的历史消息、非法 payload 写成 `status=ignored`
   - 群聊未触发消息写成 `status=buffered`
   - 私聊消息和群聊触发消息写成 `status=pending`
5. 若 `DecryptData` 失败，视为致命 ingest 错误，当前 worker 直接返回错误并退出服务。
6. 若当前消息是群聊触发消息，ingest 会在同一个事务中把该 `room_id` 下已有 `buffered` 消息一并提升为 `pending`。
7. 冷启动且 `messages` 为空时，只允许最近 10 分钟内的消息进入 `pending/buffered`；更早 archive backlog 统一写入 `ignored`。
8. dispatch 阶段按 `room_id` 聚合，同一个 room 在同一轮只触发一次 agent 调用。
9. 触发处理时，先按 `tenant_id + room_id` 读取所有 `status=pending` 的消息，作为当前 room 尚未处理的结构化上下文窗口。
10. 触发阶段按需补充解析群详情；发送方昵称在 ingest 阶段 best-effort 写入 `from_name`，失败不阻塞入库。
11. dispatch 在启动 sandbox 前确保 `rooms(tenant_id, room_id)` 元数据存在。
12. `sandbox.Orchestrator` 确保对应 claim 存在并 ready；随后等待该 room 的 sandbox gRPC 连接到 `clawman`。
13. `clawman` 通过 gRPC 把当前批次 `messages[]` 下发给 sandbox，并在下发成功后把这批消息从 `pending` 标记为 `sent`。
14. sandbox 返回 `result` 时，主服务写入 `jobs`；只有 `jobs` 写入成功后，这批消息才从 `sent` 更新为 `done`。
15. 若 sandbox 返回 `error`、等待超时或 `jobs` 写入失败，主服务会把这批消息从 `sent` 恢复为 `pending`。
16. 服务启动时执行 `ResetSentMessages()`，把上一次运行残留的 `sent` 统一恢复为 `pending`。

### 5.2 `clawman <-> sandbox` gRPC 契约
当前最小协议定义在 `proto/clawman/v1/clawman.proto`：

```proto
message Message {
  string kind = 1;
  string sandbox_id = 2;
  string room_id = 3;
  string request_id = 4;
  repeated AgentMessage messages = 5;
  string output = 6;
  string error = 7;
}

service Clawman {
  rpc RoomChat(stream Message) returns (stream Message);
}
```

当前约定：
- sandbox 建连后的第一条消息必须是 `kind=connect`，并携带 `sandbox_id`。
- `room_id` 可以由 sandbox 显式声明，也可以由 `clawman` 通过 `sandbox_id -> claim annotation` 反查。
- `clawman -> sandbox` 当前只发送 `kind=messages`。
- `sandbox -> clawman` 当前只返回：
  - `kind=result`
  - `kind=error`

### 5.3 PostgreSQL 职责
v0 中主服务当前依赖以下 PostgreSQL 结构：
- `messages`：企业微信 archive 入站事实、状态机、`seq` checkpoint
- `rooms`：已进入 agent 生命周期的 room 元数据
- `jobs`：按 `bot_id` 分队列、发给 Android 无障碍发送端的外发任务队列
- `wecom_app_clients`：Android 发送端拉取认证配置，以及 `client_id -> bot_id` 的绑定

补充语义：
- `messages` 的流程状态固定为 `ignored / buffered / pending / sent / done`。
- `rooms` 只在 room 首次进入 dispatch 时创建；它不是 transcript 表，也不承载原始消息事实。
- `messages.seq` 单调递增；系统通过 `MAX(messages.seq)` 推导下一次 archive pull 的起点。
- 群聊未触发消息保持 `status=buffered`，用于后续触发时补齐上下文。
- `jobs.seq` 单调递增；Android 发送端通过 `GET /api/wecom/jobs?seq=<last_seq>` 拉取当前 `bot_id` 的增量任务。
- Android 发送端通过 HTTP Basic 认证访问 control API；服务端先依据 `wecom_app_clients(client_id, client_secret)` 校验，再解析该 `client_id` 绑定的 `bot_id` 过滤 `jobs`。

### 5.4 当前代码中的消息批次语义
截至 2026-04-10，仓库中没有独立的业务轮次实体；一次处理由 `clawman` 在 dispatch 阶段按 room 组装当前 `pending` 消息批次：

1. ingest 先把 archive item 写入 `messages`，状态为 `ignored / buffered / pending`。
2. dispatch 按 `room_id` 列出所有 pending room，并读取该 room 下当前全部 `messages(status=pending)`。
3. 这批按 `seq` 排序的 pending 消息会被一次性组装成单个 `Message{kind=messages, messages[]}`，并通过 gRPC 下发给 sandbox。
4. gRPC 下发成功后，这批消息会整体从 `pending` 更新为 `sent`。
5. 只有当 sandbox 返回 `result` 且 reply 成功写入 `jobs` 时，这批消息才会继续从 `sent` 更新为 `done`。
6. 如果 sandbox 返回 `error`、等待结果超时或 `jobs` 写入失败，主服务会把这批 `sent` 消息恢复为 `pending`，由下一轮 dispatch 重新投递。
7. 如果 `jobs` 已成功写入但 `messages.done` 更新失败，这批消息会停在 `sent`，并在下次服务启动时被 `ResetSentMessages()` 恢复到 `pending`。

这意味着当前实现里：
- 处理边界是“当前时刻该 room 下全部 pending 消息”，而不是显式持久化的业务实体。
- 请求边界仍然不稳定；重试时会重新基于当时的 pending 集合组装请求。
- 当前只有最小 `result/error` 语义；中断、取消、增量流式输出等 richer 事件尚未进入主状态机。

## 6. Agent Runtime 设计

### 6.1 运行模式
- `echo`
  - 用于联调和基础测试。
- `claude_agent_sdk`
  - 用于真实 Claude 执行。

### 6.2 入口与连接契约
- HTTP：
  - `GET /healthz`
- gRPC：
  - 作为 client 主动连接 `CLAWMAN_GRPC_ADDR`
  - 使用 `RoomChat(stream Message)` 持续接收 `messages[]` 批次
- 运行目录：
  - `AGENT_WORKDIR`
  - `AGENT_TMPDIR`
- Claude 凭据：
  - `ANTHROPIC_API_KEY` 或 `CLAUDE_CODE_OAUTH_TOKEN`
- Claude system prompt：
  - `CLAUDE_SYSTEM_PROMPT_APPEND`

说明：
- 当前 agent runtime 不再暴露 `POST /agent`。
- 当前 room 级上下文通过 gRPC `messages[]` 传入，而不是通过 HTTP JSON 请求体传入。

### 6.3 进程模型

```text
PID 1: tini / entrypoint
  └─ node dist/main.js
```

约束：
- 不在镜像内运行 supervisor。
- 失败由 sandbox / K8s 拉起。
- 空闲时保持 `/healthz` server 与 gRPC bridge 进程存活，由控制面决定是否回收。

## 7. 主服务职责

### 7.1 Ingress
- 拉取企业微信消息。
- 解密与标准化。
- 解析用户和群详情并缓存。
- 驱动 room 级 dispatch。

### 7.2 Sandbox Orchestrator
- 为每个 `room_id` 推导确定性的 `SandboxClaim.name`。
- 创建或复用 `SandboxClaim`，并等待 `Ready=True`。
- 维护最小 `rooms` 表，用于保存平台级 room 元数据。
- 当前用进程内 room session cache 复用活跃 room 的 claim 信息。

### 7.3 Reply Delivery
- 在 `dispatchRoom` 成功拿到 agent reply 后，把结果写入 PostgreSQL `jobs` outbox。
- 只有 `jobs` 写入成功后，当前这批 `messages` 才会从 `sent` 更新到 `done`。
- 如果 sandbox 返回错误、上下文取消或 `jobs` 写入失败，当前这批消息会回退到 `pending`，等待后续 dispatch 重试。

## 8. 失败处理与恢复
- `SandboxClaim` 创建失败：
  - 当前触发消息已经写入 `messages(status=pending)`；dispatch 会在后续轮询中继续重试。
- sandbox 长时间未 ready：
  - 视为当前 room dispatch 失败，不影响已持久化的 `messages.seq`。
- gRPC 批次下发失败：
  - 消息仍保持 `pending`，由后续 dispatch 重试。
- sandbox 返回 `error`：
  - 当前批次从 `sent` 恢复为 `pending`。
- 上下文超时 / 服务退出：
  - 当前批次会回退为 `pending`；若进程中断时有残留 `sent`，启动时统一恢复。
- 企业微信详情解析失败：
  - 发送者昵称解析失败不会阻塞入库；若后续群详情解析失败，则由该 room 在后续轮询中重试。
- bot 自发消息：
  - 仍然会落一条 `messages(status=ignored)`，但不会进入 dispatch。
- `jobs` 写入失败：
  - 当前批次回退到 `pending`，由后续 dispatch 继续重试。

已知窗口：
- 如果 `jobs` 已成功写入，但 `messages.done` 更新失败，当前版本不会立即重试；这批消息会停留在 `sent`，依赖后续服务重启时 `ResetSentMessages()` 恢复，存在重复出队窗口。

## 9. 可观测性
当前代码已经暴露：
- `tinyclaw_messages_pulled_total`
- `tinyclaw_messages_dispatched_total`
- `tinyclaw_messages_skipped_total{reason}`
- `tinyclaw_sandbox_invocations_total{result}`
- `tinyclaw_sandbox_duration_seconds`
- `tinyclaw_db_duration_seconds{operation}`
- `tinyclaw_pull_cycle_errors_total`

当前缺口：
- 尚未把 `activeSandboxes` 真正接到 room / claim 生命周期。
- 尚未提供 `sent` backlog、`jobs.done` 更新失败次数、gRPC 断线次数等关键运维指标。
- 尚未沉淀告警阈值和 runbook。

## 10. v0 当前结论
v0 方案的重点已经从“Redis 驱动 sandbox 自拉”切换为“官方 `SandboxTemplate + SandboxClaim` 控制面 + `clawman` gRPC message gateway + PostgreSQL 最小事实源”：
- 控制面更贴近官方 agent-sandbox 资源模型。
- 数据面已经脱离 `sandbox-router + HTTP /agent`。
- sandbox 不再依赖 per-room Redis 凭据和 consumer group。
- 主服务职责更清晰，后续可在此基础上继续扩 richer streaming、scheduler、memory 和 warm pool。

## 11. 2026-04-10 之后的扩展方向
本节描述的是下一阶段优先方向，不覆盖上文对当前实现的事实描述。

### 11.1 保留的架构原则
- `room_id -> sandbox` 仍然是核心隔离边界。
- 外部渠道事实源、审计、重试与投递仍然收敛在 `clawman`。
- 群聊粗 trigger 仍在 `clawman`，不下放到 sandbox。

### 11.2 下一阶段推荐方向
- 在现有 `result/error` 之上扩 richer streaming 事件：
  - `typing`
  - `assistant_delta`
  - `assistant_final`
  - `tool_event`
  - `failed`
- 为 gRPC 连接补齐鉴权、心跳、断线恢复和更细粒度错误模型。
- 为 `sent -> done` 补后台恢复与幂等策略，而不是只依赖进程重启恢复。
- 在不破坏现有 `messages` 事实源的前提下，引入 memory、scheduler 和文件上下文能力。
