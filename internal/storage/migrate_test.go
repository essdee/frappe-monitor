package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"frappe-monitor/ent"
)

func TestRunMigrations_PatchOnceSeedsEveryTime(t *testing.T) {
	// Swap the global registries so this test is isolated and doesn't leak
	// patches into other tests in the package.
	savedP, savedS := patchRegistry, seedRegistry
	t.Cleanup(func() { patchRegistry, seedRegistry = savedP, savedS })
	patchRegistry, seedRegistry = nil, nil

	patchRuns, seedRuns := 0, 0
	RegisterPatch(Patch{Name: "0001-test-patch", Run: func(context.Context, *ent.Client) error {
		patchRuns++
		return nil
	}})
	RegisterSeed(Seed{Name: "baseline", Run: func(context.Context, *ent.Client) error {
		seedRuns++
		return nil
	}})

	s := newTestStore(t).(*EntStore)
	ctx := context.Background()

	require.NoError(t, s.RunMigrations(ctx, nil))
	require.Equal(t, 1, patchRuns)
	require.Equal(t, 1, seedRuns)

	// Second run: the one-time patch is recorded and skipped; the seed re-runs.
	require.NoError(t, s.RunMigrations(ctx, nil))
	require.Equal(t, 1, patchRuns, "one-time patch must run exactly once")
	require.Equal(t, 2, seedRuns, "idempotent seed runs on every boot")
}

func TestRunMigrations_FailedPatchNotRecordedThenRetries(t *testing.T) {
	savedP, savedS := patchRegistry, seedRegistry
	t.Cleanup(func() { patchRegistry, seedRegistry = savedP, savedS })
	patchRegistry, seedRegistry = nil, nil

	calls := 0
	fail := true
	RegisterPatch(Patch{Name: "flaky", Run: func(context.Context, *ent.Client) error {
		calls++
		if fail {
			return errors.New("boom")
		}
		return nil
	}})

	s := newTestStore(t).(*EntStore)
	ctx := context.Background()

	// Failing patch: tx rolls back, nothing recorded, RunMigrations errors.
	require.Error(t, s.RunMigrations(ctx, nil))
	require.Equal(t, 1, calls)

	// Since it wasn't recorded, the next boot retries it (now it succeeds).
	fail = false
	require.NoError(t, s.RunMigrations(ctx, nil))
	require.Equal(t, 2, calls, "unrecorded patch is retried")

	// Now recorded → never runs again.
	require.NoError(t, s.RunMigrations(ctx, nil))
	require.Equal(t, 2, calls, "recorded patch is not re-run")
}

func TestSetConnPool_SQLiteStaysSingleWriter(t *testing.T) {
	s := newTestStore(t).(*EntStore)
	s.SetConnPool(20, 5) // a config trying to raise the cap
	require.Equal(t, 1, s.sqlDB.Stats().MaxOpenConnections,
		"sqlite must keep its single-writer cap regardless of config")
}
