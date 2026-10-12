# Pitfalls Research — AIGateway

## 1. SSE 流式转发陷阱

### 坑：ReverseProxy 不即时 flush
- **症状：** 客户端收到大量数据一次性到达，而非逐 chunk
- **原因：** `httputil.ReverseProxy` 默认缓冲响应，不设 `FlushInterval`
- **解决：** 设 `FlushInterval: -1`（即时 flush），或自建流式转发不用 ReverseProxy
- **发现时机：** Phase 1（代理层）
- **参考：** [Go Issue #27816](https://github.com/golang/go/issues/27816)

### 坑：goroutine 泄漏
- **症状：** 内存持续增长，连接数不降
- **原因：** 上游断开但下游 goroutine 没退出，或 `io.Pipe` writer 没 Close
- **解决：** 每个 goroutine 必须 `defer close()` + 监听 `ctx.Done()`。Go 1.26 的 goroutine leak profiling 可辅助检测
- **发现时机：** Phase 2（SSE 运行时）

### 坑：Content-Length 导致 chunked 失效
- **症状：** 流式响应变成一次性返回
- **原因：** 设置了 `Content-Length` header，Go 会等全部数据就绪才发送
- **解决：** 不设置 `Content-Length`，让 Go 自动使用 `Transfer-Encoding: chunked`
- **发现时机：** Phase 1

## 2. 多模型路由陷阱

### 坑：提供商协议差异
- **症状：** 同一个请求格式，不同提供商返回不同的错误码/格式
- **原因：** OpenAI 兼容 ≠ 完全一致，各提供商有自己的边缘行为
- **解决：** Provider 适配器模式，每个提供商单独处理差异
- **发现时机：** Phase 1

### 坑：Failover 时的幂等性问题
- **症状：** 重试导致重复计费或重复执行
- **原因：** 非幂等操作的重试
- **解决：** 只对幂等操作（chat completions）做 failover，不对非幂等操作（fine-tuning）重试
- **发现时机：** Phase 1

### 坑：Token 计算不准确
- **症状：** 成本统计与实际账单不符
- **原因：** 不同模型的 tokenizer 不同，cached tokens 和 reasoning tokens 计费不同
- **解决：** 使用提供商返回的 `usage` 字段，不自己计算。对 cached/reasoning tokens 单独计费
- **发现时机：** Phase 1

### 坑：多实例部署时路由状态单实例化
- **症状：** 横向扩展后，不同实例路由到不同 provider，或某个实例驱逐了 provider 而其他实例仍在用
- **原因：** `Latency`(EMA)、`Healthy/ErrorRate`(滑动窗口错误率)、`RoundRobin` 计数器都是**进程内存态**，各实例独立统计、独立决策（`router.go`）
- **严重度分层（不是一刀切）：**
  - **健康状态 Healthy/ErrorRate —— 最严重**：实例 A 驱逐 provider X、实例 B 仍打 X，failover 一致性被打破，且不自愈
  - **Latency(EMA) —— 次之**：各实例到上游的网络路径不同，观察到的延迟不一致，决策持续分裂
  - **RoundRobin 计数器 —— 最轻**：长期来看下游分布仍均匀（各实例各自均匀之和仍是均匀）；真实风险只是冷启动时计数器同频造成的瞬时对齐，会随时间自然错开。非加权轮询没有加权平滑的跨实例一致性问题
  - **Cost / Weighted —— 无此问题**：输入来自配置文件（静态），全局一致
- **解决：** 水平扩展时把延迟/健康状态外置到共享存储（Redis/etcd），或从 metrics 系统读聚合值；轮询可改用一致性哈希（无状态、无共享计数）；健康检查改为集中式探活器（避免 N 实例 × M provider 的探测放大）
- **发现时机：** Phase 1（路由引擎）—— 单实例下正确，MVP 保留；扩展前需改造

## 3. MCP 实现陷阱

### 坑：SSE 已废弃，必须用 Streamable HTTP
- **症状：** 实现了 SSE 传输，但新客户端不支持
- **原因：** MCP 2025-03-26 规范废弃了 HTTP+SSE，改用 Streamable HTTP
- **解决：** 实现 Streamable HTTP 传输，可选保留 SSE 作为兼容
- **发现时机：** Phase 3（MCP Server）
- **参考：** [MCP Transports Spec](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)

### 坑：2026-07-28 无状态协议变更
- **症状：** 依赖 `Mcp-Session-Id` 的实现会在新规范下失效
- **原因：** 新规范移除 session 概念，协议变为无状态
- **解决：** 从一开始就按无状态设计，不依赖 session 状态
- **发现时机：** Phase 3

### 坑：Tool 执行超时
- **症状：** Agent 调用 Tool 后一直等待，最终超时
- **原因：** Tool 执行器没有超时控制，或沙箱启动慢
- **解决：** 所有 Tool 执行必须有超时，默认 30s，可配置。沙箱预热
- **发现时机：** Phase 5

## 4. 沙箱执行陷阱

### 坑：Firecracker 冷启动延迟
- **症状：** 首次 Tool 调用延迟高（~150ms+）
- **原因：** microVM 需要启动独立内核
- **解决：** 预热池（预先启动若干 microVM），快照恢复
- **发现时机：** Phase 5

### 坑：沙箱资源泄漏
- **症状：** 容器/microVM 越来越多，磁盘和内存耗尽
- **原因：** Tool 执行异常退出，cleanup 逻辑没执行
- **解决：** 所有沙箱必须有 TTL，超时强制销毁。defer cleanup + finalizer
- **发现时机：** Phase 5

### 坑：浏览器自动化内存
- **症状：** chromedp 实例内存持续增长
- **原因：** 浏览器页面没关闭，或 JS 内存泄漏
- **解决：** 每个浏览器任务分配独立实例，任务完成立即 Close。设置最大内存限制
- **发现时机：** Phase 5

## 5. 可观测性陷阱

### 坑：高基数指标爆炸
- **症状：** Prometheus 内存爆炸，查询变慢
- **原因：** 将 request_id、user_id 等高基数值作为 metric label
- **解决：** 只用低基数 label（model、provider、status_code）。高基数数据放 trace/log
- **发现时机：** Phase 4

### 坑：Trace 数据量过大
- **症状：** 存储成本失控
- **原因：** 每个请求都记录完整 trace，包括大 payload
- **解决：** 采样策略（错误请求 100%、正常请求 10%）。不记录 request/response body
- **发现时机：** Phase 4

## 6. 多 Agent 通信陷阱

### 坑：消息丢失
- **症状：** Agent A 发的消息 Agent B 没收到
- **原因：** Pub/Sub 模式下，如果 B 没在订阅，消息直接丢弃
- **解决：** 关键消息用 JetStream（持久化 + at-least-once）。非关键用 Pub/Sub
- **发现时机：** Phase 6

### 坑：NATS 单点故障
- **症状：** NATS 挂了，所有 Agent 通信中断
- **原因：** 单节点 NATS 部署
- **解决：** NATS cluster 至少 3 节点。或进程内 bus 降级方案
- **发现时机：** Phase 6

---
*Confidence: High — 基于 Go 社区实际踩坑经验和竞品已知问题*
*Last updated: 2026-06-30*
