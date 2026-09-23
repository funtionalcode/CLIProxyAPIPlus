package proxygateway

// GatewayStats provides real-time status and telemetry for the forward proxy gateway.
type GatewayStats struct {
	Enabled           bool          `json:"enabled"`
	Bind              string        `json:"bind"`
	Port              int           `json:"port"`
	ListenAddress     string        `json:"listen_address"`
	TotalRequests     uint64        `json:"total_requests"`
	ActiveConnections int64         `json:"active_connections"`
	TotalErrors       uint64        `json:"total_errors"`
	TotalProxies      int           `json:"total_proxies"`
	Pools             []PoolSummary `json:"pools"`
}

// PoolSummary represents the status of an individual proxy pool within the gateway.
type PoolSummary struct {
	Name           string   `json:"name"`
	Enabled        bool     `json:"enabled"`
	Weight         int      `json:"weight,omitempty"`
	Proxies        []string `json:"proxies"`
	ProxyURLSource string   `json:"proxy-url-source,omitempty"`
	ProxyCount     int      `json:"proxy_count"`
}

// TestResult represents the result of testing an upstream proxy's connectivity.
type TestResult struct {
	ProxyURL         string   `json:"proxy_url"`
	Proxies          []string `json:"proxies,omitempty"`
	Success          bool     `json:"success"`
	LatencyMs        int64    `json:"latency_ms"`
	Error            string   `json:"error,omitempty"`
	Stage            string   `json:"stage,omitempty"`
	SourceProxyCount int      `json:"source_proxy_count,omitempty"`
	SourceLatencyMs  int64    `json:"source_latency_ms,omitempty"`
}
