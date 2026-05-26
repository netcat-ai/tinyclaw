# tinyclaw 下一步

## 当前状态

- 主服务只保留 Core Model control plane。
- Channel Adapter 作为外部服务调用 TinyClaw HTTP API，不再由主服务直接拉取企业微信 archive。
- 旧 sandbox runtime、gRPC bridge、`messages/jobs/wecom_app_clients` 链路已经从当前代码中移除。
- 当前最小闭环是 `registered room -> message -> agent session run -> delivery -> ack`。
- `AGENT_RUNNER=codex` 已可用；2026-05-20 已用 MobileClaw 真机跑通企业微信发送链路。
- Codex runner 复用同一 Agent Session 的 Codex CLI thread，continuation id 保存在 `agent_sessions.codex_session_id`。
- 生产 Codex runner 默认关闭 `apps,tool_suggest,plugins` 三个 Codex CLI feature；当前 K8s 部署用 `hostAliases` 修正 `api.openai.com` 与 `chatgpt.com` 的集群 DNS 污染。
- Room Memory 第一条纵切已接入：Codex run 可获得 run-bound Memory Search capability，Agent Run Result 可携带 Memory Write Proposals，后台 worker 异步应用 Memory Write Jobs；Memory Search 失败会降级为 Codex 可见的 error result，不阻塞当前回复。
- `/draw <prompt>` 已确认作为第一版 Clawman-owned Command 设计：直接调用 `gpt-image-2` 生成图片，上传 S3-compatible object storage，并通过 Delivery 携带 24h presigned S3 URL。

## 当前优先级

1. 实现 `/draw` Generated Media 纵切：
   - 破坏性统一 source message window 字段为 `source_message_from_id` / `source_message_to_id`。
   - 在 `POST /api/messages` 的新 Message 路径识别 trim 后行首 `/draw` 命令；重复 Message 不启动副作用。
   - `/draw` 不更新 Agent Session trigger boundary；Message 表不保存 `skipped`。
   - 第一版用 in-process background goroutine 执行，不提供 crash recovery。
   - 调用 image provider：默认 `IMAGE_PROVIDER_BASE_URL=https://code.v4.chat`、`IMAGE_PROVIDER_MODEL=gpt-image-2`，API key 优先 `IMAGE_PROVIDER_API_KEY`，否则兼容读取 `CODEX_AUTH_JSON.OPENAI_API_KEY`。
   - 上传 PNG 到 S3-compatible object storage，Delivery payload 使用 `media_url_kind=presigned_s3` 和 24h presigned URL。
   - 成功产生 `command_progress`、`command_output` 文本、`command_output` 图片三条 Delivery；失败产生 `command_failure`。
2. 补 Channel Adapter 契约：
   - 明确 `POST /api/rooms` 的 channel identity、display name、outbound alias、agent session 配置约定。
   - 明确 `POST /api/messages` 的 room id、source message id、payload 约定。
   - 明确 `GET /api/deliveries?channels=<channels>` 的 pending 轮询和 ack 语义。
   - 为企业微信、微信群、斗鱼直播间分别补输入 payload 示例。
3. 补 Room Memory 验证：
   - 继续补充真实 Codex 与真实业务 channel 的端到端联调用例。
   - 评估 TencentDB Agent Memory 是否适合作为 optional backend/sidecar。
4. 补 agent execution loop：
   - 固化 Codex runner 的运行参数、超时、日志与错误分类。
   - 补 agent session lock 过期后的恢复与超时处理。
