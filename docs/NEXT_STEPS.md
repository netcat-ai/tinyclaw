# tinyclaw 下一步（2026-04-10）

## 当前状态
- 最小闭环已经打通：企业微信 archive ingress -> PostgreSQL `messages` -> room 级 `SandboxClaim` -> sandbox gRPC bridge -> PostgreSQL `jobs` -> Android 轮询发送。
- 当前实现已经不是“计划中的 gRPC”；`clawman` gRPC server、sandbox gRPC client 和 `messages[]` 批次协议都已落地。
- `messages` 当前状态机已扩成 `ignored / buffered / pending / sent / done`，但 `sent -> done` 的恢复策略还不完整。
- 控制面已从“随机会话句柄”收敛到“按 `room_id` 推导确定性 claim 名称”，但回收与长期复用策略仍未做完。

## 已完成
1. 明确 `room_id` 规则与主服务职责边界。
2. 落地 `SandboxTemplate` / `SandboxClaim` 基础集成。
3. 去掉“sandbox 自拉 Redis ingress”的旧链路。
4. 把 `agent` 收敛成 `/healthz` + gRPC bridge runtime。
5. 打通 `clawman gRPC server + sandbox gRPC client` 的最小消息协议。
6. 引入 `jobs` pull outbox，由 Android 发送端轮询拉取。
7. 引入 `pending -> sent -> done` 状态推进，并在启动时执行 `sent -> pending` 恢复。

## 当前优先级
1. 完善 `sent` 恢复路径：
   - 处理“`jobs` 已成功写入，但 `messages.done` 更新失败”后消息卡在 `sent` 的问题。
   - 决定是否补 reply 幂等键、去重策略或后台修复任务。
2. 补 room / claim 回收：
   - 为进程内 room session cache 增加回收策略。
   - 为遗留 `SandboxClaim` 增加清理或 idle 回收策略。
3. 补缓存回收：
   - 为 `ttlCache` 增加主动清扫，避免仅靠读路径删除过期项。
4. 补观测与联调：
   - 为 gRPC 断线、room connect 等待超时、`sent` backlog、`done` 更新失败增加指标与日志。
   - 完成冷启动、热路径、失败恢复、Android 拉取链路的真实环境联调。

## 下一阶段
1. 把最小 `result/error` 协议扩展成 richer streaming：
   - `typing`
   - `assistant_delta`
   - `assistant_final`
   - `tool_event`
   - `failed`
2. 为 gRPC message gateway 增加鉴权、心跳和更稳定的断线恢复模型。
3. 引入 scheduler / reminder 输入源，但仍由 `clawman` 持久化与调度，不下放到 sandbox。
4. 接入长期 memory 与文件上下文能力，保持 `messages` 为原始事实源。
5. 评估 warm pool、idle / hibernate / terminate 自动化策略。
6. 设计 skill gateway：
   - 由 `clawman` 提供私有 `find_skill / install_skill` 能力。
   - 评估是否把 [FindSkills](https://www.findskills.org/) 作为外部 skill discovery 上游，而 `clawman` 负责 trust、allowlist、缓存与安装分发。

## 部署专项
1. 固化 `SandboxTemplate` 契约：
   - 统一镜像
   - 统一端口 `8888`
   - `/healthz` readiness / liveness
   - Claude 运行时环境变量
   - `CLAWMAN_GRPC_ADDR`
2. 固化 `clawman` gRPC server 暴露方式、Service 命名和网络访问策略。
3. 明确 graceful shutdown 时 `SandboxClaim` 删除策略，避免和“跨重连复用”目标互相冲突。

## PostgreSQL 最小范围（当前版）
- `messages`：企业微信 archive 入站事实、状态机、`seq` checkpoint
- `rooms`：agent-managed room 元数据
- `jobs`：Android 发送端增量拉取的外发任务
- `wecom_app_clients`：Android 客户端认证配置

## 验收标准
1. 任意一条企业微信消息可触发对应 sandbox ready 并拿到回复。
2. sandbox 通过 gRPC 主动连接 `clawman`，不再经过 router/HTTP。
3. 同一 `room_id` 的 claim 名称稳定可推导。
4. sandbox 返回错误、上下文超时或 `jobs` 写入失败时，对应消息能从 `sent` 恢复到 `pending`。
5. `jobs.done` 更新失败不会让消息长期卡在 `sent`。

## 文档 Review 清单
1. 不再出现“agent 在 sandbox 内自拉 Redis Stream”的描述。
2. 不再出现“当前实现仍然依赖 `sandbox-router + HTTP /agent`”的描述。
3. 统一使用 `SandboxTemplate`、`SandboxClaim`、`RoomChat(stream Message)` 术语。
4. Android 拉取链路统一描述为“`client_id` 鉴权后映射到 `bot_id` 过滤 `jobs`”，不再把 `jobs` 写成按 `client_id` 直绑。
5. 状态机统一描述为 `ignored / buffered / pending / sent / done`。
