package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// DBRepository provides database persistence for API keys.
type DBRepository struct {
	db *sql.DB
}

// NewDBRepository creates a new database repository.
func NewDBRepository(dbURL string) (*DBRepository, error) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(3 * time.Minute)

	return &DBRepository{db: db}, nil
}

// InitSchema creates the api_keys table if it doesn't exist.
func (r *DBRepository) InitSchema(ctx context.Context) error {
	schema := `
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

	-- 索引：按租户查询
	CREATE INDEX IF NOT EXISTS idx_api_keys_tenant_id ON api_keys(tenant_id);

	-- 索引：按 key_hash 查询（Validate 操作）
	CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys(key_hash);

	-- 索引：筛选未撤销的 Key（常用查询优化）
	CREATE INDEX IF NOT EXISTS idx_api_keys_revoked ON api_keys(revoked) WHERE revoked = FALSE;

	-- 索引：按创建时间排序（List 操作）
	CREATE INDEX IF NOT EXISTS idx_api_keys_created_at ON api_keys(created_at DESC);

	-- 字段注释（PostgreSQL 标准语法）
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
	`

	_, err := r.db.ExecContext(ctx, schema)
	if err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	return nil
}

// Create inserts a new API key into the database.
func (r *DBRepository) Create(ctx context.Context, apiKey *APIKey) error {
	// Convert permissions to JSONB
	permissionsJSON, err := json.Marshal(apiKey.Permissions)
	if err != nil {
		return fmt.Errorf("failed to marshal permissions: %w", err)
	}

	// Convert rate_limit to JSONB
	var rateLimitJSON []byte
	if apiKey.RateLimit != nil {
		rateLimitJSON, err = json.Marshal(apiKey.RateLimit)
		if err != nil {
			return fmt.Errorf("failed to marshal rate_limit: %w", err)
		}
	}

	// Convert quota to JSONB
	var quotaJSON []byte
	if apiKey.Quota != nil {
		quotaJSON, err = json.Marshal(apiKey.Quota)
		if err != nil {
			return fmt.Errorf("failed to marshal quota: %w", err)
		}
	}

	query := `
		INSERT INTO api_keys (
			id, key_hash, key_prefix, key_encrypted, encryption_key_id,
			tenant_id, name, permissions, rate_limit, quota,
			created_at, expires_at, revoked
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8::jsonb, $9::jsonb, $10::jsonb,
			$11, $12, $13
		)
	`

	_, err = r.db.ExecContext(
		ctx, query,
		apiKey.ID,
		apiKey.KeyHash,
		apiKey.KeyPrefix,
		apiKey.KeyEncrypted,
		apiKey.EncryptionKeyID,
		apiKey.TenantID,
		apiKey.Name,
		string(permissionsJSON),
		rateLimitJSON,
		quotaJSON,
		apiKey.CreatedAt,
		apiKey.ExpiresAt,
		apiKey.Revoked,
	)

	if err != nil {
		return fmt.Errorf("failed to insert api key: %w", err)
	}

	return nil
}

