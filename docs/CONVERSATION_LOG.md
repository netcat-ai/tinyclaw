# TinyClaw 对话记录（启动会）

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
在新的目录 `~/git/tinyclaw` 将刚刚的对话记录下，我们开始这个项目。

### 当前状态
- 已创建目录：`/Users/fish/git/tinyclaw`
- 本文件即为启动会记录。

---

## 5. CI/CD 与部署校准（2026-03-10）

### 变更结论
1. CI workflow 从单文件 `ci.yml` 拆分为：
   - `.github/workflows/build.yml`
   - `.github/workflows/deploy.yml`
2. `Deploy` 通过 `workflow_run` 监听 `Build` 成功（`main` 分支）触发，保留 `workflow_dispatch` 手动触发。
3. K8s Deployment 资源名统一为 `clawman`，部署步骤使用：
   - `kubectl -n claw set image deployment/clawman clawman=...`
   - `kubectl -n claw rollout status deployment/clawman`
4. 移除节点排除规则（不再排除 `cvm-sz2`）。
5. Tailscale 鉴权从 `TS_AUTHKEY` 迁移为 OAuth client：
   - `TS_OAUTH_CLIENT_ID`
   - `TS_OAUTH_SECRET`
6. Deploy 中 kube context 采用 `azure/k8s-set-context@v4`，要求 `KUBE_CONFIG` 中存在 `default` context。

---

## 6. 消息链路校准（2026-03-19）

### 变更结论
1. `clawman` 当前以 `3s` 周期拉取企业微信会话存档，单批上限 `100` 条，游标存储在 PostgreSQL `ingest_cursors(source=wecom_archive)`。
2. ingress 侧统一跳过 `from == WECOM_BOT_ID` 的归档消息，不再只限于私聊，避免 bot 自己的群消息再次被当作用户输入。
3. room 级 sandbox session 仍然是进程内 cache，但遇到 SDK `ErrOrphanedClaim` 时会先 `Close()` 再重建，避免单个 room 进入永久报错状态。
4. 当前消息只有在以下步骤全部成功后才会推进 cursor：
   - 企业微信详情解析成功
   - sandbox 打开成功
   - agent 执行成功
   - PostgreSQL 事务成功写入 inbound / outbound / outbox
5. `outbox_deliveries` 由独立 egress consumer 轮询回发；消息发送成功后才标记 `sent`。
6. 因为 cursor 只在成功路径推进，所以停机或持续失败后恢复服务时，会从旧 cursor 开始回放历史 archive backlog；这属于当前版本的既定运行语义。
