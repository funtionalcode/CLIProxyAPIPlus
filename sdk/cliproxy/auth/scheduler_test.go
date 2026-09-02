package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type schedulerTestExecutor struct {
	provider string
}

func (e schedulerTestExecutor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return "test"
}

func (schedulerTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (schedulerTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (schedulerTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (schedulerTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (schedulerTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

type fakePluginScheduler struct {
	resp     pluginapi.SchedulerPickResponse
	handled  bool
	err      error
	calls    int
	requests []pluginapi.SchedulerPickRequest
	pick     func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error)
}

func (s *fakePluginScheduler) PickAuth(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	s.calls++
	s.requests = append(s.requests, req)
	if s.pick != nil {
		return s.pick(ctx, req)
	}
	return s.resp, s.handled, s.err
}

type inactivePluginScheduler struct {
	fakePluginScheduler
}

type authKindHomeDispatcher struct {
	auths  []Auth
	counts []int
}

func (d *authKindHomeDispatcher) HeartbeatOK() bool {
	return true
}

func (d *authKindHomeDispatcher) RPopAuth(_ context.Context, _ string, _ string, _ http.Header, count int) ([]byte, error) {
	d.counts = append(d.counts, count)
	if count < 1 || count > len(d.auths) {
		return nil, home.ErrAuthNotFound
	}
	return json.Marshal(homeAuthDispatchResponse{Auth: d.auths[count-1]})
}

func (*authKindHomeDispatcher) AbortAmbiguousDispatch() {}

func (s *inactivePluginScheduler) HasScheduler() bool {
	return false
}

type trackingSelector struct {
	calls      int
	lastAuthID []string
}

func (s *trackingSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	s.calls++
	s.lastAuthID = s.lastAuthID[:0]
	for _, auth := range auths {
		s.lastAuthID = append(s.lastAuthID, auth.ID)
	}
	if len(auths) == 0 {
		return nil, nil
	}
	return auths[len(auths)-1], nil
}

func newSchedulerForTest(selector Selector, auths ...*Auth) *authScheduler {
	scheduler := newAuthScheduler(selector)
	scheduler.rebuild(auths)
	return scheduler
}

func registerSchedulerModels(t *testing.T, provider string, model string, authIDs ...string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	for _, authID := range authIDs {
		reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		for _, authID := range authIDs {
			reg.UnregisterClient(authID)
		}
	})
}

func TestSchedulerPick_RoundRobinHighestPriority(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "low", Provider: "gemini", Attributes: map[string]string{"priority": "0"}},
		&Auth{ID: "high-b", Provider: "gemini", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "high-a", Provider: "gemini", Attributes: map[string]string{"priority": "10"}},
	)

	want := []string{"high-a", "high-b", "high-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_RoundRobinWeightedByAuthWeight(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{Weighted: true},
		&Auth{ID: "a", Provider: "gemini", Attributes: map[string]string{"weight": "2"}},
		&Auth{ID: "b", Provider: "gemini", Attributes: map[string]string{"weight": "1"}},
	)

	want := []string{"a", "b", "a", "a", "b", "a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_RoundRobinWeightedSmoothlyInterleavesAuths(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{Weighted: true},
		&Auth{ID: "a", Provider: "gemini", Attributes: map[string]string{"weight": "11"}},
		&Auth{ID: "b", Provider: "gemini", Attributes: map[string]string{"weight": "7"}},
		&Auth{ID: "c", Provider: "gemini", Attributes: map[string]string{"weight": "10"}},
		&Auth{ID: "d", Provider: "gemini", Attributes: map[string]string{"weight": "2"}},
		&Auth{ID: "e", Provider: "gemini", Attributes: map[string]string{"weight": "1"}},
	)

	seen := make(map[string]bool)
	for index := 0; index < 5; index++ {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		seen[got.ID] = true
	}
	if len(seen) < 3 {
		t.Fatalf("first 5 weighted picks touched %d auths, want at least 3; seen=%v", len(seen), seen)
	}
}

func TestSchedulerPick_RoundRobinWeightedIgnoresPriority(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{Weighted: true},
		&Auth{ID: "high", Provider: "gemini", Attributes: map[string]string{"priority": "10", "weight": "1"}},
		&Auth{ID: "low", Provider: "gemini", Attributes: map[string]string{"priority": "0", "weight": "2"}},
	)

	want := []string{"low", "high", "low", "low", "high", "low"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_FillFirstSticksToFirstReady(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&FillFirstSelector{},
		&Auth{ID: "b", Provider: "gemini"},
		&Auth{ID: "a", Provider: "gemini"},
		&Auth{ID: "c", Provider: "gemini"},
	)

	for index := 0; index < 3; index++ {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != "a" {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, "a")
		}
	}
}

func TestSchedulerPick_PromotesExpiredCooldownBeforePick(t *testing.T) {
	t.Parallel()

	model := "gemini-2.5-pro"
	registerSchedulerModels(t, "gemini", model, "cooldown-expired")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{
			ID:       "cooldown-expired",
			Provider: "gemini",
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusError,
					Unavailable:    true,
					NextRetryAfter: time.Now().Add(-1 * time.Second),
				},
			},
		},
	)

	got, errPick := scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickSingle() auth = nil")
	}
	if got.ID != "cooldown-expired" {
		t.Fatalf("pickSingle() auth.ID = %q, want %q", got.ID, "cooldown-expired")
	}
}

