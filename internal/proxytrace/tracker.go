package proxytrace

import (
	"sync"
	"time"
)

// Route describes one proxy-gateway routing decision without credentials.
type Route struct {
	Pool       string
	Proxy      string
	Target     string
	SelectedAt time.Time
}

var routes = struct {
	sync.Mutex
	values map[string]Route
}{values: make(map[string]Route)}

// Record stores a short-lived routing decision for correlation with its caller.
func Record(traceID string, route Route) {
	if traceID == "" {
		return
	}
	now := time.Now()
	route.SelectedAt = now
	routes.Lock()
	for id, existing := range routes.values {
		if now.Sub(existing.SelectedAt) > 5*time.Minute {
			delete(routes.values, id)
		}
	}
	if len(routes.values) >= 1024 {
		var oldestID string
		var oldest time.Time
		for id, existing := range routes.values {
			if oldestID == "" || existing.SelectedAt.Before(oldest) {
				oldestID, oldest = id, existing.SelectedAt
			}
		}
		delete(routes.values, oldestID)
	}
	routes.values[traceID] = route
	routes.Unlock()
}

// Take returns and removes one correlated routing decision.
func Take(traceID string) (Route, bool) {
	routes.Lock()
	defer routes.Unlock()
	route, ok := routes.values[traceID]
	delete(routes.values, traceID)
	return route, ok
}
