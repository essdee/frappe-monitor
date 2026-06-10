package streamer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sshpkg "frappe-monitor/internal/ssh"
)

// recordingHandler captures every Handler call so tests can assert
// the session loop dispatched the expected events.
type recordingHandler struct {
	mu        sync.Mutex
	versions  []string
	lines     []recordedLine
	fileErrs  []recordedFileErr
	states    []recordedState
	flushed   int
}

type recordedLine struct {
	serverID int
	fileID   string
	content  string
	byteLen  int
}

type recordedFileErr struct {
	serverID int
	fileID   string
	token    string
}

type recordedState struct {
	serverID  int
	connected bool
	reason    string
}

func (h *recordingHandler) OnVersion(_ context.Context, _ int, v string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.versions = append(h.versions, v)
}

func (h *recordingHandler) OnLine(_ context.Context, serverID int, fid, content string, _ time.Time, byteLen int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, recordedLine{serverID, fid, content, byteLen})
}

func (h *recordingHandler) OnFileError(_ context.Context, serverID int, fid, tok string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fileErrs = append(h.fileErrs, recordedFileErr{serverID, fid, tok})
}

func (h *recordingHandler) OnSessionState(_ context.Context, serverID int, connected bool, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.states = append(h.states, recordedState{serverID, connected, reason})
}

func (h *recordingHandler) Flush(_ context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.flushed++
	return nil
}

