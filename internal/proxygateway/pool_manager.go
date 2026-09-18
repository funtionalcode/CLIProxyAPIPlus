package proxygateway

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/turnstate"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

type activePool struct {
	config  config.ProxyPoolConfig
	proxies []string
}

// PoolManager aggregates multiple proxy pools and delivers upstream proxies in round-robin fashion.
type PoolManager struct {
	mu           sync.RWMutex
	pools        []*activePool
	flatProxies  []string
	currentIndex atomic.Uint64
	httpClient   *http.Client
}

// NewPoolManager creates a new PoolManager initialized with the given pool configs.
func NewPoolManager(pools []config.ProxyPoolConfig) *PoolManager {
	pm := &PoolManager{
		httpClient: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
			Timeout: 15 * time.Second,
		},
	}
	pm.UpdatePools(pools)
	return pm
}

// UpdatePools updates the pool configuration and rebuilds the active proxy list.
func (pm *PoolManager) UpdatePools(pools []config.ProxyPoolConfig) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	var newPools []*activePool
	var flat []string

	for _, p := range pools {
		ap := &activePool{
			config: p,
		}
		var normalized []string
		for _, raw := range p.Proxies {
			norm := turnstate.NormalizeProxyURL(raw)
			if norm != "" {
				normalized = append(normalized, norm)
			}
		}
		ap.proxies = normalized
		newPools = append(newPools, ap)

		if p.Enabled {
			flat = append(flat, normalized...)
		}
	}

	pm.pools = newPools
	pm.flatProxies = flat
}

// NextProxy retrieves the next upstream proxy across all enabled pools via round-robin.
// Returns empty string if no proxies are configured or available.
func (pm *PoolManager) NextProxy() string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	n := len(pm.flatProxies)
	if n == 0 {
		return ""
	}
	idx := pm.currentIndex.Add(1) - 1
	return pm.flatProxies[idx%uint64(n)]
}

// TotalProxyCount returns the number of active proxies across all enabled pools.
func (pm *PoolManager) TotalProxyCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.flatProxies)
}

// Summaries returns a snapshot of pool summaries for monitoring and management APIs.
func (pm *PoolManager) Summaries() []PoolSummary {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	out := make([]PoolSummary, len(pm.pools))
	for i, p := range pm.pools {
		out[i] = PoolSummary{
			Name:           p.config.Name,
			Enabled:        p.config.Enabled,
			Weight:         p.config.Weight,
			Proxies:        append([]string(nil), p.config.Proxies...),
			ProxyURLSource: p.config.ProxyURLSource,
			ProxyCount:     len(p.proxies),
		}
	}
	return out
}

// RefreshDynamicSources polls all pools with configured ProxyURLSource and updates their proxies.
func (pm *PoolManager) RefreshDynamicSources(ctx context.Context) {
	pm.mu.RLock()
	poolsCopy := make([]*activePool, len(pm.pools))
	copy(poolsCopy, pm.pools)
	pm.mu.RUnlock()

	updatedAny := false
	for _, ap := range poolsCopy {
		source := strings.TrimSpace(ap.config.ProxyURLSource)
		if source == "" {
			continue
		}

		fetched, errFetch := fetchProxySource(ctx, pm.httpClient, source)
		if errFetch != nil {
			log.Debugf("proxy gateway: failed to refresh pool %q: %v", ap.config.Name, errFetch)
			continue
		}
		pm.mu.Lock()
		ap.proxies = fetched
		updatedAny = true
		pm.mu.Unlock()
		log.Infof("proxy gateway: refreshed %d proxies for pool %q", len(fetched), ap.config.Name)
	}

	if updatedAny {
		pm.mu.Lock()
		var flat []string
		for _, ap := range pm.pools {
			if ap.config.Enabled {
				flat = append(flat, ap.proxies...)
			}
		}
		pm.flatProxies = flat
		pm.mu.Unlock()
	}
}
