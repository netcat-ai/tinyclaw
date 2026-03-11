# TinyClaw 多 Agent 协作指南

## 概述

TinyClaw 的多 agent 机制基于 **渐进性披露** 和 **上下文隔离** 原则，通过子会话实现任务委托和结果回传。

## 核心概念

### 会话层级

```
主会话: stream:session:room-123
  └─ 子会话: stream:session:room-123:subagent:a1b2c3d4
      └─ 孙会话: stream:session:room-123:subagent:a1b2c3d4:subagent:e5f6g7h8
```

每个子会话：
- 拥有独立的 Redis stream
- 运行在独立的 sandbox 中
- 继承父会话的权限边界（tenant_id、chat_type）

### 工作流程

1. **父 agent 发起任务**：调用 `sessions_spawn` 创建子会话
2. **子 agent 执行**：在隔离环境中处理任务
3. **自动通知**：子 agent 完成后，结果自动写入父 stream
4. **父 agent 继续**：收到结果后继续处理

## 使用示例

### 场景 1：SQL 查询 + 验证

用户请求："查询本月销售额"

```go
// 主 agent 收到请求后
spawner := NewSessionSpawner(redis, ensurer, "stream:session")

// 步骤 1: 委托 SQL agent 查询
resp, _ := spawner.Spawn(ctx, SpawnRequest{
    ParentSessionKey: "room-123",
    AgentID:          "sql-agent",
    Task:             "查询本月销售总额，返回数值",
    Model:            "gpt-3.5-turbo",
    TenantID:         "corp-001",
    ChatType:         "group",
})

// 主 agent 继续 XREADGROUP，等待子 agent 结果
// 子 agent 完成后自动发送 subagent.result 事件

// 步骤 2: 收到结果后，委托 validator agent 验证
spawner.Spawn(ctx, SpawnRequest{
    ParentSessionKey: "room-123",
    AgentID:          "validator-agent",
    Task:             "验证数值是否合理：" + sqlResult,
    TenantID:         "corp-001",
    ChatType:         "group",
})

// 步骤 3: 验证通过后，回复用户
```

### 场景 2：搜索 + 摘要

用户请求："搜索最新的 AI 新闻并总结"

```go
// 步骤 1: 搜索
spawner.Spawn(ctx, SpawnRequest{
    ParentSessionKey: "user-alice",
    AgentID:          "search-agent",
    Task:             "搜索最近7天的AI行业新闻，返回前5条",
    Model:            "gpt-4o-mini",
})

// 步骤 2: 收到搜索结果后，生成摘要
// (主 agent 处理，或再委托给 summary-agent)
```

## Agent 注册表

当前可用的专业 agent：

| Agent ID | 用途 | 默认模型 | 工具 |
|----------|------|----------|------|
| `sql-agent` | 数据库查询 | gpt-3.5-turbo | sql_query |
| `validator-agent` | 结果验证 | gpt-3.5-turbo | - |
| `search-agent` | 网络搜索 | gpt-4o-mini | web_search |
| `file-agent` | 文件处理 | gpt-4o-mini | file_read, file_parse |

## 最佳实践

### 1. 任务描述要明确

❌ 错误：`"处理订单数据"`
✅ 正确：`"查询订单表，统计本月总金额，返回数值"`

### 2. 使用廉价模型

对于简单任务（SQL 查询、格式转换），使用 `gpt-3.5-turbo` 降低成本。

### 3. 强制验证步骤

对于关键操作（数据修改、外部调用），必须经过 validator-agent 复核。

### 4. 避免过深嵌套

建议最多 2 层子会话（主 → 子 → 孙），避免调试困难。

## 成本对比

| 方案 | Token 消耗 | LLM 调用次数 | 成本 |
|------|-----------|-------------|------|
| 单 agent（长上下文） | ~8000 tokens | 1 次 | 高 |
| 多 agent（上下文隔离） | ~2000 tokens × 3 | 3 次 | 低 30% |
| 带 Orchestrator | ~2000 tokens × 3 + 1000 | 4 次 | 高 20% |

TinyClaw 的无 Orchestrator 设计节省了额外的编排调用。

## 故障处理

### 子 agent 超时

子 agent 如果 30 秒内未响应，父 agent 应：
1. 记录超时日志
2. 向用户报告："子任务处理超时，请稍后重试"
3. 不阻塞主流程

### 子 agent 返回错误

子 agent 通过 `status: "failed"` 标记失败：

```go
spawner.Announce(ctx, AnnounceRequest{
    ParentSessionKey: "room-123",
    ChildSessionKey:  childKey,
    AgentID:          "sql-agent",
    Result:           "查询失败：表不存在",
    Status:           "failed",
})
```

父 agent 收到后应降级处理或请求用户澄清。

## 扩展新 Agent

在 `agent_registry.go` 中添加：

```go
"custom-agent": {
    ID:          "custom-agent",
    Name:        "自定义Agent",
    Description: "处理特定业务逻辑",
    Model:       "gpt-4o-mini",
    Tools:       []string{"custom_tool"},
},
```

然后在 sandbox 中实现对应的工具和 prompt。
