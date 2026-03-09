# tinyclaw

## 文档
- [架构设计草案 v0](./docs/ARCHITECTURE_V0.md)
- [Agent Sandbox 集成设计 v0](./docs/AGENT_SANDBOX_INTEGRATION_V0.md)
- [下一步执行清单](./docs/NEXT_STEPS.md)

## 当前共识（2026-03-09）
1. 会话键：`session_key = {chat_id_or_user_id}`，`tenant_id` 和 `chat_type` 作为独立字段保留。
2. Redis 设计：每个会话一个 stream，键为 `stream:session:{session_key}`。
3. 不引入 `stream:dispatch`，由 Ingress 收到新消息后直接触发 `ensure(session_key)`。
4. agent 在 sandbox 内自行 `XREADGROUP BLOCK` 持续拉取消息并串行消费。
5. 不建设独立 `session registry` 表，暂不维护中心化 `last_seen_at`。
6. 交付语义简化为：agent 消费成功并回发后 `XACK`，不引入额外幂等设计。
7. 空闲策略先采用软休眠（阻塞等待），硬休眠（退出/缩容）后续按压测引入，自动销毁后置到 v1。

## 项目目标
构建云端 AI Agent Runtime，让企业员工可在企业微信私聊/群聊中与 agent 交互：
- 每个会话运行在隔离 sandbox 中。
- agent 可自由执行代码（在安全边界内）并访问企业内部/外部工具。
- 记忆与人格等结构化数据放在智能表格，文件上下文放在企业云盘。
- 主服务负责消息拉取、会话分发、唤醒与治理；agent 负责消费、执行与回发。

## K8s 部署
- 命名空间固定为 `claw`。
- 部署清单：
  - `k8s/namespace.yaml`
  - `k8s/deployment.yaml`
  - `k8s/secret.example.yaml`
- GitHub Actions 在 `main` 分支 push 后自动：
  1. `go test ./...`
  2. 构建并推送镜像到 `ghcr.io/<owner>/tinyclaw`
  3. 部署到 `claw` 命名空间

## CI/CD 前置条件
- `deploy-claw` job 通过 `tailscale/github-action@v3` 接入 tailnet 后再执行 `kubectl`。
- 需要在 GitHub 仓库 secrets 中配置：
  - `TS_AUTHKEY`：Tailscale auth key（建议绑定最小权限 tag，如 `tag:ci`）。
  - `KUBE_CONFIG`：Kubernetes kubeconfig 内容（apiserver 必须是 tailnet 可达地址）。
  - `REDIS_ADDR`
  - `REDIS_PASSWORD`
  - `STREAM_PREFIX`
  - `WECOM_CORP_ID`
  - `WECOM_CORP_SECRET`
  - `WECOM_RSA_PRIVATE_KEY`
  - `WECOM_SEQ_KEY`
- 目前仓库内默认值：
  - `REDIS_ADDR=redis:6379`
  - `STREAM_PREFIX=stream:group`
  - `WECOM_SEQ_KEY=msg:seq`
