package logger

import (
	"testing"

	"github.com/luxueq/aigateway/internal/common/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestInit_JSONFormat(t *testing.T) {
	cfg := config.LoggingConfig{
		Level:  "info",
		Format: "json",
	}

	log, err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if log == nil {
		t.Fatal("Init() returned nil logger")
	}

	// Verify it's a production (JSON) logger
	if log.Core().Enabled(zapcore.DebugLevel) {
		t.Error("info level logger should not enable debug")
	}
	if !log.Core().Enabled(zapcore.InfoLevel) {
		t.Error("info level logger should enable info")
	}
}

func TestInit_DevelopmentFormat(t *testing.T) {
	cfg := config.LoggingConfig{
		Level:  "debug",
		Format: "console",
	}

	log, err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if log == nil {
		t.Fatal("Init() returned nil logger")
	}

	// Debug level should enable debug
	if !log.Core().Enabled(zapcore.DebugLevel) {
		t.Error("debug level logger should enable debug")
	}
}

func TestInit_InvalidLevel(t *testing.T) {
	cfg := config.LoggingConfig{
		Level:  "invalid",
		Format: "json",
	}

	_, err := Init(cfg)
	if err == nil {
		t.Error("Init() should return error for invalid level")
	}
}

func TestInit_LogOutput(t *testing.T) {
	cfg := config.LoggingConfig{
		Level:  "info",
		Format: "json",
	}

	log, err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// This should not panic
	log.Info("test message", zap.String("key", "value"))
	log.Warn("test warning")
	log.Error("test error")
}
