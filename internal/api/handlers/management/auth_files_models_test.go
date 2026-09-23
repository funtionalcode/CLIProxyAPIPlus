package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetAuthFileModelsDisabledCodexUsesCapabilityCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	baseModels := registry.GetCodexFreeModels()
	if len(baseModels) < 2 {
		t.Fatal("expected at least two Codex free models")
	}

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "disabled-codex-model-catalog-test",
		FileName: "disabled-codex.json",
		Provider: "codex",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
		Attributes: map[string]string{
			"plan_type":       "free",
			"excluded_models": baseModels[0].ID,
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	router := gin.New()
	router.GET("/models", handler.GetAuthFileModels)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/models?name=disabled-codex.json", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Models) == 0 {
		t.Fatal("disabled Codex auth returned an empty capability catalog")
	}
	for _, model := range response.Models {
		if model.ID == baseModels[0].ID {
			t.Fatalf("excluded model %q was returned", model.ID)
		}
	}
}
