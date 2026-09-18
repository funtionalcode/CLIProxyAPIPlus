package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/proxygateway"
)

// GetProxyGateway returns the current status and metrics of the proxy gateway.
func (h *Handler) GetProxyGateway(c *gin.Context) {
	stats := proxygateway.GetGateway().Stats()
	c.JSON(http.StatusOK, stats)
}

// PutProxyGateway updates and persists the proxy gateway configuration, applying changes immediately.
func (h *Handler) PutProxyGateway(c *gin.Context) {
	var payload struct {
		config.ProxyGatewayConfig
		AuthUser *string `json:"auth-user"`
		AuthPass *string `json:"auth-pass"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	previous := h.cfg.ProxyGateway
	h.cfg.ProxyGateway = payload.ProxyGatewayConfig
	h.cfg.ProxyGateway.AuthUser = previous.AuthUser
	h.cfg.ProxyGateway.AuthPass = previous.AuthPass
	if payload.AuthUser != nil {
		h.cfg.ProxyGateway.AuthUser = *payload.AuthUser
	}
	if payload.AuthPass != nil {
		h.cfg.ProxyGateway.AuthPass = *payload.AuthPass
	}
	h.cfg.SanitizeProxyGatewayConfig()
	if errApply := proxygateway.GetGateway().UpdateConfig(h.cfg.ProxyGateway); errApply != nil {
		h.cfg.ProxyGateway = previous
		_ = proxygateway.GetGateway().UpdateConfig(previous)
		c.JSON(http.StatusBadRequest, gin.H{"error": errApply.Error()})
		return
	}
	if !h.persistLocked(c) {
		h.cfg.ProxyGateway = previous
		_ = proxygateway.GetGateway().UpdateConfig(previous)
	}
}

type testProxyRequest struct {
	ProxyURL       string `json:"proxy_url"`
	ProxyURLSource string `json:"proxy_url_source"`
	Gateway        bool   `json:"gateway"`
}

// TestProxyGatewayProxy checks connectivity through a specific upstream proxy.
func (h *Handler) TestProxyGatewayProxy(c *gin.Context) {
	var req testProxyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	proxyURL := strings.TrimSpace(req.ProxyURL)
	source := strings.TrimSpace(req.ProxyURLSource)
	choices := 0
	if proxyURL != "" {
		choices++
	}
	if source != "" {
		choices++
	}
	if req.Gateway {
		choices++
	}
	if choices != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provide exactly one of proxy_url, proxy_url_source or gateway"})
		return
	}
	if req.Gateway {
		c.JSON(http.StatusOK, proxygateway.GetGateway().TestGateway(c.Request.Context()))
		return
	}
	if source != "" {
		c.JSON(http.StatusOK, proxygateway.GetGateway().TestProxySource(c.Request.Context(), source))
		return
	}

	latency, err := proxygateway.GetGateway().TestProxy(c.Request.Context(), proxyURL)
	if err != nil {
		c.JSON(http.StatusOK, proxygateway.TestResult{
			ProxyURL:  proxyURL,
			Success:   false,
			LatencyMs: latency,
			Error:     err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, proxygateway.TestResult{
		ProxyURL:  proxyURL,
		Success:   true,
		LatencyMs: latency,
	})
}
