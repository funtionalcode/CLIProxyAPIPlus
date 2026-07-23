package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type streamRetryAfterStatusErr struct {
	retryAfter time.Duration
}

func (e streamRetryAfterStatusErr) Error() string {
	return "usage limit reached"
}

func (e streamRetryAfterStatusErr) StatusCode() int {
	return http.StatusTooManyRequests
}

func (e streamRetryAfterStatusErr) RetryAfter() *time.Duration {
	d := e.retryAfter
	return &d
}

func TestWrapStreamResultPreservesRetryAfterForCooldown(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "codex-free", Provider: "codex"}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	retryAfter := 2 * time.Minute
	remaining := make(chan cliproxyexecutor.StreamChunk, 1)
	remaining <- cliproxyexecutor.StreamChunk{Err: streamRetryAfterStatusErr{retryAfter: retryAfter}}
	close(remaining)

	result := m.wrapStreamResult(context.Background(), auth, "codex", "gpt-5.4-mini", nil, nil, remaining, OAuthModelAliasResult{}, false)
	for range result.Chunks {
	}

	updated, ok := m.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth %q missing after stream failure", auth.ID)
	}
	state := updated.ModelStates["gpt-5.4-mini"]
	if state == nil {
		t.Fatal("model state missing after stream failure")
	}
	diff := time.Until(state.NextRetryAfter)
	if diff < retryAfter-5*time.Second || diff > retryAfter+5*time.Second {
		t.Fatalf("NextRetryAfter diff = %v, want about %v", diff, retryAfter)
	}
}
