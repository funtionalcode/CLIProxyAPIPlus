package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	claudeCWDLinePattern  = regexp.MustCompile(`(?m)^(\s*(?:Working directory|Current directory|Current working directory|cwd|CWD|pwd|PWD)\s*:\s*).*$`)
	claudeHomeLinePattern = regexp.MustCompile(`(?m)^(\s*(?:Home|HOME|Home directory|home directory)\s*:\s*).*$`)
	claudeUserLinePattern = regexp.MustCompile(`(?m)^(\s*(?:Current user|Username|User|USER)\s*:\s*).*$`)
	claudeCWDTagPattern   = regexp.MustCompile(`(?is)(<cwd>\s*)[^<]*(\s*</cwd>)`)
	claudeHomeTagPattern  = regexp.MustCompile(`(?is)(<home>\s*)[^<]*(\s*</home>)`)
	claudeUserTagPattern  = regexp.MustCompile(`(?is)(<user>\s*)[^<]*(\s*</user>)`)
)

// ApplyClaudeIdentityControls applies account-scoped request identity controls
// that must run after request translation and before upstream signing.
func ApplyClaudeIdentityControls(payload []byte, cfg *config.Config, auth *cliproxyauth.Auth, apiKey string) ([]byte, error) {
	var err error
	payload, err = ApplyClaudeSyntheticDeviceID(payload, cfg, auth, apiKey)
	if err != nil {
		return nil, err
	}
	payload, err = ApplyClaudeEnvironmentNormalization(payload, cfg, auth)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// ApplyClaudeSyntheticDeviceID rewrites metadata.user_id.device_id to a
// deterministic per-upstream-account pseudonym while preserving the client
// session_id and any other existing JSON fields.
func ApplyClaudeSyntheticDeviceID(payload []byte, cfg *config.Config, auth *cliproxyauth.Auth, apiKey string) ([]byte, error) {
	if cfg == nil || !cfg.Claude.SyntheticDeviceID.Enabled || !gjson.ValidBytes(payload) {
		return payload, nil
	}
	deviceID := ClaudeSyntheticDeviceID(cfg, auth, apiKey)
	if deviceID == "" {
		return payload, nil
	}

	userID := make(map[string]any)
	rawUserID := strings.TrimSpace(gjson.GetBytes(payload, "metadata.user_id").String())
	if rawUserID != "" {
		var existing map[string]any
		if errUnmarshal := json.Unmarshal([]byte(rawUserID), &existing); errUnmarshal == nil && existing != nil {
			userID = existing
		}
	}
	userID["device_id"] = deviceID
	if _, ok := userID["account_uuid"]; !ok {
		userID["account_uuid"] = ""
	}
	if _, ok := userID["session_id"]; !ok {
		userID["session_id"] = ""
	}

	raw, errMarshal := json.Marshal(userID)
	if errMarshal != nil {
		return nil, errMarshal
	}
	updated, errSet := sjson.SetBytes(payload, "metadata.user_id", string(raw))
	if errSet != nil {
		return nil, errSet
	}
	return updated, nil
}

// ClaudeSyntheticDeviceID returns sha256(server_salt + account_key) as hex.
func ClaudeSyntheticDeviceID(cfg *config.Config, auth *cliproxyauth.Auth, apiKey string) string {
	if cfg == nil || !cfg.Claude.SyntheticDeviceID.Enabled {
		return ""
	}
	accountKey := ClaudeAccountKey(auth, apiKey)
	if accountKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(cfg.Claude.SyntheticDeviceID.Salt + accountKey))
	return hex.EncodeToString(sum[:])
}

// ClaudeAccountKey returns a stable, account-scoped key for deterministic
// fingerprints. Secret tokens are used only as a last-resort internal fallback.
func ClaudeAccountKey(auth *cliproxyauth.Auth, apiKey string) string {
	if auth != nil {
		for _, value := range []string{auth.ID, auth.Index, auth.Label, auth.FileName} {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
		for _, key := range []string{"auth_id", "account_id", "email", "source"} {
			if auth.Attributes != nil {
				if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
					return value
				}
			}
			if auth.Metadata != nil {
				if value, ok := auth.Metadata[key].(string); ok {
					if trimmed := strings.TrimSpace(value); trimmed != "" {
						return trimmed
					}
				}
			}
		}
	}
	if trimmed := strings.TrimSpace(apiKey); trimmed != "" {
		return trimmed
	}
	return "global"
}

