package turnstate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	defaultProbeUserAgent  = "codex-tui/0.146.0 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.146.0)"
	defaultProbeOriginator = "codex-tui"
	defaultCodexBaseURL    = "https://chatgpt.com/backend-api/codex"
)

// AuthSupplierFunc is a callback that returns active Codex auth credentials for probing.
type AuthSupplierFunc func() (apiKey, authID, accountID string, err error)

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
	p.cfg = cfg
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
	p.mu.Unlock()

	p.wg.Add(1)
	go p.probeLoop(probeCtx)
	log.Infof("codex turn-state prober started (min_spare=%d, max_pool=%d, interval=%v)",
		p.cfg.Codex.TurnState.Probe.MinSpare,
		p.cfg.Codex.TurnState.Probe.MaxPoolSize,
		p.cfg.Codex.TurnState.Probe.Interval,
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

	interval := p.cfg.Codex.TurnState.Probe.Interval
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
	if !p.cfg.Codex.TurnState.Enabled {
		return
	}
	p.pool.PruneExpired()

	currentCount := p.pool.Len()
	minSpare := p.cfg.Codex.TurnState.Probe.MinSpare
	if currentCount >= minSpare {
		return
	}

	needed := minSpare - currentCount
	concurrency := p.cfg.Codex.TurnState.Probe.Concurrency
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
func (p *Prober) ExecuteProbe(ctx context.Context, proxyURL string) (*Ticket, error) {
	p.mu.Lock()
	cfg := p.cfg
	supplier := p.authSupplier
	p.mu.Unlock()

	var apiKey, authID, accountID string
	var errAuth error

	if cfg != nil && cfg.Codex.TurnState.Probe.APIKey != "" {
		apiKey = cfg.Codex.TurnState.Probe.APIKey
		authID = cfg.Codex.TurnState.Probe.AuthID
	} else if supplier != nil {
		apiKey, authID, accountID, errAuth = supplier()
		if errAuth != nil {
			p.totalFailed.Add(1)
			return nil, fmt.Errorf("failed to obtain auth for probe: %w", errAuth)
		}
	}

	if strings.TrimSpace(apiKey) == "" {
		p.totalFailed.Add(1)
		return nil, fmt.Errorf("no codex credentials available for probe")
	}

	baseURL := defaultCodexBaseURL
	if cfg != nil && strings.TrimSpace(cfg.Codex.TurnState.Probe.BaseURL) != "" {
		baseURL = strings.TrimSpace(cfg.Codex.TurnState.Probe.BaseURL)
	}
	targetURL := strings.TrimSuffix(baseURL, "/") + "/responses"

	model := "gpt-5.3-codex"
	if cfg != nil && strings.TrimSpace(cfg.Codex.TurnState.Probe.Model) != "" {
		model = strings.TrimSpace(cfg.Codex.TurnState.Probe.Model)
	}

	prompt := "ping"
	if cfg != nil && strings.TrimSpace(cfg.Codex.TurnState.Probe.Prompt) != "" {
		prompt = strings.TrimSpace(cfg.Codex.TurnState.Probe.Prompt)
	}

	payloadMap := map[string]any{
		"model":  model,
		"stream": true,
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
		ProxyURL: proxyURL,
	}
	client := helps.NewCodexFingerprintHTTPClient(probeCtx, cfg, authForClient, 30*time.Second)

	p.totalProbed.Add(1)
	httpResp, err := client.Do(httpReq)
	if err != nil {
		p.totalFailed.Add(1)
		log.Debugf("turn-state prober: probe failed via proxy %q: %v", proxyURL, err)
		return nil, err
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		p.totalFailed.Add(1)
		bodySnippet, _ := io.ReadAll(io.LimitReader(httpResp.Body, 512))
		log.Debugf("turn-state prober: upstream status %d via proxy %q: %s", httpResp.StatusCode, proxyURL, string(bodySnippet))
		return nil, fmt.Errorf("upstream probe returned status %d: %s", httpResp.StatusCode, string(bodySnippet))
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
		log.Debugf("turn-state prober: turn-state too short (%d < %d), degraded compute state discarded via proxy %q", len(turnState), minLength, proxyURL)
		return nil, fmt.Errorf("captured turn-state length %d below required minimum %d", len(turnState), minLength)
	}

	ttl := 15 * time.Minute
	if cfg != nil && cfg.Codex.TurnState.Probe.TicketTTL > 0 {
		ttl = cfg.Codex.TurnState.Probe.TicketTTL
	}

	now := time.Now()
	ticket := &Ticket{
		State:      turnState,
		Length:     len(turnState),
		AcquiredAt: now,
		ExpiresAt:  now.Add(ttl),
		Proxy:      proxyURL,
		AuthID:     authID,
		Model:      model,
	}

	p.pool.Push(ticket)
	p.totalSuccess.Add(1)
	log.Infof("turn-state prober: successfully captured high-compute state (len=%d) via proxy %q", len(turnState), proxyURL)
	return ticket, nil
}

// Metrics returns atomic counters for total probed, succeeded, and failed requests.
func (p *Prober) Metrics() (probed, success, failed uint64) {
	return p.totalProbed.Load(), p.totalSuccess.Load(), p.totalFailed.Load()
}
