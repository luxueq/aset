package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/luxueq/aigateway/internal/common/config"
	"github.com/luxueq/aigateway/internal/common/logger"
	"github.com/luxueq/aigateway/internal/gateway"
	"go.uber.org/zap"
)

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

	// Create gateway proxy
	proxy := gateway.NewProxy(cfg, log)

	// Setup HTTP server
	mux := http.NewServeMux()

	// Health check endpoints
	mux.HandleFunc("GET /health", healthHandler)
	mux.HandleFunc("GET /ready", readyHandler(cfg))

	// API proxy endpoints
	mux.Handle("/v1/", proxy)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      mux,
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
	gracefulShutdown(server, log)
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

func gracefulShutdown(server *http.Server, log *zap.Logger) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	<-ctx.Done()

	log.Info("shutdown signal received, starting graceful shutdown")

	// Mark unhealthy (LB/K8s stops routing new traffic)
	// In production, we'd set a global flag here

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
