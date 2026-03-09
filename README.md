# oneclaw

## 文档
- [架构设计草案 v0](./ARCHITECTURE_V0.md)
- [Agent Sandbox 集成设计 v0](./AGENT_SANDBOX_INTEGRATION_V0.md)
- [下一步执行清单](./NEXT_STEPS.md)

## 当前共识（2026-03-09）
1. 会话键：`session_key = {chat_id_or_user_id}`，`tenant_id` 和 `chat_type` 作为独立字段保留。
2. Redis 设计：每个会话一个 stream，键为 `stream:session:{session_key}`。
3. 不引入 `stream:dispatch`，由 Ingress 收到新消息后直接触发 `ensure(session_key)`。
4. agent 在 sandbox 内自行 `XREADGROUP BLOCK` 持续拉取消息并串行消费。
5. 不建设独立 `session registry` 表，暂不维护中心化 `last_seen_at`。
6. 交付语义为 at-least-once，必须做入流/消费/回发三段幂等。
7. 空闲策略先采用软休眠（阻塞等待），硬休眠（退出/缩容）后续按压测引入，自动销毁后置到 v1。

## 项目目标
构建云端 AI Agent Runtime，让企业员工可在企业微信私聊/群聊中与 agent 交互：
- 每个会话运行在隔离 sandbox 中。
- agent 可自由执行代码（在安全边界内）并访问企业内部/外部工具。
- 记忆与人格等结构化数据放在智能表格，文件上下文放在企业云盘。
- 主服务负责消息拉取、会话分发、唤醒与治理；agent 负责消费、执行与回发。
