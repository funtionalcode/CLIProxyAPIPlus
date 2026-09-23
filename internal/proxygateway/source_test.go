package proxygateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestFetchProxySource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		count  int
	}{
		{"正常列表", 200, "# comment\n127.0.0.1:8080\nsocks5://user:pass@[::1]:1080\n127.0.0.1:8080\n", 2},
		{"过滤无效地址", 200, "error\n<html>error</html>\nftp://host:21\nhttp://host:99999\n127.0.0.1:8080", 1},
		{"接口错误", 403, "secret-key", 0},
		{"空列表", 200, "\n# empty\n", 0},
		{"JSON 错误正文", 200, `{"error":"invalid token"}`, 0},
		{"响应过大", 200, strings.Repeat("x", (1<<20)+1), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			proxies, errFetch := fetchProxySource(context.Background(), srv.Client(), srv.URL+"?token=secret-key")
			if len(proxies) != tc.count || (errFetch != nil) != (tc.count == 0) {
				t.Fatalf("count=%d error=%v", len(proxies), errFetch)
			}
			if errFetch != nil && strings.Contains(errFetch.Error(), "secret-key") {
				t.Fatal("错误泄露凭据")
			}
		})
	}
	if _, errFetch := fetchProxySource(context.Background(), http.DefaultClient, "file:///etc/passwd"); errFetch == nil {
		t.Fatal("不应接受非 HTTP 来源")
	}
}

func TestSourceProbeUsesFetchedProxyAndReportsStages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "127.0.0.1:8080\n127.0.0.1:8081\n")
	}))
	defer srv.Close()
	for _, fail := range []bool{false, true} {
		calls := 0
		result := testProxySource(context.Background(), srv.Client(), srv.URL, func(_ context.Context, proxy string) (int64, error) {
			calls++
			if proxy != "http://127.0.0.1:8080" {
				t.Fatalf("错误的代理地址 %s", proxy)
			}
			if fail {
				return 12, fmt.Errorf("connection refused")
			}
			return 12, nil
		})
		if calls != 1 || result.Success == fail || result.Stage != "proxy" || result.SourceProxyCount != 2 || len(result.Proxies) != 2 || result.LatencyMs != 12 {
			t.Fatalf("unexpected result: %+v calls=%d", result, calls)
		}
	}
	result := testProxySource(context.Background(), srv.Client(), "invalid", func(context.Context, string) (int64, error) {
		t.Fatal("来源无效时不应调用代理测试")
		return 0, nil
	})
	if result.Success || result.Stage != "source" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDynamicRefreshPreservesConfiguredStaticList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "127.0.0.1:8081\n127.0.0.1:8082\n")
	}))
	defer srv.Close()
	pm := NewPoolManager([]config.ProxyPoolConfig{{Name: "dynamic", Enabled: true, Proxies: []string{"http://127.0.0.1:8080"}, ProxyURLSource: srv.URL}})
	pm.RefreshDynamicSources(context.Background())
	summary := pm.Summaries()[0]
	if summary.ProxyCount != 2 || len(summary.Proxies) != 1 || summary.Proxies[0] != "http://127.0.0.1:8080" {
		t.Fatalf("动态结果不能覆盖静态配置: %+v", summary)
	}
	if pm.NextProxy() != "http://127.0.0.1:8081" || pm.NextProxy() != "http://127.0.0.1:8082" {
		t.Fatal("动态代理未参与轮询")
	}
}

func TestGatewayProbeTraversesAuthenticatedRoundRobinGateway(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		targetCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	var counts [2]atomic.Int64
	proxies := make([]string, 0, 2)
	for i := range counts {
		index := i
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counts[index].Add(1)
			req := r.Clone(r.Context())
			req.RequestURI = ""
			resp, errForward := transport.RoundTrip(req)
			if errForward != nil {
				http.Error(w, errForward.Error(), 502)
				return
			}
			defer func() { _ = resp.Body.Close() }()
			w.WriteHeader(resp.StatusCode)
		}))
		defer upstream.Close()
		proxies = append(proxies, upstream.URL)
	}
	pm := NewPoolManager([]config.ProxyPoolConfig{{Name: "test", Enabled: true, Proxies: proxies}})
	server := NewServer(pm, "test-user", "test-password")
	listener := httptest.NewServer(server)
	defer listener.Close()
	u, _ := url.Parse(listener.URL)
	host, portText, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portText)
	gateway := &Gateway{poolMgr: pm, running: true, cfg: config.ProxyGatewayConfig{Bind: host, Port: port, AuthUser: "test-user", AuthPass: "test-password"}}
	for range 2 {
		result := gateway.testGateway(context.Background(), func(ctx context.Context, proxy string) (int64, error) { return testProxy(ctx, proxy, target.URL) })
		if !result.Success || result.Stage != "gateway" || strings.Contains(result.ProxyURL, "test-password") {
			t.Fatalf("unexpected result: %+v", result)
		}
	}
	if targetCalls.Load() != 2 || counts[0].Load() != 1 || counts[1].Load() != 1 {
		t.Fatal("测试请求未通过网关轮询两个上游代理")
	}
	requests, _, _ := server.Metrics()
	if requests != 2 {
		t.Fatalf("网关请求计数 %d", requests)
	}
}

func TestGatewayProbeRejectsStoppedOrEmptyGateway(t *testing.T) {
	for _, running := range []bool{false, true} {
		gateway := &Gateway{poolMgr: NewPoolManager(nil), running: running}
		result := gateway.testGateway(context.Background(), func(context.Context, string) (int64, error) { t.Fatal("不应绕过代理池直连"); return 0, nil })
		if result.Success || result.Error == "" {
			t.Fatalf("unexpected result: %+v", result)
		}
	}
}

func TestProxyProbeRejectsHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer srv.Close()
	if _, errProbe := testProxy(context.Background(), srv.URL, "http://example.test"); errProbe == nil {
		t.Fatal("HTTP 502 不应显示测试成功")
	}
}
