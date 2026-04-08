# tinyclaw

## 文档
- [架构设计草案 v0](./docs/ARCHITECTURE_V0.md)
- [Agent Sandbox 集成设计 v0](./docs/AGENT_SANDBOX_INTEGRATION_V0.md)
- [架构调整计划（2026-04）](./docs/ARCHITECTURE_REFACTOR_2026_04.md)
- [下一步执行清单](./docs/NEXT_STEPS.md)

## 文档批注约定
在 `docs/*.md` 中补充批注时，统一使用以下两种格式之一，避免把原文与后续动作混在一起。

可见批注：

```md
> [!NOTE]
> 批注：这里的结论需要调整为 `clawman` 做 message gateway。
> 动作：后续同步更新 `README.md` 和架构文档。
```

隐藏批注：

```md
<!-- TODO(codex): 把这里的 Redis mailbox 方案标记为历史讨论，不再作为推荐方案 -->
```

约定：
- 需要长期保留给人阅读的说明，用可见批注。
- 只是给后续文档整理或实现工作的指令，用隐藏批注。
- 尽量显式写出“批注”或 `TODO(codex)`，并附上预期动作，便于后续自动识别和执行。

## 当前实现
TinyClaw 当前已经切到官方 `agent-sandbox` 资源与通信模型：
- `clawman` 从企业微信会话存档拉取消息、解密并标准化。
- `clawman` 当前拆成两个协程：ingest worker 每 3 秒拉取一次会话存档并逐条入库，dispatch worker 每秒扫描 `messages(status=pending)` 并按 `room_id` 聚合调用 sandbox。
- `messages.seq` 作为唯一拉取 checkpoint，不再保留独立 cursor 表；拉取恢复位置由 `MAX(messages.seq)` 推导。
- 除了解密失败这种致命 ingress 错误外，所有拉到的 archive item 都会先落到 PostgreSQL `messages`；bot/self、冷启动历史消息和非法 payload 通过 `status=ignored` 保留事实但不进入处理链路。
- 主服务直接管理 `SandboxClaim` 生命周期，并按 `room_id` 维护进程内 room session。
- sandbox 启动后通过 gRPC 主动连接 `clawman`，`clawman` 再把对应 room 的 `messages[]` 批次下发到该连接。
- `agent` 在 sandbox 内保留 `/healthz` 作为探针入口，实际消息处理通过 gRPC bridge 完成。
- `clawman` 在 room 首次进入 dispatch 时确保最小 `rooms` 元数据存在；Claude session 复用逻辑保留在 agent 内部。
- 私聊消息直接进入 `pending`；群聊消息只有命中 `@` 提及或触发关键字时才进入 `pending`，未触发消息先以 `status=buffered` 保留，等后续触发消息一并带入上下文。
- 冷启动且 `messages` 为空时，仅最近 10 分钟内的消息允许进入 `pending/buffered`；更早的 backlog 仍会落库，但统一写成 `status=ignored`。
- 如果消息已写入 `messages(status=pending)`，但在后续 gRPC 下发、sandbox 结果处理或 `jobs` 写入阶段失败，消息不会丢失；dispatch worker 会持续复用这批 `pending` 消息重试。
- 主服务提供一个轻量 control API，dispatch 成功后把 agent reply 写入 PostgreSQL `jobs` outbox，再由 Android 发送端按 `client_id` 轮询拉取并发送。

## 当前消息流程
1. ingest worker 从 `messages` 查询当前 `MAX(seq)`，以此作为 `GetChatData(seq, limit)` 的起点，当前固定 `limit=100`。
2. 对每条 archive item 依次执行解密、JSON 反序列化、基础字段校验，并把完整解密结果保存到 `messages.payload`。
3. 无论消息是否可处理，都会先落库一条 `messages` 记录：
   - bot/self、冷启动历史消息、非法 payload 写成 `status=ignored`
   - 群聊未触发消息写成 `status=buffered`
   - 私聊消息和群聊触发消息写成 `status=pending`
4. 若当前消息是群聊触发消息，会在同一个事务中把该 `room_id` 下历史 `buffered` 消息一起提升为 `pending`，避免只处理触发语句而丢掉前文上下文。
5. dispatch worker 每秒扫描 `status=pending` 的 room，按 `room_id` 聚合所有待处理消息。
6. 触发处理时，`clawman` 先确保该 room 的最小 `rooms` 元数据存在。
7. `sandbox.Orchestrator` 按 `room_id` 查找或创建 `SandboxClaim`，并等待对应 sandbox ready。
8. sandbox 主动连接 `clawman` 的 gRPC server；`clawman` 把聚合后的 `messages[]` 批次下发到该 room 对应的连接。
9. agent runtime 用 `claude_agent_sdk` 执行；当前实现会在同一 sandbox 生命周期内由 agent 自己复用 Claude session，避免每次 query 都开新 session；`SandboxTemplate` 当前通过 `CLAUDE_SYSTEM_PROMPT_APPEND` 注入系统提示词。
10. sandbox 返回最终输出后，主服务把结果写入 `jobs` outbox。
11. 只要 outbox 写入成功，主服务就会把本轮参与处理的 `messages` 标记为 `done`；Android 发送端后续通过 control API 拉取并发送文本。

