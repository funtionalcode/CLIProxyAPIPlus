package helps

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type proxyHelperRoundTripper func(*http.Request) (*http.Response, error)

func (f proxyHelperRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func resetHTTPClientCacheForTest(t *testing.T) {
	t.Helper()
	httpClientCacheMutex.Lock()
	previous := httpClientCache
	httpClientCache = make(map[string]*http.Client)
	httpClientCacheMutex.Unlock()
	t.Cleanup(func() {
		httpClientCacheMutex.Lock()
		httpClientCache = previous
		httpClientCacheMutex.Unlock()
	})
}

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
		0,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewProxyAwareHTTPClientDoesNotCacheContextRoundTripper(t *testing.T) {
	resetHTTPClientCacheForTest(t)

	var firstCalled bool
	firstCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", proxyHelperRoundTripper(func(req *http.Request) (*http.Response, error) {
		firstCalled = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("first")),
			Request:    req,
		}, nil
	}))
	firstClient := NewProxyAwareHTTPClient(firstCtx, nil, nil, 0)
	firstResp, errFirst := firstClient.Get("https://example.com/first")
	if errFirst != nil {
		t.Fatalf("first Get error: %v", errFirst)
	}
	_ = firstResp.Body.Close()
	if !firstCalled {
		t.Fatal("expected first context RoundTripper to be called")
	}

	var secondCalled bool
	secondCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", proxyHelperRoundTripper(func(req *http.Request) (*http.Response, error) {
		secondCalled = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("second")),
			Request:    req,
		}, nil
	}))
	secondClient := NewProxyAwareHTTPClient(secondCtx, nil, nil, 0)
	secondResp, errSecond := secondClient.Get("https://example.com/second")
	if errSecond != nil {
		t.Fatalf("second Get error: %v", errSecond)
	}
	_ = secondResp.Body.Close()
	if !secondCalled {
		t.Fatal("expected second context RoundTripper to be called")
	}
}
