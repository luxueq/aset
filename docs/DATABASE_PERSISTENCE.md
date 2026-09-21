# API Key 数据库持久化

## 概述

API Key 系统支持两种存储模式：
1. **内存存储**（默认）：适合开发和测试
2. **PostgreSQL 持久化**：适合生产环境

## 数据库 Schema

### 表结构

```sql
-- API Keys 表：存储租户的 API 密钥及其配置
CREATE TABLE IF NOT EXISTS api_keys (
    -- 主键：UUID v4，全局唯一标识符（16字节）
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- API Key 的 SHA-256 哈希值，用于快速查询（不存储明文）
    key_hash VARCHAR(64) UNIQUE NOT NULL DEFAULT '',

    -- API Key 前缀（前13位 + "***"），用于展示和识别
    key_prefix VARCHAR(20) NOT NULL DEFAULT '',

    -- AES-256-GCM 加密后的 API Key 明文（格式：keyID:base64(nonce+ciphertext)）
    key_encrypted TEXT NOT NULL DEFAULT '',

    -- 使用的加密密钥 ID（支持租户级密钥隔离）
    encryption_key_id VARCHAR(100) NOT NULL DEFAULT 'default',

    -- 租户 ID：标识该 API Key 属于哪个租户
    tenant_id VARCHAR(100) NOT NULL DEFAULT '',

    -- API Key 名称：用户自定义的描述性名称
    name VARCHAR(255) NOT NULL DEFAULT '',

    -- 权限列表：JSON 数组格式，如 ["chat", "embeddings", "images"]
    permissions JSONB NOT NULL DEFAULT '[]'::jsonb,

    -- 限流配置：JSON 对象，包含 tokens_per_minute, tokens_per_day, max_concurrent
    rate_limit JSONB,

    -- 配额配置：JSON 对象，包含 monthly_tokens, monthly_budget
    quota JSONB,

    -- 创建时间：API Key 的创建时间戳
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- 过期时间：API Key 的过期时间戳（NULL 表示永不过期）
    expires_at TIMESTAMPTZ,

    -- 最后使用时间：最后一次成功验证的时间戳（用于活跃度分析）
    last_used_at TIMESTAMPTZ,

    -- 撤销标志：TRUE 表示该 API Key 已被撤销，不能再使用
    revoked BOOLEAN NOT NULL DEFAULT FALSE
);
```

### 索引

```sql
-- 按租户查询
CREATE INDEX idx_api_keys_tenant_id ON api_keys(tenant_id);

-- 按 key_hash 查询（Validate 操作）
CREATE INDEX idx_api_keys_key_hash ON api_keys(key_hash);

-- 筛选未撤销的 Key（常用查询优化）
CREATE INDEX idx_api_keys_revoked ON api_keys(revoked) WHERE revoked = FALSE;

-- 按创建时间排序（List 操作）
CREATE INDEX idx_api_keys_created_at ON api_keys(created_at DESC);
```

### 字段注释

```sql
COMMENT ON TABLE api_keys IS 'API Keys 表：存储租户的 API 密钥及其配置信息';
COMMENT ON COLUMN api_keys.id IS '主键：UUID v4，全局唯一标识符（16字节）';
COMMENT ON COLUMN api_keys.key_hash IS 'API Key 的 SHA-256 哈希值，用于快速查询（不存储明文）';
COMMENT ON COLUMN api_keys.key_prefix IS 'API Key 前缀（前13位 + "***"），用于展示和识别';
COMMENT ON COLUMN api_keys.key_encrypted IS 'AES-256-GCM 加密后的 API Key 明文（格式：keyID:base64(nonce+ciphertext)）';
COMMENT ON COLUMN api_keys.encryption_key_id IS '使用的加密密钥 ID（支持租户级密钥隔离）';
COMMENT ON COLUMN api_keys.tenant_id IS '租户 ID：标识该 API Key 属于哪个租户';
COMMENT ON COLUMN api_keys.name IS 'API Key 名称：用户自定义的描述性名称';
COMMENT ON COLUMN api_keys.permissions IS '权限列表：JSON 数组格式，如 ["chat", "embeddings", "images"]';
COMMENT ON COLUMN api_keys.rate_limit IS '限流配置：JSON 对象，包含 tokens_per_minute, tokens_per_day, max_concurrent';
COMMENT ON COLUMN api_keys.quota IS '配额配置：JSON 对象，包含 monthly_tokens, monthly_budget';
COMMENT ON COLUMN api_keys.created_at IS '创建时间：API Key 的创建时间戳';
COMMENT ON COLUMN api_keys.expires_at IS '过期时间：API Key 的过期时间戳（NULL 表示永不过期）';
COMMENT ON COLUMN api_keys.last_used_at IS '最后使用时间：最后一次成功验证的时间戳（用于活跃度分析）';
COMMENT ON COLUMN api_keys.revoked IS '撤销标志：TRUE 表示该 API Key 已被撤销，不能再使用';
```

