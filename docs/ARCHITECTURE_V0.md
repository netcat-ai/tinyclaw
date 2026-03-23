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
+---------------------------+       | - persist archive into messages  |
                                    | - scan pending rooms             |
                                    | - manage sandbox via Go SDK      |
                                    | - invoke /agent via SDK Run      |
                                    +----------------+-----------------+
                                                     |
                                   +-----------------v------------------+
                                   | PostgreSQL                           |
                                   | - messages                           |
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
1. `clawman` 运行两个独立协程：ingest worker 每 3 秒执行一次 archive pull，dispatch worker 每秒扫描一次 `messages(status=pending)`。
2. ingest worker 先查询当前 tenant 的 `MAX(messages.seq)`，再调用 `GetChatData(seq, 100)`；`messages.seq` 是唯一拉取 checkpoint。
3. 对每条 archive item 按 `seq` 升序执行解密、JSON 解析与字段校验，并把完整解密结果写入 `messages.payload`。
4. 所有 archive item 都必须先落库：
   - bot/self、冷启动窗口外的历史消息、非法 payload 写成 `status=ignored`
   - 群聊未触发消息写成 `status=buffered`
   - 私聊消息和群聊触发消息写成 `status=pending`
5. 若 `DecryptData` 失败，视为致命 ingest 错误，当前 worker 直接返回错误并退出服务，而不是把该消息写成 `ignored`。
6. 若当前消息是群聊触发消息，ingest 会在同一个事务中把该 `room_id` 下已有 `buffered` 消息一并提升为 `pending`。
7. 冷启动且 `messages` 为空时，只允许最近 10 分钟内的消息进入 `pending/buffered`；更早的 archive backlog 仍会写入 `messages`，但统一标记为 `ignored`。
8. dispatch 阶段按 `room_id` 聚合，同一个 room 在同一轮只触发一次 agent 调用。
9. 触发处理时，先按 `tenant_id + room_id` 读取所有 `status=pending` 的消息，作为当前 room 尚未处理的结构化上下文窗口。
10. 触发阶段按需补充解析群详情；发送方昵称在 ingest 阶段 best-effort 写入 `from_name`，失败不阻塞入库。
11. 通过官方 Go SDK `Open()` 确保该 room 的 sandbox session ready；进程内 room session cache 遇到 `ErrOrphanedClaim` 时会先 `Close()` 再重建。
12. 通过官方 Go SDK `Run()` 在 sandbox 内部桥接调用本机 `POST /agent`。
13. agent 成功返回后，主服务直接通过 WorkTool 回发企业微信。
14. 只有当 WorkTool 回发成功后，主服务才把本轮参与处理的 `messages` 标记为 `done`。

### 5.2 Router 调用契约
当前主服务不再直接从业务层拼接 router 请求头，而是让官方 Go SDK 通过 direct-url 模式连接 `sandbox-router`，并通过 `/execute` 在 sandbox 内部调用：

```text
POST http://127.0.0.1:{AGENT_SERVER_PORT}/agent
```

请求体最小集：

```json
{
  "msgid": "wecom_msg_abc",
  "room_id": "chat_123",
  "tenant_id": "corp_id",
  "chat_type": "group",
  "messages": [
    {
      "seq": 123,
      "msgid": "wecom_msg_abc",
      "from_id": "zhangsan",
      "from_name": "张三",
      "msg_time": "2026-03-21T10:00:00Z",
      "payload": "{\"msgtype\":\"text\",\"text\":{\"content\":\"你好\"}}"
    }
  ]
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
- `messages`：企业微信 archive 入站事实、待处理状态、拉取 checkpoint

补充语义：
- `messages` 的流程状态固定为 `ignored / buffered / pending / done`。
- `messages.seq` 单调递增；系统通过 `MAX(messages.seq)` 推导下一次 archive pull 的起点。
- 群聊未触发消息保持 `status=buffered`，用于后续触发时补齐上下文。
- dispatch/发送失败不会回滚 `messages.seq`；后续轮询会基于已持久化的 `pending` 消息继续重试，而不会丢失上下文。

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

### 7.3 Reply Delivery
- 在 `dispatchRoom` 成功拿到 agent reply 后，直接调用 WorkTool 回发企业微信。
- 只有发送成功后，当前这批 `messages` 才会从 `pending` 更新到 `done`。
- 如果 WorkTool 回发失败，当前这批消息保持 `pending`，等待后续 dispatch 重试。

## 8. 失败处理
- `SandboxClaim` 创建失败：
  - 当前触发消息已经写入 `messages(status=pending)`；dispatch 会在后续轮询中继续重试。
- sandbox 未 ready：
  - 视为当前触发消息 dispatch 失败，但不影响已经持久化的 `messages.seq`。
- sandbox 返回 5xx：
  - 当前触发消息保留重试机会，但不会回滚已经持久化的 `messages.seq`。
- 企业微信详情解析失败：
  - 发送者昵称解析失败不会阻塞入库；如果后续群详情解析失败，则由 pending room 在后续轮询中重试。
- bot 自发消息：
  - 仍然会落一条 `messages(status=ignored)`，但不会进入 dispatch。
- WorkTool 回发失败：
  - 当前消息保持 `messages(status=pending)`，由后续 dispatch 继续重试。

现网含义：
- 冷启动且 `messages` 为空时，只会处理最近 10 分钟消息；更早的 archive backlog 统一写入 `ignored`，避免误回复历史对话。
- 非冷启动场景下，服务恢复后会从 `MAX(messages.seq)` 继续拉取；如果已有 `pending` 消息尚未完成处理，dispatch 会继续复用同一批消息重试，而不是重复插入。

## 9. 可观测性
核心指标建议：
- `sandbox_claim_ready_latency_ms`
- `sandbox_invoke_latency_ms`
- `sandbox_http_error_rate`
- `reply_e2e_ms`
- `worktool_send_error_rate`

## 10. 结论
v0 方案的重点已经从“Redis 驱动 sandbox 自拉”切换为“官方 agent-sandbox Go SDK + router/direct-url + PostgreSQL 最小事实源”：
- 控制面更贴近官方演进方向。
- 模板复用能力更强。
- sandbox 不再依赖 per-room Redis 凭据和 consumer group。
- 主服务职责更清晰，后续接 warm pool、snapshot、router gateway 都更顺。
