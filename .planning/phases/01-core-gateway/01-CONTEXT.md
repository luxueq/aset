# Phase 1: Core Gateway - Context

**Gathered:** 2026-06-30
**Status:** Ready for planning

## Phase Boundary

建立统一代理层，开发者通过一个 API 端点（`/v1/chat/completions`）即可调用任意 AI 模型提供商。包含：Provider 适配器、智能路由、自动 Failover、Token 限流、精确匹配缓存、语义缓存、配额与预算控制。

**13 requirements (GW-01 ~ GW-13)**

## Implementation Decisions

### Provider 适配策略
- **D-01:** 响应格式默认统一为 OpenAI 格式返回，同时支持可选透传（通过特殊 header 或 endpoint 保留提供商原始响应）— 与 LiteLLM/Portkey 保持一致
- **D-02:** 不支持的参数默认 drop（静默丢弃），提供商特有参数始终透传；可配置为严格模式（不支持则报错）
- **D-03:** API 端点类型：Chat Completions + Embeddings + Image Generation
- **D-04:** 错误统一映射为 OpenAI 风格错误响应（error.message, error.type）
- **D-05:** 新增提供商方式：配置文件定义基础提供商 + 运行时 API 动态添加
- **D-06:** 提供商 API Key 存储：配置文件 + 数据库混合（优先数据库，配置文件作为 fallback）
- **D-07:** 重试策略：429/500/502/503 自动重试，其他错误不重试

### 路由与 Failover
- **D-08:** 路由规则配置：配置文件定义默认路由 + API 动态覆盖
- **D-09:** 路由策略全部支持：轮询/固定分配 + 延迟优先 + 成本优先 + 加权分配
- **D-10:** Failover 行为可配置（自动透明 Failover 或返回错误由客户端决定），最多尝试 3 个提供商（可配置）
- **D-11:** 模型命名：客户端必须指定 provider/model 格式（如 `openai/gpt-4`）
- **D-12:** 提供商可用性判断：主动健康检查 + 被动错误率监控结合

### 缓存架构
- **D-13:** 缓存存储：L1 本地内存（热数据）+ L2 Redis（共享缓存）
- **D-14:** 精确缓存 key 计算：model + messages + 关键参数（temperature/seed），其他参数可通过配置忽略
- **D-15:** 语义缓存：专用向量数据库存储 embedding，embedding 计算方式可配置（API 或本地模型），相似度阈值租户可配置
- **D-16:** 缓存失效：TTL（默认 5 分钟，可配置）+ 手动清除 API + LRU 内存淘汰
- **D-17:** 缓存命中通知：响应头 `X-Cache: HIT/MISS` 标记
- **D-18:** 缓存指标：独立记录缓存命中率/未命中次数

### 限流与配额
- **D-19:** 限流存储：L1 本地内存（快速判断）+ L2 Redis（全局确认）
- **D-20:** 限流维度：Per API Key + Per 租户 + Per 模型 + 全局（4 级全支持）
- **D-21:** 配额计算：Token 配额（如每月 100 万 tokens）
- **D-22:** 超额行为可配置：直接拒绝（429）或降级到更便宜的模型
- **D-23:** 预算告警：阈值百分比（80%/90%/100%）+ 多渠道通知（Webhook/邮件/钉钉）+ Grafana 告警规则 + 响应头剩余配额
- **D-24:** 限流响应：正常请求带配额头（X-RateLimit-Limit/Remaining/Reset），被限流时返回 429 + Retry-After + 标准错误体

### 项目结构与模块化
- **D-25:** 目录布局：按领域划分，单 Go module

```
├── cmd/aigateway/       # 入口
├── internal/
│   ├── gateway/         # 核心代理层
│   ├── router/          # 路由引擎
│   ├── cache/           # 缓存（精确+语义）
│   ├── ratelimit/       # 限流
│   ├── provider/        # 提供商适配器
│   │   ├── openai/
│   │   ├── anthropic/
│   │   ├── gemini/
│   │   └── ...
│   ├── auth/            # 认证
│   └── common/          # 公共工具
├── api/                 # OpenAPI spec + proto
├── config/              # 配置文件
├── deployments/         # Docker/K8s 部署文件
└── docs/                # 文档
```

