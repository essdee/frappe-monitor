// Package realtime is the monitor-side WebSocket push layer. A single
// Hub holds every connected dashboard client; producers (the collector
// pull pipeline, the alerts evaluator, the streamer sink, server CRUD
// handlers) call Hub.Broadcast to fan an Event out to the clients that
// subscribed to its topic. This replaces the dashboard's old
// setInterval polling — there is no long-polling and no repeated REST
// calls; the browser opens one WebSocket and receives pushes.
//
// Files:
//   - event.go   — the Event envelope, type constants, topic helpers.
//   - hub.go      — the Hub: client registry + topic broadcast.
//   - client.go   — one connection: read pump (subscribe control) +
//     write pump (drains the send queue, pings for keepalive).
//   - handler.go  — the HTTP handler that upgrades GET /api/v1/ws.
package realtime

import "fmt"

// Event is the server→client message envelope. Marshaled to JSON and
// written as a single WebSocket text frame.
type Event struct {
	// Type is the event kind (see the Type* constants).
	Type string `json:"type"`
	// Topic is the room this event belongs to; only clients subscribed
	// to it receive the event. Empty topic = delivered to every client.
	Topic string `json:"topic,omitempty"`
	// Data is the type-specific payload (any JSON-marshalable value).
	Data any `json:"data,omitempty"`
	// TS is the emit time in Unix milliseconds.
	TS int64 `json:"ts"`
}

// Server→client event types.
const (
	TypeServerStatus   = "server.status"   // a pull updated a server's reachability
	TypeServerCreated  = "server.created"  // a server was added via the API
	TypeServerUpdated  = "server.updated"  // a server's fields were edited
	TypeServerDeleted  = "server.deleted"  // a server was removed
	TypeMetrics        = "metrics"         // fresh metrics from a collection cycle
	TypeAlertFiring    = "alert.firing"    // an alert started firing
	TypeAlertResolved  = "alert.resolved"  // an alert cleared
	TypeLogLine        = "log.line"        // a live log line from the streamer
	TypeHello          = "hello"           // sent once on connect (server clock + version)
	TypeDBStatus       = "db.status"       // a DB target's replication status changed
	TypeDBCreated      = "db.created"      // a DB target was added
	TypeDBUpdated      = "db.updated"      // a DB target was edited
	TypeDBDeleted      = "db.deleted"      // a DB target was removed
	TypeControlStarted = "control.started" // a control-panel action was queued
	TypeControlUpdated = "control.updated" // a control-panel action changed state
)

// Client→server control actions (see ClientMessage.Action).
const (
	ActionSubscribe   = "subscribe"
	ActionUnsubscribe = "unsubscribe"
)

// ClientMessage is a control frame sent by the browser to manage its
// topic subscriptions. The browser sends, e.g.,
// {"action":"subscribe","topics":["servers","server:3"]}.
type ClientMessage struct {
	Action string   `json:"action"`
	Topics []string `json:"topics"`
}

// Topic helpers keep the room names in one place so producers and the
// frontend agree on the exact strings.

// TopicServers is the room for the server list + any server's status.
func TopicServers() string { return "servers" }

// TopicServer is the room for a single server's status + metrics.
func TopicServer(id int) string { return fmt.Sprintf("server:%d", id) }

// TopicAlerts is the room for alert fire/resolve events.
func TopicAlerts() string { return "alerts" }

// TopicLogs is the room for a single server's live log lines.
func TopicLogs(serverID int) string { return fmt.Sprintf("logs:%d", serverID) }

// TopicDatabases is the room for the DB-target list + replication status.
func TopicDatabases() string { return "databases" }

// TopicControl is the room for control-panel action lifecycle events.
func TopicControl() string { return "control" }

// Broadcaster is the narrow interface producers depend on so they don't
// import the whole hub. *Hub implements it. HasSubscribers lets a hot
// producer (e.g. the per-line log streamer) skip building an event when
// nobody is listening on the topic.
type Broadcaster interface {
	Broadcast(ev Event)
	HasSubscribers(topic string) bool
}

// NopBroadcaster is a no-op Broadcaster used when the hub is absent
// (so producers never need a nil check).
type NopBroadcaster struct{}

func (NopBroadcaster) Broadcast(Event)            {}
func (NopBroadcaster) HasSubscribers(string) bool { return false }
