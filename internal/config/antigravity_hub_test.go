package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAntigravityHubConfigRoundTrip(t *testing.T) {
	for _, raw := range []string{
		"antigravity:\n  device-profile: {enabled: false}\n  system-prompt: {enabled: false, text: 'Custom {os}/{arch}', text-mode: protected, as-user: true, client-filter: {enabled: true, builtins: false, mode: rewrite, rules: {Codex: Antigravity}}}\n  telemetry: {mode: preview}\n",
		"config-version: 8\noauth:\n  providers:\n    antigravity:\n      device-profile: {enabled: false}\n      system-prompt: {enabled: false, text: 'Custom {os}/{arch}', text-mode: protected, as-user: true, client-filter: {enabled: true, builtins: false, mode: rewrite, rules: {Codex: Antigravity}}}\n      telemetry: {mode: preview}\n",
	} {
		cfg, errParse := ParseConfigBytes([]byte(raw))
		if errParse != nil {
			t.Fatal(errParse)
		}
		check := func(c *Config) {
			t.Helper()
			if c.Antigravity.DeviceProfile.Enabled == nil || *c.Antigravity.DeviceProfile.Enabled || c.Antigravity.SystemPrompt.Enabled == nil || *c.Antigravity.SystemPrompt.Enabled || c.Antigravity.SystemPrompt.Text != "Custom {os}/{arch}" || c.Antigravity.Telemetry.EffectiveMode() != "preview" {
				t.Fatalf("Antigravity settings lost: %#v", c.Antigravity)
			}
			prompt := c.Antigravity.SystemPrompt
			filter := prompt.ClientFilter
			if prompt.TextMode != "protected" || !prompt.AsUser || filter.Enabled == nil || !*filter.Enabled || filter.Builtins == nil || *filter.Builtins || filter.Mode != "rewrite" || filter.Rules["Codex"] != "Antigravity" {
				t.Fatalf("prompt processing settings lost: %#v", prompt)
			}
		}
		check(cfg)
		clone := cfg.CloneForRuntime()
		*cfg.Antigravity.SystemPrompt.Enabled = true
		*cfg.Antigravity.DeviceProfile.Enabled = true
		cfg.Antigravity.SystemPrompt.ClientFilter.Rules["Codex"] = "changed"
		check(clone)
		path := filepath.Join(t.TempDir(), "config.yaml")
		if errWrite := os.WriteFile(path, []byte(raw), 0600); errWrite != nil {
			t.Fatal(errWrite)
		}
		if errSave := SaveConfigPreserveComments(path, clone); errSave != nil {
			t.Fatal(errSave)
		}
		reloaded, errLoad := LoadConfig(path)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		check(reloaded)
	}
}

func TestAntigravityTelemetryModeIsExplicit(t *testing.T) {
	for input, want := range map[string]string{"": "preview", "preview": "preview", "send": "send", " SEND ": "send", "off": "off", "true": "off", "invalid": "off"} {
		if got := (AntigravityTelemetryConfig{Mode: input}).EffectiveMode(); got != want {
			t.Errorf("mode %q = %q, want %q", input, got, want)
		}
	}
}
