package storage

import (
	"sync"
	"sync/atomic"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

// Ring keeps the most recent N entries in memory
type Ring struct {
	mu    sync.RWMutex
	buf   []*models.LogEntry
	next  int
	count int
}

// NewRing creates a ring holding up to size entries
func NewRing(size int) *Ring {
	if size <= 0 {
		size = 1000
	}
	return &Ring{buf: make([]*models.LogEntry, size)}
}

// Add stores an entry, evicting the oldest when full
func (r *Ring) Add(e *models.LogEntry) {
	r.mu.Lock()
	r.buf[r.next] = e
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
	r.mu.Unlock()
}

// Last returns up to n matching entries, oldest first
func (r *Ring) Last(n int, q *Query) []*models.LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*models.LogEntry
	for i := 0; i < r.count && len(out) < n; i++ {
		idx := (r.next - 1 - i + len(r.buf)) % len(r.buf)
		if e := r.buf[idx]; q == nil || q.Match(e) {
			out = append(out, e)
		}
	}
	// reverse to oldest-first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Subscription receives live entries matching a query
type Subscription struct {
	C       <-chan *models.LogEntry
	c       chan *models.LogEntry
	query   *Query
	dropped atomic.Int64
}

// Dropped returns how many entries were skipped because the subscriber was slow
func (s *Subscription) Dropped() int64 {
	return s.dropped.Load()
}

// Hub fans out live entries to subscribers. Slow subscribers lose entries
// instead of slowing down the pipeline.
type Hub struct {
	mu   sync.RWMutex
	subs map[*Subscription]struct{}
}

// NewHub creates an empty hub
func NewHub() *Hub {
	return &Hub{subs: make(map[*Subscription]struct{})}
}

// Subscribe registers a subscriber for entries matching q (nil = all)
func (h *Hub) Subscribe(q *Query, buffer int) *Subscription {
	if buffer <= 0 {
		buffer = 256
	}
	c := make(chan *models.LogEntry, buffer)
	s := &Subscription{C: c, c: c, query: q}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

// Unsubscribe removes a subscriber and closes its channel
func (h *Hub) Unsubscribe(s *Subscription) {
	h.mu.Lock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
		close(s.c)
	}
	h.mu.Unlock()
}

// Publish delivers e to every matching subscriber without blocking
func (h *Hub) Publish(e *models.LogEntry) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs {
		if s.query != nil && !s.query.Match(e) {
			continue
		}
		select {
		case s.c <- e:
		default:
			s.dropped.Add(1)
		}
	}
}

// Subscribers returns the number of active subscribers
func (h *Hub) Subscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
