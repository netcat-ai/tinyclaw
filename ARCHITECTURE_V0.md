# tinyclaw Architecture v0

## 1. 目标与边界

### 1.1 项目目标
tinyclaw 是一个面向企业微信会话的 AI Agent Runtime：
- 员工在企业微信私聊/群聊中与 Agent 交互。
- 每个会话（私聊/群聊）映射到独立 sandbox agent。
- 主服务负责消息拉取、事件入流、分发调度。
- Agent 负责消费消息、执行任务、回发结果。
- Agent 可访问企业数据源（智能表格、云盘）维护记忆和上下文。

### 1.2 非目标（v0 不做）
- 不训练基础模型，不自建模型服务。
- 不做复杂多 agent 协作图（先单会话单 agent）。
- 不做跨租户共享记忆。

## 2. 架构总览

```text
+---------------------------+       +---------------------------+
| WeCom Session Archive API |-----> | Ingress Service           |
| WeCom Send API            | <-----| (pull/normalize/publish)  |
+---------------------------+       +---------------------------+
                                              |
                                              v
                                   +---------------------------+
                                   | Redis Streams             |
                                   | stream:session:{key}      |
                                   +---------------------------+
                                              |
                                   +----------+-----------+
                                   | Session Runtime Mgr  |
                                   | (ensure session pod) |
                                   +----------+-----------+
                                              |
                    +-------------------------+-------------------------+
                    |                                                   |
                    v                                                   v
        +--------------------------+                        +--------------------------+
        | Agent Pod (session A)    |                        | Agent Pod (session B)    |
        | - stream consumer        |                        | - stream consumer        |
        | - model runtime          |                        | - model runtime          |
        | - tool adapter           |                        | - tool adapter           |
        +-------------+------------+                        +-------------+------------+
                      |                                                       |
                      v                                                       v
      +-------------------------------+                          +-------------------------------+
      | Memory Adapter                |                          | Memory Adapter                |
      | - SmartSheet (profile/memory)|                          | - SmartSheet (profile/memory)|
      | - WeDrive (files)             |                          | - WeDrive (files)             |
      +-------------------------------+                          +-------------------------------+
```

## 3. 会话与隔离模型

### 3.1 session_key 规范
建议统一：

```text
session_key = {chat_id_or_user_id}
```

- `chat_id_or_user_id`: 群聊 ID 或员工用户 ID。
- `tenant_id` 与 `chat_type` 作为独立字段保留在事件中，用于审计、统计和权限校验。
- 如果后续发现 `chat_id_or_user_id` 在跨租户场景不是全局唯一，可平滑升级为 `session_key = {tenant_id}:{chat_id_or_user_id}`。

所有核心能力都围绕 `session_key`：
- 路由（该消息发给哪个 agent）。
- 隔离（Pod/命名空间/权限边界）。
- 生命周期管理（休眠/唤醒/销毁）。

### 3.2 隔离等级（建议）
- 运行时隔离：每个 `session_key` 对应一个独立 Pod（或容器实例）。
- 文件系统隔离：每个 session 独立工作目录和 quota。
- 网络隔离：默认 deny，按工具白名单放行目标域名。
- 密钥隔离：按 session 注入最小权限短期凭证（避免平台全局长 token 泄露）。

### 3.3 部署选项（除“每会话独立 Pod”外）
1. Pool Worker（共享进程 + 逻辑隔离）
   - 优点：成本最低、冷启动快。
   - 缺点：隔离性最弱，逃逸和资源争抢风险高。
2. Sandbox Pool（预热沙箱池）
   - 优点：兼顾隔离和冷启动，唤醒速度明显好于按需新建 Pod。
   - 缺点：需要维持预热池容量，调度复杂度更高。
3. 每会话独立 Pod（当前建议）
   - 优点：隔离边界清晰，审计和治理最简单。
   - 缺点：冷启动慢，成本高于池化方案。

## 4. 事件流与消息语义

### 4.1 Redis Stream 设计
采用按会话分流（每个 `session_key` 一个 stream）：

