package helps

import "testing"

func resetDeviceIDCache() {
	deviceIDCacheMu.Lock()
	deviceIDCache = make(map[string]deviceIDCacheEntry)
	deviceIDCacheMu.Unlock()
}

func TestCachedDeviceID_ReusesWithinScope(t *testing.T) {
	resetDeviceIDCache()

	first := CachedDeviceID("scope-1")
	second := CachedDeviceID("scope-1")

	if first == "" {
		t.Fatal("expected generated device_id to be non-empty")
	}
	if first != second {
		t.Fatalf("expected cached device_id to be reused, got %q and %q", first, second)
	}
}

func TestCachedDeviceID_IsScoped(t *testing.T) {
	resetDeviceIDCache()

	first := CachedDeviceID("scope-1")
	second := CachedDeviceID("scope-2")

	if first == second {
		t.Fatalf("expected different scopes to have different device_id values, got %q", first)
	}
}
