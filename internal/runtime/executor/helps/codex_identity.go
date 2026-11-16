package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/MoeForge/moego/regexp"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
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
	if cfg == nil || !cfg.Codex.NormalizeEnvironment.Enabled || !ojson.ValidBytes(payload) {
		return payload
	}
	doc := ojson.NewDoc(payload)
	ApplyCodexEnvironmentNormalizationDoc(doc, cfg, auth)
	return doc.Bytes()
}

// ApplyCodexEnvironmentNormalizationDoc updates the shared request document before payload rules run.
func ApplyCodexEnvironmentNormalizationDoc(doc *ojson.Doc, cfg *config.Config, auth *cliproxyauth.Auth) {
	if doc == nil || cfg == nil || !cfg.Codex.NormalizeEnvironment.Enabled {
		return
	}
	env := resolveCodexCanonicalEnvironment(cfg, auth)
	normalizeCodexInstructionsText(doc, env)
	normalizeCodexInputText(doc, env)
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

func normalizeCodexInstructionsText(doc *ojson.Doc, env codexCanonicalEnvironment) {
	instructions := doc.Get("instructions")
	if !instructions.Exists() || instructions.Type != ojson.String {
		return
	}
	setCodexNormalizedText(doc, "instructions", instructions.String(), env, true)
}

func normalizeCodexInputText(doc *ojson.Doc, env codexCanonicalEnvironment) {
	input := doc.Get("input")
	if input.Type == ojson.String {
		setCodexNormalizedText(doc, "input", input.String(), env, false)
		return
	}
	if !input.IsArray() {
		return
	}

	input.ForEach(func(inputIdx, item ojson.Result) bool {
		itemPath := pathWithIndex("input", inputIdx.Int())
		if text := item.Get("text"); text.Exists() && text.Type == ojson.String {
			setCodexNormalizedText(doc, itemPath+".text", text.String(), env, false)
		}

		content := item.Get("content")
		switch {
		case content.Type == ojson.String:
			setCodexNormalizedText(doc, itemPath+".content", content.String(), env, false)
		case content.IsArray():
			content.ForEach(func(contentIdx, part ojson.Result) bool {
				text := part.Get("text")
				if text.Exists() && text.Type == ojson.String {
					path := itemPath + ".content." + intPath(contentIdx.Int()) + ".text"
					setCodexNormalizedText(doc, path, text.String(), env, false)
				}
				return true
			})
		}
		return true
	})
}

func setCodexNormalizedText(doc *ojson.Doc, path, text string, env codexCanonicalEnvironment, force bool) {
	if !force && !codexTextLooksLikeEnvironmentReminder(text) {
		return
	}
	normalized := normalizeCodexEnvironmentText(text, env)
	if normalized == text {
		return
	}
	_ = doc.Set(path, normalized)
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
