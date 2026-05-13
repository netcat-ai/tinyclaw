# TinyClaw + Agent Sandbox Integration v0

> 状态说明：本文档描述的是 2026-04-10 当前实现。控制面使用 `SandboxTemplate + SandboxClaim`，数据面已经切到 `clawman` gRPC server + sandbox gRPC client；旧的 `sandbox-router + HTTP /agent` 链路已不再代表当前代码。

## 1. 目标

把 `kubernetes-sigs/agent-sandbox` 作为 TinyClaw 的执行层底座，并与当前实现保持一致：
- 用 `SandboxTemplate` 描述统一 agent 运行环境。
- 用 `SandboxClaim` 表示 room 级 sandbox 归属。
- 用 `clawman` 暴露内部 `gRPC` message gateway。
- sandbox 启动后主动连接 `clawman`，通过 `messages[]` 批次收发最小消息协议。
- 不再使用“sandbox 自拉 Redis ingress”的旧链路。

非目标：
- v0 不引入 `SandboxWarmPool`。
- v0 不做 snapshot / restore。
- v0 不在镜像内实现 supervisor。
- v0 不引入跨会话共享 sandbox。
- v0 先不做 gRPC 鉴权和 richer streaming 事件。

## 2. 组件划分

1. `Ingress Service (clawman)`
   - 拉取企业微信消息并标准化。
   - 按 `room_id` 创建或复用 `SandboxClaim`。
   - 暴露 `RoomChat(stream Message)` gRPC server。
   - 暴露内部 `POST /internal/media/fetch`，按需返回图片二进制。
   - 将入站消息写入 PostgreSQL `messages`。
   - agent 返回后把 reply 写入 PostgreSQL `jobs`，并推进 `messages` 状态。

2. `Sandbox Orchestrator`
   - 负责 `SandboxClaim` 的创建、查询与 ready 等待。
   - 当前 claim 名称由 `room_id` 稳定推导。
   - 当前版按 `room_id` 在进程内缓存 claim 信息。

3. `Agent Runtime (in Sandbox)`
   - 暴露 `GET /healthz`。
   - 启动后主动连接 `clawman` gRPC gateway。
   - 调用 Claude runtime 或 echo runtime。
   - 通过 in-process MCP custom tool 在需要时下载图片到本地工作目录。
   - 管理本地工作目录与临时文件，不直接读写 Redis ingress。

## 3. 资源映射

当前约定：

```text
SandboxTemplate.name = tinyclaw-agent-template
room_id             -> deterministic SandboxClaim.name
```

标签与注解建议：

```text
labels:
  app: tinyclaw-sandbox

annotations:
  tinyclaw/room-id: "{room_id}"
```

说明：
- 当前 claim 名称由 `room_id` 哈希推导，便于 crash 后重新识别同一个 room 对应的 claim。
- v0 不单独维护 `room_runtime` 表，因此 room 级复用仍然先收敛在单进程生命周期内。

## 4. 控制面契约

### 4.1 SandboxTemplate
`SandboxTemplate` 由平台预先部署，负责固化：
- 镜像
- 监听端口
- readiness / liveness
- Claude 凭据注入方式
- 运行目录
- `CLAWMAN_GRPC_ADDR`
- `CLAWMAN_INTERNAL_BASE_URL`
- `CLAWMAN_INTERNAL_TOKEN`

推荐最小模板：
- 容器端口：`8888`
- readiness：`GET /healthz`
- liveness：`GET /healthz`
- `AGENT_SERVER_PORT=8888`
- `AGENT_RUNTIME_MODE=claude_agent_sdk`
- `AGENT_WORKDIR=/workspace`
- `CLAWMAN_GRPC_ADDR=clawman-svc.claw.svc.cluster.local:8092`
- `CLAWMAN_INTERNAL_BASE_URL=http://clawman-svc.claw.svc.cluster.local:8081`
- `CLAWMAN_INTERNAL_TOKEN=<shared-secret>`

