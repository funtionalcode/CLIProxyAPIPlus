package turnstate

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

var (
	globalManager *Manager
	globalOnce    sync.Once
)

// Manager coordinates the Turn-State ticket pool, proxy pool, and background probing.
type Manager struct {
	mu           sync.RWMutex
	cfg          *config.Config
	pool         *Pool
	proxyManager *ProxyManager
	prober       *Prober
}

// GetManager returns the process-wide TurnState Manager singleton.
func GetManager() *Manager {
	globalOnce.Do(func() {
		pool := NewPool(50, 160, 15*timeMinuteDefault)
		pm := NewProxyManager(nil, "")
		prober := NewProber(pool, pm, nil, nil)
		globalManager = &Manager{
			pool:         pool,
			proxyManager: pm,
			prober:       prober,
		}
	})
	return globalManager
}

const timeMinuteDefault = 15 * 60 * 1000000000 // 15m in ns

// UpdateConfig updates the manager with the latest configuration.
func (m *Manager) UpdateConfig(cfg *config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cfg = cfg.CloneForRuntime()
	if cfg == nil {
		return
	}

	tsCfg := cfg.Codex.TurnState
	probeCfg := tsCfg.Probe

	m.pool.UpdateLimits(probeCfg.MaxPoolSize, tsCfg.MinLength, probeCfg.TicketTTL)
	m.proxyManager.SetProxies(probeCfg.Proxies)
	m.prober.UpdateConfig(cfg)
}

// SetAuthSupplier provides the credentials resolver for the prober.
func (m *Manager) SetAuthSupplier(supplier AuthSupplierFunc) {
	m.prober.SetAuthSupplier(supplier)
}

// Start initiates background probing if enabled by configuration.
func (m *Manager) Start(ctx context.Context) {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()

	if cfg == nil || !cfg.Codex.TurnState.Enabled || !cfg.Codex.TurnState.Probe.IsEnabled(cfg.Codex.TurnState.Enabled) {
		return
	}
	m.prober.Start(ctx)
}

// Stop halts the background prober.
func (m *Manager) Stop() {
	m.prober.Stop()
}

// Pool returns the underlying ticket pool.
func (m *Manager) Pool() *Pool {
	return m.pool
}

// ProxyManager returns the underlying proxy manager.
func (m *Manager) ProxyManager() *ProxyManager {
	return m.proxyManager
}

// Prober returns the underlying prober.
func (m *Manager) Prober() *Prober {
	return m.prober
}

// InjectRequestHeader injects a high-compute turn-state ticket into the outgoing request headers.
func (m *Manager) InjectRequestHeader(headers http.Header, cfg *config.Config) {
	if headers == nil {
		return
	}
	if cfg == nil {
		m.mu.RLock()
		cfg = m.cfg
		m.mu.RUnlock()
	}
	if cfg == nil || !cfg.Codex.TurnState.IsInjectBusiness() {
		return
	}

	minLength := cfg.Codex.TurnState.MinLength
	if minLength <= 0 {
		minLength = 160
	}

	existing := strings.TrimSpace(headers.Get(HeaderName))
	if existing == "" {
		existing = strings.TrimSpace(headers.Get(strings.ToLower(HeaderName)))
	}

	// If force-inject is disabled, and existing state is already high-compute, keep it
	if !cfg.Codex.TurnState.ForceInject && len(existing) >= minLength {
		m.recordInjection(Event{Outcome: "skipped", Length: len(existing), Message: "已保留客户端携带的状态头"})
		return
	}

	ticket, ok := m.pool.ticketForInjection()
	if !ok || ticket.State == "" {
		m.recordInjection(Event{Outcome: "skipped", Message: "状态池为空或已过期，未注入"})
		return
	}

	headers.Set(HeaderName, ticket.State)
	m.recordInjection(Event{
		Outcome: "success", Length: ticket.Length, AuthID: ticket.AuthID, Model: ticket.Model,
		GatewayPool: ticket.GatewayPool, GatewayProxy: ticket.GatewayProxy,
		Message: "已向业务请求注入状态头",
	})
}

func (m *Manager) recordInjection(event Event) {
	if m.prober != nil {
		event.Kind = "injection"
		m.prober.events.add(event)
	}
}

// RecordResponseHeader records high-compute turn-state received from OpenAI responses into the ticket pool.
func (m *Manager) RecordResponseHeader(headers http.Header, cfg *config.Config, authID string, model string) {
	if headers == nil {
		return
	}
	if cfg == nil {
		m.mu.RLock()
		cfg = m.cfg
		m.mu.RUnlock()
	}
	if cfg == nil || !cfg.Codex.TurnState.Enabled {
		return
	}

	state := strings.TrimSpace(headers.Get(HeaderName))
	if state == "" {
		state = strings.TrimSpace(headers.Get(strings.ToLower(HeaderName)))
	}
	if state == "" {
		return
	}

	if m.pool.Feed(state, "", authID, model) {
		log.Debugf("turn-state: captured response state (len=%d) into pool", len(state))
	}
}

// ProbeOnce triggers an immediate probe execution via next proxy.
func (m *Manager) ProbeOnce(ctx context.Context) (*Ticket, error) {
	proxy := m.proxyManager.NextProxy()
	return m.prober.ExecuteProbe(ctx, proxy)
}

// Stats returns a snapshot of current Turn-State operational metrics.
func (m *Manager) Stats() Stats {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()

	enabled := false
	injectBusiness := false
	minSpare := 5
	maxPool := 50
	minLength := 160
	if cfg != nil {
		enabled = cfg.Codex.TurnState.Enabled
		injectBusiness = cfg.Codex.TurnState.IsInjectBusiness()
		if cfg.Codex.TurnState.Probe.MinSpare > 0 {
			minSpare = cfg.Codex.TurnState.Probe.MinSpare
		}
		if cfg.Codex.TurnState.Probe.MaxPoolSize > 0 {
			maxPool = cfg.Codex.TurnState.Probe.MaxPoolSize
		}
		if cfg.Codex.TurnState.MinLength > 0 {
			minLength = cfg.Codex.TurnState.MinLength
		}
	}

	injected, collected, _ := m.pool.Metrics()
	probed, success, failed := m.prober.Metrics()
	events, skipped := m.prober.events.snapshot()
	poolSize := m.pool.Len()
	recentTickets := m.pool.RecentSummaries(10)
	currentStateLength := 0
	if len(recentTickets) > 0 {
		currentStateLength = recentTickets[0].Length
	}

	return Stats{
		Enabled:            enabled,
		InjectBusiness:     injectBusiness,
		ProberActive:       m.prober.IsActive(),
		PoolSize:           poolSize,
		MinSpare:           minSpare,
		MaxPoolSize:        maxPool,
		MinLength:          minLength,
		ProxyCount:         m.proxyManager.Count(),
		TotalProbed:        probed,
		TotalSuccess:       success,
		TotalFailed:        failed,
		TotalInjected:      injected,
		TotalCollected:     collected,
		CurrentStateLength: currentStateLength,
		RecentTickets:      recentTickets,
		RecentEvents:       events,
		TotalSkipped:       skipped,
	}
}

// InjectRequestHeader is a package-level convenience function forwarding to the singleton manager.
func InjectRequestHeader(headers http.Header, cfg *config.Config) {
	GetManager().InjectRequestHeader(headers, cfg)
}

// RecordResponseHeader is a package-level convenience function forwarding to the singleton manager.
func RecordResponseHeader(headers http.Header, cfg *config.Config, authID string, model string) {
	GetManager().RecordResponseHeader(headers, cfg, authID, model)
}
