package config

import "testing"

func TestV8MigrationPreservesLocalExtensions(t *testing.T) {
	legacy := []byte(`
enable-gemini-cli-endpoint: true
success-request-log: true
success-logs-max-files: 17
proxy-gateway:
  enabled: true
  port: 18899
codex:
  identity-confuse: true
  turn-state:
    enabled: true
    probe:
      auth-source: external
claude:
  synthetic-device-id:
    enabled: true
oauth-auth-model-alias:
  codex:
    - plan-type: plus
      aliases:
        - name: gpt-5.5
          alias: local-model
`)
	migrated, _, err := NormalizeConfigLayout(legacy, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateV8Config(migrated); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableGeminiCLIEndpoint || !cfg.SuccessRequestLog || cfg.SuccessLogsMaxFiles != 17 {
		t.Fatal("endpoint or logging settings were lost")
	}
	if !cfg.ProxyGateway.Enabled || cfg.ProxyGateway.Port != 18899 || !cfg.Codex.IdentityConfuse || !cfg.Codex.TurnState.Enabled || cfg.Codex.TurnState.Probe.AuthSource != "external" {
		t.Fatal("gateway or Codex settings were lost")
	}
	if !cfg.Claude.SyntheticDeviceID.Enabled || len(cfg.OAuthAuthModelAlias["codex"]) != 1 {
		t.Fatal("identity or account alias settings were lost")
	}
}
