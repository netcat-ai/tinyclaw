# TinyClaw Room Memory v0

> 状态说明：本文档是独立草案，用于沉淀 room 级长期 memory 设计，避免与当前正在编辑的主架构文档产生冲突。后续确认后再合并回 `README.md`、`ARCHITECTURE_V0.md`、`ARCHITECTURE_REFACTOR_2026_04.md`。

## 1. 目标

为 TinyClaw 增加 `room_id` 级长期 memory 能力，并保持现有边界：
- 保留 `room_id -> sandbox` 作为执行隔离边界。
- 保留 `messages` 作为原始入站事实源。
- 不把长期 memory 绑定到某个 sandbox 实例。
- 不让 sandbox 直接持有企业微信智能表格的长期高权限凭据。

本文聚焦以下问题：
- room 的长期聊天记忆由谁主导
- memory 的读写链路应该放在哪一层
- 企业微信智能表格是否适合作为 memory backend

## 2. 结论

### 2.1 记忆归属

room 的长期 memory 应绑定到 `tenant_id + room_id`，而不是绑定到 `sandbox_id`。

原因：
- sandbox 是执行容器，可回收、可重建、可迁移。
- room 才是业务上的连续会话实体。
- 同一个 room 在不同 sandbox 生命周期中应看到同一份长期 memory。

### 2.2 职责划分

采用以下分工：
- `agent` 负责 memory 语义与使用时机。
- `clawman` 负责 memory 能力暴露、权限治理、审计、重试与 backend 适配。

一句话总结：

```text
agent owns memory behavior
clawman owns memory capability and governance
```

这意味着：
- agent 决定“要不要记”“记什么”“什么时候召回”“什么时候压缩”
- clawman 决定“怎么鉴权”“怎么写外部系统”“怎么重试”“怎么审计”

### 2.3 backend 选择

企业微信智能表格可以作为长期 memory backend，但不应作为唯一上下文来源。

更合理的三层结构：
- 短期事实源：`messages`
- 运行期工作记忆：sandbox `/workspace`
- 长期结构化记忆：企业微信智能表格

## 3. 为什么不把长期 memory 直接放在 sandbox 本地

不采用“把长期 memory 保存在 `/workspace` 或容器本地”的方案，原因如下：
- 当前 sandbox 卷是 `emptyDir`，生命周期跟随 Pod。
- sandbox 重建后，本地文件会丢失。
- 本地文件不利于人工运营、审计和跨生命周期恢复。
- 本地文件不适合作为企业级共享数据源。

因此：
- `/workspace` 只适合存当前运行期的 scratchpad、临时 summary、下载文件、待处理 artifact。
- 长期 memory 必须落在 sandbox 外的 durable store。

## 4. Memory 分层

### 4.1 L0: 原始事实

来源：`messages`

特点：
- 完整保留原始入站事实
- 作为唯一聊天原文事实源
- 用于失败重放、审计、补偿和重新提炼

不建议：
- 不把整个 transcript 复制到智能表格
- 不从智能表格反推原始聊天事实

### 4.2 L1: 运行期工作记忆

来源：sandbox 本地状态，例如 `/workspace`

适合保存：
- 当前任务 scratchpad
- 本轮临时 summary
- 当前下载的文件与处理中间结果
- 尚未固化为长期事实的临时观察

特点：
- 允许丢失
- 生命周期与 sandbox 绑定
- 主要服务单个 room 的连续执行体验

### 4.3 L2: 长期结构化记忆

来源：智能表格等 durable backend

适合保存：
- `profile`
- `preferences`
- `facts`
- `open_todos`
- `summary`
- `artifacts`

特点：
- 以 `tenant_id + room_id` 为主维度
- 跨 sandbox 生命周期可恢复
- 可人工查看、修正和运营

## 5. 职责边界

### 5.1 agent

agent 负责：
- 在处理消息前判断需要读取哪些 memory
- 在处理消息后判断哪些信息值得沉淀为长期 memory
- 决定 memory 的压缩、合并、淘汰与升级
- 将 memory 操作表达为结构化 capability 调用

