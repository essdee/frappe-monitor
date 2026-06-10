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
// HubConfig tunes per-connection behavior. Zero values get sane defaults,
// so NewHub(logger, HubConfig{}) is valid (used by tests).
type HubConfig struct {
	SendBuffer   int           // per-client queue depth (default 128)
	PingInterval time.Duration // keepalive cadence (default 30s)
	WriteTimeout time.Duration // per-frame write / ping bound (default 10s)
	MaxClients   int           // total concurrent connection cap; 0 = unlimited
}

func (c HubConfig) withDefaults() HubConfig {
	if c.SendBuffer <= 0 {
		c.SendBuffer = 128
	}
	if c.PingInterval <= 0 {
		c.PingInterval = 30 * time.Second
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 10 * time.Second
	}
	return c
}

type Hub struct {
	logger  *slog.Logger
	cfg     HubConfig
	mu      sync.RWMutex
	clients map[*Client]struct{}
	// topicSubs is a per-topic subscriber count so HasSubscribers is O(1)
	// and a hot producer can skip work for a topic nobody is watching.
	topicSubs map[string]int
	closed    bool
}

// NewHub constructs an empty Hub with the given connection tuning.
func NewHub(logger *slog.Logger, cfg HubConfig) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		logger:    logger,
		cfg:       cfg.withDefaults(),
		clients:   map[*Client]struct{}{},
		topicSubs: map[string]int{},
	}
}

// HasSubscribers reports whether any client is currently subscribed to
// the topic. Lets producers skip building an event nobody will receive.
func (h *Hub) HasSubscribers(topic string) bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.topicSubs[topic] > 0
}

// incSub / decSub maintain the per-topic subscriber count. Called by a
// client as its subscription set changes (and by remove on disconnect).
func (h *Hub) incSub(topic string) {
	h.mu.Lock()
	h.topicSubs[topic]++
	h.mu.Unlock()
}

func (h *Hub) decSub(topic string) {
	h.mu.Lock()
	if h.topicSubs[topic] <= 1 {
		delete(h.topicSubs, topic)
	} else {
		h.topicSubs[topic]--
	}
	h.mu.Unlock()
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

// remove unregisters a client and releases its topic-subscriber counts.
// Idempotent.
func (h *Hub) remove(c *Client) {
	subs := c.topicSet()
	h.mu.Lock()
	delete(h.clients, c)
	for _, t := range subs {
		if h.topicSubs[t] <= 1 {
			delete(h.topicSubs, t)
		} else {
			h.topicSubs[t]--
		}
	}
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
	h.topicSubs = map[string]int{}
	h.mu.Unlock()

	// Close concurrently so a few half-open sockets can't serialize into a
	// slow shutdown. Each close() cancels the read ctx first, which makes
	// the underlying conn close return promptly.
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			c.close()
		}(c)
	}
	wg.Wait()
}