## 设计决策

### 为什么使用 UUID v4 作为主键？

| 方案 | 存储大小 | 优点 | 缺点 |
|------|----------|------|------|
| BIGSERIAL | 8 bytes | 最快、最小 | 可预测、多实例冲突 |
| **UUID v4** | **16 bytes** | **全局唯一、不可预测、多实例友好** | 比 BIGINT 大 |
| TEXT(32) | 32 bytes | 灵活 | 慢、大、非标准 |

**选择 UUID v4 的原因：**
1. ✅ 多实例部署无需协调
2. ✅ 不可预测（安全）
3. ✅ PostgreSQL 原生支持 `gen_random_uuid()`
4. ✅ 行业标准（Stripe、GitHub 等）
5. ✅ 性能比 TEXT(32) 好得多

### 字段类型选择

| 字段 | 类型 | 原因 |
|------|------|------|
| `id` | UUID | 全局唯一，适合分布式系统 |
| `key_hash` | VARCHAR(64) | SHA-256 固定 64 字符 |
| `key_prefix` | VARCHAR(20) | 固定格式 "aigw_xxxxx***" |
| `key_encrypted` | TEXT | 加密后长度不固定 |
| `tenant_id` | VARCHAR(100) | 租户标识，长度可变 |
| `name` | VARCHAR(255) | 用户自定义名称 |
| `permissions` | JSONB | 数组格式，需要灵活查询 |
| `rate_limit` | JSONB | 对象格式，字段可能变化 |
| `quota` | JSONB | 对象格式，字段可能变化 |
| `created_at` | TIMESTAMPTZ | 带时区的时间戳 |
| `revoked` | BOOLEAN | 布尔标志 |

## 使用方法

### 1. 配置数据库连接

```bash
# 环境变量
export AIGATEWAY_DATABASE_URL="postgres://aigw:aigw@localhost:5432/aigateway?sslmode=disable"
```

或在 `config/default.yaml` 中配置：

```yaml
database:
  url: "postgres://aigw:aigw@localhost:5432/aigateway?sslmode=disable"
  max_open_conns: 25
  max_idle_conns: 10
  conn_max_lifetime: 5m
  conn_max_idle_time: 3m
```

### 2. 初始化数据库

```go
// 在 main.go 中
if cfg.Database != nil && cfg.Database.URL != "" {
    // 使用数据库存储
    dbRepo, err := auth.NewDBRepository(cfg.Database.URL)
    if err != nil {
        log.Fatal("failed to connect to database", zap.Error(err))
    }
    defer dbRepo.Close()

    // 初始化表结构
    ctx := context.Background()
    if err := dbRepo.InitSchema(ctx); err != nil {
        log.Fatal("failed to init database schema", zap.Error(err))
    }

    // 创建 KeyService（使用数据库）
    keyService = auth.NewKeyServiceWithDB(encryptionSecret, dbRepo)
} else {
    // 使用内存存储（开发环境）
    keyService, err = auth.NewKeyService(encryptionSecret)
}
```

### 3. KeyService 接口

```go
// 内存存储
keyService, err := auth.NewKeyService(encryptionSecret)

// 数据库存储
dbRepo, err := auth.NewDBRepository(dbURL)
keyService := auth.NewKeyServiceWithDB(encryptionSecret, dbRepo)
```

## 性能优化

### 1. 连接池配置

