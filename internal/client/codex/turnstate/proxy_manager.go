package turnstate

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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

func isNumericPort(s string) bool {
	p, err := strconv.Atoi(s)
	return err == nil && p > 0 && p <= 65535
}

// NormalizeProxyURL cleans and normalizes proxy strings, supporting:
// - Standard URLs: http://user:pass@host:port, socks5://host:port
// - Webshare / Scraper format: host:port:user:pass -> http://user:pass@host:port
// - Reverse format: user:pass:host:port -> http://user:pass@host:port
// - Bracketed and unbracketed IPv6 addresses
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

	rest = strings.TrimSuffix(rest, "/")

	// Format check 1: Bracketed IPv6 with port and user:pass, e.g. [2001:db8::1]:8080:user:pass
	if strings.HasPrefix(rest, "[") {
		closeBracket := strings.Index(rest, "]")
		if closeBracket != -1 && strings.HasPrefix(rest[closeBracket:], "]:") {
			ipv6 := rest[:closeBracket+1]
			remaining := rest[closeBracket+2:]
			remParts := strings.Split(remaining, ":")
			if len(remParts) == 3 && isNumericPort(remParts[0]) {
				port, user, pass := remParts[0], remParts[1], remParts[2]
				return fmt.Sprintf("%s%s:%s@%s:%s", scheme, user, pass, ipv6, port)
			}
		}
	}

	// Format check 2: If contains @, standard user:pass@host:port format
	if atIdx := strings.LastIndex(rest, "@"); atIdx != -1 {
		userInfo := rest[:atIdx+1]
		hostPort := rest[atIdx+1:]
		if strings.HasPrefix(hostPort, "[") && strings.Contains(hostPort, "]") {
			return scheme + userInfo + hostPort
		}
		colonCount := strings.Count(hostPort, ":")
		if colonCount > 1 {
			lastColon := strings.LastIndex(hostPort, ":")
			ipPart := hostPort[:lastColon]
			portPart := hostPort[lastColon+1:]
			hostPort = "[" + ipPart + "]:" + portPart
		}
		return scheme + userInfo + hostPort
	}

	// Format check 3: IP:PORT:USER:PASS (e.g. Webshare format: 31.59.20.176:6754:jxpogrlk:suzliqoleexq)
	parts := strings.Split(rest, ":")
	if len(parts) == 4 {
		if isNumericPort(parts[1]) {
			ip, port, user, pass := parts[0], parts[1], parts[2], parts[3]
			return fmt.Sprintf("%s%s:%s@%s:%s", scheme, user, pass, ip, port)
		}
		if isNumericPort(parts[3]) {
			user, pass, ip, port := parts[0], parts[1], parts[2], parts[3]
			return fmt.Sprintf("%s%s:%s@%s:%s", scheme, user, pass, ip, port)
		}
	}

	// Format check 4: Bracketed IPv6 e.g. [2001:db8::1]:8080
	if strings.HasPrefix(rest, "[") && strings.Contains(rest, "]") {
		return scheme + rest
	}

	// Format check 5: Unbracketed IPv6 with port (e.g. 2001:db8::1:8080)
	colonCount := strings.Count(rest, ":")
	if colonCount > 1 {
		lastColon := strings.LastIndex(rest, ":")
		ipPart := rest[:lastColon]
		portPart := rest[lastColon+1:]
		return fmt.Sprintf("%s[%s]:%s", scheme, ipPart, portPart)
	}

	// Format check 6: Standard host:port
	return scheme + rest
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
