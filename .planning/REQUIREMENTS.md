# Requirements: AIGateway

**Defined:** 2026-06-30
**Core Value:** 让任何 AI Agent 都能通过统一网关安全、高效、可观测地运行和协作

## v1 Requirements

### GW - 核心网关

- [x] **GW-01**: 开发者可以通过统一的 OpenAI 兼容 API 格式调用任意已配置的模型提供商
- [ ] **GW-02**: 系统支持动态路由到不同模型提供商（按延迟、成本、可用性、加权轮询策略）
- [ ] **GW-03**: 当目标模型提供商不可用或返回错误时，系统自动 Failover 到备用提供商
- [ ] **GW-04**: 系统支持基于连接数/延迟/权重的负载均衡策略
- [ ] **GW-05**: 管理员可以创建、轮转、撤销 API Key，并设置 Key 的权限级别
- [ ] **GW-06**: 系统支持 Token 级别限流（per-user/per-key/per-IP），支持固定窗口和滑动窗口
- [ ] **GW-07**: 系统支持精确匹配缓存，相同请求直接返回缓存结果
- [ ] **GW-08**: 系统支持语义缓存，相似 prompt 复用响应，降低重复调用成本
- [ ] **GW-09**: 管理员可以为每个租户/API Key 设置配额和预算上限
- [ ] **GW-10**: 系统在预算接近上限时自动触发告警通知
- [x] **GW-11**: 系统支持 OpenAI、Anthropic、Google Gemini 等国际主流模型提供商
- [ ] **GW-12**: 系统支持通义千问、文心一言、讯飞星火、智谱 GLM、DeepSeek 等国产模型
- [ ] **GW-13**: 系统支持对接本地/私有化部署的模型（vLLM、Ollama、TGI 等 OpenAI 兼容接口）

### SSE - Agent 运行时

- [ ] **SSE-01**: 系统可以同时维持数十万并发 SSE 长连接
- [ ] **SSE-02**: 系统支持零拷贝流式数据转发（LLM chunk → 用户）
- [ ] **SSE-03**: 系统支持背压控制，当客户端消费慢时自动降速，防止内存溢出
- [ ] **SSE-04**: 系统支持连接中断检测，客户端断线后自动重连
- [ ] **SSE-05**: 系统支持按 graphId/agentId 将请求路由到不同 Agent 实例

### MCP - MCP Server

- [ ] **MCP-01**: 系统实现 MCP Server，支持 JSON-RPC 2.0 over Streamable HTTP
- [ ] **MCP-02**: 开发者可以通过 MCP Server 注册 Tool，供 LLM 调用
- [ ] **MCP-03**: 开发者可以通过 MCP Server 注册 Resource（只读数据源）
- [ ] **MCP-04**: 开发者可以通过 MCP Server 注册 Prompt 模板
- [ ] **MCP-05**: 系统支持 MCP 多租户隔离，不同租户的 Tool/Resource/Prompt 互不可见
- [ ] **MCP-06**: 系统支持 MCP 协议升级适配（stdio ↔ Streamable HTTP）

### TOOL - Tool 执行与沙箱

- [ ] **TOOL-01**: 系统提供统一的 Tool 执行框架，支持注册、调度、超时、重试
- [ ] **TOOL-02**: 系统支持并发调用多个 HTTP Tool 并聚合结果
- [ ] **TOOL-03**: 系统支持通过 Docker/K8s API 启动和销毁容器沙箱
- [ ] **TOOL-04**: 系统支持通过 CDP（chromedp）进行浏览器自动化操作
- [ ] **TOOL-05**: 系统支持通过 Firecracker/gVisor 进行代码沙箱隔离执行
- [ ] **TOOL-06**: 系统支持沙箱生命周期管理（创建、健康检查、超时销毁、资源限额）

### STATE - 状态管理

- [ ] **STATE-01**: 系统支持 Agent Checkpoint 的高吞吐写入和低延迟读取
- [ ] **STATE-02**: 系统支持批量聚合 checkpoint 写入，减少存储压力
- [ ] **STATE-03**: 系统支持 LRU 缓存热点 State，加速读取
- [ ] **STATE-04**: 系统支持增量 diff 计算，减少数据传输量
- [ ] **STATE-05**: 系统支持基于 Redis Redlock 的分布式锁

### BUS - 多 Agent 通信

- [ ] **BUS-01**: 系统实现基于 NATS 的消息总线，支持 Agent 间消息传递
- [ ] **BUS-02**: 系统支持单实例场景下的 in-memory 通信总线（channel + select）
- [ ] **BUS-03**: 系统支持广播、点对点、主题订阅三种通信模式
- [ ] **BUS-04**: 系统支持 GroupChat（多 Agent 群组协作对话）
- [ ] **BUS-05**: 系统支持通过 JetStream 实现消息持久化和 at-least-once 投递

