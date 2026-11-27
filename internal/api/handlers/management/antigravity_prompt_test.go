package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestAntigravityPromptPreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"rewrite", "block"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Antigravity.SystemPrompt.ClientFilter.Mode = mode
			h := &Handler{cfg: cfg}
			router := gin.New()
			router.POST("/preview", h.PreviewAntigravityPrompt)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(`{"model":"gemini","system":"Use Codex"}`)))
			if mode == "block" {
				if response.Code != http.StatusBadRequest {
					t.Fatalf("status=%d", response.Code)
				}
				return
			}
			if response.Code != http.StatusOK || ojson.Get(response.Body.String(), "request.systemInstruction.parts.1.text").String() != "Use Antigravity" {
				t.Fatalf("unexpected preview: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
