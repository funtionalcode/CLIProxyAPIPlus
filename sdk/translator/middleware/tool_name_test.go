package middleware

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/ir"
)

func TestToolNameNormalization_ShortensLongNames(t *testing.T) {
	longName := "mcp__very_long_server_name_that_exceeds_the_sixty_four_character_limit__tool"
	req := &ir.IRRequest{
		Tools: []ir.Tool{
			{Name: longName, Description: "a tool"},
			{Name: "short_tool", Description: "another tool"},
		},
	}

	mw := ToolNameNormalization()
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		return r, nil
	}

	result, err := mw(context.Background(), req, translator.FormatClaude, translator.FormatCodex, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Long name should be shortened.
	if len(result.Tools[0].Name) > 64 {
		t.Errorf("tool name not shortened: len=%d, name=%q", len(result.Tools[0].Name), result.Tools[0].Name)
	}
	// Short name should be unchanged.
	if result.Tools[1].Name != "short_tool" {
		t.Errorf("short tool name changed: %q", result.Tools[1].Name)
	}
	// Original name should be stored.
	if result.Tools[0].OriginalName != longName {
		t.Errorf("OriginalName = %q, want %q", result.Tools[0].OriginalName, longName)
	}
	// Short tool should not have OriginalName set.
	if result.Tools[1].OriginalName != "" {
		t.Errorf("short tool OriginalName should be empty, got %q", result.Tools[1].OriginalName)
	}
}

func TestToolNameNormalization_NormalizesToolCallIDs(t *testing.T) {
	longID := strings.Repeat("a", 100)
	req := &ir.IRRequest{
		Messages: []ir.Message{
			{
				Role: "assistant",
				ToolCalls: []ir.ToolCall{
					{ID: longID, Type: "function", Name: "test"},
				},
			},
		},
	}

	mw := ToolNameNormalization()
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		return r, nil
	}

	result, err := mw(context.Background(), req, translator.FormatClaude, translator.FormatCodex, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	normalizedID := result.Messages[0].ToolCalls[0].ID
	if len(normalizedID) >= len(longID) {
		t.Errorf("tool call ID not shortened: len=%d", len(normalizedID))
	}
	if len(normalizedID) > 49 {
		t.Errorf("normalized ID too long: len=%d, want <=49", len(normalizedID))
	}
}

func TestToolNameNormalization_StoresMapsInMetadata(t *testing.T) {
	longName := "mcp__very_long_server_name_that_exceeds_the_sixty_four_character_limit__tool"
	req := &ir.IRRequest{
		Tools: []ir.Tool{
			{Name: longName},
		},
	}

	mw := ToolNameNormalization()
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		return r, nil
	}

	result, err := mw(context.Background(), req, translator.FormatClaude, translator.FormatCodex, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Metadata == nil {
		t.Fatal("metadata should not be nil")
	}
	nameMap, ok := result.Metadata["__tool_name_map"].(map[string]string)
	if !ok {
		t.Fatal("__tool_name_map not found in metadata")
	}
	if len(nameMap) == 0 {
		t.Error("name map should not be empty")
	}
}

func TestToolNameNormalization_SkipsEmptyToolsAndMessages(t *testing.T) {
	req := &ir.IRRequest{
		Model: "gpt-4",
	}

	mw := ToolNameNormalization()
	called := false
	terminal := func(ctx context.Context, r *ir.IRRequest) (*ir.IRRequest, error) {
		called = true
		return r, nil
	}

	_, err := mw(context.Background(), req, translator.FormatClaude, translator.FormatCodex, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("terminal was not called")
	}
}

func TestToolNameRestoration_RestoresNamesFromContext(t *testing.T) {
	nameMap := map[string]string{
		"short_name": "very_long_original_name_that_was_shortened",
	}
	idMap := map[string]string{
		"abc123": "original-long-id-abc123",
	}

	resp := &ir.IRResponse{
		ToolCalls: []ir.ToolCall{
			{Name: "short_name", ID: "abc123", Type: "function"},
			{Name: "unmapped", ID: "def456", Type: "function"},
		},
	}

	mw := ToolNameRestoration()
	terminal := func(ctx context.Context, r *ir.IRResponse) (*ir.IRResponse, error) {
		return r, nil
	}

	ctx := ContextWithToolMaps(context.Background(), nameMap, idMap)
	result, err := mw(ctx, resp, translator.FormatCodex, translator.FormatClaude, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ToolCalls[0].Name != "very_long_original_name_that_was_shortened" {
		t.Errorf("name not restored: %q", result.ToolCalls[0].Name)
	}
	if result.ToolCalls[0].ID != "original-long-id-abc123" {
		t.Errorf("id not restored: %q", result.ToolCalls[0].ID)
	}
	// Unmapped values should remain unchanged.
	if result.ToolCalls[1].Name != "unmapped" {
		t.Errorf("unmapped name changed: %q", result.ToolCalls[1].Name)
	}
	if result.ToolCalls[1].ID != "def456" {
		t.Errorf("unmapped id changed: %q", result.ToolCalls[1].ID)
	}
}

func TestToolNameRestoration_NilMaps(t *testing.T) {
	resp := &ir.IRResponse{
		ToolCalls: []ir.ToolCall{
			{Name: "tool_a", ID: "id_1", Type: "function"},
		},
	}

	mw := ToolNameRestoration()
	terminal := func(ctx context.Context, r *ir.IRResponse) (*ir.IRResponse, error) {
		return r, nil
	}

	result, err := mw(context.Background(), resp, translator.FormatCodex, translator.FormatClaude, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should remain unchanged.
	if result.ToolCalls[0].Name != "tool_a" {
		t.Errorf("name changed: %q", result.ToolCalls[0].Name)
	}
}

func TestContextWithToolMaps_SetsAndGets(t *testing.T) {
	nameMap := map[string]string{"a": "b"}
	idMap := map[string]string{"c": "d"}

	ctx := ContextWithToolMaps(context.Background(), nameMap, idMap)

	gotName, ok := ctx.Value(ctxKeyToolNameMap).(map[string]string)
	if !ok {
		t.Fatal("name map not found in context")
	}
	if gotName["a"] != "b" {
		t.Errorf("name map value = %q, want %q", gotName["a"], "b")
	}

	gotID, ok := ctx.Value(ctxKeyToolIDMap).(map[string]string)
	if !ok {
		t.Fatal("id map not found in context")
	}
	if gotID["c"] != "d" {
		t.Errorf("id map value = %q, want %q", gotID["c"], "d")
	}
}

func TestContextWithToolMaps_NilMaps(t *testing.T) {
	ctx := ContextWithToolMaps(context.Background(), nil, nil)

	if ctx.Value(ctxKeyToolNameMap) != nil {
		t.Error("name map should be nil")
	}
	if ctx.Value(ctxKeyToolIDMap) != nil {
		t.Error("id map should be nil")
	}
}

func TestToolNameNormalization_JSONRoundTrip(t *testing.T) {
	// Verify that metadata with tool maps survives JSON round-trip.
	nameMap := map[string]string{"short": "long_original"}
	meta := map[string]any{
		"__tool_name_map": nameMap,
	}

	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var restored map[string]any
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	restoredMap, ok := restored["__tool_name_map"].(map[string]any)
	if !ok {
		t.Fatal("name map not found after round-trip")
	}
	if restoredMap["short"] != "long_original" {
		t.Errorf("value = %q, want %q", restoredMap["short"], "long_original")
	}
}
