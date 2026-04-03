# TinyClaw + Agent Sandbox Integration v0

> 状态说明：本文档描述的是 v0 当前实现，即 `sandbox-router + HTTP /agent` 集成链路。2026-04-01 起，仓库额外形成一条下一阶段共识：保留 `room_id -> sandbox` 隔离模型，但把 `clawman` 收敛为统一 `message gateway`，并优先评估 `clawman <-> sandbox` 的 `gRPC bidirectional streaming` 内部协议。

## 1. 目标

把 `kubernetes-sigs/agent-sandbox` 作为 TinyClaw 的执行层底座，并且对齐官方扩展接口：
- 用 `SandboxTemplate` 描述统一 agent 运行环境。
- 用官方 Go SDK 管理 `SandboxClaim` 生命周期与 router 连接。
- 用 `sandbox-router` 作为 SDK direct-url 模式的入口。
- 不再使用“sandbox 自拉 Redis ingress”的旧链路。

非目标：
- v0 不引入 `SandboxWarmPool`。
- v0 不做 snapshot / restore。
- v0 不在镜像内实现 supervisor。
- v0 不引入跨会话共享 sandbox。

## 2. 组件划分

1. `Ingress Service`
   - 拉取企业微信消息并标准化。
   - 调用 SDK `Open()`，保证对应 sandbox ready。
   - 复用当前 sandbox claim 元数据，经 router 直接调用 HTTP runtime `/agent`。
   - 将入站消息写入 PostgreSQL `messages`；agent 返回后由主服务写入 `jobs` outbox，并在写入成功后把对应 `messages` 标记为 `done`。

2. `Sandbox Orchestrator`
   - 调用官方 Go SDK 创建 room 级 sandbox session。
   - 以 SDK `Open()` 完成 claim ready 与连接建立作为可调用条件。
   - 当前版按 `room_id` 在进程内缓存已打开的 sandbox session。

3. `Agent Runtime (in Sandbox)`
   - 暴露 `GET /healthz`、`POST /agent` 与标准 `/execute`、`/upload`、`/download`、`/list`、`/exists`。
   - 调用 Claude runtime 或 echo runtime。
   - 管理本地工作目录与临时文件，不直接读写 Redis ingress。

## 3. 资源映射

当前约定：

```text
SandboxTemplate.name = tinyclaw-agent-template
room_id             -> process-local SDK client
```

标签建议：

```text
labels:
  app: tinyclaw-sandbox
  tinyclaw/room-id: "{room_id}"
```

说明：
- 当前官方 Go SDK 不支持由业务层指定确定性 `SandboxClaim.name`。
- v0 不单独维护 `room_runtime` 表，因此 room 级复用先收敛在单进程生命周期内。

## 4. 控制面契约

### 4.1 SandboxTemplate
`SandboxTemplate` 由平台预先部署，负责固化：
- 镜像
- 监听端口
- readiness / liveness
- Claude 凭据注入方式
- 运行目录

推荐最小模板：
- 容器端口：`8888`
- readiness：`GET /healthz`
- liveness：`GET /healthz`
- `AGENT_SERVER_PORT=8888`
- `AGENT_RUNTIME_MODE=claude_agent_sdk`
- `AGENT_WORKDIR=/workspace`

### 4.2 SandboxClaim
官方 Go SDK 在 `Open()` 内部创建 claim：

```yaml
apiVersion: extensions.agents.x-k8s.io/v1alpha1
kind: SandboxClaim
metadata:
  name: sandbox-claim-<sdk-generated>
spec:
  sandboxTemplateRef:
    name: tinyclaw-agent-template
```

ready 判定：
- `status.conditions[type=Ready].status == True`

### 4.3 Router
SDK direct-url 模式需要一个固定 router 地址，例如：

```text
http://sandbox-router-svc.claw.svc.cluster.local:8080
```

当前 `X-Sandbox-*` 头由主服务复用 SDK 已建立的 sandbox claim 元数据填充，业务逻辑不再通过 shell 桥接 `/execute`。

## 5. 数据面契约

### 5.1 主服务 -> sandbox

当前实现由主服务直接调用 router `/agent`：
1. SDK `Open()` 保证当前 room 对应的 sandbox ready
2. 主服务对 router `POST /agent`
3. 请求头带上 `X-Sandbox-ID / X-Sandbox-Namespace / X-Sandbox-Port`
4. `/agent` 返回的 JSON 直接回主服务

`/agent` 的请求体保持：

```http
POST http://127.0.0.1:8888/agent
Content-Type: application/json
```

请求体：

```json
{
  "msgid": "wecom_msg_abc",
  "room_id": "room-123",
  "tenant_id": "corp-id",
  "chat_type": "group",
  "messages": [
    {
      "seq": 123,
      "msgid": "wecom_msg_abc",
      "from_id": "zhangsan",
      "from_name": "张三",
      "msg_time": "2026-03-21T10:00:00Z",
      "payload": "{\"msgtype\":\"text\",\"text\":{\"content\":\"hello\"}}"
    }
  ]
}
```

响应体：

```json
{
  "stdout": "agent reply",
  "stderr": "",
  "exit_code": 0
}
```

### 5.2 sandbox -> 主服务
- v0 不走主动回调。
- sandbox 直接同步返回 HTTP 响应。
- 主服务收到结果后写入 PostgreSQL `jobs(bot_id, recipient_alias, message, max_seq)`，并在写入成功后更新 `messages(status=done)`。

