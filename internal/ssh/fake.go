package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

type fakeResp struct {
	out string
	err error
}

type fakeCall struct {
	cmd   string
	stdin string
}

// fakeStreamResp configures the next Stream() call. Either Reader or
// Err is set. When Reader is set, the returned StreamHandle reads from
// it until EOF (or the test calls Close on the handle, which signals
// the test-side reader via the Done channel).
type fakeStreamResp struct {
	reader io.Reader
	err    error
}

type FakeExecutor struct {
	mu          sync.Mutex
	resp        map[string]fakeResp
	streamResp  map[string]fakeStreamResp
	calls       map[string]int
	streamCalls map[string]int
	lastIn      map[string]fakeCall // per-host last cmd + stdin
	lastStream  map[string]string   // per-host last cmd passed to Stream
	openStreams []*fakeStreamHandle // tracked so tests can assert leak-freedom
}

func NewFakeExecutor() *FakeExecutor {
	return &FakeExecutor{
		resp:        map[string]fakeResp{},
		streamResp:  map[string]fakeStreamResp{},
		calls:       map[string]int{},
		streamCalls: map[string]int{},
		lastIn:      map[string]fakeCall{},
		lastStream:  map[string]string{},
	}
}

func (f *FakeExecutor) SetResponse(host, out string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resp[host] = fakeResp{out: out, err: err}
}

// SetStreamResponse configures what the next Stream() call to host
// returns. Pass body="..." to have the handle's Read return those
// bytes followed by io.EOF; pass err to fail the Stream() call
// itself. To control reads from the test side, pass a custom reader
// via SetStreamReader.
func (f *FakeExecutor) SetStreamResponse(host, body string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		f.streamResp[host] = fakeStreamResp{err: err}
		return
	}
	f.streamResp[host] = fakeStreamResp{reader: strings.NewReader(body)}
}

// SetStreamReader is the escape hatch for tests that need to push
// bytes incrementally — e.g. assert that the consumer parses lines
// as they arrive. The supplied reader's Read method will be called
// directly by the streamer.
func (f *FakeExecutor) SetStreamReader(host string, r io.Reader) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamResp[host] = fakeStreamResp{reader: r}
}

func (f *FakeExecutor) CallCount(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[host]
}

// StreamCallCount returns how many times Stream was invoked for host.
// Useful for asserting reconnect behaviour.
func (f *FakeExecutor) StreamCallCount(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streamCalls[host]
}

// LastStdin returns the stdin string captured on the most recent call to
// this host (Run captures ""); useful in tests for deploy-style commands.
func (f *FakeExecutor) LastStdin(host string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastIn[host].stdin
}

// LastCmd returns the cmd string captured on the most recent call.
func (f *FakeExecutor) LastCmd(host string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastIn[host].cmd
}

// LastStreamCmd returns the cmd passed to the most recent Stream call
// for host. Lets tests assert the streamer is invoking the right
// remote command line.
func (f *FakeExecutor) LastStreamCmd(host string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastStream[host]
}

// OpenStreamCount returns how many StreamHandles for any host are
// still open (Close not yet called). Useful for checking that the
// streamer doesn't leak sessions on shutdown.
func (f *FakeExecutor) OpenStreamCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, h := range f.openStreams {
		if !h.isClosed() {
			n++
		}
	}
	return n
}

func (f *FakeExecutor) Run(ctx context.Context, tgt Target, cmd string) (string, error) {
	return f.RunWithInput(ctx, tgt, cmd, "")
}

func (f *FakeExecutor) RunWithInput(_ context.Context, tgt Target, cmd, stdin string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[tgt.Host]++
	f.lastIn[tgt.Host] = fakeCall{cmd: cmd, stdin: stdin}
	r, ok := f.resp[tgt.Host]
	if !ok {
		return "", fmt.Errorf("fake: no response configured for %q", tgt.Host)
	}
	return r.out, r.err
}

func (f *FakeExecutor) Stream(_ context.Context, tgt Target, cmd string) (StreamHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamCalls[tgt.Host]++
	f.lastStream[tgt.Host] = cmd
	r, ok := f.streamResp[tgt.Host]
	if !ok {
		return nil, fmt.Errorf("fake: no stream response configured for %q", tgt.Host)
	}
	if r.err != nil {
		return nil, r.err
	}
	h := &fakeStreamHandle{r: r.reader, closed: make(chan struct{})}
	f.openStreams = append(f.openStreams, h)
	return h, nil
}

// fakeStreamHandle is the StreamHandle the fake returns. Read passes
// through to the configured reader. Close is idempotent and signals
// any goroutine that's waiting for Close (via the closed channel).
type fakeStreamHandle struct {
	r io.Reader

	mu        sync.Mutex
	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error
}

func (h *fakeStreamHandle) Read(p []byte) (int, error) {
	if h.r == nil {
		return 0, io.EOF
	}
	// If Close was called, all subsequent Reads return EOF — mirrors
	// the real ssh session behaviour: closing the session unblocks
	// blocking Reads with an error.
	select {
	case <-h.closed:
		return 0, io.EOF
	default:
	}
	return h.r.Read(p)
}

func (h *fakeStreamHandle) Close() error {
	h.closeOnce.Do(func() {
		close(h.closed)
		// If the underlying reader is itself a Closer, close it too —
		// lets test code use io.PipeReader/PipeWriter pairs cleanly.
		if c, ok := h.r.(io.Closer); ok {
			h.closeErr = c.Close()
		}
	})
	return h.closeErr
}

func (h *fakeStreamHandle) isClosed() bool {
	select {
	case <-h.closed:
		return true
	default:
		return false
	}
}

// _ asserts FakeExecutor satisfies the Executor interface at compile
// time. If we ever add a method to Executor and forget to implement
// it on the fake, this will fail to build before the tests run.
var _ Executor = (*FakeExecutor)(nil)

// errSentinel for tests that want to recognise a known-bad return
// without string-matching.
var errFakeStreamUnconfigured = errors.New("fake: stream unconfigured")

// Suppress "errSentinel declared but unused" — exported via package-private use.
var _ = errFakeStreamUnconfigured
