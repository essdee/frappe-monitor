package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"
)

func dialHub(t *testing.T, hub *Hub) (*websocket.Conn, context.Context, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(hub.ServeWS))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)

	// First frame is always the hello.
	var hello Event
	require.NoError(t, wsjson.Read(ctx, conn, &hello))
	require.Equal(t, TypeHello, hello.Type)

	cleanup := func() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		cancel()
		hub.Close()
		srv.Close()
	}
	return conn, ctx, cleanup
}

// readUntil reads frames until one of type want arrives or ctx expires.
func readUntil(ctx context.Context, conn *websocket.Conn, want string) (Event, error) {
	for {
		var ev Event
		if err := wsjson.Read(ctx, conn, &ev); err != nil {
			return Event{}, err
		}
		if ev.Type == want {
			return ev, nil
		}
	}
}

func TestHub_BroadcastReachesSubscriber(t *testing.T) {
	hub := NewHub(nil, HubConfig{})
	conn, ctx, cleanup := dialHub(t, hub)
	defer cleanup()

	require.NoError(t, wsjson.Write(ctx, conn,
		ClientMessage{Action: ActionSubscribe, Topics: []string{TopicServers()}}))

	got := make(chan Event, 1)
	go func() {
		ev, err := readUntil(ctx, conn, TypeServerStatus)
		if err == nil {
			got <- ev
		}
	}()

	// Re-broadcast until received: the subscribe control frame and the
	// first broadcast can race, and events to a not-yet-subscribed client
	// are dropped (not buffered).
	deadline := time.After(3 * time.Second)
	for {
		hub.Broadcast(Event{Type: TypeServerStatus, Topic: TopicServers(),
			Data: map[string]any{"id": 1, "status": "reachable"}})
		select {
		case ev := <-got:
			require.Equal(t, TopicServers(), ev.Topic)
			require.NotZero(t, ev.TS, "TS should be stamped")
			return
		case <-time.After(25 * time.Millisecond):
		case <-deadline:
			t.Fatal("subscriber never received the broadcast")
		}
	}
}

func TestHub_FiltersUnsubscribedTopics(t *testing.T) {
	hub := NewHub(nil, HubConfig{})
	conn, ctx, cleanup := dialHub(t, hub)
	defer cleanup()

	// Subscribe only to "servers" — never to "alerts".
	require.NoError(t, wsjson.Write(ctx, conn,
		ClientMessage{Action: ActionSubscribe, Topics: []string{TopicServers()}}))

	// First, reliably land a servers event so we know the subscription
	// is active on the hub side.
	confirm := make(chan struct{})
	go func() {
		if _, err := readUntil(ctx, conn, TypeServerStatus); err == nil {
			close(confirm)
		}
	}()
	deadline := time.After(3 * time.Second)
loop:
	for {
		hub.Broadcast(Event{Type: TypeServerStatus, Topic: TopicServers()})
		select {
		case <-confirm:
			break loop
		case <-time.After(25 * time.Millisecond):
		case <-deadline:
			t.Fatal("subscription never became active")
		}
	}

	// Now: an alerts event (NOT subscribed) followed by a marked servers
	// event. The next frame the client reads must be the servers marker —
	// if filtering were broken, the alerts event would arrive first.
	hub.Broadcast(Event{Type: TypeAlertFiring, Topic: TopicAlerts()})
	hub.Broadcast(Event{Type: TypeServerStatus, Topic: TopicServers(),
		Data: map[string]any{"marker": "last"}})

	ev, err := readUntil(ctx, conn, TypeServerStatus)
	require.NoError(t, err)
	require.Equal(t, TopicServers(), ev.Topic)
	require.NotEqual(t, TypeAlertFiring, ev.Type, "unsubscribed topic must not be delivered")
}

func TestHub_ClientCountAndClose(t *testing.T) {
	hub := NewHub(nil, HubConfig{})
	conn, ctx, cleanup := dialHub(t, hub)
	defer cleanup()
	_ = ctx

	require.Eventually(t, func() bool { return hub.ClientCount() == 1 },
		2*time.Second, 20*time.Millisecond, "client should register")

	_ = conn.Close(websocket.StatusNormalClosure, "")
	require.Eventually(t, func() bool { return hub.ClientCount() == 0 },
		2*time.Second, 20*time.Millisecond, "client should unregister on disconnect")
}

// TestHub_HasSubscribers guards the per-topic subscriber index that lets
// hot producers skip building events nobody is watching.
func TestHub_HasSubscribers(t *testing.T) {
	hub := NewHub(nil, HubConfig{})
	conn, ctx, cleanup := dialHub(t, hub)
	defer cleanup()

	require.False(t, hub.HasSubscribers(TopicLogs(7)), "no subscribers initially")

	require.NoError(t, wsjson.Write(ctx, conn,
		ClientMessage{Action: ActionSubscribe, Topics: []string{TopicLogs(7)}}))
	require.Eventually(t, func() bool { return hub.HasSubscribers(TopicLogs(7)) },
		2*time.Second, 20*time.Millisecond, "subscribe should register the topic")

	require.NoError(t, wsjson.Write(ctx, conn,
		ClientMessage{Action: ActionUnsubscribe, Topics: []string{TopicLogs(7)}}))
	require.Eventually(t, func() bool { return !hub.HasSubscribers(TopicLogs(7)) },
		2*time.Second, 20*time.Millisecond, "unsubscribe should clear the topic")
}

// TestHub_HasSubscribersClearedOnDisconnect ensures a dropped connection
// releases its topic counts (no leak that would keep producers working).
func TestHub_HasSubscribersClearedOnDisconnect(t *testing.T) {
	hub := NewHub(nil, HubConfig{})
	conn, ctx, cleanup := dialHub(t, hub)
	defer cleanup()

	require.NoError(t, wsjson.Write(ctx, conn,
		ClientMessage{Action: ActionSubscribe, Topics: []string{TopicAlerts()}}))
	require.Eventually(t, func() bool { return hub.HasSubscribers(TopicAlerts()) },
		2*time.Second, 20*time.Millisecond)

	_ = conn.Close(websocket.StatusNormalClosure, "")
	require.Eventually(t, func() bool { return !hub.HasSubscribers(TopicAlerts()) },
		2*time.Second, 20*time.Millisecond, "disconnect must release the subscriber count")
}
