# Stack Research — AIGateway

## 1. 核心 HTTP/SSE 层

### 推荐方案：net/http 标准库 + 自建中间件

| 组件 | 推荐方案 | 理由 |
|------|---------|------|
| HTTP 框架 | `net/http` 标准库 | SSE/流式场景需要底层控制，`httputil.ReverseProxy` 需要设 `FlushInterval=-1` 才能即时刷新 |
| SSE 库 | 自建 handler（`http.Flusher` 接口）| 参考 `tmaxmax/go-sse`（spec-compliant）和 `mroth/sseserver`（high-perf） |
| JSON 序列化 | `bytedance/sonic` v2 | 比标准库快 3-5x，高吞吐场景必要 |
| 路由 | `go-chi/chi/v5` | 轻量、兼容 `net/http`、中间件链 |

### SSE 流式转发核心模式

```go
// io.Pipe 提供天然背压：Write 阻塞直到 Read 消费
pr, pw := io.Pipe()
go func() {
    defer pw.Close()
    scanner := bufio.NewScanner(upstreamResp.Body)
    for scanner.Scan() {
        // 有界 channel 提供背压——客户端消费慢时阻塞
        select {
        case ch <- scanner.Bytes():
        case <-ctx.Done():
            return
        }
    }
}()
// 不设 Content-Length，使用 Transfer-Encoding: chunked
w.Header().Set("Content-Type", "text/event-stream")
w.Header().Set("Cache-Control", "no-cache")
flusher := w.(http.Flusher)
io.Copy(w, pr)
flusher.Flush()
```

**关键坑点：**
- `httputil.ReverseProxy` 默认不即时 flush → 必须设 `FlushInterval: -1`
- 不设 `Content-Length`，否则禁用 chunked transfer encoding
- goroutine 生命周期：`defer pw.Close()` + 监听 `ctx.Done()`
- Go 1.26 新增 goroutine leak profiling，可检测阻塞在 channel/mutex 上的不可达 goroutine

### 背压策略

| 策略 | 实现方式 | 适用场景 |
|------|---------|---------|
| 有界 channel | `make(chan []byte, N)` + `select` | 单连接背压 |
| io.Pipe | 同步管道，Write 阻塞 | 上下游流式对接 |
| Token bucket | `uber-go/ratelimit` | 全局限流 |
| Load shedding | channel 满时 drop + 通知 | 过载保护 |
| 自适应降速 | 监控 channel 深度，动态调整读取速率 | 大模型慢响应场景 |

