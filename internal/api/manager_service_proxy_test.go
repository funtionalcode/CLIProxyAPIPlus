package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestManagerServiceProxyRoutesForwardManagerOnlyPaths(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"path":   r.URL.Path,
			"method": r.Method,
		})
	}))
	defer upstream.Close()

	t.Setenv("CPA_MANAGER_PROXY_BASE_URL", upstream.URL)

	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/usage-service/info", nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("usage-service info status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"/usage-service/info"`) {
		t.Fatalf("usage-service info body = %s, want proxied path", rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/model-prices", nil)
	rr = httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("model-prices status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"/v0/management/model-prices"`) {
		t.Fatalf("model-prices body = %s, want proxied path", rr.Body.String())
	}

	if upstreamCalls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", upstreamCalls.Load())
	}
}

func TestManagerServiceProxyDoesNotCaptureCoreManagementRoutes(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()

	t.Setenv("CPA_MANAGER_PROXY_BASE_URL", upstream.URL)
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("config status = %d, want %d body=%s", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls.Load())
	}
}

func TestManagerServiceProxyHeaderCanForwardConflictingUsagePath(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path})
	}))
	defer upstream.Close()

	t.Setenv("CPA_MANAGER_PROXY_BASE_URL", upstream.URL)
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("usage without proxy header status = %d, want %d body=%s", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream calls after plain usage = %d, want 0", upstreamCalls.Load())
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)
	req.Header.Set("X-CPA-Manager-Service", "true")
	rr = httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("usage with proxy header status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"/v0/management/usage"`) {
		t.Fatalf("usage body = %s, want proxied usage path", rr.Body.String())
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream calls after proxy usage = %d, want 1", upstreamCalls.Load())
	}
}
