package realtime

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// ServeWS upgrades an HTTP request to a WebSocket and runs the client
// until it disconnects. It must be mounted behind the auth gate: the
// browser sends the session cookie with the upgrade request and the
// middleware validates it before this handler runs, so only an
// authenticated dashboard can open a socket.
//
// Origin: coder/websocket's default same-origin check is used (the SPA
// is served from the same host as this endpoint). The dev Vite proxy
// preserves the Host header so same-origin holds there too. Cross-site
// connections are additionally blocked because the session cookie is
// SameSite=Strict and won't be sent on a cross-site upgrade.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		h.logger.Warn("realtime: websocket accept failed", "err", err)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	c := &Client{
		hub:    h,
		conn:   conn,
		logger: h.logger,
		send:   make(chan Event, sendBuffer),
		done:   make(chan struct{}),
		cancel: cancel,
		subs:   map[string]bool{},
	}

	if !h.add(c) {
		// Hub is shutting down — reject politely.
		_ = conn.Close(websocket.StatusGoingAway, "server shutting down")
		cancel()
		return
	}
	defer h.remove(c)
	defer c.close()

	// Greet the client so it can confirm the socket is live and sync the
	// server clock. This goes straight to the queue (picked up by the
	// write pump once it starts).
	c.enqueue(Event{Type: TypeHello, TS: time.Now().UnixMilli()})

	go c.writePump(ctx)
	c.readPump(ctx) // blocks until the client disconnects or ctx cancels
}
