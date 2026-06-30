# Research Summary — AIGateway

## Executive Summary

AIGateway 是一个用 Go 构建的生产级 AI Agent 基础设施平台，对标 Portkey + Cloudflare AI Gateway + LiteLLM 的能力，同时增加 Agent 运行时、MCP 原生支持、沙箱执行和多 Agent 通信等差异化能力。

---

## Stack: 关键技术选型

| 层 | 选型 | 置信度 |
|----|------|--------|
| HTTP/SSE | `net/http` 标准库 + 自建 SSE handler | ⭐⭐⭐⭐⭐ |
| JSON 序列化 | `bytedance/sonic` v2 | ⭐⭐⭐⭐ |
| MCP | 官方 `go-sdk/mcp`（Streamable HTTP，SSE 已废弃） | ⭐⭐⭐⭐⭐ |
| 消息总线 | NATS + `nats.go`（外部）/ `channel+select`（进程内） | ⭐⭐⭐⭐⭐ |
| 可观测性 | OTel → Prometheus（Phase 1）→ ClickHouse（Phase 2） | ⭐⭐⭐⭐ |
| 沙箱 | Firecracker microVM（默认）/ gVisor（容器兼容） | ⭐⭐⭐⭐ |
| 浏览器自动化 | `chromedp`（Go 原生 CDP） | ⭐⭐⭐⭐ |
| 分布式锁 | `go-redsync`（Redis Redlock） | ⭐⭐⭐⭐ |

**关键发现：** Go 1.26 新增 goroutine leak profiling，对长连接 SSE 网关场景非常有价值。MCP 2026-07-28 规范将协议改为无状态，从一开始就应按无状态设计。

## Table Stakes: 必须有的功能

1. **统一代理** — OpenAI 兼容协议，100+ 模型提供商
2. **智能路由** — 动态路由 + 自动 Failover + 负载均衡
3. **Token 限流** — per-user/per-key/per-model，固定窗口 + 滑动窗口
4. **成本追踪** — Token 用量统计 + 成本归因（Portkey 级别：40+ 数据点/请求）
5. **可观测性** — 请求级 tracing + 指标 + 日志
6. **API Key 管理** — 创建/轮转/撤销/权限分级

## Watch Out For: 关键陷阱

1. **SSE 流式转发** — `httputil.ReverseProxy` 默认不 flush，必须设 `FlushInterval: -1` 或自建转发
2. **goroutine 泄漏** — 长连接场景最大风险，必须 `defer close()` + `ctx.Done()` 监听
3. **MCP SSE 已废弃** — 2025-03-26 规范已废弃 HTTP+SSE，必须用 Streamable HTTP
4. **高基数指标** — 不要把 request_id/user_id 作为 Prometheus label
5. **沙箱资源泄漏** — 所有沙箱必须有 TTL + 强制销毁
6. **Token 计算** — 使用提供商返回的 `usage` 字段，不同模型 tokenizer 不同
7. **Content-Length** — 流式响应不能设置，否则禁用 chunked transfer encoding

## Differentiators: AIGateway 的差异化优势

| 差异化 | 竞品状态 | AIGateway 方案 |
|-------|---------|---------------|
| Agent 运行时 | Portkey 无、LiteLLM 无、Cloudflare 有推理层 | SSE 网关 + 背压控制 + 自动重连 |
| MCP 深度集成 | Portkey/Cloudflare 基础支持 | 完整 MCP Server + 协议升级 + 多租户 |
| 沙箱执行 | 几乎无竞品内置 | Docker/Firecracker/chromedp 统一管理 |
| 多 Agent 通信 | 无竞品内置 | NATS 消息总线 + GroupChat |
| Go 高性能 | 竞品多为 Python/Node | Go 原生高并发，适合网关场景 |
| 国产模型 | 大部分竞品不支持 | 通义/文心/讯飞/智谱/DeepSeek 原生支持 |

## Build Order: 构建顺序

```
Phase 1: L0 Core Gateway → 能代理请求、路由、限流
Phase 2: L1 SSE Runtime → 能处理流式请求
Phase 3: L1 MCP Server → 能注册和调用 Tool
Phase 4: Observability → 能看到系统运行状态
Phase 5: L2 Tool Executor + Sandbox → 能执行 Tool
Phase 6: L2 State + Bus → 多 Agent 能协作
Phase 7: L3 Governance → 能对外服务
Phase 8: Optimization → 缓存 + ClickHouse 深度分析
Phase 9: SDK → Java + Go SDK 封装
```

## Research Files

- `STACK.md` — 技术栈选型（含 SSE 流式模式代码示例）
- `FEATURES.md` — 功能分层 + 竞品对标
- `ARCHITECTURE.md` — 架构设计 + 组件边界 + 数据流
- `PITFALLS.md` — 常见陷阱 + 预防策略

---
*Last updated: 2026-06-30*