### 4.2 SandboxClaim
`clawman` 会为 room 创建或复用 claim：

```yaml
apiVersion: extensions.agents.x-k8s.io/v1alpha1
kind: SandboxClaim
metadata:
  name: tinyclaw-room-<stable-hash>
  namespace: claw
  annotations:
    tinyclaw/room-id: room-123
spec:
  templateRef:
    name: tinyclaw-agent-template
```

ready 判定：
- `status.conditions[type=Ready].status == True`

### 4.3 gRPC Gateway
当前由 `clawman` 暴露固定 gRPC 地址，例如：

```text
clawman-svc.claw.svc.cluster.local:8092
```

说明：
- sandbox 启动后主动拨号该地址。
- 第一条消息必须是 `kind=connect`，携带 `sandbox_id`。
- 当前版本先不做传输层鉴权。

### 4.4 Internal Media API
当前实现额外暴露一个仅供 sandbox 使用的内部 HTTP 接口：

```text
POST /internal/media/fetch
Authorization: Bearer <CLAWMAN_INTERNAL_TOKEN>
```

请求体：

```json
{
  "room_id": "room-123",
  "seq": 123,
  "msgid": "wecom_msg_abc",
  "sdk_file_id": "sdkfileid-xxx"
}
```

说明：
- `clawman` 会先按 `tenant_id + room_id + seq + msgid` 查 `messages`。
- 然后校验该消息确实是 `msgtype=image`，且 payload 中的 `sdkfileid` 与请求一致。
- 校验通过后，服务端调用企业微信存档 SDK `GetMediaData` 拉取图片并返回二进制。
- `CLAWMAN_INTERNAL_TOKEN` 为空时，该接口保持禁用。

## 5. 数据面契约

### 5.1 `clawman -> sandbox`
当前实现通过 `RoomChat(stream Message)` 下发 room 批次消息：

1. `clawman` 确保对应 room 的 claim ready。
2. `clawman` 等待该 room sandbox 的 gRPC 连接建立。
3. `clawman` 在流上发送 `kind=messages`，携带 `request_id` 与 `messages[]`。
4. 发送成功后，主服务把这批 `messages` 从 `pending` 标记为 `sent`。

当前最小请求体：

```json
{
  "kind": "messages",
  "request_id": "req-123",
  "messages": [
    {
      "seq": 123,
      "msgid": "wecom_msg_abc",
      "room_id": "room-123",
      "from_id": "zhangsan",
      "from_name": "张三",
      "msg_time": "2026-04-10T10:00:00Z",
      "payload": "{\"msgtype\":\"text\",\"text\":{\"content\":\"hello\"}}"
    }
  ]
}
```

### 5.2 `sandbox -> clawman`
当前最小输出只保留两类：
- `kind=result`
- `kind=error`

示例：

```json
{
  "kind": "result",
  "request_id": "req-123",
  "output": "agent reply"
}
```

```json
{
  "kind": "error",
  "request_id": "req-123",
  "error": "runtime timeout"
}
```

说明：
- `result` 只携带最终输出文本，由 `clawman` 写入 `jobs`。
- `error` 表示本轮处理失败，`clawman` 会把当前批次从 `sent` 恢复到 `pending`。
- 当前不保留应用层 `ack`；gRPC `Send` 成功只代表批次已送达 agent 进程，不代表业务已完成。

### 5.3 图片消息按需落地
当前图片处理链路：

1. `messages.payload` 保留企业微信原始图片 payload，包括 `image.sdkfileid`。
2. runtime 向 Claude 暴露一个 in-process MCP custom tool：`fetch_wecom_image`。
3. Claude 需要查看图片时，自行调用 `fetch_wecom_image(room_id, seq, msgid, sdk_file_id)`。
4. tool 内部请求 `POST /internal/media/fetch` 拉取图片，并把文件落到 `AGENT_WORKDIR/incoming-media/<room_id>/`。
5. tool 返回本地路径、文件名和内容类型，Claude 再基于该本地文件继续处理。

