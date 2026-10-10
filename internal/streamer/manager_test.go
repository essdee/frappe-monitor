package streamer

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

func TestConfig_ServerSpecificFilesMayReuseIDsAndPaths(t *testing.T) {
	cfg := Config{
		Enabled: true, ScriptPath: ".frappe-monitor/frappe-monitor-stream.sh",
		Files: []FileSpecConfig{
			{ServerID: 1, ID: "web_error", Path: "/srv/a/logs/web.error.log", Bench: "a", LogType: "error"},
			{ServerID: 2, ID: "web_error", Path: "/srv/a/logs/web.error.log", Bench: "b", LogType: "error"},
		},
	}
	require.NoError(t, cfg.Validate())
	require.Equal(t, "/srv/a/logs/web.error.log", filesForServer(cfg.Files, 1)[0].Path)
	require.Equal(t, "b", filesForServer(cfg.Files, 2)[0].Bench)
	require.Empty(t, filesForServer(cfg.Files, 3))

	cfg.Files = append(cfg.Files, FileSpecConfig{ID: "web_error", Path: "/global.log"})
	require.Error(t, cfg.Validate(), "a global file ID conflicts with every server-specific copy")
}

func TestConfig_RejectsRemoteShellBreakingPathsAndIgnoresLegacyScriptPath(t *testing.T) {
	require.NoError(t, Config{Enabled: true, ScriptPath: "/tmp/it's-ignored", Files: []FileSpecConfig{{ID: "web", Path: "/logs/web.log"}}}.Validate())
	require.Error(t, Config{Enabled: true, ScriptPath: "/tmp/stream.sh", Files: []FileSpecConfig{{ID: "web", Path: "/logs/it's.log"}}}.Validate())
}

func TestManager_HotAddedSessionUsesManagerLifetime(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenEntStore(ctx,
		"file:streamer-manager?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	executor := sshpkg.NewFakeExecutor()
	manager, err := NewManager(Config{
		Enabled: true, ScriptPath: "/opt/frappe-monitor/stream.sh",
		Files:                []FileSpecConfig{{ID: "web", Path: "/logs/web.log"}},
		FlushIntervalSeconds: 3600,
	}, executor, store, &fakeLoki{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	t.Cleanup(rootCancel)
	require.NoError(t, manager.Start(rootCtx))
	t.Cleanup(func() { manager.Stop(2 * time.Second) })

	server, err := store.CreateServer(ctx, storage.NewServer{
		Name: "hot", Hostname: "hot-host", SSHUser: "monitor", SSHPort: 22,
		SSHKeyPath: "/tmp/key",
	})
	require.NoError(t, err)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	executor.SetStreamReader("hot-host", reader)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	require.NoError(t, manager.LaunchServer(requestCtx, server.ID))
	require.Eventually(t, func() bool {
		return executor.StreamCallCount("hot-host") == 1
	}, time.Second, 10*time.Millisecond)
	cancelRequest()

	// The old implementation passed requestCtx to Session.Run and killed
	// every API-added stream as soon as its short launch timeout expired.
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 1, executor.OpenStreamCount(),
		"session lifetime must be owned by Manager.Start's root context")
}
