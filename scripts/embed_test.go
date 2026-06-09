package scripts

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollectorScript_Embedded(t *testing.T) {
	require.NotEmpty(t, CollectorScript, "collector script must be embedded into the binary")
	require.True(t, strings.HasPrefix(CollectorScript, "#!/usr/bin/env bash"),
		"embedded script should start with bash shebang")
	require.Contains(t, CollectorScript, "###META", "must contain META section marker")
	require.Contains(t, CollectorScript, "###SERVER", "must contain SERVER section marker")
	require.Contains(t, CollectorScript, "###END", "must contain END marker")
}

func TestCollectorVersion_ReadsVersionLine(t *testing.T) {
	v := CollectorVersion()
	require.NotEqual(t, "unknown", v, "version line must be parseable")
	require.Regexp(t, `^\d+\.\d+\.\d+$`, v, "expected semver-style version")
}

func TestStreamerScript_Embedded(t *testing.T) {
	require.NotEmpty(t, StreamerScript, "streamer script must be embedded into the binary")
	require.True(t, strings.HasPrefix(StreamerScript, "#!/usr/bin/env bash"),
		"embedded streamer should start with bash shebang")
	// The header line on stdout is the load-bearing protocol contract;
	// keep it as a literal so the monitor's parser doesn't go out of sync.
	require.Contains(t, StreamerScript, `'##V=%s\n'`,
		"streamer must emit a ##V=<version> header line")
	require.Contains(t, StreamerScript, `'##F=%s|%s\n'`,
		"streamer must emit per-line ##F=<id>|<content> records")
}

func TestStreamerVersion_ReadsVersionLine(t *testing.T) {
	v := StreamerVersion()
	require.NotEqual(t, "unknown", v, "streamer version line must be parseable")
	require.Regexp(t, `^\d+\.\d+\.\d+$`, v, "expected semver-style version")
}

// TestStreamer_TailsAndPrefixes runs the actual streamer script
// against a temp directory, appends to two log files, and verifies
// the protocol bytes appear on stdout in the expected shape. This is
// the canonical "does the bash work" check; it's the only way to
// catch logic bugs that bash -n won't (e.g. pipe interleaving, prefix
// loop typos, resume-offset arithmetic).
func TestStreamer_TailsAndPrefixes(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	dir := t.TempDir()
	webPath := filepath.Join(dir, "web.log")
	errPath := filepath.Join(dir, "err.log")

	// Pre-seed both files with one historical line — the streamer
	// should NOT emit historical lines (default tail mode is -n 0).
	require.NoError(t, os.WriteFile(webPath, []byte("OLD line\n"), 0o644))
	require.NoError(t, os.WriteFile(errPath, []byte("OLD line\n"), 0o644))

	scriptPath := filepath.Join(dir, "stream.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(StreamerScript), 0o755))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", scriptPath,
		"--files=web:"+webPath+",err:"+errPath)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// Drain lines into a thread-safe buffer the test can poll.
	var (
		mu    sync.Mutex
		lines []string
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			mu.Lock()
			lines = append(lines, scanner.Text())
			mu.Unlock()
		}
	}()

	// Wait for the version header before appending anything — the
	// streamer's tail processes haven't necessarily finished spinning
	// up before we see the header, but waiting for the header guarantees
	// the script has at least cleared its argument-parsing phase.
	requireLineMatching(t, &mu, &lines, func(s string) bool {
		return strings.HasPrefix(s, "##V=")
	}, 2*time.Second, "expected ##V= header line")

	// Give tail processes a moment to attach to the files. Without this,
	// fast appends can land before tail's inotify watch is set up,
	// which would leave us with no events to assert on.
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, appendLine(webPath, "live web line one\n"))
	require.NoError(t, appendLine(errPath, "live err line one\n"))
	require.NoError(t, appendLine(webPath, "line with a | pipe in it\n"))

	requireLineMatching(t, &mu, &lines, func(s string) bool {
		return s == "##F=web|live web line one"
	}, 3*time.Second, "expected web line to be prefixed and emitted")

	requireLineMatching(t, &mu, &lines, func(s string) bool {
		return s == "##F=err|live err line one"
	}, 3*time.Second, "expected err line to be prefixed and emitted")

	requireLineMatching(t, &mu, &lines, func(s string) bool {
		return s == "##F=web|line with a | pipe in it"
	}, 3*time.Second, "pipe in line content must be preserved verbatim")

	// Historical pre-seeded line must NOT appear — default mode is
	// follow-from-end. (If we ever change the default, update this.)
	mu.Lock()
	for _, ln := range lines {
		require.NotContains(t, ln, "OLD line",
			"streamer should not replay pre-existing log content by default")
	}
	mu.Unlock()
}

func TestStreamer_MissingFileEmitsErrorSentinel(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "stream.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(StreamerScript), 0o755))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", scriptPath,
		"--files=web:"+filepath.Join(dir, "does-not-exist.log"))
	out, _ := cmd.CombinedOutput()
	require.Contains(t, string(out), "##F=web ##E=MISSING",
		"missing file should produce a per-file error sentinel, not crash the streamer")
}

func TestStreamer_RejectsBadArgs(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "stream.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(StreamerScript), 0o755))

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no files", []string{}, "--files is required"},
		{"bad file id", []string{"--files=we!rd:/tmp/x"}, "alphanumeric"},
		{"missing colon", []string{"--files=web"}, "want id:path"},
		{"non-numeric resume", []string{"--files=web:/tmp/x", "--resume=web:abc"}, "non-negative integer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", append([]string{scriptPath}, c.args...)...)
			out, err := cmd.CombinedOutput()
			require.Error(t, err, "expected non-zero exit for %q", c.name)
			require.Contains(t, string(out), c.want)
		})
	}
}

func TestStreamer_ResumeFromOffset(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "web.log")
	// 4 historical lines. Each "lineN\n" is 6 bytes (5 + newline) — so
	// after lines 1+2 we're at byte 12, and resume=12 should replay
	// lines 3+4 (and then any new appends).
	require.NoError(t, os.WriteFile(logPath, []byte("line1\nline2\nline3\nline4\n"), 0o644))

	scriptPath := filepath.Join(dir, "stream.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(StreamerScript), 0o755))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", scriptPath,
		"--files=web:"+logPath, "--resume=web:12")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	var (
		mu    sync.Mutex
		lines []string
	)
	go func() {
		for scanner.Scan() {
			mu.Lock()
			lines = append(lines, scanner.Text())
			mu.Unlock()
		}
	}()

	requireLineMatching(t, &mu, &lines, func(s string) bool { return s == "##F=web|line3" },
		3*time.Second, "resume=12 should replay line3 (the line at byte 12 onward)")
	requireLineMatching(t, &mu, &lines, func(s string) bool { return s == "##F=web|line4" },
		3*time.Second, "resume=12 should also replay line4")

	mu.Lock()
	defer mu.Unlock()
	for _, ln := range lines {
		require.NotEqual(t, "##F=web|line1", ln, "line1 is before the resume point")
		require.NotEqual(t, "##F=web|line2", ln, "line2 is before the resume point")
	}
}

// requireLineMatching polls a shared line buffer until pred matches a
// line or the deadline expires. Blocking the test on a channel from
// stdout has worse failure modes — if the streamer never emits, we
// want a clear "expected X" message, not a goroutine leak.
func requireLineMatching(t *testing.T, mu *sync.Mutex, lines *[]string,
	pred func(string) bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, ln := range *lines {
			if pred(ln) {
				mu.Unlock()
				return
			}
		}
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("%s\nlines so far:\n  %s", msg,
		strings.Join(*lines, "\n  "))
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
