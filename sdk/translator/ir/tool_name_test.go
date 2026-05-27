package ir

import "testing"

func TestShortenToolName_NoShortening(t *testing.T) {
	short, original := ShortenToolName("search")
	if short != "search" {
		t.Errorf("short = %q, want %q", short, "search")
	}
	if original != "" {
		t.Errorf("original = %q, want empty", original)
	}
}

func TestShortenToolName_ExactLimit(t *testing.T) {
	// 64 characters exactly
	name := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if len(name) != 64 {
		t.Fatalf("test setup: name len = %d, want 64", len(name))
	}
	short, original := ShortenToolName(name)
	if short != name {
		t.Errorf("short = %q, want %q", short, name)
	}
	if original != "" {
		t.Errorf("original = %q, want empty", original)
	}
}

func TestShortenToolName_PlainTruncation(t *testing.T) {
	// 65 characters - should truncate to 64
	name := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if len(name) != 65 {
		t.Fatalf("test setup: name len = %d, want 65", len(name))
	}
	short, original := ShortenToolName(name)
	if len(short) != 64 {
		t.Errorf("short len = %d, want 64", len(short))
	}
	if short != name[:64] {
		t.Errorf("short = %q, want %q", short, name[:64])
	}
	if original != name {
		t.Errorf("original = %q, want %q", original, name)
	}
}

func TestShortenToolName_MCPPrefix(t *testing.T) {
	name := "mcp__server_name__tool_name_that_is_very_long_and_exceeds_the_limit_significantly"
	short, original := ShortenToolName(name)

	if len(short) > 64 {
		t.Errorf("short len = %d, want <= 64", len(short))
	}
	// Should preserve mcp__ prefix
	if short[:5] != "mcp__" {
		t.Errorf("short prefix = %q, want %q", short[:5], "mcp__")
	}
	// Should preserve last segment
	if original != name {
		t.Errorf("original = %q, want %q", original, name)
	}
}

func TestShortenToolName_MCPPrefix_ShortEnough(t *testing.T) {
	name := "mcp__server__tool"
	short, original := ShortenToolName(name)
	if short != name {
		t.Errorf("short = %q, want %q", short, name)
	}
	if original != "" {
		t.Errorf("original = %q, want empty", original)
	}
}

func TestBuildShortNameMap_NoCollisions(t *testing.T) {
	names := []string{"search", "lookup", "get_weather"}
	m := BuildShortNameMap(names)

	if len(m) != 3 {
		t.Fatalf("map len = %d, want 3", len(m))
	}
	for _, name := range names {
		if _, ok := m[name]; !ok {
			t.Errorf("missing key %q in map", name)
		}
	}
}

func TestBuildShortNameMap_CollisionResolution(t *testing.T) {
	// Two names that would produce the same shortened form
	name1 := "mcp__server_a__very_long_tool_name_that_exceeds_limit_significantly_here"
	name2 := "mcp__server_b__very_long_tool_name_that_exceeds_limit_significantly_here"

	m := BuildShortNameMap([]string{name1, name2})

	if len(m) != 2 {
		t.Fatalf("map len = %d, want 2", len(m))
	}

	// Both should be present with different keys
	found := 0
	for _, orig := range m {
		if orig == name1 || orig == name2 {
			found++
		}
	}
	if found != 2 {
		t.Errorf("found %d unique originals, want 2; map: %v", found, m)
	}
}

func TestBuildShortNameMap_LongNames(t *testing.T) {
	name1 := "a_very_long_tool_name_that_definitely_exceeds_the_sixty_four_character_limit_by_a_lot"
	name2 := "another_very_long_tool_name_that_definitely_exceeds_the_sixty_four_character_limit"

	m := BuildShortNameMap([]string{name1, name2})

	if len(m) != 2 {
		t.Fatalf("map len = %d, want 2", len(m))
	}

	for short := range m {
		if len(short) > 64 {
			t.Errorf("shortened name %q has len %d, want <= 64", short, len(short))
		}
	}
}

func TestRestoreToolName_Found(t *testing.T) {
	m := map[string]string{"search": "full_search_tool_name"}
	result := RestoreToolName("search", m)
	if result != "full_search_tool_name" {
		t.Errorf("result = %q, want %q", result, "full_search_tool_name")
	}
}

func TestRestoreToolName_NotFound(t *testing.T) {
	m := map[string]string{"search": "full_search_tool_name"}
	result := RestoreToolName("unknown", m)
	if result != "unknown" {
		t.Errorf("result = %q, want %q", result, "unknown")
	}
}

func TestRestoreToolName_NilMap(t *testing.T) {
	result := RestoreToolName("search", nil)
	if result != "search" {
		t.Errorf("result = %q, want %q", result, "search")
	}
}
