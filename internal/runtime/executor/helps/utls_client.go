package helps

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

type utlsClientProfile int

const (
	utlsProfileClaudeCode utlsClientProfile = iota
	utlsProfileClaudeCodeHTTP1
	utlsProfileCodexCLI
)

// utlsRoundTripper implements http.RoundTripper using utls with a provider
// client fingerprint and HTTP/2.
type utlsRoundTripper struct {
	mu          sync.Mutex
	connections map[string]*http2.ClientConn
	pending     map[string]*sync.Cond
	dialer      proxy.Dialer
	profile     utlsClientProfile
}

func newUtlsRoundTripper(proxyURL string, profile utlsClientProfile) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("utls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return &utlsRoundTripper{
		connections: make(map[string]*http2.ClientConn),
		pending:     make(map[string]*sync.Cond),
		dialer:      dialer,
		profile:     profile,
	}
}

func (t *utlsRoundTripper) getOrCreateConnection(host, addr string) (*http2.ClientConn, error) {
	t.mu.Lock()

	if h2Conn, ok := t.connections[host]; ok && h2Conn.CanTakeNewRequest() {
		t.mu.Unlock()
		return h2Conn, nil
	}

	if cond, ok := t.pending[host]; ok {
		cond.Wait()
		if h2Conn, ok := t.connections[host]; ok && h2Conn.CanTakeNewRequest() {
			t.mu.Unlock()
			return h2Conn, nil
		}
	}

	cond := sync.NewCond(&t.mu)
	t.pending[host] = cond
	t.mu.Unlock()

	h2Conn, err := t.createConnection(host, addr)

	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.pending, host)
	cond.Broadcast()

	if err != nil {
		return nil, err
	}

	t.connections[host] = h2Conn
	return h2Conn, nil
}

func (t *utlsRoundTripper) createConnection(host, addr string) (*http2.ClientConn, error) {
	tlsConn, err := dialUTLSConn(t.dialer, host, addr, t.profile)
	if err != nil {
		return nil, err
	}

	tr := &http2.Transport{}
	h2Conn, err := tr.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, err
	}

	return h2Conn, nil
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

	h2Conn, err := t.getOrCreateConnection(hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		t.mu.Lock()
		if cached, ok := t.connections[hostname]; ok && cached == h2Conn {
			delete(t.connections, hostname)
		}
		t.mu.Unlock()
		return nil, err
	}

	return resp, nil
}

type utlsHTTP1RoundTripper struct {
	transport *http.Transport
}

func newUtlsHTTP1RoundTripper(proxyURL string, profile utlsClientProfile) *utlsHTTP1RoundTripper {
	dialer := newUTLSDialer(proxyURL)
	transport := &http.Transport{
		ForceAttemptHTTP2: false,
		DialTLSContext: func(_ context.Context, _ string, addr string) (net.Conn, error) {
			host, _, errSplit := net.SplitHostPort(addr)
			if errSplit != nil {
				host = addr
			}
			return dialUTLSConn(dialer, host, addr, profile)
		},
	}
	return &utlsHTTP1RoundTripper{transport: transport}
}

func (t *utlsHTTP1RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.transport.RoundTrip(req)
}

func newUTLSDialer(proxyURL string) proxy.Dialer {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL == "" {
		return dialer
	}
	proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
	if errBuild != nil {
		log.Errorf("utls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		return dialer
	}
	if mode != proxyutil.ModeInherit && proxyDialer != nil {
		return proxyDialer
	}
	return dialer
}

func dialUTLSConn(dialer proxy.Dialer, host, addr string, profile utlsClientProfile) (*tls.UConn, error) {
	if dialer == nil {
		dialer = proxy.Direct
	}
	conn, errDial := dialer.Dial("tcp", addr)
	if errDial != nil {
		return nil, errDial
	}
	tlsConn := tls.UClient(conn, &tls.Config{ServerName: host}, tls.HelloCustom)
	spec := utlsClientHelloSpec(profile)
	if errApply := tlsConn.ApplyPreset(&spec); errApply != nil {
		conn.Close()
		return nil, errApply
	}
	if errHandshake := tlsConn.Handshake(); errHandshake != nil {
		conn.Close()
		return nil, errHandshake
	}
	return tlsConn, nil
}

func utlsClientHelloSpec(profile utlsClientProfile) tls.ClientHelloSpec {
	switch profile {
	case utlsProfileClaudeCodeHTTP1:
		return claudeCodeClientHelloSpec([]string{"http/1.1"})
	case utlsProfileCodexCLI:
		return tls.ClientHelloSpec{
			TLSVersMin:         tls.VersionTLS10,
			TLSVersMax:         tls.VersionTLS12,
			CompressionMethods: []uint8{0},
			CipherSuites: []uint16{
				tls.FAKE_TLS_EMPTY_RENEGOTIATION_INFO_SCSV,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				0xc024,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
				tls.FAKE_TLS_ECDHE_ECDSA_WITH_3DES_EDE_CBC_SHA,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				0xc028,
				tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
				tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
				tls.TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA,
				tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
				0x003d,
				tls.TLS_RSA_WITH_AES_128_CBC_SHA256,
				tls.TLS_RSA_WITH_AES_256_CBC_SHA,
				tls.TLS_RSA_WITH_AES_128_CBC_SHA,
				tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA,
			},
			Extensions: []tls.TLSExtension{
				&tls.SNIExtension{},
				&tls.SupportedCurvesExtension{Curves: []tls.CurveID{tls.CurveP256, tls.CurveP384, tls.CurveP521}},
				&tls.SupportedPointsExtension{SupportedPoints: []byte{0}},
				&tls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: defaultSignatureAlgorithms()},
				&tls.StatusRequestExtension{},
				&tls.SCTExtension{},
				&tls.ExtendedMasterSecretExtension{},
			},
		}
	default:
		return claudeCodeClientHelloSpec([]string{"h2", "http/1.1"})
	}
}

