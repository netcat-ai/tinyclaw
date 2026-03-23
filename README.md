# tinyclaw

## 文档
- [架构设计草案 v0](./docs/ARCHITECTURE_V0.md)
- [Agent Sandbox 集成设计 v0](./docs/AGENT_SANDBOX_INTEGRATION_V0.md)
- [下一步执行清单](./docs/NEXT_STEPS.md)

## 当前实现
TinyClaw 当前已经切到官方 `agent-sandbox` 资源与通信模型：
- `clawman` 从企业微信会话存档拉取消息、解密并标准化。
- `clawman` 当前拆成两个协程：ingest worker 每 3 秒拉取一次会话存档并逐条入库，dispatch worker 每秒扫描 `messages(status=pending)` 并按 `room_id` 聚合调用 sandbox。
- `messages.seq` 作为唯一拉取 checkpoint，不再保留独立 cursor 表；拉取恢复位置由 `MAX(messages.seq)` 推导。
- 除了解密失败这种致命 ingress 错误外，所有拉到的 archive item 都会先落到 PostgreSQL `messages`；bot/self、冷启动历史消息和非法 payload 通过 `status=ignored` 保留事实但不进入处理链路。
- 主服务通过官方 Go SDK 管理 sandbox 生命周期，并在进程内按 `room_id` 复用已打开的 sandbox client。
- room 级 SDK client 遇到 `ErrOrphanedClaim` 时会先 `Close()` 清理，再重建 client，避免单个 room 进入必须重启进程才能恢复的中毒状态。
- 官方 Go SDK 当前走 direct-url 模式连接 `sandbox-router`，再通过 `/execute` 在 sandbox 内部桥接调用本机 `POST /agent`。
- `agent` 在 sandbox 内提供 `/healthz`、`/agent`、`/execute`、`/upload`、`/download`、`/list`、`/exists` 等官方风格 HTTP 接口，主输入已经切到结构化 `messages[]`。
- 私聊消息直接进入 `pending`；群聊消息只有命中 `@` 提及或触发关键字时才进入 `pending`，未触发消息先以 `status=buffered` 保留，等后续触发消息一并带入上下文。
- 冷启动且 `messages` 为空时，仅最近 10 分钟内的消息允许进入 `pending/buffered`；更早的 backlog 仍会落库，但统一写成 `status=ignored`。
- 如果消息已写入 `messages(status=pending)`，但在后续 sandbox、WorkTool 回发或 `done` 更新阶段失败，消息不会丢失；dispatch worker 会持续复用这批 `pending` 消息重试。

## 当前消息流程
1. ingest worker 从 `messages` 查询当前 `MAX(seq)`，以此作为 `GetChatData(seq, limit)` 的起点，当前固定 `limit=100`。
2. 对每条 archive item 依次执行解密、JSON 反序列化、基础字段校验，并把完整解密结果保存到 `messages.payload`。
3. 无论消息是否可处理，都会先落库一条 `messages` 记录：
   - bot/self、冷启动历史消息、非法 payload 写成 `status=ignored`
   - 群聊未触发消息写成 `status=buffered`
   - 私聊消息和群聊触发消息写成 `status=pending`
4. 若当前消息是群聊触发消息，会在同一个事务中把该 `room_id` 下历史 `buffered` 消息一起提升为 `pending`，避免只处理触发语句而丢掉前文上下文。
5. dispatch worker 每秒扫描 `status=pending` 的 room，按 `room_id` 聚合所有待处理消息。
6. 触发处理时，`sandbox.Orchestrator` 按 `room_id` 查找或创建进程内 SDK client，并调用 `Open()` 保证 sandbox ready。
7. Orchestrator 通过 SDK `Run()` 在 sandbox 内执行一个本机 `curl http://127.0.0.1:8888/agent`，把聚合后的 `messages[]/msgid/room_id/tenant_id/chat_type` 传给 agent runtime。
8. agent runtime 用 `claude_agent_sdk` 执行；`SandboxTemplate` 当前通过 `CLAUDE_SYSTEM_PROMPT_APPEND` 注入系统提示词。
9. 主服务拿到 agent reply 后，直接通过 WorkTool 回发企业微信。
10. 只有当 WorkTool 回发成功后，主服务才会把本轮参与处理的 `messages` 标记为 `done`；如果发送失败或 `done` 更新失败，这批消息保持 `pending`，由后续 dispatch 重试。

## Agent Scaffold
- `agent/`：独立的 TypeScript agent 子工程，当前提供：
  - HTTP runtime server（`/healthz`、`/agent`、`/execute`、`/upload`、`/download`、`/list`、`/exists`）
  - `echo` 与 `claude_agent_sdk` 两种运行模式
  - `tini + entrypoint.sh` 进程模型
  - 本地/CI 可运行的 HTTP 集成测试与 live smoke
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
3. 官方通信接口采用 `sandbox-router + HTTP runtime`，不再让 sandbox 自拉 Redis ingress。
4. sandbox 生命周期统一通过官方 Go SDK 管理；当前 SDK 不支持自定义确定性 claim 名称，因此 room 级复用先采用进程内 client cache。
5. 主服务当前只保留最小 PostgreSQL 事实源：`messages`。
6. 主服务当前按 `room_id` 维护进程内 sandbox session，并以 SDK `Open()` 作为 session 可调用条件。
7. 不建设独立 `room registry` 表，暂不维护中心化 `last_seen_at`。
8. 空闲策略先采用软休眠；硬休眠、warm pool 和自动销毁后置到后续阶段。

## 当前待办
- 为进程内 `ttlCache` 增加过期项回收，避免长生命周期进程中缓存键只增不减。
- 为 room sandbox session cache 增加回收策略，避免历史 `room_id` 持续堆积。
- 评估如何在官方 Go SDK 支持前恢复跨重启的 room -> sandbox 稳定复用。
- 评估 direct send 模式下“发送成功但 `messages.done` 更新失败”带来的重复发送窗口，并决定是否需要补幂等或去重策略。

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
  - `k8s/sandbox-router.yaml`
  - `k8s/sandboxtemplate.yaml`
- K8s Deployment 资源名固定为 `clawman`（见 `k8s/deployment.yaml`）。
- 主服务需要集群内已部署：
  - agent-sandbox core controller
  - agent-sandbox extensions
  - `sandbox-router`
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
- `SANDBOX_ROUTER_URL`
- `SANDBOX_SERVER_PORT`

默认值：
- `SANDBOX_NAMESPACE=claw`
- `SANDBOX_TEMPLATE_NAME=tinyclaw-agent-template`
- `SANDBOX_ROUTER_URL=http://sandbox-router-svc.{namespace}.svc.cluster.local:8080`
- `SANDBOX_SERVER_PORT=8888`
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
  - `WORKTOOL_ROBOT_ID`
  - `ANTHROPIC_API_KEY`（供 sandbox template 内 agent 使用）
- 需要在 GitHub 仓库 variables 中配置：
  - `WECOM_CORP_ID`
  - `WECOM_BOT_ID`
  - `ANTHROPIC_BASE_URL`
- 可选覆盖的 GitHub variables：
  - `SANDBOX_TEMPLATE_NAME`
  - `SANDBOX_ROUTER_URL`
  - `SANDBOX_SERVER_PORT`