```go
db.SetMaxOpenConns(25)      // 最大连接数
db.SetMaxIdleConns(10)      // 最大空闲连接
db.SetConnMaxLifetime(5m)   // 连接最大生命周期
db.SetConnMaxIdleTime(3m)   // 空闲连接最大时间
```

### 2. 索引优化

- `key_hash` 唯一索引：Validate 操作 O(1)
- `tenant_id` 索引：List 操作快速过滤
- `revoked` 部分索引：只索引未撤销的 Key
- `created_at` 索引：List 按时间排序

### 3. JSONB 查询

```sql
-- 查询有 chat 权限的 Key
SELECT * FROM api_keys 
WHERE permissions @> '["chat"]'::jsonb;

-- 查询限流配置
SELECT rate_limit->>'tokens_per_minute' FROM api_keys;
```

## 数据示例

### 插入示例

```sql
INSERT INTO api_keys (
    id, key_hash, key_prefix, key_encrypted, encryption_key_id,
    tenant_id, name, permissions, rate_limit, quota, created_at
) VALUES (
    '550e8400-e29b-41d4-a716-446655440000',
    'a1b2c3d4e5f6...',
    'aigw_12345***',
    'tenant1:base64data...',
    'tenant1',
    'tenant1',
    'Production Key',
    '["chat", "embeddings"]'::jsonb,
    '{"tokens_per_minute": 10000, "tokens_per_day": 1000000}'::jsonb,
    '{"monthly_tokens": 10000000, "monthly_budget": 100.00}'::jsonb,
    NOW()
);
```

### 查询示例

```sql
-- 按 key_hash 查询（Validate）
SELECT * FROM api_keys WHERE key_hash = 'abc123...';

-- 列出租户的所有 Key
SELECT id, key_prefix, name, created_at 
FROM api_keys 
WHERE tenant_id = 'tenant1' 
ORDER BY created_at DESC;

-- 查询未撤销的 Key
SELECT * FROM api_keys 
WHERE tenant_id = 'tenant1' AND revoked = FALSE;

-- 统计每个租户的 Key 数量
SELECT tenant_id, COUNT(*) as key_count
FROM api_keys
WHERE revoked = FALSE
GROUP BY tenant_id;
```

## 迁移和版本管理

当前没有使用数据库迁移工具（如 golang-migrate），表结构通过 `InitSchema()` 自动创建。

**未来可以添加：**
- `golang-migrate` 或 `goose` 进行版本化迁移
- 多环境配置（dev/staging/prod）
- 备份和恢复策略

## 安全考虑

1. **API Key 明文不存储**：只存储 SHA-256 哈希和加密后的密文
2. **租户级加密隔离**：每个租户使用独立的加密密钥
3. **数据库访问控制**：使用独立的数据库用户，限制权限
4. **连接加密**：生产环境使用 SSL/TLS（`sslmode=require`）
5. **审计日志**：记录所有 Key 操作（创建、撤销、删除）

## 监控和维护

### 1. 监控指标

```sql
-- Key 总数
SELECT COUNT(*) FROM api_keys;

-- 活跃 Key 数（未撤销且未过期）
SELECT COUNT(*) FROM api_keys 
WHERE revoked = FALSE 
  AND (expires_at IS NULL OR expires_at > NOW());

-- 最近 7 天活跃 Key
SELECT COUNT(DISTINCT id) FROM api_keys 
WHERE last_used_at > NOW() - INTERVAL '7 days';

-- 每个租户的 Key 数量
SELECT tenant_id, COUNT(*) FROM api_keys 
GROUP BY tenant_id;
```

### 2. 清理任务

```sql
-- 删除过期的 Key（可选）
DELETE FROM api_keys 
WHERE expires_at < NOW() - INTERVAL '30 days';

-- 清理长期未使用的 Key
UPDATE api_keys SET revoked = TRUE 
WHERE last_used_at < NOW() - INTERVAL '1 year' 
  AND revoked = FALSE;
```

## 下一步

- [ ] 添加数据库迁移工具（golang-migrate）
- [ ] 实现审计日志表
- [ ] 添加数据库连接监控
- [ ] 实现数据库备份策略
- [ ] 添加慢查询日志分析