- Stream: `stream:session:{session_key}`
- Consumer Group: `cg:session:{session_key}`

触发方式：
- Ingress 每次拉到新消息后，先 `XADD stream:session:{session_key}`。
- 再调用 `Session Runtime Ensure` 保证对应 sandbox 存在并可消费。
- 不额外引入 `stream:dispatch`。

字段最小集：

```json
{
  "event_id": "uuid",
  "event_type": "message.received",
  "tenant_id": "t_001",
  "session_key": "chat_123",
  "source_msg_id": "wecom_msg_abc",
  "sender_id": "u_001",
  "chat_type": "group",
  "content_type": "text|image|file|mixed",
  "content": "...",
  "attachments": "json-array-string",
  "occurred_at": "2026-03-09T07:00:00Z",
  "trace_id": "trace_xxx"
}
```

### 4.2 交付语义
- 采用 at-least-once（至少一次）交付。
- `XACK` 是队列消费完成的唯一提交点（commit point）。

即使使用 Redis Stream，重复处理仍会发生，典型场景：
1. consumer 处理完业务但尚未 `XACK` 时崩溃，消息会被重新领取。
2. 网络抖动导致 `XACK` 结果不确定，worker 可能重试执行。
3. `XPENDING/XCLAIM` 做故障转移时，原 worker 与新 worker 短时竞争同一条消息。

结论：Redis Stream 解决了可恢复消费，不自动提供“仅一次”语义；v0 接受少量重复执行风险以换取实现简化。

推荐处理顺序（v0）：
1. `XREADGROUP` 获取消息。
2. 执行业务并回发。
3. 回发成功后 `XACK`。

### 4.3 顺序与并发
- 同一个 `session_key` 严格串行消费。
- 不同 `session_key` 并行处理。
- 出现长任务时，新消息进入缓冲队列，策略可选：
  - `append`：追加到当前上下文后继续。
  - `interrupt`：中断当前任务，优先处理新输入。

v0 建议固定为 `append`，避免实现复杂中断一致性。

## 5. Agent 生命周期状态机

```text
cold -> starting -> active -> idle -> hibernated -> terminated
                    ^          |         |            |
                    |          |         |            |
                    +----------+---------+------------+
                           (new event wakeup)
```

状态定义：
- `cold`: 无实例。
- `starting`: 正在拉起 pod，初始化 runtime。
- `active`: 正在消费并执行消息。
- `idle`: 短期空闲，保留内存态（例如 5-15 分钟）。
- `hibernated`: 休眠，持久化必要状态，释放大部分资源。
- `terminated`: 超时销毁。

时间阈值建议（可配置）：
- `idle_after`: 5 分钟无新消息。
- `hibernate_after`: 30 分钟无新消息（v0 可选，默认不启用自动硬休眠）。
- `terminate_after`: 24 小时无新消息（建议在 v1 引入中心化活跃时间后再自动化）。

## 6. 主服务组件职责

### 6.1 Ingress（拉取与标准化）
- 周期拉取企业微信会话存档。
- 将原始消息转为标准事件 schema。
- 处理媒体下载（可选异步）。
- 按 `session_key` 写入 `stream:session:{session_key}`。
- 新消息到达后直接触发 `Session Runtime Ensure`。

### 6.2 Session Runtime Manager（调度）
- 根据 `session_key` 查询（并可短时缓存）SandboxClaim/Pod 状态，判断 agent 是否在线。
- 不在线则触发 K8s 创建（或唤醒）对应 agent pod。
- 不建设独立 session registry 表。

### 6.3 Egress（回发）
- Agent 生成回复后调用统一回发 API。
- 负责频控、重试、错误码归类。
- 回发失败超过阈值进入 DLQ，支持人工重放。

## 7. Memory 设计（智能表格 + 云盘）

### 7.1 记忆分层
- L0 临时上下文：本次会话窗口（内存）。
- L1 会话摘要：每 N 轮生成摘要，落库（Redis 或 DB）。
- L2 长期记忆：结构化存放到智能表格。
- L3 文件知识：存放在云盘，按需检索与引用。

