package proxygateway

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPoolManager_RoundRobinAcrossPools(t *testing.T) {
	pools := []config.ProxyPoolConfig{
		{
			Name:    "Pool-1",
			Enabled: true,
			Proxies: []string{"http://1.1.1.1:8080", "http://1.1.1.2:8080"},
		},
		{
			Name:    "Pool-2",
			Enabled: false, // disabled pool should not be in rotation
			Proxies: []string{"http://2.2.2.2:8080"},
		},
		{
			Name:    "Pool-3",
			Enabled: true,
			Proxies: []string{"socks5://3.3.3.3:1080"},
		},
	}

	pm := NewPoolManager(pools)
	if pm.TotalProxyCount() != 3 {
		t.Fatalf("TotalProxyCount = %d, want 3", pm.TotalProxyCount())
	}

	seen := make(map[string]int)
	for i := 0; i < 6; i++ {
		p := pm.NextProxy()
		seen[p]++
	}

	if seen["http://2.2.2.2:8080"] > 0 {
		t.Fatalf("disabled pool proxy was chosen")
	}
	if seen["http://1.1.1.1:8080"] != 2 || seen["http://1.1.1.2:8080"] != 2 || seen["socks5://3.3.3.3:1080"] != 2 {
		t.Fatalf("expected round robin distribution of 2 each, got: %#v", seen)
	}
}

func TestServer_HTTPProxyForwarding(t *testing.T) {
	// Upstream target server
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom-Header", "HelloFromBackend")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend-response-ok"))
	}))
	defer backend.Close()

	// Direct pool (no upstream proxy, connects directly to backend)
	poolMgr := NewPoolManager(nil)
	server := NewServer(poolMgr, "", "")

	proxySrv := httptest.NewServer(server)
	defer proxySrv.Close()

	proxyURL, _ := url.Parse(proxySrv.URL)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get(backend.URL + "/test-path")
	if err != nil {
		t.Fatalf("client.Get error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "backend-response-ok" {
		t.Fatalf("body = %q, want %q", string(body), "backend-response-ok")
	}
	if resp.Header.Get("X-Custom-Header") != "HelloFromBackend" {
		t.Fatalf("X-Custom-Header = %q", resp.Header.Get("X-Custom-Header"))
	}
}

func TestServer_Authentication(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	poolMgr := NewPoolManager(nil)
	server := NewServer(poolMgr, "testuser", "testpass")

	proxySrv := httptest.NewServer(server)
	defer proxySrv.Close()

	// 1. Without credentials -> 407 Proxy Authentication Required
	proxyURLNoAuth, _ := url.Parse(proxySrv.URL)
	clientNoAuth := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURLNoAuth),
		},
		Timeout: 3 * time.Second,
	}
	resp, err := clientNoAuth.Get(backend.URL)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("expected status 407, got %d", resp.StatusCode)
	}

	// 2. With valid credentials -> 200 OK
	proxyURLWithAuth, _ := url.Parse(fmt.Sprintf("http://testuser:testpass@%s", proxySrv.Listener.Addr().String()))
	clientWithAuth := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURLWithAuth),
		},
		Timeout: 3 * time.Second,
	}
	respAuth, err := clientWithAuth.Get(backend.URL)
	if err != nil {
		t.Fatalf("Get with auth error: %v", err)
	}
	_ = respAuth.Body.Close()
	if respAuth.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 with auth, got %d", respAuth.StatusCode)
	}
}

func TestGateway_LifecycleAndStats(t *testing.T) {
	// Find available local port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	gw := GetGateway()
	cfg := config.ProxyGatewayConfig{
		Enabled: true,
		Bind:    "127.0.0.1",
		Port:    port,
		Pools: []config.ProxyPoolConfig{
			{
				Name:    "TestPool",
				Enabled: true,
				Proxies: []string{"http://1.2.3.4:8080"},
			},
		},
	}

	err = gw.UpdateConfig(cfg)
	if err != nil {
		t.Fatalf("UpdateConfig error: %v", err)
	}
	defer func() { _ = gw.Stop() }()

	stats := gw.Stats()
	if !stats.Enabled {
		t.Fatalf("expected stats.Enabled true")
	}
	if stats.Port != port {
		t.Fatalf("stats.Port = %d, want %d", stats.Port, port)
	}
	if len(stats.Pools) != 1 {
		t.Fatalf("pools count = %d, want 1", len(stats.Pools))
	}
	if stats.TotalProxies != 1 {
		t.Fatalf("total_proxies = %d, want 1", stats.TotalProxies)
	}

	// Disable
	cfg.Enabled = false
	_ = gw.UpdateConfig(cfg)
	if gw.Stats().Enabled {
		t.Fatalf("expected stats.Enabled false after disabling")
	}
}
