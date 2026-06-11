package storage

import (
	"context"
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
