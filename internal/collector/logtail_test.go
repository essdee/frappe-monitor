package collector

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

func newTestTailer(t *testing.T) (*LogTailer, *sshpkg.FakeExecutor, storage.Store) {
	t.Helper()
	store, err := storage.OpenEntStore(context.Background(),
		"file:logtail?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	exec := sshpkg.NewFakeExecutor()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &LogTailer{
		Store:  store,
		Exec:   exec,
		Logger: logger,
	}, exec, store
}

func makeTailServer(t *testing.T, store storage.Store) *storage.Server {
	t.Helper()
	srv, err := store.CreateServer(context.Background(), storage.NewServer{
		Name: "h", Hostname: "tail-host", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)
	return srv
}

func TestTailFile_FirstCycle(t *testing.T) {
	tailer, exec, store := newTestTailer(t)
	srv := makeTailServer(t, store)

	// Simulate the remote returning SIZE:42 + 42 bytes (no prior cursor).
	body := "line one\nline two\nline three\n"
	exec.SetResponse("tail-host", "SIZE:"+intStr(int64(len(body)))+"\n"+body, nil)

	res, err := tailer.TailFile(context.Background(),
		sshpkg.Target{Host: "tail-host"},
		srv.ID,
		map[string]string{"server": "h", "log_type": "error"},
		LogFile{Path: "/var/log/x.log", Type: "error"},
	)
	require.NoError(t, err)
	require.Equal(t, int64(len(body)), res.NewOffset)
	require.Len(t, res.Stream.Entries, 3)
	require.Equal(t, "line one", res.Stream.Entries[0].Line)
	require.Equal(t, "line three", res.Stream.Entries[2].Line)
	require.Equal(t, "h", res.Stream.Labels["server"])
	require.Equal(t, "error", res.Stream.Labels["log_type"])
}

func TestTailFile_Incremental(t *testing.T) {
	tailer, exec, store := newTestTailer(t)
	srv := makeTailServer(t, store)

	// Pre-populate cursor at offset=20.
	require.NoError(t, store.UpsertLogCursor(context.Background(), storage.LogCursor{
		ServerID: srv.ID, LogPath: "/var/log/y.log", ByteOffset: 20,
	}))

	// Remote reports total file size 60; we got 40 new bytes after offset 20.
	newBytes := "newest line one\nnewest two\n" // 26 bytes; file size sim 46
	exec.SetResponse("tail-host", "SIZE:46\n"+newBytes, nil)

	res, err := tailer.TailFile(context.Background(),
		sshpkg.Target{Host: "tail-host"},
		srv.ID,
		map[string]string{"server": "h"},
		LogFile{Path: "/var/log/y.log", Type: "error"},
	)
	require.NoError(t, err)
	// Steady state: new_offset = prev + len(body) = 20 + 26 = 46 (clamped to size).
	require.Equal(t, int64(46), res.NewOffset)
	require.Len(t, res.Stream.Entries, 2)
}

func TestTailFile_RotationResetsOffset(t *testing.T) {
	tailer, exec, store := newTestTailer(t)
	srv := makeTailServer(t, store)

	// Cursor was at 1000 bytes from a previous file; current file is shorter.
	require.NoError(t, store.UpsertLogCursor(context.Background(), storage.LogCursor{
		ServerID: srv.ID, LogPath: "/var/log/z.log", ByteOffset: 1000,
	}))

	// Remote reports SIZE:50 (much less than 1000) — file rotated. Body
	// is the entire new file's contents.
	rotated := "fresh line\n"
	exec.SetResponse("tail-host", "SIZE:50\n"+rotated, nil)

	res, err := tailer.TailFile(context.Background(),
		sshpkg.Target{Host: "tail-host"},
		srv.ID,
		map[string]string{"server": "h"},
		LogFile{Path: "/var/log/z.log", Type: "error"},
	)
	require.NoError(t, err)
	require.Equal(t, int64(len(rotated)), res.NewOffset)
	require.Len(t, res.Stream.Entries, 1)
	require.Equal(t, "fresh line", res.Stream.Entries[0].Line)
}

func TestTailFile_NoNewBytes(t *testing.T) {
	tailer, exec, store := newTestTailer(t)
	srv := makeTailServer(t, store)

	// Cursor at 100; remote file size is also 100 → no new bytes.
	require.NoError(t, store.UpsertLogCursor(context.Background(), storage.LogCursor{
		ServerID: srv.ID, LogPath: "/var/log/q.log", ByteOffset: 100,
	}))
	exec.SetResponse("tail-host", "SIZE:100\n", nil)

	res, err := tailer.TailFile(context.Background(),
		sshpkg.Target{Host: "tail-host"},
		srv.ID,
		map[string]string{"server": "h"},
		LogFile{Path: "/var/log/q.log", Type: "error"},
	)
	require.NoError(t, err)
	require.Equal(t, int64(100), res.NewOffset)
	require.Empty(t, res.Stream.Entries)
}

func TestTailFile_MissingFile(t *testing.T) {
	tailer, exec, store := newTestTailer(t)
	srv := makeTailServer(t, store)

	// Remote script reports SIZE:0 (file not present / unreadable) and exits 0.
	exec.SetResponse("tail-host", "SIZE:0\n", nil)

	res, err := tailer.TailFile(context.Background(),
		sshpkg.Target{Host: "tail-host"},
		srv.ID,
		map[string]string{"server": "h"},
		LogFile{Path: "/var/log/missing.log", Type: "error"},
	)
	require.NoError(t, err)
	require.Equal(t, int64(0), res.NewOffset)
	require.Empty(t, res.Stream.Entries)
}

func TestTailFile_RemoteCmdContainsPath(t *testing.T) {
	tailer, exec, store := newTestTailer(t)
	srv := makeTailServer(t, store)

	exec.SetResponse("tail-host", "SIZE:0\n", nil)

	_, err := tailer.TailFile(context.Background(),
		sshpkg.Target{Host: "tail-host"},
		srv.ID,
		map[string]string{},
		LogFile{Path: "/has space/log.log", Type: "error"},
	)
	require.NoError(t, err)

	// Verify the constructed remote command shell-quotes the path so a
	// space doesn't tokenize the argument.
	require.Contains(t, exec.LastCmd("tail-host"), `'/has space/log.log'`)
}

func TestParseTailOutput_RejectsMissingHeader(t *testing.T) {
	_, _, err := parseTailOutput("no header here\n")
	require.Error(t, err)
}

func TestParseTailOutput_RejectsMalformedSize(t *testing.T) {
	_, _, err := parseTailOutput("SIZE:abc\nbody\n")
	require.Error(t, err)
}

func TestSplitLines(t *testing.T) {
	require.Nil(t, splitLines(""))
	require.Equal(t, []string{"a", "b"}, splitLines("a\nb\n"))
	require.Equal(t, []string{"a", "b"}, splitLines("a\nb"))
	// Empty mid-lines are preserved by Split (they're caller's choice
	// to filter); only the last empty after a trailing \n is dropped.
	require.Equal(t, []string{"a", "", "b"}, splitLines("a\n\nb\n"))
}

func TestShellQuote_HandlesSingleQuote(t *testing.T) {
	require.Equal(t, `'simple'`, shellQuote("simple"))
	require.Equal(t, `'has spaces'`, shellQuote("has spaces"))
	require.Equal(t, `'with'\''quote'`, shellQuote(`with'quote`))
}

// helper: format int64 as decimal.
func intStr(n int64) string {
	return formatInt(n)
}

func formatInt(n int64) string {
	// Avoid an extra import in the test file by hand-writing a tiny helper.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
