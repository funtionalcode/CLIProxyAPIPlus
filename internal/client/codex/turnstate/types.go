package turnstate

import (
	"time"
)

// HeaderName is the canonical HTTP header name used by OpenAI for Codex turn state.
const HeaderName = "X-Codex-Turn-State"

// Ticket represents a high-compute turn-state ticket captured from OpenAI Codex.
type Ticket struct {
	State      string    `json:"state"`
	Length     int       `json:"length"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Proxy      string    `json:"proxy,omitempty"`
	AuthID     string    `json:"auth_id,omitempty"`
	Model      string    `json:"model,omitempty"`
}

// IsExpired returns true if the ticket has passed its expiration time.
func (t *Ticket) IsExpired() bool {
	if t == nil {
		return true
	}
	return time.Now().After(t.ExpiresAt)
}

// TicketSummary contains public summary details of a ticket.
type TicketSummary struct {
	Length     int       `json:"length"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Proxy      string    `json:"proxy,omitempty"`
	AuthID     string    `json:"auth_id,omitempty"`
}

// Stats provides real-time metrics and inspection status for the Turn-State subsystem.
type Stats struct {
	Enabled        bool            `json:"enabled"`
	ProberActive   bool            `json:"prober_active"`
	PoolSize       int             `json:"pool_size"`
	MinSpare       int             `json:"min_spare"`
	MaxPoolSize    int             `json:"max_pool_size"`
	MinLength      int             `json:"min_length"`
	ProxyCount     int             `json:"proxy_count"`
	TotalProbed    uint64          `json:"total_probed"`
	TotalSuccess   uint64          `json:"total_success"`
	TotalFailed    uint64          `json:"total_failed"`
	TotalInjected  uint64          `json:"total_injected"`
	TotalCollected uint64          `json:"total_collected"`
	RecentTickets  []TicketSummary `json:"recent_tickets,omitempty"`
}
