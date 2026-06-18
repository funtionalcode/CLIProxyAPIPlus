package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterModelsForAuth_UsesPreMergedExcludedModelsAttribute(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OAuthExcludedModels: map[string][]string{
				"gemini-cli": {"gemini-2.5-pro"},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-gemini-cli",
		Provider: "gemini-cli",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":       "oauth",
			"excluded_models": "gemini-2.5-flash",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	models := registry.GetAvailableModelsByProvider("gemini-cli")
	if len(models) == 0 {
		t.Fatal("expected gemini-cli models to be registered")
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if strings.EqualFold(modelID, "gemini-2.5-flash") {
			t.Fatalf("expected model %q to be excluded by auth attribute", modelID)
		}
	}

	seenGlobalExcluded := false
	for _, model := range models {
		if model == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(model.ID), "gemini-2.5-pro") {
			seenGlobalExcluded = true
			break
		}
	}
	if !seenGlobalExcluded {
		t.Fatal("expected global excluded model to be present when attribute override is set")
	}
}

func TestRegisterModelsForAuth_OpenAICompatibilityImageModelType(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "images",
					BaseURL: "https://example.com/v1",
					Models: []config.OpenAICompatibilityModel{
						{Name: "upstream-image", Alias: "compat-image", Image: true},
						{Name: "upstream-chat", Alias: "compat-chat"},
						{Name: "gpt-5.5", Alias: "compat-gpt55"},
					},
				},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-openai-compat-image",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":    "api_key",
			"compat_name":  "images",
			"provider_key": "images",
		},
	}

	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	models := modelRegistry.GetModelsForClient(auth.ID)
	var imageModel *internalregistry.ModelInfo
	var chatModel *internalregistry.ModelInfo
	var gpt55Model *internalregistry.ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		switch strings.TrimSpace(model.ID) {
		case "compat-image":
			imageModel = model
		case "compat-chat":
			chatModel = model
		case "compat-gpt55":
			gpt55Model = model
		}
	}
	if imageModel == nil {
		t.Fatal("expected compat-image to be registered")
	}
	if imageModel.Type != internalregistry.OpenAIImageModelType {
		t.Fatalf("image model type = %q, want %q", imageModel.Type, internalregistry.OpenAIImageModelType)
	}
	if imageModel.Thinking != nil {
		t.Fatalf("image model thinking = %+v, want nil", imageModel.Thinking)
	}
	if chatModel == nil {
		t.Fatal("expected compat-chat to be registered")
	}
	if chatModel.Type != "openai-compatibility" {
		t.Fatalf("chat model type = %q, want openai-compatibility", chatModel.Type)
	}
	if chatModel.Thinking == nil {
		t.Fatal("expected chat model to keep default thinking support")
	}
	if gpt55Model == nil {
		t.Fatal("expected compat-gpt55 to be registered")
	}
	if gpt55Model.ContextLength != 272000 {
		t.Fatalf("compat gpt-5.5 context length = %d, want 272000", gpt55Model.ContextLength)
	}
	if gpt55Model.MaxCompletionTokens != 128000 {
		t.Fatalf("compat gpt-5.5 max completion tokens = %d, want 128000", gpt55Model.MaxCompletionTokens)
	}
	if gpt55Model.Thinking == nil || len(gpt55Model.Thinking.Levels) == 0 {
		t.Fatalf("expected compat gpt-5.5 to keep static thinking levels, got %+v", gpt55Model.Thinking)
	}
}

