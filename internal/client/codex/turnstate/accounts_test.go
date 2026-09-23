package turnstate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestProbeAccountSelection(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{"first", "selected", "disabled", "expired", "cooling"} {
		a := &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "secret-" + id, "account_id": "account-" + id}}
		if id == "disabled" {
			a.Disabled = true
		}
		if id == "expired" {
			a.Metadata["expired"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
		}
		if id == "cooling" {
			a.Status = coreauth.StatusError
			a.Unavailable = true
			a.NextRetryAfter = time.Now().Add(time.Hour)
		}
		if _, err := m.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	token, id, account, err := ResolveProbeAccount(m, "selected")
	if err != nil || token != "secret-selected" || id != "selected" || account != "account-selected" {
		t.Fatalf("unexpected selected account: %s / %s, error %v", id, account, err)
	}
	for _, id := range []string{"", "missing", "disabled"} {
		if token, _, _, err := ResolveProbeAccount(m, id); err == nil || token != "" {
			t.Fatalf("unexpected fallback for %q", id)
		}
	}
	if token, id, _, err := ResolveProbeAccount(m, "cooling"); err != nil || token != "secret-cooling" || id != "cooling" {
		t.Fatalf("enabled cooling account should remain selectable: %s %v", token, err)
	}
	listed := map[string]ProbeAccount{}
	for _, account := range ListProbeAccounts(m) {
		listed[account.ID] = account
	}
	if listed["disabled"].Available || listed["disabled"].Reason != "已禁用" {
		t.Fatalf("disabled account should stay unselectable: %+v", listed["disabled"])
	}
	if !listed["cooling"].Available || listed["cooling"].Reason != "冷却中" {
		t.Fatalf("cooling account should stay selectable: %+v", listed["cooling"])
	}
	if !listed["selected"].Available || listed["selected"].Reason != "" {
		t.Fatalf("active account should stay selectable: %+v", listed["selected"])
	}
	a, _ := m.GetByID("selected")
	a.Metadata["access_token"] = "refreshed-secret"
	if _, err := m.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if token, _, _, err := ResolveProbeAccount(m, "selected"); err != nil || token != "refreshed-secret" {
		t.Fatal("refreshed token was not used")
	}
	data, _ := json.Marshal(ListProbeAccounts(m))
	if strings.Contains(string(data), "secret") {
		t.Fatal("account list leaked credentials")
	}
}

func TestProbeSourcesAndEvents(t *testing.T) {
	for _, source := range []string{"account", "external"} {
		t.Run(source, func(t *testing.T) {
			cfg := &config.Config{Codex: config.CodexConfig{TurnState: config.CodexTurnStateConfig{MinLength: 160, Probe: config.CodexTurnStateProbeConfig{AuthSource: source, AuthID: "selected", APIKey: "external-secret", Model: "test-model"}}}}
			called := 0
			p := NewProber(NewPool(5, 160, time.Minute), nil, cfg, func(id string) (string, string, string, error) {
				called++
				if id != "selected" {
					t.Fatalf("wrong account %q", id)
				}
				return "selected-secret", id, "selected-account", nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", mockRoundTripper(func(r *http.Request) (*http.Response, error) {
				want := "Bearer external-secret"
				if source == "account" {
					want = "Bearer selected-secret"
				}
				if r.Header.Get("Authorization") != want {
					t.Fatal("incorrect credentials")
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload["store"] != false || payload["instructions"] != "" {
					t.Fatal("missing Codex payload fields")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{HeaderName: []string{strings.Repeat("X", 170)}}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}))
			if _, err := p.ExecuteProbe(ctx, ""); err != nil {
				t.Fatal(err)
			}
			if (source == "account" && called != 1) || (source == "external" && called != 0) {
				t.Fatal("credential sources were mixed")
			}
			events, _ := p.events.snapshot()
			if len(events) != 1 || events[0].Outcome != "success" || events[0].Source != source || events[0].Length != 170 {
				t.Fatalf("incorrect events: %+v", events)
			}
			data, _ := json.Marshal(events)
			if strings.Contains(string(data), "secret") || strings.Contains(string(data), strings.Repeat("X", 170)) {
				t.Fatal("events leaked secret material")
			}
		})
	}
}

func TestProbeDoesNotAutomaticallyChooseAccount(t *testing.T) {
	cfg := &config.Config{}
	p := NewProber(NewPool(5, 160, time.Minute), nil, cfg, func(string) (string, string, string, error) {
		t.Fatal("automatic credential selection")
		return "", "", "", nil
	})
	if _, err := p.ExecuteProbe(context.Background(), ""); err == nil {
		t.Fatal("expected selection error")
	}
	events, _ := p.events.snapshot()
	probed, success, failed := p.Metrics()
	if len(events) != 1 || events[0].Outcome != "failed" || probed != 1 || success != 0 || failed != 1 {
		t.Fatal("missing failed probe record")
	}
}

func TestProbeNetworkErrorIncludesCauseAndRedactsCredentials(t *testing.T) {
	cfg := &config.Config{Codex: config.CodexConfig{TurnState: config.CodexTurnStateConfig{Probe: config.CodexTurnStateProbeConfig{
		AuthSource: "external", APIKey: "external-secret", BaseURL: "https://chatgpt.com/backend-api/codex", Model: "test-model",
	}}}}
	p := NewProber(NewPool(5, 160, time.Minute), nil, cfg, nil)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", mockRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("proxyconnect tcp: dial via http://user:proxy-password@192.0.2.10:8080: connection refused")
	}))
	_, err := p.ExecuteProbe(ctx, "")
	if err == nil || !strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), "192.0.2.10:8080") {
		t.Fatalf("expected detailed network error, got %v", err)
	}
	if strings.Contains(err.Error(), "proxy-password") || strings.Contains(err.Error(), "user:") {
		t.Fatalf("network error leaked proxy credentials: %v", err)
	}
	events, _ := p.events.snapshot()
	if len(events) != 1 || events[0].Message != err.Error() {
		t.Fatalf("detailed network error was not recorded: %+v", events)
	}
}

func TestInjectionEvents(t *testing.T) {
	pool := NewPool(5, 160, time.Minute)
	m := &Manager{pool: pool, prober: NewProber(pool, nil, nil, nil)}
	cfg := &config.Config{Codex: config.CodexConfig{TurnState: config.CodexTurnStateConfig{Enabled: true}}}
	m.InjectRequestHeader(http.Header{}, cfg)
	h := http.Header{HeaderName: []string{strings.Repeat("C", 170)}}
	m.InjectRequestHeader(h, cfg)
	pool.Feed(strings.Repeat("S", 180), "http://user:secret@proxy", "selected", "model")
	h = http.Header{}
	m.InjectRequestHeader(h, cfg)
	events, skipped := m.prober.events.snapshot()
	if skipped != 2 || len(events) != 3 || events[0].Outcome != "success" || events[0].AuthID != "selected" || h.Get(HeaderName) != strings.Repeat("S", 180) {
		t.Fatalf("incorrect injection events: %+v", events)
	}
	data, _ := json.Marshal(pool.RecentSummaries(5))
	if strings.Contains(string(data), "secret") {
		t.Fatal("ticket summary leaked proxy password")
	}
	for i := 0; i < 110; i++ {
		m.prober.events.add(Event{Kind: "probe"})
	}
	events, _ = m.prober.events.snapshot()
	if len(events) != 100 {
		t.Fatal("event history is not bounded")
	}
}
