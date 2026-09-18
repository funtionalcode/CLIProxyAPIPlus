package management

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/proxygateway"
)

func TestManagement_ProxyGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		ProxyGateway: config.ProxyGatewayConfig{
			Enabled: false,
			Bind:    "127.0.0.1",
			Port:    18899,
			Pools: []config.ProxyPoolConfig{
				{
					Name:    "TestPool1",
					Enabled: true,
					Proxies: []string{"http://1.1.1.1:8080"},
				},
			},
		},
	}

	handler := &Handler{cfg: cfg}
	router := gin.New()
	router.GET("/management/proxy-gateway", handler.GetProxyGateway)
	router.PUT("/management/proxy-gateway", handler.PutProxyGateway)
	router.POST("/management/proxy-gateway/test", handler.TestProxyGatewayProxy)

	// 1. GET status
	req := httptest.NewRequest(http.MethodGet, "/management/proxy-gateway", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}

	var stats proxygateway.GatewayStats
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if stats.Port != 8899 && stats.Port != 18899 {
		t.Fatalf("unexpected stats.Port: %d", stats.Port)
	}

	// 2. Test proxy connectivity endpoint with invalid proxy
	testPayload, _ := json.Marshal(map[string]string{
		"proxy_url": "http://127.0.0.1:59999",
	})
	reqTest := httptest.NewRequest(http.MethodPost, "/management/proxy-gateway/test", bytes.NewReader(testPayload))
	reqTest.Header.Set("Content-Type", "application/json")
	recTest := httptest.NewRecorder()
	router.ServeHTTP(recTest, reqTest)

	if recTest.Code != http.StatusOK {
		t.Fatalf("POST test status = %d, want 200", recTest.Code)
	}
	var testRes proxygateway.TestResult
	if err := json.Unmarshal(recTest.Body.Bytes(), &testRes); err != nil {
		t.Fatalf("unmarshal test result error: %v", err)
	}
	if testRes.Success {
		t.Fatalf("expected failure for non-existent proxy")
	}
}

func TestManagementProxyGatewayPreservesOmittedCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte("{}\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	handler := &Handler{configFilePath: path, cfg: &config.Config{ProxyGateway: config.ProxyGatewayConfig{AuthUser: "saved-user", AuthPass: "saved-password"}}}
	router := gin.New()
	router.PUT("/gateway", handler.PutProxyGateway)
	for _, tc := range []struct {
		body string
		user string
		pass string
	}{
		{`{"enabled":false,"pools":[]}`, "saved-user", "saved-password"},
		{`{"enabled":false,"auth-user":"","auth-pass":""}`, "", ""},
	} {
		req := httptest.NewRequest(http.MethodPut, "/gateway", bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || handler.cfg.ProxyGateway.AuthUser != tc.user || handler.cfg.ProxyGateway.AuthPass != tc.pass {
			t.Fatalf("认证配置未按请求保留或清空: status=%d", rec.Code)
		}
	}
}

func TestManagementProxyGatewayRejectsOccupiedPort(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	gateway := proxygateway.GetGateway()
	_ = gateway.UpdateConfig(config.ProxyGatewayConfig{Enabled: false})
	handler := &Handler{cfg: &config.Config{ProxyGateway: config.ProxyGatewayConfig{Enabled: false}}}
	router := gin.New()
	router.PUT("/gateway", handler.PutProxyGateway)
	body, _ := json.Marshal(config.ProxyGatewayConfig{Enabled: true, Bind: "127.0.0.1", Port: port})
	req := httptest.NewRequest(http.MethodPut, "/gateway", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || handler.cfg.ProxyGateway.Enabled || gateway.Stats().Enabled {
		t.Fatalf("监听失败时不应保存或返回成功: status=%d", rec.Code)
	}
}

func TestManagementProxyGatewayTestTargets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	router := gin.New()
	router.POST("/test", handler.TestProxyGatewayProxy)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer source.Close()
	for _, tc := range []struct {
		body   string
		status int
		stage  string
	}{
		{`{}`, 400, ""},
		{`{"proxy_url":"http://localhost:1","gateway":true}`, 400, ""},
		{`{"proxy_url":"http://localhost:1","proxy_url_source":"https://example.test"}`, 400, ""},
		{`{"gateway":true}`, 200, "gateway"},
		{`{"proxy_url_source":"` + source.URL + `?token=hidden"}`, 200, "source"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("status %d want %d", rec.Code, tc.status)
		}
		if tc.stage != "" {
			var result proxygateway.TestResult
			if errDecode := json.Unmarshal(rec.Body.Bytes(), &result); errDecode != nil {
				t.Fatal(errDecode)
			}
			if result.Stage != tc.stage {
				t.Fatalf("stage %s want %s", result.Stage, tc.stage)
			}
		}
	}
}
