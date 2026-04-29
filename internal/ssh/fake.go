package ssh

import (
	"context"
	"fmt"
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

type FakeExecutor struct {
	mu     sync.Mutex
	resp   map[string]fakeResp
	calls  map[string]int
	lastIn map[string]fakeCall // per-host last cmd + stdin
}

func NewFakeExecutor() *FakeExecutor {
	return &FakeExecutor{
		resp:   map[string]fakeResp{},
		calls:  map[string]int{},
		lastIn: map[string]fakeCall{},
	}
}

func (f *FakeExecutor) SetResponse(host, out string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resp[host] = fakeResp{out: out, err: err}
}

func (f *FakeExecutor) CallCount(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[host]
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
