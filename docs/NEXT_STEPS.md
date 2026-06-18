# tinyclaw 下一步

## 当前闭环

- Core Model 已收敛为 `Room -> Message -> Agent Session -> Delivery -> Ack`。
- 每个 Room 只有一个长期 Agent Session；用户可创建私有 Agent，并可将其共享为 run-scoped Subagent，被 `@key` / `@display_name` 寻址。
- Adapter 注册入口先查询 Room；已存在则直接返回，不覆盖 Room Prompt、显示名、外发别名或 Agent Session 配置。
- Message 是 channel-shaped append-only 事实；是否触发、是否进入上下文由 Trigger Policy、Command handler 和 runner 选择决定。
- Delivery 是 outbound intent；ack 只表示 consumer 已处理成功，不要求写回 Message。
- Room Prompt 是 `rooms.prompt` 上的行为设定；Codex prompt 会注入当前 Room Prompt。
- Room Memory 归属于 Room；Codex run 通过短期 token 做 Memory Search，通过 Memory Write Jobs 异步写入。
- `/draw <prompt>` 是 Clawman-owned Command，不进入普通 Agent Run。
- Control UI 已转向在线聊天室形态，支持 Channel Chat、Channel Memory、Channel Settings、Room Prompt、Delivery ack，以及用户创建 / 共享 Agent。
- Schema 当前仍由 `InitSchema` 应用；`agents.owner_id` / `agents.visibility` 使用兼容 `ALTER TABLE` 补列。
- 基础指标已覆盖 room registration、message ingestion、agent run、delivery、ack、memory write。
- Agent Run 结构化日志已覆盖 run 边界、context message count、selected subagent keys/ids、memory search count、memory write job count 和 delivery id。
- 数据库 E2E 已覆盖重复 Message、运行中新触发消息、Delivery 重复 ack、Memory Search、Memory Write 和 `/draw` 幂等副作用。
- 本地 PostgreSQL + Clawman + Control Plane + Codex runner smoke 已验证；企业微信 Linux SDK 链路暂不作为 macOS 本地验收项。

## 剩余优先级

1. 真实联调：
   - `tinybridge` 的企业微信 / 微信 adapter 已按新 `source + msgid` Message contract 写入；继续做真实设备 / Linux SDK 端到端联调。
   - WOC 微信入口优先采用 instance-side push hook：`woc-watch` 负责 cursor、解密缓存和 batch retry，TinyBridge adapter 只接收 raw-style message batch 并写入 Clawman。
   - MobileClaw 发送成功后 ack Delivery。
   - consumer 若主动写回 agent 消息，使用 `source = agent` 且 `msgid = "delivery:<delivery_id>"` 保证幂等。

2. Subagent 深化：
   - 当前实现只把被 `@` 的 shared Agent 定义注入 Codex prompt。
   - Private Agent 暂不参与 Room mention 选择；后续如需“本人可在频道调用私有 Agent”，需要把触发消息 sender 与 Agent owner 做可见性匹配。
   - 后续如需要真正并行执行，再新增 run-step 日志和子任务执行器；不要新增长期 `room_agents` 或多 Agent Session。

3. Schema 管理：
   - 当前可清库，`InitSchema` 直接应用最新表结构。
   - `messages` 已拆出 `msgid/action/from_id/tolist/roomid/msgtime/msgtype/body`；adapter-local cursor 不进入 Clawman。
   - 后续生产数据需要保留时，再引入显式 migrations。

4. QA：
   - 企业微信 / 微信真实设备链路联调后，补对应 adapter smoke 记录。

## 验收标准

1. Adapter 能注册 Room、写入 Message，并按 Trigger Policy 的立即触发或 batch 审阅触发推进 `pending_trigger_message_id`。
2. 同一 Agent Session 运行中收到新触发消息时，当前 run 不扩窗，下一轮继续处理。
3. Agent Run 成功输出或失败提示都能产生可轮询 Delivery。
4. Delivery ack 幂等，ack 后不再出现在 pending 轮询中。
5. Agent Run 可通过 token-bound Memory Search 召回当前 Room 的 active Memory Items。
6. Agent Run Result 可创建 Memory Write Jobs，后台 worker 应用后下一轮可召回。
7. `@agent` 命中的可配置 Agent 会作为 run-scoped Subagent 进入 prompt，但不创建长期 Session。
8. `/draw <prompt>` 只在新插入 Message 时启动一次副作用，重复 Message 不重复扣费或重复发图。
9. Control Plane UI 可完成 Room 排障、Room Prompt 编辑、Agent 定义编辑、Inject Message 和 Delivery ack。

## 本地验收证据

- `./scripts/local_start.sh` 可启动本地 PostgreSQL、Clawman、Control Plane 和 Codex runner；`./scripts/local_stop.sh` 可停止本地 Clawman。
- `./scripts/local_status.sh` 覆盖依赖命令、PostgreSQL health、Clawman `/healthz`、Admin UI、metrics endpoint 和活动指标。
- `./scripts/local_smoke.sh` 覆盖 Room 注册、Message 写入、Codex Agent Run、Delivery 轮询、`agent_output` payload、Delivery ack 和 ack 后 pending 消失。
- `./scripts/local_admin_smoke.sh` 覆盖 Control Plane API 凭据、Agent 创建/更新、Admin Room 注册、Inject Message、Timeline 和 Memory endpoint。
- `./scripts/local_wechat_adapter_smoke.sh` 覆盖 `tinybridge/cmd/wechat-adapter` 通过 fake `wx history` 注册 `wechat` Room、写入 provider-shaped Message，并用 `{"mode":"never"}` 验证 adapter contract 时不触发 Agent Run。
- `./scripts/local_verify.sh` 覆盖 Control Plane lint/build、Clawman lint/test、TinyBridge lint/test、本地 smoke、微信 adapter smoke 和最终 status。
- `TINYCLAW_VERIFY_FULL=true ./scripts/local_verify.sh` 额外覆盖 PostgreSQL-backed E2E；该路径会重置本地测试库，再通过 smoke 回填一个本地 Room。
- `core_e2e_test.go` 覆盖重复 Message、Delivery 重复 ack、Memory Search、Memory Write、`/draw` 幂等副作用和微信形态 Delivery。
- `internal/storage/core_test.go` 覆盖运行中新触发 Message 时当前 Agent Run 不扩窗，以及 batch trigger 按累计消息触发。
- `internal/executor/scheduler_test.go` 覆盖 Agent Run 成功、失败、空输出和 `@agent` run-scoped Subagent 选择。
- `internal/executor/codex_runner_test.go` 覆盖 Codex prompt、Room Prompt 注入、Codex thread resume/fallback、Memory Search loop 和 Memory Write Proposal 解析。
- `internal/api/core_test.go` 覆盖当前 Core Message contract、Delivery polling/ack、Admin auth、Agent 定义编辑和 Memory Search token。

企业微信 archive adapter 依赖 Linux+cgo WeCom Finance SDK；macOS 本地验收只证明 Clawman Core Model、Control Plane、Codex runner、TinyBridge 编译测试和本地/微信形态 API contract。真实企业微信 SDK、真实微信设备、WOC push hook 和 MobileClaw 发送后 ack 仍属于真实设备/Linux 联调项。
