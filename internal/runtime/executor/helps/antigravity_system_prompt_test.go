package helps

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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

func TestAntigravityClientNamesOnlyRewriteCallerSystem(t *testing.T) {
	doc := ojson.NewDocString(`{"request":{"systemInstruction":{"role":"system","parts":[{"text":"Claude Code, CLAUDE AGENT SDK, Codex CLI, Codex, Cursor and Windsurf."},{"inlineData":{"mimeType":"text/plain","data":"Codex"}}]},"contents":[{"role":"user","parts":[{"text":"Use Codex"},{"functionCall":{"name":"Codex","args":{"client":"Cursor"}}}]}],"tools":[{"functionDeclarations":[{"name":"Codex","description":"Cursor"}]}],"generationConfig":{"responseJsonSchema":{"properties":{"Codex":{"const":"Cursor"}}}}}}`)
	before := ojson.NewDoc(doc.Bytes())
	cfg := &config.Config{}
	cfg.Antigravity.SystemPrompt.Text = "Claude Code baseline is not caller input."
	cfg.Antigravity.SystemPrompt.ClientFilter.Rules = map[string]string{"cLaUdE aGeNt SdK": "", "CURSOR": "Editor"}
	ApplyAntigravitySystemPromptDoc(cfg, misc.AntigravityDeviceProfile{}, "gemini", doc)
	want := "Antigravity, , Antigravity, Antigravity, Editor and Antigravity."
	if got := doc.Get("request.systemInstruction.parts.1.text").String(); got != want {
		t.Fatalf("caller text = %q, want %q", got, want)
	}
	if got := doc.Get("request.systemInstruction.parts.0.text").String(); got != cfg.Antigravity.SystemPrompt.Text {
		t.Fatalf("baseline was rewritten: %q", got)
	}
	for _, path := range []string{"request.contents", "request.tools", "request.generationConfig"} {
		if doc.Get(path).Raw != before.Get(path).Raw {
			t.Fatalf("rewrote non-system path %s", path)
		}
	}
	if !reflect.DeepEqual(doc.Get("request.systemInstruction.parts.2").Value(), before.Get("request.systemInstruction.parts.1").Value()) {
		t.Fatal("rewrote non-text system part")
	}
	once := doc.String()
	ApplyAntigravitySystemPromptDoc(cfg, misc.AntigravityDeviceProfile{}, "gemini", doc)
	if doc.String() != once {
		t.Fatal("repeated preparation changed output")
	}
}

