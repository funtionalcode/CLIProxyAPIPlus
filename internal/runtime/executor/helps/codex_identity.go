package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	codexCWDLinePattern  = regexp.MustCompile(`(?m)^(\s*(?:Working directory|Current directory|Current working directory|cwd|CWD|pwd|PWD)\s*:\s*).*$`)
	codexHomeLinePattern = regexp.MustCompile(`(?m)^(\s*(?:Home|HOME|Home directory|home directory)\s*:\s*).*$`)
	codexUserLinePattern = regexp.MustCompile(`(?m)^(\s*(?:Current user|Username|User|USER)\s*:\s*).*$`)
	codexCWDTagPattern   = regexp.MustCompile(`(?is)(<cwd>\s*)[^<]*(\s*</cwd>)`)
	codexHomeTagPattern  = regexp.MustCompile(`(?is)(<home>\s*)[^<]*(\s*</home>)`)
	codexUserTagPattern  = regexp.MustCompile(`(?is)(<user>\s*)[^<]*(\s*</user>)`)
)

// ApplyCodexEnvironmentNormalization canonicalizes Codex instructions/input
// environment text while leaving tool call arguments untouched.
func ApplyCodexEnvironmentNormalization(payload []byte, cfg *config.Config, auth *cliproxyauth.Auth) []byte {
	if cfg == nil || !cfg.Codex.NormalizeEnvironment.Enabled || !gjson.ValidBytes(payload) {
		return payload
	}
	env := resolveCodexCanonicalEnvironment(cfg, auth)
	payload = normalizeCodexInstructionsText(payload, env)
	payload = normalizeCodexInputText(payload, env)
	return payload
}

type codexCanonicalEnvironment struct {
	Home string
	CWD  string
	User string
}

func resolveCodexCanonicalEnvironment(cfg *config.Config, auth *cliproxyauth.Auth) codexCanonicalEnvironment {
	accountKey := ClaudeAccountKey(auth, "")
	sum := sha256.Sum256([]byte(accountKey))
	shortHash := hex.EncodeToString(sum[:])[:8]

	user := strings.TrimSpace(cfg.Codex.NormalizeEnvironment.User)
	if user == "" {
		user = "codex-" + shortHash
	}
	home := strings.TrimSpace(cfg.Codex.NormalizeEnvironment.Home)
	if home == "" {
		home = "/Users/" + user
	}
	cwd := strings.TrimSpace(cfg.Codex.NormalizeEnvironment.CWD)
	if cwd == "" {
		cwd = strings.TrimRight(home, "/") + "/project"
	}
	return codexCanonicalEnvironment{Home: home, CWD: cwd, User: user}
}

func normalizeCodexInstructionsText(payload []byte, env codexCanonicalEnvironment) []byte {
	instructions := gjson.GetBytes(payload, "instructions")
	if !instructions.Exists() || instructions.Type != gjson.String {
		return payload
	}
	return setCodexNormalizedText(payload, "instructions", instructions.String(), env, true)
}

func normalizeCodexInputText(payload []byte, env codexCanonicalEnvironment) []byte {
	input := gjson.GetBytes(payload, "input")
	if input.Type == gjson.String {
		return setCodexNormalizedText(payload, "input", input.String(), env, false)
	}
	if !input.IsArray() {
		return payload
	}

	input.ForEach(func(inputIdx, item gjson.Result) bool {
		itemPath := pathWithIndex("input", inputIdx.Int())
		if text := item.Get("text"); text.Exists() && text.Type == gjson.String {
			payload = setCodexNormalizedText(payload, itemPath+".text", text.String(), env, false)
		}

		content := item.Get("content")
		switch {
		case content.Type == gjson.String:
			payload = setCodexNormalizedText(payload, itemPath+".content", content.String(), env, false)
		case content.IsArray():
			content.ForEach(func(contentIdx, part gjson.Result) bool {
				text := part.Get("text")
				if text.Exists() && text.Type == gjson.String {
					path := itemPath + ".content." + intPath(contentIdx.Int()) + ".text"
					payload = setCodexNormalizedText(payload, path, text.String(), env, false)
				}
				return true
			})
		}
		return true
	})
	return payload
}

func setCodexNormalizedText(payload []byte, path, text string, env codexCanonicalEnvironment, force bool) []byte {
	if !force && !codexTextLooksLikeEnvironmentReminder(text) {
		return payload
	}
	normalized := normalizeCodexEnvironmentText(text, env)
	if normalized == text {
		return payload
	}
	updated, errSet := sjson.SetBytes(payload, path, normalized)
	if errSet != nil {
		return payload
	}
	return updated
}

func codexTextLooksLikeEnvironmentReminder(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "<env") ||
		strings.Contains(lower, "<system-reminder") ||
		strings.Contains(lower, "working directory:") ||
		strings.Contains(lower, "current directory:")
}

func normalizeCodexEnvironmentText(text string, env codexCanonicalEnvironment) string {
	out := text
	if env.CWD != "" {
		out = codexCWDLinePattern.ReplaceAllString(out, "${1}"+env.CWD)
		out = codexCWDTagPattern.ReplaceAllString(out, "${1}"+env.CWD+"${2}")
	}
	if env.Home != "" {
		out = codexHomeLinePattern.ReplaceAllString(out, "${1}"+env.Home)
		out = codexHomeTagPattern.ReplaceAllString(out, "${1}"+env.Home+"${2}")
	}
	if env.User != "" {
		out = codexUserLinePattern.ReplaceAllString(out, "${1}"+env.User)
		out = codexUserTagPattern.ReplaceAllString(out, "${1}"+env.User+"${2}")
	}
	return out
}
