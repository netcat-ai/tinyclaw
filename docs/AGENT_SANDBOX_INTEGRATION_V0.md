# TinyClaw + Agent Sandbox Integration v0

## 1. 目标

把 `kubernetes-sigs/agent-sandbox` 作为 TinyClaw 的执行层底座，并且对齐官方扩展接口：
- 用 `SandboxTemplate` 描述统一 agent 运行环境。
- 用 `SandboxClaim` 按 `room_id` 动态申请和复用 sandbox。
- 用 `sandbox-router` 将主服务请求转发到 sandbox 内 HTTP runtime。
- 不再使用“sandbox 自拉 Redis ingress”的旧链路。

非目标：
- v0 不引入 `SandboxWarmPool`。
- v0 不做 snapshot / restore。
- v0 不在镜像内实现 supervisor。
- v0 不引入跨会话共享 sandbox。

## 2. 组件划分

1. `Ingress Service`
   - 拉取企业微信消息并标准化。
   - 调用 `ensure(room_id)`，保证对应 `SandboxClaim` ready。
   - 通过 router 调用 sandbox 内 HTTP runtime。
   - 将回复写入 egress stream，由统一回发服务处理。

2. `Sandbox Orchestrator`
   - 调用 extensions client 创建或查询 `SandboxClaim`。
   - 以 `SandboxClaim Ready=True` 作为可调用条件。
   - 使用 `lock:ensure:{room_id}` 抑制 ensure 风暴。

3. `Agent Runtime (in Sandbox)`
   - 暴露 `GET /healthz` 与 `POST /v1/chat`。
   - 调用 Claude runtime 或 echo runtime。
   - 管理本地工作目录与临时文件，不直接读写 Redis ingress。

## 3. 资源映射

命名规范：

```text
SandboxTemplate.name = tinyclaw-agent-template
SandboxClaim.name    = clawagent-{room_id_lower}
Sandbox.name         = clawagent-{room_id_lower}
```

标签建议：

```text
labels:
  app: tinyclaw-sandbox
  tinyclaw/room-id: "{room_id}"
```

说明：
- `SandboxClaim` 与底层 `Sandbox` 同名，方便 router 直接按 claim 名路由。
- v0 不单独维护 `room_runtime` 表。

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
主服务按 `room_id` create-or-get：

```yaml
apiVersion: extensions.agents.x-k8s.io/v1alpha1
kind: SandboxClaim
metadata:
  name: clawagent-room-123
spec:
  sandboxTemplateRef:
    name: tinyclaw-agent-template
```

ready 判定：
- `status.conditions[type=Ready].status == True`

### 4.3 Router
主服务需要一个固定 router 地址，例如：

```text
http://sandbox-router-svc.claw.svc.cluster.local:8080
```

转发头：
- `X-Sandbox-ID`
- `X-Sandbox-Namespace`
- `X-Sandbox-Port`

## 5. 数据面契约

### 5.1 主服务 -> sandbox

请求：

```http
POST /v1/chat
X-Sandbox-ID: clawagent-room-123
X-Sandbox-Namespace: claw
X-Sandbox-Port: 8888
Content-Type: application/json
```

请求体：

```json
{
  "msgid": "wecom_msg_abc",
  "room_id": "room-123",
  "tenant_id": "corp-id",
  "chat_type": "group",
  "text": "hello"
}
```

响应体：

```json
{
  "text": "agent reply",
  "metadata": {
    "runtime_mode": "claude_agent_sdk"
  }
}
```

### 5.2 sandbox -> 主服务
- v0 不走主动回调。
- sandbox 直接同步返回 HTTP 响应。
- 主服务收到结果后写入 `stream:o:{room_id}`。

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
2. 根据 `room_id` 调用 `ensure(room_id)`。
3. Orchestrator create-or-get `SandboxClaim`。
4. `SandboxClaim` ready 后，主服务通过 router 调用 `/v1/chat`。
5. agent 返回回复。
6. 主服务写入 `stream:o:{room_id}`。
7. egress consumer 统一回发企业微信。

### 9.2 活跃会话
1. 新消息到达。
2. 主服务直接复用现有 `SandboxClaim`。
3. 通过 router 再次调用 `/v1/chat`。
4. 不再等待 Redis ingress 被 sandbox 消费。

## 10. 失败处理

1. `SandboxClaim` 创建失败
   - 当前消息不推进 `msg:seq`，下一轮重试。
2. `SandboxClaim` 长时间未 ready
   - 视为当前消息失败，记日志并重试。
3. router 调用失败
   - 视为当前消息失败，不推进 `msg:seq`。
4. agent 运行失败
   - router 返回 5xx，主服务记录并重试。
5. egress 回发失败
   - 保留在 egress stream 中，按 consumer 逻辑重试。

## 11. 观测与告警

核心指标：
- `sandbox_claim_ready_latency_ms`
- `sandbox_http_request_latency_ms`
- `sandbox_http_error_rate`
- `reply_e2e_ms`
- `egress_retry_count`

告警建议：
- `SandboxClaim Ready` 超时率持续升高
- router 5xx 持续升高
- `stream:o:{room_id}` backlog 持续累积

## 12. 本轮确认约束

1. 官方控制面接口统一使用 `SandboxTemplate` 与 `SandboxClaim`。
2. 官方通信接口统一使用 router + HTTP runtime。
3. 不再给 sandbox 注入 per-room Redis ingress 配置。
4. Redis 只保留主服务状态、缓存和 egress 职责。
5. v0 默认软休眠，不启用 warm pool。