func TestAntigravityClientFilterIndependentSwitches(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter config.AntigravityClientFilterConfig
		want   string
	}{
		{"default", config.AntigravityClientFilterConfig{}, "Antigravity MyClient"},
		{"disabled", config.AntigravityClientFilterConfig{Enabled: new(false)}, "Codex MyClient"},
		{"custom-only", config.AntigravityClientFilterConfig{Builtins: new(false), Rules: map[string]string{"MyClient": "Antigravity"}}, "Codex Antigravity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Antigravity.SystemPrompt.Enabled = new(false)
			cfg.Antigravity.SystemPrompt.ClientFilter = tc.filter
			doc := ojson.NewDocString(`{"request":{"system_instruction":"Codex MyClient"}}`)
			ApplyAntigravitySystemPromptDoc(cfg, misc.AntigravityDeviceProfile{}, "gemini", doc)
			if got := doc.Get("request.system_instruction.parts.0.text").String(); got != tc.want {
				t.Fatalf("text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAntigravityClientFilterBlockDoesNotMutate(t *testing.T) {
	cfg := &config.Config{}
	cfg.Antigravity.SystemPrompt.ClientFilter.Mode = "block"
	cfg.Antigravity.SystemPrompt.ClientFilter.Rules = map[string]string{"Codex": "Codex"}
	doc := ojson.NewDocString(`{"request":{"systemInstruction":"Use Codex"}}`)
	before := doc.String()
	if err := ApplyAntigravitySystemPromptDoc(cfg, misc.AntigravityDeviceProfile{}, "gemini", doc); err == nil {
		t.Fatal("block mode accepted a matching request")
	}
	if doc.String() != before {
		t.Fatal("blocked request was partially rewritten")
	}
}

func TestAntigravityClaudeCodePromptSignatures(t *testing.T) {
	const source = "You are an interactive agent that helps users with software engineering tasks. Dual-use security tools (C2 frameworks, credential testing, exploit development) require clear authorization context. Refuse malicious requests."
	for _, tc := range []struct {
		name   string
		filter config.AntigravityClientFilterConfig
		want   string
	}{
		{"default", config.AntigravityClientFilterConfig{}, "You are an interactive agent that helps users with software development tasks. Security tools with both defensive and offensive applications (C2 frameworks, credential testing, exploit development) require clear authorization context. Refuse malicious requests."},
		{"disabled", config.AntigravityClientFilterConfig{Enabled: new(false)}, source},
		{"no-builtins", config.AntigravityClientFilterConfig{Builtins: new(false)}, source},
		{"custom-override", config.AntigravityClientFilterConfig{Rules: map[string]string{"SOFTWARE ENGINEERING TASKS": "software engineering tasks", "Dual-use security tools": "Dual-use security tools"}}, source},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Antigravity.SystemPrompt.ClientFilter = tc.filter
			doc := ojson.NewDocString(`{"request":{"systemInstruction":{"role":"system","parts":[]},"contents":[{"role":"user","parts":[{"text":"Dual-use security tools for software engineering tasks"}]}],"tools":[{"functionDeclarations":[{"name":"lookup","description":"Dual-use security tools","parameters":{"type":"object","properties":{"task":{"const":"software engineering tasks"}}}}]}]}}`)
			_ = doc.Set("request.systemInstruction.parts", []any{map[string]any{"text": source}})
			before := ojson.NewDoc(doc.Bytes())
			if err := ApplyAntigravitySystemPromptDoc(cfg, misc.AntigravityDeviceProfile{}, "gemini-4-hydrogen-xhigh", doc); err != nil {
				t.Fatal(err)
			}
			if got := doc.Get("request.systemInstruction.parts.1.text").String(); got != tc.want {
				t.Fatalf("guidance = %q, want %q", got, tc.want)
			}
			for _, path := range []string{"request.systemInstruction.role", "request.contents", "request.tools"} {
				if doc.Get(path).Raw != before.Get(path).Raw {
					t.Fatalf("changed %s", path)
				}
			}
		})
	}
}

func TestAntigravityPromptTextModesAndRole(t *testing.T) {
	source := "Write a response.\n```go\nvar name = `Codex`\n```\nOutput JSON only: {\"name\":\"Codex\"}\nUse /workspace/main.go\n"
	protected := antigravityEncodeCallerText(source, "protected")
	if !strings.HasPrefix(protected, "Wr173 4 r35p0n53.") || !strings.Contains(protected, source[strings.Index(source, "```go"):]) {
		t.Fatalf("protected mode lost literal spans: %s", protected)
	}
	for _, enabled := range []bool{false, true} {
		cfg := &config.Config{}
		cfg.Antigravity.SystemPrompt.Enabled = &enabled
		cfg.Antigravity.SystemPrompt.AsUser = true
		cfg.Antigravity.SystemPrompt.TextMode = "leet"
		doc := ojson.NewDocString(`{"request":{"systemInstruction":"Write a response.","contents":[{"role":"user","parts":[{"text":"original task"}]}]}}`)
		if err := ApplyAntigravitySystemPromptDoc(cfg, misc.AntigravityDeviceProfile{}, "gemini", doc); err != nil {
			t.Fatal(err)
		}
		if doc.Get("request.contents.0.role").String() != "user" || doc.Get("request.contents.0.parts.1.text").String() != "Wr173 4 r35p0n53." || doc.Get("request.contents.1.parts.0.text").String() != "original task" {
			t.Fatalf("role conversion lost history or guidance: %s", doc.String())
		}
		if !strings.Contains(doc.Get("request.systemInstruction.parts").Raw, "leet spelling") {
			t.Fatal("missing decoding guidance")
		}
	}
}

func TestAntigravityPromptPayloadRulesRemainFinal(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "gemini"}}, Params: map[string]any{"systemInstruction.parts.0.text": "Codex user override"}}}}}
	doc := ojson.NewDocString(`{"request":{"systemInstruction":"Codex caller"}}`)
	finalize := NewPayloadFinalizer(cfg, "antigravity", "gemini", "antigravity", "request", nil, cliproxyexecutor.Request{Model: "gemini"}, cliproxyexecutor.Options{})
	if err := ApplyAntigravitySystemPromptDoc(cfg, misc.AntigravityDeviceProfile{}, "gemini", doc); err != nil {
		t.Fatal(err)
	}
	FinalizePayloadDoc(WithPayloadFinalizer(context.Background(), finalize), doc)
	if got := doc.Get("request.systemInstruction.parts.0.text").String(); got != "Codex user override" {
		t.Fatalf("payload override lost: %q", got)
	}
}
