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

---

## 7. 消息与回发链路再简化（2026-03-23）

### 变更结论
1. `clawman` 继续以 PostgreSQL `messages.seq` 的 `MAX(seq)` 作为唯一 archive checkpoint，不再使用 `ingest_cursors`。
2. 主服务不再写 `outbox_deliveries`，也不再运行独立 egress consumer。
3. dispatch 拿到 agent reply 后，直接调用 WorkTool 回发企业微信。
4. 只有当 WorkTool 回发成功后，对应 `messages` 才会从 `pending` 更新为 `done`。
5. 如果 sandbox 调用失败、WorkTool 回发失败，或回发成功后 `done` 更新失败，当前消息都会继续保留在 `pending`，由后续 dispatch 重试。
6. 该简化方案接受一个已知窗口：如果 WorkTool 已成功发送，但 `messages.done` 更新失败，后续重试可能产生重复回复；当前版本先接受这一权衡，以换取更直接的实现。

---

## 8. Android 拉取 jobs 出站链路（2026-03-29）

### 变更结论
1. 主服务不再在 dispatch 成功后直接调用 WorkTool；回复改为写入 PostgreSQL `jobs`。
2. `jobs` 最小字段为：`id / seq / client_id / recipient_alias / message / max_seq / created_at`。
3. Android 发送端改为调用 `GET /api/wecom/jobs?seq=<last_seq>` 拉取增量任务，不再使用 claim/result 双接口。
4. control API 改为 HTTP Basic 认证；服务端通过 `wecom_app_clients(client_id, client_secret, enabled)` 校验客户端。
5. 只要 `jobs` 写入成功，当前批次 `messages` 就会更新为 `done`；如果 `jobs` 写入失败，则该批消息继续保持 `pending`。
6. 当前版本接受一个已知窗口：如果 `jobs` 已成功写入，但 `messages.done` 更新失败，后续 dispatch 可能重复写入同一回复任务。

---

## 9. room 级通信协议与 message gateway 讨论（2026-04-01）

### 用户
我的核心需求是每个 room 有一个 sandbox 去承载；不同用户、不同使用场景可能并行执行。

### 助手（结论）
- `room_id -> sandbox` 这个隔离模型是合理且应继续保留的。
- 但“每个 sandbox 自拉 Redis 队列”不是该需求的必然推论；room 是执行隔离边界，不等于 sandbox 必须承担消息系统 consumer 的职责。

### 用户
如果主服务持续把消息传递给 agent，agent 持续响应，中间还可能中断，是继续用 Redis、WebSocket，还是别的方式？

### 助手（结论）
- 这类需求本质上是 room 级长会话，而不是单次最终态 RPC。
- 如果只看双向实时性，`WebSocket` 比内部仿微信的 `getupdates/sendmessage` 或 Redis mailbox 更自然。
- 但从长期内部服务协议看，`gRPC bidirectional streaming` 往往比 `WebSocket` 更容易约束 schema、取消、超时、错误码和版本演进。

### 用户
我不想让 sandbox-router 继续作为中间层；是否让 sandbox 主动连回 clawman 更合理？

### 助手（结论）
- 不想要 `sandbox-router`，不等于应该让 sandbox 主动回连 `clawman`。
- 更清晰的控制面仍然是 `clawman` 持有 room ownership，并作为统一 `message gateway` 管理与 sandbox 的内部会话。

### 用户
我的意思是把 `clawman` 简化成统一 `message gateway`；sandbox 通过内部协议获取自己 room 的消息，执行完成后再调用 `clawman` 的 `sendmessage`，由 `clawman` 对接第三方渠道。

### 助手（最终结论）
1. 该方向成立，且比“sandbox 直接实现微信/飞书/email 客户端”更合理。
2. `clawman` 应统一持有外部渠道协议、认证、cursor 与发送动作；sandbox 不直接维护第三方协议状态。
3. 内部协议不应继续复刻外部渠道的 `iLink` 语义；应单独定义 room 级规范化会话协议。
4. 下一阶段首选 `gRPC bidirectional streaming` 作为 `clawman <-> sandbox` 的内部主协议；`WebSocket` 保留为探索期备选。
5. sandbox 输出的应是规范化事件和结果，例如 `typing`、`assistant_delta`、`assistant_final`、`tool_event`、`failed`；真正的 `sendmessage` 由 `clawman` 负责映射到各个外部渠道。

---

## 10. research pass：当前代码中的消息批次（2026-04-01）

### 用户
按照研究 -> 计划 -> 批注 -> 实现的方式开始；先确认当前代码里的消息批次语义到底落在哪里。

### 助手（research 结论）
1. 当前代码没有独立业务轮次实体；处理批次是 `clawman` 在 dispatch 阶段按 room 隐式形成的。
2. `dispatchRoom(room_id)` 会读取该 room 当前全部 `messages(status=pending)`，按 `seq` 排序后一次性打包成单个 `sandbox.AgentRequest{messages[]}`。
3. 因此当前处理边界是“该 room 当前所有 pending 消息集合”，而不是稳定持久化的业务实体。
4. 该隐式处理批次只有在 sandbox 成功返回且 `jobs` 写入成功后，才会整体把这批消息更新为 `done`。
5. 如果 sandbox 调用失败、`jobs` 写入失败，或 `done` 更新失败，系统不会留下独立批次状态，而是保留原始 `pending` 消息，由后续 dispatch 继续重试。
6. 这意味着后续若切到 room 级 gRPC 消息传输，应继续围绕 `messages` 与消息批次设计，而不是引入新的业务轮次概念。
