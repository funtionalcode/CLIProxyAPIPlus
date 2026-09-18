package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/turnstate"
)

// GetCodexTurnState returns current status, metrics, and tickets for the Codex turn-state subsystem.
func (h *Handler) GetCodexTurnState(c *gin.Context) {
	stats := turnstate.GetManager().Stats()
	c.JSON(http.StatusOK, stats)
}

// ProbeCodexTurnState triggers an immediate probe execution via the next available proxy.
func (h *Handler) ProbeCodexTurnState(c *gin.Context) {
	ticket, err := turnstate.GetManager().ProbeOnce(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"ticket": ticket,
	})
}
