# AIGateway

## What This Is

AIGateway 是一个生产级 AI Agent 基础设施平台，用 Go 从零构建，参考 Portkey、Cloudflare AI Gateway、LiteLLM 等大厂设计。它为内部团队和外部客户提供统一的 AI 模型接入、Agent 运行时、MCP 协议支持和多 Agent 协作能力。

核心定位：**Agent 运行时网关** —— 不只是 API 代理，而是 Agent 运行时的基础设施层，处理 SSE 长连接、流式转发、工具执行、状态管理和 Agent 间通信。

## Project Goals

### 独立自研，非拼凑

本项目**不是**市场主流项目（Portkey、LiteLLM、Kong 等）的代码拼凑或 fork 改造版本。

- **架构独立设计**：整体架构、模块划分、接口定义由团队独立设计，参考竞品但不复制其架构
- **代码全部手写**：核心功能（代理、路由、限流、缓存、MCP 等）全部自行实现，不 fork 或拷贝任何开源项目的代码
- **参考而非抄袭**：调研竞品是为了理解行业标准和最佳实践，不是照搬实现。知道"做什么"不等于复制"怎么做"

### 生产级可运行

不是 demo、不是 POC、不是玩具项目。

- **每个 Phase 交付即可上线**：Phase 1 完成后就能作为独立服务运行，处理真实流量
- **生产级质量要求**：错误处理完整、优雅停机、健康检查、日志审计、连接管理、背压控制
- **性能可量化**：并发能力、延迟指标、资源消耗有明确的基准测试

### 知识产权清洁

不受市场主流项目的版权及专利影响。

- **不依赖任何开源 AI 网关的代码**：不使用 GPL/AGPL 等有传染性的开源代码
- **第三方库审慎选择**：只使用 MIT/Apache/BSD 等宽松许可的库，且每个依赖都记录许可证
- **协议兼容性设计**：实现 OpenAI 兼容协议是因为它是行业事实标准（公开 API 规范），而非复制任何项目的私有实现
- **MCP 协议遵循公开规范**：MCP 是 Anthropic 发布的开放协议，遵循其公开规范文档实现，不依赖任何私有 SDK

## Core Value

**让任何 AI Agent 都能通过统一网关安全、高效、可观测地运行和协作。** 如果其他所有功能都失败了，SSE 流式转发 + 多模型路由必须正常工作。

## Requirements

### Validated

(None yet — ship to validate)

### Active

#### L0：核心网关层
- [ ] 统一 API 入口，支持 OpenAI 兼容协议格式
- [ ] 多模型提供商路由（OpenAI、Anthropic、Google、国产模型、本地部署）
- [ ] 智能路由策略（延迟优先、成本优先、可用性优先、加权轮询）
- [ ] 自动 Failover（提供商故障自动切换）
- [ ] 负载均衡（基于连接数/延迟/权重的分发策略）
- [ ] Token 级别限流（per-user/per-key/per-IP，固定窗口+滑动窗口）
- [ ] 配额管理与预算控制
- [ ] 精确匹配缓存 + 语义缓存（相似 prompt 复用）
- [ ] API Key 安全管理（加密存储、轮转、权限分级）
- [ ] 请求/响应日志与审计

#### L1：Agent 运行时层
- [ ] SSE 长连接网关（维持数十万并发连接）
- [ ] 流式数据转发（LLM chunk → 用户，零拷贝转发）
- [ ] 背压控制（用户消费慢时自动降速）
- [ ] 连接中断检测 + 自动重连
- [ ] 请求路由（按 graphId/agentId 分发到不同 Agent 实例）
- [ ] MCP Server 实现（JSON-RPC 2.0 over HTTP/SSE）
- [ ] MCP Tool/Resource/Prompt 注册与管理
- [ ] MCP 多租户隔离
- [ ] MCP 协议升级适配（stdio ↔ HTTP ↔ SSE）

#### L2：Agent 基础设施层
- [ ] Tool 执行器框架（统一的 tool 调用接口）
- [ ] 容器沙箱调度（Docker/K8s API 启停隔离环境）
- [ ] 浏览器自动化（CDP/chromedp 操控浏览器）
- [ ] 代码沙箱执行（gVisor/firecracker 隔离）
- [ ] HTTP tool 并发调用与结果聚合
- [ ] State Store / Checkpoint 代理
- [ ] 批量聚合 checkpoint 写入
- [ ] 热点 state 缓存（LRU）
- [ ] 增量 diff 计算
- [ ] 分布式锁（Redlock）
- [ ] 多 Agent 通信总线
- [ ] 消息总线（NATS 或 in-memory channel+select）
- [ ] 广播、点对点、主题订阅
- [ ] GroupChat 支持

#### L3：治理与合规层
- [ ] 多租户管理（租户隔离、资源配额）
- [ ] RBAC 权限控制
- [ ] Guardrails（输入/输出内容审查）
- [ ] PII 过滤与数据防泄漏
- [ ] Prompt 注入防护
- [ ] 成本归因（按租户/团队/模型/应用）
- [ ] 可观测性 Dashboard
- [ ] OTel traces + metrics + logs 统一采集
- [ ] Prometheus 指标暴露（Phase 1）
- [ ] ClickHouse 深度分析（Phase 2）
- [ ] Grafana 可视化

