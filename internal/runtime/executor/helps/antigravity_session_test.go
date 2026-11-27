package helps

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
)

func TestAntigravitySessionScopeFor(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	req, opts := session.Enrich(cliproxyexecutor.Request{Payload: payload}, cliproxyexecutor.Options{})
	if scope := AntigravitySessionScopeFor(req, opts); scope.Conversation != "" {
		t.Fatalf("inferred opening text must not select a Hub conversation: %+v", scope)
	}
	opts.Headers = http.Header{"Session-Id": {"explicit-session"}, "Authorization": {"Bearer caller-a"}}
	first := AntigravitySessionScopeFor(req, opts)
	if first.Conversation == "" || first.Caller == "" || strings.Contains(first.Caller, "caller-a") {
		t.Fatalf("explicit session or caller scope missing/unhashed: %+v", first)
	}
	opts.Headers.Set("Authorization", "Bearer caller-b")
	second := AntigravitySessionScopeFor(req, opts)
	if first.Conversation != second.Conversation || first.Caller == second.Caller {
		t.Fatalf("downstream callers not isolated: %+v %+v", first, second)
	}
	opts.Metadata = map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "trusted-caller"}
	opts.OriginalRequest = []byte(`{"sessionId":"original-session"}`)
	opts.Headers = nil
	if scope := AntigravitySessionScopeFor(req, opts); scope.Conversation == "" || scope.Caller != "trusted-caller" {
		t.Fatalf("original body/trusted caller not used: %+v", scope)
	}
	opts.OriginalRequest = payload
	req.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "execution-session"}
	if scope := AntigravitySessionScopeFor(req, opts); scope.Conversation == "" {
		t.Fatal("explicit execution session not recognized")
	}
}