// GetByKeyHash retrieves an API key by its hash.
func (r *DBRepository) GetByKeyHash(ctx context.Context, keyHash string) (*APIKey, error) {
	query := `
		SELECT
			id, key_hash, key_prefix, key_encrypted, encryption_key_id,
			tenant_id, name, permissions, rate_limit, quota,
			created_at, expires_at, revoked, last_used_at
		FROM api_keys
		WHERE key_hash = $1
	`

	var apiKey APIKey
	var permissionsJSON []byte
	var rateLimitJSON sql.NullString
	var quotaJSON sql.NullString
	var expiresAt sql.NullTime
	var lastUsedAt sql.NullTime

	err := r.db.QueryRowContext(ctx, query, keyHash).Scan(
		&apiKey.ID,
		&apiKey.KeyHash,
		&apiKey.KeyPrefix,
		&apiKey.KeyEncrypted,
		&apiKey.EncryptionKeyID,
		&apiKey.TenantID,
		&apiKey.Name,
		&permissionsJSON,
		&rateLimitJSON,
		&quotaJSON,
		&apiKey.CreatedAt,
		&expiresAt,
		&apiKey.Revoked,
		&lastUsedAt,
	)

	if err == sql.ErrNoRows {
		return nil, ErrKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query api key: %w", err)
	}

	// Parse permissions
	if err := json.Unmarshal(permissionsJSON, &apiKey.Permissions); err != nil {
		return nil, fmt.Errorf("failed to unmarshal permissions: %w", err)
	}

	// Parse rate_limit
	if rateLimitJSON.Valid && rateLimitJSON.String != "" {
		apiKey.RateLimit = &RateLimitConfig{}
		if err := json.Unmarshal([]byte(rateLimitJSON.String), apiKey.RateLimit); err != nil {
			return nil, fmt.Errorf("failed to unmarshal rate_limit: %w", err)
		}
	}

	// Parse quota
	if quotaJSON.Valid && quotaJSON.String != "" {
		apiKey.Quota = &QuotaConfig{}
		if err := json.Unmarshal([]byte(quotaJSON.String), apiKey.Quota); err != nil {
			return nil, fmt.Errorf("failed to unmarshal quota: %w", err)
		}
	}

	// Handle nullable timestamps
	if expiresAt.Valid {
		apiKey.ExpiresAt = &expiresAt.Time
	}
	if lastUsedAt.Valid {
		apiKey.LastUsedAt = &lastUsedAt.Time
	}

	return &apiKey, nil
}

// GetByID retrieves an API key by its ID and tenant ID.
func (r *DBRepository) GetByID(ctx context.Context, tenantID, id string) (*APIKey, error) {
	query := `
		SELECT
			id, key_hash, key_prefix, key_encrypted, encryption_key_id,
			tenant_id, name, permissions, rate_limit, quota,
			created_at, expires_at, revoked, last_used_at
		FROM api_keys
		WHERE id = $1 AND tenant_id = $2
	`

	var apiKey APIKey
	var permissionsJSON []byte
	var rateLimitJSON sql.NullString
	var quotaJSON sql.NullString
	var expiresAt sql.NullTime
	var lastUsedAt sql.NullTime

	err := r.db.QueryRowContext(ctx, query, id, tenantID).Scan(
		&apiKey.ID,
		&apiKey.KeyHash,
		&apiKey.KeyPrefix,
		&apiKey.KeyEncrypted,
		&apiKey.EncryptionKeyID,
		&apiKey.TenantID,
		&apiKey.Name,
		&permissionsJSON,
		&rateLimitJSON,
		&quotaJSON,
		&apiKey.CreatedAt,
		&expiresAt,
		&apiKey.Revoked,
		&lastUsedAt,
	)

	if err == sql.ErrNoRows {
		return nil, ErrKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query api key: %w", err)
	}

	// Parse permissions
	if err := json.Unmarshal(permissionsJSON, &apiKey.Permissions); err != nil {
		return nil, fmt.Errorf("failed to unmarshal permissions: %w", err)
	}

	// Parse rate_limit
	if rateLimitJSON.Valid && rateLimitJSON.String != "" {
		apiKey.RateLimit = &RateLimitConfig{}
		if err := json.Unmarshal([]byte(rateLimitJSON.String), apiKey.RateLimit); err != nil {
			return nil, fmt.Errorf("failed to unmarshal rate_limit: %w", err)
		}
	}

	// Parse quota
	if quotaJSON.Valid && quotaJSON.String != "" {
		apiKey.Quota = &QuotaConfig{}
		if err := json.Unmarshal([]byte(quotaJSON.String), apiKey.Quota); err != nil {
			return nil, fmt.Errorf("failed to unmarshal quota: %w", err)
		}
	}

	// Handle nullable timestamps
	if expiresAt.Valid {
		apiKey.ExpiresAt = &expiresAt.Time
	}
	if lastUsedAt.Valid {
		apiKey.LastUsedAt = &lastUsedAt.Time
	}

	return &apiKey, nil
}

