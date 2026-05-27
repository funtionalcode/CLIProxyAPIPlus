package middleware

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/ir"
)

func TestThinkingPreservation_ClaudeToOpenAI_PreservesBudgetTokens(t *testing.T) {
	budget := int64(10000)
	req := &ir.IRRequest{
		Thinking: &ir.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
	}

	mw := ThinkingPreservation()
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		return r, nil
	}

	result, err := mw(context.Background(), req, translator.FormatClaude, translator.FormatOpenAIResponse, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Passthrough == nil {
		t.Fatal("passthrough should not be nil")
	}
	raw, ok := result.Passthrough["__original_budget_tokens"]
	if !ok {
		t.Fatal("__original_budget_tokens not in passthrough")
	}
	var restored int64
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatalf("failed to unmarshal budget_tokens: %v", err)
	}
	if restored != 10000 {
		t.Errorf("budget_tokens = %d, want 10000", restored)
	}
}

func TestThinkingPreservation_OpenAIToClaude_PreservesEffort(t *testing.T) {
	req := &ir.IRRequest{
		Thinking: &ir.ThinkingConfig{
			Type:   "enabled",
			Effort: "high",
		},
	}

	mw := ThinkingPreservation()
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		return r, nil
	}

	result, err := mw(context.Background(), req, translator.FormatOpenAIResponse, translator.FormatClaude, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Passthrough == nil {
		t.Fatal("passthrough should not be nil")
	}
	raw, ok := result.Passthrough["__original_effort"]
	if !ok {
		t.Fatal("__original_effort not in passthrough")
	}
	var restored string
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatalf("failed to unmarshal effort: %v", err)
	}
	if restored != "high" {
		t.Errorf("effort = %q, want %q", restored, "high")
	}
}

func TestThinkingPreservation_SkipsNilThinking(t *testing.T) {
	req := &ir.IRRequest{
		Model: "gpt-4",
	}

	mw := ThinkingPreservation()
	called := false
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		called = true
		return r, nil
	}

	result, err := mw(context.Background(), req, translator.FormatClaude, translator.FormatOpenAIResponse, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("terminal was not called")
	}
	if result.Passthrough != nil {
		t.Error("passthrough should be nil when thinking is nil")
	}
}

func TestThinkingPreservation_DoesNotOverwriteExisting(t *testing.T) {
	budget := int64(10000)
	existing := json.RawMessage(`5000`)
	req := &ir.IRRequest{
		Thinking: &ir.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
		Passthrough: map[string]json.RawMessage{
			"__original_budget_tokens": existing,
		},
	}

	mw := ThinkingPreservation()
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		return r, nil
	}

	result, err := mw(context.Background(), req, translator.FormatClaude, translator.FormatOpenAIResponse, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT overwrite.
	if string(result.Passthrough["__original_budget_tokens"]) != string(existing) {
		t.Errorf("existing value was overwritten: got %s, want %s",
			result.Passthrough["__original_budget_tokens"], existing)
	}
}

func TestThinkingPreservation_IrrelevantFormatPair(t *testing.T) {
	budget := int64(10000)
	req := &ir.IRRequest{
		Thinking: &ir.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
	}

	mw := ThinkingPreservation()
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		return r, nil
	}

	// Claude→Codex should not trigger preservation (only Claude→OpenAIResponse does).
	result, err := mw(context.Background(), req, translator.FormatClaude, translator.FormatCodex, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Passthrough != nil {
		t.Error("passthrough should be nil for irrelevant format pair")
	}
}

func TestThinkingPreservation_ClaudeToOpenAI_NoBudgetTokens(t *testing.T) {
	req := &ir.IRRequest{
		Thinking: &ir.ThinkingConfig{
			Type:   "enabled",
			Effort: "medium", // effort set but no budget_tokens
		},
	}

	mw := ThinkingPreservation()
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		return r, nil
	}

	result, err := mw(context.Background(), req, translator.FormatClaude, translator.FormatOpenAIResponse, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No budget_tokens to preserve, so passthrough should remain nil.
	if result.Passthrough != nil {
		if _, ok := result.Passthrough["__original_budget_tokens"]; ok {
			t.Error("should not store budget_tokens when it is nil")
		}
	}
}
