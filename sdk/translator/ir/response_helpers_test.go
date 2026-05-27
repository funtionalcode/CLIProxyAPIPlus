package ir

import (
	"strings"
	"testing"
)

func TestBuildClaudeToolNameReverseMap(t *testing.T) {
	tests := []struct {
		name      string
		inputJSON string
		wantNil   bool
		wantKey   string // shortened key that should exist
		wantVal   string // original value it maps to
	}{
		{
			name:      "no tools",
			inputJSON: `{"model":"test","messages":[{"role":"user","content":"hi"}]}`,
			wantNil:   true,
		},
		{
			name: "short tool names not shortened",
			inputJSON: `{
				"model":"test",
				"tools":[{"name":"lookup","description":"d","input_schema":{"type":"object"}}],
				"messages":[{"role":"user","content":"hi"}]
			}`,
			wantNil: false,
			wantKey: "lookup",
			wantVal: "lookup",
		},
		{
			name: "long tool name shortened",
			inputJSON: `{
				"model":"test",
				"tools":[{"name":"` + strings.Repeat("a", 100) + `","description":"d","input_schema":{"type":"object"}}],
				"messages":[{"role":"user","content":"hi"}]
			}`,
			wantNil: false,
			wantKey: strings.Repeat("a", 64),
			wantVal: strings.Repeat("a", 100),
		},
		{
			name: "mcp prefix preserved",
			inputJSON: `{
				"model":"test",
				"tools":[{"name":"mcp__server_with_a_very_long_name_that_exceeds_sixty_four_characters__search","description":"d","input_schema":{"type":"object"}}],
				"messages":[{"role":"user","content":"hi"}]
			}`,
			wantNil: false,
			wantKey: "mcp__search",
			wantVal: "mcp__server_with_a_very_long_name_that_exceeds_sixty_four_characters__search",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildClaudeToolNameReverseMap([]byte(tt.inputJSON))
			if tt.wantNil {
				if result != nil {
					t.Fatalf("expected nil, got %v", result)
				}
				return
			}
			if result == nil {
				t.Fatal("expected non-nil map, got nil")
			}
			if got, ok := result[tt.wantKey]; !ok {
				t.Fatalf("expected key %q in map %v", tt.wantKey, result)
			} else if got != tt.wantVal {
				t.Fatalf("map[%q] = %q, want %q", tt.wantKey, got, tt.wantVal)
			}
		})
	}
}

func TestBuildClaudeToolCallIDMap(t *testing.T) {
	longID := "toolu_" + strings.Repeat("a", 62)

	tests := []struct {
		name      string
		inputJSON string
		wantNil   bool
		wantKey   string
		wantVal   string
	}{
		{
			name:      "no messages",
			inputJSON: `{"model":"test"}`,
			wantNil:   true,
		},
		{
			name: "tool_use with short ID",
			inputJSON: `{
				"model":"test",
				"messages":[
					{"role":"assistant","content":[{"type":"tool_use","id":"short_id","name":"x","input":{}}]}
				]
			}`,
			wantNil: false,
			wantKey: "short_id",
			wantVal: "short_id",
		},
		{
			name: "tool_use with long ID",
			inputJSON: `{
				"model":"test",
				"messages":[
					{"role":"assistant","content":[{"type":"tool_use","id":"` + longID + `","name":"x","input":{}}]}
				]
			}`,
			wantNil: false,
			wantVal: longID,
		},
		{
			name: "tool_result with long ID",
			inputJSON: `{
				"model":"test",
				"messages":[
					{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + longID + `","content":"ok"}]}
				]
			}`,
			wantNil: false,
			wantVal: longID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildClaudeToolCallIDMap([]byte(tt.inputJSON))
			if tt.wantNil {
				if result != nil {
					t.Fatalf("expected nil, got %v", result)
				}
				return
			}
			if result == nil {
				t.Fatal("expected non-nil map, got nil")
			}
			// Verify the original ID is in the map values
			found := false
			for _, v := range result {
				if v == tt.wantVal {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected original ID %q in map values %v", tt.wantVal, result)
			}
			// If there's a specific expected key, check it
			if tt.wantKey != "" {
				if _, ok := result[tt.wantKey]; !ok {
					t.Fatalf("expected key %q in map %v", tt.wantKey, result)
				}
			}
		})
	}
}

func TestBuildClaudeToolCallIDMap_RoundTrip(t *testing.T) {
	longID := "toolu_" + strings.Repeat("b", 62)
	inputJSON := `{
		"model":"test",
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"` + longID + `","name":"x","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + longID + `","content":"ok"}]}
		]
	}`

	idMap := BuildClaudeToolCallIDMap([]byte(inputJSON))
	if idMap == nil {
		t.Fatal("expected non-nil map")
	}

	// The shortened form should map back to the original
	shortened := NormalizeToolCallID(longID, nil)
	restored := RestoreToolCallID(shortened, idMap)
	if restored != longID {
		t.Fatalf("round-trip failed: shortened=%q, restored=%q, want=%q", shortened, restored, longID)
	}
}
