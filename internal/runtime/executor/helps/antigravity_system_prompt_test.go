package helps

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravitySystemPromptPreservesCallerContract(t *testing.T) {
	for _, model := range []string{"gemini-3.1-pro", "claude-sonnet-4-6"} {
		t.Run(model, func(t *testing.T) {
			doc := ojson.NewDocString(`{"request":{"systemInstruction":{"role":"system","parts":[{"text":"Return JSON only in Chinese."},{"text":"Use only lookup."}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}],"tools":[{"functionDeclarations":[{"name":"lookup"}]}],"generationConfig":{"responseMimeType":"application/json"}}}`)
			before := doc.Get("request")
			profile := misc.AntigravityDeviceProfile{Version: "2.30.1", OS: "linux", Arch: "amd64"}
			ApplyAntigravitySystemPromptDoc(nil, profile, model, doc)
			ApplyAntigravitySystemPromptDoc(nil, profile, model, doc)
			if doc.Get("request.systemInstruction.parts.#").Int() != 3 || doc.Get("request.systemInstruction.role").String() != "system" {
				t.Fatalf("system role changed or template duplicated: %s", doc.String())
			}
			for i, path := range []string{"request.systemInstruction.parts.1.text", "request.systemInstruction.parts.2.text"} {
				if doc.Get(path).String() != before.Get("systemInstruction.parts").Array()[i].Get("text").String() {
					t.Fatal("caller instructions changed")
				}
			}
			if !strings.Contains(doc.Get("request.systemInstruction.parts.0.text").String(), "Hub 2.30.1, linux/amd64") {
				t.Fatal("template does not match profile")
			}
			for _, path := range []string{"contents", "tools", "generationConfig"} {
				if doc.Get("request."+path).Raw != before.Get(path).Raw {
					t.Fatalf("caller %s changed", path)
				}
			}
		})
	}
}

func TestAntigravitySystemPromptSwitches(t *testing.T) {
	for _, tc := range []struct {
		model, requestType string
		disabled           bool
	}{
		{"gemini-image", "agent", false}, {"gemini", "image_gen", false}, {"gemini", "web_search", false}, {"claude", "agent", true},
	} {
		doc := ojson.NewDocString(`{"request":{"systemInstruction":{"parts":[{"text":"caller"}]}}}`)
		_ = doc.Set("requestType", tc.requestType)
		before := doc.String()
		cfg := &config.Config{}
		if tc.disabled {
			cfg.Antigravity.SystemPrompt.Enabled = new(false)
		}
		ApplyAntigravitySystemPromptDoc(cfg, misc.AntigravityDeviceProfile{}, tc.model, doc)
		if doc.String() != before {
			t.Fatalf("excluded request changed: %+v", tc)
		}
	}
	cfg := &config.Config{}
	cfg.Antigravity.SystemPrompt.Text = "Custom {hub_version} {os}/{arch}"
	doc := ojson.NewDocString(`{"request":{"systemInstruction":"caller"}}`)
	ApplyAntigravitySystemPromptDoc(cfg, misc.AntigravityDeviceProfile{Version: "2.30.1", OS: "linux", Arch: "amd64"}, "gemini", doc)
	if doc.Get("request.systemInstruction.parts.0.text").String() != "Custom 2.30.1 linux/amd64" || doc.Get("request.systemInstruction.parts.1.text").String() != "caller" {
		t.Fatalf("custom template or original string instruction lost: %s", doc.String())
	}
}

func TestAntigravityProfileUnaffectedByTokenRotation(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "account", Metadata: map[string]any{"access_token": "old"}}
	before := AntigravityProfile(nil, auth)
	auth.Metadata["access_token"] = "new"
	if AntigravityProfile(nil, auth) != before {
		t.Fatal("token rotation changed profile")
	}
	auth.Attributes = map[string]string{"user_agent": "custom-client/1.0"}
	if got := AntigravityProfile(nil, auth); got.UserAgent != "custom-client/1.0" {
		t.Fatalf("explicit UA ignored: %#v", got)
	}
}
