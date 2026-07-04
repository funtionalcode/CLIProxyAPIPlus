package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type servingHighWaterStore struct {
	mu        sync.Mutex
	saveCount atomic.Int32
	lastSaved *cliproxyauth.Auth
}

func (s *servingHighWaterStore) List(context.Context) ([]*cliproxyauth.Auth, error) {
	return nil, nil
}

func (s *servingHighWaterStore) Save(_ context.Context, auth *cliproxyauth.Auth) (string, error) {
	s.saveCount.Add(1)
	s.mu.Lock()
	s.lastSaved = auth
	s.mu.Unlock()
	return "", nil
}

func (s *servingHighWaterStore) Delete(context.Context, string) error { return nil }

func newServingHighWaterFixture(t *testing.T, serverURL string) (*ClaudeExecutor, *cliproxyauth.Auth, *servingHighWaterStore, *cliproxyauth.Manager) {
	t.Helper()
	resetClaudeDeviceProfileCache()

	store := &servingHighWaterStore{}
	mgr := cliproxyauth.NewManager(store, nil, nil)

	const authID = "claude-serving-hw-1"
	registered := &cliproxyauth.Auth{
		ID:       authID,
		Provider: "claude",
		Metadata: map[string]any{"type": "claude"},
		Attributes: map[string]string{
			"api_key":  "key-serving-hw",
			"base_url": serverURL,
		},
	}
	if _, err := mgr.Register(context.Background(), registered); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	executor := NewClaudeExecutorWithManager(&config.Config{AuthDir: t.TempDir()}, mgr)
	servingAuth := &cliproxyauth.Auth{
		ID:       authID,
		ProxyURL: "direct",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "key-serving-hw",
			"base_url": serverURL,
		},
	}
	return executor, servingAuth, store, mgr
}

func versionedInboundHeaders(version string) http.Header {
	h := http.Header{}
	h.Set("User-Agent", "claude-cli/"+version+" (external, cli)")
	h.Set("X-Stainless-Package-Version", "0.80.0")
	h.Set("X-Stainless-Runtime-Version", "v24.6.0")
	h.Set("X-Stainless-Os", "MacOS")
	h.Set("X-Stainless-Arch", "arm64")
	return h
}

func assertServingHighWaterPersisted(t *testing.T, mgr *cliproxyauth.Manager, store *servingHighWaterStore, authID, wantVersion string) {
	t.Helper()

	stored, ok := mgr.GetByID(authID)
	if !ok {
		t.Fatalf("auth %q not found after serving request", authID)
	}
	hw, ok := cliproxyauth.ClaudeDeviceHighWaterFromMetadata(stored.Metadata)
	if !ok {
		t.Fatalf("claude_device_high_water not written to auth.Metadata after serving request: metadata=%#v", stored.Metadata)
	}
	if hw.Version != wantVersion {
		t.Fatalf("persisted high-water version = %q, want %q", hw.Version, wantVersion)
	}
	if store.saveCount.Load() == 0 {
		t.Fatalf("expected at least one Save after serving request, got 0")
	}
	store.mu.Lock()
	saved := store.lastSaved
	store.mu.Unlock()
	if saved == nil {
		t.Fatal("store captured no saved auth after serving request")
	}
	savedHW, ok := cliproxyauth.ClaudeDeviceHighWaterFromMetadata(saved.Metadata)
	if !ok || savedHW.Version != wantVersion {
		t.Fatalf("persisted snapshot high-water mismatch: ok=%v version=%q want=%q", ok, savedHW.Version, wantVersion)
	}
}

func TestClaudeExecutorExecutePersistsDeviceHighWaterFromServingPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor, auth, store, mgr := newServingHighWaterFixture(t, server.URL)

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Headers:      versionedInboundHeaders("2.5.0"),
	}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	assertServingHighWaterPersisted(t, mgr, store, auth.ID, "2.5.0")
}
