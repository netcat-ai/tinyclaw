# tinyclaw 下一步

## 当前闭环

- Core Model 已收敛为 `Room -> Message -> Agent Session -> Delivery -> Ack`。
- 每个 Room 只有一个长期 Agent Session；多个用户可配置 Agent 作为 run-scoped Subagents 被 `@key` / `@display_name` 寻址。
- Message 是原始 append-only 事实；是否触发、是否进入上下文由 Trigger Policy、Command handler 和 runner 选择决定。
- Delivery 是 outbound intent；ack 只表示 consumer 已处理成功，不要求写回 Message。
- Room Memory 归属于 Room；Codex run 通过短期 token 做 Memory Search，通过 Memory Write Jobs 异步写入。
- `/draw <prompt>` 是 Clawman-owned Command，不进入普通 Agent Run。
- Control Plane UI 已支持 Room Timeline、Room Memory、Room Settings、Inject Message、Delivery ack 和 Agent 定义编辑。
- Schema 不再考虑旧库兼容；如结构变化，允许清库后按 `schema.sql` 重建。

## 剩余优先级

1. 真实联调：
   - 企业微信 / 微信 adapter 按新 `source` contract 写入 Message。
   - MobileClaw 发送成功后 ack Delivery。
   - consumer 若主动写回 agent 消息，使用 `source_message_id = "delivery:<delivery_id>"` 保证幂等。

2. Subagent 深化：
   - 当前实现只把被 `@` 的 Agent 定义注入 Codex prompt。
   - 后续如需要真正并行执行，再新增 run-step 日志和子任务执行器；不要新增长期 `room_agents` 或多 Agent Session。

3. 观测：
   - 为 room registration、message ingestion、agent run、delivery、ack、memory write 增加基础指标。
   - Agent Run 日志记录 selected subagent keys/ids、memory search count、memory write job count。

4. Schema 管理：
   - 当前可清库，`InitSchema` 直接应用最新表结构。
   - 后续生产数据需要保留时，再引入显式 migrations。

5. QA：
   - 补重复 Message、运行中新触发消息、Delivery 重复 ack、Memory Search 降级、Agent 定义编辑的端到端用例。

## 验收标准

1. Adapter 能注册 Room、写入 Message，并按 Trigger Policy 推进 `pending_trigger_message_id`。
2. 同一 Agent Session 运行中收到新触发消息时，当前 run 不扩窗，下一轮继续处理。
3. Agent Run 成功输出或失败提示都能产生可轮询 Delivery。
4. Delivery ack 幂等，ack 后不再出现在 pending 轮询中。
5. Agent Run 可通过 token-bound Memory Search 召回当前 Room 的 active Memory Items。
6. Agent Run Result 可创建 Memory Write Jobs，后台 worker 应用后下一轮可召回。
7. `@agent` 命中的可配置 Agent 会作为 run-scoped Subagent 进入 prompt，但不创建长期 Session。
8. `/draw <prompt>` 只在新插入 Message 时启动一次副作用，重复 Message 不重复扣费或重复发图。
9. Control Plane UI 可完成 Room 排障、Agent 定义编辑、Inject Message 和 Delivery ack。
