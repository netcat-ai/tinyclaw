# OneClaw 对话记录（启动会）

记录时间：2026-03-08 12:40:51 CST

## 1. 现状梳理（NanoClaw 当前如何回复企业微信消息）

### 用户
现在是如何回复微信消息的？

### 助手（结论）
- 当前仓库只接了 `wecom` 通道（企业微信）。
- 入站流程：`/api/messages/pull` 轮询拉取 -> 标准化 -> 存 SQLite。
- 触发逻辑：
  - 群聊默认需要 `@助手名`；
  - 私聊默认 `requiresTrigger=false`，可直接触发。
- 主循环从 DB 拉增量消息，组装上下文给容器内 Agent。
- 出站流程：Agent 产出文本后调用 `channel.sendMessage`，通过 `/api/worktool/send` 回发企业微信。
- 图片/文件会尝试下载到 group 的 `incoming-media` 目录，并把路径写入消息上下文。

---

## 2. 问题定义（为什么“不够 AI native”）

### 用户
感觉 nanoclaw 不够 ai native，我想构建一个更native的模型，你和我一起探讨下。

### 助手（结论）
建议把目标从“聊天机器人”升级为“持续认知体”，核心标准：
1. Always-on（持续会话、可中断）
2. Stateful（分层记忆）
3. Agentic（拆解/执行/验证）
4. Actionable（事务化工具调用）
5. Self-improving（评估与学习闭环）
6. Governable（权限/预算/风险策略）

并给出方向：先做 agent runtime 的架构升级，不先训基础大模型。

---

## 3. 新方案草图（用户提出）

### 用户
我先说下我自己的想法，有一个主服务，调用企业微信接口获取消息，然后按照 group 写入 redis 消息队列；每个 group 都启动一个 agent 去消费。
agent启动后就去从redis队列中拉取对应的消息，开始消费。
我希望这个 agent 就是 claude code，并将结果通过 api 发送到微信。

### 助手（结论）
- 方案方向正确，建议落地为：
  - `wecom-ingress`（入站）
  - `redis stream`（事件流）
  - `group worker/actor`（每组串行消费）
  - `claude runtime`（执行与推理）
  - `wecom egress`（回发）
- 强烈建议 Redis 用 **Stream** 而不是 List：
  - `XADD / XREADGROUP / XACK / XPENDING / XCLAIM`
  - 更适合 ack/retry/dlq/replay
- 关键工程点：
  1. 幂等（入队、出队、回发三段幂等）
  2. 同组互斥（每组单 active worker）
  3. 长任务期间新消息处理（追加/中断策略）
  4. 回发重试与限流
  5. 崩溃恢复（pending reclaim）

---

## 4. 项目启动指令

### 用户
在新的目录 `~/git/oneclaw` 将刚刚的对话记录下，我们开始这个项目。

### 当前状态
- 已创建目录：`/Users/fish/git/oneclaw`
- 本文件即为启动会记录。

