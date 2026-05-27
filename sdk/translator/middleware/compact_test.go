package middleware

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/ir"
)

func TestCompactPassthrough_ExtractsFields(t *testing.T) {
	rawBody := []byte(`{
		"model": "gpt-4",
		"input": "hello",
		"context_management": {"type": "auto"},
		"truncation": {"type": "auto"}
	}`)

	req := &ir.IRRequest{
		Model: "gpt-4",
		Metadata: map[string]any{
			"compact":    true,
			"__raw_body": rawBody,
		},
	}

	mw := CompactPassthrough()
	called := false
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		called = true
		return r, nil
	}

	_, err := mw(context.Background(), req, translator.FormatOpenAIResponse, translator.FormatCodex, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("terminal was not called")
	}

	if req.Passthrough == nil {
		t.Fatal("passthrough should not be nil")
	}
	if _, ok := req.Passthrough["context_management"]; !ok {
		t.Error("context_management not preserved in passthrough")
	}
	if _, ok := req.Passthrough["truncation"]; !ok {
		t.Error("truncation not preserved in passthrough")
	}
}

func TestCompactPassthrough_SkipsNonCompact(t *testing.T) {
	req := &ir.IRRequest{
		Model:    "gpt-4",
		Metadata: map[string]any{"compact": false},
	}

	mw := CompactPassthrough()
	called := false
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		called = true
		return r, nil
	}

	result, err := mw(context.Background(), req, translator.FormatOpenAIResponse, translator.FormatCodex, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("terminal was not called")
	}
	if result.Passthrough != nil {
		t.Error("passthrough should be nil for non-compact request")
	}
}

func TestCompactPassthrough_NoRawBody(t *testing.T) {
	req := &ir.IRRequest{
		Model:    "gpt-4",
		Metadata: map[string]any{"compact": true},
	}

	mw := CompactPassthrough()
	called := false
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		called = true
		return r, nil
	}

	result, err := mw(context.Background(), req, translator.FormatOpenAIResponse, translator.FormatCodex, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("terminal was not called")
	}
	// Passthrough map may be initialized but should contain no extracted fields.
	if len(result.Passthrough) != 0 {
		t.Errorf("passthrough should be empty when no raw body, got %d entries", len(result.Passthrough))
	}
}

func TestCompactPassthrough_DoesNotOverwriteExisting(t *testing.T) {
	rawBody := []byte(`{
		"model": "gpt-4",
		"context_management": {"type": "auto"}
	}`)

	existingCM := json.RawMessage(`{"type":"manual","keep_last":5}`)
	req := &ir.IRRequest{
		Model: "gpt-4",
		Metadata: map[string]any{
			"compact":    true,
			"__raw_body": rawBody,
		},
		Passthrough: map[string]json.RawMessage{
			"context_management": existingCM,
		},
	}

	mw := CompactPassthrough()
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		return r, nil
	}

	result, err := mw(context.Background(), req, translator.FormatOpenAIResponse, translator.FormatCodex, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT overwrite the existing value.
	if string(result.Passthrough["context_management"]) != string(existingCM) {
		t.Errorf("existing context_management was overwritten: got %s, want %s",
			result.Passthrough["context_management"], existingCM)
	}
}

func TestExtractJSONField(t *testing.T) {
	raw := []byte(`{"name":"test","count":42,"nested":{"a":1}}`)

	v := extractJSONField(raw, "name")
	if v == nil {
		t.Fatal("expected non-nil for existing field")
	}
	if string(v) != `"test"` {
		t.Errorf("name = %s, want %q", v, "test")
	}

	v = extractJSONField(raw, "count")
	if v == nil {
		t.Fatal("expected non-nil for existing field")
	}
	if string(v) != "42" {
		t.Errorf("count = %s, want 42", v)
	}

	v = extractJSONField(raw, "missing")
	if v != nil {
		t.Errorf("expected nil for missing field, got %s", v)
	}
}
