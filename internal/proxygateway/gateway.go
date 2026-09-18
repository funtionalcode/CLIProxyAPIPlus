package proxygateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

var (
	globalGateway *Gateway
	globalOnce    sync.Once
)

// Gateway manages the lifecycle of the local forward proxy server and its proxy pools.
type Gateway struct {
	mu         sync.RWMutex
	cfg        config.ProxyGatewayConfig
	poolMgr    *PoolManager
	proxySrv   *Server
	httpServer *http.Server
	listener   net.Listener
	running    bool
	stopTicker chan struct{}
}

// GetGateway returns the global Gateway singleton.
func GetGateway() *Gateway {
	globalOnce.Do(func() {
		poolMgr := NewPoolManager(nil)
		proxySrv := NewServer(poolMgr, "", "")
		globalGateway = &Gateway{
			poolMgr:  poolMgr,
			proxySrv: proxySrv,
		}
	})
	return globalGateway
}

// UpdateConfig updates the gateway configuration and starts/stops/restarts the listener as needed.
func (g *Gateway) UpdateConfig(cfg config.ProxyGatewayConfig) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	oldCfg := g.cfg
	g.cfg = cfg

	g.poolMgr.UpdatePools(cfg.Pools)
	g.proxySrv.UpdateAuth(cfg.AuthUser, cfg.AuthPass)

	if !cfg.Enabled {
		if g.running {
			_ = g.stopLocked()
		}
		return nil
	}

	// If already running and bind/port didn't change, just keep running with updated pools
	if g.running && oldCfg.Bind == cfg.Bind && oldCfg.Port == cfg.Port {
		go g.poolMgr.RefreshDynamicSources(context.Background())
		return nil
	}

	// Restart listener on new bind/port
	if g.running {
		_ = g.stopLocked()
	}

	if errStart := g.startLocked(); errStart != nil {
		return errStart
	}
	go g.poolMgr.RefreshDynamicSources(context.Background())
	return nil
}

// Start launches the forward proxy gateway if enabled.
func (g *Gateway) Start() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.cfg.Enabled || g.running {
		return nil
	}
	return g.startLocked()
}

func (g *Gateway) startLocked() error {
	bind := g.cfg.Bind
	if bind == "" {
		bind = "0.0.0.0"
	}
	port := g.cfg.Port
	if port <= 0 {
		port = 8899
	}

	addr := fmt.Sprintf("%s:%d", bind, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("proxy gateway failed to listen on %s: %w", addr, err)
	}

	g.listener = ln
	g.httpServer = &http.Server{
		Handler:      g.proxySrv,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	g.running = true
	g.stopTicker = make(chan struct{})

	server := g.httpServer
	go func() {
		if errServe := server.Serve(ln); errServe != nil && errServe != http.ErrServerClosed {
			log.Errorf("proxy gateway server error: %v", errServe)
		}
	}()

	// Background ticker for dynamic sources (every 5 minutes)
	stopChan := g.stopTicker
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-stopChan:
				return
			case <-ticker.C:
				g.poolMgr.RefreshDynamicSources(context.Background())
			}
		}
	}()

	log.Infof("proxy gateway started, listening on http://%s (pools=%d, proxies=%d)", addr, len(g.cfg.Pools), g.poolMgr.TotalProxyCount())
	return nil
}

// Stop halts the forward proxy gateway.
func (g *Gateway) Stop() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.stopLocked()
}

func (g *Gateway) stopLocked() error {
	if !g.running {
		return nil
	}

	if g.stopTicker != nil {
		close(g.stopTicker)
		g.stopTicker = nil
	}

	var err error
	if g.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err = g.httpServer.Shutdown(ctx)
		g.httpServer = nil
	}

	g.listener = nil
	g.running = false
	log.Infof("proxy gateway stopped")
	return err
}

// Stats returns current gateway metrics.
func (g *Gateway) Stats() GatewayStats {
	g.mu.RLock()
	defer g.mu.RUnlock()

	bind := g.cfg.Bind
	if bind == "" {
		bind = "0.0.0.0"
	}
	port := g.cfg.Port
	if port <= 0 {
		port = 8899
	}

	totalReq, activeConn, totalErr := g.proxySrv.Metrics()

	return GatewayStats{
		Enabled:           g.running,
		Bind:              bind,
		Port:              port,
		ListenAddress:     fmt.Sprintf("http://%s:%d", bind, port),
		TotalRequests:     totalReq,
		ActiveConnections: activeConn,
		TotalErrors:       totalErr,
		TotalProxies:      g.poolMgr.TotalProxyCount(),
		Pools:             g.poolMgr.Summaries(),
	}
}

// TestProxy checks connectivity of a single upstream proxy by making a quick HTTPS request through it.
func (g *Gateway) TestProxy(ctx context.Context, proxyURL string) (int64, error) {
	return testProxy(ctx, proxyURL, "https://www.cloudflare.com/cdn-cgi/trace")
}

func testProxy(ctx context.Context, proxyURL, targetURL string) (int64, error) {
	transport, _, err := proxyutil.BuildHTTPTransport(proxyURL)
	if err != nil {
		return 0, err
	}
	if transport != nil {
		defer transport.CloseIdleConnections()
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	testCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()
	// Target Cloudflare trace or httpbin for quick reliable test
	req, err := http.NewRequestWithContext(testCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return time.Since(start).Milliseconds(), err
	}
	_ = resp.Body.Close()

	latency := time.Since(start).Milliseconds()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return latency, fmt.Errorf("proxy test returned HTTP %d", resp.StatusCode)
	}
	return latency, nil
}

func (g *Gateway) TestGateway(ctx context.Context) TestResult {
	return g.testGateway(ctx, g.TestProxy)
}

func (g *Gateway) testGateway(ctx context.Context, probe func(context.Context, string) (int64, error)) TestResult {
	g.mu.RLock()
	cfg, running := g.cfg, g.running
	g.mu.RUnlock()
	result := TestResult{Stage: "gateway"}
	if !running {
		result.Error = "proxy gateway is not running"
		return result
	}
	if g.poolMgr.TotalProxyCount() == 0 {
		result.Error = "proxy gateway has no active upstream proxies"
		return result
	}
	host := strings.Trim(cfg.Bind, "[]")
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	} else if host == "::" {
		host = "::1"
	}
	port := cfg.Port
	if port <= 0 {
		port = 8899
	}
	proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort(host, fmt.Sprint(port))}
	result.ProxyURL = proxyURL.String()
	if cfg.AuthUser != "" {
		proxyURL.User = url.UserPassword(cfg.AuthUser, cfg.AuthPass)
	}
	latency, errProbe := probe(ctx, proxyURL.String())
	result.LatencyMs = latency
	result.Success = errProbe == nil
	if errProbe != nil {
		result.Error = errProbe.Error()
	}
	return result
}
