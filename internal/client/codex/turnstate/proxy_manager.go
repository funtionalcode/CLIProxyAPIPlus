package turnstate

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

// ProxyManager manages static and dynamically loaded proxy pools (including IPv6 proxies).
type ProxyManager struct {
	mu           sync.RWMutex
	proxies      []string
	sourceURL    string
	currentIndex atomic.Uint64
	client       *http.Client
}

// NewProxyManager creates a new ProxyManager from a list of proxies and an optional source URL.
func NewProxyManager(proxies []string, sourceURL string) *ProxyManager {
	pm := &ProxyManager{
		sourceURL: strings.TrimSpace(sourceURL),
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
	pm.SetProxies(proxies)
	return pm
}

// NormalizeProxyURL cleans and normalizes proxy strings, ensuring correct IPv6 bracket formatting.
func NormalizeProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	scheme := "http://"
	rest := raw
	if idx := strings.Index(raw, "://"); idx != -1 {
		scheme = raw[:idx+3]
		rest = raw[idx+3:]
	}

	userInfo := ""
	hostPort := rest
	if atIdx := strings.LastIndex(rest, "@"); atIdx != -1 {
		userInfo = rest[:atIdx+1]
		hostPort = rest[atIdx+1:]
	}

	// Remove trailing slash if any
	hostPort = strings.TrimSuffix(hostPort, "/")

	// Check if hostPort already has bracketed IPv6
	if strings.HasPrefix(hostPort, "[") && strings.Contains(hostPort, "]") {
		return scheme + userInfo + hostPort
	}

	colonCount := strings.Count(hostPort, ":")
	if colonCount > 1 {
		// Unbracketed IPv6 with port (e.g., 2001:db8::1:8080)
		lastColon := strings.LastIndex(hostPort, ":")
		ipPart := hostPort[:lastColon]
		portPart := hostPort[lastColon+1:]
		hostPort = "[" + ipPart + "]:" + portPart
	}

	return scheme + userInfo + hostPort
}

// SetProxies replaces the current proxy list with the given items after normalization.
func (pm *ProxyManager) SetProxies(proxies []string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	var normalized []string
	seen := make(map[string]struct{})
	for _, p := range proxies {
		norm := NormalizeProxyURL(p)
		if norm == "" {
			continue
		}
		if _, exists := seen[norm]; !exists {
			seen[norm] = struct{}{}
			normalized = append(normalized, norm)
		}
	}
	pm.proxies = normalized
}

// Count returns the number of currently configured proxies.
func (pm *ProxyManager) Count() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.proxies)
}

// NextProxy retrieves the next proxy in round-robin fashion, or empty string if no proxies exist.
func (pm *ProxyManager) NextProxy() string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	n := len(pm.proxies)
	if n == 0 {
		return ""
	}
	idx := pm.currentIndex.Add(1) - 1
	return pm.proxies[idx%uint64(n)]
}

// GetAll returns a copy of all current proxies.
func (pm *ProxyManager) GetAll() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	out := make([]string, len(pm.proxies))
	copy(out, pm.proxies)
	return out
}

// RefreshFromSource fetches the proxy list from sourceURL if configured.
func (pm *ProxyManager) RefreshFromSource(ctx context.Context) error {
	pm.mu.RLock()
	source := pm.sourceURL
	pm.mu.RUnlock()

	if source == "" {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "CLIProxyAPI/1.0 (TurnState Proxy Fetcher)")

	resp, err := pm.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &url.Error{Op: "Get", URL: source, Err: &httpStatusError{statusCode: resp.StatusCode, body: string(body)}}
	}

	scanner := bufio.NewScanner(resp.Body)
	var fetched []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fetched = append(fetched, line)
	}
	if errScan := scanner.Err(); errScan != nil {
		return errScan
	}

	if len(fetched) > 0 {
		pm.SetProxies(fetched)
		log.Debugf("turn-state: refreshed %d proxies from %s", len(fetched), source)
	}
	return nil
}

type httpStatusError struct {
	statusCode int
	body       string
}

func (e *httpStatusError) Error() string {
	return e.body
}