func TestSchedulerPick_CodexWebsocketPrefersWebsocketEnabledSubset(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-http", Provider: "codex"},
		&Auth{ID: "codex-ws-a", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
		&Auth{ID: "codex-ws-b", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	want := []string{"codex-ws-a", "codex-ws-b", "codex-ws-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_XAIWebsocketPrefersWebsocketEnabledSubset(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "xai-http", Provider: "xai"},
		&Auth{ID: "xai-ws-a", Provider: "xai", Attributes: map[string]string{"websockets": "true"}},
		&Auth{ID: "xai-ws-b", Provider: "xai", Attributes: map[string]string{"websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	want := []string{"xai-ws-a", "xai-ws-b", "xai-ws-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(ctx, "xai", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_CodexWebsocketPrefersWebsocketEnabledAcrossPriorities(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-http", Provider: "codex", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "codex-ws-a", Provider: "codex", Attributes: map[string]string{"priority": "0", "websockets": "true"}},
		&Auth{ID: "codex-ws-b", Provider: "codex", Attributes: map[string]string{"priority": "0", "websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	want := []string{"codex-ws-a", "codex-ws-b", "codex-ws-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_MixedProvidersUsesWeightedProviderRotationOverReadyCandidates(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "gemini-a", Provider: "gemini"},
		&Auth{ID: "gemini-b", Provider: "gemini"},
		&Auth{ID: "claude-a", Provider: "claude"},
	)

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestSchedulerPick_MixedProvidersWeightedRoundRobin(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&WeightedRoundRobinSelector{},
		&Auth{ID: "gemini-a", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "5"}},
		&Auth{ID: "claude-b", Provider: "claude", Attributes: map[string]string{AttributeWeight: "3"}},
		&Auth{ID: "claude-c", Provider: "claude", Attributes: map[string]string{AttributeWeight: "2"}},
	)

	counts := make(map[string]int)
	for index := 0; index < 100; index++ {
		got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil || provider == "" {
			t.Fatalf("pickMixed() #%d returned auth=%v provider=%q", index, got, provider)
		}
		counts[got.ID]++
	}
	want := map[string]int{"gemini-a": 50, "claude-b": 30, "claude-c": 20}
	for authID, wantCount := range want {
		if counts[authID] != wantCount {
			t.Fatalf("auth %q picks = %d, want %d", authID, counts[authID], wantCount)
		}
	}
}

func TestSchedulerPick_MixedProvidersResetsCreditsWhenWeightsChange(t *testing.T) {
	t.Parallel()

	authA := &Auth{ID: "gemini-a", Provider: "gemini", Attributes: map[string]string{AttributeWeight: "1000000"}}
	authB := &Auth{ID: "claude-b", Provider: "claude", Attributes: map[string]string{AttributeWeight: "1"}}
	scheduler := newSchedulerForTest(&WeightedRoundRobinSelector{}, authA, authB)
	providers := []string{"gemini", "claude"}
	for index := 0; index < 1000; index++ {
		if _, _, errPick := scheduler.pickMixed(context.Background(), providers, "", cliproxyexecutor.Options{}, nil); errPick != nil {
			t.Fatalf("warmup pickMixed() #%d error = %v", index, errPick)
		}
	}

	authA.Attributes[AttributeWeight] = "1"
	scheduler.upsertAuth(authA)
	counts := make(map[string]int)
	for index := 0; index < 20; index++ {
		got, _, errPick := scheduler.pickMixed(context.Background(), providers, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() after weight change #%d error = %v", index, errPick)
		}
		counts[got.ID]++
	}
	if counts[authA.ID] != 10 || counts[authB.ID] != 10 {
		t.Fatalf("mixed picks after weight change = %#v, want 10 each", counts)
	}
}

func TestSchedulerPickMixed_RetryTriedFilterPreservesSmoothWeightedDistribution(t *testing.T) {
	t.Parallel()

	authA := &Auth{ID: "auth-a", Provider: "provider-a"}
	authB := &Auth{ID: "auth-b", Provider: "provider-b"}
	authC := &Auth{ID: "auth-c", Provider: "provider-c"}
	authD := &Auth{ID: "auth-d", Provider: "provider-d"}
	auths := []*Auth{authA, authB, authC, authD}

	scheduler := newSchedulerForTest(&WeightedRoundRobinSelector{}, auths...)
	providers := []string{"provider-a", "provider-b", "provider-c", "provider-d"}

	// Simulate retries where auth-a failed and is in the tried filter:
	// Verify that retry picks rotate smoothly across auth-b, auth-c, auth-d without alphabetical bias towards auth-b.
	retryCounts := make(map[string]int)
	tried := map[string]struct{}{"auth-a": {}}
	for index := 0; index < 30; index++ {
		picked, _, errPick := scheduler.pickMixed(context.Background(), providers, "", cliproxyexecutor.Options{}, tried)
		if errPick != nil {
			t.Fatalf("pickMixed(tried) error = %v", errPick)
		}
		retryCounts[picked.ID]++
	}

	for _, authID := range []string{"auth-b", "auth-c", "auth-d"} {
		if retryCounts[authID] != 10 {
			t.Fatalf("auth %q retry picks = %d, want 10 (even distribution without alphabetical bias, counts=%#v)", authID, retryCounts[authID], retryCounts)
		}
	}
}

func TestReadyViewRoundRobinPreservesSuccessorAcrossRebuild(t *testing.T) {
	t.Parallel()

	entry := func(id string) *scheduledAuth {
		return &scheduledAuth{auth: &Auth{ID: id}}
	}

	t.Run("a cooling resumes at b", func(t *testing.T) {
		original := readyView{
			flat: []*scheduledAuth{entry("A"), entry("B"), entry("C")},
		}
		if got := original.pickRoundRobin(nil); got == nil || got.auth.ID != "A" {
			t.Fatalf("first pick = %v, want A", got)
		}

		state := snapshotReadyViewCursors(original)
		rebuilt := readyView{
			flat: []*scheduledAuth{entry("B"), entry("C")},
		}
		restoreReadyViewCursors(&rebuilt, state)

		got := rebuilt.pickRoundRobin(nil)
		if got == nil || got.auth.ID != "B" {
			t.Fatalf("pick after A cooldown = %v, want B", got)
		}
	})

	t.Run("b cooling resumes at c", func(t *testing.T) {
		original := readyView{
			flat: []*scheduledAuth{entry("A"), entry("B"), entry("C")},
		}
		// Pick A, then B
		if got := original.pickRoundRobin(nil); got == nil || got.auth.ID != "A" {
			t.Fatalf("first pick = %v, want A", got)
		}
		if got := original.pickRoundRobin(nil); got == nil || got.auth.ID != "B" {
			t.Fatalf("second pick = %v, want B", got)
		}

		state := snapshotReadyViewCursors(original)
		rebuilt := readyView{
			flat: []*scheduledAuth{entry("A"), entry("C")},
		}
		restoreReadyViewCursors(&rebuilt, state)

		got := rebuilt.pickRoundRobin(nil)
		if got == nil || got.auth.ID != "C" {
			t.Fatalf("pick after B cooldown = %v, want C", got)
		}
	})

	t.Run("c cooling wraps to a", func(t *testing.T) {
		original := readyView{
			flat: []*scheduledAuth{entry("A"), entry("B"), entry("C")},
		}
		// Pick A, B, C
		for _, want := range []string{"A", "B", "C"} {
			if got := original.pickRoundRobin(nil); got == nil || got.auth.ID != want {
				t.Fatalf("pick = %v, want %s", got, want)
			}
		}

		state := snapshotReadyViewCursors(original)
		rebuilt := readyView{
			flat: []*scheduledAuth{entry("A"), entry("B")},
		}
		restoreReadyViewCursors(&rebuilt, state)

		got := rebuilt.pickRoundRobin(nil)
		if got == nil || got.auth.ID != "A" {
			t.Fatalf("pick after C cooldown = %v, want A", got)
		}
	})

	t.Run("recovery preserves successor", func(t *testing.T) {
		original := readyView{
			flat: []*scheduledAuth{entry("B"), entry("C")},
		}
		if got := original.pickRoundRobin(nil); got == nil || got.auth.ID != "B" {
			t.Fatalf("first pick = %v, want B", got)
		}

		state := snapshotReadyViewCursors(original)
		// A recovered and is prepended back
		rebuilt := readyView{
			flat: []*scheduledAuth{entry("A"), entry("B"), entry("C")},
		}
		restoreReadyViewCursors(&rebuilt, state)

		got := rebuilt.pickRoundRobin(nil)
		if got == nil || got.auth.ID != "C" {
			t.Fatalf("pick after A recovery = %v, want C", got)
		}
	})

	t.Run("retry exclusion resumes without rebuild", func(t *testing.T) {
		view := readyView{
			flat: []*scheduledAuth{entry("A"), entry("B"), entry("C")},
		}
		if got := view.pickRoundRobin(nil); got == nil || got.auth.ID != "A" {
			t.Fatalf("first pick = %v, want A", got)
		}
		got := view.pickRoundRobin(func(candidate *scheduledAuth) bool {
			return candidate.auth.ID != "B"
		})
		if got == nil || got.auth.ID != "C" {
			t.Fatalf("pick after excluding B = %v, want C", got)
		}
	})

	t.Run("multiple cooldown skips to first surviving successor", func(t *testing.T) {
		original := readyView{
			flat: []*scheduledAuth{entry("A"), entry("B"), entry("C"), entry("D")},
		}
		if got := original.pickRoundRobin(nil); got == nil || got.auth.ID != "A" {
			t.Fatalf("first pick = %v, want A", got)
		}

		state := snapshotReadyViewCursors(original)
		rebuilt := readyView{
			flat: []*scheduledAuth{entry("C"), entry("D")},
		}
		restoreReadyViewCursors(&rebuilt, state)

		got := rebuilt.pickRoundRobin(nil)
		if got == nil || got.auth.ID != "C" {
			t.Fatalf("pick after A and B cooldown = %v, want C", got)
		}
	})
}

func TestScheduledSuccessorIndex_WrapsAndSkipsFilteredCandidates(t *testing.T) {
	t.Parallel()

	entries := []*scheduledAuth{
		{auth: &Auth{ID: "aaa"}},
		{auth: &Auth{ID: "ccc"}},
		{auth: &Auth{ID: "eee"}},
	}
	tests := []struct {
		name   string
		lastID string
		want   int
	}{
		{name: "no previous pick starts at head", lastID: "", want: 0},
		{name: "resumes after previous pick", lastID: "aaa", want: 1},
		{name: "resumes after filtered-out pick", lastID: "bbb", want: 1},
		{name: "wraps at the end of the ring", lastID: "eee", want: 0},
		{name: "wraps for removed trailing pick", lastID: "zzz", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scheduledSuccessorIndex(entries, tt.lastID); got != tt.want {
				t.Fatalf("scheduledSuccessorIndex(%q) = %d, want %d", tt.lastID, got, tt.want)
			}
		})
	}
	if got := scheduledSuccessorIndex(nil, "aaa"); got != 0 {
		t.Fatalf("scheduledSuccessorIndex(nil, aaa) = %d, want 0", got)
	}
}

func TestManagerRoundRobinPreservesSuccessorAcrossCooldown(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	model := "test-successor-model"
	authIDs := []string{"successor-auth-a", "successor-auth-b", "successor-auth-c"}
	registerSchedulerModels(t, "gemini", model, authIDs...)

	for _, id := range authIDs {
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
	}

	got, errPick := manager.scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle #1 error = %v", errPick)
	}
	if got == nil || got.ID != "successor-auth-a" {
		t.Fatalf("pickSingle #1 = %v, want successor-auth-a", got)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "successor-auth-a",
		Provider: "gemini",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: 429, Message: "rate limit"},
	})

	got, errPick = manager.scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle #2 after successor-auth-a cooldown error = %v", errPick)
	}
	if got == nil || got.ID != "successor-auth-b" {
		t.Fatalf("pickSingle #2 after successor-auth-a cooldown = %v, want successor-auth-b", got)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "successor-auth-b",
		Provider: "gemini",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: 429, Message: "rate limit"},
	})

	got, errPick = manager.scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle #3 after successor-auth-b cooldown error = %v", errPick)
	}
	if got == nil || got.ID != "successor-auth-c" {
		t.Fatalf("pickSingle #3 after successor-auth-b cooldown = %v, want successor-auth-c", got)
	}
}