agent 不负责：
- 直接持有智能表格长期凭据
- 直接耦合智能表格 API 细节
- 自己实现鉴权、限流、审计和租户隔离

### 5.2 clawman

clawman 负责：
- 为 sandbox 注入 room-scoped memory capability
- 校验 `tenant_id + room_id + token`
- 把 memory 请求路由到真实 backend
- 对外部 API 做重试、幂等、限流与错误记录
- 保留 memory 访问审计日志

clawman 不负责：
- 仅靠规则决定“哪些聊天内容应该被记住”
- 代替 agent 生成语义 memory

## 6. 推荐链路

### 6.1 读取链路

读取长期 memory 的主导权在 agent。

推荐流程：
1. `clawman` 将新消息投递到 room sandbox。
2. sandbox 内 agent 根据当前任务决定是否读取 memory。
3. agent 调用 `clawman` 暴露的 memory capability。
4. `clawman` 校验 room 身份后读取智能表格。
5. `clawman` 将结构化 memory 返回给 agent。

这样做的原因：
- 并非每次消息都必须读取全部长期 memory。
- 由 agent 决定读哪些 memory，更符合长期运行 agent 的行为模型。
- clawman 仍能控制权限和 backend 细节。

### 6.2 写入链路

写入长期 memory 的主导权也在 agent，但实际写操作由 clawman 执行。

推荐流程：
1. agent 处理完当前消息。
2. agent 判断是否需要沉淀 memory。
3. agent 调用 memory capability，例如 upsert fact / append summary / close todo。
4. `clawman` 负责把请求写入智能表格 backend。
5. backend 写入结果返回给 agent。

不建议让 `clawman` 在没有 agent 判断的情况下，直接依据消息规则写 memory。

## 7. capability 形态与传输

### 7.1 统一信封

当前最新内部协议已经收敛为统一的 `Message` 结构：

```proto
message Message {
  string kind = 1;
  string sandbox_id = 2;
  string room_id = 3;
  string request_id = 4;
  repeated AgentMessage messages = 5;
  string output = 6;
  string error = 7;
}
```

当前已使用的 `kind` 至少包括：
- `connect`
- `messages`
- `result`
- `error`

因此，memory 设计应复用同一条 `RoomChat(stream Message)` 连接，以及同一个 `Message` 信封。

### 7.2 不复用 `AgentMessage`

虽然已经有统一 `Message` 信封，但不应把 memory 请求直接塞进 `AgentMessage/messages[]`。

原因：
- `messages[]` 当前表示聊天事实，是 `messages` 表的结构化投递形态。
- `messages[]` 自带 `seq / msgid / from_id / msg_time` 等聊天语义。
- memory 调用不是聊天事实，不应进入聊天消息的重放与状态机语义。

因此要区分：
- 复用统一 stream 与 `Message` 信封
- 不复用 `AgentMessage` 作为 memory 载体

### 7.3 room-scoped capability

推荐把 memory 暴露为 room-scoped capability，而不是让 agent 直接裸连智能表格。

可选实现：
- gRPC service
- HTTP service
- tool call wrapper

当前优先建议：
- 不新增独立 API
- 继续复用现有 `RoomChat(stream Message)` 连接
- 通过新增 `kind` 扩展 memory 相关消息

无论底层具体字段如何演进，能力边界建议稳定为：
- `memory.get_context`
- `memory.search`
- `memory.upsert_fact`
- `memory.set_preference`
- `memory.append_summary`
- `memory.add_todo`
- `memory.close_todo`
- `memory.list_artifacts`

原则：
- agent 看见的是 memory 语义接口
- backend 细节由 clawman 屏蔽

### 7.4 新格式下的推荐演进

在当前 `Message` 结构下，推荐把 memory 设计成新的 `kind`，而不是新开一条独立传输链路。

推荐方向：
- `kind = connect`
- `kind = messages`
- `kind = result`
- `kind = error`
- `kind = memory_request`
- `kind = memory_response`

但同时要注意：当前 `Message` 只有 `messages / output / error` 这几类业务字段，还不足以优雅承载通用 capability 参数。