**参考来源：** [Streaming LLM Tokens to 10K Users](https://mvpfactory.io/blog/streaming-llm-tokens-to-10k-concurrent-users-backpressure-coroutine-channels/), [Backpressure Strategies for Go](https://blog.stackademic.com/backpressure-strategies-for-high-throughput-go-services-a2335b0590e3), [Go ReverseProxy Streaming Issue](https://github.com/golang/go/issues/27816)

---

## 2. 可观测性层

### Phase 1：OTel + Prometheus + Grafana

| 组件 | 推荐方案 | Go SDK |
|------|---------|--------|
| 指标采集 | OpenTelemetry → Prometheus | `go.opentelemetry.io/otel` |
| 链路追踪 | OTel → Tempo/Jaeger | `go.opentelemetry.io/otel/sdk/trace` |
| 日志 | `go.uber.org/zap` 或 `log/slog`（Go 1.21+） | 结构化 JSON 日志 |
| 可视化 | Grafana | — |

### Phase 2：+ ClickHouse 深度分析

| 组件 | 推荐方案 | 理由 |
|------|---------|------|
| 分析存储 | `ClickHouse/clickhouse-go/v2` | 列存天然适合 token 成本聚合、用量趋势分析 |
| 写入优化 | 批量聚合写入（减少存储压力） | 百万级日调用场景必要 |

**参考来源：** [OTel Go SDK](https://pkg.go.dev/go.opentelemetry.io/otel), [ClickHouse Go Driver](https://github.com/ClickHouse/clickhouse-go)

---

## 3. MCP 层

### 推荐方案：官方 Go SDK

| 组件 | 推荐方案 | 状态 |
|------|---------|------|
| MCP SDK | `github.com/modelcontextprotocol/go-sdk/mcp` | 2026-05 更新，官方维护 |
| 传输协议 | Streamable HTTP（SSE 已废弃） | 2025-03-26 规范 |
| 消息格式 | JSON-RPC 2.0，UTF-8 编码 | — |

### MCP 2026 关键变化

1. **无状态协议**（2026-07-28 RC）——移除 `Mcp-Session-Id`，任何请求可打到任何服务器
2. **Streamable HTTP 是标准**——SSE 已废弃，统一端点 POST
3. **企业安全模型**——OAuth 2.0 + TLS + 沙箱 + 用户同意流
4. **三大原语**：Tools（函数调用）、Resources（只读数据）、Prompts（模板消息）

### 实现要点

- Server 能力注册：`server.AddTool()`, `server.AddResource()`, `server.AddPrompt()`
- 传输适配层：stdio ↔ Streamable HTTP（协议升级）
- 多租户：通过 HTTP header（`X-Tenant-ID`）或 URL path 隔离
- 连接池：HTTP client 连接池管理

**参考来源：** [MCP Specification](https://modelcontextprotocol.io/specification/draft), [MCP Go SDK](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp), [MCP 2026-07-28 RC](https://blog.modelcontextprotocol.io/posts/2026-07-28-release-candidate/), [SSE vs Streamable HTTP](https://brightdata.com/blog/ai/sse-vs-streamable-http)

---

## 4. 消息总线层

### 推荐方案：NATS

| 组件 | 推荐方案 | 理由 |
|------|---------|------|
| 消息总线 | NATS (`nats.go`) | Go 原生、高性能 pub/sub、JetStream 持久化 |
| 进程内总线 | `channel + select` | 单实例场景零延迟 |

### NATS Subject 设计规范（多 Agent）

```
agents.{verb}.{agent}.{owner}.{name}    # 动词优先的命名空间
agents.{name}.events                     # Agent 事件广播
agents.{name}.tasks                      # 任务分配（JetStream）
group.{id}.chat                          # GroupChat
workflow.{pipeline}.{stage}              # 工作流管道
kv.agents.{name}.status                  # Agent 状态发现（KV Store）
```

### 通信模式映射

| 模式 | NATS 原语 | Subject 约定 |
|------|----------|-------------|
| Agent 广播/事件 | Pub/Sub | `agents.{name}.events` |
| Agent 工具调用 | Request-Reply | `agents.{verb}.{agent}.{owner}.{name}` |
| Agent 任务队列 | JetStream + Queue Groups | `agents.{name}.tasks` |
| GroupChat | Wildcard subscriptions + Pub/Sub | `group.{id}.chat` with `group.>` |
| 持久化工作流 | JetStream streams + consumers | `workflow.{pipeline}.{stage}` |
| Agent 发现/状态 | KV Store | `kv.agents.{name}.status` |

**参考来源：** [NATS Native Protocol for AI Agents](https://nats.io/blog/nats-native-protocol-for-ai-agents/), [NATS Subject Design for Multi-Agent](https://agent.ceo/blog/nats-subject-design-ai-agents-cyborgenic), [Agentic AI with Message Queues](https://callsphere.ai/blog/agentic-ai-message-queues-nats-kafka-rabbitmq-patterns), [NVIDIA Built AI Factory Event Bus on NATS](https://www.synadia.com/blog/nvidia-built-its-ai-factory-event-bus-on-nats)

---

## 5. 沙箱执行层

### 隔离技术对比

| 技术 | 隔离级别 | 冷启动 | 内存开销 | 适用场景 |
|------|---------|--------|---------|---------|
| **Firecracker microVM** | 硬件级（独立内核） | ~150ms | ~1GB/sandbox | 最强隔离，代码执行 |
| **gVisor** | 用户空间内核（seccomp） | ~0ms（容器上叠加） | 低 | 容器兼容需求 |
| **Docker container** | 进程级（namespace+cgroup） | ~500ms | 中 | 工具执行、浏览器 |
| **WASM** | 沙箱级 | ~1ms | 极低 | 轻量级计算 |

### 2026 共识

- **默认选 Firecracker microVM**——硬件级隔离，最强安全性
- **容器兼容场景用 gVisor**——在现有 Docker 运行时叠加用户空间内核
- **纵深防御**——microVM + gVisor + 网络隔离 + 资源限制 组合使用
- **E2B** 是最流行的 sandbox-as-a-service，默认 ~1GB RAM/Firecracker sandbox

### Go 集成方式

| 功能 | Go 库 | 说明 |
|------|------|------|
| Docker 调度 | `docker/go-dockerclient` 或 `docker/client` | 容器生命周期管理 |
| K8s 调度 | `kubernetes/client-go` | Pod 创建/销毁 |
| 浏览器自动化 | `chromedp` | Go 原生 CDP 客户端 |
| Firecracker | `firecracker-microvm/fcctl` | microVM 管理 |

**参考来源：** [How to Sandbox AI Agents](https://northflank.com/blog/how-to-sandbox-ai-agents), [Firecracker vs gVisor 2026](https://www.alekseialeinikov.com/en/blog/topics/devops/microvms-firecracker-vs-gvisor-secure-workloads-2026), [AI Agent Sandbox Guide 2026](https://noqta.tn/en/blog/ai-agent-sandbox-secure-code-execution-2026), [Awesome Sandbox](https://github.com/restyler/awesome-sandbox)

---

## 6. NOT to use

| 不推荐 | 理由 |
|-------|------|
| Gin/Echo | 框架封装太重，SSE 流式需要底层控制 |
| gRPC | AI 模型 API 基本都是 HTTP/REST |
| RabbitMQ | 过重，NATS 更轻量且 Go 原生 |
| Elasticsearch | ClickHouse 在 token 分析场景性价比更高 |
| WebSocket | AI 模型 API 都用 SSE，不需要 WebSocket |

---
*Confidence: High — 基于 2026 年 Go 生态、MCP 官方规范、NATS 官方文档和实际案例*
*Last updated: 2026-06-30*
