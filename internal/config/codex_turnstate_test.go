package config

import (
	"testing"
	"time"
)

func TestLoadConfig_CodexTurnState(t *testing.T) {
	raw := []byte(`
codex:
  turn-state:
    enabled: true
    force-inject: true
    min-length: 180
    probe:
      enabled: true
      interval: 5s
      min-spare: 8
      max-pool-size: 80
      ticket-ttl: 20m
      proxies:
        - "http://[2001:db8::1]:8080"
        - "socks5://user:pass@127.0.0.1:1080"
      prompt: "ping-test"
      model: "gpt-5-codex"
      concurrency: 4
      base-url: "https://custom.chatgpt.com"
`)

	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}

	ts := cfg.Codex.TurnState
	if !ts.Enabled {
		t.Fatalf("expected turn-state enabled")
	}
	if !ts.IsInjectBusiness() {
		t.Fatalf("expected IsInjectBusiness to default to true when Enabled")
	}
	if !ts.ForceInject {
		t.Fatalf("expected force-inject true")
	}
	if ts.MinLength != 180 {
		t.Fatalf("min-length = %d, want 180", ts.MinLength)
	}

	probe := ts.Probe
	if !probe.Enabled || !probe.IsEnabled(ts.Enabled) {
		t.Fatalf("probe expected enabled")
	}
	if probe.Interval != 5*time.Second {
		t.Fatalf("probe interval = %v, want 5s", probe.Interval)
	}
	if probe.MinSpare != 8 {
		t.Fatalf("min-spare = %d, want 8", probe.MinSpare)
	}
	if probe.MaxPoolSize != 80 {
		t.Fatalf("max-pool-size = %d, want 80", probe.MaxPoolSize)
	}
	if probe.TicketTTL != 20*time.Minute {
		t.Fatalf("ticket-ttl = %v, want 20m", probe.TicketTTL)
	}
	if len(probe.Proxies) != 2 {
		t.Fatalf("proxies len = %d, want 2", len(probe.Proxies))
	}
	if probe.Proxies[0] != "http://[2001:db8::1]:8080" {
		t.Fatalf("proxies[0] = %q", probe.Proxies[0])
	}
	if probe.Prompt != "ping-test" {
		t.Fatalf("prompt = %q, want ping-test", probe.Prompt)
	}
	if probe.Model != "gpt-5-codex" {
		t.Fatalf("model = %q, want gpt-5-codex", probe.Model)
	}
	if probe.Concurrency != 4 {
		t.Fatalf("concurrency = %d, want 4", probe.Concurrency)
	}
	if probe.BaseURL != "https://custom.chatgpt.com" {
		t.Fatalf("base-url = %q", probe.BaseURL)
	}
}

func TestLoadConfig_CodexTurnState_Defaults(t *testing.T) {
	raw := []byte(`
codex:
  turn-state:
    enabled: true
    probe:
      proxies:
        - "http://[2001:db8::1]:8080"
`)

	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}

	ts := cfg.Codex.TurnState
	if !ts.Enabled {
		t.Fatalf("expected turn-state enabled")
	}
	if !ts.IsInjectBusiness() {
		t.Fatalf("expected IsInjectBusiness to default to true")
	}
	if ts.ForceInject {
		t.Fatalf("expected default force-inject false")
	}
	if ts.MinLength != 160 {
		t.Fatalf("min-length default = %d, want 160", ts.MinLength)
	}

	probe := ts.Probe
	if !probe.IsEnabled(ts.Enabled) {
		t.Fatalf("probe expected enabled when proxies configured")
	}
	if probe.Interval != 10*time.Second {
		t.Fatalf("probe interval default = %v, want 10s", probe.Interval)
	}
	if probe.MinSpare != 5 {
		t.Fatalf("min-spare default = %d, want 5", probe.MinSpare)
	}
	if probe.MaxPoolSize != 50 {
		t.Fatalf("max-pool-size default = %d, want 50", probe.MaxPoolSize)
	}
	if probe.TicketTTL != 15*time.Minute {
		t.Fatalf("ticket-ttl default = %v, want 15m", probe.TicketTTL)
	}
	if probe.Prompt != "ping" {
		t.Fatalf("prompt default = %q, want ping", probe.Prompt)
	}
	if probe.Model != "gpt-5.3-codex" {
		t.Fatalf("model default = %q, want gpt-5.3-codex", probe.Model)
	}
	if probe.Concurrency != 2 {
		t.Fatalf("concurrency default = %d, want 2", probe.Concurrency)
	}
}
