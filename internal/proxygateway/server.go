package proxygateway

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/proxytrace"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

var hopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// Server executes the local forward proxy gateway.
type Server struct {
	poolMgr           *PoolManager
	authUser          string
	authPass          string
	totalRequests     atomic.Uint64
	activeConnections atomic.Int64
	totalErrors       atomic.Uint64
}

// NewServer creates a new forward proxy Server.
func NewServer(poolMgr *PoolManager, authUser, authPass string) *Server {
	return &Server{
		poolMgr:  poolMgr,
		authUser: strings.TrimSpace(authUser),
		authPass: strings.TrimSpace(authPass),
	}
}

// UpdateAuth updates basic authentication credentials.
func (s *Server) UpdateAuth(user, pass string) {
	s.authUser = strings.TrimSpace(user)
	s.authPass = strings.TrimSpace(pass)
}

func (s *Server) checkAuth(r *http.Request) bool {
	if s.authUser == "" && s.authPass == "" {
		return true
	}

	authHeader := r.Header.Get("Proxy-Authorization")
	if authHeader == "" {
		return false
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Basic") {
		return false
	}

	payload, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}

	pair := strings.SplitN(string(payload), ":", 2)
	if len(pair) != 2 {
		return false
	}

	return pair[0] == s.authUser && pair[1] == s.authPass
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.totalRequests.Add(1)
	s.activeConnections.Add(1)
	defer s.activeConnections.Add(-1)

	if !s.checkAuth(r) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="Proxy Gateway"`)
		http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
		return
	}

	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
	} else {
		s.handleHTTP(w, r)
	}
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	selection := s.poolMgr.NextSelection()
	upstream := selection.Proxy
	traceID := strings.TrimSpace(r.Header.Get(proxyutil.GatewayTraceHeader))
	if len(traceID) == 32 && isSafeGatewayTraceID(traceID) {
		proxytrace.Record(traceID, proxytrace.Route{
			Pool:   selection.Pool,
			Proxy:  redactProxyEndpoint(upstream),
			Target: r.Host,
		})
	}

	dialer, _, err := proxyutil.BuildDialer(upstream)
	if err != nil {
		s.totalErrors.Add(1)
		http.Error(w, fmt.Sprintf("failed to configure upstream dialer: %v", err), http.StatusBadGateway)
		return
	}
	var targetConn net.Conn
	if dialer != nil {
		targetConn, err = dialer.Dial("tcp", r.Host)
	} else {
		targetConn, err = net.DialTimeout("tcp", r.Host, 10*time.Second)
	}
	if err != nil {
		s.totalErrors.Add(1)
		log.Debugf("proxy gateway: connect error to %s via %s: %v", r.Host, upstream, err)
		http.Error(w, fmt.Sprintf("failed to reach destination via proxy: %v", err), http.StatusBadGateway)
		return
	}
	defer targetConn.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		s.totalErrors.Add(1)
		http.Error(w, "webserver does not support hijacking", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		s.totalErrors.Add(1)
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer clientConn.Close()

	_, err = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if err != nil {
		s.totalErrors.Add(1)
		return
	}

	// Full-duplex tunnel relay
	errChan := make(chan error, 2)
	go func() {
		_, errCopy := io.Copy(targetConn, clientConn)
		errChan <- errCopy
	}()
	go func() {
		_, errCopy := io.Copy(clientConn, targetConn)
		errChan <- errCopy
	}()

	<-errChan
}

func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	selection := s.poolMgr.NextSelection()
	upstream := selection.Proxy

	transport, _, err := proxyutil.BuildHTTPTransport(upstream)
	if err != nil {
		s.totalErrors.Add(1)
		http.Error(w, fmt.Sprintf("failed to configure transport: %v", err), http.StatusBadGateway)
		return
	}
	if transport == nil {
		transport = proxyutil.NewDirectTransport()
	}

	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""

	for _, h := range hopHeaders {
		outReq.Header.Del(h)
	}

	resp, err := transport.RoundTrip(outReq)
	if err != nil {
		s.totalErrors.Add(1)
		log.Debugf("proxy gateway: roundtrip error to %s via %s: %v", r.URL.String(), upstream, err)
		http.Error(w, fmt.Sprintf("failed to fetch URL via proxy: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	for _, h := range hopHeaders {
		w.Header().Del(h)
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func isSafeGatewayTraceID(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if (ch < 'a' || ch > 'f') && (ch < '0' || ch > '9') {
			return false
		}
	}
	return true
}

func redactProxyEndpoint(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "direct"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<invalid proxy>"
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
}

// Metrics returns runtime statistics.
func (s *Server) Metrics() (totalReq uint64, activeConn int64, totalErr uint64) {
	return s.totalRequests.Load(), s.activeConnections.Load(), s.totalErrors.Load()
}