func TestSchedulerPick_RoundRobinPreservesWebsocketSuccessorAcrossCooldown(t *testing.T) {
	t.Parallel()

	wsA := &Auth{ID: "codex-ws-a", Provider: "codex", Attributes: map[string]string{"websockets": "true"}}
	wsB := &Auth{ID: "codex-ws-b", Provider: "codex", Attributes: map[string]string{"websockets": "true"}}
	wsC := &Auth{ID: "codex-ws-c", Provider: "codex", Attributes: map[string]string{"websockets": "true"}}
	httpOnly := &Auth{ID: "codex-http", Provider: "codex"}
	scheduler := newSchedulerForTest(&RoundRobinSelector{}, httpOnly, wsA, wsB, wsC)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	got, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() first error = %v", errPick)
	}
	if got == nil || got.ID != "codex-ws-a" {
		t.Fatalf("pickSingle() first = %v, want codex-ws-a", got)
	}

	wsA.Unavailable = true
	wsA.NextRetryAfter = time.Now().Add(time.Hour)
	scheduler.upsertAuth(wsA)

	got, errPick = scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() after ws-a cooldown error = %v", errPick)
	}
	if got == nil || got.ID != "codex-ws-b" {
		t.Fatalf("pickSingle() after ws-a cooldown = %v, want codex-ws-b", got)
	}
}

func TestSchedulerPick_MixedProvidersPrefersHighestPriorityTier(t *testing.T) {
	t.Parallel()

	model := "gpt-default"
	registerSchedulerModels(t, "provider-low", model, "low")
	registerSchedulerModels(t, "provider-high-a", model, "high-a")
	registerSchedulerModels(t, "provider-high-b", model, "high-b")

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "low", Provider: "provider-low", Attributes: map[string]string{"priority": "4"}},
		&Auth{ID: "high-a", Provider: "provider-high-a", Attributes: map[string]string{"priority": "7"}},
		&Auth{ID: "high-b", Provider: "provider-high-b", Attributes: map[string]string{"priority": "7"}},
	)

	providers := []string{"provider-low", "provider-high-a", "provider-high-b"}
	wantProviders := []string{"provider-high-a", "provider-high-b", "provider-high-a", "provider-high-b"}
	wantIDs := []string{"high-a", "high-b", "high-a", "high-b"}
	for index := range wantProviders {
		got, provider, errPick := scheduler.pickMixed(context.Background(), providers, model, cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestSchedulerPick_MixedProvidersSessionAffinitySticksAcrossProviders(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	scheduler := newSchedulerForTest(
		selector,
		&Auth{ID: "compat-a", Provider: "compat-a"},
		&Auth{ID: "compat-b", Provider: "compat-a"},
		&Auth{ID: "compat-c", Provider: "compat-b"},
	)

	headers := http.Header{}
	headers.Set("X-Session-ID", "mixed-sticky-session")
	opts := cliproxyexecutor.Options{Headers: headers}
	first, provider, errPick := scheduler.pickMixed(context.Background(), []string{"compat-a", "compat-b"}, "", opts, nil)
	if errPick != nil {
		t.Fatalf("pickMixed() first error = %v", errPick)
	}
	if first == nil {
		t.Fatalf("pickMixed() first auth = nil")
	}

	for index := 0; index < 3; index++ {
		got, gotProvider, errAgain := scheduler.pickMixed(context.Background(), []string{"compat-a", "compat-b"}, "", opts, nil)
		if errAgain != nil {
			t.Fatalf("pickMixed() repeat #%d error = %v", index, errAgain)
		}
		if got == nil {
			t.Fatalf("pickMixed() repeat #%d auth = nil", index)
		}
		if got.ID != first.ID || gotProvider != provider {
			t.Fatalf("pickMixed() repeat #%d = (%q, %q), want (%q, %q)", index, gotProvider, got.ID, provider, first.ID)
		}
	}
}

func TestSchedulerPick_MixedProvidersSessionAffinityNewBindingsUseWeight(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{Weighted: true},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	scheduler := newSchedulerForTest(
		selector,
		&Auth{ID: "provider-a", Provider: "provider-a", Attributes: map[string]string{"weight": "2"}},
		&Auth{ID: "provider-b", Provider: "provider-b", Attributes: map[string]string{"weight": "1"}},
	)

	want := []struct {
		provider string
		authID   string
	}{
		{provider: "provider-a", authID: "provider-a"},
		{provider: "provider-b", authID: "provider-b"},
		{provider: "provider-a", authID: "provider-a"},
	}
	for index, tc := range want {
		headers := http.Header{}
		headers.Set("X-Session-ID", "mixed-weighted-session-"+string(rune('a'+index)))
		opts := cliproxyexecutor.Options{Headers: headers}
		got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"provider-a", "provider-b"}, "", opts, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickMixed() #%d auth = nil", index)
		}
		if provider != tc.provider || got.ID != tc.authID {
			t.Fatalf("pickMixed() #%d = (%q, %q), want (%q, %q)", index, provider, got.ID, tc.provider, tc.authID)
		}

		again, againProvider, errAgain := scheduler.pickMixed(context.Background(), []string{"provider-a", "provider-b"}, "", opts, nil)
		if errAgain != nil {
			t.Fatalf("pickMixed() #%d repeat error = %v", index, errAgain)
		}
		if again == nil || againProvider != tc.provider || again.ID != tc.authID {
			t.Fatalf("pickMixed() #%d repeat = (%q, %v), want (%q, %q)", index, againProvider, again, tc.provider, tc.authID)
		}
	}
}