// List retrieves all API keys for a tenant.
func (r *DBRepository) List(ctx context.Context, tenantID string) ([]*APIKey, error) {
	query := `
		SELECT
			id, key_hash, key_prefix, key_encrypted, encryption_key_id,
			tenant_id, name, permissions, rate_limit, quota,
			created_at, expires_at, revoked, last_used_at
		FROM api_keys
		WHERE tenant_id = $1
		ORDER BY created_at DESC
	`

	rows, err := r.db.QueryContext(ctx, query, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to query api keys: %w", err)
	}
	defer rows.Close()

	var keys []*APIKey
	for rows.Next() {
		var apiKey APIKey
		var permissionsJSON []byte
		var rateLimitJSON sql.NullString
		var quotaJSON sql.NullString
		var expiresAt sql.NullTime
		var lastUsedAt sql.NullTime

		err := rows.Scan(
			&apiKey.ID,
			&apiKey.KeyHash,
			&apiKey.KeyPrefix,
			&apiKey.KeyEncrypted,
			&apiKey.EncryptionKeyID,
			&apiKey.TenantID,
			&apiKey.Name,
			&permissionsJSON,
			&rateLimitJSON,
			&quotaJSON,
			&apiKey.CreatedAt,
			&expiresAt,
			&apiKey.Revoked,
			&lastUsedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan api key: %w", err)
		}

		// Parse permissions
		if err := json.Unmarshal(permissionsJSON, &apiKey.Permissions); err != nil {
			return nil, fmt.Errorf("failed to unmarshal permissions: %w", err)
		}

		// Parse rate_limit
		if rateLimitJSON.Valid && rateLimitJSON.String != "" {
			apiKey.RateLimit = &RateLimitConfig{}
			if err := json.Unmarshal([]byte(rateLimitJSON.String), apiKey.RateLimit); err != nil {
				return nil, fmt.Errorf("failed to unmarshal rate_limit: %w", err)
			}
		}

		// Parse quota
		if quotaJSON.Valid && quotaJSON.String != "" {
			apiKey.Quota = &QuotaConfig{}
			if err := json.Unmarshal([]byte(quotaJSON.String), apiKey.Quota); err != nil {
				return nil, fmt.Errorf("failed to unmarshal quota: %w", err)
			}
		}

		// Handle nullable timestamps
		if expiresAt.Valid {
			apiKey.ExpiresAt = &expiresAt.Time
		}
		if lastUsedAt.Valid {
			apiKey.LastUsedAt = &lastUsedAt.Time
		}

		keys = append(keys, &apiKey)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating api keys: %w", err)
	}

	return keys, nil
}

// UpdateLastUsedAt updates the last_used_at timestamp for an API key.
func (r *DBRepository) UpdateLastUsedAt(ctx context.Context, keyHash string, lastUsedAt time.Time) error {
	query := `UPDATE api_keys SET last_used_at = $1 WHERE key_hash = $2`

	_, err := r.db.ExecContext(ctx, query, lastUsedAt, keyHash)
	if err != nil {
		return fmt.Errorf("failed to update last_used_at: %w", err)
	}

	return nil
}

// Revoke marks an API key as revoked.
func (r *DBRepository) Revoke(ctx context.Context, tenantID, id string) error {
	query := `UPDATE api_keys SET revoked = TRUE WHERE id = $1 AND tenant_id = $2`

	result, err := r.db.ExecContext(ctx, query, id, tenantID)
	if err != nil {
		return fmt.Errorf("failed to revoke api key: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return ErrKeyNotFound
	}

	return nil
}

// Delete permanently removes an API key.
func (r *DBRepository) Delete(ctx context.Context, tenantID, id string) error {
	query := `DELETE FROM api_keys WHERE id = $1 AND tenant_id = $2`

	result, err := r.db.ExecContext(ctx, query, id, tenantID)
	if err != nil {
		return fmt.Errorf("failed to delete api key: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return ErrKeyNotFound
	}

	return nil
}

// UpdateEncryptionKey updates the encrypted key and encryption_key_id.
func (r *DBRepository) UpdateEncryptionKey(ctx context.Context, id, keyEncrypted, encryptionKeyID string) error {
	query := `UPDATE api_keys SET key_encrypted = $1, encryption_key_id = $2 WHERE id = $3`

	_, err := r.db.ExecContext(ctx, query, keyEncrypted, encryptionKeyID, id)
	if err != nil {
		return fmt.Errorf("failed to update encryption key: %w", err)
	}

	return nil
}

// Close closes the database connection.
func (r *DBRepository) Close() error {
	return r.db.Close()
}