## 6. Runtime 环境变量契约

必需：

```text
AGENT_SERVER_PORT          # 默认 8888
CLAWMAN_GRPC_ADDR          # clawman gRPC gateway
CLAWMAN_INTERNAL_TOKEN     # 内部图片下载 bearer token
ANTHROPIC_API_KEY          # 与 CLAUDE_CODE_OAUTH_TOKEN 二选一（echo 模式可省略）
CLAUDE_CODE_OAUTH_TOKEN
```

可选：

```text
ANTHROPIC_BASE_URL
AGENT_RUNTIME_MODE         # echo | claude_agent_sdk
AGENT_WORKDIR
AGENT_TMPDIR
CLAWMAN_INTERNAL_BASE_URL  # 缺省时由 CLAWMAN_GRPC_ADDR 推导 host，并默认端口 8081
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
- room 级上下文从每次 gRPC `messages[]` 批次中传入。
- 图片二进制不复用 `RoomChat(stream Message)` 传输，而是通过内部 HTTP API 按需下载。

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
- `CLAWMAN_GRPC_ADDR`
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
1. 关闭 HTTP server。
2. gRPC bridge 随主进程退出。
3. 由 sandbox / K8s 负责后续拉起。

## 9. 端到端时序

### 9.1 首条消息
1. Ingress 拉到企业微信消息并标准化。
2. 根据 `room_id` 创建或复用对应 claim。
3. 等待 claim ready。
4. 等待 sandbox 通过 gRPC 连接 `clawman`。
5. `clawman` 在流上发送 `messages[]` 批次。
6. 发送成功后把当前批次标记为 `sent`。
7. agent 返回 `result`。
8. 主服务把 reply 写入 `jobs`。
9. `jobs` 写入成功后，把参与处理的 `messages` 标记为 `done`。

### 9.2 活跃会话
1. 新消息到达。
2. 主服务直接复用现有 claim 和 room 连接。
3. 再次通过同一 room 的 gRPC 流下发 `messages[]`。
4. 不再经过 router/HTTP，也不再等待 Redis ingress 被 sandbox 消费。

## 10. 失败处理

1. `SandboxClaim` 创建失败
   - 当前消息保持 `messages(status=pending)`，下一轮重试。
2. `SandboxClaim` 长时间未 ready
   - 视为当前消息失败，记日志并重试。
3. gRPC 批次发送失败
   - 当前消息保持 `pending`，由后续 dispatch 重试。
4. agent 返回 `error`
   - 当前批次从 `sent` 恢复到 `pending`。
5. `jobs` 写入失败
   - 当前批次从 `sent` 恢复到 `pending`。
6. `jobs` 写入成功但 `messages.done` 更新失败
   - 当前批次停留在 `sent`；服务下次启动时会通过 `ResetSentMessages()` 恢复到 `pending`，当前版本接受该重复窗口。

## 11. 观测与告警

当前代码已提供：
- `tinyclaw_messages_pulled_total`
- `tinyclaw_messages_dispatched_total`
- `tinyclaw_messages_skipped_total{reason}`
- `tinyclaw_sandbox_invocations_total{result}`
- `tinyclaw_sandbox_duration_seconds`
- `tinyclaw_db_duration_seconds{operation}`
- `tinyclaw_pull_cycle_errors_total`

仍需补充：
- gRPC 连接数、断线次数、等待 room connect 超时次数
- `messages(status=sent)` backlog
- `jobs.done` 更新失败次数
- `activeSandboxes` 与实际 claim 生命周期的绑定

## 12. 本轮确认约束

1. 官方控制面接口统一使用 `SandboxTemplate` 与 `SandboxClaim`。
2. 当前数据面统一使用 `clawman` gRPC gateway，不再使用 `sandbox-router + HTTP runtime`。
3. 不再给 sandbox 注入 per-room Redis ingress 配置。
4. 主服务最小状态统一落 PostgreSQL，不再依赖 Redis。
5. v0 默认软休眠，不启用 warm pool。
