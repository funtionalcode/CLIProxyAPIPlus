package api

import (
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

const (
	defaultManagerServiceProxyBase = "http://127.0.0.1:18317"
	managerServiceProxyHeader      = "X-CPA-Manager-Service"
)

var managerServiceProxyBaseEnvKeys = []string{
	"CPA_MANAGER_PROXY_BASE_URL",
	"CPA_MANAGER_SERVICE_BASE_URL",
	"CLI_PROXY_MANAGER_SERVICE_BASE_URL",
}

func (s *Server) managerServiceProxyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil || c.Request == nil || c.Request.URL == nil {
			c.Next()
			return
		}
		if !shouldProxyManagerServiceRequest(c.Request) {
			c.Next()
			return
		}
		s.proxyManagerService(c)
	}
}

func (s *Server) registerManagerServiceProxyRoutes() {
	if s == nil || s.engine == nil {
		return
	}

	s.engine.Any("/health", s.proxyManagerService)
	s.engine.Any("/status", s.proxyManagerService)
	s.engine.Any("/setup", s.proxyManagerService)
	s.engine.Any("/usage-service/*proxyPath", s.proxyManagerService)

	s.engine.Any("/v0/management/model-prices", s.proxyManagerService)
	s.engine.Any("/v0/management/model-prices/*proxyPath", s.proxyManagerService)
	s.engine.Any("/v0/management/api-key-aliases", s.proxyManagerService)
	s.engine.Any("/v0/management/api-key-aliases/*proxyPath", s.proxyManagerService)
	s.engine.Any("/v0/management/dashboard/summary", s.proxyManagerService)
	s.engine.Any("/v0/management/monitoring/header-snapshots", s.proxyManagerService)
	s.engine.Any("/v0/management/monitoring/analytics", s.proxyManagerService)
	s.engine.Any("/v0/management/account-action-candidates", s.proxyManagerService)
	s.engine.Any("/v0/management/account-action-candidates/*proxyPath", s.proxyManagerService)
	s.engine.Any("/v0/management/codex-inspection/*proxyPath", s.proxyManagerService)
	s.engine.Any("/v0/management/usage/accounts", s.proxyManagerService)
	s.engine.Any("/v0/management/usage/accounts/*proxyPath", s.proxyManagerService)
	s.engine.Any("/v0/management/usage/api-keys", s.proxyManagerService)
	s.engine.Any("/v0/management/usage/api-keys/*proxyPath", s.proxyManagerService)
	s.engine.Any("/v0/management/usage/realtime", s.proxyManagerService)
	s.engine.Any("/v0/management/usage/realtime/*proxyPath", s.proxyManagerService)
	s.engine.Any("/v0/management/usage/models", s.proxyManagerService)
	s.engine.Any("/v0/management/usage/models/*proxyPath", s.proxyManagerService)
}

func (s *Server) proxyManagerService(c *gin.Context) {
	target, err := managerServiceProxyTarget()
	if err != nil {
		log.WithError(err).Warn("manager service proxy target is invalid")
		c.JSON(http.StatusBadGateway, gin.H{"error": "manager service proxy target is invalid"})
		c.Abort()
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, proxyErr error) {
		log.WithError(proxyErr).Warn("manager service proxy request failed")
		http.Error(w, "manager service unavailable", http.StatusBadGateway)
	}
	proxy.ServeHTTP(managerServiceProxyResponseWriter{ResponseWriter: c.Writer}, c.Request)
	c.Abort()
}

type managerServiceProxyResponseWriter struct {
	gin.ResponseWriter
}

func (w managerServiceProxyResponseWriter) CloseNotify() <-chan bool {
	return make(chan bool)
}

func managerServiceProxyTarget() (*url.URL, error) {
	raw := ""
	for _, key := range managerServiceProxyBaseEnvKeys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			raw = value
			break
		}
	}
	if raw == "" {
		raw = defaultManagerServiceProxyBase
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("manager service proxy target must include scheme and host")
	}
	return parsed, nil
}

func shouldProxyManagerServiceRequest(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	path := strings.TrimRight(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}

	if isManagerServiceStandalonePath(path) {
		return true
	}
	if isManagerServiceOnlyManagementPath(path) {
		return true
	}
	if isConflictingManagerServicePath(path) {
		return strings.EqualFold(strings.TrimSpace(r.Header.Get(managerServiceProxyHeader)), "true")
	}
	return false
}

func isManagerServiceStandalonePath(path string) bool {
	switch {
	case path == "/health", path == "/status", path == "/setup":
		return true
	case path == "/usage-service" || strings.HasPrefix(path, "/usage-service/"):
		return true
	default:
		return false
	}
}

func isManagerServiceOnlyManagementPath(path string) bool {
	switch {
	case path == "/v0/management/model-prices" || strings.HasPrefix(path, "/v0/management/model-prices/"):
		return true
	case path == "/v0/management/api-key-aliases" || strings.HasPrefix(path, "/v0/management/api-key-aliases/"):
		return true
	case path == "/v0/management/dashboard/summary":
		return true
	case path == "/v0/management/monitoring/header-snapshots":
		return true
	case path == "/v0/management/monitoring/analytics":
		return true
	case path == "/v0/management/account-action-candidates" || strings.HasPrefix(path, "/v0/management/account-action-candidates/"):
		return true
	case strings.HasPrefix(path, "/v0/management/codex-inspection/"):
		return true
	case path == "/v0/management/usage/accounts" || strings.HasPrefix(path, "/v0/management/usage/accounts/"):
		return true
	case path == "/v0/management/usage/api-keys" || strings.HasPrefix(path, "/v0/management/usage/api-keys/"):
		return true
	case path == "/v0/management/usage/realtime" || strings.HasPrefix(path, "/v0/management/usage/realtime/"):
		return true
	case path == "/v0/management/usage/models" || strings.HasPrefix(path, "/v0/management/usage/models/"):
		return true
	default:
		return false
	}
}

func isConflictingManagerServicePath(path string) bool {
	switch path {
	case "/v0/management/usage", "/v0/management/usage/export", "/v0/management/usage/import":
		return true
	default:
		return false
	}
}