func TestAntigravityHubSessions(t *testing.T) {
	var sessions AntigravitySessions
	auth := &cliproxyauth.Auth{ID: "account-a"}
	scope := AntigravitySessionScope{Caller: "caller-a", Conversation: "explicit-session"}
	build := func(auth *cliproxyauth.Auth, model string, scope AntigravitySessionScope) *ojson.Doc {
		doc := ojson.NewDocString(`{"requestType":"agent","request":{"sessionId":"caller-value","contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
		sessions.ApplyDoc(auth, model, scope, doc)
		return doc
	}
	first := build(auth, "gemini-test", scope)
	second := build(auth.Clone(), "gemini-test", scope)
	installation := first.Get("request.sessionId").String()
	if value, errParse := strconv.ParseInt(installation, 10, 64); errParse != nil || value >= 0 {
		t.Fatalf("installation ID is not a negative int64: %s", installation)
	}
	if second.Get("request.sessionId").String() != installation || second.Get("request.labels.trajectory_id").String() != first.Get("request.labels.trajectory_id").String() {
		t.Fatal("credential clone or continuation changed Hub identity")
	}
	if second.Get("request.labels.last_step_index").String() != "1" || first.Get("request.labels.request_id").String() != second.Get("request.labels.request_id").String() {
		t.Fatal("continuation did not increment the step while keeping trajectory label")
	}
	parts := strings.Split(second.Get("requestId").String(), "/")
	if len(parts) != 5 || parts[0] != "agent" || parts[4] != "2" || parts[1] != second.Get("request.labels.cascade_id").String() || parts[3] != second.Get("request.labels.trajectory_id").String() {
		t.Fatalf("request ID and labels disagree: %v", parts)
	}
	if _, errParse := uuid.Parse(parts[1]); errParse != nil {
		t.Fatal(errParse)
	}
	for _, scenario := range []struct {
		name  string
		auth  *cliproxyauth.Auth
		model string
		scope AntigravitySessionScope
	}{
		{"account", &cliproxyauth.Auth{ID: "account-b"}, "gemini-test", scope},
		{"caller", auth, "gemini-test", AntigravitySessionScope{Caller: "caller-b", Conversation: scope.Conversation}},
		{"model", auth, "claude-test", scope},
		{"conversation", auth, "gemini-test", AntigravitySessionScope{Caller: scope.Caller, Conversation: "other-session"}},
		{"no explicit session", auth, "gemini-test", AntigravitySessionScope{Caller: scope.Caller}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			other := build(scenario.auth, scenario.model, scenario.scope)
			if other.Get("request.labels.cascade_id").String() == first.Get("request.labels.cascade_id").String() || other.Get("request.labels.last_step_index").String() != "0" {
				t.Fatal("independent conversation reused another scope")
			}
			if (other.Get("request.sessionId").String() == installation) != (scenario.auth.ID == auth.ID) {
				t.Fatal("installation must depend on account only")
			}
			if scenario.scope.Conversation == "" && build(scenario.auth, scenario.model, scenario.scope).Get("request.labels.trajectory_id").String() == other.Get("request.labels.trajectory_id").String() {
				t.Fatal("identical opening text merged independent requests")
			}
		})
	}
}

func TestAntigravitySessionsConcurrentSteps(t *testing.T) {
	var sessions AntigravitySessions
	const count = 48
	states := make(chan antigravityConversation, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			states <- sessions.next("account", "model", AntigravitySessionScope{Caller: "caller", Conversation: "session"}, false)
		})
	}
	wg.Wait()
	close(states)
	steps := make(map[uint64]bool)
	trajectory := ""
	for state := range states {
		if steps[state.step] || (trajectory != "" && trajectory != state.trajectory) {
			t.Fatal("concurrent continuation duplicated a step or split a trajectory")
		}
		trajectory = state.trajectory
		steps[state.step] = true
	}
	if !steps[1] || !steps[count] || len(steps) != count {
		t.Fatalf("unexpected steps: %v", steps)
	}
}

func TestAntigravitySessionExpiryAndCapacity(t *testing.T) {
	now := time.Unix(100, 0)
	sessions := AntigravitySessions{now: func() time.Time { return now }}
	scope := AntigravitySessionScope{Caller: "caller", Conversation: "session"}
	first := sessions.next("account", "model", scope, false)
	now = now.Add(antigravitySessionTTL)
	second := sessions.next("account", "model", scope, false)
	if second.trajectory == first.trajectory || second.step != 1 {
		t.Fatal("expired session was reused")
	}
	for i := 0; i < antigravitySessionLimit; i++ {
		now = now.Add(time.Nanosecond)
		sessions.next("account", "model", AntigravitySessionScope{Conversation: fmt.Sprint(i)}, false)
	}
	if len(sessions.sessions) != antigravitySessionLimit {
		t.Fatalf("cache size = %d", len(sessions.sessions))
	}
	if sessions.next("account", "model", scope, false).trajectory == second.trajectory {
		t.Fatal("oldest session was not evicted")
	}
}

func TestAntigravitySessionSpecialRequests(t *testing.T) {
	var sessions AntigravitySessions
	auth := &cliproxyauth.Auth{ID: "account"}
	for _, requestType := range []string{"image_gen", "web_search"} {
		doc := ojson.NewDocString(`{"request":{}}`)
		_ = doc.Set("requestType", requestType)
		if requestType == "image_gen" {
			_ = doc.Set("request.sessionId", "old-agent-session")
			_ = doc.Set("request.labels.cascade_id", "old-agent-cascade")
		}
		sessions.ApplyDoc(auth, "model", AntigravitySessionScope{Conversation: "session"}, doc)
		if doc.Get("request.sessionId").Exists() || doc.Get("request.labels").Exists() {
			t.Fatalf("%s contains agent session fields: %s", requestType, doc.String())
		}
		if requestType == "web_search" && doc.Get("requestId").Exists() {
			t.Fatal("independent web search acquired an agent ID")
		}
		if requestType == "image_gen" {
			parts := strings.Split(doc.Get("requestId").String(), "/")
			if len(parts) != 4 || parts[0] != "image_gen" || parts[3] != "1" {
				t.Fatalf("invalid image request ID: %v", parts)
			}
		}
	}
}
