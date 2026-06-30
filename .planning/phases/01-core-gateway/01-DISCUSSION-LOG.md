# Phase 1: Core Gateway - Discussion Log

**Date:** 2026-06-30
**Mode:** Default (interactive)

## Areas Discussed

### Area 1: Provider 适配策略
| Question | Options | Selected | Notes |
|----------|---------|----------|-------|
| 响应格式处理 | 统一 OpenAI / 透传原始 / 默认统一+可选透传 | 默认统一+可选透传 | 与 LiteLLM/Portkey 一致 |
| 新增提供商方式 | 配置文件 / 运行时动态 / 配置+动态混合 | 配置+动态混合 | — |
| API Key 存储 | 数据库 / 环境变量+Vault / 配置+数据库混合 | 配置+数据库混合 | — |
| 错误映射 | 统一错误格式 / 保留原始错误 | 统一错误格式 | OpenAI 风格 |
| API 端点类型 | Chat / Embeddings / Image / Audio（多选） | Chat+Embeddings+Image | — |
| 参数处理 | 自动丢弃 / 严格报错 / 默认drop+可配置 | 默认drop+可配置 | 调研后确认 |
| 重试策略 | 特定状态码 / 全部 / 不重试 | 特定状态码重试 | 429/500/502/503 |

### Area 2: 路由与 Failover
| Question | Options | Selected | Notes |
|----------|---------|----------|-------|
| 路由配置方式 | 配置文件 / API动态 / 配置+API覆盖 | 配置+API覆盖 | — |
| 路由策略 | 轮询/延迟/成本/加权（多选） | 全部4种 | — |
| Failover行为 | 自动透明 / 返回错误 / 可配置 | 可配置 | — |
| 可用性判断 | 主动健康检查 / 被动错误率 / 主动+被动 | 主动+被动 | — |
| 模型命名 | 逻辑模型名 / 必须指定provider / 两种都支持 | 必须指定provider/model | — |
| 重试上限 | 2次 / 3次 / 可配置默认3 | 可配置默认3 | — |

### Area 3: 缓存架构
| Question | Options | Selected | Notes |
|----------|---------|----------|-------|
| 缓存介质 | Redis / 本地内存 / L1+L2 | L1内存+L2 Redis | — |
| Embedding计算 | API调用 / 本地模型 / 可配置 | 可配置 | — |
| 缓存key | 完整请求体 / 仅messages / 智能key / 完全可配置 | 智能key | model+messages+关键参数 |
| 失效策略 | TTL / TTL+版本标签 / TTL+手动+LRU | TTL+手动+LRU | 默认5分钟 |
| 缓存命中通知 | 响应头标记 / 不标记 | 响应头标记 | X-Cache: HIT/MISS |
| 缓存指标 | 单独统计 / 不单独 | 单独统计 | — |
| 语义阈值 | 0.95严格 / 0.90宽松 / 租户可配置 | 租户可配置 | — |
| 向量存储 | Redis Vector / 专用向量数据库 | 专用向量数据库 | — |

### Area 4: 限流与配额
| Question | Options | Selected | Notes |
|----------|---------|----------|-------|
| 限流存储 | Redis / 本地内存 / L1+L2 | L1本地+L2 Redis | — |
| 限流维度 | Per Key/租户/模型/全局（多选） | 全部4级 | — |
| 配额计算 | Token配额 / 请求次数 / 两种都支持 | Token配额 | — |
| 超额行为 | 直接拒绝 / 降级 / 可配置 | 可配置 | — |
| 预算告警 | 阈值/多渠道/Grafana/响应头（多选） | 全部4种 | — |
| 限流响应 | 429+Retry-After / 排队等待 / 两者都用 | 两者都用 | 正常带配额头，被限流429 |

### Area 5: 项目结构与模块化
| Question | Options | Selected | Notes |
|----------|---------|----------|-------|
| 目录布局 | 标准Go / 按领域 / 多模块monorepo | 按领域划分 | 单Go module |
| 依赖注入 | DI框架 / 手动注入 / 运行时DI | DI框架(wire/fx) | — |
| 组件通信 | Interface-first / 直接struct | Interface-first | — |

### Area 6: 配置管理
| Question | Options | Selected | Notes |
|----------|---------|----------|-------|
| 配置格式 | YAML / TOML / JSON | YAML+TOML两种 | — |
| 配置加载 | Viper / 纯环境变量 / 自研 | Viper | — |
| 配置优先级 | 环境变量优先 / 配置文件优先 / 分层 | 分层优先级 | 基础设施→环境变量，业务→配置文件 |
| 热加载 | 支持 / 不支持 | 支持 | SIGHUP或文件监控 |

### Area 7: 数据库选型
| Question | Options | Selected | Notes |
|----------|---------|----------|-------|
| 数据库 | PostgreSQL / MySQL / SQLite / SQLite+PG | PostgreSQL | JSONB/分区表/MVCC优势 |
| ORM | GORM / sqlc+ent / 纯SQL | sqlc/ent | 类型安全 |

### Area 8: 日志与脱敏
| Question | Options | Selected | Notes |
|----------|---------|----------|-------|
| 日志格式 | JSON / 纯文本 / 环境自适应 | JSON结构化 | — |
| 脱敏范围 | 认证/消息/ProviderKey/PII（多选） | 全部4种 | — |
| 脱敏方案 | 分级+可配置 / 全量脱敏 / 默认+可关闭 | 分级脱敏+可配置 | INFO脱敏+DEBUG完整+审计独立 |
| 日志级别 | INFO摘要 / INFO+DEBUG / 全量 | INFO级别摘要 | — |

### Area 9: API 版本控制
| Question | Options | Selected | Notes |
|----------|---------|----------|-------|
| 版本策略 | URL路径 / Header / 不做 | URL路径版本 | /v1/ 与OpenAI一致 |

### Area 10: 健康检查与优雅停机
| Question | Options | Selected | Notes |
|----------|---------|----------|-------|
| 健康检查 | 全套 / 仅/health | 全套 | /health+/ready+优雅停机 |

### Area 11: 流式/非流式统一处理
| Question | Options | Selected | Notes |
|----------|---------|----------|-------|
| 处理方式 | 统一handler自动切换 / 分离路由 | 统一handler | 根据stream参数自动切换 |

### Area 12: 并发与连接池
| Question | Options | Selected | Notes |
|----------|---------|----------|-------|
| 连接池策略 | 每提供商独立 / 全局共享 | 每提供商独立 | 可配置最大连接数/空闲超时 |

## Summary

- **Total areas discussed:** 12
- **Total decisions captured:** 41 (D-01 ~ D-41) + Go 代码风格规范
- **Canonical references:** 7 project docs + 4 industry references + 1 standard
- **Deferred ideas:** 7 items (Prompt管理、智能路由、Agent编排等)

---
*Discussion log: 2026-06-30*
