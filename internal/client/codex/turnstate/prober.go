package turnstate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/proxytrace"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	defaultProbeUserAgent  = "codex-tui/0.146.0 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.146.0)"
	defaultProbeOriginator = "codex-tui"
	defaultCodexBaseURL    = "https://chatgpt.com/backend-api/codex"
)

var (
	probeProxyCredentialPattern = regexp.MustCompile(`(?i)\b(https?|socks5h?)://[^\s/@]+(?::[^\s/@]*)?@`)
	probeBearerPattern          = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
)

// AuthSupplierFunc is a callback that returns active Codex auth credentials for probing.
type AuthSupplierFunc func(requestedID string) (apiKey, authID, accountID string, err error)

// Prober manages the probe track that hunts for high-compute turn-state tickets via the proxy pool.
type Prober struct {
	pool         *Pool
	proxyManager *ProxyManager
	cfg          *config.Config
	authSupplier AuthSupplierFunc

	mu           sync.Mutex
	active       atomic.Bool
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	totalProbed  atomic.Uint64
	totalSuccess atomic.Uint64
	totalFailed  atomic.Uint64
	events       eventLog
}

// NewProber creates a new Prober instance.
func NewProber(pool *Pool, pm *ProxyManager, cfg *config.Config, authSupplier AuthSupplierFunc) *Prober {
	return &Prober{
		pool:         pool,
		proxyManager: pm,
		cfg:          cfg,
		authSupplier: authSupplier,
	}
}

// SetAuthSupplier updates the credentials supplier for probing.
func (p *Prober) SetAuthSupplier(supplier AuthSupplierFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.authSupplier = supplier
}

// UpdateConfig updates the configuration reference.
func (p *Prober) UpdateConfig(cfg *config.Config) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = cfg.CloneForRuntime()
}

// Start launches the background probing loop.
func (p *Prober) Start(ctx context.Context) {
	p.mu.Lock()
	if p.active.Load() {
		p.mu.Unlock()
		return
	}

	probeCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.active.Store(true)
	cfg := p.cfg
	p.wg.Add(1)
	p.mu.Unlock()

	go p.probeLoop(probeCtx)
	log.Infof("codex turn-state prober started (min_spare=%d, max_pool=%d, interval=%v)",
		cfg.Codex.TurnState.Probe.MinSpare,
		cfg.Codex.TurnState.Probe.MaxPoolSize,
		cfg.Codex.TurnState.Probe.Interval,
	)
}

// Stop terminates the background probing loop.
func (p *Prober) Stop() {
	p.mu.Lock()
	if !p.active.Load() {
		p.mu.Unlock()
		return
	}
	p.active.Store(false)
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()

	p.wg.Wait()
	log.Infof("codex turn-state prober stopped")
}

// IsActive returns true if the background prober is running.
func (p *Prober) IsActive() bool {
	return p.active.Load()
}

func (p *Prober) probeLoop(ctx context.Context) {
	defer p.wg.Done()

	p.mu.Lock()
	interval := p.cfg.Codex.TurnState.Probe.Interval
	p.mu.Unlock()
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial check on start
	p.checkAndProbe(ctx)

	// Proxy source refresh ticker (every 5 minutes)
	refreshTicker := time.NewTicker(5 * time.Minute)
	defer refreshTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-refreshTicker.C:
			if p.proxyManager != nil {
				_ = p.proxyManager.RefreshFromSource(ctx)
			}
		case <-ticker.C:
			p.checkAndProbe(ctx)
		}
	}
}

func (p *Prober) checkAndProbe(ctx context.Context) {
	p.mu.Lock()
	cfg := p.cfg
	p.mu.Unlock()
	if cfg == nil || !cfg.Codex.TurnState.Enabled {
		return
	}
	p.pool.PruneExpired()

	currentCount := p.pool.Len()
	minSpare := cfg.Codex.TurnState.Probe.MinSpare
	if currentCount >= minSpare {
		return
	}

	needed := minSpare - currentCount
	concurrency := cfg.Codex.TurnState.Probe.Concurrency
	if concurrency <= 0 {
		concurrency = 2
	}
	if needed > concurrency {
		needed = concurrency
	}

	var batchWg sync.WaitGroup
	for i := 0; i < needed; i++ {
		batchWg.Add(1)
		go func() {
			defer batchWg.Done()
			proxy := ""
			if p.proxyManager != nil {
				proxy = p.proxyManager.NextProxy()
			}
			_, _ = p.ExecuteProbe(ctx, proxy)
		}()
	}
	batchWg.Wait()
}

