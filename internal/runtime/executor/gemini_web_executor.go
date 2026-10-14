package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps/geminiweb"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

// GeminiWebExecutor bridges text conversations to an authenticated Gemini website.
type GeminiWebExecutor struct{ cfg *config.Config }

func NewGeminiWebExecutor(cfg *config.Config) *GeminiWebExecutor { return &GeminiWebExecutor{cfg: cfg} }
func (e *GeminiWebExecutor) Identifier() string                  { return "gemini-web" }
func (e *GeminiWebExecutor) RequestToFormat(_ exec.Request, _ exec.Options) translator.Format {
	return translator.FormatOpenAI
}

func (e *GeminiWebExecutor) client(ctx context.Context, auth *coreauth.Auth) (*geminiweb.Client, error) {
	var credentials geminiweb.Credentials
	if auth != nil {
		credentials.Secure1PSID, _ = auth.Metadata["secure_1psid"].(string)
		credentials.Secure1PSIDTS, _ = auth.Metadata["secure_1psidts"].(string)
	}
	return geminiweb.NewClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0), credentials)
}

func (e *GeminiWebExecutor) prepare(ctx context.Context, req exec.Request, opts exec.Options) ([]byte, []byte, string, error) {
	if err := geminiweb.ValidateInput(req.Payload); err != nil {
		return nil, nil, "", err
	}
	modelID := ""
	mode := 0
	for _, model := range geminiweb.Models() {
		if model.ID == req.Model {
			modelID = model.UpstreamID
			mode = model.Mode
			break
		}
	}
	if modelID == "" {
		return nil, nil, "", &geminiweb.Error{Code: 400, Message: "unknown web model"}
	}
	translated, err := helps.TranslateRequestReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, translator.FormatOpenAI, req.Model, req.Payload, opts.Stream, false)
	if err != nil {
		return nil, nil, "", err
	}
	prompt, err := geminiweb.Prompt(translated)
	if err != nil {
		return nil, nil, "", err
	}
	body := geminiweb.Payload(prompt, mode)
	// Rules target the native positional JSON (for example 0.0 is the prompt).
	// Only form encoding, headers and read-only validation may follow this barrier.
	body = helps.NewPayloadFinalizer(e.cfg, e.Identifier(), req.Model, e.Identifier(), "", body, req, opts)(body)
	if !json.Valid(body) {
		return nil, nil, "", &geminiweb.Error{Code: 400, Message: "payload rules produced invalid JSON"}
	}
	return translated, body, modelID, nil
}

func closeGeminiWebBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("gemini-web: close response")
		}
	}
}

func (e *GeminiWebExecutor) open(ctx context.Context, auth *coreauth.Auth, payload []byte, modelID string) (*http.Response, error) {
	client, err := e.client(ctx, auth)
	if err != nil {
		return nil, err
	}
	if err = client.Bootstrap(ctx); err != nil {
		return nil, err
	}
	return client.Generate(ctx, modelID, payload)
}

func (e *GeminiWebExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req exec.Request, opts exec.Options) (result exec.Response, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)
	opts.Stream = false
	translated, body, modelID, err := e.prepare(ctx, req, opts)
	if err != nil {
		return result, err
	}
	response, err := e.open(ctx, auth, body, modelID)
	if err != nil {
		return result, err
	}
	defer closeGeminiWebBody(response)
	answer, err := geminiweb.ReadStream(response.Body, nil)
	if err != nil {
		return result, err
	}
	completion, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-" + uuid.NewString(), "object": "chat.completion", "created": time.Now().Unix(), "model": req.Model,
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop"}},
	})
	var state any
	output := translator.TranslateNonStream(ctx, translator.FormatOpenAI, exec.ResponseFormatOrSource(opts), req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, completion, &state)
	if len(output) == 0 || helps.ApplyPatchTranslationError(state) != nil {
		return result, &geminiweb.Error{Code: 502, Message: "response translation failed"}
	}
	reporter.EnsurePublished(ctx)
	// Never forward Google Set-Cookie or private response headers to API clients.
	return exec.Response{Payload: output, Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}

func (e *GeminiWebExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req exec.Request, opts exec.Options) (_ *exec.StreamResult, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)
	opts.Stream = true
	translated, body, modelID, err := e.prepare(ctx, req, opts)
	if err != nil {
		return nil, err
	}
	response, err := e.open(ctx, auth, body, modelID)
	if err != nil {
		return nil, err
	}
	out := make(chan exec.StreamChunk)
	go func() {
		defer close(out)
		defer closeGeminiWebBody(response)
		var state any
		id := "chatcmpl-" + uuid.NewString()
		created := time.Now().Unix()
		// The stream translator reads this transport flag from the source request.
		original, _ := sjson.SetBytes(helps.ApplyPatchOriginalRequest(req, opts), "stream", true)
		send := func(line []byte) error {
			chunks := translator.TranslateStream(ctx, translator.FormatOpenAI, exec.ResponseFormatOrSource(opts), req.Model, original, translated, line, &state)
			if helps.ApplyPatchTranslationError(state) != nil {
				return &geminiweb.Error{Code: 502, Message: "stream translation failed"}
			}
			for _, chunk := range chunks {
				select {
				case out <- exec.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}
		frame := func(delta map[string]any, finish any) error {
			payload, _ := json.Marshal(map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": req.Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
			return send(append([]byte("data: "), payload...))
		}
		previous := ""
		_, errRead := geminiweb.ReadStream(response.Body, func(answer string) error {
			delta := map[string]any{"content": strings.TrimPrefix(answer, previous)}
			if previous == "" {
				delta["role"] = "assistant"
			}
			previous = answer
			return frame(delta, nil)
		})
		if errRead == nil {
			errRead = frame(map[string]any{}, "stop")
		}
		if errRead == nil {
			errRead = send([]byte("data: [DONE]"))
		}
		if errRead != nil {
			reporter.PublishFailure(ctx, errRead)
			select {
			case out <- exec.StreamChunk{Err: errRead}:
			case <-ctx.Done():
			}
			return
		}
		reporter.EnsurePublished(ctx)
	}()
	return &exec.StreamResult{Headers: http.Header{"Content-Type": {"text/event-stream"}}, Chunks: out}, nil
}

func (e *GeminiWebExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	client, err := e.client(ctx, auth)
	if err != nil {
		return nil, err
	}
	if err = client.Bootstrap(ctx); err != nil {
		return nil, err
	}
	updated := auth.Clone()
	credentials := client.Credentials()
	updated.Metadata["secure_1psid"] = credentials.Secure1PSID
	updated.Metadata["secure_1psidts"] = credentials.Secure1PSIDTS
	return updated, nil
}

func (e *GeminiWebExecutor) CountTokens(_ context.Context, _ *coreauth.Auth, _ exec.Request, _ exec.Options) (exec.Response, error) {
	return exec.Response{}, &geminiweb.Error{Code: 501, Message: "Gemini Web does not expose token counting"}
}

func (e *GeminiWebExecutor) HttpRequest(_ context.Context, _ *coreauth.Auth, _ *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("gemini-web: arbitrary authenticated HTTP requests are not supported")
}
