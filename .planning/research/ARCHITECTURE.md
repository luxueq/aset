# Architecture Research — AIGateway

## 整体架构

```
                    ┌─────────────────────────────────────────┐
                    │              SDK Layer                    │
                    │    Java SDK    │    Go SDK    │  HTTP    │
                    └────────┬───────┴───────┬──────┴───┬─────┘
                             │               │          │
                    ┌────────▼───────────────▼──────────▼─────┐
                    │           API Gateway (L0)               │
                    │  Auth │ Rate Limit │ Router │ Cache      │
                    └────────┬───────────────┬──────────┬─────┘
                             │               │          │
              ┌──────────────▼──┐  ┌────────▼────┐  ┌──▼──────────┐
              │  SSE Runtime    │  │  MCP Server  │  │  Provider   │
              │  (L1)           │  │  (L1)        │  │  Adapters   │
              │  Stream/Fan-out │  │  JSON-RPC    │  │  OpenAI/    │
              │  Backpressure   │  │  Tool/Res/   │  │  Anthropic/ │
              │  Reconnect      │  │  Prompt      │  │  Gemini/... │
              └────────┬────────┘  └──────┬───────┘  └─────────────┘
                       │                  │
              ┌────────▼──────────────────▼────────────────────┐
              │              Agent Infrastructure (L2)           │
              │  ┌──────────┐ ┌──────────┐ ┌─────────────────┐ │
              │  │ Tool     │ │ State    │ │ Communication   │ │
              │  │ Executor │ │ Store    │ │ Bus             │ │
              │  │ Sandbox  │ │ Checkpt  │ │ NATS/In-memory  │ │
              │  └──────────┘ └──────────┘ └─────────────────┘ │
              └─────────────────────┬──────────────────────────┘
                                    │
              ┌─────────────────────▼──────────────────────────┐
              │              Governance (L3)                     │
              │  Multi-tenant │ RBAC │ Guardrails │ Audit       │
              └─────────────────────────────────────────────────┘
```

## 组件边界

### L0: Core Gateway

**职责：** 请求接入、认证、限流、路由、转发、日志

**组件：**
- `proxy` — HTTP 反向代理，流式转发
- `router` — 路由策略引擎（延迟/成本/可用性/权重）
- `ratelimit` — Token 级限流（固定窗口 + 滑动窗口）
- `cache` — 精确匹配缓存 + 语义缓存
- `auth` — API Key 验证、JWT、OAuth
- `middleware` — 中间件链（logging, metrics, tracing）
- `provider` — 模型提供商适配器（统一接口 → 各提供商协议）

**边界：** 只处理同步 HTTP 请求/响应，不管理连接状态

### L1: Agent Runtime

**职责：** SSE 长连接管理、流式转发、MCP 协议

**组件：**
- `sse` — SSE 连接管理器（连接注册/注销、心跳、重连）
- `stream` — 流式转发引擎（LLM chunk → 用户，零拷贝）
- `backpressure` — 背压控制器（channel 深度监控、自适应降速）
- `mcp` — MCP Server（Tool/Resource/Prompt 注册、JSON-RPC 处理）
- `mcp/transport` — MCP 传输适配（stdio ↔ Streamable HTTP）

**边界：** 管理长连接状态，但不执行 Tool 或管理 Agent 状态

### L2: Agent Infrastructure

**职责：** Tool 执行、状态管理、Agent 间通信

**组件：**
- `tool` — Tool 执行框架（注册、调度、超时、重试）
- `sandbox` — 沙箱管理器（Docker/K8s/Firecracker 生命周期）
- `sandbox/browser` — 浏览器自动化（chromedp）
- `sandbox/code` — 代码沙箱（隔离执行）
- `state` — State Store（checkpoint 写入/读取、LRU 缓存）
- `state/lock` — 分布式锁（Redlock）
- `bus` — 通信总线（NATS client / in-memory channel+select）
- `bus/groupchat` — GroupChat 实现（广播/订阅/路由）

**边界：** 提供 Agent 运行的基础设施，但不实现 Agent 逻辑本身

### L3: Governance

**职责：** 多租户、权限、安全、审计

**组件：**
- `tenant` — 多租户管理（隔离、配额）
- `rbac` — 权限控制
- `guardrail` — 输入/输出审查（PII、毒性、注入）
- `audit` — 审计日志
- `billing` — 成本归因与计费

**边界：** 横切关注点，通过中间件注入 L0-L2

## 数据流

### 普通请求（Chat Completion）

```
Client → SDK → Gateway(L0: Auth→RateLimit→Route→Cache)
  → Provider Adapter → Upstream LLM
  → Response → Gateway(L0: Log→Metrics) → Client
```

### 流式请求（SSE Streaming）

```
Client → SDK → Gateway(L0: Auth→RateLimit→Route)
  → SSE Runtime(L1: Register→Stream→Backpressure)
  → Provider Adapter → Upstream LLM
  → Chunks → SSE Runtime → Stream to Client
  → (同时) Metrics/Log 异步记录
```

### MCP Tool 调用

```
LLM → MCP Client → AIGateway MCP Server(L1)
  → Tool Registry → Tool Executor(L2)
  → Sandbox(HTTP/Container/Browser/Code)
  → Result → MCP Server → LLM
```

### Agent 间通信

```
Agent A → Bus(L2: Publish) → NATS/In-memory
  → Bus(L2: Subscribe) → Agent B
  → (可选) JetStream 持久化用于审计/回放
```

## 构建顺序（依赖关系）

```
Phase 1: L0 Core Gateway (proxy + router + ratelimit + auth)
    ↓ 必须有基础代理能力
Phase 2: L1 SSE Runtime (sse + stream + backpressure)
    ↓ SSE 是 Agent 运行时的基础
Phase 3: L1 MCP Server (mcp + transport + tool registry)
    ↓ MCP 需要 SSE + 基础代理
Phase 4: L0 Observability (otel + prometheus + grafana)
    ↓ 有了流量才有指标
Phase 5: L2 Tool Executor + Sandbox
    ↓ MCP Tool 注册后需要执行环境
Phase 6: L2 State Store + Communication Bus
    ↓ 多 Agent 场景需要状态和通信
Phase 7: L3 Governance (tenant + rbac + guardrails)
    ↓ 面向外部客户时必须有
Phase 8: L0 Cache + L2 ClickHouse
    ↓ 优化和深度分析
Phase 9: SDK (Java + Go)
    ↓ API 稳定后封装 SDK
```

## 关键架构决策

1. **Proxy 模式而非 SDK 模式** — 网关作为透明代理，不改变请求/响应语义
2. **中间件链** — 所有横切关注点（日志、限流、认证）通过中间件实现
3. **Provider 适配器模式** — 统一内部接口，适配各提供商协议差异
4. **SSE 连接与应用分离** — SSE 管理器只管连接，流数据由 stream engine 处理
5. **NATS 作为默认外部消息总线** — 单实例时用 in-memory，多实例时用 NATS
6. **Checkpoint 批量写入** — 内存聚合后批量落盘，减少存储压力

---
*Confidence: High — 基于竞品架构分析和 Go 生态最佳实践*
*Last updated: 2026-06-30*
