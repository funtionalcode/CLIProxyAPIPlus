package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/turnstate"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexExecutor_TurnState_InjectionAndFeedback(t *testing.T) {
	highComputeTicket := strings.Repeat("T", 168)
	responseTicket := strings.Repeat("R", 175)

	var receivedTurnState string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTurnState = r.Header.Get("X-Codex-Turn-State")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Codex-Turn-State", responseTicket)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
	}))
	defer ts.Close()

	cfg := &config.Config{
		Codex: config.CodexConfig{
			TurnState: config.CodexTurnStateConfig{
				Enabled:        true,
				InjectBusiness: boolPtr(true),
				ForceInject:    true,
				MinLength:      160,
			},
		},
	}

	mgr := turnstate.GetManager()
	mgr.UpdateConfig(cfg)
	mgr.Pool().Push(&turnstate.Ticket{
		State:      highComputeTicket,
		Length:     len(highComputeTicket),
		ExpiresAt:  time.Now().Add(10 * time.Minute),
		AcquiredAt: time.Now(),
	})

	exec := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{
		ID:       "auth-test",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "test-token",
			"base_url": ts.URL,
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	}

	_, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify that the high-compute state was injected into the upstream request
	if receivedTurnState != highComputeTicket {
		t.Fatalf("upstream received turn-state = %q, want %q", receivedTurnState, highComputeTicket)
	}

	// Verify that the response ticket was fed back into the pool
	latestTicket, ok := mgr.Pool().GetTicket()
	if !ok {
		t.Fatalf("expected pool to have tickets")
	}
	if latestTicket != responseTicket && latestTicket != highComputeTicket {
		t.Fatalf("expected latest ticket in pool to be either %q or %q, got %q", responseTicket, highComputeTicket, latestTicket)
	}
}

func boolPtr(b bool) *bool {
	return &b
}