func TestManager_PickNextMixed_UsesWeightedProviderRotationBeforeCredentialRotation(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, map[string]struct{}{})
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickNextMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickNextMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickNextMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManager_PickNextMixed_DisallowFreeAuthSkipsCodexFreePlan(t *testing.T) {
	t.Parallel()

	model := "gpt-5.4-mini"
	registerSchedulerModels(t, "codex", model, "codex-a-free", "codex-b-plus")

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-a-free", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}}); errRegister != nil {
		t.Fatalf("Register(codex-a-free) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-b-plus", Provider: "codex", Attributes: map[string]string{"plan_type": "plus"}}); errRegister != nil {
		t.Fatalf("Register(codex-b-plus) error = %v", errRegister)
	}

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.DisallowFreeAuthMetadataKey: true},
	}
	got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"codex"}, model, opts, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNextMixed() auth = nil")
	}
	if provider != "codex" {
		t.Fatalf("pickNextMixed() provider = %q, want %q", provider, "codex")
	}
	if got.ID != "codex-b-plus" {
		t.Fatalf("pickNextMixed() auth.ID = %q, want %q", got.ID, "codex-b-plus")
	}
}

func TestManagerPluginSchedulerSelectsAuthID(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}

	scheduler := &fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-b"},
		handled: true,
	}
	manager.SetPluginScheduler(scheduler)

	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{Stream: true}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNext() auth = nil")
	}
	if got.ID != "auth-b" {
		t.Fatalf("pickNext() auth.ID = %q, want %q", got.ID, "auth-b")
	}
	if scheduler.calls != 1 {
		t.Fatalf("scheduler.calls = %d, want %d", scheduler.calls, 1)
	}
	if len(scheduler.requests) != 1 {
		t.Fatalf("len(scheduler.requests) = %d, want %d", len(scheduler.requests), 1)
	}
	if !scheduler.requests[0].Stream {
		t.Fatalf("scheduler request Stream = false, want true")
	}
}

func TestManagerSelectAuthByKindSkipsAPIKey(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	for _, candidate := range []*Auth{
		{ID: "codex-api-key", Provider: "codex", Attributes: map[string]string{AttributeAPIKey: "test-key"}},
		{ID: "codex-oauth", Provider: "codex", Metadata: map[string]any{"access_token": "test-token"}},
	} {
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", candidate.ID, errRegister)
		}
	}

	scheduler := &fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, AuthID: "codex-api-key"},
		handled: true,
	}
	manager.SetPluginScheduler(scheduler)

	selected, errSelect := manager.SelectAuthByKind(context.Background(), "codex", "", AuthKindOAuth, cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("SelectAuthByKind() error = %v", errSelect)
	}
	if selected == nil || selected.ID != "codex-oauth" {
		t.Fatalf("SelectAuthByKind() auth = %#v, want codex-oauth", selected)
	}
	if scheduler.calls != 1 {
		t.Fatalf("scheduler.calls = %d, want 1", scheduler.calls)
	}
	if len(scheduler.requests) != 1 || len(scheduler.requests[0].Candidates) != 1 || scheduler.requests[0].Candidates[0].ID != "codex-oauth" {
		t.Fatalf("scheduler candidates = %#v, want only codex-oauth", scheduler.requests)
	}
}

func TestManagerSelectAuthByKindReturnsErrorWhenUnavailable(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:         "codex-api-key",
		Provider:   "codex",
		Attributes: map[string]string{AttributeAPIKey: "test-key"},
	}); errRegister != nil {
		t.Fatalf("Register(codex-api-key) error = %v", errRegister)
	}

	selected, errSelect := manager.SelectAuthByKind(context.Background(), "codex", "", AuthKindOAuth, cliproxyexecutor.Options{})
	if selected != nil {
		t.Fatalf("SelectAuthByKind() auth = %#v, want nil", selected)
	}
	var authErr *Error
	if !errors.As(errSelect, &authErr) || authErr.Code != "auth_not_found" {
		t.Fatalf("SelectAuthByKind() error = %#v, want auth_not_found", errSelect)
	}
}

func TestManagerSelectAuthByKindRejectsInvalidKind(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	selected, errSelect := manager.SelectAuthByKind(context.Background(), "codex", "", "certificate", cliproxyexecutor.Options{})
	if selected != nil {
		t.Fatalf("SelectAuthByKind() auth = %#v, want nil", selected)
	}
	var authErr *Error
	if !errors.As(errSelect, &authErr) || authErr.Code != "invalid_auth_kind" || authErr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("SelectAuthByKind() error = %#v, want invalid_auth_kind", errSelect)
	}
}

func TestManagerLegacySelectAuthFailsClosedWhenHomeEnabled(t *testing.T) {
	dispatcher := &authKindHomeDispatcher{auths: []Auth{{
		ID:       "home-oauth",
		Provider: "test",
		Metadata: map[string]any{"access_token": "test-token"},
	}}}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetHomeExecutionRegistry(executionregistry.New())
	manager.RegisterExecutor(schedulerTestExecutor{})

	for name, selectAuth := range map[string]func() (*Auth, error){
		"SelectAuth": func() (*Auth, error) {
			return manager.SelectAuth(context.Background(), "test", "model", cliproxyexecutor.Options{})
		},
		"SelectAuthByKind": func() (*Auth, error) {
			return manager.SelectAuthByKind(context.Background(), "test", "model", AuthKindOAuth, cliproxyexecutor.Options{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			selected, errSelect := selectAuth()
			if selected != nil {
				t.Fatalf("%s() auth = %#v, want nil", name, selected)
			}
			var authErr *Error
			if !errors.As(errSelect, &authErr) || authErr.Code != "home_unavailable" || authErr.HTTPStatus != http.StatusServiceUnavailable {
				t.Fatalf("%s() error = %#v, want home_unavailable", name, errSelect)
			}
		})
	}
	if len(dispatcher.counts) != 0 {
		t.Fatalf("legacy selection issued Home RPOP calls: %v", dispatcher.counts)
	}
}

func TestSelectHomeAuthByKindReturnsHomeSelection(t *testing.T) {
	dispatcher := &authKindHomeDispatcher{auths: []Auth{{
		ID:       "home-oauth",
		Provider: "test",
		Metadata: map[string]any{"access_token": "test-token"},
	}}}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher {
		return dispatcher
	}
	t.Cleanup(func() {
		currentHomeDispatcher = oldCurrentHomeDispatcher
	})

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetHomeExecutionRegistry(executionregistry.New())
	manager.RegisterExecutor(schedulerTestExecutor{})

	selection, errSelect := manager.SelectHomeAuthByKind(context.Background(), "test", "gpt-5.4", AuthKindOAuth, cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("SelectHomeAuthByKind() error = %v", errSelect)
	}
	if selection == nil || selection.Auth == nil || selection.Auth.ID != "home-oauth" {
		t.Fatalf("SelectHomeAuthByKind() = %#v, want home-oauth", selection)
	}
	if selection.Executor == nil || selection.Provider != "test" {
		t.Fatalf("selection executor/provider = %#v/%q, want test", selection.Executor, selection.Provider)
	}
	selection.End("test_complete")
}

func TestSelectHomeAuthByKindSkipsProviderMismatch(t *testing.T) {
	dispatcher := &authKindHomeDispatcher{auths: []Auth{
		{ID: "wrong-provider", Provider: "other", Metadata: map[string]any{"access_token": "test-token"}},
		{ID: "matching-provider", Provider: "test", Metadata: map[string]any{"access_token": "test-token"}},
	}}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher {
		return dispatcher
	}
	t.Cleanup(func() {
		currentHomeDispatcher = oldCurrentHomeDispatcher
	})

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetHomeExecutionRegistry(executionregistry.New())
	manager.RegisterExecutor(schedulerTestExecutor{})
	manager.RegisterExecutor(schedulerTestExecutor{provider: "other"})

	selection, errSelect := manager.SelectHomeAuthByKind(context.Background(), "test", "gpt-5.4", AuthKindOAuth, cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("SelectHomeAuthByKind() error = %v", errSelect)
	}
	if selection == nil || selection.Auth == nil || selection.Auth.ID != "matching-provider" {
		t.Fatalf("SelectHomeAuthByKind() = %#v, want matching provider auth", selection)
	}
	if got := dispatcher.counts; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("home auth counts = %v, want [1 2]", got)
	}
	selection.End("test_complete")
}

