package management

import "testing"

func TestCodexRefreshPreservedMetadataKeepsCustomFields(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"id_token": "old-id",
		"access_token": "old-access",
		"refresh_token": "old-refresh",
		"account_id": "old-account",
		"last_refresh": "old-refresh-time",
		"email": "old@example.com",
		"type": "codex",
		"expired": "old-expire",
		"model_aliases": "gpt-5.3-codex-spark=gpt-5.4",
		"priority": 10
	}`)

	got := codexRefreshPreservedMetadata(raw)
	if got == nil {
		t.Fatal("codexRefreshPreservedMetadata() = nil")
	}
	if got["model_aliases"] != "gpt-5.3-codex-spark=gpt-5.4" {
		t.Fatalf("model_aliases = %v, want alias mapping", got["model_aliases"])
	}
	if got["priority"] != float64(10) {
		t.Fatalf("priority = %v, want 10", got["priority"])
	}
	for _, key := range []string{"id_token", "access_token", "refresh_token", "account_id", "last_refresh", "email", "type", "expired"} {
		if _, exists := got[key]; exists {
			t.Fatalf("%s should not be preserved", key)
		}
	}
}
