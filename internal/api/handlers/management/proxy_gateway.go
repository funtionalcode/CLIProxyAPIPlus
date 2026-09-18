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
	var payload config.ProxyGatewayConfig
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.mu.Lock()
	h.cfg.ProxyGateway = payload
	h.cfg.SanitizeProxyGatewayConfig()
	saved := h.persistLocked(c)
	h.mu.Unlock()

	if !saved {
		return
	}

	_ = proxygateway.GetGateway().UpdateConfig(h.cfg.ProxyGateway)
}

type testProxyRequest struct {
	ProxyURL string `json:"proxy_url"`
}

// TestProxyGatewayProxy checks connectivity through a specific upstream proxy.
func (h *Handler) TestProxyGatewayProxy(c *gin.Context) {
	var req testProxyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	proxyURL := strings.TrimSpace(req.ProxyURL)
	if proxyURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "proxy_url cannot be empty"})
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
