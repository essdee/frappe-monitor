package storage

import (
	"context"
	"fmt"
	"log/slog"

	"frappe-monitor/ent"
	entpatchlog "frappe-monitor/ent/patchlog"
)

// This file implements the data-migration layer that runs on startup, the same
// way `bench migrate` does for Frappe:
//
//   1. ent auto-migration (in Open) creates/alters tables to match the schema.
//   2. RunMigrations applies any pending one-time PATCHES (recorded so each
//      runs exactly once), then runs idempotent SEEDS on every boot.
//
// To add a patch later, append a Patch to patchRegistry (or call RegisterPatch
// from an init()); it runs automatically on the next start.

// Patch is a one-time data migration: a named unit that runs once ever (tracked
// in the patch log). Use for backfills, data reshaping, or one-off fixes — NOT
// schema DDL, which ent's auto-migration already handles. Keep Run idempotent
// anyway as defense-in-depth.
type Patch struct {
	Name string
	Run  func(ctx context.Context, client *ent.Client) error
}

// Seed runs on EVERY startup and MUST be idempotent (check-then-insert). Use it
// to guarantee baseline rows exist.
type Seed struct {
	Name string
	Run  func(ctx context.Context, client *ent.Client) error
}

// patchRegistry is the ordered list of one-time patches; execution order is
// list order. Names are permanent identifiers — never rename or reuse a name
// once it has shipped. (Empty today; the machinery is ready for the first one.)
var patchRegistry []Patch

// seedRegistry holds idempotent seeds run on every boot.
var seedRegistry []Seed

// RegisterPatch / RegisterSeed let other packages contribute migrations from an
// init(). Built-in patches/seeds can also be appended to the slices directly.
func RegisterPatch(p Patch) { patchRegistry = append(patchRegistry, p) }
func RegisterSeed(s Seed)   { seedRegistry = append(seedRegistry, s) }

// RunMigrations applies pending one-time patches (recording each in the patch
// log) and then runs all idempotent seeds. Call once at startup, after Open.
// Safe to run repeatedly: already-applied patches are skipped.
func (s *EntStore) RunMigrations(ctx context.Context, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	applied := 0
	for _, p := range patchRegistry {
		done, err := s.client.PatchLog.Query().Where(entpatchlog.Name(p.Name)).Exist(ctx)
		if err != nil {
			return fmt.Errorf("check patch %q: %w", p.Name, err)
		}
		if done {
			continue
		}
		logger.Info("applying patch", "name", p.Name)
		if err := p.Run(ctx, s.client); err != nil {
			return fmt.Errorf("patch %q: %w", p.Name, err)
		}
		if err := s.client.PatchLog.Create().SetName(p.Name).Exec(ctx); err != nil {
			return fmt.Errorf("record patch %q: %w", p.Name, err)
		}
		applied++
	}
	if applied > 0 {
		logger.Info("patches applied", "count", applied)
	}
	for _, sd := range seedRegistry {
		if err := sd.Run(ctx, s.client); err != nil {
			return fmt.Errorf("seed %q: %w", sd.Name, err)
		}
	}
	return nil
}