func TestSelectHomeAuthByKindKeepsLogicalProviderWhenUsingCompatibilityExecutor(t *testing.T) {
	dispatcher := &authKindHomeDispatcher{auths: []Auth{{
		ID:       "compat-auth",
		Provider: "base-url-provider",
		Attributes: map[string]string{
			"base_url":      "https://compat.example.com",
			AttributeAPIKey: "test-key",
		},
	}}}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher {
		return dispatcher
	}
	t.Cleanup(func() {
		currentHomeDispatcher = oldCurrentHomeDispatcher
	})

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetHomeExecutionRegistry(executionregistry.New())
	manager.RegisterExecutor(schedulerTestExecutor{provider: "openai-compatibility"})

	selection, errSelect := manager.SelectHomeAuthByKind(context.Background(), "base-url-provider", "gpt-5.4", AuthKindAPIKey, cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("SelectHomeAuthByKind() error = %v", errSelect)
	}
	if selection == nil || selection.Auth == nil || selection.Auth.ID != "compat-auth" {
		t.Fatalf("SelectHomeAuthByKind() = %#v, want compat-auth", selection)
	}
	if selection.Provider != "base-url-provider" {
		t.Fatalf("selection.Provider = %q, want logical provider base-url-provider", selection.Provider)
	}
	if selection.Executor == nil || selection.Executor.Identifier() != "openai-compatibility" {
		t.Fatalf("selection.Executor = %#v, want openai-compatibility", selection.Executor)
	}
	selection.End("test_complete")
}

func TestPickNextViaHomeEndsPendingOnInvalidAuth(t *testing.T) {
	dispatcher := &authKindHomeDispatcher{auths: []Auth{{Provider: "test"}}}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher {
		return dispatcher
	}
	t.Cleanup(func() {
		currentHomeDispatcher = oldCurrentHomeDispatcher
	})

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	manager.SetHomeExecutionRegistry(registry)
	manager.RegisterExecutor(schedulerTestExecutor{})

	_, _, _, errPick := manager.pickNextViaHome(context.Background(), "gpt-5.4", cliproxyexecutor.Options{}, nil)
	var authErr *Error
	if !errors.As(errPick, &authErr) || authErr.Code != "invalid_auth" {
		t.Fatalf("pickNextViaHome() error = %v, want invalid_auth", errPick)
	}

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	if errDrain := registry.Drain(drainCtx); errDrain != nil {
		t.Fatalf("Drain() error = %v, pending dispatch was not ended", errDrain)
	}
}

func TestManagerPluginSchedulerSkippedWhenHomeEnabled(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	scheduler := &fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-a"},
		handled: true,
	}
	manager.SetPluginScheduler(scheduler)

	_, _, _ = manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)

	if scheduler.calls != 0 {
		t.Fatalf("scheduler.calls = %d, want %d", scheduler.calls, 0)
	}
}

func TestManagerInactivePluginSchedulerKeepsFastPath(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}

	scheduler := &inactivePluginScheduler{}
	manager.SetPluginScheduler(scheduler)

	gotA, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() first error = %v", errPick)
	}
	gotB, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() second error = %v", errPick)
	}
	if gotA == nil || gotB == nil {
		t.Fatalf("pickNext() auths = %v, %v; want non-nil", gotA, gotB)
	}
	if gotA.ID != "auth-a" || gotB.ID != "auth-b" {
		t.Fatalf("fast path picks = %q, %q; want auth-a, auth-b", gotA.ID, gotB.ID)
	}
	if scheduler.calls != 0 {
		t.Fatalf("scheduler.calls = %d, want %d", scheduler.calls, 0)
	}
}

func TestManagerPluginSchedulerCalledOutsideManagerLock(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}

	scheduler := &fakePluginScheduler{
		handled: true,
		pick: func(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
			if !manager.mu.TryLock() {
				t.Fatalf("plugin scheduler called while manager lock is held")
			}
			manager.mu.Unlock()
			return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-a"}, true, nil
		},
	}
	manager.SetPluginScheduler(scheduler)

	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNext() auth = nil")
	}
	if got.ID != "auth-a" {
		t.Fatalf("pickNext() auth.ID = %q, want auth-a", got.ID)
	}
	if scheduler.calls != 1 {
		t.Fatalf("scheduler.calls = %d, want %d", scheduler.calls, 1)
	}
}

func TestManagerPluginSchedulerErrorStopsPick(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}

	scheduler := &fakePluginScheduler{
		handled: true,
		err:     errors.New("tenant denied"),
	}
	manager.SetPluginScheduler(scheduler)

	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick == nil {
		t.Fatalf("pickNext() error = nil, want tenant denied")
	}
	if errPick.Error() != "tenant denied" {
		t.Fatalf("pickNext() error = %v, want tenant denied", errPick)
	}
	if got != nil {
		t.Fatalf("pickNext() auth = %v, want nil", got)
	}
}

func TestManagerPluginSchedulerFallsBackWhenUnhandledOrUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resp    pluginapi.SchedulerPickResponse
		handled bool
	}{
		{
			name:    "unhandled",
			resp:    pluginapi.SchedulerPickResponse{Handled: false},
			handled: false,
		},
		{
			name:    "unknown auth id",
			resp:    pluginapi.SchedulerPickResponse{Handled: true, AuthID: "missing"},
			handled: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, &FillFirstSelector{}, nil)
			manager.executors["gemini"] = schedulerTestExecutor{}
			if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
				t.Fatalf("Register(auth-b) error = %v", errRegister)
			}
			if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
				t.Fatalf("Register(auth-a) error = %v", errRegister)
			}

			scheduler := &fakePluginScheduler{resp: tc.resp, handled: tc.handled}
			manager.SetPluginScheduler(scheduler)

			got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickNext() error = %v", errPick)
			}
			if got == nil {
				t.Fatalf("pickNext() auth = nil")
			}
			if got.ID != "auth-a" {
				t.Fatalf("pickNext() auth.ID = %q, want %q", got.ID, "auth-a")
			}
		})
	}
}

