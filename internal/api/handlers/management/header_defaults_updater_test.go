package management

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/headerdefaults"
)

func TestHeaderVersionsPersistReloadAndPreserveConfiguration(t *testing.T) {
	for _, v8 := range []bool{false, true} {
		name := "legacy"
		if v8 {
			name = "v8"
		}
		t.Run(name, func(t *testing.T) {
			raw := []byte(`# Keep operator comments
port: 8317
api-keys: [keep-client-key]
claude-header-defaults:
  auto-update: true
  user-agent: "claude-cli/2.1.280 (external, cli)" # Keep UA comment
  package-version: "0.94.0"
  runtime-version: "v24.3.0"
  os: MacOS
  arch: arm64
  timeout: "600"
  stabilize-device-profile: true
codex-header-defaults:
  auto-update: true
  user-agent: "codex_cli_rs/0.146.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9"
  beta-features: custom
`)
			if v8 {
				var err error
				raw, _, err = config.NormalizeConfigLayout(raw, true)
				if err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Home.Enabled = true
			h := &Handler{cfg: cfg, configFilePath: path}
			reloads := make(chan *config.Config, 1)
			h.SetConfigReloadHook(func(_ context.Context, next *config.Config) { reloads <- next })
			before, _ := os.Stat(path)
			versions := headerdefaults.Versions{Claude: "2.1.318", Package: "1.4.0", Runtime: "v26.13.0", Codex: "0.166.0"}
			if err = h.applyHeaderVersions(context.Background(), versions); err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := os.Stat(path)
			if !os.SameFile(before, after) {
				t.Fatal("replaced mounted configuration inode")
			}
			if !strings.Contains(string(saved), "# Keep operator comments") || !strings.Contains(string(saved), "# Keep UA comment") {
				t.Fatal("lost config comments")
			}
			if strings.Contains(string(saved), "config-version: 8") != v8 {
				t.Fatal("unexpected configuration migration")
			}
			next, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if next.ClaudeHeaderDefaults.UserAgent != "claude-cli/2.1.318 (external, cli)" || next.ClaudeHeaderDefaults.PackageVersion != "1.4.0" || next.ClaudeHeaderDefaults.RuntimeVersion != "v26.13.0" {
				t.Fatal("Claude software versions not saved")
			}
			if next.CodexHeaderDefaults.UserAgent != "codex_cli_rs/0.166.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9" || next.CodexHeaderDefaults.BetaFeatures != "custom" {
				t.Fatal("Codex platform or beta features changed")
			}
			if next.ClaudeHeaderDefaults.OS != "MacOS" || next.ClaudeHeaderDefaults.Arch != "arm64" || next.ClaudeHeaderDefaults.Timeout != "600" || !*next.ClaudeHeaderDefaults.StabilizeDeviceProfile || len(next.APIKeys) != 1 || next.APIKeys[0] != "keep-client-key" {
				t.Fatal("unrelated settings changed")
			}
			select {
			case runtime := <-reloads:
				if !runtime.Home.Enabled || runtime.CodexHeaderDefaults != next.CodexHeaderDefaults {
					t.Fatal("runtime reload did not preserve ownership or effective headers")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runtime not reloaded")
			}
			if err = h.applyHeaderVersions(context.Background(), versions); err != nil {
				t.Fatal(err)
			}
			unchanged, _ := os.ReadFile(path)
			if string(unchanged) != string(saved) || h.reloadGeneration != 1 {
				t.Fatal("unchanged versions rewrote config or reloaded runtime")
			}
		})
	}
}

func TestHeaderVersionsRespectConcurrentOptOutAndEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	initial := []byte("codex-header-defaults: {auto-update: true, user-agent: 'codex_cli_rs/0.1.0'}\n")
	if err := os.WriteFile(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	for _, raw := range []string{
		"codex-header-defaults: {auto-update: false, user-agent: 'manual'}\nrequest-retry: 7\n",
		"codex-header-defaults: {auto-update: true, user-agent: 'codex_cli_rs/0.1.0 changed-platform'}\nrequest-retry: 8\n",
	} {
		if err = os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err = h.applyHeaderVersions(context.Background(), headerdefaults.Versions{Codex: "0.166.0"}); err != nil {
			t.Fatal(err)
		}
		loaded, err := config.LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.CodexHeaderDefaults.AutoUpdate {
			if loaded.CodexHeaderDefaults.UserAgent != "codex_cli_rs/0.166.0 changed-platform" || loaded.RequestRetry != 8 {
				t.Fatal("overwrote concurrent edit with stale snapshot")
			}
		} else {
			data, _ := os.ReadFile(path)
			if string(data) != raw {
				t.Fatal("updated after operator disabled automatic updates")
			}
		}
	}
	before, _ := os.ReadFile(path)
	if err = h.applyHeaderVersions(context.Background(), headerdefaults.Versions{}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed/empty metadata cleared stored values")
	}
}

func TestHeaderVersionSchedule(t *testing.T) {
	var schedule headerVersionSchedule
	cfg := &config.Config{}
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	assert := func(advance time.Duration, wantClaude, wantCodex bool) {
		t.Helper()
		claude, codex := schedule.due(now.Add(advance), cfg)
		if claude != wantClaude || codex != wantCodex {
			t.Fatalf("due = %v, %v; want %v, %v", claude, codex, wantClaude, wantCodex)
		}
	}
	assert(0, false, false)
	cfg.ClaudeHeaderDefaults.AutoUpdate = true
	assert(0, true, false)
	assert(time.Second, false, false)
	cfg.CodexHeaderDefaults.AutoUpdate = true
	assert(time.Minute, false, true)
	assert(headerVersionRefreshInterval, true, false)
	assert(headerVersionRefreshInterval+time.Minute, false, true)
	cfg.ClaudeHeaderDefaults.AutoUpdate = false
	assert(headerVersionRefreshInterval+2*time.Minute, false, false)
	cfg.ClaudeHeaderDefaults.AutoUpdate = true
	assert(headerVersionRefreshInterval+3*time.Minute, true, false)
}
