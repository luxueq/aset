# API Key Management

## 概述

AIGateway 实现了完整的 API Key 管理系统，支持：
- ✅ 租户级加密隔离（每个租户独立的加密密钥）
- ✅ API Key 生命周期管理（创建、查询、撤销、删除）
- ✅ Bearer Token 认证中间件
- ✅ 权限控制（chat/embeddings/images）
- ✅ AES-256-GCM 加密存储

## 架构

```
┌─────────────────────────────────────────────────────────┐
│  HTTP Layer (handler.go)                                │
│  - POST /v1/api-keys     创建 API Key                   │
│  - GET  /v1/api-keys     列出 API Keys                  │
│  - GET  /v1/api-keys/:id 查询单个 API Key               │
│  - DELETE /v1/api-keys/:id 删除 API Key                 │
├─────────────────────────────────────────────────────────┤
│  Auth Middleware (middleware.go)                        │
│  - 拦截 /v1/chat/completions, /v1/embeddings 等请求     │
│  - 验证 Bearer Token                                    │
│  - 检查权限                                             │
│  - 设置请求上下文                                       │
├─────────────────────────────────────────────────────────┤
│  Business Logic (key.go)                                │
│  - KeyService: Create/Validate/Get/List/Revoke/Delete   │
│  - 租户隔离                                             │
│  - 过期检查                                             │
│  - 权限验证                                             │
├─────────────────────────────────────────────────────────┤
│  Encryption (encryptor.go)                              │
│  - AES-256-GCM 加密                                     │
│  - 租户级密钥隔离                                       │
│  - 密钥轮换支持                                         │
└─────────────────────────────────────────────────────────┘
```

## 使用示例

### 1. 创建 API Key

```bash
curl -X POST http://localhost:8080/v1/api-keys \
  -H "Content-Type: application/json" \
  -d '{
    "tenant_id": "tenant1",
    "name": "Production Key",
    "permissions": ["chat", "embeddings"],
    "rate_limit": {
      "tokens_per_minute": 10000,
      "tokens_per_day": 1000000
    },
    "quota": {
      "monthly_tokens": 10000000,
      "monthly_budget": 100.00
    }
  }'
```

响应：
```json
{
  "id": "abc123...",
  "key": "aigw_1234567890abcdef...",  // 只显示一次！
  "key_prefix": "aigw_12345***",
  "tenant_id": "tenant1",
  "name": "Production Key",
  "created_at": "2026-09-07T15:00:00Z"
}
```

⚠️ **重要**：`key` 字段只在创建时返回一次，请妥善保存！

### 2. 使用 API Key 调用模型

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer aigw_1234567890abcdef..." \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### 3. 查询 API Keys

```bash
curl -X GET "http://localhost:8080/v1/api-keys?tenant_id=tenant1"
```

响应：
```json
{
  "data": [
    {
      "id": "abc123...",
      "key_prefix": "aigw_12345***",
      "tenant_id": "tenant1",
      "name": "Production Key",
      "permissions": ["chat", "embeddings"],
      "revoked": false,
      "created_at": "2026-09-07T15:00:00Z",
      "last_used_at": "2026-09-07T16:30:00Z"
    }
  ]
}
```

### 4. 撤销 API Key

```bash
curl -X DELETE "http://localhost:8080/v1/api-keys/abc123...?tenant_id=tenant1"
```

## 租户级加密隔离

### 为租户添加独立加密密钥

```go
// 在代码中或启动时配置
keyService.GetEncryptor().AddTenantKey("tenant1", "tenant1-secret-key")
```

### 密钥轮换

```go
// 轮换租户的加密密钥（会自动重新加密该租户的所有 API Keys）
err := keyService.RotateTenantKey("tenant1", "new-secret-key")
```

### 加密格式

API Key 存储格式：`keyID:base64(nonce+ciphertext)`

示例：`tenant1:aGVsbG8gd29ybGQ=...`

- `keyID`: 使用的加密密钥 ID（租户 ID 或 "default"）
- `nonce`: 随机数（保证相同明文加密结果不同）
- `ciphertext`: AES-256-GCM 加密后的数据

## 安全特性

### 1. 多租户隔离

- 每个租户的 API Key 使用独立的加密密钥
- Get/Revoke/Delete 操作强制租户隔离检查
- 租户无法访问其他租户的 API Keys

### 2. 加密存储

- AES-256-GCM 加密（NIST 推荐）
- 每次加密使用随机 nonce
- 密钥从环境变量读取，不硬编码

### 3. 权限控制

- 基于路径的权限检查
- `/v1/chat/completions` → 需要 `chat` 权限
- `/v1/embeddings` → 需要 `embeddings` 权限
- `/v1/images` → 需要 `images` 权限

### 4. 生命周期管理

- 支持设置过期时间
- 支持撤销（软删除）
- 记录最后使用时间

## 配置

### 环境变量

```bash
# 默认加密密钥（所有租户共用）
export AIGATEWAY_KEY_ENCRYPTION_SECRET="your-secret-key"

# 租户级加密密钥（可选，代码中配置）
# keyService.GetEncryptor().AddTenantKey("tenant1", "tenant1-secret")
```

### 生产环境建议

1. **使用强随机密钥**：
   ```bash
   openssl rand -base64 32
   ```

2. **启用租户级隔离**：
   - 为每个租户配置独立的加密密钥
   - 定期轮换密钥

3. **集成 KMS**（Phase 7）：
   - AWS KMS / 阿里云 KMS / Vault
   - 自动密钥轮换
   - 审计日志

4. **数据库持久化**（Phase 1 后续）：
   - 当前使用内存存储
   - 生产环境需要 PostgreSQL
   - 支持多实例共享

## 错误响应

所有错误遵循 OpenAI 格式：

```json
{
  "error": {
    "message": "invalid_api_key",
    "type": "authentication_error",
    "code": "invalid_api_key"
  }
}
```

常见错误：
- `401 Unauthorized`: 无效/过期/撤销的 API Key
- `403 Forbidden`: 权限不足
- `404 Not Found`: API Key 不存在（或不属于该租户）

## 测试

```bash
# 运行所有测试
go test ./internal/auth/... -v

# 运行特定测试
go test ./internal/auth/... -run TestKeyService_TenantIsolation -v
```

## 下一步

- [ ] 数据库持久化（PostgreSQL）
- [ ] Admin 认证保护 API Key 管理端点
- [ ] 集成 KMS（Phase 7）
- [ ] 审计日志记录所有 Key 操作
- [ ] 支持批量操作（批量撤销/删除）