// ExecuteProbe sends a single lightweight ping request to OpenAI Codex and evaluates the resulting turn state.

func formatProbeHTTPError(status int, body io.Reader) error {
	raw, _ := io.ReadAll(io.LimitReader(body, 2048))
	trimmed := bytes.TrimSpace(raw)
	detail := strings.TrimSpace(string(trimmed))
	if json.Valid(trimmed) {
		var compact bytes.Buffer
		if json.Compact(&compact, trimmed) == nil {
			detail = compact.String()
		}
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > 1500 {
		detail = detail[:1500] + "…"
	}
	if detail == "" {
		return fmt.Errorf("探测接口返回 HTTP %d", status)
	}
	return fmt.Errorf("探测接口返回 HTTP %d：%s", status, detail)
}

func formatProbeNetworkError(err error) error {
	if err == nil {
		return fmt.Errorf("探测连接失败")
	}
	detail := strings.Join(strings.Fields(err.Error()), " ")
	detail = probeProxyCredentialPattern.ReplaceAllString(detail, "$1://<凭据已隐藏>@")
	detail = probeBearerPattern.ReplaceAllString(detail, "Bearer <凭据已隐藏>")
	if len(detail) > 1000 {
		detail = detail[:1000] + "…"
	}
	if detail == "" {
		return fmt.Errorf("探测连接失败")
	}
	return fmt.Errorf("探测连接失败：%s", detail)
}

func (p *Prober) ExecuteProbe(ctx context.Context, proxyURL string) (result *Ticket, resultErr error) {
	p.mu.Lock()
	cfg := p.cfg
	supplier := p.authSupplier
	p.mu.Unlock()

	var apiKey, authID, accountID string
	var errAuth error
	var gatewayTraceID, gatewayPool, gatewayProxy string
	source := "account"
	if cfg != nil {
		source = cfg.Codex.TurnState.Probe.AuthSource
		if source == "" {
			source = "account"
			if strings.TrimSpace(cfg.Codex.TurnState.Probe.AuthID) == "" && cfg.Codex.TurnState.Probe.APIKey != "" {
				source = "external"
			}
		}
	}
	started := time.Now()
	p.totalProbed.Add(1)
	defer func() {
		if route, ok := proxytrace.Take(gatewayTraceID); ok {
			gatewayPool, gatewayProxy = route.Pool, route.Proxy
			if result != nil {
				result.GatewayPool, result.GatewayProxy = gatewayPool, gatewayProxy
			}
		}
		event := Event{Kind: "probe", Source: source, AuthID: authID, LatencyMS: time.Since(started).Milliseconds(), Outcome: "success", GatewayPool: gatewayPool, GatewayProxy: gatewayProxy, Message: "探测成功，已获取符合配置长度的状态头"}
		if cfg != nil {
			event.Model = cfg.Codex.TurnState.Probe.Model
			if source == "account" && event.AuthID == "" {
				event.AuthID = cfg.Codex.TurnState.Probe.AuthID
			}
		}
		if resultErr != nil {
			event.Outcome, event.Message = "failed", resultErr.Error()
		} else if result != nil {
			event.Length = result.Length
		}
		p.events.add(event)
	}()

	if source == "account" && cfg != nil && strings.TrimSpace(cfg.Codex.TurnState.Probe.AuthID) != "" {
		if supplier == nil {
			p.totalFailed.Add(1)
			return nil, fmt.Errorf("账户管理服务不可用")
		}
		apiKey, authID, accountID, errAuth = supplier(strings.TrimSpace(cfg.Codex.TurnState.Probe.AuthID))
		if errAuth != nil {
			p.totalFailed.Add(1)
			return nil, errAuth
		}
	} else if source == "external" && cfg != nil && cfg.Codex.TurnState.Probe.APIKey != "" {
		apiKey = cfg.Codex.TurnState.Probe.APIKey
	}

	if strings.TrimSpace(apiKey) == "" {
		p.totalFailed.Add(1)
		return nil, fmt.Errorf("请先选择用于探针的已登录账户，或配置独立探针接口密钥")
	}

	baseURL := defaultCodexBaseURL
	if source == "external" && cfg != nil && strings.TrimSpace(cfg.Codex.TurnState.Probe.BaseURL) != "" {
		baseURL = strings.TrimSpace(cfg.Codex.TurnState.Probe.BaseURL)
	}
	targetURL := strings.TrimSuffix(baseURL, "/") + "/responses"
	if target, err := url.Parse(targetURL); err == nil {
		ip := net.ParseIP(target.Hostname())
		if source == "external" && (target.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())) {
			proxyURL = "direct"
		}
	}
	transportProxyURL := proxyURL
	transportProxyURL, gatewayTraceID = attachGatewayTrace(transportProxyURL, cfg)

	model := "gpt-5.3-codex"
	if cfg != nil && strings.TrimSpace(cfg.Codex.TurnState.Probe.Model) != "" {
		model = strings.TrimSpace(cfg.Codex.TurnState.Probe.Model)
	}

	prompt := "ping"
	if cfg != nil && strings.TrimSpace(cfg.Codex.TurnState.Probe.Prompt) != "" {
		prompt = strings.TrimSpace(cfg.Codex.TurnState.Probe.Prompt)
	}

	payloadMap := map[string]any{
		"model":        model,
		"stream":       true,
		"store":        false,
		"instructions": "",
		"input": []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{
						"type": "input_text",
						"text": prompt,
					},
				},
			},
		},
	}
	payloadBytes, _ := json.Marshal(payloadMap)

	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(probeCtx, http.MethodPost, targetURL, bytes.NewReader(payloadBytes))
	if err != nil {
		p.totalFailed.Add(1)
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("User-Agent", defaultProbeUserAgent)
	httpReq.Header.Set("Originator", defaultProbeOriginator)
	if accountID != "" {
		httpReq.Header.Set("Chatgpt-Account-Id", accountID)
	}

	authForClient := &cliproxyauth.Auth{
		ProxyURL: transportProxyURL,
	}
	client := helps.NewCodexFingerprintHTTPClient(probeCtx, cfg, authForClient, 30*time.Second)

	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	httpResp, err := client.Do(httpReq)
	if err != nil {
		p.totalFailed.Add(1)
		return nil, formatProbeNetworkError(err)
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		p.totalFailed.Add(1)
		return nil, formatProbeHTTPError(httpResp.StatusCode, httpResp.Body)
	}

	turnState := strings.TrimSpace(httpResp.Header.Get(HeaderName))
	if turnState == "" {
		// Try lowercase fallback
		turnState = strings.TrimSpace(httpResp.Header.Get(strings.ToLower(HeaderName)))
	}

	minLength := 160
	if cfg != nil && cfg.Codex.TurnState.MinLength > 0 {
		minLength = cfg.Codex.TurnState.MinLength
	}

	if len(turnState) < minLength {
		p.totalFailed.Add(1)
		return nil, fmt.Errorf("状态头长度为 %d，未达到配置阈值 %d", len(turnState), minLength)
	}

	ttl := 15 * time.Minute
	if cfg != nil && cfg.Codex.TurnState.Probe.TicketTTL > 0 {
		ttl = cfg.Codex.TurnState.Probe.TicketTTL
	}
	if route, ok := proxytrace.Take(gatewayTraceID); ok {
		gatewayPool, gatewayProxy = route.Pool, route.Proxy
	}
	gatewayTraceID = ""

	now := time.Now()
	ticket := &Ticket{
		State:        turnState,
		Length:       len(turnState),
		AcquiredAt:   now,
		ExpiresAt:    now.Add(ttl),
		Proxy:        proxyURL,
		GatewayPool:  gatewayPool,
		GatewayProxy: gatewayProxy,
		AuthID:       authID,
		Model:        model,
	}

	p.pool.Push(ticket)
	p.totalSuccess.Add(1)
	return ticket, nil
}

func attachGatewayTrace(proxyURL string, cfg *config.Config) (string, string) {
	if cfg == nil || !cfg.ProxyGateway.Enabled {
		return proxyURL, ""
	}
	parsed, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return proxyURL, ""
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	gatewayPort := cfg.ProxyGateway.Port
	if gatewayPort <= 0 {
		gatewayPort = 8899
	}
	if port != fmt.Sprint(gatewayPort) {
		return proxyURL, ""
	}
	traceBytes := make([]byte, 16)
	if _, err = rand.Read(traceBytes); err != nil {
		return proxyURL, ""
	}
	traceID := hex.EncodeToString(traceBytes)
	query := parsed.Query()
	query.Set(proxyutil.GatewayTraceQueryParameter, traceID)
	parsed.RawQuery = query.Encode()
	return parsed.String(), traceID
}

// Metrics returns atomic counters for total probed, succeeded, and failed requests.
func (p *Prober) Metrics() (probed, success, failed uint64) {
	return p.totalProbed.Load(), p.totalSuccess.Load(), p.totalFailed.Load()
}
