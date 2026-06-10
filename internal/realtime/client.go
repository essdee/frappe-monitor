package realtime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	// sendBuffer is how many events we queue per client before declaring
	// it too slow and dropping it. Generous enough to ride out a brief
	// stall, small enough that a dead client doesn't pin much memory.
	sendBuffer = 128
	// pingInterval keeps the connection alive through idle proxies and
	// detects half-open connections.
	pingInterval = 30 * time.Second
	// writeTimeout bounds a single frame write / ping so one wedged
	// client can't block its own write pump forever.
	writeTimeout = 10 * time.Second
)

// Client is one connected dashboard WebSocket. It owns a read pump
// (handling subscribe/unsubscribe control frames) and a write pump
// (draining the send queue and pinging for keepalive).
type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	logger *slog.Logger

	send   chan Event
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once

	mu   sync.RWMutex
	subs map[string]bool
}

// wants reports whether this client should receive an event on topic.
// An empty topic is a broadcast to everyone.
func (c *Client) wants(topic string) bool {
	if topic == "" {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.subs[topic]
}

// topicSet returns a snapshot of the client's subscribed topics. Used by
// the hub on remove() to release the per-topic subscriber counts.
func (c *Client) topicSet() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.subs))
	for t := range c.subs {
		out = append(out, t)
	}
	return out
}

// enqueue does a non-blocking send onto the client's queue. If the queue
// is full (slow/stuck client) the client is closed rather than blocking
// the broadcasting producer.
func (c *Client) enqueue(ev Event) {
	select {
	case c.send <- ev:
	case <-c.done:
	default:
		// Slow/stuck client: tear it down WITHOUT blocking the broadcast.
		// close() cancels the read ctx (which closes the socket promptly),
		// but we run it off the hub lock the caller holds so one slow tab
		// can never stall a producer even momentarily.
		c.logger.Warn("realtime: client send queue full, dropping client")
		go c.close()
	}
}

// close tears the client down exactly once: signals the pumps to stop
// and closes the underlying connection (which unblocks an in-flight
// Read). The send channel is intentionally never closed — enqueue and
// the write pump select on `done` instead, so there's no send-on-closed
// race.
func (c *Client) close() {
	c.once.Do(func() {
		close(c.done)
		if c.cancel != nil {
			c.cancel()
		}
		_ = c.conn.Close(websocket.StatusNormalClosure, "")
	})
}

// readPump processes client→server control frames until the connection
// closes or ctx is canceled. Returning ends the connection.
func (c *Client) readPump(ctx context.Context) {
	defer c.close()
	for {
		var msg ClientMessage
		if err := wsjson.Read(ctx, c.conn, &msg); err != nil {
			return // normal close, ctx cancel, or protocol error
		}
		c.handle(msg)
	}
}

// handle applies a subscribe/unsubscribe control frame and keeps the
// hub's per-topic subscriber counts in sync.
func (c *Client) handle(msg ClientMessage) {
	var added, removed []string
	c.mu.Lock()
	switch msg.Action {
	case ActionSubscribe:
		for _, t := range msg.Topics {
			if t != "" && !c.subs[t] {
				c.subs[t] = true
				added = append(added, t)
			}
		}
	case ActionUnsubscribe:
		for _, t := range msg.Topics {
			if c.subs[t] {
				delete(c.subs, t)
				removed = append(removed, t)
			}
		}
	default:
		c.logger.Debug("realtime: unknown client action", "action", msg.Action)
	}
	c.mu.Unlock()
	// Update hub counts outside c.mu — incSub/decSub take the hub lock.
	for _, t := range added {
		c.hub.incSub(t)
	}
	for _, t := range removed {
		c.hub.decSub(t)
	}
}

// writePump drains the send queue to the socket and pings periodically.
// Any write/ping error closes the client.
func (c *Client) writePump(ctx context.Context) {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case ev := <-c.send:
			if err := c.writeEvent(ctx, ev); err != nil {
				c.close()
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.conn.Ping(pctx)
			cancel()
			if err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *Client) writeEvent(ctx context.Context, ev Event) error {
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return wsjson.Write(wctx, c.conn, ev)
}