func TestManagerPluginSchedulerDelegatesBuiltin(t *testing.T) {
	t.Run("round-robin", func(t *testing.T) {
		manager := NewManager(nil, &FillFirstSelector{}, nil)
		manager.executors["gemini"] = schedulerTestExecutor{}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(auth-a) error = %v", errRegister)
		}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(auth-b) error = %v", errRegister)
		}
		manager.SetPluginScheduler(&fakePluginScheduler{
			resp:    pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinRoundRobin},
			handled: true,
		})

		gotA, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNext() first error = %v", errPick)
		}
		gotB, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNext() second error = %v", errPick)
		}
		if gotA == nil || gotB == nil {
			t.Fatalf("pickNext() auths = %v, %v; want non-nil", gotA, gotB)
		}
		if gotA.ID != "auth-a" || gotB.ID != "auth-b" {
			t.Fatalf("round-robin picks = %q, %q; want auth-a, auth-b", gotA.ID, gotB.ID)
		}
	})

	t.Run("round-robin model cursors", func(t *testing.T) {
		reg := registry.GetGlobalRegistry()
		models := []*registry.ModelInfo{{ID: "model-a"}, {ID: "model-b"}}
		for _, authID := range []string{"auth-a", "auth-b"} {
			reg.RegisterClient(authID, "gemini", models)
			t.Cleanup(func() {
				reg.UnregisterClient(authID)
			})
		}

		manager := NewManager(nil, &FillFirstSelector{}, nil)
		manager.executors["gemini"] = schedulerTestExecutor{}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(auth-a) error = %v", errRegister)
		}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(auth-b) error = %v", errRegister)
		}
		manager.SetPluginScheduler(&fakePluginScheduler{
			resp:    pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinRoundRobin},
			handled: true,
		})

		gotModelA, _, errPick := manager.pickNext(context.Background(), "gemini", "model-a", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNext(model-a) error = %v", errPick)
		}
		gotModelB, _, errPick := manager.pickNext(context.Background(), "gemini", "model-b", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNext(model-b) error = %v", errPick)
		}
		if gotModelA == nil || gotModelB == nil {
			t.Fatalf("pickNext() auths = %v, %v; want non-nil", gotModelA, gotModelB)
		}
		if gotModelA.ID != "auth-a" || gotModelB.ID != "auth-a" {
			t.Fatalf("model-scoped round-robin picks = %q, %q; want auth-a, auth-a", gotModelA.ID, gotModelB.ID)
		}
	})

	t.Run("fill-first", func(t *testing.T) {
		manager := NewManager(nil, &RoundRobinSelector{}, nil)
		manager.executors["gemini"] = schedulerTestExecutor{}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(auth-b) error = %v", errRegister)
		}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(auth-a) error = %v", errRegister)
		}
		manager.SetPluginScheduler(&fakePluginScheduler{
			resp:    pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst},
			handled: true,
		})

		got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNext() error = %v", errPick)
		}
		if got == nil {
			t.Fatalf("pickNext() auth = nil")
		}
		if got.ID != "auth-a" {
			t.Fatalf("fill-first pick = %q, want auth-a", got.ID)
		}
	})

	t.Run("round-robin honors weighted", func(t *testing.T) {
		manager := NewManager(nil, &RoundRobinSelector{Weighted: true}, nil)
		manager.executors["gemini"] = schedulerTestExecutor{}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini", Attributes: map[string]string{"weight": "2"}}); errRegister != nil {
			t.Fatalf("Register(auth-a) error = %v", errRegister)
		}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini", Attributes: map[string]string{"weight": "1"}}); errRegister != nil {
			t.Fatalf("Register(auth-b) error = %v", errRegister)
		}
		manager.SetPluginScheduler(&fakePluginScheduler{
			resp:    pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinRoundRobin},
			handled: true,
		})

		want := []string{"auth-a", "auth-b", "auth-a"}
		for index, wantID := range want {
			got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickNext() #%d error = %v", index, errPick)
			}
			if got == nil || got.ID != wantID {
				t.Fatalf("pickNext() #%d auth = %v, want %q", index, got, wantID)
			}
		}
	})

	t.Run("fill-first ignores weighted", func(t *testing.T) {
		manager := NewManager(nil, &RoundRobinSelector{Weighted: true}, nil)
		manager.executors["gemini"] = schedulerTestExecutor{}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini", Attributes: map[string]string{"weight": "100"}}); errRegister != nil {
			t.Fatalf("Register(auth-b) error = %v", errRegister)
		}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini", Attributes: map[string]string{"weight": "1"}}); errRegister != nil {
			t.Fatalf("Register(auth-a) error = %v", errRegister)
		}
		manager.SetPluginScheduler(&fakePluginScheduler{
			resp:    pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst},
			handled: true,
		})

		for index := 0; index < 2; index++ {
			got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickNext() #%d error = %v", index, errPick)
			}
			if got == nil || got.ID != "auth-a" {
				t.Fatalf("pickNext() #%d auth = %v, want auth-a", index, got)
			}
		}
	})
}

func TestManagerPluginSchedulerDelegateRoundRobinUsesNativeMixedRotation(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}
	manager.SetPluginScheduler(&fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinRoundRobin},
		handled: true,
	})

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickNextMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickNextMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickNextMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManagerPluginSchedulerPickNextMixedSelectsProvider(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}
	scheduler := &fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, AuthID: "claude-a"},
		handled: true,
	}
	manager.SetPluginScheduler(scheduler)

	got, executor, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNextMixed() auth = nil")
	}
	if got.ID != "claude-a" {
		t.Fatalf("pickNextMixed() auth.ID = %q, want claude-a", got.ID)
	}
	if provider != "claude" {
		t.Fatalf("pickNextMixed() provider = %q, want claude", provider)
	}
	if executor == nil {
		t.Fatalf("pickNextMixed() executor = nil")
	}
	if len(scheduler.requests) != 1 {
		t.Fatalf("len(scheduler.requests) = %d, want %d", len(scheduler.requests), 1)
	}
	req := scheduler.requests[0]
	if req.Provider != "" {
		t.Fatalf("scheduler request Provider = %q, want empty for mixed provider pick", req.Provider)
	}
	if len(req.Providers) != 2 || req.Providers[0] != "gemini" || req.Providers[1] != "claude" {
		t.Fatalf("scheduler request Providers = %#v, want [gemini claude]", req.Providers)
	}
}

func TestManagerInactivePluginSchedulerKeepsMixedFastPath(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	scheduler := &inactivePluginScheduler{}
	manager.SetPluginScheduler(scheduler)

	got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNextMixed() auth = nil")
	}
	if provider != "gemini" {
		t.Fatalf("pickNextMixed() provider = %q, want gemini", provider)
	}
	if got.ID != "gemini-a" {
		t.Fatalf("pickNextMixed() auth.ID = %q, want gemini-a", got.ID)
	}
	if scheduler.calls != 0 {
		t.Fatalf("scheduler.calls = %d, want %d", scheduler.calls, 0)
	}
}

func TestManagerPluginSchedulerCandidatesAreSafeCopies(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	auth := &Auth{
		ID:       "auth-a",
		Provider: "gemini",
		Status:   StatusActive,
		Attributes: map[string]string{
			"access_token": "token-value",
			"api_key":      "api-key-value",
			"cookie":       "cookie-value",
			"priority":     "7",
			"team":         "alpha",
		},
		Metadata: map[string]any{"tenant": "one"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}

	scheduler := &fakePluginScheduler{
		handled: true,
		pick: func(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
			if len(req.Candidates) != 1 {
				t.Fatalf("len(req.Candidates) = %d, want %d", len(req.Candidates), 1)
			}
			candidate := req.Candidates[0]
			if candidate.ID != "auth-a" || candidate.Provider != "gemini" || candidate.Priority != 7 || candidate.Status != string(StatusActive) {
				t.Fatalf("scheduler candidate = %#v, want sanitized auth-a metadata", candidate)
			}
			for _, key := range []string{"access_token", "api_key", "cookie"} {
				if _, ok := candidate.Attributes[key]; ok {
					t.Fatalf("scheduler candidate Attributes contains sensitive key %q", key)
				}
			}
			if candidate.Attributes["priority"] != "7" {
				t.Fatalf("scheduler candidate priority attribute = %q, want 7", candidate.Attributes["priority"])
			}
			if len(candidate.Metadata) != 0 {
				t.Fatalf("scheduler candidate Metadata = %#v, want empty", candidate.Metadata)
			}
			candidate.Attributes["team"] = "mutated"
			req.Candidates[0] = candidate
			return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-a"}, true, nil
		},
	}
	manager.SetPluginScheduler(scheduler)

	if _, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil); errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}

	manager.mu.RLock()
	gotAttr := manager.auths["auth-a"].Attributes["team"]
	gotAPIKey := manager.auths["auth-a"].Attributes["api_key"]
	manager.mu.RUnlock()
	if gotAttr != "alpha" {
		t.Fatalf("manager auth attribute team = %q, want alpha", gotAttr)
	}
	if gotAPIKey != "api-key-value" {
		t.Fatalf("manager auth attribute api_key = %q, want api-key-value", gotAPIKey)
	}
}

func TestManager_PickNextMixed_UsesAuthScopedModelAlias(t *testing.T) {
	routeModel := "gpt-5.3-codex-spark"
	upstreamModel := "gpt-5.4"
	authID := "codex-plus-auth-scoped-alias"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: upstreamModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "codex",
		Attributes: map[string]string{
			"plan_type": "plus",
		},
		Metadata: map[string]any{
			"model_aliases": routeModel + "=" + upstreamModel,
		},
	}); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", authID, errRegister)
	}

	got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"codex"}, routeModel, cliproxyexecutor.Options{}, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatal("pickNextMixed() auth = nil")
	}
	if got.ID != authID {
		t.Fatalf("pickNextMixed() auth.ID = %q, want %q", got.ID, authID)
	}
	if provider != "codex" {
		t.Fatalf("pickNextMixed() provider = %q, want codex", provider)
	}
	models := manager.prepareExecutionModels(got, routeModel)
	if len(models) != 1 || models[0] != upstreamModel {
		t.Fatalf("prepareExecutionModels() = %#v, want [%q]", models, upstreamModel)
	}
}

