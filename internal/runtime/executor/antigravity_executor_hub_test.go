package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type antigravityHubTestTransport func(*http.Request) (*http.Response, error)

func (f antigravityHubTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAntigravityHubFeaturesAcrossExecutionPaths(t *testing.T) {
	// The process-wide usage dispatcher must live outside the synthetic clock.
	usage.StartDefault(context.Background())
	for _, model := range []string{"gemini-3.6-flash-high", "claude-sonnet-4-6"} {
		for _, operation := range []string{"execute", "stream", "count"} {
			for _, override := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/override=%t", model, operation, override), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						var requestBody, telemetryBody []byte
						var requestHeaders http.Header
						telemetryCalls := 0
						transport := antigravityHubTestTransport(func(r *http.Request) (*http.Response, error) {
							body, errRead := io.ReadAll(r.Body)
							if errRead != nil {
								return nil, errRead
							}
							if r.URL.Host == "play.googleapis.com" {
								telemetryBody = body
								telemetryCalls++
								return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
							}
							requestBody, requestHeaders = body, r.Header.Clone()
							response := `{"response":{"modelVersion":"served-model","responseId":"response-test","candidates":[{"content":{"role":"model","parts":[{"text":"private-answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"totalTokenCount":13}}}`
							contentType := "application/json"
							if strings.Contains(r.URL.Path, "streamGenerateContent") {
								contentType = "text/event-stream"
								response = "data: " + response + "\n\n"
							}
							if strings.Contains(r.URL.Path, "countTokens") {
								response = `{"totalTokens":10}`
							}
							return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
						})
						cfg := &config.Config{}
						cfg.Antigravity.Telemetry.Mode = "send"
						auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "antigravity", Metadata: map[string]any{"access_token": "secret-token", "project_id": "project", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}}
						if override {
							auth.Attributes = map[string]string{"header:User-Agent": "antigravity/hub/2.30.1 (aidev_client; os_type=linux; arch=amd64)", "header:Accept-Language": "ja-JP", "header:Host": "custom.example"}
						}
						ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
						req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"systemInstruction":{"parts":[{"text":"Caller contract: JSON only."}]},"contents":[{"role":"user","parts":[{"text":"private-prompt"}]}]}`)}
						opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini, Headers: http.Header{"Session-Id": {"conversation"}}}
						exec := NewAntigravityExecutor(cfg)
						switch operation {
						case "execute":
							if _, errExecute := exec.Execute(ctx, auth, req, opts); errExecute != nil {
								t.Fatal(errExecute)
							}
						case "stream":
							result, errStream := exec.ExecuteStream(ctx, auth, req, opts)
							if errStream != nil {
								t.Fatal(errStream)
							}
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									t.Fatal(chunk.Err)
								}
							}
						case "count":
							if _, errCount := exec.CountTokens(ctx, auth, req, opts); errCount != nil {
								t.Fatal(errCount)
							}
						}
						synctest.Wait()
						profile := misc.ParseAntigravityDeviceProfile(requestHeaders.Get("User-Agent"))
						wire := ojson.NewDoc(requestBody)
						if !strings.Contains(wire.Get("request.systemInstruction.parts.0.text").String(), "Hub "+profile.Version+", "+profile.OS+"/"+profile.Arch) {
							t.Fatal("prompt profile differs from final HTTP headers")
						}
						if wire.Get("request.systemInstruction.parts.1.text").String() != "Caller contract: JSON only." {
							t.Fatal("caller system instruction changed")
						}
						wantLocale := helps.AntigravityProfile(cfg, auth).Locale
						if override {
							wantLocale = "ja-JP"
						}
						if requestHeaders.Get("Accept-Language") != wantLocale {
							t.Fatal("locale override/profile not applied")
						}
						wantTelemetry := 1
						if operation == "count" {
							wantTelemetry = 0
						}
						if telemetryCalls != wantTelemetry {
							t.Fatalf("telemetry calls=%d, want %d", telemetryCalls, wantTelemetry)
						}
						if wantTelemetry == 1 && (!strings.Contains(string(telemetryBody), model) || !strings.Contains(string(telemetryBody), profile.Version) || !strings.Contains(string(telemetryBody), wire.Get("request.labels.trajectory_id").String())) {
							t.Fatal("telemetry does not match request/profile")
						}
						for _, secret := range []string{"private-prompt", "private-answer", "secret-token", "Caller contract"} {
							if strings.Contains(string(telemetryBody), secret) {
								t.Fatalf("telemetry includes %s", secret)
							}
						}
					})
				})
			}
		}
	}
}
