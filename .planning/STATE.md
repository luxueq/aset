---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: in_progress
last_updated: "2026-09-21T07:10:00.000Z"
progress:
  total_phases: 8
  completed_phases: 0
  total_plans: 6
  completed_plans: 5
  percent: 0
---

# STATE: AIGateway

## Project Reference

See: .planning/PROJECT.md (updated 2026-06-30)

**Core value:** 让任何 AI Agent 都能通过统一网关安全、高效、可观测地运行和协作
**Current focus:** Phase 01 — core-gateway

## Current Phase

**Phase 1: Core Gateway** — Status: In Progress (5/6 plans complete)

## Progress

| Phase | Name | Status | Notes |
|-------|------|--------|-------|
| 1 | Core Gateway | In Progress | 01-01~01-05 complete (skeleton, provider adapters, router+failover+auth, rate limiting, caching); 01-06 pending |
| 2 | SSE Agent Runtime | Not Started | 流式转发 + 背压控制 |
| 3 | MCP Server | Not Started | MCP 协议 + Tool 注册 |
| 4 | Observability | Not Started | OTel + Prometheus + 成本追踪 |
| 5 | Tool Executor & Sandbox | Not Started | 沙箱执行 + 浏览器自动化 |
| 6 | Agent Infrastructure | Not Started | 状态管理 + 多 Agent 通信 |
| 7 | Governance | Not Started | 多租户 + 安全合规 |
| 8 | SDK & Documentation | Not Started | Java + Go SDK |

## Decisions Log

| Date | Decision | Rationale |
|------|----------|-----------|
| 2026-06-30 | Go 语言 | 高并发 SSE/流式场景天然优势 |
| 2026-06-30 | 自研核心，参考大厂设计 | 深度掌控每个细节 |
| 2026-06-30 | 全量蓝图分期交付 | L0→L1→L2→L3 四层递进 |
| 2026-06-30 | OpenAI 兼容协议基准 | 行业事实标准 |
| 2026-06-30 | MCP 核心能力 | Agent 工具调用标准 |
| 2026-06-30 | SDK 优先 Java + Go | 团队主力技术栈 |
| 2026-06-30 | OTel→Prometheus→ClickHouse | 渐进式可观测性 |
| 2026-06-30 | Vertical MVP 模式 | 每阶段交付端到端能力 |
| 2026-06-30 | Phase 1 Context (41 decisions) | 见 01-CONTEXT.md |
| 2026-09-21 | 01-01: Drain rejection as mux-wrapping middleware | 单一 503 来源，atomic flag，避免 per-handler 状态 |
| 2026-09-21 | 01-02: 4-method Provider interface（无 ParseResponse/SupportsCapability） | 流式转发为原样字节透传，无归一化需求即无接口负担 |
| 2026-09-21 | 01-02: provider/model 前缀在 proxy 层 parseModel 剥离 | D-11 路由语义在入口统一处理，adapter 收到纯模型名 |
| 2026-09-21 | 01-02: 01-02 只补测试不改实现 | Wave 1 实现已满足全部验收标准，测试固化行为防回归 |
| 2026-09-21 | 01-03: Router 拥有 model→pool 解析（路由规则/provider pin/default provider） | parseModel 隐式 openai 默认移除，路由语义单一来源 |
| 2026-09-21 | 01-03: 5xx/429 触发跨提供商 failover，2xx/4xx 为最终结果 | 400 是客户端问题的答案而非故障，不应切换提供商 |
| 2026-09-21 | 01-03: 先 per-provider 重试（D-07）后跨提供商 failover（D-10） | 两层机制组合而不合并，各自可配置 |
| 2026-09-21 | 01-03: DELETE /v1/api-keys 改为 Revoke 软删除 | 撤销的 key 必须能返回 401，硬删除无法满足验收标准 |
| 2026-09-21 | 01-03: 滑动窗口错误率（5m/100 events/MinSamples=3）替代累计计数 | 累计计数在首次失败即驱逐提供商（rate=100%），破坏 failover 恢复 |

## Resume

To resume work on this project, read: `.planning/phases/01-core-gateway/01-05-SUMMARY.md`（下一计划：01-06-PLAN.md）

---
*Last updated: 2026-10-20 after completing 01-05 (caching: L1 LRU + L2 Redis + semantic + middleware + metrics)*
