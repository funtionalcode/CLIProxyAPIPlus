package translator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/ir"
)

func TestWrapIRRequestMiddleware_ParsesAndSerializes(t *testing.T) {
	// Register a simple IR middleware that adds a field.
	mw := func(ctx context.Context, req *ir.IRRequest, from, to Format, next IRRequestHandler) (*ir.IRRequest, error) {
		if req.Metadata == nil {
			req.Metadata = make(map[string]any)
		}
		req.Metadata["injected"] = true
		return next(ctx, req)
	}

	wrapped := WrapIRRequestMiddleware(mw)

	input := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)
	env := RequestEnvelope{
		Format:   FormatOpenAI,
		Model:    "gpt-4",
		Body:     input,
		Metadata: map[string]any{"__to_format": string(FormatClaude)},
	}

	terminal := func(ctx context.Context, req RequestEnvelope) (RequestEnvelope, error) {
		return req, nil
	}

	result, err := wrapped(context.Background(), env, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The middleware should have parsed the request, injected the field, and serialized back.
	var parsed map[string]any
	if err := json.Unmarshal(result.Body, &parsed); err != nil {
		t.Fatalf("failed to parse result body: %v", err)
	}
}

func TestWrapIRRequestMiddleware_UnregisteredFormat_Passthrough(t *testing.T) {
	mw := func(ctx context.Context, req *ir.IRRequest, from, to Format, next IRRequestHandler) (*ir.IRRequest, error) {
		return next(ctx, req)
	}

	wrapped := WrapIRRequestMiddleware(mw)

	input := []byte(`{"model":"test"}`)
	env := RequestEnvelope{
		Format: Format("unknown-format"),
		Body:   input,
	}

	terminal := func(ctx context.Context, req RequestEnvelope) (RequestEnvelope, error) {
		return req, nil
	}

	result, err := wrapped(context.Background(), env, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Body should be unchanged since format is not registered.
	if string(result.Body) != string(input) {
		t.Errorf("body was modified for unregistered format")
	}
}

func TestWrapIRRequestMiddlewareWithFormats_ExplicitFormats(t *testing.T) {
	var capturedFrom, capturedTo Format
	mw := func(ctx context.Context, req *ir.IRRequest, from, to Format, next IRRequestHandler) (*ir.IRRequest, error) {
		capturedFrom = from
		capturedTo = to
		return next(ctx, req)
	}

	wrapped := WrapIRRequestMiddlewareWithFormats(mw, FormatClaude, FormatOpenAI)

	input := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}`)
	env := RequestEnvelope{
		Format: FormatClaude,
		Body:   input,
	}

	terminal := func(ctx context.Context, req RequestEnvelope) (RequestEnvelope, error) {
		return req, nil
	}

	_, err := wrapped(context.Background(), env, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedFrom != FormatClaude {
		t.Errorf("from = %q, want %q", capturedFrom, FormatClaude)
	}
	if capturedTo != FormatOpenAI {
		t.Errorf("to = %q, want %q", capturedTo, FormatOpenAI)
	}
}

func TestWrapIRResponseMiddleware_ParsesAndSerializes(t *testing.T) {
	mw := func(ctx context.Context, resp *ir.IRResponse, from, to Format, next IRResponseHandler) (*ir.IRResponse, error) {
		resp.Model = "modified-model"
		return next(ctx, resp)
	}

	wrapped := WrapIRResponseMiddleware(mw)

	input := []byte(`{"id":"resp-1","model":"gpt-4","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}`)
	env := ResponseEnvelope{
		Format: FormatCodex,
		Body:   input,
	}

	terminal := func(ctx context.Context, resp ResponseEnvelope) (ResponseEnvelope, error) {
		return resp, nil
	}

	result, err := wrapped(context.Background(), env, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(result.Body, &parsed); err != nil {
		t.Fatalf("failed to parse result body: %v", err)
	}
	if parsed["model"] != "modified-model" {
		t.Errorf("model = %q, want %q", parsed["model"], "modified-model")
	}
}

func TestWrapIRResponseMiddleware_UnregisteredFormat_Passthrough(t *testing.T) {
	mw := func(ctx context.Context, resp *ir.IRResponse, from, to Format, next IRResponseHandler) (*ir.IRResponse, error) {
		return next(ctx, resp)
	}

	wrapped := WrapIRResponseMiddleware(mw)

	input := []byte(`{"id":"resp-1","model":"gpt-4"}`)
	env := ResponseEnvelope{
		Format: Format("unknown"),
		Body:   input,
	}

	terminal := func(ctx context.Context, resp ResponseEnvelope) (ResponseEnvelope, error) {
		return resp, nil
	}

	result, err := wrapped(context.Background(), env, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(result.Body) != string(input) {
		t.Errorf("body was modified for unregistered format")
	}
}
