# tinyclaw 下一步

## 当前状态

- 主服务只保留 Core Model control plane。
- Channel Adapter 作为外部服务调用 TinyClaw HTTP API，不再由主服务直接拉取企业微信 archive。
- 旧 sandbox runtime、gRPC bridge、`messages/jobs/wecom_app_clients` 链路已经从当前代码中移除。
- 当前最小闭环是 `inbound message -> invocation -> delivery -> ack`。

## 当前优先级

1. 补 Channel Adapter 契约：
   - 明确 `POST /api/inbound` 的 channel 字段、source message id、room type、payload 约定。
   - 明确 `GET /api/deliveries?channel=<channel>&id=<last_id>` 的轮询和 ack 语义。
   - 为企业微信、微信群、斗鱼直播间分别补输入 payload 示例。
2. 补 invocation 执行侧：
   - 用进程内 scheduler 在 invocation 创建后启动执行。
   - 接入真实 agent runner，当前未配置时会落到 failed 并生成失败 delivery。
   - 补 queued/running invocation 的启动恢复与超时处理。
3. 补 schema 管理：
   - 把当前 `InitSchema` 迁移到显式 migration。
   - 处理历史库里旧表的保留或清理策略。
4. 补观测与联调：
   - 为 inbound、invocation、delivery、ack 增加基础指标。
   - 增加重复消息、active invocation append、delivery 重复 ack 的联调用例。

## PostgreSQL 当前范围

- `rooms`：TinyClaw room，与外部 channel room 映射。
- `messages`：room 内入站消息事实，`dispatch_state` 记录等待、忽略或绑定的 invocation。
- `invocations`：room 级 agent 执行。
- `deliveries`：invocation 产生的外发消息。

## 验收标准

1. 外部 Channel Adapter 能写入消息并触发 invocation。
2. 同一 room 在 queued/running invocation 存在时，新消息会追加到该 invocation。
3. invocation complete/fail 都能产生可轮询的 delivery。
4. Channel Adapter ack 后 delivery 不再重复返回。
5. 主服务启动不需要任何 provider-specific 配置。
