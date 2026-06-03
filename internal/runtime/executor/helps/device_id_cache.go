package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/google/uuid"
)

type deviceIDCacheEntry struct {
	value  string
	expire time.Time
}

var (
	deviceIDCache            = make(map[string]deviceIDCacheEntry)
	deviceIDCacheMu          sync.RWMutex
	deviceIDCacheCleanupOnce sync.Once
)

const (
	deviceIDTTL                = time.Hour
	deviceIDCacheCleanupPeriod = 15 * time.Minute
)

func startDeviceIDCacheCleanup() {
	go func() {
		ticker := time.NewTicker(deviceIDCacheCleanupPeriod)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredDeviceIDs()
		}
	}()
}

func purgeExpiredDeviceIDs() {
	now := time.Now()
	deviceIDCacheMu.Lock()
	for key, entry := range deviceIDCache {
		if !entry.expire.After(now) {
			delete(deviceIDCache, key)
		}
	}
	deviceIDCacheMu.Unlock()
}

func deviceIDCacheKey(scope string) string {
	sum := sha256.Sum256([]byte(scope))
	return hex.EncodeToString(sum[:])
}

// CachedDeviceID returns a stable device UUID for the provided scope,
// refreshing the TTL on each access.
func CachedDeviceID(scope string) string {
	if scope == "" {
		return uuid.New().String()
	}

	deviceIDCacheCleanupOnce.Do(startDeviceIDCacheCleanup)

	key := deviceIDCacheKey(scope)
	now := time.Now()

	deviceIDCacheMu.RLock()
	entry, ok := deviceIDCache[key]
	valid := ok && entry.value != "" && entry.expire.After(now)
	deviceIDCacheMu.RUnlock()
	if valid {
		deviceIDCacheMu.Lock()
		entry = deviceIDCache[key]
		if entry.value != "" && entry.expire.After(now) {
			entry.expire = now.Add(deviceIDTTL)
			deviceIDCache[key] = entry
			deviceIDCacheMu.Unlock()
			return entry.value
		}
		deviceIDCacheMu.Unlock()
	}

	newID := uuid.New().String()

	deviceIDCacheMu.Lock()
	entry, ok = deviceIDCache[key]
	if !ok || entry.value == "" || !entry.expire.After(now) {
		entry.value = newID
	}
	entry.expire = now.Add(deviceIDTTL)
	deviceIDCache[key] = entry
	deviceIDCacheMu.Unlock()
	return entry.value
}
