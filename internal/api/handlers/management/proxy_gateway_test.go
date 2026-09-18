package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
