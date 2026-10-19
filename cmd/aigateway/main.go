package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/luxueq/aigateway/internal/auth"
	"github.com/luxueq/aigateway/internal/cache"
	"github.com/luxueq/aigateway/internal/common/config"
	"github.com/luxueq/aigateway/internal/common/logger"
	"github.com/luxueq/aigateway/internal/gateway"
	"github.com/luxueq/aigateway/internal/ratelimit"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

// serverState tracks the draining flag used during graceful shutdown.
// Once draining is set, new requests are rejected so load balancers
// can route traffic elsewhere while in-flight requests complete.
type serverState struct {
	draining atomic.Bool
}

// rejectWhenDraining wraps a handler and rejects new requests with 503
// once the server has started draining.
func (s *serverState) rejectWhenDraining(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.draining.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":{"message":"server is shutting down","type":"server_error"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	// Load configuration
	cfgFile := "config/default.yaml"
	if envCfg := os.Getenv("AIGATEWAY_CONFIG"); envCfg != "" {
		cfgFile = envCfg
	}

	cfg, err := config.Load(cfgFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger
	log, err := logger.Init(cfg.Logging)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to init logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync()

	log.Info("starting AIGateway",
		zap.Int("port", cfg.Server.Port),
		zap.String("version", "0.1.0"),
	)

	// Initialize API Key service with tenant-level encryption
	encryptionSecret := os.Getenv("AIGATEWAY_KEY_ENCRYPTION_SECRET")
	if encryptionSecret == "" {
		encryptionSecret = "default-dev-secret-change-in-production"
		log.Warn("using default encryption secret, set AIGATEWAY_KEY_ENCRYPTION_SECRET in production")
	}

	keyService, err := auth.NewKeyService(encryptionSecret)
	if err != nil {
		log.Fatal("failed to create key service", zap.Error(err))
	}

	// Create gateway proxy
	proxy := gateway.NewProxy(cfg, log)

	// Setup HTTP server
	state := &serverState{}
	mux := http.NewServeMux()

	// Health check endpoints (no auth required)
	// During drain, the rejectWhenDraining middleware returns 503 for all
	// endpoints including /health, so LB/K8s stop routing new traffic.
	mux.HandleFunc("GET /health", healthHandler)
	mux.HandleFunc("GET /ready", readyHandler(cfg))

	// API Key management endpoints
	// NOTE: In production, protect these with admin authentication
	keyHandler := auth.NewHandler(keyService)
	keyHandler.RegisterRoutes(mux)

	// Create rate limiter (optional: nil when not configured)
	var limiter ratelimit.RateLimiter
	if cfg.RateLimiter != nil {
		limiter, err = ratelimit.NewRateLimiter(cfg.RateLimiter)
		if err != nil {
			log.Fatal("failed to create rate limiter", zap.Error(err))
		}
		// Redis/Hybrid/Sharded modes own backend connections (and hybrid a
		// background lease loop); release them on exit.
		defer limiter.Close()
	}

	// Create response cache (optional: nil when not configured)
	var responseCache *cache.Cache
	var semanticCache *cache.SemanticCache
	if cfg.Cache != nil && cfg.Cache.Enabled {
		var l2 *cache.RedisCache
		if cfg.Redis != nil {
			l2, err = cache.NewRedisCacheFromConfig(cfg.Redis, cfg.Cache.TTL)
			if err != nil {
				log.Fatal("failed to create cache redis client", zap.Error(err))
			}
			defer l2.Close()
		}
		responseCache, err = cache.NewCache(cfg.Cache.MaxItems, l2, cfg.Cache.TTL)
		if err != nil {
			log.Fatal("failed to create cache", zap.Error(err))
		}

		if cfg.Cache.SemanticEnabled {
			var embedder cache.Embedder
			if cfg.Cache.EmbeddingBackend == "local" {
				embedder = cache.NewLocalEmbedder(0, 0)
			} else {
				baseURL, apiKey := cfg.Cache.EmbeddingBaseURL, cfg.Cache.EmbeddingAPIKey
				if baseURL == "" {
					// Default to the first OpenAI-compatible provider instance.
					for _, p := range cfg.Providers {
						if p.Type == "openai" {
							baseURL = p.BaseURL
							if apiKey == "" {
								apiKey = p.APIKey
							}
							break
						}
					}
				}
				embedder = cache.NewOpenAICompatibleEmbedder(baseURL, apiKey, cfg.Cache.EmbeddingModel)
			}
			semanticCache = cache.NewSemanticCache(
				true,
				embedder,
				cache.NewMemoryVectorStore(cfg.Cache.MaxItems),
				cfg.Cache.SemanticThreshold,
				cfg.Cache.TTL,
			)
		}
	}

	// API proxy endpoints (protected by API key auth + caching + rate limiting)
	authMiddleware := auth.AuthMiddleware(keyService)
	var api http.Handler = proxy
	if limiter != nil {
		api = ratelimit.RateLimitMiddleware(limiter, log)(api)
	}
	if responseCache != nil {
		api = cache.CacheMiddleware(responseCache, semanticCache, log)(api)
	}
	mux.Handle("/v1/chat/completions", authMiddleware(api))
	mux.Handle("/v1/embeddings", authMiddleware(api))
	mux.Handle("/v1/images", authMiddleware(api))

	// Metrics endpoint (cache metrics; full observability is Phase 4)
	mux.Handle("GET /metrics", promhttp.Handler())

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      state.rejectWhenDraining(mux),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	// Start server in goroutine
	go func() {
		log.Info("server listening", zap.String("addr", server.Addr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("server error", zap.Error(err))
		}
	}()

	// Graceful shutdown
	gracefulShutdown(server, log, state)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func readyHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// TODO: Check DB and Redis connectivity
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ready"}`))
	}
}

func gracefulShutdown(server *http.Server, log *zap.Logger, state *serverState) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	<-ctx.Done()

	log.Info("shutdown signal received, starting graceful shutdown")

	// Mark unhealthy: LB/K8s stops routing new traffic and the server
	// rejects any new requests that still arrive.
	state.draining.Store(true)

	// Wait for endpoint removal to propagate
	time.Sleep(5 * time.Second)

	// Drain connections
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", zap.Error(err))
		os.Exit(1)
	}

	log.Info("server stopped gracefully")
}
