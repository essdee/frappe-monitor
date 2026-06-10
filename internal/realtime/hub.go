package realtime

import (
	"log/slog"
	"sync"
	"time"
)

// Hub is the central registry of connected WebSocket clients. It is
// safe for concurrent use: producers Broadcast from many goroutines
// while clients register/unregister from their own.
//
// Design: a mutex-guarded client set (no central run-loop). Each client
// owns a buffered send queue; Broadcast does a non-blocking enqueue and
// drops a client that can't keep up, so one slow browser can never wedge
// a producer. Our scale (a handful of dashboard tabs) makes the simple
// lock-per-broadcast approach more than fast enough.
type Hub struct {
	logger  *slog.Logger
	mu      sync.RWMutex
	clients map[*Client]struct{}
	closed  bool
}

// NewHub constructs an empty Hub.
func NewHub(logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{logger: logger, clients: map[*Client]struct{}{}}
}

// Broadcast fans an event out to every client subscribed to ev.Topic
// (or to all clients when ev.Topic is empty). It never blocks on a slow
// client — that client is dropped instead. TS is stamped if unset.
func (h *Hub) Broadcast(ev Event) {
	// nil-safe: a nil *Hub stored in a Broadcaster interface (e.g. an
	// unconfigured Deps.Hub in tests) is a no-op rather than a panic.
	if h == nil {
		return
	}
	if ev.TS == 0 {
		ev.TS = time.Now().UnixMilli()
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.wants(ev.Topic) {
			c.enqueue(ev)
		}
	}
}

// add registers a client. Returns false if the hub is already closed
// (the caller should reject the connection).
func (h *Hub) add(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	h.clients[c] = struct{}{}
	return true
}

// remove unregisters a client. Idempotent.
func (h *Hub) remove(c *Client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// ClientCount returns the number of currently connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Close disconnects every client and marks the hub closed so no new
// connections are accepted. Called during graceful shutdown, before the
// HTTP server stops, so the long-lived WS handlers return promptly.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.clients = map[*Client]struct{}{}
	h.mu.Unlock()

	for _, c := range clients {
		c.close()
	}
}
