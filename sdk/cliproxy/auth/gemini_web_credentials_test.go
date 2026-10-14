package auth

import (
	"context"
	"testing"
)

func TestGeminiWebCookieReplacementResetsUnauthorizedState(t *testing.T) {
	for _, key := range []string{"secure_1psid", "secure_1psidts"} {
		t.Run(key, func(t *testing.T) {
			ctx := WithSkipPersist(context.Background())
			store := &recordingCooldownStateStore{}
			manager := NewManager(nil, nil, nil)
			manager.SetCooldownStateStore(store)
			auth := &Auth{ID: "gemini-web-test", Provider: "gemini-web", Status: StatusActive,
				Metadata: map[string]any{"email": "original", "secure_1psid": "session", "secure_1psidts": "timestamp"}}
			if _, err := manager.Register(ctx, auth); err != nil {
				t.Fatal(err)
			}
			manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "gemini-web-3.5-flash-lite", Success: false, Error: &Error{Message: "unauthorized", HTTPStatus: 401}})
			before, _ := manager.GetByID(auth.ID)
			renamed := before.Clone()
			renamed.Metadata["email"] = "renamed"
			if CredentialsChanged(before, renamed) {
				t.Fatal("name change is not a credential change")
			}
			if _, err := manager.Update(ctx, renamed); err != nil {
				t.Fatal(err)
			}
			if len(store.savedRecords()) == 0 {
				t.Fatal("rename unexpectedly cleared authentication failure")
			}
			updated := renamed.Clone()
			updated.Metadata[key] = "replacement"
			if _, err := manager.Update(ctx, updated); err != nil {
				t.Fatal(err)
			}
			after, _ := manager.GetByID(auth.ID)
			if after.CredentialVersion <= before.CredentialVersion {
				t.Fatal("credential version did not advance")
			}
			if len(store.savedRecords()) != 0 {
				t.Fatal("old unauthorized cooldown remains")
			}
			if state := after.ModelStates["gemini-web-3.5-flash-lite"]; state != nil && state.LastError != nil {
				t.Fatal("old unauthorized model error remains")
			}
		})
	}
}
