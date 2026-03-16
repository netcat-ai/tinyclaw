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
+---------------------------+       | - ensure SandboxClaim            |
                                    | - invoke sandbox via router      |
                                    | - append egress stream           |
                                    +----------------+-----------------+
                                                     |
                                   +-----------------v------------------+
                                   | Redis                                |
                                   | - msg:seq                            |
                                   | - wecom:* cache                      |
                                   | - lock:ensure:{room_id}              |
                                   | - stream:o:{room_id}                 |
                                   +-----------------+------------------+
                                                     |
                                   +-----------------v------------------+
                                   | Egress Consumer                     |
                                   | - WorkTool / WeCom send             |
                                   | - retry / DLQ                       |
                                   +-------------------------------------+

                                                     |
                                                     v
                              +-------------------------------------------+
                              | agent-sandbox extensions                  |
                              | SandboxTemplate + SandboxClaim + router   |
                              +-------------------+-----------------------+
                                                  |
                                                  v
                              +-------------------------------------------+
                              | Sandbox Pod per room                      |
                              | - tinyclaw agent HTTP server              |
                              | - /healthz                                |
                              | - /v1/chat                                |
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
- 控制面隔离：每个 `room_id` 对应一个确定性 `SandboxClaim`。
- 运行时隔离：每个 room 一个 sandbox pod。
- 文件系统隔离：每个 sandbox 拥有独立 `/workspace`。
- 网络隔离：由 agent-sandbox 模板和网络策略控制，默认按最小权限配置。

## 4. 控制面设计

### 4.1 官方资源模型
- `SandboxTemplate`
  - 描述统一的 agent 镜像、端口、探针和运行时环境。
- `SandboxClaim`
  - 由主服务按 `room_id` create-or-get。
  - claim ready 后即可通过 router 访问对应 sandbox。
- `sandbox-router`
  - 作为统一入口，根据请求头把流量转发到对应 sandbox service。

### 4.2 命名约定

```text
SandboxClaim.name = clawagent-{room_id_lower}
Sandbox.name      = clawagent-{room_id_lower}
```

说明：
- claim 与 sandbox 使用同名，便于 router 直接用 `X-Sandbox-ID` 路由。
- `lock:ensure:{room_id}` 仍保留，用于抑制 ensure 风暴。

## 5. 数据面设计

### 5.1 Ingress 流程
1. `clawman` 周期拉取企业微信会话存档。
2. 解密并解析消息，过滤非法或 bot 自发消息。
3. 根据 `room_id` 解析发送方或群详情，写入 Redis 缓存。
4. 调用 `ensure(room_id)`，等待 `SandboxClaim` ready。
5. 通过 router 发送 `POST /v1/chat` 到 sandbox。
6. 拿到回复后写入 `stream:o:{room_id}`。
7. egress consumer 统一回发企业微信。

### 5.2 Router 调用契约
请求路径：

```text
POST {SANDBOX_ROUTER_URL}/v1/chat
```

请求头：
- `X-Sandbox-ID: clawagent-{room_id_lower}`
- `X-Sandbox-Namespace: claw`
- `X-Sandbox-Port: 8888`

请求体最小集：

```json
{
  "msgid": "wecom_msg_abc",
  "room_id": "chat_123",
  "tenant_id": "corp_id",
  "chat_type": "group",
  "text": "用户消息正文"
}
```

返回体最小集：

```json
{
  "text": "agent reply",
  "metadata": {
    "runtime_mode": "claude_agent_sdk"
  }
}
```

### 5.3 Redis 职责
v0 中 Redis 不再承担 sandbox ingress，保留以下职责：
- `msg:seq`：企业微信拉取游标
- `wecom:*`：企业微信详情缓存
- `lock:ensure:{room_id}`：ensure 防抖
- `stream:o:{room_id}`：回复出站流
- `dlq:reply`：回发死信

## 6. Agent Runtime 设计

### 6.1 运行模式
- `echo`
  - 用于联调和基础测试。
- `claude_agent_sdk`
  - 用于真实 Claude 执行。

### 6.2 入口契约
- HTTP:
  - `GET /healthz`
  - `POST /v1/chat`
- 运行目录：
  - `AGENT_WORKDIR`
  - `AGENT_TMPDIR`
- Claude 凭据：
  - `ANTHROPIC_API_KEY` 或 `CLAUDE_CODE_OAUTH_TOKEN`

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
- 通过 `SandboxClaim` create-or-get 管理 room sandbox。
- 等待 claim ready。
- 不单独维护 room registry 表。

### 7.3 Egress
- 消费 `stream:o:{room_id}`。
- 统一回发企业微信。
- 失败重试，必要时进入 DLQ。

## 8. 失败处理
- `SandboxClaim` 创建失败：
  - 主服务不推进 `msg:seq`，在下一轮拉取时重试。
- sandbox 未 ready：
  - 视为当前消息处理失败，不推进 `msg:seq`。
- sandbox 返回 5xx：
  - 当前消息保留重试机会，不推进 `msg:seq`。
- egress 回发失败：
  - 保留在 egress stream 中重试，超过阈值进入 `dlq:reply`。

## 9. 可观测性
核心指标建议：
- `sandbox_claim_ready_latency_ms`
- `sandbox_invoke_latency_ms`
- `sandbox_http_error_rate`
- `reply_e2e_ms`
- `egress_retry_count`

## 10. 结论
v0 方案的重点已经从“Redis 驱动 sandbox 自拉”切换为“官方 agent-sandbox 控制面 + router 驱动 HTTP 调用”：
- 控制面更贴近官方演进方向。
- 模板复用能力更强。
- sandbox 不再依赖 per-room Redis 凭据和 consumer group。
- 主服务职责更清晰，后续接 warm pool、snapshot、router gateway 都更顺。 
