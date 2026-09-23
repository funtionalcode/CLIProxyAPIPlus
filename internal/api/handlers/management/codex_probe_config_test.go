package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexProxyExternalAccountLoginFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var relayCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer external-secret" {
			t.Fatal("external API key was not forwarded")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth/status":
			_, _ = w.Write([]byte(`{"authenticated":true,"pool":{"total":2,"active":1}}`))
		case "/auth/login-start":
			_, _ = w.Write([]byte(`{"authUrl":"https://auth.openai.com/oauth/authorize?state=test-state-1234567890","state":"test-state-1234567890"}`))
		case "/auth/code-relay":
			var body struct {
				CallbackURL string `json:"callbackUrl"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.CallbackURL != "http://localhost:1455/auth/callback?code=test-code&scope=openid&state=test-state-1234567890" {
				t.Fatalf("unexpected callback URL %q", body.CallbackURL)
			}
			relayCalls++
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/auth/device-login":
			_, _ = w.Write([]byte(`{"userCode":"ABCD-EFGH","verificationUri":"https://auth.openai.com/codex/device","verificationUriComplete":"https://auth.openai.com/codex/device?user_code=ABCD-EFGH","deviceCode":"device-code-123456","expiresIn":900,"interval":5}`))
		case "/auth/device-poll/device-code-123456":
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	h := &Handler{cfg: &config.Config{Codex: config.CodexConfig{TurnState: config.CodexTurnStateConfig{Probe: config.CodexTurnStateProbeConfig{
		AuthSource: "external", BaseURL: upstream.URL + "/v1", APIKey: "external-secret",
	}}}}}
	r := gin.New()
	r.GET("/account", h.GetCodexProxyExternalAccount)
	r.POST("/login/start", h.StartCodexProxyExternalLogin)
	r.POST("/login/complete", h.CompleteCodexProxyExternalLogin)
	r.POST("/device/start", h.StartCodexProxyExternalDeviceLogin)
	r.POST("/device/poll", h.PollCodexProxyExternalDeviceLogin)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}

	w := request(http.MethodGet, "/account", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"total_accounts":2`) || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("unexpected status response: %d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodPost, "/login/start", `{}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "https://auth.openai.com/") || !strings.Contains(w.Body.String(), `"state":"test-state-1234567890"`) {
		t.Fatalf("unexpected login start response: %d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodPost, "/login/complete", `{"callback_url":"https://attacker.example/callback?code=x&state=y"}`)
	if w.Code != http.StatusBadRequest || relayCalls != 0 {
		t.Fatal("unsafe callback URL was accepted")
	}
	w = request(http.MethodPost, "/login/complete", `{"callback_url":"http://localhost:1455/auth/callback?code=test-code&scope=openid","state":"test-state-1234567890"}`)
	if w.Code != http.StatusOK || relayCalls != 1 {
		t.Fatalf("valid callback was not relayed: %d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodPost, "/device/start", `{}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"user_code":"ABCD-EFGH"`) || strings.Contains(w.Body.String(), "external-secret") {
		t.Fatalf("device login start failed: %d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodPost, "/device/poll", `{"device_code":"device-code-123456"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("device login poll failed: %d %s", w.Code, w.Body.String())
	}
}

func TestExternalProxyErrorIncludesSafeDetail(t *testing.T) {
	err := externalProxyError(http.StatusBadRequest, strings.NewReader(`{"error":"URL must contain state; code=sensitive-code"}`))
	if err == nil || !strings.Contains(err.Error(), "URL must contain state") || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("missing external error detail: %v", err)
	}
	if strings.Contains(err.Error(), "sensitive-code") {
		t.Fatalf("external error leaked OAuth code: %v", err)
	}
}

func TestCodexProbeConfigSaveAndIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	_, err := manager.Register(context.Background(), &coreauth.Auth{ID: "probe-account", Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "oauth-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Codex: config.CodexConfig{TurnState: config.CodexTurnStateConfig{Probe: config.CodexTurnStateProbeConfig{APIKey: "external-secret", Proxies: []string{"http://proxy:8898"}}}}}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("codex: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path, authManager: manager}
	r := gin.New()
	r.GET("/config", h.GetCodexProbeConfig)
	r.PUT("/config", h.PutCodexProbeConfig)
	request := func(method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	w := request(http.MethodPut, `{"auth_source":"account","auth_id":"probe-account","model":"test-model"}`)
	if w.Code != 200 {
		t.Fatalf("save failed: %s", w.Body.String())
	}
	if cfg.Codex.TurnState.Enabled || cfg.Codex.TurnState.Probe.APIKey != "external-secret" || len(cfg.Codex.TurnState.Probe.Proxies) != 1 {
		t.Fatal("unrelated settings changed")
	}
	loaded, err := config.LoadConfig(path)
	if err != nil || loaded.Codex.TurnState.Probe.AuthID != "probe-account" {
		t.Fatalf("selection did not persist: %v", err)
	}
	w = request(http.MethodGet, "")
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("config API leaked credentials")
	}
	w = request(http.MethodPut, `{"auth_source":"account","auth_id":"missing","model":"test-model"}`)
	if w.Code != 400 || cfg.Codex.TurnState.Probe.AuthID != "probe-account" {
		t.Fatal("invalid selection overwrote configuration")
	}
	w = request(http.MethodPut, `{"auth_source":"external","base_url":"http://127.0.0.1:18318/v1","model":"test-model"}`)
	if w.Code != 200 || cfg.Codex.TurnState.Probe.AuthSource != "external" || cfg.Codex.TurnState.Probe.APIKey != "external-secret" {
		t.Fatal("external source configuration failed")
	}
	h.configFilePath = filepath.Join(t.TempDir(), "missing", "config.yaml")
	w = request(http.MethodPut, `{"auth_source":"account","auth_id":"probe-account","model":"another-model"}`)
	if w.Code != 500 || cfg.Codex.TurnState.Probe.AuthSource != "external" {
		t.Fatal("failed save did not roll back")
	}
}