## Android 外发拉取

TinyClaw 现在提供一个最小 outbox 拉取接口，给 Android 无障碍发送端使用：

- `GET /api/wecom/jobs?seq=<last_seq>`

说明：

- 这是“手机主动轮询 TinyClaw”的 pull 模式，不是 TinyClaw 主动打到手机。
- `recipient_alias` 由手机本地联系人配置解析，所以服务端不需要知道企业微信 UI 细节。
- 每条 `jobs` 在写入时就已经绑定 `client_id`，App 只拉属于自己的消息。
- App 使用 HTTP Basic 认证：
  - username = `client_id`
  - password = `client_secret`
- 当客户端首次请求 `seq=0` 时，服务端只返回 `next_seq`，不返回历史消息。

## Agent Scaffold
- `agent/`：独立的 TypeScript agent 子工程，当前提供：
  - `/healthz` HTTP probe
  - gRPC client bridge to `clawman`
  - `echo` 与 `claude_agent_sdk` 两种运行模式
  - `tini + entrypoint.sh` 进程模型
  - 本地/CI 可运行的 healthz 与 runtime 测试
- 常用命令：
  - `cd agent && npm run check`
  - `cd agent && npm test`
  - `cd agent && npm run test:live`  # 真实 Claude smoke，依赖 `../.env`
- `claude_agent_sdk` 运行时通过 `ANTHROPIC_API_KEY` 或 `CLAUDE_CODE_OAUTH_TOKEN` 认证；`ANTHROPIC_BASE_URL` 可选。
- `claude_agent_sdk` 运行时会显式查找 `claude` 可执行程序，可通过 `CLAUDE_CODE_EXECUTABLE` 覆盖。
- 本地开发时，`agent` 会自动尝试加载 `agent/.env` 和仓库根目录 `.env`；测试可通过 `AGENT_LOAD_DOTENV=0` 关闭。

## 当前共识（2026-03-16）
1. 统一房间标识：`room_id = {roomid_or_from}`，群聊取 `roomid`，私聊取 `from`；`tenant_id` 和 `chat_type` 作为独立字段保留。
2. 官方控制面接口采用 `SandboxTemplate + SandboxClaim`，不再由业务侧直接创建 `Sandbox`。
3. sandbox 不再自拉 Redis ingress。
4. sandbox 生命周期当前通过 `clawman` 直接管理 `SandboxClaim`，room 级复用先采用进程内 session cache。
5. 主服务当前使用最小 PostgreSQL 结构：`messages`、`rooms`、`jobs`、`wecom_app_clients`；其中 `rooms` 只承载平台级 room 元数据。
6. 主服务当前按 `room_id` 维护进程内 sandbox session，并以 SDK `Open()` 作为 session 可调用条件。
7. 维护最小 `rooms` 表用于平台级 room 元数据，但暂不建设带 `last_seen_at`/调度状态的重型 `room registry`。
8. 空闲策略先采用软休眠；硬休眠、warm pool 和自动销毁后置到后续阶段。

## 下一阶段通信决议（2026-04-01）
1. 继续保持 `room_id -> sandbox` 的隔离模型；每个 room 的工作目录、执行状态和并发控制仍收敛在独立 sandbox 内。
2. `clawman` 下一阶段收敛为统一 `message gateway`：负责企业微信/飞书/email 等外部通道的 ingress、egress、认证、cursor 与审计。
3. sandbox 不直接实现外部渠道客户端，不直接维护 `GetUpdates`/`sendmessage` 等第三方协议状态，也不再回退到“每个 sandbox 自拉 Redis 队列”。
4. `clawman` 与 sandbox 之间改为 room 级内部消息传输协议；第一阶段只传递 `messages[]`，由 agent 自行判断新消息、追加消息与中断。
5. `gRPC server` 放在 `clawman`，sandbox 作为 `gRPC client` 主动连接。
6. 集群内内部通信优先考虑 `gRPC streaming`；`WebSocket` 可作为探索期备选，但不再优先设计成内部 `getupdates/sendmessage` 式长轮询协议。
7. 外发动作仍由 `clawman` 统一落地到具体渠道；sandbox 不直接携带第三方发送协议状态。

## 当前待办
- 为进程内 `ttlCache` 增加过期项回收，避免长生命周期进程中缓存键只增不减。
- 为 room sandbox session cache 增加回收策略，避免历史 `room_id` 持续堆积。
- 评估如何在官方 Go SDK 支持前恢复跨重启的 room -> sandbox 稳定复用。
- 评估“`jobs` 写入成功但 `messages.done` 更新失败”带来的重复出队窗口，并决定是否需要补幂等或去重策略。
- 设计并验证 `clawman` gRPC server 与 sandbox gRPC client 的 `messages[]` 传输契约。

