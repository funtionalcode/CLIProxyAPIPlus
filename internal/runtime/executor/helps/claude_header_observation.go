package helps

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const claudeHeaderObservationLimit = 20

type ClaudeHeaderObservation struct {
	FirstSeen      time.Time `json:"first_seen"`
	LastSeen       time.Time `json:"last_seen"`
	Count          int64     `json:"count"`
	UserAgent      string    `json:"user_agent,omitempty"`
	PackageVersion string    `json:"package_version,omitempty"`
	RuntimeVersion string    `json:"runtime_version,omitempty"`
	OS             string    `json:"os,omitempty"`
	Arch           string    `json:"arch,omitempty"`
	Timeout        string    `json:"timeout,omitempty"`
}

var claudeHeaderObservations = struct {
	sync.Mutex
	items map[string]ClaudeHeaderObservation
}{items: make(map[string]ClaudeHeaderObservation)}

func ObserveClaudeClientHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	observation := ClaudeHeaderObservation{
		UserAgent:      strings.TrimSpace(headers.Get("User-Agent")),
		PackageVersion: strings.TrimSpace(headers.Get("X-Stainless-Package-Version")),
		RuntimeVersion: strings.TrimSpace(headers.Get("X-Stainless-Runtime-Version")),
		OS:             strings.TrimSpace(headers.Get("X-Stainless-Os")),
		Arch:           strings.TrimSpace(headers.Get("X-Stainless-Arch")),
		Timeout:        strings.TrimSpace(headers.Get("X-Stainless-Timeout")),
	}
	if observation.UserAgent == "" && observation.PackageVersion == "" && observation.RuntimeVersion == "" && observation.OS == "" && observation.Arch == "" && observation.Timeout == "" {
		return
	}

	key := strings.Join([]string{
		observation.UserAgent,
		observation.PackageVersion,
		observation.RuntimeVersion,
		observation.OS,
		observation.Arch,
		observation.Timeout,
	}, "\x00")
	now := time.Now()

	claudeHeaderObservations.Lock()
	defer claudeHeaderObservations.Unlock()

	current := claudeHeaderObservations.items[key]
	if current.Count == 0 {
		observation.FirstSeen = now
		observation.LastSeen = now
		observation.Count = 1
		claudeHeaderObservations.items[key] = observation
		trimClaudeHeaderObservationsLocked()
		return
	}
	current.LastSeen = now
	current.Count++
	claudeHeaderObservations.items[key] = current
}

func RecentClaudeHeaderObservations() []ClaudeHeaderObservation {
	claudeHeaderObservations.Lock()
	defer claudeHeaderObservations.Unlock()

	out := make([]ClaudeHeaderObservation, 0, len(claudeHeaderObservations.items))
	for _, item := range claudeHeaderObservations.items {
		out = append(out, item)
	}
	sortClaudeHeaderObservations(out)
	return out
}

func trimClaudeHeaderObservationsLocked() {
	if len(claudeHeaderObservations.items) <= claudeHeaderObservationLimit {
		return
	}
	items := make([]ClaudeHeaderObservation, 0, len(claudeHeaderObservations.items))
	for _, item := range claudeHeaderObservations.items {
		items = append(items, item)
	}
	sortClaudeHeaderObservations(items)
	keep := make(map[string]struct{}, claudeHeaderObservationLimit)
	for i, item := range items {
		if i >= claudeHeaderObservationLimit {
			break
		}
		key := strings.Join([]string{item.UserAgent, item.PackageVersion, item.RuntimeVersion, item.OS, item.Arch, item.Timeout}, "\x00")
		keep[key] = struct{}{}
	}
	for key := range claudeHeaderObservations.items {
		if _, ok := keep[key]; !ok {
			delete(claudeHeaderObservations.items, key)
		}
	}
}

func sortClaudeHeaderObservations(items []ClaudeHeaderObservation) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].LastSeen.Equal(items[j].LastSeen) {
			return items[i].UserAgent < items[j].UserAgent
		}
		return items[i].LastSeen.After(items[j].LastSeen)
	})
}

func ClaudeHeaderPassthroughPolicy() string {
	return "Claude headers: pass through per incoming Claude CLI request; no CPA-managed Claude Header Defaults are applied"
}

func CodexHeaderPolicy() string {
	return "Codex headers: pass through client headers when present; otherwise use built-in Codex Desktop/codex-proxy-compatible fallback headers"
}
