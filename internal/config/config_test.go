package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaultsAndAliases(t *testing.T) {
	t.Setenv("KAITEN_API_TOKEN", "")
	t.Setenv("KAITEN_TOKEN", "secret")
	t.Setenv("KAITEN_URL", "")
	t.Setenv("KAITEN_BASE_URL", "https://tenant.example/")
	t.Setenv("KAITEN_RATE_LIMIT_RPS", "")
	t.Setenv("KAITEN_RATE_LIMIT", "4")
	cfg, err := Load(true, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "secret" || cfg.BaseURL.String() != "https://tenant.example" {
		t.Fatalf("unexpected credentials: token=%q url=%v", cfg.Token, cfg.BaseURL)
	}
	if cfg.APIPrefix != "/api/v1" || cfg.CacheTTL != time.Minute || cfg.MaxConcurrency != 5 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestProcessEnvironmentWinsOverDotEnv(t *testing.T) {
	tmp := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	if err := os.WriteFile(filepath.Join(tmp, ".env"), []byte("KAITEN_API_TOKEN=file-token\nKAITEN_URL=https://file.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KAITEN_API_TOKEN", "process-token")
	t.Setenv("KAITEN_URL", "https://process.example")
	cfg, err := Load(true, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "process-token" || cfg.BaseURL.Host != "process.example" || cfg.DotEnvPath == "" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestInvalidValuesFail(t *testing.T) {
	t.Setenv("KAITEN_API_TOKEN", "x")
	t.Setenv("KAITEN_URL", "https://user:pass@example.test?q=1")
	if _, err := Load(true, Overrides{}); err == nil {
		t.Fatal("expected URL validation error")
	}
	t.Setenv("KAITEN_URL", "https://example.test")
	t.Setenv("KAITEN_MAX_CONCURRENCY", "0")
	if _, err := Load(true, Overrides{}); err == nil {
		t.Fatal("expected concurrency validation error")
	}
}

func TestOverridesTakePriority(t *testing.T) {
	t.Setenv("KAITEN_MCP_TRANSPORT", "stdio")
	t.Setenv("KAITEN_MCP_PORT", "8000")
	cfg, err := Load(false, Overrides{Transport: "streamable-http", Port: 9100, Path: "rpc"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPTransport != "streamable-http" || cfg.MCPPort != 9100 || cfg.MCPPath != "/rpc" {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}