### 7.2 智能表格 schema（建议最小版）
- `persona`: 语气、角色、禁忌、语言偏好。
- `user_profile`: 员工偏好、职责、常见任务。
- `facts`: 事实记忆（key/value/source/updated_at）。
- `tasks`: 任务状态（open/doing/done/blocker）。

写入策略：
- 强一致需求字段（任务状态）立即写。
- 低优先字段（偏好、摘要）批量写或延迟写。

### 7.3 云盘访问策略
- 只读取授权目录，禁止全盘扫描。
- 大文件采用“元数据检索 + 按片读取”。
- 上传更新文件走审计记录（谁在何时改了什么）。

## 8. 安全与治理

### 8.1 权限边界
- 所有工具调用都走 Tool Gateway（统一鉴权和审计）。
- 不允许 agent 直接持有平台 root secret。
- 每次工具调用附带 `trace_id` 和 `session_key`。

### 8.2 资源治理
- pod 级别资源限额（CPU/Mem/ephemeral storage）。
- 单会话 token/分钟与成本预算上限。
- 超预算降级策略：只读模式或请求确认。

### 8.3 审计与合规
- 日志分级：业务日志、工具调用日志、安全日志。
- PII 脱敏后再进入可观测系统。
- 保留消息与动作审计链，支持追溯。

## 9. 可观测性与 SLO

核心指标：
- 入流延迟：`ingress_lag_ms`
- 会话确保延迟：`runtime_ensure_lag_ms`
- 首 token 延迟：`first_token_ms`
- 端到端回复延迟：`reply_e2e_ms`
- 失败率：`reply_error_rate`
- 唤醒成功率：`agent_wakeup_success_rate`

SLO（MVP）建议：
- P95 `reply_e2e_ms` < 8s（纯文本场景）
- 回发成功率 >= 99.5%
- 同 session 顺序错乱率 < 0.1%

## 10. MVP 分阶段落地

### Phase 1: 消息闭环
- Ingress -> `stream:session:{session_key}` -> agent 自拉消费 -> Egress。
- 接入 trace_id 和基础重试链路。

### Phase 2: 会话隔离
- 按 `session_key` 拉起独立 agent pod。
- 先实现 `idle`（软休眠）状态迁移；`hibernate/terminate` 在压测后再启用自动化。

### Phase 3: 记忆与文件
- 智能表格读写（persona/profile/tasks）。
- 云盘文件检索与引用。

### Phase 4: 治理与稳定性
- 预算与限流策略。
- 完整 DLQ + replay 工具。
- 监控告警与压测。

## 11. 待确认决策（需要团队讨论）

已确认：
1. `session_key = {chat_id_or_user_id}`（`tenant_id`、`chat_type` 保留为独立字段）。
2. 长任务策略 v0 固定为 `append`，中断机制后续评估。
3. 记忆写入采用“关键字段实时写 + 低优先字段批量/延迟写”。
4. 不引入 `stream:dispatch`，由 Ingress 在消息到达时直接触发 `ensure`。
5. agent 在 sandbox 内自拉 `stream:session:{session_key}`（`XREADGROUP BLOCK`）。
6. 不建设独立 session registry 表，暂不维护中心化 `last_seen_at`。
7. v0 默认软休眠；自动硬休眠/自动销毁后置。

待确认：
1. Agent Runtime：是否固定使用 Claude Code，还是支持多 runtime 插件化。
2. Session 级别：群聊是否需要进一步按 `thread/topic` 细分子会话。
3. 隔离成本：每会话独立 Pod 与 Sandbox Pool 的切换阈值（QPS、并发会话数、成本上限）。

## 12. 结论

该 v0 方案优先保证四件事：
- 会话隔离可证明。
- 消息语义可恢复（`XACK`、重试、DLQ）。
- Agent 生命周期可控（休眠/唤醒/销毁）。
- 企业数据源可读写且可审计。

在此基础上，再逐步增强 agent 能力，而不牺牲稳定性与安全边界。