func TestRegisterModelsForAuth_CodexPrefixedConfigModelKeepsStaticTokenMetadata(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			CodexKey: []config.CodexKey{{
				APIKey:  "test-key",
				BaseURL: "https://example.com",
				Prefix:  "codex-fast",
				Models: []internalconfig.CodexModel{{
					Name: "gpt-5.5",
				}},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-codex-prefixed-gpt55",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Prefix:   "codex-fast",
		Attributes: map[string]string{
			"auth_kind": "api_key",
			"api_key":   "test-key",
			"base_url":  "https://example.com",
		},
	}

	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	info := modelRegistry.GetModelInfo("codex-fast/gpt-5.5", "codex")
	if info == nil {
		t.Fatal("expected prefixed gpt-5.5 model to be registered")
	}
	if info.ContextLength != 272000 {
		t.Fatalf("context length = %d, want 272000", info.ContextLength)
	}
	if info.MaxCompletionTokens != 128000 {
		t.Fatalf("max completion tokens = %d, want 128000", info.MaxCompletionTokens)
	}
	if info.Thinking == nil || len(info.Thinking.Levels) == 0 {
		t.Fatalf("expected static thinking levels, got %+v", info.Thinking)
	}

	var claudeModel map[string]any
	for _, model := range modelRegistry.GetAvailableModels("claude") {
		if strings.TrimSpace(stringValue(model["id"])) == "codex-fast/gpt-5.5" {
			claudeModel = model
			break
		}
	}
	if claudeModel == nil {
		t.Fatal("expected prefixed gpt-5.5 model in Claude-compatible model list")
	}
	for _, key := range []string{"context_length", "context_window", "context_window_size", "context_window_tokens", "contextWindow", "max_context_window"} {
		if got, ok := claudeModel[key].(int); !ok || got != 272000 {
			t.Fatalf("%s = %#v, want 272000", key, claudeModel[key])
		}
	}
	for _, key := range []string{"max_completion_tokens", "max_output_tokens", "output_token_limit"} {
		if got, ok := claudeModel[key].(int); !ok || got != 128000 {
			t.Fatalf("%s = %#v, want 128000", key, claudeModel[key])
		}
	}
}

func TestRegisterModelsForAuth_CodexConfigModelKeepsStaticTokenMetadata(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			CodexKey: []config.CodexKey{{
				APIKey:  "test-key",
				BaseURL: "https://example.com",
				Models: []internalconfig.CodexModel{{
					Name: "gpt-5.5",
				}},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-codex-plain-gpt55",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "api_key",
			"api_key":   "test-key",
			"base_url":  "https://example.com",
		},
	}

	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	info := modelRegistry.GetModelInfo("gpt-5.5", "codex")
	if info == nil {
		t.Fatal("expected gpt-5.5 model to be registered")
	}
	if info.ContextLength != 272000 {
		t.Fatalf("context length = %d, want 272000", info.ContextLength)
	}
	if info.MaxCompletionTokens != 128000 {
		t.Fatalf("max completion tokens = %d, want 128000", info.MaxCompletionTokens)
	}
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func TestRegisterModelsForAuth_AntigravityFetchesWebSearchCapability(t *testing.T) {
	var sawFetch bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != antigravityModelsPath {
			t.Fatalf("path = %q, want %s", r.URL.Path, antigravityModelsPath)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		sawFetch = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
				"models": {
					"gemini-3.1-flash-lite": {
						"displayName": "Gemini 3.1 Flash Lite",
						"maxTokens": 1,
						"maxOutputTokens": 2
					},
					"fetched-only-search-model": {
						"displayName": "Fetched Only Search Model"
					}
				},
				"webSearchModelIds": ["gemini-3.1-flash-lite", "fetched-only-search-model"]
			}`))
	}))
	defer server.Close()

	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "auth-antigravity-fetch-models",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
		},
	}

	registry := internalregistry.GetGlobalRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)
	if !sawFetch {
		t.Fatal("expected fetchAvailableModels request")
	}

	models := registry.GetModelsForClient(auth.ID)
	staticModels := internalregistry.GetAntigravityModels()
	staticByID := make(map[string]*internalregistry.ModelInfo, len(staticModels))
	for _, model := range staticModels {
		if model != nil {
			staticByID[model.ID] = model
		}
	}

	var webSearchModel, agentModel, staticOnlyModel, fetchedOnlyModel *internalregistry.ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		switch strings.TrimSpace(model.ID) {
		case "gemini-3.1-flash-lite":
			webSearchModel = model
		case "gemini-3-flash-agent":
			agentModel = model
		case "gpt-oss-120b-medium":
			staticOnlyModel = model
		case "fetched-only-search-model":
			fetchedOnlyModel = model
		}
	}
	if webSearchModel == nil {
		t.Fatal("expected gemini-3.1-flash-lite to be registered")
	}
	if !webSearchModel.SupportsWebSearch {
		t.Fatal("expected gemini-3.1-flash-lite to support web search")
	}
	staticWebSearchModel := staticByID["gemini-3.1-flash-lite"]
	if staticWebSearchModel == nil {
		t.Fatal("expected static gemini-3.1-flash-lite definition")
	}
	if webSearchModel.ContextLength != staticWebSearchModel.ContextLength || webSearchModel.MaxCompletionTokens != staticWebSearchModel.MaxCompletionTokens {
		t.Fatalf("static token limits should be preserved, got=%#v static=%#v", webSearchModel, staticWebSearchModel)
	}
	if agentModel == nil {
		t.Fatal("expected gemini-3-flash-agent to be registered")
	}
	if agentModel.SupportsWebSearch {
		t.Fatal("gemini-3-flash-agent should not support web search")
	}
	if staticOnlyModel == nil {
		t.Fatal("expected static-only Antigravity model to remain registered")
	}
	if fetchedOnlyModel != nil {
		t.Fatalf("fetched-only model should not be registered: %#v", fetchedOnlyModel)
	}
}
