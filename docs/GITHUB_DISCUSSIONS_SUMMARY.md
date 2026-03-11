# GitHub 讨论总结

本文档总结了 TinyClaw 项目在 GitHub 上发起的三个核心讨论，以及基于社区反馈的技术方案演进。

---

## 讨论 1：用户故事征集

**Issue**: [#12 用户故事征集：你希望在企业微信中用 AI Agent 做什么？](https://github.com/fishioon/tinyclaw/issues/12)

**目标**：收集真实的企业场景和痛点，指导技术路线图

**已提出的 8 个场景**：
1. 智能客服自动化（电商企业，客服成本降低 60%）
2. 内部知识库问答（科技公司，入职时间 2 周 → 3 天）
3. 数据查询与报表生成（零售企业，查询时间 1-2 天 → 10 秒）
4. 审批流程自动化（制造企业，审批时间 3 天 → 4 小时）
5. 会议纪要自动生成（咨询公司，整理时间 30 分钟 → 1 分钟）
6. 销售线索自动跟进（B2B 企业，线索流失率 40% → 10%）
7. IT 运维自动化（互联网公司，告警响应 30 分钟 → 1 分钟）
8. HR 招聘自动化（创业公司，简历筛选 2 小时 → 10 分钟）

**参与方式**：在 Issue 下评论分享你的场景

---

## 讨论 2：企业工具集成优先级投票

**Issue**: [#13 企业工具集成优先级投票 - 你最需要哪些？](https://github.com/fishioon/tinyclaw/issues/13)

**目标**：确定企业工具集成的优先级

**候选工具（10 个）**：
1. 智能表格（记忆存储）
2. 企业云盘（文件上下文）
3. 审批流（流程自动化）
4. CRM 系统（线索管理）
5. 项目管理（任务跟踪）
6. BI 报表（数据查询）
7. 邮件系统（邮件处理）
8. 日历系统（会议安排）
9. 数据仓库（数据分析）
10. 监控告警（故障处理）

**参与方式**：在 Issue 下投票并说明使用场景

---

## 讨论 3：多 Agent 协作体系设计

**Issue**: [#14 多 Agent 协作体系设计方案（RFC）](https://github.com/fishioon/tinyclaw/issues/14)

**目标**：设计多 Agent 协作架构，解决复杂任务分解和上下文隔离问题

### 方案演进

#### 第一版：Orchestrator + Event Bus（已否定）

**架构**：
```
用户消息 → Orchestrator → 解析工作流 → 调度 Agent 1 → Agent 2 → Agent 3 → 聚合结果
```

**问题**：
- 过度设计，需要预定义工作流（YAML/JSON）
- Orchestrator 成为单点瓶颈
- 不灵活，无法动态调整
- 开发成本高（5000+ 行代码）

---

#### 第二版：渐进性披露 + 上下文隔离

**核心理念**（来自社区反馈）：
> 完成一个事情需要经过多步才能保证结果。每个 Agent 只知道自己需要的信息，通过**交接模板**约束必须做什么，下个 Agent 必须**检查复核**上个 Agent 的内容。

**设计原则**：
1. **上下文隔离**：每个 Agent 只接收最小必要信息（< 500 字 vs 18000 字）
2. **交接模板**：强制结构化交接，验证必要字段
3. **强制复核**：下个 Agent 必须检查上个 Agent 的输出
4. **成本优化**：小上下文 + 小模型，成本降低 99%

**解决的问题**：
- ✅ 降低幻觉（上下文污染导致的错误）
- ✅ 强制验证（避免直接执行危险操作）
- ✅ 可追溯性（每步可审计）

---

#### 第三版：基于 OpenClaw 实践的简化方案（当前推荐）

**灵感来源**：深入研究 [OpenClaw](https://github.com/openclaw/openclaw) 的多 Agent 实现

**OpenClaw 的核心设计哲学**：
1. **Session 是一切的基础**：每个 Agent 运行在独立 session 中
2. **工具驱动协作**：通过 `sessions_spawn` 工具让 Agent 自己决定何时启动子 Agent
3. **天然上下文隔离**：子 Agent 只看到 `task` 参数，父 Agent 只看到 `result`
4. **配置控制权限**：通过配置管理每个 Agent 的工具权限

**新架构**：
```
用户消息 → Main Agent → 自己决定是否需要子 Agent → 调用 sessions_spawn → 子 Agent 独立运行 → 结果自动返回
```

**核心组件**：

1. **Session 管理**
```go
// Session Key 格式
stream:session:{chat_id}                    // 主会话
stream:session:{chat_id}:subagent:{uuid}    // 子 Agent 会话
```

2. **sessions_spawn 工具**
```go
sessions_spawn({
    agentId: "sql-agent",
    task: "查询订单数据",
    model: "gpt-3.5-turbo",  // 可选：使用更便宜的模型
    context: {               // 最小上下文
        user_id: "user_123"
    }
})
```

3. **自动 Announce 机制**
- 子 Agent 完成后自动发送结果到父 Agent 的 Stream
- 父 Agent 收到 `subagent.completed` 事件
- 无需手动聚合结果

4. **工具权限控制**
```yaml
agents:
  defaults:
    tools:
      allow: ["*"]  # 主 Agent 所有工具
    
    subagents:
      tools:
        deny: ["exec", "write", "sessions_spawn"]  # 子 Agent 禁止危险工具
        allow: ["read", "search", "sql_query"]
      
      max_concurrent: 8
      max_children_per_agent: 5
      max_spawn_depth: 2
```

**优势对比**：

| 维度 | 旧方案（Orchestrator） | 新方案（OpenClaw 风格） |
|------|---------------------|---------------------|
| 架构复杂度 | 高（5000+ 行） | 低（500 行） |
| 灵活性 | 低（预定义工作流） | 高（Agent 动态决策） |
| 上下文隔离 | 需要手动设计 | 天然隔离（Session） |
| 开发成本 | 4 周 | 1 周 |
| 维护成本 | 高 | 低 |
| 单点故障 | 有（Orchestrator） | 无 |

**完整示例**：

```
用户："我上个月买的手机还没到，帮我查一下"
    ↓
Main Agent 分析：需要查询订单 + 查询物流
    ↓
并行启动 2 个子 Agent：
    ├─ sessions_spawn({agentId: "sql-agent", task: "查询订单"})
    └─ sessions_spawn({agentId: "logistics-agent", task: "查询物流"})
    ↓
子 Agent 1 完成 → announce 结果到 Main Agent
子 Agent 2 完成 → announce 结果到 Main Agent
    ↓
Main Agent 收到 2 个结果 → 综合回复用户
```

**成本对比**：
- 单 Agent（全用 GPT-4）：$0.16
- 多 Agent（小模型 + 大模型）：$0.001
- **节省 99%**

---

## 技术决策

### 已确定
- ✅ 采用 OpenClaw 风格的简化方案
- ✅ 实现 `sessions_spawn` 工具
- ✅ 基于 Redis Streams 的 Session 管理
- ✅ 配置驱动的工具权限控制

### 待讨论
- ⏳ 是否需要保留部分 Orchestrator 功能？
- ⏳ 交接模板是否需要强制验证？
- ⏳ 如何平衡灵活性和安全性？

---

## 参与讨论

欢迎在各个 Issue 下评论：
- [Issue #12](https://github.com/fishioon/tinyclaw/issues/12) - 分享你的使用场景
- [Issue #13](https://github.com/fishioon/tinyclaw/issues/13) - 投票你需要的工具
- [Issue #14](https://github.com/fishioon/tinyclaw/issues/14) - 讨论技术方案

---

## 参考资料

- [OpenClaw 源码](https://github.com/openclaw/openclaw)
- [OpenClaw Subagents 文档](https://docs.openclaw.ai/tools/subagents)
- [OpenClaw Multi-Agent 文档](https://docs.openclaw.ai/tools/multi-agent-sandbox-tools)
- [Minimal Multi-Agent Solution](http://tools.leng.ishanggang.com/omo/minimal.html#solution)

---

**最后更新**：2026-03-11