5. 补 Clawman Control Plane UI：
   - 新增 `api_clients` 鉴权模型，使用 HTTP Basic 的 `client_id/client_secret`；第一版权限只区分 `adapter` 和 `admin` 能力包。
   - 第一版默认一个配置内建 `admin` client；secret 读取 `CLAWMAN_ADMIN_SECRET`，不写入 `api_clients`，未配置则禁用默认 admin client 和 `/admin/api/*`。
   - 第一阶段先实现 `/admin/api/*` 后端读模型和管理写入口，再搭 `web/control/` 前端工程。
   - 新增 `web/control/` 独立 Vue 3 + Composition API + UnoCSS + pnpm 前端工程。
   - 新增 `/admin/api/*` 管理接口，支撑 Room 列表、Room Timeline、Room Settings、Delivery ack。
   - Room 列表聚合展示 channel、display/outbound target、默认 Agent Session 边界、pending Delivery 数和最近消息时间。
   - Room Timeline 合并展示 Messages、Agent Session trigger/processed 边界、Deliveries 和 ack 状态。
   - Room Timeline 默认返回最近 N 条 Room 事件，并支持 `before_message_id` 向前翻页。
   - Room Detail 提供只读 Room Memory tab，支持按 status/type 过滤 Memory Items。
   - 第一版允许注册/更新 Room、启用/停用默认 Agent Session、更新 `display_name` / `outbound_alias` / `trigger_policy`、ack Delivery、Inject Message。
   - Inject Message 是调试用入站 Message 注入，不是外部发送；source id 使用 `admin:<uuid>`，并进入 Room Timeline 审计。
   - 第一版不提供删除 Room、删除 Message、直接修改 Message、直接修改 Room Memory。
   - 构建产物可由 clawman 同域服务到 `/admin/`，但前端源码和构建链保持独立，后续可单独部署。
6. 补 schema 管理：
   - 把当前 `InitSchema` 迁移到显式 migration。
   - 处理历史库里旧表的保留或清理策略。
7. 补观测与联调：
   - 为 room registration、message、agent run、delivery、ack 增加基础指标。
   - 增加重复消息、agent session trigger window、delivery 重复 ack 的联调用例。
   - MobileClaw 增加发送结果页，避免只依赖 logcat 判断发送结果。

## PostgreSQL 当前范围

- `api_clients`：可调用 Clawman HTTP API 的外部或管理客户端，保存 client secret hash 和权限集合。
- `rooms`：TinyClaw room，与外部 channel room 映射。
- `agent_sessions`：一个 Room 内唯一默认 orchestrator 的启用状态、trigger 边界、caught-up 边界和 Codex CLI continuation id。
- `agents`：用户可配置的 agent 定义，可被 @ 寻址，也可作为 run-scoped Subagent 执行。
- `messages`：room 内 append-only 原始消息事实；是否触发 agent、是否进入上下文由 Trigger Policy、Command handler 和 runner 读取策略决定。
- `deliveries`：agent run 或 command 产生的外发消息，并用 `source_message_from_id` / `source_message_to_id` 记录来源 Message 闭区间。
- `memory_items`：Room-owned durable memory，第一版类型为 fact / preference / todo。
- `memory_write_jobs`：Agent Run Result 产生的异步 memory 写入任务。
- `memory_change_audit`：Room Memory 变更审计。
- `memory_capability_tokens`：绑定到 Agent Run 的短期 Memory Search authority。

## 验收标准

1. 外部 Channel Adapter 能注册 Room 后写入 Message，并更新匹配 Agent Session 的 trigger 边界。
2. 同一 Agent Session 运行中收到新触发消息时，当前 run 不扩窗，下一轮 run 继续处理。
3. agent run 成功输出或失败提示都能产生可轮询的 delivery。
4. Channel Adapter ack 后 delivery 不再重复返回。
5. Agent Run 可以通过 token-bound Memory Search 召回当前 Room 的 active Memory Items。
6. Agent Run Result 可以创建 Memory Write Jobs，后台 worker 应用后下一轮可被 Memory Search 召回。
7. 主服务启动不需要任何 provider-specific 配置。
8. Codex runner 可以在运行中先请求 Memory Search，再用召回的 Memory Items 生成最终输出。
9. 同一 Agent Session 的第二次及后续 Codex run 会使用已保存的 `codex_session_id` 继续同一个 Codex CLI thread。
10. `/draw <prompt>` 只在新插入 Message 时启动一次异步生图；重复 Message 不重复扣费或重复发图。
11. `/draw` 成功时 Delivery 顺序为 `正在画图...`、`图片已生成：<media_id>`、图片 payload；图片 payload 携带 24h presigned S3 URL。
12. `/draw` 不推进 Agent Session trigger boundary，不写 Room Memory，失败通过 `command_failure` Delivery 异步告知用户。
13. Control Plane UI 可以查看 Room Timeline，并能完成 Room 注册/更新、默认 Agent Session 启停、Delivery ack、Inject Message。