- **D-26:** 依赖注入：DI 框架（uber-go/fx 或 google/wire）
- **D-27:** 组件间通信：Interface-first，每个组件定义 Go interface

### 配置管理
- **D-28:** 配置文件格式：支持 YAML 和 TOML 两种
- **D-29:** 配置加载：spf13/viper
- **D-30:** 配置优先级：分层策略——基础设施配置（端口/地址）环境变量优先，业务配置（路由/限流/缓存规则）配置文件优先
- **D-31:** 支持配置热加载（SIGHUP 信号或文件监控），业务配置实时生效

### 数据库选型
- **D-32:** 元数据存储：PostgreSQL（JSONB 支持好，审计日志分区表，MVCC 高并发写入）
- **D-33:** ORM：sqlc 或 ent（类型安全，SQL-like API）

### 日志与脱敏
- **D-34:** 日志格式：JSON 结构化日志
- **D-35:** 脱敏策略：分级脱敏 + 可配置规则
  - INFO 级别：脱敏后的摘要（API Key 前 8 位 + `***`，messages 只记录 token 数和字符长度）
  - DEBUG 级别：完整记录，默认关闭，排查时临时开启
  - 独立审计日志：完整请求/响应存储到独立安全存储，授权角色可查
- **D-36:** 脱敏范围：认证信息 + 用户消息内容 + 提供商 API Key + PII 信息
- **D-37:** 请求日志默认级别：INFO 摘要（状态码 + 延迟 + token 用量）

### API 与运行时
- **D-38:** API 版本控制：URL 路径版本（`/v1/chat/completions`），与 OpenAI 保持一致
- **D-39:** 健康检查：全套（`/health` 存活检查 + `/ready` 就绪检查含依赖连接 + 优雅停机排空连接）
- **D-40:** 流式/非流式：统一 handler，根据 `stream` 参数自动切换处理逻辑
- **D-41:** HTTP 连接池：每个提供商独立的 HTTP client 连接池，可配置最大连接数/空闲超时

### Go 代码风格规范（基于 everything-claude-code golang-patterns）

#### 命名规范
- **包名**：全小写，单词间不用下划线（`gateway`, `ratelimit`, `cache`）
- **导出标识符**：PascalCase（`NewGateway`, `ProviderAdapter`）
- **非导出标识符**：camelCase（`httpClient`, `maxRetries`）
- **接口名**：描述行为，加 `-er` 后缀或用名词（`Router`, `Cache`, `Provider`）
- **常量**：PascalCase 或全大写 + 分组（`const MaxRetries = 3`）
- **缩写一致**：全大写或全小写（`HTTPClient` 而非 `HttpClient`，`APIKey` 而非 `ApiKey`）

#### 错误处理
- 始终检查 error，不允许 `_ = someFunc()` 忽略
- 错误向上传播，附带上下文：`fmt.Errorf("failed to connect to provider %s: %w", name, err)`
- 使用 `errors.Is()` 和 `errors.As()` 进行错误类型判断
- 定义领域特定错误类型（`var ErrProviderNotFound = errors.New("provider not found")`）
- 不要在底层包 import 高层包的错误

#### 并发模式
- 优先使用 channel 通信，而非共享内存
- 使用 `context.Context` 控制超时和取消
- goroutine 必须有明确的生命周期管理（`defer cancel()` 或 `errgroup`）
- 使用 `sync.WaitGroup` 或 `errgroup.Group` 等待一组 goroutine
- 避免 goroutine 泄漏：确保每个 goroutine 都能退出

#### 性能优化
- 避免在热路径上分配内存（使用 `sync.Pool` 复用对象）
- 字符串拼接使用 `strings.Builder`
- 切片预分配（`make([]T, 0, expectedCap)`）
- 使用 `bytedance/sonic` 替代 `encoding/json` 提升序列化性能
- 避免不必要的 interface 转换

