// Package scheduler runs per-server collection jobs on cron expressions.
// Phase 2 wires the per-server pull (ssh → parse → push to VM) here.
//
// Each fired job acquires a semaphore (bounding maxParallel concurrent
// jobs across the whole scheduler), then runs under a per-job timeout
// context derived from the scheduler's parent context. Stop cancels the
// parent context — running jobs see ctx.Done() and unwind — and waits
// for the cron loop and goroutines to drain.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Job is one per-server unit of work registered via Add.
// ServerID is informational (used in log lines); Run is what actually
// executes when the cron entry fires.
type Job struct {
	ServerID int
	Run      func(ctx context.Context) error
}

type Scheduler struct {
	cron    *cron.Cron
	sema    chan struct{}
	timeout time.Duration
	logger  *slog.Logger

	parentCtx    context.Context
	parentCancel context.CancelFunc

	wg sync.WaitGroup
}

// New builds a scheduler with the supplied parallelism cap and per-job
// timeout. logger is required (non-nil) — caller's responsibility.
//
// maxParallel < 1 clamps to 1. perJobTimeout < 1s clamps to 1s — a
// zero or sub-second timeout would create an already- or near-already-
// cancelled context, which is almost always a config bug.
//
// Drift / catch-up behavior follows robfig/cron/v3's default: missed
// ticks during a process pause are dropped (no catch-up) and at most
// one fire per boundary is queued. Acceptable for metrics; not for
// tasks that must execute every interval no matter what.
func New(maxParallel int, perJobTimeout time.Duration, logger *slog.Logger) *Scheduler {
	if maxParallel < 1 {
		maxParallel = 1
	}
	if perJobTimeout < time.Second {
		perJobTimeout = time.Second
	}
	pctx, pcancel := context.WithCancel(context.Background())
	return &Scheduler{
		cron:         cron.New(),
		sema:         make(chan struct{}, maxParallel),
		timeout:      perJobTimeout,
		logger:       logger,
		parentCtx:    pctx,
		parentCancel: pcancel,
	}
}

// Add registers a job under the supplied cron spec. Returns an error if
// the spec is unparseable. Safe to call before Start; calling after Start
// makes the new entry effective on the next cron tick.
func (s *Scheduler) Add(spec string, j Job) error {
	_, err := s.cron.AddFunc(spec, func() {
		s.runOnce(j)
	})
	if err != nil {
		return fmt.Errorf("scheduler: add %q: %w", spec, err)
	}
	return nil
}

// Start begins the cron loop. Caller must invoke at most once; cron/v3
// does NOT guard against double-start (it spawns a second run loop).
func (s *Scheduler) Start() {
	s.cron.Start()
}

// Stop cancels the scheduler's parent context (so in-flight jobs see
// ctx.Done()), stops the cron from firing new entries, and blocks until
// all running goroutines return — or until ctx expires, whichever first.
func (s *Scheduler) Stop(ctx context.Context) error {
	s.parentCancel()
	cronCtx := s.cron.Stop()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// ensure cron's own loop also finished
		<-cronCtx.Done()
		return nil
	case <-ctx.Done():
		return fmt.Errorf("scheduler: stop: %w", ctx.Err())
	}
}

// runOnce is the body of every fired cron entry: track via wg, gate on
// the semaphore, build the per-job timeout context, and call Job.Run.
func (s *Scheduler) runOnce(j Job) {
	s.wg.Add(1)
	defer s.wg.Done()

	// Acquire the parallelism semaphore. Bail if we're already shutting down.
	select {
	case s.sema <- struct{}{}:
	case <-s.parentCtx.Done():
		return
	}
	defer func() { <-s.sema }()

	ctx, cancel := context.WithTimeout(s.parentCtx, s.timeout)
	defer cancel()

	if err := j.Run(ctx); err != nil {
		s.logger.Error("scheduler: job failed",
			"server_id", j.ServerID,
			"err", err)
	}
}