## 项目目标
构建云端 AI Agent Runtime，让企业员工可在企业微信私聊/群聊中与 agent 交互：
- 每个 `room_id` 运行在隔离 sandbox 中。
- agent 可在安全边界内执行代码、调用模型与外部工具。
- 记忆与人格等结构化数据放在智能表格，文件上下文放在企业云盘。
- 主服务负责消息拉取、会话分发、唤醒与治理；agent 负责推理与工具执行。

## 企业微信详情解析
- 私聊消息按 `from` 分流：
  - 客户：调用 `externalcontact/get` 获取客户详情
  - 员工：调用 `user/get` 获取内部用户详情
- 群聊消息按 `roomid` 分流：
  - 若发送者是客户：调用 `externalcontact/groupchat/get` 解析客户群详情
  - 若发送者是员工：调用 `msgaudit/groupchat/get` 解析内部群详情
- 进程内会做短期 TTL cache，避免同一批消息重复请求企业微信详情接口。

## K8s 部署
- 命名空间固定为 `claw`。
- 部署清单：
  - `k8s/namespace.yaml`
  - `k8s/configmap.example.yaml`
  - `k8s/secret.example.yaml`
  - `k8s/rbac.yaml`
  - `k8s/deployment.yaml`
  - `k8s/sandboxtemplate.yaml`
- 若镜像来自私有 GHCR 仓库，需先在 `claw` 命名空间创建名为 `ghcr-pull` 的 `docker-registry` secret，供 `clawman` 拉取镜像。
- K8s Deployment 资源名固定为 `clawman`（见 `k8s/deployment.yaml`）。
- 主服务需要集群内已部署：
  - agent-sandbox core controller
  - agent-sandbox extensions
  - 一个可复用的 `SandboxTemplate`
- PostgreSQL 需要由外部服务或平台层提供；仓库当前不内置数据库部署清单。

## 配置分层
- 非敏感配置进入 `ConfigMap` / GitHub `vars`
- 敏感凭据进入 `Secret` / GitHub `secrets`
- Claude 运行时统一只使用 `ANTHROPIC_API_KEY` 与 `ANTHROPIC_BASE_URL`

主服务关键配置：
- `DATABASE_URL`
- `WECOM_GROUP_TRIGGER_MENTIONS`
- `WECOM_GROUP_TRIGGER_KEYWORDS`
- `SANDBOX_NAMESPACE`
- `SANDBOX_TEMPLATE_NAME`
- `SANDBOX_WAKE_PLACEHOLDER`
- `CLAWMAN_GRPC_LISTEN_ADDR`
- `CLAWMAN_GRPC_ADDR`
- `CONTROL_API_ADDR`
- `WECOM_BOT_ID`

默认值：
- `SANDBOX_NAMESPACE=claw`
- `SANDBOX_TEMPLATE_NAME=tinyclaw-agent-template`
- `SANDBOX_WAKE_PLACEHOLDER=虾虾正在起床，请稍等一下下～`
  关闭方式：显式设为 `off` / `false` / `0` / `no`；空字符串会回到默认文案
- `CLAWMAN_GRPC_LISTEN_ADDR=:8092`
- `CLAWMAN_GRPC_ADDR=clawman.{namespace}.svc.cluster.local:8092`
- `CONTROL_API_ADDR=:8081`
- `WECOM_GROUP_TRIGGER_MENTIONS={WECOM_BOT_ID}`（若未显式配置）
- `WECOM_GROUP_TRIGGER_KEYWORDS=`（默认空）

agent 运行时关键配置：
- `AGENT_SERVER_PORT`
- `AGENT_RUNTIME_MODE`
- `AGENT_WORKDIR`
- `ANTHROPIC_API_KEY` 或 `CLAUDE_CODE_OAUTH_TOKEN`
- `ANTHROPIC_BASE_URL`
- `CLAUDE_SYSTEM_PROMPT_APPEND`

## CI/CD 前置条件
- `deploy-claw` job 通过 `tailscale/github-action@v4`（OAuth client）接入 tailnet 后再执行 `kubectl`。
- 需要在 GitHub 仓库 secrets 中配置：
  - `TS_OAUTH_CLIENT_ID`
  - `TS_OAUTH_SECRET`
  - `KUBE_CONFIG`
  - `DATABASE_URL`
  - `WECOM_CORP_SECRET`
  - `WECOM_RSA_PRIVATE_KEY`
  - `WECOM_CONTACT_SECRET`
  - `ANTHROPIC_API_KEY`（供 sandbox template 内 agent 使用）
- 需要在 GitHub 仓库 variables 中配置：
  - `WECOM_CORP_ID`
  - `WECOM_BOT_ID`
  - `ANTHROPIC_BASE_URL`
- 可选覆盖的 GitHub variables：
  - `SANDBOX_TEMPLATE_NAME`
  - `SANDBOX_WAKE_PLACEHOLDER`
  - `CLAWMAN_GRPC_LISTEN_ADDR`
  - `CLAWMAN_GRPC_ADDR`