#### 测试规范（基于 everything-claude-code golang-testing）
- 表驱动测试（table-driven tests）为标准模式
- 使用 `t.Run()` 创建子测试
- 使用 `t.Helper()` 标记辅助函数
- 使用 `t.Parallel()` 并行执行独立测试
- 覆盖率目标：核心业务逻辑 100%，公共 API 90%+，通用代码 80%+
- 使用 interface mock 而非第三方 mock 框架
- HTTP handler 测试使用 `httptest.NewRecorder()`

### Claude's Discretion
- 具体 Go 代码风格和命名规范：遵循 golang-patterns 规范
- 中间件链的具体顺序由 Claude 根据最佳实践决定
- 错误码的具体编号方案由 Claude 设计
- DI 框架选择（wire vs fx）由 Claude 根据项目需要决定
- ORM 选择（sqlc vs ent）由 Claude 根据项目需要决定

## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### 项目文档
- `.planning/PROJECT.md` — 项目上下文和核心决策
- `.planning/ROADMAP.md` — Phase 1 目标和 Success Criteria
- `.planning/REQUIREMENTS.md` — GW-01 ~ GW-13 需求详情
- `.planning/research/STACK.md` — 技术栈选型
- `.planning/research/FEATURES.md` — 功能分层和竞品对标
- `.planning/research/ARCHITECTURE.md` — 架构设计和组件边界
- `.planning/research/PITFALLS.md` — 常见陷阱和预防策略

### 行业参考
- LiteLLM Pass-Through: https://docs.litellm.ai/docs/proxy/pass_through — 透传模式参考
- LiteLLM Drop Params: https://docs.litellm.ai/docs/completion/drop_params — 参数处理参考
- Portkey Passthrough: https://portkey.ai/docs/integrations/llms/passthrough — 透传模式参考
- Go ReverseProxy Streaming: https://github.com/golang/go/issues/27816 — SSE 流式转发坑点

### 技术标准
- OpenAI API Format: https://platform.openai.com/docs/api-reference — 统一协议格式基准

## Existing Code Insights

### Reusable Assets
- 无现有代码（greenfield 项目）

### Established Patterns
- Go 标准库 `net/http` 为基础，不使用 Gin/Echo 等重框架
- SSE 流式转发使用 `io.Pipe` + `http.Flusher` 模式
- `httputil.ReverseProxy` 需要设 `FlushInterval: -1` 才能即时 flush
- Interface-first 设计，所有组件定义 Go interface
- 表驱动测试为默认测试模式

### Integration Points
- Phase 2 (SSE Runtime) 依赖 Phase 1 的代理层基础
- Phase 4 (Observability) 需要在 Phase 1 的中间件中埋入采集点

## Specific Ideas

- 项目结构按领域划分，每个功能域内聚在一个目录下
- 配置管理使用 Viper，支持 YAML/TOML + 环境变量 + 热加载
- 缓存采用 L1（本地内存）+ L2（Redis）双层架构
- 限流采用 L1（本地内存）+ L2（Redis）双层架构
- 日志采用分级脱敏 + 可配置规则，与 LiteLLM/Portkey 的脱敏策略一致
- 数据库用 PostgreSQL + sqlc/ent，JSONB 支持好

## Deferred Ideas

- WebSocket 支持 — AI 模型 API 都用 SSE，不需要 WebSocket（Out of Scope）
- gRPC 支持 — AI 模型 API 基本都是 HTTP/REST（Out of Scope）
- 模型训练/微调 — 只做推理代理，不做训练（Out of Scope）
- 前端 UI 应用 — 提供管理控制台 API，不开发前端应用（Out of Scope）
- Prompt 版本管理和 A/B 测试 — v2 需求（DEFERRED）
- 基于内容分类的智能路由 — v2 需求（DEFERRED）
- Agent 工作流编排 — 属于后续 Phase（DEFERRED）

---

*Phase: 1-Core Gateway*
*Context gathered: 2026-06-30*
