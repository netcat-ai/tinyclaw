# tinyclaw 下一步

## 当前状态

- 主服务只保留 Core Model control plane。
- Channel Adapter 作为外部服务调用 TinyClaw HTTP API，不再由主服务直接拉取企业微信 archive。
- 旧 sandbox runtime、gRPC bridge、`messages/jobs/wecom_app_clients` 链路已经从当前代码中移除。
- 当前最小闭环是 `registered room -> message -> agent session run -> delivery -> ack`。
- `AGENT_RUNNER=codex` 已可用；2026-05-20 已用 MobileClaw 真机跑通企业微信发送链路。
- Room Memory 第一条纵切已接入：Codex run 可获得 run-bound Memory Search capability，Agent Run Result 可携带 Memory Write Proposals，后台 worker 异步应用 Memory Write Jobs。

## 当前优先级

1. 补 Channel Adapter 契约：
   - 明确 `POST /api/rooms` 的 channel identity、display name、outbound alias、agent session 配置约定。
   - 明确 `POST /api/messages` 的 room id、source message id、payload 约定。
   - 明确 `GET /api/deliveries?channel=<channel>&id=<last_id>` 的轮询和 ack 语义。
   - 为企业微信、微信群、斗鱼直播间分别补输入 payload 示例。
2. 补 Room Memory 验证：
   - 继续补充真实 Codex 与真实业务 channel 的端到端联调用例。
   - 补 rejected / failed Memory Write Job 的可观测性。
   - 评估 TencentDB Agent Memory 是否适合作为 optional backend/sidecar。
3. 补 agent execution loop：
   - 固化 Codex runner 的运行参数、超时、日志与错误分类。
   - 补 agent session lock 过期后的恢复与超时处理。
4. 补 schema 管理：
   - 把当前 `InitSchema` 迁移到显式 migration。
   - 处理历史库里旧表的保留或清理策略。
5. 补观测与联调：
   - 为 room registration、message、agent run、delivery、ack 增加基础指标。
   - 增加重复消息、agent session trigger window、delivery 重复 ack 的联调用例。
   - MobileClaw 增加发送结果页，避免只依赖 logcat 判断发送结果。

## PostgreSQL 当前范围

- `rooms`：TinyClaw room，与外部 channel room 映射。
- `agent_sessions`：一个 Room 内的 agent 配置、trigger 边界和已处理消息边界。
- `messages`：room 内 append-only 入站消息事实，`skipped` 标记是否排除出 agent 上下文。
- `deliveries`：agent run 产生的外发消息，并记录 source message window。
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