因此，后续协议演进建议是：
- 保留统一 `Message` 信封
- 新增适合 capability 的字段，例如：
  - `capability`
  - `payload_json`
  - `status`
- 或者新增专门的 `capability_request` / `capability_response` 字段

设计原则不变：
- 不再开第二条独立 API
- 不把 memory 硬塞进聊天消息字段
- 保持 memory backend 可替换

## 8. bootstrap 与权限模型

### 8.1 注入内容

sandbox 启动时，`clawman` 应注入最小 bootstrap 信息：
- `tenant_id`
- `room_id`
- memory gateway 地址
- 短期有效的 room-scoped token
- token 过期时间

### 8.2 权限范围

token 至少应限制到：
- 单一 `tenant_id`
- 单一 `room_id`
- 指定 capability 集
- 指定有效期

不建议：
- 给 sandbox 注入企业级全局长期 token
- 让任意 room 访问其他 room 的 memory

## 9. 智能表格作为 backend 的使用边界

智能表格适合承载：
- 低频更新的结构化事实
- 可人工查看和编辑的长期记忆
- 带状态的事项列表
- room 摘要与偏好配置

智能表格不适合承载：
- 高频逐条写入的原始 transcript
- 极低延迟的热路径随机读写
- 大规模向量检索

因此，推荐使用方式是：
- 原始聊天继续落 `messages`
- agent 在需要时调用 memory capability
- clawman 将提炼后的结构化数据写入智能表格

## 10. 最小数据模型建议

如果使用智能表格，建议至少拆成以下逻辑表：

### 10.1 room_profile

字段建议：
- `tenant_id`
- `room_id`
- `chat_type`
- `display_name`
- `language`
- `persona_notes`
- `updated_at`

### 10.2 room_facts

字段建议：
- `tenant_id`
- `room_id`
- `fact_key`
- `fact_value`
- `confidence`
- `source_msgid`
- `updated_at`

### 10.3 room_todos

字段建议：
- `tenant_id`
- `room_id`
- `todo_id`
- `content`
- `status`
- `owner`
- `due_at`
- `source_msgid`
- `updated_at`

### 10.4 room_summaries

字段建议：
- `tenant_id`
- `room_id`
- `summary_kind`
- `content`
- `covers_min_seq`
- `covers_max_seq`
- `updated_at`

说明：
- 这是逻辑模型，不要求第一版物理上一定拆成多张表。
- 第一版也可以收敛为一张智能表格，多列区分记录类型。

## 11. 失败与补偿

### 11.1 读取失败

若 memory 读取失败：
- 不应阻塞本轮消息处理
- agent 应退化为仅基于 `messages[]` 和本地工作目录继续执行
- clawman 应记录错误并暴露监控

### 11.2 写入失败

若 memory 写入失败：
- 不应导致本轮用户回复回滚
- 应记录失败事件并允许后续补偿重试
- 必要时可把 memory write 记录到内部 outbox

### 11.3 幂等

memory 写入应尽量支持幂等：
- 事实类更新采用 upsert
- summary 类更新带覆盖范围
- todo 类更新带稳定 `todo_id`

## 12. v0 建议

在不破坏当前架构的前提下，建议按以下顺序推进：

1. 先定义 agent 可调用的 memory capability contract。
2. 在 `clawman` 中实现 room-scoped memory proxy。
3. 先接通最小 backend：
   - `room_profile`
   - `room_facts`
   - `room_summaries`
4. 先让 agent 只做：
   - 读取 summary / facts
   - 写入 summary / facts
5. `todo`、artifact、复杂检索后置。

## 13. 最终结论

room memory 更适合由 agent 主导，而不是由 clawman 规则驱动。

但 agent 不应直接持有智能表格的原生长期权限，而应通过 clawman 提供的 room-scoped capability 访问 memory backend。

因此，推荐的稳定方案是：

```text
memory semantics and timing -> agent
memory access, auth, audit, retry, backend integration -> clawman
durable room memory backend -> WeCom smart sheet
```
