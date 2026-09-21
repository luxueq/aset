# ROADMAP: AIGateway

**Created:** 2026-06-30
**Project Mode:** mvp
**Granularity:** coarse
**Total Phases:** 8
**Total Requirements:** 56

---

## Phase Overview

| # | Phase | Goal | Requirements | Success Criteria |
|---|-------|------|--------------|------------------|
| 1 | Core Gateway | 2/6 | In Progress|  |
| 2 | SSE Agent Runtime | 流式转发 + 背压控制 + 重连 | SSE-01~05 (5) | 数十万并发 SSE 连接稳定运行 |
| 3 | MCP Server | MCP 协议实现 + Tool 注册 | MCP-01~06 (6) | LLM 可通过 MCP 协议调用注册的 Tool |
| 4 | Observability | 可观测性 + 成本追踪 | OBS-01~07 (7) | Grafana 可视化 token 用量/成本/延迟 |
| 5 | Tool Executor & Sandbox | 沙箱执行 + 浏览器自动化 | TOOL-01~06 (6) | Tool 在隔离沙箱中安全执行 |
| 6 | Agent Infrastructure | 状态管理 + 多 Agent 通信 | STATE-01~05, BUS-01~05 (10) | 多 Agent 可协作并维持状态 |
| 7 | Governance | 多租户 + 安全合规 | GOV-01~05 (5) | 可对外部客户开放使用 |
| 8 | SDK & Documentation | 客户端 SDK + API 文档 | SDK-01~04 (4) | Java/Go 开发者可快速接入 |

---

### Phase 1: Core Gateway
**Goal:** 建立统一代理层，开发者通过一个 API 端点即可调用任意 AI 模型提供商
**Mode:** mvp

**Requirements:** GW-01, GW-02, GW-03, GW-04, GW-05, GW-06, GW-07, GW-08, GW-09, GW-10, GW-11, GW-12, GW-13

**Success Criteria:**
1. 开发者可以通过 `POST /v1/chat/completions` 统一端点调用 OpenAI、Anthropic、Google Gemini、通义千问、文心一言等 5+ 提供商
2. 当目标提供商不可用时，系统自动在 < 1s 内 Failover 到备用提供商
3. 管理员可以创建 API Key 并设置 Token 级限流（per-key），超限请求返回 429
4. 精确匹配缓存命中时，响应延迟 < 10ms（不经过上游模型）
5. 语义缓存可配置相似度阈值，命中时返回缓存结果
6. 管理员可设置预算上限，接近上限时触发告警

**Build Order:**
1. 项目骨架搭建（Go module、目录结构、配置管理）
2. Provider 适配器框架 + OpenAI/Anthropic 适配
3. 路由引擎（策略模式：延迟/成本/可用性/轮询）
4. Failover 机制 + 负载均衡
5. API Key 管理 + 认证中间件
6. Token 限流（固定窗口 + 滑动窗口）
7. 精确匹配缓存（Redis/内存）
8. 语义缓存（embedding + 向量相似度）
9. 预算控制 + 告警
10. 国产模型适配（通义/文心/讯飞/智谱/DeepSeek）
11. 本地模型适配（vLLM/Ollama 兼容接口）

---

### Phase 2: SSE Agent Runtime
**Goal:** 支持 SSE 长连接的流式转发，Agent 运行时可以高效地将 LLM 流式输出转发给用户
**Mode:** mvp

**Requirements:** SSE-01, SSE-02, SSE-03, SSE-04, SSE-05

**Success Criteria:**
1. 系统可同时维持 10 万+ 并发 SSE 连接，内存增长可控
2. 流式转发延迟 < 5ms（从上游 chunk 到达到客户端接收）
3. 客户端消费慢时，背压机制生效，不导致内存溢出
4. 客户端断线后 < 3s 内自动重连，不丢失数据
5. 系统可以按 graphId/agentId 将请求路由到不同 Agent 实例

