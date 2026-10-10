package streamer

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStreamerScriptReportsAbsoluteOriginAndRotation(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "web.log")
	require.NoError(t, os.WriteFile(logPath, []byte("existing\n"), 0o600))

	cmd := exec.Command("bash", "../../scripts/frappe-monitor-stream.sh", "--files=web:"+logPath)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())

	events := make(chan Event, 32)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			events <- ParseLine(scanner.Text())
		}
		close(events)
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-waitDone:
		case <-time.After(3 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-waitDone
		}
	})

	next := func(match func(Event) bool) Event {
		t.Helper()
		timer := time.NewTimer(4 * time.Second)
		defer timer.Stop()
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					t.Fatalf("streamer exited early: %s", stderr.String())
				}
				if match(ev) {
					return ev
				}
			case <-timer.C:
				t.Fatalf("timed out waiting for streamer event: %s", stderr.String())
			}
		}
	}

	version := next(func(ev Event) bool { return ev.Type == EventVersion })
	require.Equal(t, ProtocolVersion, version.Version)
	origin := next(func(ev Event) bool { return ev.Type == EventOffset && ev.FileID == "web" })
	require.NotEmpty(t, origin.FileIdentity)
	require.Equal(t, int64(len("existing\n")), origin.Offset,
		"a new stream must record EOF as its absolute origin")

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("new\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	line := next(func(ev Event) bool { return ev.Type == EventLine && ev.Content == "new" })
	require.Equal(t, origin.FileIdentity, line.FileIdentity)
	require.Equal(t, int64(len("existing\nnew\n")), line.Offset)

	unicodeLine := "hé🙂"
	f, err = os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(unicodeLine + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	unicodeEvent := next(func(ev Event) bool { return ev.Type == EventLine && ev.Content == unicodeLine })
	require.Equal(t, int64(len("existing\nnew\n")+len(unicodeLine)+1), unicodeEvent.Offset,
		"protocol offsets must count bytes, not locale-dependent characters")

	require.NoError(t, os.Rename(logPath, logPath+".1"))
	require.NoError(t, os.WriteFile(logPath, []byte("rotated\n"), 0o600))
	rotation := next(func(ev Event) bool {
		return ev.Type == EventOffset && ev.FileID == "web" && ev.FileIdentity != origin.FileIdentity
	})
	require.Equal(t, int64(0), rotation.Offset)
	rotatedLine := next(func(ev Event) bool { return ev.Type == EventLine && ev.Content == "rotated" })
	require.Equal(t, rotation.FileIdentity, rotatedLine.FileIdentity)
	require.Equal(t, int64(len("rotated\n")), rotatedLine.Offset)
}
