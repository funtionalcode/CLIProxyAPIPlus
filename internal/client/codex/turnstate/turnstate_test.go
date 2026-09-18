package turnstate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestNormalizeProxyURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "IPv4 standard",
			input:    "http://1.2.3.4:8080",
			expected: "http://1.2.3.4:8080",
		},
		{
			name:     "IPv4 without scheme",
			input:    "1.2.3.4:8080",
			expected: "http://1.2.3.4:8080",
		},
		{
			name:     "IPv4 with credentials",
			input:    "http://user:pass@1.2.3.4:8080",
			expected: "http://user:pass@1.2.3.4:8080",
		},
		{
			name:     "SOCKS5 IPv4",
			input:    "socks5://user:pass@1.2.3.4:1080",
			expected: "socks5://user:pass@1.2.3.4:1080",
		},
		{
			name:     "IPv6 bracketed",
			input:    "http://[2001:db8::1]:8080",
			expected: "http://[2001:db8::1]:8080",
		},
		{
			name:     "IPv6 unbracketed without scheme",
			input:    "2001:db8::1:8080",
			expected: "http://[2001:db8::1]:8080",
		},
		{
			name:     "IPv6 unbracketed with scheme",
			input:    "http://2001:db8::1:8080",
			expected: "http://[2001:db8::1]:8080",
		},
		{
			name:     "IPv6 unbracketed with credentials",
			input:    "http://user:pass@2001:db8::1:8080",
			expected: "http://user:pass@[2001:db8::1]:8080",
		},
		{
			name:     "SOCKS5 IPv6 unbracketed",
			input:    "socks5://user:pass@2001:db8::1:1080",
			expected: "socks5://user:pass@[2001:db8::1]:1080",
		},
		{
			name:     "Webshare standard format IP:PORT:USER:PASS",
			input:    "31.59.20.176:6754:jxpogrlk:suzliqoleexq",
			expected: "http://jxpogrlk:suzliqoleexq@31.59.20.176:6754",
		},
		{
			name:     "Alternative format USER:PASS:IP:PORT",
			input:    "jxpogrlk:suzliqoleexq:31.59.20.176:6754",
			expected: "http://jxpogrlk:suzliqoleexq@31.59.20.176:6754",
		},
		{
			name:     "Bracketed IPv6 with port and USER:PASS",
			input:    "[2001:db8::1]:8080:myuser:mypass",
			expected: "http://myuser:mypass@[2001:db8::1]:8080",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeProxyURL(tc.input)
			if got != tc.expected {
				t.Fatalf("NormalizeProxyURL(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestProxyManager_RoundRobinAndDeduplication(t *testing.T) {
	pm := NewProxyManager([]string{
		"http://1.1.1.1:8080",
		"http://1.1.1.1:8080", // duplicate
		"http://2.2.2.2:8080",
		"http://[2001:db8::1]:8080",
	}, "")

	if pm.Count() != 3 {
		t.Fatalf("pm.Count() = %d, want 3", pm.Count())
	}

	p1 := pm.NextProxy()
	p2 := pm.NextProxy()
	p3 := pm.NextProxy()
	p4 := pm.NextProxy()

	if p1 != "http://1.1.1.1:8080" || p2 != "http://2.2.2.2:8080" || p3 != "http://[2001:db8::1]:8080" {
		t.Fatalf("round robin failed: %q, %q, %q", p1, p2, p3)
	}
	if p4 != p1 {
		t.Fatalf("expected wrap around to %q, got %q", p1, p4)
	}
}

func TestPool_PushGetAndExpiry(t *testing.T) {
	pool := NewPool(3, 10, 100*time.Millisecond)

	// Below minLength (len < 10)
	if pool.Push(&Ticket{State: "short", ExpiresAt: time.Now().Add(time.Minute)}) {
		t.Fatalf("expected short state to be rejected")
	}

	longState1 := strings.Repeat("a", 20)
	longState2 := strings.Repeat("b", 20)
	longState3 := strings.Repeat("c", 20)
	longState4 := strings.Repeat("d", 20)

	pool.Push(&Ticket{State: longState1, ExpiresAt: time.Now().Add(time.Minute)})
	pool.Push(&Ticket{State: longState2, ExpiresAt: time.Now().Add(time.Minute)})
	pool.Push(&Ticket{State: longState3, ExpiresAt: time.Now().Add(time.Minute)})

	if pool.Len() != 3 {
		t.Fatalf("pool.Len() = %d, want 3", pool.Len())
	}

	// Max capacity reached: adding 4th should evict 1st (longState1)
	pool.Push(&Ticket{State: longState4, ExpiresAt: time.Now().Add(time.Minute)})
	if pool.Len() != 3 {
		t.Fatalf("pool.Len() = %d, want 3", pool.Len())
	}

	// Get ticket should return valid states in rotation
	ticket, ok := pool.GetTicket()
	if !ok || ticket == "" {
		t.Fatalf("expected valid ticket from pool")
	}

	// Test expiration
	shortLivedPool := NewPool(5, 10, 10*time.Millisecond)
	shortLivedPool.Push(&Ticket{State: strings.Repeat("z", 15), ExpiresAt: time.Now().Add(10 * time.Millisecond)})
	time.Sleep(20 * time.Millisecond)

	if shortLivedPool.Len() != 0 {
		t.Fatalf("expected pool to be empty after expiration, got %d", shortLivedPool.Len())
	}
	if _, ok := shortLivedPool.GetTicket(); ok {
		t.Fatalf("expected GetTicket to fail on expired pool")
	}
}

func TestManager_HeaderInjection(t *testing.T) {
	cfg := &config.Config{
		Codex: config.CodexConfig{
			TurnState: config.CodexTurnStateConfig{
				Enabled:     true,
				ForceInject: false,
				MinLength:   160,
			},
		},
	}

	mgr := &Manager{
		pool:         NewPool(10, 160, 10*time.Minute),
		proxyManager: NewProxyManager(nil, ""),
		cfg:          cfg,
	}

	highComputeTicket := strings.Repeat("H", 170)
	mgr.pool.Push(&Ticket{
		State:      highComputeTicket,
		Length:     len(highComputeTicket),
		ExpiresAt:  time.Now().Add(10 * time.Minute),
		AcquiredAt: time.Now(),
	})

	// Case 1: Empty headers -> should inject high-compute state
	h1 := http.Header{}
	mgr.InjectRequestHeader(h1, cfg)
	if h1.Get(HeaderName) != highComputeTicket {
		t.Fatalf("expected injected state %q, got %q", highComputeTicket, h1.Get(HeaderName))
	}

	// Case 2: Client provided degraded state (len < 160) -> should be replaced with high-compute state
	h2 := http.Header{}
	degradedState := "short-degraded-state-only-40-characters"
	h2.Set(HeaderName, degradedState)
	mgr.InjectRequestHeader(h2, cfg)
	if h2.Get(HeaderName) != highComputeTicket {
		t.Fatalf("expected degraded state to be replaced by high-compute state, got %q", h2.Get(HeaderName))
	}

	// Case 3: Client provided high-compute state (len >= 160) and ForceInject=false -> should retain client state
	h3 := http.Header{}
	clientHighCompute := strings.Repeat("C", 165)
	h3.Set(HeaderName, clientHighCompute)
	mgr.InjectRequestHeader(h3, cfg)
	if h3.Get(HeaderName) != clientHighCompute {
		t.Fatalf("expected client high-compute state to be retained, got %q", h3.Get(HeaderName))
	}

	// Case 4: ForceInject=true -> should override client state
	cfgForce := &config.Config{
		Codex: config.CodexConfig{
			TurnState: config.CodexTurnStateConfig{
				Enabled:     true,
				ForceInject: true,
				MinLength:   160,
			},
		},
	}
	h4 := http.Header{}
	h4.Set(HeaderName, clientHighCompute)
	mgr.InjectRequestHeader(h4, cfgForce)
	if h4.Get(HeaderName) != highComputeTicket {
		t.Fatalf("expected force inject to override client state, got %q", h4.Get(HeaderName))
	}
}

func TestManager_RecordResponseHeader(t *testing.T) {
	cfg := &config.Config{
		Codex: config.CodexConfig{
			TurnState: config.CodexTurnStateConfig{
				Enabled:   true,
				MinLength: 160,
			},
		},
	}

	mgr := &Manager{
		pool:         NewPool(10, 160, 10*time.Minute),
		proxyManager: NewProxyManager(nil, ""),
		cfg:          cfg,
	}

	// Record short response state -> ignored
	hShort := http.Header{}
	hShort.Set(HeaderName, "short-response")
	mgr.RecordResponseHeader(hShort, cfg, "auth-1", "gpt-5.3-codex")
	if mgr.pool.Len() != 0 {
		t.Fatalf("short response state should not be recorded, pool len=%d", mgr.pool.Len())
	}

	// Record full-fat response state -> added to pool
	hFull := http.Header{}
	fullState := strings.Repeat("F", 168)
	hFull.Set(HeaderName, fullState)
	mgr.RecordResponseHeader(hFull, cfg, "auth-1", "gpt-5.3-codex")
	if mgr.pool.Len() != 1 {
		t.Fatalf("full response state should be recorded, pool len=%d", mgr.pool.Len())
	}
}

type mockRoundTripper func(req *http.Request) (*http.Response, error)

func (m mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m(req)
}

func TestProber_ExecuteProbe(t *testing.T) {
	highComputeState := strings.Repeat("P", 165)
	degradedState := "too-short"

	var mockStatusCode int
	var mockHeaderState string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if mockHeaderState != "" {
			w.Header().Set(HeaderName, mockHeaderState)
		}
		w.WriteHeader(mockStatusCode)
		_, _ = w.Write([]byte(`data: {"type":"response.completed"}` + "\n\n"))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	cfg := &config.Config{
		Codex: config.CodexConfig{
			TurnState: config.CodexTurnStateConfig{
				Enabled:   true,
				MinLength: 160,
				Probe: config.CodexTurnStateProbeConfig{
					Enabled:   true,
					BaseURL:   server.URL,
					APIKey:    "test-api-key",
					TicketTTL: 10 * time.Minute,
				},
			},
		},
	}

	pool := NewPool(10, 160, 10*time.Minute)
	pm := NewProxyManager(nil, "")
	prober := NewProber(pool, pm, cfg, nil)

	// Test 1: Successful high-compute probe
	mockStatusCode = http.StatusOK
	mockHeaderState = highComputeState

	ticket, err := prober.ExecuteProbe(context.Background(), "")
	if err != nil {
		t.Fatalf("ExecuteProbe error: %v", err)
	}
	if ticket == nil || ticket.State != highComputeState {
		t.Fatalf("expected ticket state %q, got %#v", highComputeState, ticket)
	}
	if pool.Len() != 1 {
		t.Fatalf("pool.Len() = %d, want 1", pool.Len())
	}

	// Test 2: Degraded compute state (len < 160) -> probe should reject
	mockStatusCode = http.StatusOK
	mockHeaderState = degradedState

	_, errDegraded := prober.ExecuteProbe(context.Background(), "")
	if errDegraded == nil {
		t.Fatalf("expected error on degraded turn-state")
	}
	if pool.Len() != 1 {
		t.Fatalf("pool.Len() should still be 1 after rejected probe")
	}

	// Test 3: Upstream 429 status
	mockStatusCode = http.StatusTooManyRequests
	mockHeaderState = highComputeState

	_, err429 := prober.ExecuteProbe(context.Background(), "")
	if err429 == nil {
		t.Fatalf("expected error on 429 upstream")
	}
}