// ApplyClaudeEnvironmentNormalization canonicalizes Claude Code env/system
// reminder text while leaving tool parameters and user paths untouched.
func ApplyClaudeEnvironmentNormalization(payload []byte, cfg *config.Config, auth *cliproxyauth.Auth) ([]byte, error) {
	if cfg == nil || !cfg.Claude.NormalizeEnvironment.Enabled || !gjson.ValidBytes(payload) {
		return payload, nil
	}
	env := resolveClaudeCanonicalEnvironment(cfg, auth)

	var err error
	payload, err = normalizeClaudeSystemText(payload, env)
	if err != nil {
		return nil, err
	}
	payload, err = normalizeClaudeMessageReminderText(payload, env)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

type claudeCanonicalEnvironment struct {
	Home string
	CWD  string
	User string
}

func resolveClaudeCanonicalEnvironment(cfg *config.Config, auth *cliproxyauth.Auth) claudeCanonicalEnvironment {
	accountKey := ClaudeAccountKey(auth, "")
	sum := sha256.Sum256([]byte(accountKey))
	shortHash := hex.EncodeToString(sum[:])[:8]

	user := strings.TrimSpace(cfg.Claude.NormalizeEnvironment.User)
	if user == "" {
		user = "claude-" + shortHash
	}
	home := strings.TrimSpace(cfg.Claude.NormalizeEnvironment.Home)
	if home == "" {
		home = "/Users/" + user
	}
	cwd := strings.TrimSpace(cfg.Claude.NormalizeEnvironment.CWD)
	if cwd == "" {
		cwd = strings.TrimRight(home, "/") + "/project"
	}
	return claudeCanonicalEnvironment{Home: home, CWD: cwd, User: user}
}

func normalizeClaudeSystemText(payload []byte, env claudeCanonicalEnvironment) ([]byte, error) {
	system := gjson.GetBytes(payload, "system")
	if !system.Exists() {
		return payload, nil
	}
	if system.Type == gjson.String {
		return setClaudeNormalizedText(payload, "system", system.String(), env, true)
	}
	if !system.IsArray() {
		return payload, nil
	}

	var err error
	system.ForEach(func(idx, part gjson.Result) bool {
		if err != nil {
			return false
		}
		index := idx.Int()
		switch {
		case part.Type == gjson.String:
			payload, err = setClaudeNormalizedText(payload, pathWithIndex("system", index), part.String(), env, true)
		case part.Get("text").Exists():
			payload, err = setClaudeNormalizedText(payload, pathWithIndex("system", index)+".text", part.Get("text").String(), env, true)
		}
		return err == nil
	})
	return payload, err
}

func normalizeClaudeMessageReminderText(payload []byte, env claudeCanonicalEnvironment) ([]byte, error) {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload, nil
	}

	var err error
	messages.ForEach(func(messageIdx, message gjson.Result) bool {
		if err != nil {
			return false
		}
		content := message.Get("content")
		messagePath := pathWithIndex("messages", messageIdx.Int()) + ".content"
		switch {
		case content.Type == gjson.String:
			payload, err = setClaudeNormalizedText(payload, messagePath, content.String(), env, false)
		case content.IsArray():
			content.ForEach(func(contentIdx, part gjson.Result) bool {
				if err != nil {
					return false
				}
				if part.Get("type").String() == "text" && part.Get("text").Exists() {
					path := messagePath + "." + intPath(contentIdx.Int()) + ".text"
					payload, err = setClaudeNormalizedText(payload, path, part.Get("text").String(), env, false)
				}
				return err == nil
			})
		}
		return err == nil
	})
	return payload, err
}

func setClaudeNormalizedText(payload []byte, path, text string, env claudeCanonicalEnvironment, force bool) ([]byte, error) {
	if !force && !claudeTextLooksLikeEnvironmentReminder(text) {
		return payload, nil
	}
	normalized := normalizeClaudeEnvironmentText(text, env)
	if normalized == text {
		return payload, nil
	}
	return sjson.SetBytes(payload, path, normalized)
}

func claudeTextLooksLikeEnvironmentReminder(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "<env") ||
		strings.Contains(lower, "<system-reminder") ||
		strings.Contains(lower, "working directory:") ||
		strings.Contains(lower, "current directory:")
}

func normalizeClaudeEnvironmentText(text string, env claudeCanonicalEnvironment) string {
	out := text
	if env.CWD != "" {
		out = claudeCWDLinePattern.ReplaceAllString(out, "${1}"+env.CWD)
		out = claudeCWDTagPattern.ReplaceAllString(out, "${1}"+env.CWD+"${2}")
	}
	if env.Home != "" {
		out = claudeHomeLinePattern.ReplaceAllString(out, "${1}"+env.Home)
		out = claudeHomeTagPattern.ReplaceAllString(out, "${1}"+env.Home+"${2}")
	}
	if env.User != "" {
		out = claudeUserLinePattern.ReplaceAllString(out, "${1}"+env.User)
		out = claudeUserTagPattern.ReplaceAllString(out, "${1}"+env.User+"${2}")
	}
	return out
}

func pathWithIndex(prefix string, index int64) string {
	return prefix + "." + intPath(index)
}

func intPath(index int64) string {
	return strconv.FormatInt(index, 10)
}
