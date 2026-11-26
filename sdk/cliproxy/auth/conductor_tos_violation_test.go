package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TOS failures retain diagnostic details and normal 403 cooling, but never
// change the operator-controlled enabled state of an account or model.
func TestMarkResult_TOSViolation_DoesNotDisableCredential(t *testing.T) {
	for _, tc := range []struct {
		name           string
		model          string
		disableCooling bool
	}{
		{name: "model", model: "gemini-3-pro"},
		{name: "auth"},
		{name: "model without cooling", model: "gemini-3-pro", disableCooling: true},
		{name: "auth without cooling", disableCooling: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			ctx := context.Background()
			auth := &Auth{
				ID: "tos-cred", Provider: "antigravity", Status: StatusActive,
				Metadata: map[string]any{"disabled": false, "disable_cooling": tc.disableCooling},
			}
			if _, err := manager.Register(ctx, auth); err != nil {
				t.Fatalf("register auth: %v", err)
			}
			manager.MarkResult(ctx, Result{
				AuthID: auth.ID, Provider: "antigravity", Model: tc.model, CredentialScope: true,
				Error: &Error{
					HTTPStatus: http.StatusForbidden,
					Code:       "tos_violation",
					Message:    "TOS_VIOLATION",
					Metadata:   map[string]string{"tos_appeal_url": "https://forms.gle/x", "tos_appeal_url_text": "Submit Appeal"},
				},
			})

			updated, ok := manager.GetByID(auth.ID)
			if !ok || updated == nil {
				t.Fatal("auth not found after MarkResult")
			}
			if updated.Status != StatusError || updated.Disabled || updated.Metadata["disabled"] != false {
				t.Fatalf("TOS failure changed enabled state: status=%q disabled=%v metadata=%v", updated.Status, updated.Disabled, updated.Metadata["disabled"])
			}
			if updated.StatusMessage != "tos_violation" || updated.LastError == nil || updated.LastError.Code != "tos_violation" {
				t.Fatalf("TOS diagnostic lost: status=%q error=%+v", updated.StatusMessage, updated.LastError)
			}
			if updated.Metadata["tos_appeal_url"] != "https://forms.gle/x" || updated.Metadata["tos_appeal_url_text"] != "Submit Appeal" {
				t.Fatal("TOS appeal metadata lost")
			}
			var wantRetry time.Time
			if !tc.disableCooling {
				wantRetry = updated.UpdatedAt.Add(30 * time.Minute)
			}
			if !updated.NextRetryAfter.Equal(wantRetry) || updated.Unavailable == tc.disableCooling {
				t.Fatalf("cooldown = %v (unavailable=%v), want %v", updated.NextRetryAfter, updated.Unavailable, wantRetry)
			}
			if tc.model != "" {
				state := updated.ModelStates[tc.model]
				if state == nil || state.Status != StatusError || !state.NextRetryAfter.Equal(wantRetry) {
					t.Fatalf("model should retain normal 403 cooling: %+v", state)
				}
			}
		})
	}
}

// TestMarkResult_NonTOS403_StillCools confirms the generic 403 path is
// unchanged: a plain PERMISSION_DENIED without the tos_violation code must
// continue to apply the 30-minute cooldown rather than deactivating.
func TestMarkResult_NonTOS403_StillCools(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	auth := &Auth{ID: "plain-403", Provider: "antigravity", Status: StatusActive}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "antigravity",
		Model:    "gemini-3-pro",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusForbidden, Message: "permission denied"},
	})

	updated, _ := manager.GetByID(auth.ID)
	if updated.Status == StatusDisabled {
		t.Fatal("plain 403 must not deactivate the credential")
	}
	if updated.NextRetryAfter.IsZero() {
		t.Fatal("plain 403 must keep the 30-minute cooldown")
	}
}

func TestMarkResult_TOSViolation_SuccessRecovers(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	auth := &Auth{ID: "tos-late", Provider: "antigravity", Status: StatusActive}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Model: "gemini-3-pro",
		Success: false, CredentialScope: true,
		Error: &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation"},
	})
	// A successful request can clear the error without an enable action.
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Model: "gemini-3-pro", Success: true,
	})
	updated, _ := manager.GetByID(auth.ID)
	if updated.Status != StatusActive || updated.Disabled || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("success did not recover credential: status=%q disabled=%v unavailable=%v next=%v", updated.Status, updated.Disabled, updated.Unavailable, updated.NextRetryAfter)
	}
}

