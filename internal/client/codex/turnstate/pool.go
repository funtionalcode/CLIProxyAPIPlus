package turnstate

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Pool manages a thread-safe collection of unexpired, high-compute turn-state tickets.
type Pool struct {
	mu             sync.RWMutex
	tickets        []*Ticket
	maxSize        int
	ttl            time.Duration
	minLength      int
	currentIndex   atomic.Uint64
	totalInjected  atomic.Uint64
	totalCollected atomic.Uint64
	totalExpired   atomic.Uint64
}

// NewPool creates a new Pool instance with the given configuration limits.
func NewPool(maxSize, minLength int, ttl time.Duration) *Pool {
	if maxSize <= 0 {
		maxSize = 50
	}
	if minLength <= 0 {
		minLength = 160
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &Pool{
		maxSize:   maxSize,
		minLength: minLength,
		ttl:       ttl,
	}
}

// UpdateLimits dynamically updates the pool limits.
func (p *Pool) UpdateLimits(maxSize, minLength int, ttl time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if maxSize > 0 {
		p.maxSize = maxSize
	}
	if minLength > 0 {
		p.minLength = minLength
	}
	if ttl > 0 {
		p.ttl = ttl
	}
}

// Push adds a new ticket to the pool after verifying its validity and minimum length.
func (p *Pool) Push(ticket *Ticket) bool {
	if ticket == nil {
		return false
	}
	ticket.State = strings.TrimSpace(ticket.State)
	ticket.Length = len(ticket.State)

	p.mu.Lock()
	defer p.mu.Unlock()

	if ticket.Length < p.minLength || ticket.IsExpired() {
		return false
	}

	// Deduplicate: if state already exists, update expiration if newer
	for _, existing := range p.tickets {
		if existing.State == ticket.State {
			if ticket.ExpiresAt.After(existing.ExpiresAt) {
				existing.ExpiresAt = ticket.ExpiresAt
			}
			return true
		}
	}

	// Prune expired tickets while holding lock
	p.pruneLocked(time.Now())

	// If at max capacity, remove oldest ticket
	if len(p.tickets) >= p.maxSize {
		p.tickets = p.tickets[1:]
	}

	p.tickets = append(p.tickets, ticket)
	p.totalCollected.Add(1)
	return true
}

// Feed wraps Push for raw state strings, setting default TTL and length checks.
func (p *Pool) Feed(state string, proxy string, authID string, model string) bool {
	state = strings.TrimSpace(state)
	p.mu.RLock()
	minLength := p.minLength
	ttl := p.ttl
	p.mu.RUnlock()

	if len(state) < minLength {
		return false
	}

	now := time.Now()
	ticket := &Ticket{
		State:      state,
		Length:     len(state),
		AcquiredAt: now,
		ExpiresAt:  now.Add(ttl),
		Proxy:      proxy,
		AuthID:     authID,
		Model:      model,
	}
	return p.Push(ticket)
}

// GetTicket retrieves a valid, non-expired ticket for request injection.
// It rotates amongst currently valid tickets so they can be reused across multiple requests.
func (p *Pool) GetTicket() (string, bool) {
	ticket, ok := p.ticketForInjection()
	return ticket.State, ok
}

func (p *Pool) ticketForInjection() (Ticket, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.pruneLocked(time.Now())
	n := len(p.tickets)
	if n == 0 {
		return Ticket{}, false
	}

	idx := p.currentIndex.Add(1) - 1
	ticket := p.tickets[idx%uint64(n)]
	p.totalInjected.Add(1)
	return *ticket, true
}

// Pop extracts and removes the oldest valid ticket from the pool.
func (p *Pool) Pop() (*Ticket, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.pruneLocked(time.Now())
	if len(p.tickets) == 0 {
		return nil, false
	}

	ticket := p.tickets[0]
	p.tickets = p.tickets[1:]
	p.totalInjected.Add(1)
	return ticket, true
}

// Len returns the current count of unexpired tickets in the pool.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.pruneLocked(time.Now())
	return len(p.tickets)
}

// PruneExpired removes all expired tickets.
func (p *Pool) PruneExpired() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.pruneLocked(time.Now())
}

func (p *Pool) pruneLocked(now time.Time) int {
	var valid []*Ticket
	expiredCount := 0
	for _, t := range p.tickets {
		if now.Before(t.ExpiresAt) {
			valid = append(valid, t)
		} else {
			expiredCount++
		}
	}
	p.tickets = valid
	if expiredCount > 0 {
		p.totalExpired.Add(uint64(expiredCount))
	}
	return expiredCount
}

// RecentSummaries returns a snapshot of recent tickets for inspection.
func (p *Pool) RecentSummaries(limit int) []TicketSummary {
	p.mu.RLock()
	defer p.mu.RUnlock()

	n := len(p.tickets)
	if n == 0 {
		return nil
	}
	if limit <= 0 || limit > n {
		limit = n
	}

	out := make([]TicketSummary, limit)
	// Return the most recent tickets first
	for i := 0; i < limit; i++ {
		t := p.tickets[n-1-i]
		out[i] = TicketSummary{
			Length:       t.Length,
			AcquiredAt:   t.AcquiredAt,
			ExpiresAt:    t.ExpiresAt,
			GatewayPool:  t.GatewayPool,
			GatewayProxy: t.GatewayProxy,
			AuthID:       t.AuthID,
		}
	}
	return out
}

// Metrics returns atomic counters for total injected, collected, and expired tickets.
func (p *Pool) Metrics() (injected, collected, expired uint64) {
	return p.totalInjected.Load(), p.totalCollected.Load(), p.totalExpired.Load()
}
