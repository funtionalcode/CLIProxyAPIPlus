package management

import (
	"net/http"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

// PreviewAntigravityPrompt applies the runtime prompt processor without an upstream call.
func (h *Handler) PreviewAntigravityPrompt(c *gin.Context) {
	var input struct {
		Model  string `json:"model"`
		System string `json:"system"`
	}
	if errBind := c.ShouldBindJSON(&input); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	h.mu.Lock()
	cfg := h.cfg.CloneForRuntime()
	h.mu.Unlock()
	doc := ojson.NewDocString(`{"request":{}}`)
	_ = doc.Set("request.systemInstruction.parts", []any{map[string]any{"text": input.System}})
	if errPrompt := helps.ApplyAntigravitySystemPromptDoc(cfg, helps.AntigravityProfile(cfg, nil), input.Model, doc); errPrompt != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errPrompt.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "application/json", doc.Bytes())
}
