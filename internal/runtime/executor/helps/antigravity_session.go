package helps

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
)

const (
	antigravitySessionTTL   = 30 * time.Minute
	antigravitySessionLimit = 4096
)

// AntigravitySessionScope identifies an explicit downstream conversation.
// Inferred prompt identities are deliberately excluded: equal text does not
// establish that two requests belong to the same conversation.
type AntigravitySessionScope struct {
	Caller       string
	Conversation string
}

// AntigravitySessionScopeFor resolves the original request before translation.
func AntigravitySessionScopeFor(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) AntigravitySessionScope {
	var scope AntigravitySessionScope
	metadata := make(map[string]any, 2)
	for _, key := range []string{cliproxyexecutor.CallerScopeMetadataKey, cliproxyexecutor.ExecutionSessionMetadataKey} {
		value := metadataString(opts.Metadata, key)
		if value == "" {
			value = metadataString(req.Metadata, key)
		}
		metadata[key] = value
	}
	scope.Caller = metadataString(metadata, cliproxyexecutor.CallerScopeMetadataKey)
	if scope.Caller == "" {
		scope.Caller = session.CallerScope(opts.Headers.Get("Authorization"))
		if scope.Caller == "" {
			scope.Caller = session.CallerScope(opts.Headers.Get("X-Api-Key"))
		}
	}
	original := opts.OriginalRequest
	if len(original) == 0 {
		original = req.Payload
	}
	if info, ok := session.ExtractSessionInfo(opts.Headers, original, metadata); ok {
		scope.Conversation = info.SessionID
	}
	return scope
}

type antigravityConversation struct {
	conversation string
	trajectory   string
	step         uint64
	lastUsed     time.Time
}

// AntigravitySessions allocates process-local Hub trajectories. Its zero value
// is ready to use. A restart or eviction starts a new trajectory, while the
// upstream installation session remains stable for the selected credential.
type AntigravitySessions struct {
	mu       sync.Mutex
	sessions map[[32]byte]antigravityConversation
	now      func() time.Time
}

// ApplyDoc adds Hub request metadata before the final user payload rules.
func (s *AntigravitySessions) ApplyDoc(auth *cliproxyauth.Auth, model string, scope AntigravitySessionScope, doc *ojson.Doc) {
	requestType := doc.Get("requestType").String()
	if requestType == "web_search" {
		return
	}
	isImage := requestType == "image_gen" || strings.Contains(model, "image")
	account := ""
	if auth != nil {
		account = strings.TrimSpace(auth.ID)
		if account == "" {
			account = strings.TrimSpace(auth.FileName)
		}
		if account == "" {
			account, _ = auth.Metadata["email"].(string)
		}
	}
	if account == "" {
		account = uuid.NewString()
	}

	state := s.next(account, model, scope, isImage)
	if isImage {
		_ = doc.Set("requestId", fmt.Sprintf("image_gen/%d/%s/1", state.lastUsed.UnixMilli(), state.trajectory))
		_ = doc.Delete("request.sessionId")
		_ = doc.Delete("request.labels")
		return
	}
	sum := sha256.Sum256([]byte("cli-proxy-api:antigravity:installation\x00" + account))
	value := binary.BigEndian.Uint64(sum[:8]) & 0x7FFFFFFFFFFFFFFF
	if value == 0 {
		value = 1
	}
	_ = doc.Set("request.sessionId", "-"+strconv.FormatUint(value, 10))
	_ = doc.Set("requestId", fmt.Sprintf("agent/%s/%d/%s/%d", state.conversation, state.lastUsed.UnixMilli(), state.trajectory, state.step))
	for key, value := range map[string]string{
		"cascade_id":               state.conversation,
		"root_cascade_id":          state.conversation,
		"trajectory_id":            state.trajectory,
		"request_id":               state.trajectory + "-0",
		"last_step_index":          strconv.FormatUint(state.step-1, 10),
		"used_claude":              strconv.FormatBool(strings.Contains(strings.ToLower(model), "claude")),
		"used_claude_conservative": strconv.FormatBool(strings.Contains(strings.ToLower(model), "claude")),
		"used_non_gemini_model":    strconv.FormatBool(!strings.HasPrefix(strings.ToLower(model), "gemini")),
	} {
		_ = doc.Set("request.labels."+key, value)
	}
}

func (s *AntigravitySessions) next(account, model string, scope AntigravitySessionScope, image bool) antigravityConversation {
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	if scope.Conversation == "" || image {
		return antigravityConversation{conversation: uuid.NewString(), trajectory: uuid.NewString(), step: 1, lastUsed: now}
	}
	// Hash framed fields so caller-controlled IDs cannot cross namespaces.
	identity, _ := json.Marshal([4]string{account, scope.Caller, model, scope.Conversation})
	key := sha256.Sum256(identity)
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, ok := s.sessions[key]; ok && now.Sub(state.lastUsed) < antigravitySessionTTL {
		state.step++
		state.lastUsed = now
		s.sessions[key] = state
		return state
	}
	if s.sessions == nil {
		s.sessions = make(map[[32]byte]antigravityConversation)
	}
	var oldestKey [32]byte
	var oldest time.Time
	for candidate, state := range s.sessions {
		if now.Sub(state.lastUsed) >= antigravitySessionTTL {
			delete(s.sessions, candidate)
			continue
		}
		if oldest.IsZero() || state.lastUsed.Before(oldest) {
			oldestKey, oldest = candidate, state.lastUsed
		}
	}
	if len(s.sessions) >= antigravitySessionLimit {
		delete(s.sessions, oldestKey)
	}
	state := antigravityConversation{conversation: uuid.NewString(), trajectory: uuid.NewString(), step: 1, lastUsed: now}
	s.sessions[key] = state
	return state
}