**Build Order:**
1. SSE 连接管理器（连接注册/注销/心跳/清理）
2. 流式转发引擎（io.Pipe + bufio.Scanner）
3. 背压控制器（有界 channel + select + 自适应降速）
4. 连接中断检测 + 自动重连（Last-Event-ID）
5. 按 graphId/agentId 路由分发

---

### Phase 3: MCP Server
**Goal:** 实现 MCP 协议服务端，LLM 可以通过标准协议发现和调用注册的 Tool
**Mode:** mvp

**Requirements:** MCP-01, MCP-02, MCP-03, MCP-04, MCP-05, MCP-06

**Success Criteria:**
1. MCP 兼容客户端（如 Claude Desktop、Cursor）可以连接到 AIGateway 的 MCP Server
2. 开发者可以注册 Tool（名称、描述、参数 schema），LLM 可以发现并调用
3. 开发者可以注册 Resource（数据源），LLM 可以读取
4. 开发者可以注册 Prompt 模板
5. 不同租户的 Tool/Resource/Prompt 完全隔离
6. 系统支持 stdio 和 Streamable HTTP 两种传输方式

**Build Order:**
1. JSON-RPC 2.0 消息处理框架
2. Streamable HTTP 传输实现
3. Tool 注册与调用（`tools/list`, `tools/call`）
4. Resource 注册与读取（`resources/list`, `resources/read`）
5. Prompt 注册与管理（`prompts/list`, `prompts/get`）
6. 多租户隔离（header/path 路由）
7. stdio 传输适配（本地开发场景）

---

### Phase 4: Observability
**Goal:** 全面可观测性——能看到每次 AI 调用的 token 用量、成本、延迟、链路
**Mode:** mvp

**Requirements:** OBS-01, OBS-02, OBS-03, OBS-04, OBS-05, OBS-06, OBS-07

**Success Criteria:**
1. 每次 API 调用自动生成 trace，包含完整请求链路
2. Grafana Dashboard 可实时查看：QPS、延迟分布、错误率、token 用量
3. 可按模型/提供商/租户/API Key 维度查看成本
4. Token 用量精确到 input/output/cached/reasoning 分类
5. 日志可通过 OTel 统一采集，支持结构化查询
6. Grafana Dashboard JSON 模板开箱即用

**Build Order:**
1. OTel SDK 集成（traces + metrics + logs 初始化）
2. 请求中间件注入（trace context propagation）
3. Token 用量采集（从 provider response 的 usage 字段提取）
4. Prometheus 指标暴露（`/metrics` 端点）
5. 成本计算引擎（model cost map + token 单价）
6. Grafana Dashboard 模板
7. 日志结构化 + OTel 导出

---

### Phase 5: Tool Executor & Sandbox
**Goal:** Tool 可以在隔离的沙箱环境中安全执行，支持容器、浏览器、代码沙箱
**Mode:** mvp

**Requirements:** TOOL-01, TOOL-02, TOOL-03, TOOL-04, TOOL-05, TOOL-06

**Success Criteria:**
1. MCP Tool 调用可以在容器沙箱中安全执行，不影响宿主机
2. 浏览器自动化（chromedp）可以完成网页操作（截图、表单填写、数据提取）
3. 代码沙箱可以执行 Python/JS/Go 代码，资源受限（CPU/内存/时间）
4. 所有沙箱有 TTL，超时自动销毁，无资源泄漏
5. 多个 HTTP Tool 可以并发调用并聚合结果

**Build Order:**
1. Tool 执行框架（注册、调度、超时、重试、结果处理）
2. HTTP Tool 并发调用 + 结果聚合
3. Docker 容器沙箱管理（创建/销毁/健康检查）
4. chromedp 浏览器自动化集成
5. Firecracker/gVisor 代码沙箱集成
6. 沙箱生命周期管理（TTL、资源限额、清理）

---

