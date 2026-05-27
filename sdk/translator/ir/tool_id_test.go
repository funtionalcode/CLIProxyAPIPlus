package ir

import "testing"

func TestNormalizeToolCallID_NoShortening(t *testing.T) {
	idMap := map[string]string{}
	result := NormalizeToolCallID("call_123", idMap)
	if result != "call_123" {
		t.Errorf("result = %q, want %q", result, "call_123")
	}
	if len(idMap) != 0 {
		t.Errorf("idMap should be empty for short IDs")
	}
}

func TestNormalizeToolCallID_ExactLimit(t *testing.T) {
	// 64 characters exactly
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if len(id) != 64 {
		t.Fatalf("test setup: id len = %d, want 64", len(id))
	}
	idMap := map[string]string{}
	result := NormalizeToolCallID(id, idMap)
	if result != id {
		t.Errorf("result = %q, want %q", result, id)
	}
	if len(idMap) != 0 {
		t.Errorf("idMap should be empty for IDs at limit")
	}
}

func TestNormalizeToolCallID_Shortening(t *testing.T) {
	// 65 characters - should be shortened
	id := "toolu_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if len(id) != 65 {
		t.Fatalf("test setup: id len = %d, want 65", len(id))
	}
	idMap := map[string]string{}
	result := NormalizeToolCallID(id, idMap)

	if len(result) > 64 {
		t.Errorf("result len = %d, want <= 64", len(result))
	}
	if result == id {
		t.Error("result should differ from original")
	}
	// Should preserve prefix
	if result[:32] != id[:32] {
		t.Errorf("prefix = %q, want %q", result[:32], id[:32])
	}
	// Should preserve suffix
	if result[len(result)-16:] != id[len(id)-16:] {
		t.Errorf("suffix = %q, want %q", result[len(result)-16:], id[len(id)-16:])
	}
	// Should store in map
	if idMap[result] != id {
		t.Errorf("idMap[%q] = %q, want %q", result, idMap[result], id)
	}
}

func TestNormalizeToolCallID_VeryLongID(t *testing.T) {
	id := "toolu_01ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_extra_suffix_here"
	idMap := map[string]string{}
	result := NormalizeToolCallID(id, idMap)

	if len(result) > 64 {
		t.Errorf("result len = %d, want <= 64", len(result))
	}
	if idMap[result] != id {
		t.Errorf("idMap[%q] = %q, want %q", result, idMap[result], id)
	}
}

func TestNormalizeToolCallID_NilMap(t *testing.T) {
	id := "toolu_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	result := NormalizeToolCallID(id, nil)

	if len(result) > 64 {
		t.Errorf("result len = %d, want <= 64", len(result))
	}
}

func TestNormalizeToolCallID_Deterministic(t *testing.T) {
	id := "toolu_01ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_extra"
	idMap1 := map[string]string{}
	idMap2 := map[string]string{}

	result1 := NormalizeToolCallID(id, idMap1)
	result2 := NormalizeToolCallID(id, idMap2)

	if result1 != result2 {
		t.Errorf("results differ: %q vs %q", result1, result2)
	}
}

func TestRestoreToolCallID_Found(t *testing.T) {
	idMap := map[string]string{
		"short_id": "original_very_long_id_here",
	}
	result := RestoreToolCallID("short_id", idMap)
	if result != "original_very_long_id_here" {
		t.Errorf("result = %q, want %q", result, "original_very_long_id_here")
	}
}

func TestRestoreToolCallID_NotFound(t *testing.T) {
	idMap := map[string]string{
		"short_id": "original_very_long_id_here",
	}
	result := RestoreToolCallID("unknown_id", idMap)
	if result != "unknown_id" {
		t.Errorf("result = %q, want %q", result, "unknown_id")
	}
}

func TestRestoreToolCallID_NilMap(t *testing.T) {
	result := RestoreToolCallID("short_id", nil)
	if result != "short_id" {
		t.Errorf("result = %q, want %q", result, "short_id")
	}
}

func TestNormalizeAndRestore_RoundTrip(t *testing.T) {
	ids := []string{
		"short",
		"toolu_01ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789",
		"a_very_very_very_very_very_very_very_very_very_very_very_very_long_id",
	}

	idMap := map[string]string{}
	normalized := make([]string, len(ids))

	for i, id := range ids {
		normalized[i] = NormalizeToolCallID(id, idMap)
	}

	for i, id := range ids {
		restored := RestoreToolCallID(normalized[i], idMap)
		if restored != id {
			t.Errorf("round-trip failed for %q: got %q", id, restored)
		}
	}
}