func claudeCodeClientHelloSpec(alpnProtocols []string) tls.ClientHelloSpec {
	return tls.ClientHelloSpec{
		TLSVersMin:         tls.VersionTLS12,
		TLSVersMax:         tls.VersionTLS13,
		CompressionMethods: []uint8{0},
		CipherSuites: []uint16{
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
		Extensions: []tls.TLSExtension{
			&tls.SNIExtension{},
			&tls.ExtendedMasterSecretExtension{},
			&tls.RenegotiationInfoExtension{Renegotiation: tls.RenegotiateOnceAsClient},
			&tls.SupportedCurvesExtension{Curves: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384}},
			&tls.SupportedPointsExtension{SupportedPoints: []byte{0}},
			&tls.SessionTicketExtension{},
			&tls.ALPNExtension{AlpnProtocols: alpnProtocols},
			&tls.StatusRequestExtension{},
			&tls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: defaultSignatureAlgorithms()},
			&tls.SCTExtension{},
			&tls.KeyShareExtension{KeyShares: []tls.KeyShare{{Group: tls.X25519}}},
			&tls.PSKKeyExchangeModesExtension{Modes: []uint8{tls.PskModeDHE}},
			&tls.SupportedVersionsExtension{Versions: []uint16{tls.VersionTLS13, tls.VersionTLS12}},
			&tls.UtlsPaddingExtension{GetPaddingLen: tls.BoringPaddingStyle},
		},
	}
}

func defaultSignatureAlgorithms() []tls.SignatureScheme {
	return []tls.SignatureScheme{
		tls.ECDSAWithP256AndSHA256,
		tls.ECDSAWithP384AndSHA384,
		tls.ECDSAWithP521AndSHA512,
		tls.PSSWithSHA256,
		tls.PSSWithSHA384,
		tls.PSSWithSHA512,
		tls.PKCS1WithSHA256,
		tls.PKCS1WithSHA384,
		tls.PKCS1WithSHA512,
		tls.ECDSAWithSHA1,
		tls.PKCS1WithSHA1,
	}
}

// utlsProtectedHosts contains the hosts that should use provider-specific uTLS
// fingerprints to avoid the default Go TLS fingerprint.
var utlsProtectedHosts = map[string]struct{}{
	"api.anthropic.com": {},
	"chatgpt.com":       {},
}

// fallbackRoundTripper uses uTLS for protected HTTPS hosts and falls back to
// standard transport for all other requests.
type fallbackRoundTripper struct {
	utls     http.RoundTripper
	fallback http.RoundTripper
}

func (f *fallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" {
		if _, ok := utlsProtectedHosts[strings.ToLower(req.URL.Hostname())]; ok {
			return f.utls.RoundTrip(req)
		}
	}
	return f.fallback.RoundTrip(req)
}

// NewUtlsHTTPClient creates an HTTP client using the Claude Code TLS fingerprint.
// Use this for Claude API requests to match real Claude Code's TLS behavior.
// Falls back to standard transport for non-HTTPS requests.
func NewUtlsHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	var ctxRoundTripper http.RoundTripper
	if ctx != nil {
		ctxRoundTripper, _ = ctx.Value("cliproxy.roundtripper").(http.RoundTripper)
	}

	var utlsRT http.RoundTripper
	if cfg != nil && cfg.Claude.TLS.HTTP1Only {
		utlsRT = newUtlsHTTP1RoundTripper(proxyURL, utlsProfileClaudeCodeHTTP1)
	} else {
		utlsRT = newUtlsRoundTripper(proxyURL, utlsProfileClaudeCode)
	}
	var standardTransport http.RoundTripper = http.DefaultTransport
	if proxyURL != "" {
		if transport := buildProxyTransport(proxyURL); transport != nil {
			standardTransport = transport
		}
	} else if ctxRoundTripper != nil {
		utlsRT = ctxRoundTripper
		standardTransport = ctxRoundTripper
	}

	client := &http.Client{
		Transport: &fallbackRoundTripper{
			utls:     utlsRT,
			fallback: standardTransport,
		},
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return withAPIRequestLoggingHTTPClient(ctx, cfg, auth, client, timeout)
}

func NewCodexFingerprintHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	if proxyURL == "" && ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			client := &http.Client{Transport: rt}
			if timeout > 0 {
				client.Timeout = timeout
			}
			return withAPIRequestLoggingHTTPClient(ctx, cfg, auth, client, timeout)
		}
	}
	client := &http.Client{Transport: newUtlsHTTP1RoundTripper(proxyURL, utlsProfileCodexCLI)}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return withAPIRequestLoggingHTTPClient(ctx, cfg, auth, client, timeout)
}

func CodexFingerprintDialTLSContext(cfg *config.Config, auth *cliproxyauth.Auth) func(context.Context, string, string) (net.Conn, error) {
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	dialer := newUTLSDialer(proxyURL)
	return func(_ context.Context, _ string, addr string) (net.Conn, error) {
		host, _, errSplit := net.SplitHostPort(addr)
		if errSplit != nil {
			host = addr
		}
		return dialUTLSConn(dialer, host, addr, utlsProfileCodexCLI)
	}
}