// Late results must still honor an explicit operator disable.
func TestMarkResult_TOSViolation_PreservesManualDisable(t *testing.T) {
	for _, model := range []string{"gemini-3-pro", ""} {
		t.Run("model="+model, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			ctx := context.Background()
			auth := &Auth{
				ID: "manual-disabled", Provider: "antigravity", Status: StatusDisabled,
				Disabled: true, StatusMessage: "disabled by operator",
				Metadata: map[string]any{"disabled": true},
			}
			if _, err := manager.Register(ctx, auth); err != nil {
				t.Fatalf("register auth: %v", err)
			}
			for _, result := range []Result{
				{Error: &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation"}},
				{Error: &Error{HTTPStatus: http.StatusForbidden, Message: "plain 403"}},
				{Success: true},
			} {
				result.AuthID, result.Provider, result.Model = auth.ID, auth.Provider, model
				result.CredentialScope = true
				manager.MarkResult(ctx, result)
				updated, _ := manager.GetByID(auth.ID)
				if updated.Status != StatusDisabled || !updated.Disabled || updated.Metadata["disabled"] != true {
					t.Fatalf("result overwrote manual disable: status=%q disabled=%v", updated.Status, updated.Disabled)
				}
				if updated.StatusMessage != "disabled by operator" || !updated.NextRetryAfter.IsZero() {
					t.Fatalf("result changed manual disable details: status=%q next=%v", updated.StatusMessage, updated.NextRetryAfter)
				}
			}
		})
	}
}

// TestMarkResult_TOSViolation_NotDroppedByCredentialVersionBumps verifies that
// a TOS result dispatched before an OAuth token refresh is not discarded as
// stale when the refresh bumps CredentialVersion first. The suspension is
// service-scoped and applies to the account, not the token generation.
func TestMarkResult_TOSViolation_NotDroppedByCredentialVersionBumps(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	auth := &Auth{ID: "tos-stale", Provider: "antigravity", Status: StatusActive, CredentialVersion: 2}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	// A result recorded under credential version 1 (i.e. dispatched before a
	// refresh) still carries a valid terminal TOS signal.
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Success: false, CredentialScope: true,
		CredentialVersion: 1,
		Error:             &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation", Metadata: map[string]string{"tos_appeal_url": "https://forms.gle/x"}},
	})
	updated, _ := manager.GetByID(auth.ID)
	if updated.Status != StatusError || updated.Disabled || updated.StatusMessage != "tos_violation" || updated.NextRetryAfter.IsZero() {
		t.Fatalf("TOS result dropped as stale after version bump: status=%q disabled=%v", updated.Status, updated.Disabled)
	}
	if got := updated.Metadata["tos_appeal_url"]; got != "https://forms.gle/x" {
		t.Fatalf("appeal metadata lost on stale-guarded path: %v", got)
	}
}

// TestMarkResult_TOSViolation_RejectedAfterCredentialReplacement verifies that
// a TOS result from a request issued under a previous credential is dropped as
// stale after the credential is genuinely replaced (refresh token swapped),
// rather than penalizing the fresh credential.
func TestMarkResult_TOSViolation_RejectedAfterCredentialReplacement(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	auth := &Auth{
		ID: "tos-replaced", Provider: "antigravity", Status: StatusActive, CredentialVersion: 2,
		Metadata: map[string]any{"access_token": "new-access", "refresh_token": "new-refresh"},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	// Result dispatched under the OLD credential: old access token, old refresh
	// token, older credential version. Refresh token differs -> different account.
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Success: false, CredentialScope: true,
		AccessToken: "old-access", RefreshToken: "old-refresh", CredentialVersion: 1,
		Error: &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation"},
	})
	updated, _ := manager.GetByID(auth.ID)
	if updated.Status != StatusActive || updated.Disabled || updated.LastError != nil || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("stale TOS result penalized fresh credential: status=%q disabled=%v", updated.Status, updated.Disabled)
	}
}

// TestMarkResult_TOSViolation_SameAccountRefreshApplies verifies that a TOS
// result issued before a same-account OAuth refresh (access token rotated,
// refresh token retained) is NOT dropped as stale: the refresh token proves it
// is the same account, so the error and cooldown are applied.
func TestMarkResult_TOSViolation_SameAccountRefreshApplies(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	// Current credential after a same-account refresh: new access token, SAME
	// refresh token, bumped credential version.
	auth := &Auth{
		ID: "tos-same-acct", Provider: "antigravity", Status: StatusActive, CredentialVersion: 2,
		Metadata: map[string]any{"access_token": "new-access", "refresh_token": "same-refresh"},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	// Old request: old access token (no longer matches), same refresh token.
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Success: false, CredentialScope: true,
		AccessToken: "old-access", RefreshToken: "same-refresh", CredentialVersion: 1,
		Error: &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation"},
	})
	updated, _ := manager.GetByID(auth.ID)
	if updated.Status != StatusError || updated.Disabled || updated.StatusMessage != "tos_violation" || updated.NextRetryAfter.IsZero() {
		t.Fatalf("same-account TOS result dropped after refresh: status=%q disabled=%v", updated.Status, updated.Disabled)
	}
}
