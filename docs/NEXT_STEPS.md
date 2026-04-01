# tinyclaw 下一步（基于官方 agent-sandbox 接口）

## 2026-04-01 新增方向
- 保留当前 `SandboxTemplate + SandboxClaim` 资源模型作为已运行基线。
- 下一阶段把 `clawman` 收敛为统一 `message gateway`，统一对接外部通道，不让 sandbox 自己持有第三方协议状态。
- 设计 `messages[]` 传输协议，替换“同步 `/agent` 最终态 JSON”这一数据面契约。
- 内部通信优先评估“`clawman` 提供 gRPC server，sandbox 主动连接”的 `gRPC streaming` 方案；`WebSocket` 仅作为探索期备选。

## 2026-04-01 Research 结论
- 当前代码没有独立业务轮次实体；dispatch 每次把某个 room 下全部 `pending` 消息聚合成一次处理批次。
- 该处理批次的完成条件是：sandbox 成功返回，且 reply 成功写入 `jobs`；随后这批 `messages` 才会整体更新为 `done`。
- 失败路径不会记录独立批次状态；系统通过保留 `messages(status=pending)` 来实现重试，因此下一轮 dispatch 可能会重放同一批或更大一批消息。
- 群聊触发语义当前依赖 `buffered -> pending` 提升；这部分业务规则仍在 `clawman`，尚未进入 sandbox。
- 因此下一阶段若引入 gRPC 消息传输，应继续围绕 `messages`、`pending` 和消息批次来设计，而不是引入新的业务轮次概念。

## MVP 目标
在企业微信中实现最小可用闭环：
- Ingress 拉取真实消息并解密。
- 按 `room_id` 拉起或复用当前进程内 sandbox session。
- sandbox 通过 gRPC 主动连接 `clawman` 并接收 `messages[]`。
- agent 成功后把回复写入 PostgreSQL `jobs`，再由 Android 发送端轮询拉取。
- `jobs` 写入成功后把对应 `messages` 标记为 `done`。

## 第一阶段（已完成）
1. 明确 `room_id` 规则与主服务职责边界。
2. 落地 `SandboxTemplate` / `SandboxClaim` 基础集成。
3. 把 `agent` 改成 HTTP runtime，不再自拉 Redis ingress。
4. 打通主服务到 sandbox 的基础生命周期链路。

## 第二阶段（当前进行）
1. 在集群内部署并固化：
   - agent-sandbox core
   - agent-sandbox extensions
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
1. 评估“`jobs` 写入成功但 `messages.done` 更新失败”的重复出队窗口与幂等策略。
2. 引入 warm pool，降低冷启动。
3. 评估 idle / hibernate / terminate 自动化策略。
4. 接入长期记忆与文件上下文能力。
5. 评估并实现 `clawman` gRPC server + sandbox gRPC client 的 `messages[]` 传输协议。
6. 明确 `clawman` gRPC 暴露方式、sandbox 连接方式与配置项命名。

## 部署专项
1. 固化 `SandboxTemplate` 契约：
   - 统一镜像
   - 统一端口 `8888`
   - `/healthz` readiness / liveness
   - Claude 运行时环境变量
   - `CLAWMAN_GRPC_ADDR`
2. 固化 `clawman` gRPC server 暴露方式与 sandbox 连接方案。

## PostgreSQL 最小范围（当前版）
- `messages`：企业微信 archive 入站事实、状态机、`seq` checkpoint
- `jobs`：Android 发送端增量拉取的外发任务
- `wecom_app_clients`：Android 客户端认证配置

## 验收标准
1. 任意一条企业微信消息可触发对应 sandbox ready 并拿到回复。
2. 主服务不再写 `stream:i:{room_id}` 给 sandbox 消费。
3. sandbox 通过 gRPC 主动连接 `clawman`，而不是经过 router/HTTP。
4. 同一 `room_id` 的 sandbox 标识稳定且可复用。
5. `jobs` 写入失败时，对应 `messages` 保持 `pending` 并可在后续 dispatch 中重试。

## 文档 Review 清单
1. 不再出现“agent 在 sandbox 内自拉 Redis Stream”的描述。
2. 不再出现“主服务直接创建 Sandbox CR”的描述。
3. 统一使用 `SandboxTemplate`、`SandboxClaim` 术语。
4. 不再出现“Redis 仍承担主服务状态或 egress”的描述。
5. agent 镜像设计先不绑定 Tailscale，网络扩展后置。
