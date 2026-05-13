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
<!-- TODO(codex): 把这里的历史方案明确标记为已弃用 -->
```

约定：
- 需要长期保留给人阅读的说明，用可见批注。
- 只是给后续文档整理或实现工作的指令，用隐藏批注。
- 尽量显式写出“批注”或 `TODO(codex)`，并附上预期动作，便于后续自动识别和执行。

## 当前项目状态（2026-04-10）
TinyClaw 当前已经完成最小可运行闭环，且数据面已切到 `clawman gRPC server + sandbox gRPC client`：
- `clawman` 从企业微信会话存档拉取消息、解密并标准化。
- `clawman` 当前拆成两个协程：ingest worker 每 3 秒拉取一次会话存档并逐条入库，dispatch worker 每秒扫描待处理 room。
- `messages.seq` 作为唯一拉取 checkpoint，不再保留独立 cursor 表；恢复位置由 `MAX(messages.seq)` 推导。
- 除了解密失败这类致命 ingress 错误外，所有 archive item 都会先落到 PostgreSQL `messages`。
- `messages` 当前状态机为 `ignored / buffered / pending / sent / done`。
- 私聊消息直接进入 `pending`；群聊消息只有命中 `@` 提及或触发关键字时才进入 `pending`，未触发消息先以 `status=buffered` 保留，等后续触发消息一并带入上下文。
- 冷启动且 `messages` 为空时，仅最近 10 分钟内的消息允许进入 `pending/buffered`；更早 backlog 仍会落库，但统一写成 `status=ignored`。
- 主服务直接管理 `SandboxClaim` 生命周期，并按 `room_id` 生成确定性的 claim 名称；claim 上补 `tinyclaw/room-id` 注解，便于 sandbox 反查所属 room。
- sandbox 启动后通过 gRPC `RoomChat(stream Message)` 主动连接 `clawman`；`clawman` 再把对应 room 的 `messages[]` 批次下发到该连接。
- `agent` 在 sandbox 内仅保留 `/healthz` 作为探针入口，实际消息处理通过 gRPC bridge 完成，不再暴露 `POST /agent`。
- `clawman` 在 room 首次进入 dispatch 时确保最小 `rooms` 元数据存在；Claude session 复用逻辑保留在 agent 内部。
- `clawman` 额外提供内部媒体下载接口 `POST /internal/media/fetch`；agent 在需要看图时可通过自定义工具按消息中的 `sdkfileid` 拉取图片到本地工作目录。
- dispatch 把一批消息成功送达 sandbox 后，会先把这批 `messages` 标记为 `sent`；sandbox 返回 `error`、上下文超时或 `jobs` 写入失败时，再回退到 `pending` 重试。
- 服务启动时会执行一次 `ResetSentMessages()`，把遗留的 `sent` 统一恢复到 `pending`，避免上次处理中断后长期卡住。
- 主服务提供一个轻量 control API；dispatch 成功后把 agent reply 写入 PostgreSQL `jobs`，Android 发送端再通过轮询拉取并发送。

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
7. `sandbox.Orchestrator` 按 `room_id` 推导确定性的 `SandboxClaim.name`，确保 claim 存在并等待 `Ready=True`。
8. sandbox 主动连接 `clawman` 的 gRPC server；第一条消息必须是 `kind=connect`，携带 `sandbox_id`。
9. `clawman` 以 `Message{kind=messages, request_id, messages[]}` 的形式把当前批次消息送到对应 room 的流连接。
10. gRPC 下发成功后，主服务先把这批 `messages` 标记为 `sent`。
11. agent runtime 用 `claude_agent_sdk` 执行；当前实现会在同一 sandbox 生命周期内由 agent 自己复用 Claude session，避免每次 query 都开新 session。
12. 若本轮需要查看图片，agent 会调用自定义工具 `fetch_wecom_image`；该工具再请求 `POST /internal/media/fetch`，校验 `room_id + seq + msgid + sdkfileid` 后把图片下载到 `/workspace/incoming-media/<room_id>/`，并把本地路径返回给 Claude。
13. sandbox 在同一条流上返回 `kind=result` 或 `kind=error`：
   - `result`：携带最终输出文本
   - `error`：表示本轮处理失败
14. 收到 `result` 后，主服务把结果写入 `jobs` outbox。
15. 只有当 `jobs` 写入成功后，主服务才会把本轮 `messages` 从 `sent` 更新为 `done`。
16. 若 sandbox 返回 `error`、等待结果超时，或 `jobs` 写入失败，主服务会把这批 `sent` 消息恢复为 `pending`，交给后续 dispatch 重试。
17. 若 `jobs` 已成功写入但 `messages.done` 更新失败，这批消息会停留在 `sent`；当前版本依赖服务重启时的 `ResetSentMessages()` 把它们重新放回重试队列。

## Android 外发拉取

TinyClaw 现在提供一个最小 outbox 拉取接口，给 Android 无障碍发送端使用：

- `GET /api/wecom/jobs?seq=<last_seq>`

说明：

- 这是“手机主动轮询 TinyClaw”的 pull 模式，不是 TinyClaw 主动打到手机。
- `recipient_alias` 由手机本地联系人配置解析，所以服务端不需要知道企业微信 UI 细节。
- `jobs` 记录绑定的是 `bot_id`，不是 `client_id`。
- App 使用 HTTP Basic 认证：
  - username = `client_id`
  - password = `client_secret`
- 服务端先校验 `wecom_app_clients(client_id, client_secret, enabled)`，再把该 `client_id` 映射到对应 `bot_id` 过滤 `jobs`。
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

## 当前共识（2026-04-10）
1. 统一房间标识：`room_id = {roomid_or_from}`，群聊取 `roomid`，私聊取 `from`；`tenant_id` 和 `chat_type` 作为独立字段保留。
2. 控制面统一采用 `SandboxTemplate + SandboxClaim`；`SandboxClaim.name` 当前由 `room_id` 推导出确定性值，而不是依赖随机 claim identity。
3. 数据面当前采用 `clawman gRPC server + sandbox gRPC client`；主服务不再通过 `sandbox-router + HTTP /agent` 调用 sandbox。
4. `agent` 在 sandbox 内保留 `/healthz` 作为探针；真正的消息处理入口是 `RoomChat(stream Message)`。
5. PostgreSQL 最小结构当前为 `messages`、`rooms`、`jobs`、`wecom_app_clients`；其中 `rooms` 只承载平台级 room 元数据。
6. `messages` 状态机当前固定为 `ignored / buffered / pending / sent / done`；服务启动时统一把残留 `sent` 恢复到 `pending`。
7. 群聊粗 trigger 仍保留在 `clawman`；sandbox 不负责企业微信 trigger、cursor、认证或外发协议状态。
8. 当前 room 级复用仍以单进程内 `room_id -> claimName` cache 为主，但确定性 claim 名称已经为跨重连重用打下基础。

## 当前待办
- 为进程内 `ttlCache` 增加主动过期回收，避免长生命周期进程中缓存键只增不减。
- 为 room session cache 与 `SandboxClaim` 生命周期增加回收策略，避免历史 `room_id` 和遗留 claim 持续堆积。
- 处理“`jobs` 写入成功但 `messages.done` 更新失败”后消息卡在 `sent` 的恢复路径，并决定是否补幂等或去重策略。
- 补齐 `clawman` gRPC gateway 的鉴权、心跳/断线诊断和更细粒度观测。
- 在真实环境完成冷启动、热路径、失败恢复和 Android 拉取链路联调。
- 设计 richer streaming 事件（如 `typing`、`assistant_delta`、tool 事件），而不是只停留在 `result/error`。
- 评估长期 memory、文件上下文、scheduler 和 warm pool 的引入顺序。

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
- `CLAWMAN_INTERNAL_TOKEN`
- `WECOM_BOT_ID`
- `METRICS_ADDR`

默认值：
- `SANDBOX_NAMESPACE=claw`
- `SANDBOX_TEMPLATE_NAME=tinyclaw-agent-template`
- `SANDBOX_WAKE_PLACEHOLDER=虾虾正在起床，请稍等一下下～`
  关闭方式：显式设为 `off` / `false` / `0` / `no`；空字符串会回到默认文案
- `CLAWMAN_GRPC_LISTEN_ADDR=:8092`
- `CLAWMAN_GRPC_ADDR=clawman-svc.{namespace}.svc.cluster.local:8092`
- `CONTROL_API_ADDR=:8081`
- `CLAWMAN_INTERNAL_TOKEN=`（默认空；为空时内部媒体接口禁用）
- `METRICS_ADDR=:9090`
- `WECOM_GROUP_TRIGGER_MENTIONS={WECOM_BOT_ID}`（若未显式配置）
- `WECOM_GROUP_TRIGGER_KEYWORDS=`（默认空）

agent 运行时关键配置：
- `AGENT_SERVER_PORT`
- `AGENT_RUNTIME_MODE`
- `AGENT_WORKDIR`
- `ANTHROPIC_API_KEY` 或 `CLAUDE_CODE_OAUTH_TOKEN`
- `ANTHROPIC_BASE_URL`
- `CLAUDE_SYSTEM_PROMPT_APPEND`
- `CLAWMAN_GRPC_ADDR`
- `CLAWMAN_INTERNAL_BASE_URL`
- `CLAWMAN_INTERNAL_TOKEN`

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
  - `METRICS_ADDR`
