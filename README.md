# tinyclaw

## 文档
- [架构设计草案 v0](./docs/ARCHITECTURE_V0.md)
- [Agent Sandbox 集成设计 v0](./docs/AGENT_SANDBOX_INTEGRATION_V0.md)
- [下一步执行清单](./docs/NEXT_STEPS.md)

## 当前实现
TinyClaw 当前已经切到官方 `agent-sandbox` 资源与通信模型：
- `clawman` 从企业微信会话存档拉取消息、解密并标准化。
- 主服务通过官方 Go SDK 管理 sandbox 生命周期，并在进程内按 `room_id` 复用已打开的 sandbox client。
- 官方 Go SDK 当前走默认的 `port-forward` 模式直连 sandbox，再通过 `/execute` 在 sandbox 内部桥接调用本机 `POST /agent`。
- `agent` 在 sandbox 内提供 `/healthz`、`/agent`、`/execute`、`/upload`、`/download`、`/list`、`/exists` 等官方风格 HTTP 接口，内部使用 `claude_agent_sdk` 或 `echo` runtime 执行。
- sandbox 返回回复后，主服务把入站消息、出站消息和待发送回执写入 PostgreSQL，再由 egress consumer 从 outbox 统一回发企业微信。

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
3. 官方通信接口采用 Go SDK `port-forward + HTTP runtime`，不再让 sandbox 自拉 Redis ingress。
4. sandbox 生命周期统一通过官方 Go SDK 管理；当前 SDK 不支持自定义确定性 claim 名称，因此 room 级复用先采用进程内 client cache。
5. 主服务当前只保留最小 PostgreSQL 事实源：`ingest_cursors`、`messages`、`outbox_deliveries`。
6. 主服务当前按 `room_id` 维护进程内 sandbox session，并以 SDK `Open()` 作为 session 可调用条件。
7. 不建设独立 `room registry` 表，暂不维护中心化 `last_seen_at`。
8. 空闲策略先采用软休眠；硬休眠、warm pool 和自动销毁后置到后续阶段。

## 当前待办
- 为进程内 `ttlCache` 增加过期项回收，避免长生命周期进程中缓存键只增不减。
- 为 room sandbox session cache 增加回收策略，避免历史 `room_id` 持续堆积。
- 评估如何在官方 Go SDK 支持前恢复跨重启的 room -> sandbox 稳定复用。
- 为 `outbox_deliveries` 的发送完成/失败更新增加 attempt fencing，避免租约过期重领后旧 attempt 覆盖新状态。

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
  - 先调用 `msgaudit/groupchat/get` 解析内部群详情
  - 若不是内部群，再调用 `externalcontact/groupchat/get` 解析客户群详情
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
- `SANDBOX_NAMESPACE`
- `SANDBOX_TEMPLATE_NAME`
- `SANDBOX_SERVER_PORT`

默认值：
- `SANDBOX_NAMESPACE=claw`
- `SANDBOX_TEMPLATE_NAME=tinyclaw-agent-template`
- `SANDBOX_SERVER_PORT=8888`

agent 运行时关键配置：
- `AGENT_SERVER_PORT`
- `AGENT_RUNTIME_MODE`
- `AGENT_WORKDIR`
- `ANTHROPIC_API_KEY` 或 `CLAUDE_CODE_OAUTH_TOKEN`
- `ANTHROPIC_BASE_URL`

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
  - `SANDBOX_SERVER_PORT`
