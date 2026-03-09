# tinyclaw 下一步（可直接执行）

## MVP 目标
在企业微信中实现最小可用闭环：
- Ingress 拉取消息并写入 `stream:session:{session_key}`
- Ingress 触发 `ensure(session_key)` 拉起/唤醒 sandbox
- agent 在 sandbox 内自拉 Redis Stream 并串行消费
- 回发企业微信并在成功后 `XACK`

## 第一阶段（Day 1-2）
1. 定义统一事件 schema（message / reply / error）
2. 定义 Redis key 规范
3. 打通最小链路：
   - mock ingress -> session stream
   - mock ensure -> mock sandbox
   - mock agent pull -> mock reply

## 第二阶段（Day 3-4）
1. 接入 WeCom ingress（真实拉取）
2. 实现 `POST /internal/session-runtime/ensure`（create-or-get）
3. 接入 agent 真实消费循环（`XREADGROUP BLOCK`）
4. 接入 WeCom egress（真实回发）

## 第三阶段（Day 5-7）
1. 完善 ACK/重试/DLQ 链路
2. 失败重试（指数退避）
3. DLQ（`runtime_dlq` + `dlq:reply`）
4. 空闲策略（先软休眠，硬休眠按压测可选）

## 关键 Redis 设计（当前版）
- `stream:session:{session_key}`：会话消息流
- `cg:session:{session_key}`：会话 consumer group
- `lock:ensure:{session_key}`：ensure 防抖（`SET NX EX 3`）
- `dlq:reply`：回发死信
- `runtime_dlq`：sandbox 启动/运行死信

## 验收标准
1. 任意一条企业微信消息可在 5 秒内入流
2. 同 `session_key` 消息顺序处理，不并发乱序
3. agent 崩溃后可从 pending 恢复
4. 回发失败可重试，超过阈值进入 DLQ
5. 休眠会话在新消息到来后可自动唤醒

## 文档 Review 清单
1. 不出现 `stream:events` / `stream:dispatch` 旧方案
2. 不出现独立 session registry 表与 `last_seen_at` 依赖
3. 不出现“主服务 push turn 到 agent”的旧链路
4. 交付语义保持简化：成功回发后 `XACK`
