# Phase 1: Core Gateway - Research

**Researched:** 2026-06-30
**Phase Goal:** 建立统一代理层，开发者通过一个 API 端点调用任意 AI 模型提供商

---

## 1. Reverse Proxy & Streaming

### 核心问题：httputil.ReverseProxy 与流式

Go 标准库 `httputil.ReverseProxy` 在代理 SSE 流式响应（LLM API）时有已知问题（[Go Issue #27816](https://github.com/golang/go/issues/27816)）：内部缓冲导致 token 批量到达而非逐 chunk 转发。

**解决方案：**
```go
proxy := httputil.NewSingleHostReverseProxy(target)
proxy.FlushInterval = -1 // 立即 flush
```

Go 1.20+ 对 `text/event-stream` 自动应用 `FlushInterval = -1`，但 charset 参数的边界情况需要额外处理（[#47359](https://github.com/golang/go/issues/47359)）。

### io.Pipe 流式转换模式

当需要**转换**响应体（如格式归一化）时，`ModifyResponse` 不够，需要用 `io.Pipe`：

```go
pr, pw := io.Pipe()
go func() {
    defer pw.Close()
    scanner := bufio.NewScanner(upstreamResp.Body)
    for scanner.Scan() {
        transformed := adaptSSEEvent(scanner.Text(), fromProvider, toFormat)
        fmt.Fprintf(pw, "%s\n", transformed)
    }
}()
w.Header().Set("Content-Type", "text/event-stream")
flusher := w.(http.Flusher)
io.Copy(w, pr)
flusher.Flush()
```

**关键特性：**
- 零拷贝流式（无中间缓冲区无限增长）
- 天然背压：上游阻塞如果下游慢
- 干净的 goroutine 边界

### Context 取消与反向取消（最关键）

**问题：** 客户端断连后，上游 LLM API 可能继续生成 token（浪费金钱）。

**解决方案：始终通过 `http.NewRequestWithContext(r.Context(), ...)` 创建上游请求**

```go
ctx := r.Context()
upstreamReq, _ := http.NewRequestWithContext(ctx, "POST", providerURL, body)
resp, err := httpClient.Do(upstreamReq)
// 当 ctx 被取消（客户端断连），上游请求自动中止
```

**io.Pipe 场景的双向取消：**
```go
ctx, cancel := context.WithCancel(r.Context())
defer cancel()

pr, pw := io.Pipe()
go func() {
    defer cancel() // 上游完成时取消 context
    defer pw.Close()
    // ... 读上游，写 pw ...
}()
io.Copy(w, pr) // 客户端断连时 r.Context() 取消 → 上游中止 → pw 关闭 → io.Copy 返回
```

### Provider Adapter 模式

采用 **Hub-and-Spoke 中间表示（IR）** 架构：

```
[Client] → [Canonical IR] → [Provider-specific wire format]
                                  ↑ adapter
[Provider SSE stream] → [Canonical IR] → [Client SSE stream]
                          ↑ adapter
```

```go
type Provider interface {
    Name() string
    ConvertRequest(canonical *ChatRequest) (*http.Request, error)
    StreamAdapter(body io.ReadCloser) <-chan *CanonicalChunk
}
```

**参考实现：** [Bifrost](https://github.com/maximhq/bifrost)（Go, 23+ 提供商）, [CC-Relay](https://cc-relay.ai/)

---

## 2. Rate Limiting (限流)

### 算法选型

| 算法 | 优点 | 缺点 | 适用场景 |
|------|------|------|---------|
| **Token Bucket** | 允许突发流量 | 实现稍复杂 | API 限流（推荐） |
| **Sliding Window Counter** | 平滑，无边界问题 | 有近似误差 | 高精度限流 |
| **Fixed Window** | 实现简单 | 边界突发问题 | 简单场景 |

### Redis 分布式限流实现

**Token Bucket + Redis Lua 脚本：**
```lua
-- KEYS[1] = rate limit key
-- ARGV[1] = capacity, ARGV[2] = tokens, ARGV[3] = now (ms)
local bucket = redis.call('HMGET', KEYS[1], 'tokens', 'last_refill')
local tokens = tonumber(bucket[1]) or tonumber(ARGV[1])
local last_refill = tonumber(bucket[2]) or tonumber(ARGV[3])
local elapsed = tonumber(ARGV[3]) - last_refill
local refill_rate = tonumber(ARGV[1]) / 60000 -- tokens per ms
tokens = math.min(tonumber(ARGV[1]), tokens + elapsed * refill_rate)
if tokens >= tonumber(ARGV[2]) then
    tokens = tokens - tonumber(ARGV[2])
    redis.call('HMSET', KEYS[1], 'tokens', tokens, 'last_refill', ARGV[3])
    redis.call('PEXPIRE', KEYS[1], 60000)
    return 1 -- allowed
end
return 0 -- denied
```

**L1 本地 + L2 Redis 双层：**
```go
type RateLimiter struct {
    local  *LocalTokenBucket   // 进程内快速判断
    redis  *RedisTokenBucket   // 全局精确计数
}

func (rl *RateLimiter) Allow(ctx context.Context, key string, tokens int) (bool, error) {
    // L1 快速判断：本地允许才查 Redis
    if !rl.local.Allow(key, tokens) {
        return false, nil
    }
    // L2 Redis 全局确认
    return rl.redis.Allow(ctx, key, tokens)
}
```

**4 级限流维度：**
- Per API Key: `ratelimit:key:{key_id}`
- Per Tenant: `ratelimit:tenant:{tenant_id}`
- Per Model: `ratelimit:model:{model_name}`
- Global: `ratelimit:global`

**参考：** [Redis Rate Limiting Guide](https://redis.io/tutorials/howtos/ratelimiting/), [Production Rate Limiter in Go with Redis and Lua](https://levelup.gitconnected.com/building-a-production-ready-rate-limiter-in-go-with-redis-and-lua-616d79873418)

---

## 3. Semantic Caching (语义缓存)

### 架构模式

```
Request → Embedding Model → Vector → Vector DB 查询
                                        ↓ 相似度 > 阈值?
                                       Yes → 返回缓存响应
                                       No  → 调用 LLM → 存储结果
```

### Redis Vector Sets（2025+ 新特性）

Redis 原生支持向量存储和相似度搜索，不需要额外的向量数据库：

```go
// 存储
embedding := getEmbedding(query)
redis.Do(ctx, "VSET.ADD", cacheKey, "VALUES", embedding, "SET", responseJSON, "EX", ttl)

// 查询
results, _ := redis.Do(ctx, "VSET.SIMQRY", cacheKey, "VALUES", queryEmbedding, "COUNT", 1, "WITHSCORES")
if results[0].Score > threshold {
    return cachedResponse // 命中
}
```

**但 CONTEXT.md 已决策用专用向量数据库**，可选方案：
- **Milvus**（开源，Go SDK 支持好）
- **Qdrant**（高性能，REST API）
- **Weaviate**（GraphQL 接口）

### Embedding 模型选型

| 模型 | 维度 | 成本 | 质量 |
|------|------|------|------|
| OpenAI text-embedding-3-small | 1536 | $0.02/1M tokens | 高 |
| OpenAI text-embedding-3-large | 3072 | $0.13/1M tokens | 最高 |
| 本地 all-MiniLM-L6-v2 | 384 | 免费 | 中 |

### 相似度阈值

- 默认建议 0.92-0.95
- CONTEXT.md 已决策：租户可配置
- 阈值过低 → 错误命中（返回不相关结果）
- 阈值过高 → 命中率太低

**参考：** [Redis Semantic Cache Docs](https://redis.io/docs/latest/develop/use-cases/semantic-cache/), [Semantic Caching for LLMs arXiv](https://arxiv.org/html/2603.03301v1)

---

## 4. Health Check & Graceful Shutdown

### 健康检查端点

```go
mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    w.Write([]byte(`{"status":"ok"}`))
})

mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
    // 检查依赖连接
    if !db.Ping() || !redis.Ping() {
        w.WriteHeader(http.StatusServiceUnavailable)
        return
    }
    w.WriteHeader(http.StatusOK)
    w.Write([]byte(`{"status":"ready"}`))
})
```

### 优雅停机流程

```go
func gracefulShutdown(server *http.Server, ctx context.Context) {
    // 1. 捕获信号
    ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
    defer stop()

    <-ctx.Done()

    // 2. 标记为不健康（让 LB/K8s 停止路由新流量）
    setUnhealthy()

    // 3. 等待 endpoint 摘除传播
    time.Sleep(5 * time.Second)

    // 4. 排空连接
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    server.Shutdown(shutdownCtx)
}
```

**关键要点：**
- `signal.NotifyContext` 捕获 SIGTERM/SIGINT
- 先标记 unhealthy，再 drain connections
- 给 K8s 留出 endpoint 摘除传播时间（~5s）
- `server.Shutdown(ctx)` 排空连接，超时后强制关闭

**参考：** [Graceful Shutdown in Go: Practical Patterns](https://victoriametrics.com/blog/go-graceful-shutdown/), [Go Graceful HTTP Shutdown: Zero-Downtime Production Patterns](https://backendbytes.com/articles/go-graceful-shutdown-production/)

---

## 5. Go 项目参考实现

| 项目 | 说明 | 参考价值 |
|------|------|---------|
| [Bifrost](https://github.com/maximhq/bifrost) | Go 高性能 AI 网关，23+ 提供商 | Provider 适配器、流式处理 |
| [CC-Relay](https://cc-relay.ai/) | Go 多提供商 HTTP 代理 | 架构设计、路由 |
| [distributed-rate-limiter](https://github.com/uppnrise/distributed-rate-limiter) | Go 分布式限流器 | Token Bucket/Sliding Window 实现 |

---

## Validation Architecture

### Dimensions for Validation

1. **Functional Correctness** — 请求正确路由到目标提供商，响应格式正确归一化
2. **Streaming Latency** — 首 token 延迟 < 100ms，chunk 转发延迟 < 5ms
3. **Error Handling** — Failover 正确触发，错误格式统一
4. **Rate Limiting** — 4 级限流正确生效，Redis 原子性保证
5. **Cache Hit Rate** — 精确缓存命中延迟 < 10ms
6. **Graceful Shutdown** — 无请求丢失，连接正确排空
7. **Context Cancellation** — 客户端断连后上游请求正确取消
8. **Concurrency** — 10 万并发连接无 goroutine 泄漏

---
*Research completed: 2026-06-30*
