# tinyclaw 下一步（基于官方 agent-sandbox 接口）

## MVP 目标
在企业微信中实现最小可用闭环：
- Ingress 拉取真实消息并解密。
- `ensure(room_id)` 通过 `SandboxClaim` 拉起或复用 sandbox。
- 主服务通过 `sandbox-router` 调用 sandbox 内 `agent` 的 HTTP 接口。
- 回复写入 `stream:o:{room_id}`，由统一 egress 回发企业微信。

## 第一阶段（已完成）
1. 明确 `room_id` 规则与主服务职责边界。
2. 落地 `SandboxClaim` create-or-get + ready wait。
3. 把 `agent` 改成 HTTP runtime，不再自拉 Redis ingress。
4. 打通主服务到 sandbox 的 router 调用链路。

## 第二阶段（当前进行）
1. 在集群内部署并固化：
   - agent-sandbox core
   - agent-sandbox extensions
   - `sandbox-router`
   - `SandboxTemplate`
2. 补充真实环境联调：
   - 首条消息冷启动
   - 活跃会话热路径
   - 失败重试与错误可见性
3. 接入 sandbox 级别监控：
   - `sandbox_ready_latency_ms`
   - `sandbox_invoke_latency_ms`
   - `reply_e2e_ms`

## 第三阶段
1. 完善 ACK/重试/DLQ 链路。
2. 引入 warm pool，降低冷启动。
3. 评估 idle / hibernate / terminate 自动化策略。
4. 接入长期记忆与文件上下文能力。

## 部署专项
1. 固化 `SandboxTemplate` 契约：
   - 统一镜像
   - 统一端口 `8888`
   - `/healthz` readiness / liveness
   - Claude 运行时环境变量
2. 固化 router 访问契约：
   - `X-Sandbox-ID`
   - `X-Sandbox-Namespace`
   - `X-Sandbox-Port`
3. 验证命名约定：
   - `SandboxClaim.name = clawagent-{room_id_lower}`

## Redis 保留范围（当前版）
- `msg:seq`：企业微信拉取游标
- `lock:ensure:{room_id}`：ensure 防抖锁
- `wecom:*`：详情缓存
- `stream:o:{room_id}`：egress reply stream
- `dlq:reply`：回发死信

## 验收标准
1. 任意一条企业微信消息可触发对应 `SandboxClaim` ready 并拿到回复。
2. 主服务不再写 `stream:i:{room_id}` 给 sandbox 消费。
3. sandbox 通信统一经过 router/HTTP，而不是 Redis ingress。
4. 同一 `room_id` 的 sandbox 标识稳定且可复用。
5. 回发失败可重试，超过阈值进入 DLQ。

## 文档 Review 清单
1. 不再出现“agent 在 sandbox 内自拉 Redis Stream”的描述。
2. 不再出现“主服务直接创建 Sandbox CR”的描述。
3. 统一使用 `SandboxTemplate`、`SandboxClaim`、`sandbox-router` 术语。
4. Redis 的职责只保留在主服务侧状态与 egress，不再承担 sandbox ingress。
5. agent 镜像设计先不绑定 Tailscale，网络扩展后置。
