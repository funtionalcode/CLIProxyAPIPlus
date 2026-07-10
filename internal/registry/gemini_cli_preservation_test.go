package registry

import (
	"strings"
	"testing"
)

func TestGeminiCLIStaticModelsRemainAddressable(t *testing.T) {
	models := GetStaticModelDefinitionsByChannel("gemini-cli")
	if len(models) == 0 {
		t.Fatal("GetStaticModelDefinitionsByChannel(gemini-cli) returned no models")
	}
	if got := LookupStaticModelInfo(models[0].ID); got == nil {
		t.Fatalf("LookupStaticModelInfo(%q) = nil", models[0].ID)
	}
}

func TestDetectChangedProvidersIncludesGeminiCLI(t *testing.T) {
	oldData := &staticModelsJSON{GeminiCLI: []*ModelInfo{{ID: "gemini-old"}}}
	newData := &staticModelsJSON{GeminiCLI: []*ModelInfo{{ID: "gemini-new"}}}

	changed := detectChangedProviders(oldData, newData)
	if !containsProvider(changed, "gemini-cli") {
		t.Fatalf("detectChangedProviders() = %v, want gemini-cli", changed)
	}
}

func TestValidateModelsCatalogRequiresGeminiCLI(t *testing.T) {
	data := *getModels()
	data.GeminiCLI = []*ModelInfo{nil}

	err := validateModelsCatalog(&data)
	if err == nil || !strings.Contains(err.Error(), "gemini-cli") {
		t.Fatalf("validateModelsCatalog() error = %v, want gemini-cli validation error", err)
	}
}

func containsProvider(providers []string, want string) bool {
	for _, provider := range providers {
		if provider == want {
			return true
		}
	}
	return false
}