### OBS - 可观测性

- [ ] **OBS-01**: 系统通过 OpenTelemetry 统一采集 traces、metrics、logs
- [ ] **OBS-02**: 系统暴露 Prometheus 格式的指标端点
- [ ] **OBS-03**: 系统支持请求级 tracing，每次调用记录完整的请求链路
- [ ] **OBS-04**: 系统记录每个请求的 token 用量（input/output/cached/reasoning tokens）
- [ ] **OBS-05**: 系统记录每个请求的延迟（首 token 延迟、总延迟）、错误率
- [ ] **OBS-06**: 系统支持按模型/提供商/租户/API Key 维度的成本追踪
- [ ] **OBS-07**: 系统支持 Grafana Dashboard 可视化（提供 Dashboard JSON 模板）

### GOV - 治理与合规

- [ ] **GOV-01**: 系统支持多租户管理，租户间数据和配置完全隔离
- [ ] **GOV-02**: 系统支持 RBAC 权限控制（管理员/开发者/只读角色）
- [ ] **GOV-03**: 系统支持输入/输出 Guardrails（PII 过滤、毒性检测、Prompt 注入防护）
- [ ] **GOV-04**: 系统记录完整的审计日志（谁在什么时间调用了什么模型、传了什么参数）
- [ ] **GOV-05**: 系统支持 ClickHouse 深度分析（token 成本归因、用量趋势、异常检测）

### SDK - 客户端 SDK

- [ ] **SDK-01**: 系统提供 Java SDK（Spring Boot Starter 形式），封装所有网关 API
- [ ] **SDK-02**: 系统提供 Go SDK，封装所有网关 API
- [ ] **SDK-03**: 系统提供完整的 HTTP API 文档（OpenAPI 3.0 规范）
- [ ] **SDK-04**: 客户端无需 SDK 也能通过纯 HTTP API 接入

## v2 Requirements

### Prompt 管理
- **PROMPT-01**: 支持 Prompt 版本管理和 A/B 测试
- **PROMPT-02**: 支持 Prompt 模板库
- **PROMPT-03**: 支持 Prompt 评估和比较

### 高级路由
- **ROUTE-01**: 支持基于内容分类的智能路由（简单问题→小模型，复杂问题→大模型）
- **ROUTE-02**: 支持基于成本的自动降级（预算不足时切换到更便宜的模型）

### Agent 编排
- **ORCH-01**: 支持 DAG 形式的多 Agent 工作流编排
- **ORCH-02**: 支持 Supervisor 模式（主 Agent 调度子 Agent）
- **ORCH-03**: 支持人机协作（Human-in-the-loop）

### 模型评估
- **EVAL-01**: 支持模型输出质量自动评估
- **EVAL-02**: 支持自定义评估指标
- **EVAL-03**: 支持评估结果可视化

## Out of Scope

| Feature | Reason |
|---------|--------|
| 模型训练/微调 | 只做推理代理，训练是独立的基础设施问题 |
| 模型托管/推理引擎 | 对接现有推理服务（vLLM、Ollama 等），不自建推理引擎 |
| 前端 UI 应用 | 用 Grafana 做可观测性，不自建管理前端 |
| 自研 LLM | 只做网关和运行时，不做模型本身 |
| 特定框架绑定 | 保持协议层中立，不绑定 LangChain/CrewAI 等 |
| 移动端 SDK | 优先 Java + Go，移动端后续考虑 |
| WebSocket 支持 | AI 模型 API 都用 SSE，不需要 WebSocket |
| gRPC 支持 | AI 模型 API 基本都是 HTTP/REST |

## Traceability

| Requirement | Phase | Status |
|-------------|-------|--------|
| GW-01 ~ GW-13 | Phase 1 | In Progress (GW-01, GW-11 done) |
| SSE-01 ~ SSE-05 | Phase 2 | Pending |
| MCP-01 ~ MCP-06 | Phase 3 | Pending |
| OBS-01 ~ OBS-07 | Phase 4 | Pending |
| TOOL-01 ~ TOOL-06 | Phase 5 | Pending |
| STATE-01 ~ STATE-05 | Phase 6 | Pending |
| BUS-01 ~ BUS-05 | Phase 6 | Pending |
| GOV-01 ~ GOV-05 | Phase 7 | Pending |
| SDK-01 ~ SDK-04 | Phase 8 | Pending |

**Coverage:**
- v1 requirements: 56 total
- Mapped to phases: 56
- Unmapped: 0 ✓

---
*Requirements defined: 2026-06-30*
*Last updated: 2026-09-21 after 01-02 (GW-11 verified — provider adapters live)*