func (h *recordingHandler) snapshot() (versions []string, lines []recordedLine, fileErrs []recordedFileErr, states []recordedState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	versions = append([]string(nil), h.versions...)
	lines = append([]recordedLine(nil), h.lines...)
	fileErrs = append([]recordedFileErr(nil), h.fileErrs...)
	states = append([]recordedState(nil), h.states...)
	return
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBuildRemoteCommand(t *testing.T) {
	cmd, err := BuildRemoteCommand("/opt/frappe-monitor/stream.sh",
		[]FileSpec{
			{ID: "web", Path: "/home/frappe/web.log"},
			{ID: "err", Path: "/home/frappe/err.log"},
		},
		map[string]int64{"web": 12345, "err": 0})
	require.NoError(t, err)
	require.Contains(t, cmd, "/opt/frappe-monitor/stream.sh")
	require.Contains(t, cmd, "--files=web:/home/frappe/web.log,err:/home/frappe/err.log")
	require.Contains(t, cmd, "--resume=web:12345")
	require.NotContains(t, cmd, "err:0",
		"a 0 offset means tail-from-end; we omit it from --resume")
}

func TestBuildRemoteCommand_RejectsForbiddenChars(t *testing.T) {
	_, err := BuildRemoteCommand("/script", []FileSpec{
		{ID: "web", Path: "/path/with'quote.log"},
	}, nil)
	require.Error(t, err, "single-quote in path must be rejected — would break the shell wrap")

	_, err = BuildRemoteCommand("/script", []FileSpec{
		{ID: "we'rd", Path: "/safe.log"},
	}, nil)
	require.Error(t, err, "single-quote in id must be rejected")

	_, err = BuildRemoteCommand("/script", nil, nil)
	require.Error(t, err, "no files = bail")
}

func TestSession_DispatchesProtocolEvents(t *testing.T) {
	body := strings.Join([]string{
		"##V=8.0.0",
		"##F=web|GET /api/method/foo",
		"##F=err|ValidationError: x",
		"##F=slowq ##E=MISSING",
		"some random line that's neither prefix",
		"##F=web|GET /api/method/bar",
		"",
	}, "\n") + "\n"

	exec := sshpkg.NewFakeExecutor()
	exec.SetStreamResponse("h1", body, nil)
	h := &recordingHandler{}

	sess := NewSession(SessionConfig{
		ServerID: 42,
		Target:   sshpkg.Target{Host: "h1", User: "u", KeyPath: "/k"},
		Files:    []FileSpec{{ID: "web", Path: "/web.log"}},
		// RemoteCommand can be anything — fake executor doesn't run
		// it, just records that Stream was called.
		RemoteCommand: "true",
		MinBackoff:    50 * time.Millisecond,
		MaxBackoff:    100 * time.Millisecond,
	}, exec, h, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := sess.Run(ctx)

	// Stream is finite; once it's drained the session loop tries to
	// reconnect (clean EOF). Stop after a short window.
	require.Eventually(t, func() bool {
		_, lines, _, _ := h.snapshot()
		return len(lines) >= 3
	}, time.Second, 20*time.Millisecond, "expected three EventLine dispatches")

	sess.Stop(time.Second)
	<-done

	versions, lines, fileErrs, states := h.snapshot()
	require.Equal(t, []string{"8.0.0"}, versions)
	require.Equal(t, []string{"GET /api/method/foo", "ValidationError: x", "GET /api/method/bar"},
		[]string{lines[0].content, lines[1].content, lines[2].content})
	require.Equal(t, "web", lines[0].fileID)
	require.Equal(t, "err", lines[1].fileID)

	require.Len(t, fileErrs, 1)
	require.Equal(t, "MISSING", fileErrs[0].token)
	require.Equal(t, "slowq", fileErrs[0].fileID)

	// At least one connected=true and one connected=false state.
	gotUp := false
	gotDown := false
	for _, s := range states {
		if s.connected {
			gotUp = true
		} else {
			gotDown = true
		}
	}
	require.True(t, gotUp, "session should report connected=true at least once")
	require.True(t, gotDown, "session should report connected=false on EOF reconnect")
}

func TestSession_ReconnectsAfterStreamFailure(t *testing.T) {
	exec := sshpkg.NewFakeExecutor()
	// First call: configured failure.
	exec.SetStreamResponse("h1", "", errors.New("simulated dial failure"))
	h := &recordingHandler{}

	sess := NewSession(SessionConfig{
		ServerID:      9,
		Target:        sshpkg.Target{Host: "h1"},
		Files:         []FileSpec{{ID: "web", Path: "/x"}},
		RemoteCommand: "true",
		MinBackoff:    20 * time.Millisecond,
		MaxBackoff:    50 * time.Millisecond,
	}, exec, h, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := sess.Run(ctx)

	// Wait for at least 2 attempted Stream calls — proves reconnect
	// loop is firing.
	require.Eventually(t, func() bool {
		return exec.StreamCallCount("h1") >= 2
	}, 2*time.Second, 20*time.Millisecond,
		"session should retry the stream after a failure")

	sess.Stop(time.Second)
	<-done
}

func TestSession_StopExitsCleanly(t *testing.T) {
	pr, pw := io.Pipe() // never written to → Read blocks forever

	exec := sshpkg.NewFakeExecutor()
	exec.SetStreamReader("h1", pr)
	h := &recordingHandler{}

	sess := NewSession(SessionConfig{
		ServerID:      1,
		Target:        sshpkg.Target{Host: "h1"},
		Files:         []FileSpec{{ID: "web", Path: "/x"}},
		RemoteCommand: "true",
		MinBackoff:    10 * time.Millisecond,
		MaxBackoff:    100 * time.Millisecond,
	}, exec, h, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := sess.Run(ctx)

	// Confirm the session is actually reading (handle was opened).
	require.Eventually(t, func() bool {
		return exec.StreamCallCount("h1") >= 1
	}, time.Second, 10*time.Millisecond)

	// Stop must unblock the Read by Closing the handle.
	stopped := sess.Stop(2 * time.Second)
	require.True(t, stopped, "Stop should unblock the session within timeout")
	<-done
	pw.Close()

	// And the fake reports no still-open streams (Stop closes handles).
	require.Eventually(t, func() bool {
		return exec.OpenStreamCount() == 0
	}, time.Second, 20*time.Millisecond)
}

func TestNextBackoff(t *testing.T) {
	require.Equal(t, 2*time.Second, nextBackoff(time.Second, 60*time.Second))
	require.Equal(t, 60*time.Second, nextBackoff(50*time.Second, 60*time.Second),
		"capped at max")
	require.Equal(t, 60*time.Second, nextBackoff(60*time.Second, 60*time.Second),
		"already at max stays at max")
}
