# Features Research — AIGateway

## 功能分层

### Table Stakes（必须有，否则用户不会用）

#### 统一代理
- 一个 API 入口对接 100+ 模型提供商
- OpenAI 兼容协议格式（行业事实标准）
- 请求/响应格式标准化，屏蔽提供商差异
- 支持 Chat Completions、Embeddings、Images 等端点

#### 智能路由
- 按延迟/成本/可用性动态路由
- 自动 Failover（提供商故障自动切换）
- 负载均衡（连接数/延迟/权重）
- 模型版本管理（一个逻辑模型名映射到多个物理模型）

#### 成本控制
- Token 用量统计（per-user/per-key/per-model/per-team）
- 成本追踪（每个模型的 token 单价 × 用量）
- 配额管理与预算控制
- 预算告警

#### 限流与安全
- Token 级别限流（固定窗口 + 滑动窗口）
- API Key 管理（创建/轮转/撤销/权限分级）
- 基本请求日志与审计

#### 可观测性
- 请求级 tracing（每次调用记录 40+ 数据点）
- 延迟/错误率/token 用量监控
- Dashboard 可视化

### Differentiators（差异化竞争优势）

#### Agent 运行时
- SSE 长连接网关（数十万并发）
- 流式数据转发（零拷贝）
- 背压控制（自适应降速）
- 连接中断检测 + 自动重连
- 按 graphId/agentId 路由到不同 Agent 实例

#### MCP 原生支持
- MCP Server 实现（Tool/Resource/Prompt 注册）
- 多租户隔离
- 协议升级适配（stdio ↔ Streamable HTTP）
- MCP 流量代理与安全扫描

#### 沙箱执行
- 容器化沙箱（Docker/K8s）
- 浏览器自动化（CDP）
- 代码沙箱（Firecracker/gVisor）
- Tool 并发调用与结果聚合

#### 多 Agent 协作
- 消息总线（NATS / 进程内）
- GroupChat / Pub-Sub / Request-Reply
- Agent 发现与状态管理
- 工作流编排

### Anti-Features（故意不做的）

| 不做 | 理由 |
|------|------|
| 模型训练/微调 | 只做推理代理，训练是独立的基础设施问题 |
| 模型托管/推理引擎 | 对接现有推理服务，不自建 vLLM/TGI |
| 前端 UI 应用 | 用 Grafana 做可观测性，不自建前端 |
| 自研 LLM | 只做网关和运行时 |
| 特定框架绑定 | 保持协议层中立，不绑定 LangChain/CrewAI |

## 竞品功能对标

| 功能 | Portkey | Cloudflare AI GW | LiteLLM | AIGateway 规划 |
|------|---------|------------------|---------|---------------|
| 统一代理 | ✅ | ✅ | ✅ | ✅ |
| 智能路由 | ✅ | ✅ | ✅ | ✅ |
| 自动 Failover | ✅ | ✅ | ✅ | ✅ |
| Token 限流 | ✅ | ✅ | ✅ | ✅ |
| 语义缓存 | ✅ | ✅ | ❌ | ✅（Phase 2） |
| 成本追踪 | ✅（40+数据点）| ✅ | ✅ | ✅ |
| Guardrails | ✅ | ✅ | ❌ | ✅ |
| Prompt 管理 | ✅ | ❌ | ❌ | Phase 3 |
| MCP 支持 | ✅ | ✅ | ❌ | ✅（核心能力）|
| Agent 运行时 | ❌ | ✅（推理层）| ❌ | ✅（核心差异化）|
| 沙箱执行 | ❌ | ❌ | ❌ | ✅（差异化）|
| 多 Agent 通信 | ❌ | ❌ | ❌ | ✅（差异化）|
| Go 实现 | ❌（Node.js）| ✅（Workers）| ❌（Python）| ✅ |
| 国产模型 | ❌ | ❌ | 部分 | ✅ |

## 复杂度评估

| 模块 | 复杂度 | 预估工作量 | 依赖 |
|------|--------|----------|------|
| 统一代理 + 路由 | 中 | 2-3 周 | 无 |
| 限流 + 缓存 | 中 | 1-2 周 | 代理层 |
| Token 统计 + 成本 | 中 | 1-2 周 | 代理层 |
| SSE 流式网关 | 高 | 3-4 周 | 代理层 |
| MCP Server | 中高 | 2-3 周 | SSE 网关 |
| 可观测性 | 中 | 2 周 | 代理层 |
| 沙箱执行 | 很高 | 4-6 周 | 代理层 + MCP |
| 多 Agent 通信 | 中高 | 3-4 周 | SSE 网关 |
| SDK (Java + Go) | 中 | 2-3 周 | API 稳定后 |
| 安全合规 | 高 | 3-4 周 | 全栈 |

---
*Confidence: High — 基于竞品功能对标和技术可行性分析*
*Last updated: 2026-06-30*