func TestManager_PickNextMixed_UsesConfigAuthScopedModelAliasForPlusPlan(t *testing.T) {
	routeModel := "gpt-5.3-codex-spark"
	upstreamModel := "gpt-5.4"
	plusAuthID := "codex-user-plus.json"
	proAuthID := "codex-user-pro.json"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(plusAuthID, "codex", []*registry.ModelInfo{{ID: upstreamModel}})
	reg.RegisterClient(proAuthID, "codex", []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(plusAuthID)
		reg.UnregisterClient(proAuthID)
	})

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{
		OAuthAuthModelAlias: map[string][]internalconfig.OAuthAuthModelAlias{
			"codex": {
				{
					PlanType: "plus",
					Aliases: []internalconfig.OAuthModelAlias{
						{Name: upstreamModel, Alias: routeModel},
					},
				},
			},
		},
	})
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: plusAuthID, Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", plusAuthID, errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: proAuthID, Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", proAuthID, errRegister)
	}

	plusAuth, ok := manager.GetByID(plusAuthID)
	if !ok {
		t.Fatalf("plus auth not registered")
	}
	proAuth, ok := manager.GetByID(proAuthID)
	if !ok {
		t.Fatalf("pro auth not registered")
	}
	if got := manager.selectionModelForAuth(plusAuth, routeModel); got != upstreamModel {
		t.Fatalf("plus selectionModelForAuth() = %q, want %q", got, upstreamModel)
	}
	if got := manager.selectionModelForAuth(proAuth, routeModel); got != routeModel {
		t.Fatalf("pro selectionModelForAuth() = %q, want %q", got, routeModel)
	}
	models := manager.prepareExecutionModels(plusAuth, routeModel)
	if len(models) != 1 || models[0] != upstreamModel {
		t.Fatalf("plus prepareExecutionModels() = %#v, want [%q]", models, upstreamModel)
	}
}

func TestSchedulerPick_SessionAffinitySticksWithinHighestPriority(t *testing.T) {
	model := "claude-3"
	registerSchedulerModels(t, "claude", model, "sticky-low", "sticky-high-a", "sticky-high-b")
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	scheduler := newSchedulerForTest(
		selector,
		&Auth{ID: "sticky-low", Provider: "claude", Attributes: map[string]string{"priority": "0"}},
		&Auth{ID: "sticky-high-b", Provider: "claude", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "sticky-high-a", Provider: "claude", Attributes: map[string]string{"priority": "10"}},
	)
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_scheduler-sticky"}}`),
	}

	first, errPick := scheduler.pickSingle(context.Background(), "claude", model, opts, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() first error = %v", errPick)
	}
	if first == nil {
		t.Fatalf("pickSingle() first auth = nil")
	}
	if first.ID != "sticky-high-a" {
		t.Fatalf("pickSingle() first auth.ID = %q, want %q", first.ID, "sticky-high-a")
	}

	for index := 0; index < 5; index++ {
		got, errPick := scheduler.pickSingle(context.Background(), "claude", model, opts, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != first.ID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, first.ID)
		}
	}
}

func TestSchedulerPick_SessionAffinityNewBindingsUseWeight(t *testing.T) {
	model := "claude-3"
	registerSchedulerModels(t, "claude", model, "weighted-a", "weighted-b")
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{Weighted: true},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	scheduler := newSchedulerForTest(
		selector,
		&Auth{ID: "weighted-a", Provider: "claude", Attributes: map[string]string{"weight": "2"}},
		&Auth{ID: "weighted-b", Provider: "claude", Attributes: map[string]string{"weight": "1"}},
	)

	want := []string{"weighted-a", "weighted-b", "weighted-a"}
	for index, wantID := range want {
		headers := http.Header{}
		headers.Set("X-Session-ID", "scheduler-weighted-session-"+string(rune('a'+index)))
		opts := cliproxyexecutor.Options{Headers: headers}
		got, errPick := scheduler.pickSingle(context.Background(), "claude", model, opts, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}

		again, errAgain := scheduler.pickSingle(context.Background(), "claude", model, opts, nil)
		if errAgain != nil {
			t.Fatalf("pickSingle() #%d repeat error = %v", index, errAgain)
		}
		if again == nil || again.ID != wantID {
			t.Fatalf("pickSingle() #%d repeat auth = %v, want %q", index, again, wantID)
		}
	}
}

func TestSchedulerPick_SessionAffinityWeightedNewBindingsIgnorePriority(t *testing.T) {
	model := "claude-3-priority-weight"
	registerSchedulerModels(t, "claude", model, "high", "low")
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{Weighted: true},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	scheduler := newSchedulerForTest(
		selector,
		&Auth{ID: "high", Provider: "claude", Attributes: map[string]string{"priority": "10", "weight": "1"}},
		&Auth{ID: "low", Provider: "claude", Attributes: map[string]string{"priority": "0", "weight": "2"}},
	)

	want := []string{"low", "high", "low"}
	for index, wantID := range want {
		headers := http.Header{}
		headers.Set("X-Session-ID", "scheduler-weighted-priority-session-"+string(rune('a'+index)))
		opts := cliproxyexecutor.Options{Headers: headers}
		got, errPick := scheduler.pickSingle(context.Background(), "claude", model, opts, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_SessionAffinityPreservesLowerPriorityCache(t *testing.T) {
	model := "claude-3"
	registerSchedulerModels(t, "claude", model, "priority-low", "priority-high-a", "priority-high-b")
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	lowScheduler := newSchedulerForTest(
		selector,
		&Auth{ID: "priority-low", Provider: "claude", Attributes: map[string]string{"priority": "0"}},
	)
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_scheduler-priority"}}`),
	}

	first, errPick := lowScheduler.pickSingle(context.Background(), "claude", model, opts, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() low-only error = %v", errPick)
	}
	if first == nil || first.ID != "priority-low" {
		t.Fatalf("pickSingle() low-only auth = %v, want priority-low", first)
	}

	scheduler := newSchedulerForTest(
		selector,
		&Auth{ID: "priority-low", Provider: "claude", Attributes: map[string]string{"priority": "0"}},
		&Auth{ID: "priority-high-b", Provider: "claude", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "priority-high-a", Provider: "claude", Attributes: map[string]string{"priority": "10"}},
	)
	got, errPick := scheduler.pickSingle(context.Background(), "claude", model, opts, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() with high-priority auths error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickSingle() with high-priority auths = nil")
	}
	if got.ID != "priority-low" {
		t.Fatalf("pickSingle() auth.ID = %q, want sticky %q", got.ID, "priority-low")
	}

	newSessionOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_scheduler-priority-new"}}`),
	}
	newSession, errPick := scheduler.pickSingle(context.Background(), "claude", model, newSessionOpts, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() new session error = %v", errPick)
	}
	if newSession == nil || newSession.ID != "priority-high-a" {
		t.Fatalf("pickSingle() new session auth = %v, want priority-high-a", newSession)
	}
}

func TestSchedulerPick_SessionAffinityReselectsUnavailableCachedAuthInPriorityBucket(t *testing.T) {
	model := "claude-3"
	registerSchedulerModels(t, "claude", model, "reselect-high-a", "reselect-high-b")
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_scheduler-reselect"}}`),
	}
	scheduler := newSchedulerForTest(
		selector,
		&Auth{ID: "reselect-high-a", Provider: "claude", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "reselect-high-b", Provider: "claude", Attributes: map[string]string{"priority": "10"}},
	)

	first, errPick := scheduler.pickSingle(context.Background(), "claude", model, opts, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() first error = %v", errPick)
	}
	if first == nil || first.ID != "reselect-high-a" {
		t.Fatalf("pickSingle() first auth = %v, want reselect-high-a", first)
	}

	cooling := &Auth{
		ID:         "reselect-high-a",
		Provider:   "claude",
		Attributes: map[string]string{"priority": "10"},
		ModelStates: map[string]*ModelState{
			model: {
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(time.Minute),
				Quota:          QuotaState{Exceeded: true},
			},
		},
	}
	scheduler.upsertAuth(cooling)

	got, errPick := scheduler.pickSingle(context.Background(), "claude", model, opts, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() after cooldown error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickSingle() after cooldown auth = nil")
	}
	if got.ID != "reselect-high-b" {
		t.Fatalf("pickSingle() after cooldown auth.ID = %q, want %q", got.ID, "reselect-high-b")
	}
}

