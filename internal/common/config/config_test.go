package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_DefaultValues(t *testing.T) {
	// Create a minimal config file
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(cfgFile, []byte(`
server:
  port: 9090
providers:
  openai:
    base_url: "https://api.openai.com/v1"
    api_key: "test-key"
logging:
  level: debug
  format: json
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Check server config
	if cfg.Server.Port != 9090 {
		t.Errorf("Server.Port = %d, want 9090", cfg.Server.Port)
	}

	// Check defaults for unset values
	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("Server.ReadTimeout = %v, want 30s", cfg.Server.ReadTimeout)
	}
	if cfg.Server.WriteTimeout != 120*time.Second {
		t.Errorf("Server.WriteTimeout = %v, want 120s", cfg.Server.WriteTimeout)
	}

	// Check provider config
	if cfg.Providers.OpenAI == nil {
		t.Fatal("Providers.OpenAI is nil")
	}
	if cfg.Providers.OpenAI.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("OpenAI.BaseURL = %q, want %q", cfg.Providers.OpenAI.BaseURL, "https://api.openai.com/v1")
	}
	if cfg.Providers.OpenAI.APIKey != "test-key" {
		t.Errorf("OpenAI.APIKey = %q, want %q", cfg.Providers.OpenAI.APIKey, "test-key")
	}

	// Check logging config
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "debug")
	}
	if cfg.Logging.Format != "json" {
		t.Errorf("Logging.Format = %q, want %q", cfg.Logging.Format, "json")
	}
}

func TestLoad_EnvironmentVariableOverride(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(cfgFile, []byte(`
server:
  port: 8080
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Set environment variable to override port
	os.Setenv("AIGATEWAY_SERVER_PORT", "9999")
	defer os.Unsetenv("AIGATEWAY_SERVER_PORT")

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 9999 {
		t.Errorf("Server.Port = %d, want 9999 (from env)", cfg.Server.Port)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Error("Load() should return error for missing file")
	}
}

func TestLoad_DefaultPort(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")
	// Minimal config without port
	err := os.WriteFile(cfgFile, []byte(`
logging:
  level: info
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want 8080 (default)", cfg.Server.Port)
	}
}
