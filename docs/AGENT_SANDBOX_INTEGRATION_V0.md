# TinyClaw + Agent Sandbox Integration v0

## 1. 目标

把 `kubernetes-sigs/agent-sandbox` 作为 TinyClaw 的执行层底座，并明确 `agent` 容器内部镜像的运行契约，实现：
- 每个 `room_id` 对应独立可控 sandbox。
- Ingress 收到新消息后写入对应 room stream，并调用 `ensure` 保证 sandbox 可用。
- Agent 在 sandbox 内自行持续拉取 Redis Stream，处理并回发。
- v0 先采用软休眠，agent 在无消息时阻塞等待，不在镜像内实现硬休眠恢复。

非目标：
- 不在 v0 引入跨会话共享 sandbox。
- 不在 v0 做中断恢复（仍使用 `append` 策略）。
- 不在 v0 处理 tailnet / Tailscale 接入。
- 不建设独立 room registry 表。

## 2. 组件划分

1. `Ingress Service`
   - 拉取企业微信消息并标准化。
   - 按 `stream:room:{room_id}` 写入消息流。
   - 调用 `ensure`，保证目标 sandbox 存在或被唤醒。

2. `Sandbox Orchestrator`
   - 调用 K8s API 创建/更新 `Sandbox`。
   - 生命周期操作：`create/get/terminate`。
   - 运行态以 K8s `Sandbox` / Pod 状态为准。

3. `Agent Runtime (in Sandbox)`
   - 使用 `XREADGROUP BLOCK` 持续拉取 `stream:room:{room_id}`。
   - 串行处理消息、调用模型与工具、回发企业微信。
   - 容器内只负责 agent 运行时初始化、消息消费和优雅退出。

## 3. 资源映射

建议命名规范（K8s）：

```text
Sandbox.name = tinyclaw-agent-{hash(room_id)}
labels:
  tinyclaw/room_id: "{room_id}"
  tinyclaw/tenant_id: "{tenant_id}"
  tinyclaw/chat_type: "{chat_type}"
annotations:
  tinyclaw/idle_after_sec: "300"
  tinyclaw/terminate_after_sec: "86400"  # v1 再自动化
```

说明：
- v0 直接使用 `Sandbox` 作为控制面对象，不要求 `SandboxClaim`。
- 使用确定性命名，`ensure` 采用 create-or-get。
- 不单独维护 `room_runtime` 表。

## 4. Agent 镜像设计目标

agent 镜像应满足以下约束：
- 单镜像可直接作为 sandbox 的 `PodTemplate.spec.containers[0].image`。
- 容器启动后可自行完成运行时初始化和消息消费。
- 容器关闭时可优雅停止 agent 进程。
- 镜像尽量不要求额外 Linux capability、`/dev/net/tun` 或特权模式。
- 允许后续替换内部 agent runtime 实现，但外部契约保持稳定。

建议镜像内包含的基础组件：
- `bash` / `sh`
- `ca-certificates`
- `curl`
- `git`
- `jq`
- `redis-cli` 或等价调试工具
- `tini` 或等价 PID 1 init
- agent runtime 所需解释器和 CLI（例如 Node.js、Claude Code runtime、辅助脚本）

## 5. Agent 进程模型

v0 采用“单容器、双进程”模型：

```text
PID 1: tini / entrypoint
  └─ agent-runtime
```

启动顺序：
1. entrypoint 创建运行目录、校验必需环境变量。
2. 初始化 agent runtime 所需本地目录和配置。
3. agent runtime 确保 Redis consumer group 存在。
4. agent runtime 进入 `XREADGROUP BLOCK` 循环。

关闭顺序：
1. `SIGTERM` 先发给 agent runtime，等待其停止接单并完成当前清理。
2. entrypoint 以非零码传播启动失败，以零码传播正常退出。

约束：
- v0 不在镜像内实现 supervisor 重启策略；进程异常退出后由 sandbox / K8s 拉起。
- v0 不在镜像内做 hibernate / resume；空闲即阻塞等待。

## 6. Runtime 环境变量契约

必需环境变量：

```text
ROOM_ID
TENANT_ID
CHAT_TYPE

REDIS_ADDR
REDIS_PASSWORD           # 可空
REDIS_DB                 # 默认 0
STREAM_PREFIX            # v0 目标值为 stream:room
CONSUMER_GROUP_PREFIX    # 默认 cg:room
CONSUMER_NAME            # 默认 hostname

WECOM_EGRESS_BASE_URL
WECOM_EGRESS_TOKEN

ANTHROPIC_API_KEY         # 与 CLAUDE_CODE_OAUTH_TOKEN 二选一
CLAUDE_CODE_OAUTH_TOKEN   # 与 ANTHROPIC_API_KEY 二选一
```

可选环境变量：

```text
ANTHROPIC_BASE_URL        # 可选，自定义 Claude API Base URL
CLAUDE_MODEL              # 默认 claude-sonnet-4-5
CLAUDE_SYSTEM_PROMPT_APPEND
CLAUDE_ALLOWED_TOOLS      # 逗号分隔
CLAUDE_DISALLOWED_TOOLS   # 逗号分隔
CLAUDE_MAX_TURNS          # 默认 16

AGENT_IDLE_AFTER_SEC     # 默认 300，仅用于状态打点，不触发硬休眠
AGENT_LOG_LEVEL
AGENT_READ_BLOCK_MS      # 默认 5000，测试和优雅退出可调小
AGENT_WORKDIR            # 默认 /workspace
AGENT_TMPDIR             # 默认 /tmp
```

