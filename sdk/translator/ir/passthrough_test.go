package ir

import (
	"encoding/json"
	"testing"
)

func TestStorePassthrough_ExtractsUnknownFields(t *testing.T) {
	rawJSON := []byte(`{
		"model": "test",
		"stream": true,
		"context_management": {"compaction": {"enabled": true}},
		"truncation": {"type": "auto"},
		"custom_field": "preserved"
	}`)

	known := map[string]bool{
		"model":  true,
		"stream": true,
	}

	pt := StorePassthrough(rawJSON, known)

	if pt == nil {
		t.Fatal("passthrough is nil")
	}
	if _, ok := pt["context_management"]; !ok {
		t.Error("missing context_management in passthrough")
	}
	if _, ok := pt["truncation"]; !ok {
		t.Error("missing truncation in passthrough")
	}
	if _, ok := pt["custom_field"]; !ok {
		t.Error("missing custom_field in passthrough")
	}
	if _, ok := pt["model"]; ok {
		t.Error("model should not be in passthrough")
	}
	if _, ok := pt["stream"]; ok {
		t.Error("stream should not be in passthrough")
	}
}

func TestStorePassthrough_AllKnown(t *testing.T) {
	rawJSON := []byte(`{"model": "test", "stream": true}`)

	known := map[string]bool{
		"model":  true,
		"stream": true,
	}

	pt := StorePassthrough(rawJSON, known)
	if pt != nil {
		t.Errorf("passthrough should be nil, got %v", pt)
	}
}

func TestStorePassthrough_InvalidJSON(t *testing.T) {
	pt := StorePassthrough([]byte(`not json`), nil)
	if pt != nil {
		t.Errorf("passthrough should be nil for invalid JSON, got %v", pt)
	}
}

func TestMergePassthrough_AddsFields(t *testing.T) {
	target := []byte(`{"model": "test"}`)
	pt := map[string]json.RawMessage{
		"context_management": json.RawMessage(`{"compaction":{"enabled":true}}`),
		"truncation":         json.RawMessage(`{"type":"auto"}`),
	}

	result := MergePassthrough(target, pt)

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if obj["model"] != "test" {
		t.Errorf("model = %v, want %q", obj["model"], "test")
	}
	if obj["context_management"] == nil {
		t.Error("missing context_management")
	}
	if obj["truncation"] == nil {
		t.Error("missing truncation")
	}
}

func TestMergePassthrough_DoesNotOverwrite(t *testing.T) {
	target := []byte(`{"model": "test"}`)
	pt := map[string]json.RawMessage{
		"model": json.RawMessage(`"overwritten"`),
	}

	result := MergePassthrough(target, pt)

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if obj["model"] != "test" {
		t.Errorf("model = %v, want %q (should not be overwritten)", obj["model"], "test")
	}
}

func TestMergePassthrough_EmptyPassthrough(t *testing.T) {
	target := []byte(`{"model": "test"}`)
	result := MergePassthrough(target, nil)

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if obj["model"] != "test" {
		t.Errorf("model = %v, want %q", obj["model"], "test")
	}
}

func TestKnownRequestFields(t *testing.T) {
	fields := KnownRequestFields()

	expected := []string{"model", "messages", "input", "tools", "stream", "thinking", "reasoning"}
	for _, f := range expected {
		if !fields[f] {
			t.Errorf("missing expected field %q", f)
		}
	}
}

func TestStorePassthrough_RoundTrip(t *testing.T) {
	rawJSON := []byte(`{
		"model": "test-model",
		"messages": [],
		"stream": true,
		"context_management": {"compaction": {"enabled": true}},
		"truncation": {"type": "auto"},
		"user": "user-123"
	}`)

	known := KnownRequestFields()
	pt := StorePassthrough(rawJSON, known)

	// Simulate translation: build a new target JSON
	target := []byte(`{"model":"test-model","messages":[]}`)

	// Merge passthrough back
	result := MergePassthrough(target, pt)

	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Original fields should be present
	if obj["model"] != "test-model" {
		t.Errorf("model = %v, want %q", obj["model"], "test-model")
	}

	// Passthrough fields should be restored
	if obj["context_management"] == nil {
		t.Error("missing context_management after merge")
	}
	if obj["truncation"] == nil {
		t.Error("missing truncation after merge")
	}
	if obj["user"] == nil {
		t.Error("missing user after merge")
	}
}