### Phase 6: Agent Infrastructure
**Goal:** 多 Agent 可以维持状态并相互通信协作
**Mode:** mvp

**Requirements:** STATE-01~05, BUS-01~05

**Success Criteria:**
1. Agent Checkpoint 可以高吞吐写入（10K+ writes/s）
2. 热点 State 通过 LRU 缓存，读取延迟 < 1ms
3. 多个 Agent 可以通过消息总线互相发送消息
4. GroupChat 支持 3+ Agent 协作对话
5. 关键消息通过 JetStream 持久化，不丢失

**Build Order:**
1. State Store 基础（checkpoint 读写接口）
2. 批量聚合写入 + LRU 缓存
3. 增量 diff 计算
4. Redis Redlock 分布式锁
5. NATS 消息总线集成
6. In-memory 通信总线（channel + select）
7. Pub/Sub + Request-Reply + 主题订阅
8. GroupChat 实现
9. JetStream 持久化 + at-least-once 投递

---

### Phase 7: Governance
**Goal:** 系统可以对内外部客户安全开放，支持多租户、权限控制、内容审查
**Mode:** mvp

**Requirements:** GOV-01~05

**Success Criteria:**
1. 不同租户的数据、配置、用量完全隔离
2. RBAC 权限控制有效（管理员/开发者/只读）
3. 输入/输出 Guardrails 可拦截 PII、毒性内容、Prompt 注入
4. 审计日志完整记录每次操作
5. ClickHouse 可查询 token 成本归因、用量趋势

**Build Order:**
1. 多租户框架（tenant context propagation）
2. RBAC 权限控制中间件
3. Guardrails 引擎（输入审查 + 输出审查）
4. 审计日志（异步写入，不阻塞请求）
5. ClickHouse 集成（表设计 + 数据管道 + 查询接口）

---

### Phase 8: SDK & Documentation
**Goal:** Java 和 Go 开发者可以通过 SDK 快速接入 AIGateway
**Mode:** mvp

**Requirements:** SDK-01~04

**Success Criteria:**
1. Java 开发者添加 Spring Boot Starter 依赖后即可调用所有网关 API
2. Go 开发者 `go get` 后即可调用所有网关 API
3. OpenAPI 3.0 文档完整，覆盖所有端点
4. 无 SDK 的开发者可以通过 HTTP API 文档直接接入

**Build Order:**
1. OpenAPI 3.0 规范文件生成
2. Go SDK（与网关同 repo，确保 API 一致性）
3. Java SDK（Spring Boot Starter，自动配置）
4. API 文档站（Swagger UI / Redoc）

---

## Phase Dependencies

```
Phase 1 (Core Gateway)
  ↓
Phase 2 (SSE Runtime) ←── 需要代理层基础
  ↓
Phase 3 (MCP Server) ←── 需要 SSE + 代理层
  ↓
Phase 4 (Observability) ←── 需要流量数据
  ↓
Phase 5 (Tool + Sandbox) ←── 需要 MCP Tool 注册
  ↓
Phase 6 (Agent Infra) ←── 需要 Tool 执行环境
  ↓
Phase 7 (Governance) ←── 需要全栈稳定
  ↓
Phase 8 (SDK) ←── 需要 API 稳定
```

## Risk Summary

| Risk | Impact | Mitigation |
|------|--------|------------|
| 1-2 人团队做全量平台 | 高 | 严格分期，每阶段独立可用 |
| SSE 长连接 goroutine 泄漏 | 高 | Go 1.26 leak profiling + 严格 ctx 管理 |
| MCP 2026-07-28 规范变更 | 中 | 按无状态设计，不依赖 session |
| 多提供商协议差异 | 中 | Provider 适配器隔离差异 |
| 沙箱资源泄漏 | 中 | 强制 TTL + cleanup |

---
*Roadmap created: 2026-06-30*
*Last updated: 2026-09-21 after completing 01-02 (provider adapters)*
