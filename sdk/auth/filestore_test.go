package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractAccessToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata map[string]any
		expected string
	}{
		{
			"antigravity top-level access_token",
			map[string]any{"access_token": "tok-abc"},
			"tok-abc",
		},
		{
			"gemini nested token.access_token",
			map[string]any{
				"token": map[string]any{"access_token": "tok-nested"},
			},
			"tok-nested",
		},
		{
			"top-level takes precedence over nested",
			map[string]any{
				"access_token": "tok-top",
				"token":        map[string]any{"access_token": "tok-nested"},
			},
			"tok-top",
		},
		{
			"empty metadata",
			map[string]any{},
			"",
		},
		{
			"whitespace-only access_token",
			map[string]any{"access_token": "   "},
			"",
		},
		{
			"wrong type access_token",
			map[string]any{"access_token": 12345},
			"",
		},
		{
			"token is not a map",
			map[string]any{"token": "not-a-map"},
			"",
		},
		{
			"nested whitespace-only",
			map[string]any{
				"token": map[string]any{"access_token": "  "},
			},
			"",
		},
		{
			"fallback to nested when top-level empty",
			map[string]any{
				"access_token": "",
				"token":        map[string]any{"access_token": "tok-fallback"},
			},
			"tok-fallback",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractAccessToken(tt.metadata)
			if got != tt.expected {
				t.Errorf("extractAccessToken() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestInferCodexPlanTypeFromFilename(t *testing.T) {
	t.Parallel()

	got := inferCodexPlanType(map[string]any{"type": "codex"}, "codex-user@example.com-pro.json")
	if got != "pro" {
		t.Fatalf("inferCodexPlanType() = %q, want pro", got)
	}
}

func TestInferCodexPlanTypePrefersMetadata(t *testing.T) {
	t.Parallel()

	got := inferCodexPlanType(map[string]any{
		"type":      "codex",
		"plan_type": "plus",
	}, "codex-user@example.com-free.json")
	if got != "plus" {
		t.Fatalf("inferCodexPlanType() = %q, want plus", got)
	}
}

func TestFileTokenStoreListInfersCodexPlanTypeFromFilename(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "codex-user@example.com-pro.json"),
		[]byte(`{"type":"codex","email":"user@example.com","access_token":"access","refresh_token":"refresh"}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	auths, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("len(auths) = %d, want 1", len(auths))
	}
	if got := auths[0].Attributes["plan_type"]; got != "pro" {
		t.Fatalf("Attributes[plan_type] = %q, want pro", got)
	}
}