说明：
- agent 应从 `STREAM_PREFIX` 和 `ROOM_ID` 组合 stream key，而不是在镜像内写死完整键名。
- consumer group 名称可由 `CONSUMER_GROUP_PREFIX + ":" + ROOM_ID` 推导。
- 回发企业微信建议走统一 egress API，而不是让 agent 直连企业微信全部接口。

## 7. 文件系统与卷布局

建议目录约定：

```text
/workspace                 # agent 工作目录
/var/lib/tinyclaw          # agent 本地状态、临时缓存
/var/log/tinyclaw          # 可选，默认仍输出 stdout/stderr
/tmp                       # 临时文件
```

卷建议：
- `/workspace`：`emptyDir`，v0 默认即可。
- `/var/lib/tinyclaw`：`emptyDir`，用于摘要缓存、下载中的临时文件。

说明：
- v0 不依赖 PVC，不要求跨 Pod 保留 agent 本地状态。
- 长期记忆不放在镜像本地文件系统，仍应写回外部存储。

## 8. 启动、就绪与退出

### 8.1 启动检查

entrypoint 应至少检查：
- `ROOM_ID`
- `REDIS_ADDR`
- `STREAM_PREFIX`
- `WECOM_EGRESS_BASE_URL`
- `WECOM_EGRESS_TOKEN`
- `ANTHROPIC_API_KEY` 或 `CLAUDE_CODE_OAUTH_TOKEN`

### 8.2 Readiness

agent 容器可认定为 ready 的条件：
1. Redis 可连通。
2. consumer group 可创建或已存在。
3. agent runtime 主循环已进入阻塞消费态。

### 8.3 Liveness

推荐最小策略：
- agent 主进程存活。
- 最近一次 Redis ping 在阈值内成功。

### 8.4 优雅退出

收到 `SIGTERM` 后：
1. 停止接收新任务。
2. 尽量完成当前消息的本地清理。
3. 未成功回发的消息不 `XACK`，交由 pending/retry 机制处理。

## 9. 消息消费与 ACK 契约

stream 约定：
- Stream: `stream:room:{room_id}`
- Consumer Group: `cg:room:{room_id}`

ACK 规则（简化版）：
- `XACK` 是队列完成的唯一提交点。
- 仅在“业务处理成功 + 回发成功”后 `XACK`。
- 任一环节失败不 `XACK`，由重试机制接管。

消费规则：
1. 启动时确保 consumer group 存在。
2. 使用 `XREADGROUP BLOCK` 持续消费。
3. 同一容器内严格串行处理同一 `room_id` 的消息。
4. v0 固定 `append` 策略，不实现任务中断。

## 10. 端到端时序

### 10.1 首条消息触发

1. Ingress 拉到企业微信消息并标准化。
2. `XADD stream:room:{room_id}`。
3. Ingress 同步调用 `POST /internal/room-runtime/ensure`。
4. Orchestrator create-or-get `Sandbox`。
5. sandbox 就绪后，容器内 entrypoint 启动 agent runtime。
6. agent 确保 consumer group 存在，再 `XREADGROUP BLOCK` 拉到消息并处理。
7. agent 调用统一 egress API 回发企业微信。
8. 回发成功后 `XACK`。

### 10.2 活跃会话

1. 新消息持续 `XADD` 到该 room stream。
2. 同一 `room_id` 的 agent 串行消费，保持顺序。
3. 无新消息时保持阻塞等待，不主动退出。

## 11. 失败处理

1. Sandbox 启动失败
   - 重试 3 次（指数退避），仍失败进入 `runtime_dlq`。
2. Agent 启动失败
   - 缺少必需环境变量或 bootstrap 失败时直接启动失败，由 sandbox 重试。
3. Agent 执行失败
   - 记录失败消息，支持重试或人工重放。
4. Egress 回发失败
   - 进入重试队列，超阈值进入 `dlq:reply`。
5. 重复执行（可接受）
   - 由于不引入额外幂等设计，极端故障下可能出现重复处理或重复回发。

## 12. 观测与告警

核心指标：
- `sandbox_start_latency_ms`
- `agent_bootstrap_latency_ms`
- `room_stream_pending_depth`
- `room_cold_start_ratio`
- `reply_e2e_ms`

告警建议：
- `sandbox.ready` 超时率 > 2%（5 分钟窗口）
- `runtime_dlq` 持续增长
- `room_stream_pending_depth` 持续超过阈值

## 13. 推荐落地顺序

1. 定义 agent image 的 entrypoint 和环境变量契约。
2. 在 agent runtime 内实现 `XREADGROUP BLOCK` 消费循环。
3. 接入统一 egress API、重试和基础健康检查。
4. 完成镜像最小化、启动速度和可观测性优化。
5. 需要访问内网能力时，再单独设计 Tailscale / tailnet 接入。

## 14. 本轮确认约束

1. 不引入 `stream:dispatch`。
2. 由 Ingress 在消息到达时直接触发 `ensure`。
3. 增加 `lock:ensure:{room_id}`（`SET NX EX 3`）防止 ensure 风暴。
4. 不建设独立 `room registry` 表。
5. 暂不维护 `last_seen_at` 等中心化时间戳字段。
6. agent 在 sandbox 内自行拉取并消费 room stream。
7. v0 默认软休眠，不启用自动销毁。
8. v0 agent 镜像先不考虑 Tailscale，聚焦消息消费、回发和运行时契约。
