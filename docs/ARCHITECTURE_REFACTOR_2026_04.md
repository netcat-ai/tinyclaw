# TinyClaw Architecture Refactor Plan (2026-04)

## 1. 目标

本文件只记录这轮架构调整已经确认的结论：
- 哪些边界保留
- 哪些边界改变
- `clawman` 和 sandbox 分别负责什么
- 第一阶段内部通信怎么做
- 预计会改哪些模块

它描述的是下一阶段目标架构，不等同于当前 v0 已上线实现。

## 2. 当前结论

### 2.1 保留

- 保留 `room_id -> sandbox` 作为执行隔离边界。
- 保留 `clawman` 作为外部渠道统一入口。
- 保留 `messages` 作为入站事实源。
- 保留 `jobs` 作为当前外发任务承载结构。
- 群聊 `trigger` 暂时继续放在 `clawman`，作为粗准入与控噪层。
- 用户创建的定时任务继续由 `clawman` 统一持久化、调度与控制。

### 2.2 改变

- 不再把 `sandbox-router + HTTP /agent` 视为长期数据面。
- `clawman` 收敛为统一 `message gateway`。
- `clawman` 不再负责业务级事件编排，只负责把 `messages[]` 传给对应 room sandbox。
- sandbox 不直接持有企业微信 / 飞书 / email 等第三方协议状态。

## 3. 目标拓扑

```text
External Channels
  - WeCom
  - Feishu
  - Email
          |
          v
+-----------------------------+
| clawman                     |
| - channel adapters          |
| - messages store            |
| - coarse trigger            |
| - scheduler                 |
| - message gateway           |
| - gRPC server               |
| - jobs / delivery           |
+---------------+-------------+
                ^
                | sandbox dials clawman
                | gRPC streaming
                |
+-----------------------------+
| room sandbox                |
| - room runtime state        |
| - workspace                 |
| - agent logic               |
| - gRPC client               |
+-----------------------------+
```

## 4. 职责边界

### 4.1 clawman

`clawman` 负责：
- 外部渠道 ingress / egress
- 渠道认证、cursor、同步状态
- 入站消息规范化并持久化到 `messages`
- 群聊粗 trigger 判定
- 用户创建的 schedule / reminder / cron 类任务的持久化、调度与停启控制
- 暴露供 sandbox 连接的 `gRPC server`
- 把某个 room 当前应处理的 `messages[]` 传给 sandbox
- 承接 sandbox 触发的发送动作，并映射到真实渠道

### 4.2 sandbox

sandbox 负责：
- 按 room 保持独立工作目录与执行上下文
- 启动后主动连接 `clawman` 的 `gRPC server`
- 建连时上报自己的 `sandbox_id`
- 接收 `clawman` 传来的 `messages[]`
- 自行判断这些消息代表：
  - 新任务
  - 追加消息
  - 是否需要打断当前执行
- 自行决定何时调用 `clawman` 的发送能力

## 5. trigger、消息批次、定时任务

### 5.1 trigger

- 当前不下放到 sandbox。
- `clawman` 继续负责群聊粗 trigger，当前逻辑保持不变（私聊默认触发，群聊为@或内容包含关键字）
- sandbox 被唤醒后，仍可自行决定是否回复、是否只记录不回应。

### 5.2 定时任务

- 用户在群聊或私聊中创建的定时任务，统一由 `clawman` 调度。
- 到点后由 `clawman` 把对应消息投递给 room sandbox。
- 第一阶段不把 scheduler 下放到 sandbox。

### 5.3 room 归属

- `clawman` 直接为某个 `room_id` 创建对应的 `SandboxClaim`。
- sandbox 启动后上报自己的 `sandbox_id`。
- `clawman` 通过 `sandbox_id -> room_id` 的映射识别该 sandbox 所属 room。
- 一个 sandbox 在当前生命周期内只归属于一个 room。

## 6. 第一阶段内部协议

### 6.1 连接方向

- `gRPC server` 放在 `clawman`
- sandbox 作为 `gRPC client` 主动连接 `clawman`
- `Connect` 建连时必须声明 `sandbox_id`
- `Connect` 可选声明当前 `room_id`
- 当前版本先不做 gRPC 鉴权
- 当前实现建议配置名：
  - `CLAWMAN_GRPC_ADDR`

### 6.2 协议原则

- 第一阶段不设计内部业务事件类型。
- 第一阶段只传 `messages[]`。
- `Connect` 是唯一的注册入口：
  - 上报 `sandbox_id`
- `messages[]` 保留最小字段集：
  - `seq`
  - `msgid`
  - `room_id`
  - `from_id`
  - `from_name`
  - `msg_time`
  - `payload`

### 6.3 sandbox -> clawman

第一阶段最小输出只保留两类：
- `result`
- `error`

说明：
- 第一阶段不保留应用层 `ack`。
- gRPC 调用成功表示本次消息批次已送达；调用失败则保留在重试路径中。
- `result`：只携带最终输出文本，由 `clawman` 写入 `jobs`，再由发送线程处理。
- `error`：表示本次处理失败，供 `clawman` 做重试、诊断和告警。

第一阶段不扩展更多过程输出。

## 7. 不做什么

本轮不做：
- 不回退到 Redis per-room mailbox
- 不让 sandbox 直接实现外部渠道客户端
- 不在第一阶段把 scheduler 下放到 sandbox
- 不同时重做长期记忆、文件上下文、warm pool

## 8. 预期改动范围

### 8.1 数据与配置

保留：
- `messages`
- `jobs`

可能新增：
- gRPC gateway 地址配置
- scheduler / automation 相关持久化结构
- 消息传输关联字段

### 8.2 代码

