# OneClaw + Agent Sandbox Integration v0

## 1. 目标

把 `kubernetes-sigs/agent-sandbox` 作为 OneClaw 的执行层底座，实现：
- 每个 `session_key` 对应独立可控 sandbox。
- Ingress 收到新消息后写入对应 session stream，并幂等 `ensure` sandbox 可用。
- Agent 在 sandbox 内自行持续拉取 Redis Stream，处理并回发。
- 一段时间无新消息后，agent 进入休眠策略（软休眠或硬休眠）。

非目标：
- 不在 v0 引入跨会话共享 sandbox。
- 不在 v0 做中断恢复（仍使用 `append` 策略）。
- 不建设独立 session registry 表。

## 2. 组件划分

1. `Ingress Service`
   - 拉取企业微信消息并标准化。
   - 按 `session_key` 写入 `stream:session:{session_key}`。
   - 调用 `ensure`，保证目标 sandbox 存在或被唤醒。

2. `Sandbox Orchestrator`
   - 调用 K8s API 创建/更新 `SandboxClaim`。
   - 生命周期操作：`create/activate/sleep/terminate`。
   - 运行态以 K8s `SandboxClaim/Pod` 状态为准。

3. `Agent Runtime (in Sandbox)`
   - 使用 `XREADGROUP BLOCK` 持续拉取 `stream:session:{session_key}`。
   - 串行处理消息、调用模型与工具、回发企业微信。
   - 根据空闲时长执行休眠策略。

## 3. 资源映射

建议命名规范（K8s）：

```text
SandboxClaim.name = oneclaw-sbx-{hash(session_key)}
labels:
  oneclaw/session_key: "{session_key}"
  oneclaw/tenant_id: "{tenant_id}"
  oneclaw/chat_type: "{chat_type}"
annotations:
  oneclaw/idle_after_sec: "300"
  oneclaw/hibernate_after_sec: "1800"   # v0 可选
  oneclaw/terminate_after_sec: "86400"  # 建议 v1 自动化
```

说明：
- 使用确定性命名，`ensure` 可以幂等 create-or-get。
- 不单独维护 `session_runtime` 表。

## 4. 状态机与触发

```text
cold -> starting -> active -> idle -> hibernated -> terminated
```

触发规则：

1. `message.received`
   - Ingress `XADD stream:session:{session_key}`。
   - Ingress 调用 `ensure(session_key)`。
   - `cold/terminated/hibernated` 会话进入 `starting`。

2. `sandbox.ready`
   - 状态 `starting -> active`。
   - agent 确保 consumer group 存在（不存在则创建）。
   - agent 开始 `XREADGROUP BLOCK` 消费该 session stream。

3. `session.idle_timeout`
   - 由 agent 本地空闲计时器触发。
   - 状态 `active -> idle`，继续空闲可进入 `hibernated`。

4. `message.received` during `hibernated`
   - Ingress 再次触发 `ensure`，状态 `hibernated -> starting -> active`。

5. `session.terminate_timeout`
   - v0 默认不自动触发；由人工或运维任务触发 `terminate`。

## 5. 端到端时序

### 5.1 首条消息触发

1. Ingress 拉到企业微信消息，按 `source_msg_id` 去重。
2. `XADD stream:session:{session_key}`。
3. Ingress 同步调用 `POST /internal/session-runtime/ensure`。
4. Orchestrator create-or-get `SandboxClaim`。
5. sandbox 就绪后，agent 在容器内确保 consumer group 存在，再 `XREADGROUP BLOCK` 拉到消息并处理。
6. agent 回发企业微信。
7. 回发成功后 `XACK`，并打回发幂等标记。

### 5.2 活跃会话

1. 新消息持续 `XADD` 到该 session stream。
2. 同一 session 的 agent 串行消费，保持顺序。
3. 采用 `append`，不抢占当前进行中的任务。

### 5.3 休眠与唤醒

1. 无消息时 agent 持续 `BLOCK` 等待。
2. 超过 `idle_after` 可进入 `idle`。
3. 超过 `hibernate_after` 可执行硬休眠（退出/缩容）。
4. 新消息到来由 Ingress `ensure` 触发唤醒。

## 6. API 草图（OneClaw 内部）

### 6.1 Session Runtime API

```http
POST /internal/session-runtime/ensure
Content-Type: application/json

{
  "session_key": "chat_123",
  "tenant_id": "t_001",
  "chat_type": "group",
  "trace_id": "trace_xxx"
}
```

返回：

```json
{
  "session_key": "chat_123",
  "runtime_state": "starting",
  "sandbox_ref": {
    "namespace": "oneclaw-runtime",
    "name": "oneclaw-sbx-a1b2c3"
  }
}
```

```http
POST /internal/session-runtime/{session_key}/sleep
POST /internal/session-runtime/{session_key}/terminate
```

运行态查询：
- 直接读 K8s `SandboxClaim` / Pod 状态。

## 7. 幂等与一致性

必须保留三段幂等：

1. 入流幂等：`dedup:ingress:{tenant_id}:{source_msg_id}`
2. 消费幂等：`dedup:msg:{tenant_id}:{source_msg_id}`
3. 回发幂等：`dedup:reply:{tenant_id}:{reply_id}`

ACK 规则：
- 仅在“业务处理成功 + 回发成功 + 回发幂等标记成功”后 `XACK`。
- 任一环节失败不 `XACK`，由重试机制接管。

## 8. 失败处理

1. Sandbox 启动失败
   - 重试 3 次（指数退避），仍失败进入 `runtime_dlq`。
2. Sandbox 就绪超时
   - 标记 `starting_timeout`，可自动重建一次。
3. Agent 执行失败
   - 记录失败消息，支持重试或人工重放。
4. Egress 回发失败
   - 进入重试队列，超阈值进入 `dlq:reply`。

## 9. 观测与告警

核心指标：
- `sandbox_start_latency_ms`
- `sandbox_wakeup_latency_ms`
- `session_stream_pending_depth`
- `session_cold_start_ratio`
- `ensure_call_rate`

告警建议：
- `sandbox.ready` 超时率 > 2%（5 分钟窗口）
- `runtime_dlq` 持续增长
- `session_stream_pending_depth` 持续超过阈值

## 10. 休眠策略选项

1. 软休眠（默认）
   - 进程不退出，只是 `XREADGROUP BLOCK` 等待。
   - 优点：无冷启动。
   - 缺点：仍占用少量资源。

2. 硬休眠（可选）
   - agent 退出或缩容到 0。
   - 优点：省资源。
   - 缺点：下一条消息有冷启动。

3. 自动销毁（建议后置到 v1）
   - 依赖可靠的中心化活跃时间来源。
   - 当前未引入 session registry / `last_seen_at`，不作为 v0 默认能力。

建议：MVP 先软休眠，压测后再引入硬休眠；自动销毁后置。

## 11. 推荐落地顺序

1. 实现 `ensure` API（确定性命名 create-or-get）。
2. 接入 Orchestrator（create/ready/sleep/terminate）。
3. 在 agent 内实现 `XREADGROUP BLOCK` 消费循环。
4. 接入幂等、回发、重试和告警。

## 12. 本轮确认约束

1. 不引入 `stream:dispatch`。
2. 由 Ingress 在消息到达时直接触发 `ensure`。
3. 增加 `lock:ensure:{session_key}`（`SET NX EX 3`）防止 ensure 风暴。
4. 不建设独立 `session registry` 表。
5. 暂不维护 `last_seen_at` 等中心化时间戳字段。
6. agent 在 sandbox 内自行拉取并消费 session stream。
7. v0 默认软休眠，不启用自动销毁。
