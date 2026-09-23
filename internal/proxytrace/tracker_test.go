package proxytrace

import "testing"

func TestRecordAndTakeRoute(t *testing.T) {
	Record("trace-1", Route{Pool: "pool-a", Proxy: "socks5://1.2.3.4:1080", Target: "chatgpt.com:443"})
	route, ok := Take("trace-1")
	if !ok || route.Pool != "pool-a" || route.Proxy != "socks5://1.2.3.4:1080" || route.Target != "chatgpt.com:443" {
		t.Fatalf("unexpected route: %#v, ok=%v", route, ok)
	}
	if _, exists := Take("trace-1"); exists {
		t.Fatal("route was not removed after Take")
	}
}
