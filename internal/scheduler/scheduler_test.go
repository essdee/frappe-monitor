package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// robfig/cron/v3's ConstantDelaySchedule (used by `@every`) has a 1-second
// minimum: any sub-second duration rounds up to 1s. So all timing tests
// in this file use 1-second cron intervals and adjust their sleeps to
// allow at least one boundary crossing.

func newTestScheduler(t *testing.T, maxParallel int, perJobTimeout time.Duration) *Scheduler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(maxParallel, perJobTimeout, logger)
}

func TestScheduler_RunsOnSchedule(t *testing.T) {
	s := newTestScheduler(t, 4, 5*time.Second)

	var count atomic.Int32
	require.NoError(t, s.Add("@every 1s", Job{
		ServerID: 1,
		Run: func(_ context.Context) error {
			count.Add(1)
			return nil
		},
	}))

	s.Start()
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	// Cron fires at second boundaries. After 2.6s wall time, we should
	// see at least 2 firings — possibly 3 depending on phase.
	time.Sleep(2600 * time.Millisecond)
	require.GreaterOrEqual(t, count.Load(), int32(2))
}

func TestScheduler_SemaphoreBoundsParallelism(t *testing.T) {
	const maxParallel = 2
	s := newTestScheduler(t, maxParallel, 5*time.Second)

	var (
		mu          sync.Mutex
		concurrent  int32
		peakSeen    int32
	)
	require.NoError(t, s.Add("@every 1s", Job{
		ServerID: 1,
		Run: func(_ context.Context) error {
			c := atomic.AddInt32(&concurrent, 1)
			defer atomic.AddInt32(&concurrent, -1)
			mu.Lock()
			if c > peakSeen {
				peakSeen = c
			}
			mu.Unlock()
			// Job runs longer than the 1s tick → backpressure on the semaphore.
			time.Sleep(1500 * time.Millisecond)
			return nil
		},
	}))

	s.Start()
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	// Run for ~4s — enough for several ticks to overlap.
	time.Sleep(4 * time.Second)

	mu.Lock()
	got := peakSeen
	mu.Unlock()
	require.LessOrEqual(t, got, int32(maxParallel),
		"peak concurrent jobs %d exceeded maxParallel %d", got, maxParallel)
	require.Greater(t, got, int32(0), "no jobs ran at all — scheduler broken")
}

func TestScheduler_StopCancelsRunning(t *testing.T) {
	s := newTestScheduler(t, 4, time.Hour) // long timeout — Stop is the canceler

	jobStarted := make(chan struct{}, 1)
	jobReturned := make(chan struct{}, 1)
	require.NoError(t, s.Add("@every 1s", Job{
		ServerID: 1,
		Run: func(ctx context.Context) error {
			select {
			case jobStarted <- struct{}{}:
			default:
			}
			<-ctx.Done()
			jobReturned <- struct{}{}
			return ctx.Err()
		},
	}))

	s.Start()

	// Wait for the first firing — up to ~1.5s.
	select {
	case <-jobStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("job never started")
	}

	stopErr := make(chan error, 1)
	go func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopErr <- s.Stop(stopCtx)
	}()

	// The running job should observe ctx cancellation and return.
	select {
	case <-jobReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("running job never returned after Stop")
	}

	select {
	case err := <-stopErr:
		require.NoError(t, err, "Stop should return cleanly once jobs drain")
	case <-time.After(3 * time.Second):
		t.Fatal("Stop never returned")
	}
}

func TestScheduler_RejectsBadCronSpec(t *testing.T) {
	s := newTestScheduler(t, 1, time.Second)
	err := s.Add("not a valid cron spec", Job{
		ServerID: 1,
		Run:      func(_ context.Context) error { return nil },
	})
	require.Error(t, err)
}

func TestScheduler_LogsJobError(t *testing.T) {
	// Verify a returning-error job does not crash the scheduler.
	s := newTestScheduler(t, 1, 5*time.Second)

	var ran atomic.Int32
	require.NoError(t, s.Add("@every 1s", Job{
		ServerID: 1,
		Run: func(_ context.Context) error {
			ran.Add(1)
			return errors.New("oops")
		},
	}))

	s.Start()
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	time.Sleep(2600 * time.Millisecond)
	require.GreaterOrEqual(t, ran.Load(), int32(2),
		"scheduler must keep firing even after a job returns an error")
}