#### SDK
- [ ] Java SDK（Spring Boot Starter 形式）
- [ ] Go SDK
- [ ] 纯 HTTP API（无需 SDK 也可接入）

### Out of Scope

- 模型训练/微调 —— 只做推理代理，不做训练
- 模型托管/推理引擎 —— 对接现有推理服务（vLLM、Ollama 等），不自建推理引擎
- 前端 UI 应用 —— 提供管理控制台 API，不开发前端应用（Grafana 做可观测性）
- 移动端 SDK —— 优先 Java + Go，移动端后续考虑
- 自研 LLM —— 只做网关和运行时，不做模型本身

## Context

### 技术背景
- 团队目前使用 Spring AI 框架做 AI 调用，缺乏 token 统计、埋点和可观测性
- 项目从 SSEGateway 演进而来，SSE 流式处理是核心基因
- 目标对标 Portkey（企业级 AI 控制平面）、Cloudflare AI Gateway（边缘推理层）、LiteLLM（100+ 模型统一代理）

### 市场参考
- **Portkey**：最功能丰富的企业级方案，每次请求记录 40+ 数据点，语义缓存降本 40-70%
- **Cloudflare AI Gateway**：边缘原生，330+ PoP，2026 年定位为"为 Agent 设计的推理层"
- **LiteLLM**：Python 生态，100+ 提供商覆盖，路由灵活但高并发下性能有瓶颈
- **Kong AI Gateway**：传统 API 网关扩展 AI 能力，适合已有 Kong 基础设施

### 竞品差异化机会
- Go 实现的高性能 SSE 网关（竞品多为 Python/Node）
- Agent 运行时原生支持（不只是 API 代理）
- MCP 协议深度集成
- 国产模型原生支持（通义千问、文心、讯飞、智谱、DeepSeek）

## Constraints

- **团队**: 1-2 人 —— 必须极度聚焦，每个阶段独立可用
- **语言**: Go —— 高并发场景的最佳选择
- **性能**: 支持数十万并发 SSE 连接、百万级日调用量
- **兼容性**: 必须兼容 OpenAI API 协议格式（行业事实标准）
- **部署**: 独立服务部署 + SDK 接入方式
- **渐进式**: 可观测性先 Prometheus，后加 ClickHouse
- **知识产权**: 不 fork/拷贝任何开源项目代码；第三方依赖仅限 MIT/Apache/BSD 许可
- **独立实现**: 核心功能全部自研，竞品仅作为功能和设计参考
- **纯 Go**: 不依赖 CGO，不引用依赖 CGO 的包（`CGO_ENABLED=0` 编译）。保证跨平台编译简单、部署无 C 编译器依赖

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Go 语言 | 高并发 SSE/流式场景天然优势，内存效率好，goroutine 轻量 | — Pending |
| 自研核心，参考大厂设计 | 深度掌控每个细节；避免版权/专利风险；不受开源项目路线绑架 | — Pending |
| 全量蓝图分期交付 | L0→L1→L2→L3 四层递进，每层独立可用 | — Pending |
| OpenAI 兼容协议为基准 | 行业事实标准，降低用户接入成本 | — Pending |
| MCP 作为核心能力 | 2025-2026 Agent 工具调用事实标准，Portkey/Cloudflare 均在支持 | — Pending |
| SDK 优先 Java + Go | 团队主力技术栈（Java）+ 网关自身语言（Go） | — Pending |
| 可观测性渐进式 | 先 OTel+Prometheus 跑起来，后期加 ClickHouse 做深度 token 分析 | — Pending |
| 独立服务 + SDK 部署 | 解耦网关与业务系统，支持多团队共享 | — Pending |

## Architecture Layers

```
┌─────────────────────────────────────────────────────────────┐
│                      SDK Layer (Java / Go)                    │
├─────────────────────────────────────────────────────────────┤
│  L3: Governance   │ Multi-tenant │ RBAC │ Guardrails │ Audit │
├─────────────────────────────────────────────────────────────┤
│  L2: Agent Infra  │ Tool Exec │ Sandbox │ State │ Comm Bus  │
├─────────────────────────────────────────────────────────────┤
│  L1: Agent Runtime│ SSE Gateway │ MCP Server │ Streaming    │
├─────────────────────────────────────────────────────────────┤
│  L0: Core Gateway │ Routing │ Fallback │ Cache │ Rate Limit │
├─────────────────────────────────────────────────────────────┤
│  Observability    │ OTel → Prometheus (+ ClickHouse later)  │
└─────────────────────────────────────────────────────────────┘
```

## Evolution

This document evolves at phase transitions and milestone boundaries.

**After each phase transition** (via `/gsd-transition`):
1. Requirements invalidated? → Move to Out of Scope with reason
2. Requirements validated? → Move to Validated with phase reference
3. New requirements emerged? → Add to Active
4. Decisions to log? → Add to Key Decisions
5. "What This Is" still accurate? → Update if drifted

**After each milestone** (via `/gsd:complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state (users, feedback, metrics)

---
*Last updated: 2026-06-30 after initialization*