高概率改动：
- `clawman.go`
- `store.go`
- `config.go`
- `sandbox/orchestrator.go`
- `agent/src/server.ts`
- `agent/src/runtime.ts`
- `README.md`
- `docs/NEXT_STEPS.md`
- `docs/ARCHITECTURE_V0.md`
- `docs/AGENT_SANDBOX_INTEGRATION_V0.md`

可能新增：
- `proto/` 或等价的 gRPC contract 定义
- gRPC server / client 集成代码
- 消息传输集成测试

## 9. Review Items

当前文档已经足够描述方向，但还不足以直接支持一次性研发。下面按 review 条目合并列出“仍需补充的问题”和“推荐做法”，方便逐条批注。

### 9.1 gRPC 契约

待确认：
- `service` 和 `method` 的具体定义
- `Connect` 中 `sandbox_id` 的字段定义
- `messages[]` 的完整字段集
- `result / error` 的最小字段集
- 连接是否为单条长 stream

建议：
- 第一阶段只保留一个最小 service，承载 `messages[]` 输入和 `result / error` 输出。
- `Connect` 建连时明确声明 `sandbox_id`。
- 命名保持长期有效，避免引入临时性的 `event`、`session_update` 一类词。

> Service: Clawman, Method: Connect。建连时声明 `sandbox_id`。消息字段就用 `messages[]`，输出字段只保留 `result`、`error`，不引入任何业务语义。

原因：
- 协议过大一定返工。
- 命名一旦污染，后续很难收拾。

### 9.2 可靠性与恢复

待确认：
- `error` 之后由谁触发重试
- 断线重连后如何恢复未完成消息
- 重复投递如何去重

建议：
- 把 `error`、重连恢复点和状态推进规则写死在协议里，不靠口头约定。
- 先设计失败路径，再设计 happy path。

> 不需要 ack，请求通过 gRPC 调用成功即表示消息已送达，调用失败则表示未送达，需要重试。现在默认就是轮询 `pending` 消息，如果失败进入下一次轮询即可。
> 这批消息发送成功后，状态从 `pending` 要修改成 `sent`，等后续接收到 `result` 后，再修改成 `done`（将 room 中 `min_seq <= seq <= max_seq` 的记录）。

原因：
- 这类系统真正的复杂度在失败恢复，不在正常路径。
- 如果不先写清，代码实现会自然分叉。

### 9.3 scheduler 输入语义

待确认：
- 定时任务投递给 sandbox 时，是否复用普通 `messages[]` 结构
- scheduler 生成的输入是否需要单独标记来源
- 定时任务取消、停用、修改后的行为边界

建议：
- scheduler 统一作为输入生产者。
- 对 sandbox 来说，定时任务尽量仍表现为一批待处理消息，而不是独立业务流。

> OK

原因：
- 输入模型统一后，测试、重放和恢复都会简单很多。
- 避免“普通消息”和“定时消息”两套处理逻辑长期并存。

### 9.4 sandbox -> clawman 发送路径

待确认：
- `result` 的具体字段集
- 一次 invocation 能否触发多条发送
- 发送失败时，由谁负责重试与补偿

建议：
- `clawman` 继续做平台层发送执行者，sandbox 不直接接触外部渠道协议。
- 第一阶段只定义最小发送路径，不预扩展复杂过程输出。
> `result` 只带最终输出，由 `clawman` 写入 `jobs`，再由发送线程处理。

原因：
- 第三方渠道协议应该稳定收敛在平台层。
- 第一阶段先把正确性做稳，再决定是否需要更细粒度回传。

### 9.5 部署与鉴权

待确认：
- `CLAWMAN_GRPC_ADDR` 之外是否还需要鉴权 token 或 mTLS
- `clawman` gRPC server 在集群内的暴露方式
- sandbox 启动时地址如何注入
- `sandbox_id` 的生成与持久化方式

建议：
- 保持连接方向固定：sandbox 主动连接 `clawman`。
- 先把地址注入方案和 `sandbox_id` 方案定死。
- 鉴权先进入待办，等主链稳定后再补 token 或 mTLS。

> 当前版本先不做 gRPC 鉴权；后续再补 token 或 mTLS。

原因：
- 连接方向一旦反复摇摆，部署方案会跟着一起漂移。
- 地址、凭据、暴露方式应该一起设计，而不是拆开补。

### 9.6 测试与验收

待确认：
- 最小集成测试矩阵
- 断线恢复测试
- scheduler 投递测试
- 群聊 trigger 测试

建议：
- 继续以 `messages` 为事实源
- 第一阶段测试优先覆盖：trigger、scheduler、断线恢复、重复投递、`pending -> sent -> done` 状态推进。

> OK

原因：
- 这些点最容易决定这套架构是否真的可运行。
- AI 能加快实现，但不能替代协议与恢复语义的验证。

## 10. 分阶段迁移

### Phase 1

- 保留现有 trigger 与 scheduler 语义
- 保留当前 `messages` / `jobs`
- 明确 `sandbox_id -> room_id` 映射规则
- 明确 `messages[]` 的最小字段集
- 明确 `CLAWMAN_GRPC_ADDR` 一类配置项

### Phase 2

- 在 `clawman` 落地 gRPC server
- 在 sandbox 落地 gRPC client
- 用 `messages[]` 传输替换当前同步 `/agent` 数据面
- 引入 `pending -> sent -> done` 状态推进

### Phase 3

- 收敛 sandbox -> clawman 的发送路径
- 明确 `jobs` / delivery 的长期语义
- 把 scheduler 事件统一接入同一 room message pipeline

## 11. 用法

这份文件是本轮架构调整的主文档。

后续流程：
- 先继续在本文件上批注
- 批注稳定后再拆实现 todo
- todo 稳定后再进入代码实现
