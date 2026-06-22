package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_ClaudeHeaderDefaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
claude-header-defaults:
  user-agent: "  claude-cli/2.1.70 (external, cli)  "
  package-version: "  0.80.0  "
  runtime-version: "  v24.5.0  "
  os: "  MacOS  "
  arch: "  arm64  "
  timeout: "  900  "
  stabilize-device-profile: false
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if got := cfg.ClaudeHeaderDefaults.UserAgent; got != "claude-cli/2.1.70 (external, cli)" {
		t.Fatalf("UserAgent = %q, want %q", got, "claude-cli/2.1.70 (external, cli)")
	}
	if got := cfg.ClaudeHeaderDefaults.PackageVersion; got != "0.80.0" {
		t.Fatalf("PackageVersion = %q, want %q", got, "0.80.0")
	}
	if got := cfg.ClaudeHeaderDefaults.RuntimeVersion; got != "v24.5.0" {
		t.Fatalf("RuntimeVersion = %q, want %q", got, "v24.5.0")
	}
	if got := cfg.ClaudeHeaderDefaults.OS; got != "MacOS" {
		t.Fatalf("OS = %q, want %q", got, "MacOS")
	}
	if got := cfg.ClaudeHeaderDefaults.Arch; got != "arm64" {
		t.Fatalf("Arch = %q, want %q", got, "arm64")
	}
	if got := cfg.ClaudeHeaderDefaults.Timeout; got != "900" {
		t.Fatalf("Timeout = %q, want %q", got, "900")
	}
	if cfg.ClaudeHeaderDefaults.StabilizeDeviceProfile == nil {
		t.Fatal("StabilizeDeviceProfile = nil, want non-nil")
	}
	if got := *cfg.ClaudeHeaderDefaults.StabilizeDeviceProfile; got {
		t.Fatalf("StabilizeDeviceProfile = %v, want false", got)
	}
}

func TestLoadConfigOptional_ClaudeIdentityControls(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
claude:
  synthetic-device-id:
    enabled: true
    salt: "  secret-salt  "
  normalize-environment:
    enabled: true
    home: "  /Users/claude-a  "
    cwd: "  /Users/claude-a/project  "
    user: "  claude-a  "
  tls:
    http1-only: true
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional returned error: %v", err)
	}
	if !cfg.Claude.SyntheticDeviceID.Enabled {
		t.Fatal("synthetic-device-id.enabled = false, want true")
	}
	if got := cfg.Claude.SyntheticDeviceID.Salt; got != "secret-salt" {
		t.Fatalf("synthetic-device-id.salt = %q, want trimmed value", got)
	}
	if !cfg.Claude.NormalizeEnvironment.Enabled {
		t.Fatal("normalize-environment.enabled = false, want true")
	}
	if got := cfg.Claude.NormalizeEnvironment.Home; got != "/Users/claude-a" {
		t.Fatalf("normalize-environment.home = %q, want trimmed value", got)
	}
	if got := cfg.Claude.NormalizeEnvironment.CWD; got != "/Users/claude-a/project" {
		t.Fatalf("normalize-environment.cwd = %q, want trimmed value", got)
	}
	if got := cfg.Claude.NormalizeEnvironment.User; got != "claude-a" {
		t.Fatalf("normalize-environment.user = %q, want trimmed value", got)
	}
	if !cfg.Claude.TLS.HTTP1Only {
		t.Fatal("tls.http1-only = false, want true")
	}
}
