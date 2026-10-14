package registry

import "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps/geminiweb"

// GetGeminiWebModels exposes text-only web modes with distinct API model IDs.
func GetGeminiWebModels() []*ModelInfo {
	var models []*ModelInfo
	for _, model := range geminiweb.Models() {
		models = append(models, &ModelInfo{
			ID: model.ID, Object: "model", OwnedBy: "google", Type: "gemini-web", DisplayName: model.Name,
			SupportedEndpoints:       []string{"/chat/completions", "/responses", "/messages"},
			SupportedInputModalities: []string{"TEXT"}, SupportedOutputModalities: []string{"TEXT"},
			SupportedParameters: []string{"stream"},
		})
	}
	return models
}
