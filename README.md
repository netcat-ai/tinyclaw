# tinyclaw

## 文档
- [架构设计草案 v0](./docs/ARCHITECTURE_V0.md)
- [Agent Sandbox 集成设计 v0](./docs/AGENT_SANDBOX_INTEGRATION_V0.md)
- [下一步执行清单](./docs/NEXT_STEPS.md)

## 当前共识（2026-03-09）
1. 统一房间标识：`room_id = {roomid_or_from}`，其中群聊取 `roomid`，私聊取 `from`；`tenant_id` 和 `chat_type` 作为独立字段保留。
2. Redis 设计：每个 `room_id` 一个 stream，键为 `stream:room:{room_id}`。
3. 不引入 `stream:dispatch`，由 Ingress 收到新消息后直接触发 `ensure(room_id)`。
4. agent 在 sandbox 内自行 `XREADGROUP BLOCK` 持续拉取消息并串行消费。
5. 不建设独立 `room registry` 表，暂不维护中心化 `last_seen_at`。
6. 交付语义简化为：agent 消费成功并回发后 `XACK`，不引入额外幂等设计。
7. 空闲策略先采用软休眠（阻塞等待），硬休眠（退出/缩容）后续按压测引入，自动销毁后置到 v1。

## 项目目标
构建云端 AI Agent Runtime，让企业员工可在企业微信私聊/群聊中与 agent 交互：
- 每个 `room_id` 运行在隔离 sandbox 中。
- agent 可自由执行代码（在安全边界内）并访问企业内部/外部工具。
- 记忆与人格等结构化数据放在智能表格，文件上下文放在企业云盘。
- 主服务负责消息拉取、会话分发、唤醒与治理；agent 负责消费、执行与回发。

## K8s 部署
- 命名空间固定为 `claw`。
- 部署清单：
  - `k8s/namespace.yaml`
  - `k8s/deployment.yaml`
  - `k8s/secret.example.yaml`
- K8s Deployment 资源名固定为 `clawman`（见 `k8s/deployment.yaml`）。
- GitHub Actions 已拆分为两个 workflow：
  1. `Build`（`.github/workflows/build.yml`）：
     - 触发：`push` / `pull_request` / `workflow_dispatch`
     - 执行：`go test ./...` + Docker build
     - 仅在 `main` 的 `push` 或手动触发时推送镜像到 `ghcr.io/<owner>/tinyclaw`
  2. `Deploy`（`.github/workflows/deploy.yml`）：
     - 触发：`Build` 在 `main` 成功后自动触发（`workflow_run`），或手动 `workflow_dispatch`
     - 执行：部署到 `claw` 命名空间，并更新 `deployment/clawman`
     - 手动触发可选 `image_tag`，为空时默认使用触发提交 SHA

## CI/CD 前置条件
- `deploy-claw` job 通过 `tailscale/github-action@v4`（OAuth client）接入 tailnet 后再执行 `kubectl`。
- Tailscale OAuth client 需要：
  - 可写 `auth_keys` scope
  - 至少包含 `tag:ci`（与 workflow 中 `tags: tag:ci` 对齐）
- 需要在 GitHub 仓库 secrets 中配置：
  - `TS_OAUTH_CLIENT_ID`：Tailscale OAuth client ID（建议最小权限并限制 tag，如 `tag:ci`）。
  - `TS_OAUTH_SECRET`：Tailscale OAuth client secret。
  - `KUBE_CONFIG`：Kubernetes kubeconfig 内容（apiserver 必须是 tailnet 可达地址，且包含 `default` context）。
  - `WECOM_CORP_ID`（必需）
  - `WECOM_CORP_SECRET`（必需）
  - `WECOM_RSA_PRIVATE_KEY`（必需）
  - `REDIS_ADDR`（可选，默认 `redis:6379`）
  - `REDIS_PASSWORD`（可选）
  - `STREAM_PREFIX`（可选，默认 `stream:room`）
  - `WECOM_SEQ_KEY`（可选，默认 `msg:seq`）
- 目前仓库内默认值：
  - `REDIS_ADDR=redis:6379`
  - `STREAM_PREFIX=stream:room`
  - `WECOM_SEQ_KEY=msg:seq`
