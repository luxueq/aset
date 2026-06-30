# STATE: AIGateway

## Project Reference

See: .planning/PROJECT.md (updated 2026-06-30)

**Core value:** 让任何 AI Agent 都能通过统一网关安全、高效、可观测地运行和协作
**Current focus:** Phase 1 — Core Gateway (Context gathered, ready for planning)

## Current Phase

**Phase 1: Core Gateway** — Status: Context Gathered (Ready for Planning)

## Progress

| Phase | Name | Status | Notes |
|-------|------|--------|-------|
| 1 | Core Gateway | Context Gathered | 41 decisions captured, ready for /gsd:plan-phase 1 |
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

## Resume

To resume work on this project, read: `.planning/phases/01-core-gateway/01-CONTEXT.md`

---
*Last updated: 2026-06-30 after Phase 1 context discussion*