## 6. Runtime 环境变量契约

必需：

```text
AGENT_SERVER_PORT          # 默认 8888
ANTHROPIC_API_KEY          # 与 CLAUDE_CODE_OAUTH_TOKEN 二选一（echo 模式可省略）
CLAUDE_CODE_OAUTH_TOKEN
```

可选：

```text
ANTHROPIC_BASE_URL
AGENT_RUNTIME_MODE         # echo | claude_agent_sdk
AGENT_WORKDIR
AGENT_TMPDIR
AGENT_IDLE_AFTER_SEC
AGENT_LOG_LEVEL
CLAUDE_MODEL
CLAUDE_SYSTEM_PROMPT_APPEND
CLAUDE_ALLOWED_TOOLS
CLAUDE_DISALLOWED_TOOLS
CLAUDE_MAX_TURNS
CLAUDE_RUNTIME_TIMEOUT_MS
```

说明：
- `ROOM_ID`、`REDIS_ADDR`、`CONSUMER_GROUP_*` 不再属于 agent runtime 契约。
- room 级上下文从每次 HTTP 请求体中传入。

## 7. 文件系统与卷布局

建议目录：

```text
/workspace
/var/lib/tinyclaw
/tmp
```

卷建议：
- `/workspace`：`emptyDir`
- `/var/lib/tinyclaw`：`emptyDir`

说明：
- v0 不依赖 PVC。
- 运行中产生的长期记忆不保存在容器本地。

## 8. 启动、就绪与退出

### 8.1 启动检查
entrypoint 至少检查：
- `AGENT_RUNTIME_MODE`
- `ANTHROPIC_API_KEY` 或 `CLAUDE_CODE_OAUTH_TOKEN`（非 echo 模式）

### 8.2 Readiness
agent 容器 ready 条件：
1. HTTP server 已启动。
2. `/healthz` 返回 200。

### 8.3 Liveness
最小策略：
- 主进程存活。
- `/healthz` 可响应。

### 8.4 优雅退出
收到 `SIGTERM` 后：
1. 停止接受新 HTTP 请求。
2. 等待当前请求完成。
3. 由 sandbox / K8s 负责后续拉起。

## 9. 端到端时序

### 9.1 首条消息
1. Ingress 拉到企业微信消息并标准化。
2. 根据 `room_id` 获取或创建当前进程内 sandbox session。
3. Orchestrator 创建或复用对应 room 的 SDK client。
4. SDK `Open()` 完成后，主服务直接通过 router 调用 `/agent`。
5. agent 返回回复。
6. 主服务把回复写入 `jobs` outbox。
7. `jobs` 写入成功后，主服务把参与处理的 `messages` 标记为 `done`。

### 9.2 活跃会话
1. 新消息到达。
2. 主服务直接复用现有 SDK client。
3. 再次直接通过 router 调用 `/agent`。
4. 不再等待 Redis ingress 被 sandbox 消费。

## 10. 2026-04-01 下一阶段方向
- 保留 `room_id -> sandbox` 的隔离和复用模型，不回退到“每个 sandbox 自拉 Redis / mailbox”的方案。
- `clawman` 持有外部渠道协议、认证、cursor 与发送动作；sandbox 不直接维护企业微信/飞书/email 等第三方协议状态。
- 下一阶段会把内部数据面从“同步 `/agent` 最终态 JSON”逐步收敛为 room 级 streaming 会话协议。
- 当前首选方向是 `gRPC bidirectional streaming`；`WebSocket` 只保留为探索期备选。
- sandbox 对 `clawman` 输出的应是规范化事件和结果，例如 `typing`、`assistant_delta`、`assistant_final`、`tool_event`、`failed`；由 `clawman` 再映射到具体渠道的发送接口。

## 11. 失败处理

1. `SandboxClaim` 创建失败
   - 当前消息保持 `messages(status=pending)`，下一轮重试。
2. `SandboxClaim` 长时间未 ready
   - 视为当前消息失败，记日志并重试。
3. router 调用失败
   - 视为当前消息失败，保留 `pending` 供后续重试。
4. agent 运行失败
   - router 返回 5xx，主服务记录并重试。
5. `jobs` 写入失败
   - 当前消息保持 `messages(status=pending)`，由后续 dispatch 重试。
6. `jobs` 写入成功但 `messages.done` 更新失败
   - 后续 dispatch 可能重复写入同一回复任务；当前版本先接受这一权衡。

## 12. 观测与告警

核心指标：
- `sandbox_claim_ready_latency_ms`
- `sandbox_http_request_latency_ms`
- `sandbox_http_error_rate`
- `reply_e2e_ms`
- `job_enqueue_error_rate`

告警建议：
- `SandboxClaim Ready` 超时率持续升高
- router 5xx 持续升高
- `messages(status=pending)` backlog 持续累积

## 13. 本轮确认约束

1. 官方控制面接口统一使用 `SandboxTemplate` 与 `SandboxClaim`。
2. v0 当前实现的官方通信接口统一使用 router + HTTP runtime。
3. 不再给 sandbox 注入 per-room Redis ingress 配置。
4. 主服务最小状态统一落 PostgreSQL，不再依赖 Redis。
5. v0 默认软休眠，不启用 warm pool。
