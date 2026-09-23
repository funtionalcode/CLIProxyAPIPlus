package turnstate

import (
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Event stores probe and injection summaries without tokens, state values, or proxy credentials.
type Event struct {
	Time         time.Time `json:"time"`
	Kind         string    `json:"kind"`
	Outcome      string    `json:"outcome"`
	Source       string    `json:"source,omitempty"`
	AuthID       string    `json:"auth_id,omitempty"`
	Model        string    `json:"model,omitempty"`
	Length       int       `json:"length,omitempty"`
	LatencyMS    int64     `json:"latency_ms,omitempty"`
	GatewayPool  string    `json:"gateway_pool,omitempty"`
	GatewayProxy string    `json:"gateway_proxy,omitempty"`
	Message      string    `json:"message"`
}

type eventLog struct {
	mu      sync.Mutex
	recent  []Event
	skipped uint64
}

func (e *eventLog) add(event Event) {
	event.Time = time.Now()
	e.mu.Lock()
	if event.Kind == "injection" && event.Outcome == "skipped" {
		e.skipped++
	}
	if len(e.recent) >= 100 {
		copy(e.recent, e.recent[1:])
		e.recent = e.recent[:99]
	}
	e.recent = append(e.recent, event)
	e.mu.Unlock()
	log.WithFields(log.Fields{
		"kind": event.Kind, "outcome": event.Outcome, "source": event.Source,
		"auth_id": event.AuthID, "model": event.Model, "length": event.Length,
		"latency_ms": event.LatencyMS, "gateway_pool": event.GatewayPool,
		"gateway_proxy": event.GatewayProxy,
	}).Info("turn-state: " + event.Message)
}

func (e *eventLog) snapshot() ([]Event, uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]Event, len(e.recent))
	for i := range e.recent {
		result[i] = e.recent[len(e.recent)-1-i]
	}
	return result, e.skipped
}
