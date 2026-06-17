package config

import "testing"

func TestParseConfigBytes_RoutingWeighted(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
routing:
  strategy: round-robin
  weighted: true
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if !cfg.Routing.Weighted {
		t.Fatalf("Routing.Weighted = false, want true")
	}
}