func TestManager_InitializesSchedulerForSessionAffinitySelector(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &FillFirstSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()
	manager := NewManager(nil, selector, nil)
	if manager.scheduler == nil {
		t.Fatalf("manager.scheduler = nil")
	}
	if manager.scheduler.strategy != schedulerStrategyFillFirst {
		t.Fatalf("manager.scheduler.strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategyFillFirst)
	}
	if !manager.useSchedulerFastPath() {
		t.Fatalf("manager.useSchedulerFastPath() = false, want true")
	}
}

func TestManagerCustomSelector_FallsBackToLegacyPath(t *testing.T) {
	t.Parallel()

	selector := &trackingSelector{}
	manager := NewManager(nil, selector, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.auths["auth-a"] = &Auth{ID: "auth-a", Provider: "gemini"}
	manager.auths["auth-b"] = &Auth{ID: "auth-b", Provider: "gemini"}

	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNext() auth = nil")
	}
	if selector.calls != 1 {
		t.Fatalf("selector.calls = %d, want %d", selector.calls, 1)
	}
	if len(selector.lastAuthID) != 2 {
		t.Fatalf("len(selector.lastAuthID) = %d, want %d", len(selector.lastAuthID), 2)
	}
	if got.ID != selector.lastAuthID[len(selector.lastAuthID)-1] {
		t.Fatalf("pickNext() auth.ID = %q, want selector-picked %q", got.ID, selector.lastAuthID[len(selector.lastAuthID)-1])
	}
}

func TestManager_InitializesSchedulerForBuiltInSelector(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if manager.scheduler == nil {
		t.Fatalf("manager.scheduler = nil")
	}
	if manager.scheduler.strategy != schedulerStrategyRoundRobin {
		t.Fatalf("manager.scheduler.strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategyRoundRobin)
	}

	manager.SetSelector(&FillFirstSelector{})
	if manager.scheduler.strategy != schedulerStrategyFillFirst {
		t.Fatalf("manager.scheduler.strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategyFillFirst)
	}
}

func TestManager_SchedulerTracksRegisterAndUpdate(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}

	got, errPick := manager.scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != "auth-a" {
		t.Fatalf("scheduler.pickSingle() auth = %v, want auth-a", got)
	}

	if _, errUpdate := manager.Update(context.Background(), &Auth{ID: "auth-a", Provider: "gemini", Disabled: true}); errUpdate != nil {
		t.Fatalf("Update(auth-a) error = %v", errUpdate)
	}

	got, errPick = manager.scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() after update error = %v", errPick)
	}
	if got == nil || got.ID != "auth-b" {
		t.Fatalf("scheduler.pickSingle() after update auth = %v, want auth-b", got)
	}
}

func TestManager_PickNextMixed_UsesSchedulerRotation(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickNextMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickNextMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickNextMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManager_SchedulerSharesThinkingSuffixCooldownAndRegistryState(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	reg := registry.GetGlobalRegistry()
	baseModel := "scheduler-thinking-model"
	reg.RegisterClient("thinking-auth-a", "gemini", []*registry.ModelInfo{{ID: baseModel}})
	reg.RegisterClient("thinking-auth-b", "gemini", []*registry.ModelInfo{{ID: baseModel}})
	t.Cleanup(func() {
		reg.UnregisterClient("thinking-auth-a")
		reg.UnregisterClient("thinking-auth-b")
	})
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "thinking-auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(thinking-auth-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "thinking-auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(thinking-auth-b) error = %v", errRegister)
	}

	retryAfter := time.Hour
	manager.MarkResult(context.Background(), Result{
		AuthID:     "thinking-auth-a",
		Provider:   "gemini",
		Model:      baseModel + "(high)",
		Success:    false,
		Error:      &Error{HTTPStatus: 429, Message: "quota"},
		RetryAfter: &retryAfter,
	})

	auth, ok := manager.GetByID("thinking-auth-a")
	if !ok || auth == nil {
		t.Fatal("thinking-auth-a was not found")
	}
	if len(auth.ModelStates) != 1 || auth.ModelStates[baseModel] == nil {
		t.Fatalf("ModelStates = %+v, want only canonical key %q", auth.ModelStates, baseModel)
	}
	if count := reg.GetModelCount(baseModel); count != 0 {
		t.Fatalf("registry model count during cooldown = %d, want 0", count)
	}
	for _, model := range []string{baseModel, baseModel + "(medium)", baseModel + "(low)"} {
		got, errPick := manager.scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("scheduler.pickSingle(%q) error = %v", model, errPick)
		}
		if got == nil || got.ID != "thinking-auth-b" {
			t.Fatalf("scheduler.pickSingle(%q) auth = %v, want thinking-auth-b", model, got)
		}
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "thinking-auth-a",
		Provider: "gemini",
		Model:    baseModel + "(low)",
		Success:  true,
	})

	auth, ok = manager.GetByID("thinking-auth-a")
	if !ok || auth == nil || auth.ModelStates[baseModel] == nil {
		t.Fatal("canonical model state was not retained after success")
	}
	state := auth.ModelStates[baseModel]
	if state.Unavailable || state.Quota.Exceeded || !state.NextRetryAfter.IsZero() {
		t.Fatalf("canonical model state after success = %+v, want cleared", state)
	}
	if count := reg.GetModelCount(baseModel); count != 2 {
		t.Fatalf("registry model count after recovery = %d, want 2", count)
	}
}

func TestManager_PickNextMixed_SkipsProvidersWithoutExecutors(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNextMixed() auth = nil")
	}
	if provider != "claude" {
		t.Fatalf("pickNextMixed() provider = %q, want %q", provider, "claude")
	}
	if got.ID != "claude-a" {
		t.Fatalf("pickNextMixed() auth.ID = %q, want %q", got.ID, "claude-a")
	}
}

func TestManager_SchedulerTracksMarkResultCooldownAndRecovery(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-a", "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	reg.RegisterClient("auth-b", "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient("auth-a")
		reg.UnregisterClient("auth-b")
	})
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "auth-a",
		Provider: "gemini",
		Model:    "test-model",
		Success:  false,
		Error:    &Error{HTTPStatus: 429, Message: "quota"},
	})

	got, errPick := manager.scheduler.pickSingle(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() after cooldown error = %v", errPick)
	}
	if got == nil || got.ID != "auth-b" {
		t.Fatalf("scheduler.pickSingle() after cooldown auth = %v, want auth-b", got)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "auth-a",
		Provider: "gemini",
		Model:    "test-model",
		Success:  true,
	})

	seen := make(map[string]struct{}, 2)
	for index := 0; index < 2; index++ {
		got, errPick = manager.scheduler.pickSingle(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("scheduler.pickSingle() after recovery #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("scheduler.pickSingle() after recovery #%d auth = nil", index)
		}
		seen[got.ID] = struct{}{}
	}
	if len(seen) != 2 {
		t.Fatalf("len(seen) = %d, want %d", len(seen), 2)
	}
}
