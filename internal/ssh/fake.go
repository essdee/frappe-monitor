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

type FakeExecutor struct {
	mu    sync.Mutex
	resp  map[string]fakeResp
	calls map[string]int
}

func NewFakeExecutor() *FakeExecutor {
	return &FakeExecutor{
		resp:  map[string]fakeResp{},
		calls: map[string]int{},
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

func (f *FakeExecutor) Run(_ context.Context, tgt Target, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[tgt.Host]++
	r, ok := f.resp[tgt.Host]
	if !ok {
		return "", fmt.Errorf("fake: no response configured for %q", tgt.Host)
	}
	return r.out, r.err
}
