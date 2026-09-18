package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/turnstate"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestManagement_GetCodexTurnState(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		Codex: config.CodexConfig{
			TurnState: config.CodexTurnStateConfig{
				Enabled:   true,
				MinLength: 160,
				Probe: config.CodexTurnStateProbeConfig{
					MinSpare:    10,
					MaxPoolSize: 100,
					Proxies:     []string{"http://1.1.1.1:8080", "http://[2001:db8::1]:8080"},
				},
			},
		},
	}

	mgr := turnstate.GetManager()
	mgr.UpdateConfig(cfg)
	mgr.Pool().Push(&turnstate.Ticket{
		State:      strings.Repeat("M", 170),
		Length:     170,
		ExpiresAt:  time.Now().Add(10 * time.Minute),
		AcquiredAt: time.Now(),
		Proxy:      "http://1.1.1.1:8080",
	})

	handler := &Handler{cfg: cfg}
	router := gin.New()
	router.GET("/management/codex/turn-state", handler.GetCodexTurnState)

	req := httptest.NewRequest(http.MethodGet, "/management/codex/turn-state", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}

	var stats turnstate.Stats
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !stats.Enabled {
		t.Fatalf("expected stats.Enabled true")
	}
	if stats.MinSpare != 10 {
		t.Fatalf("min_spare = %d, want 10", stats.MinSpare)
	}
	if stats.MaxPoolSize != 100 {
		t.Fatalf("max_pool_size = %d, want 100", stats.MaxPoolSize)
	}
	if stats.ProxyCount != 2 {
		t.Fatalf("proxy_count = %d, want 2", stats.ProxyCount)
	}
	if stats.PoolSize < 1 {
		t.Fatalf("pool_size = %d, want >= 1", stats.PoolSize)
	}
	if len(stats.RecentTickets) < 1 {
		t.Fatalf("expected at least 1 recent ticket summary")
	}
}
