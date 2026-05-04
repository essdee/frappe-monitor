# Contributing & Maintenance

Conventions for keeping the project healthy when adding or changing features.

## The documentation rule

**When you add or change a feature, update the docs in `docs/guide/` in the same commit.**

This is non-negotiable. The dated `docs/YYYY-MM-DD/N.md` files capture the *decision* (why we did it, what we considered, what we rejected). The files in `docs/guide/` capture *how to use the result*. If the operator-facing surface changes, the operator-facing doc has to change with it.

Practical mapping:

| If you change… | Update… |
|---|---|
| HTTP routes, request/response shapes | [`api.md`](api.md) |
| YAML config keys or defaults | [`configuration.md`](configuration.md) |
| Install layout, systemd units, prerequisites | [`deployment.md`](deployment.md) |
| Lifecycle, logging, backups, sizing | [`operations.md`](operations.md) |
| What metrics/logs are emitted, dashboard pages | [`usage.md`](usage.md) |
| Components, data flow, storage | [`architecture.md`](architecture.md) |
| Common failure mode you discovered | [`troubleshooting.md`](troubleshooting.md) |

Reviewers should reject diffs that change behavior without touching the relevant guide file. Same standard as missing tests.

## Where things live

```
docs/
  guide/                      ← evergreen operator docs (kept current)
  YYYY-MM-DD/N.md             ← dated decisions, frozen in time
  hardening-backlog.md        ← non-blocking quality items, FIFO
```

Everything in `docs/guide/` should always reflect what the code does *today*. Everything in `docs/YYYY-MM-DD/` is a snapshot — never edit a dated decision after it's merged; supersede it with a new dated entry that links back.

## Branching

`feat/phase-2-server-metrics` is the long-lived working branch. No per-feature branches; no PRs per feature. The user (project owner) does final manual testing in flight.

Commits in `main` are reserved for shippable releases. Phase rollups land in `main` after the user signs off on the working branch.

## Commit format

```
<area>(<scope>): <imperative summary>

<body explaining why, not what>

Co-Authored-By: <attribution>
```

Common scopes: `api`, `web`, `collector`, `parser`, `scheduler`, `storage`, `metrics`, `logs`, `config`, `deploy`, `docs`, `chore`, `feat`, `fix`, `test`. The `area` and `scope` together should make the commit's purpose obvious from the log.

Existing examples in the log:

```
feat(api): bench + site hierarchy endpoints derived from VM
feat(web): bench + site list/detail pages with metrics + logs
chore: phase 5 smoke + acceptance
```

## Tests

```bash
make test
```

Runs `go test ./... -race -count=1`. Targets the per-package suites in `internal/*` and `scripts/`. Frontend doesn't have unit tests today (the SPA is small enough that the `npm run build` typecheck catches most regressions); when it grows, add `vitest`.

For end-to-end coverage:

```bash
./scripts/smoke.sh
```

This is the canonical "did I break anything" check. It builds the binary, brings up VM + Loki, exercises every HTTP route, writes synthetic metrics + logs, queries them back through the binary, and verifies the SIGTERM-clean-exit contract. Phase by phase the smoke gets longer; expect ≤ 60s end-to-end.

Add a Phase N section to `scripts/smoke.sh` whenever you ship a new HTTP-visible behavior. The pattern is:

```bash
echo "==> Phase <N> (<feature>)"
# … set-up …
ok "did the thing"
# assertion
[ "$(...)" = "expected" ] || fail "expected ..., got $(...)"
ok "behavior verified"
```

## Coding standards

| Rule | Why |
|---|---|
| `CGO_ENABLED=0`. Stay pure Go. | Cross-compile, simpler distribution, no libc dependency. |
| Error wrapping with `%w`. | Lets the SSH pool's typed sentinels reach the `test-connection` handler without string-matching. |
| Slog (`log/slog`) for all logs. | Structured, journald-friendly. No `fmt.Print*` in non-test code. |
| `chi` router only. | Don't introduce a second router; keep middleware ordering predictable. |
| `ent` for SQLite. | Schema migrations are forward-only; commit `ent/` generated code. |
| Build tag `embed_dist` for SPA. | Lets `go build` work without npm during fast iteration. |

For frontend:

| Rule | Why |
|---|---|
| Vue 3 `<script setup>` + TypeScript. | One pattern across the SPA. |
| ECharts for charts. | Already the chart library; don't add a second. |
| Composables under `composables/`. | Stateful logic isolated from views. |
| `vite build` must pass strict TS. | The check is in `make build`. |

## Phase status (project-wide)

The master plan is in `docs/2026-04-22/1.md`. Current state:

| Phase | Done? |
|---|---|
| 1 — Foundation | ✓ |
| 2 — Server metrics → VM | ✓ |
| 3 — Bench/site metrics + Loki | ✓ |
| 4 — Dashboard foundation | ✓ |
| 5 — Dashboard full coverage | ✓ |
| 6 — Telegram alerting | ✓ |
| 7 v1 — Auth + in-dashboard server CRUD | ✓ |
| 7 v2 — Deploy markers, multi-tenant, search | post-v1 backlog |

Each phase has a planning doc in `docs/YYYY-MM-DD/N.md` and an acceptance writeup the day it's signed off.

## Hardening backlog

`docs/hardening-backlog.md` is the FIFO list of "we noticed this could be better but it's not blocking the current phase." Push items there liberally; pull them off when the next phase has slack.

Examples of what belongs there: tighter Caddy auth defaults, jitter in scheduler ticks, p95 instead of avg in site detail, in-dashboard add-server form (deferred from Phase 5). Things that **don't** belong there: bugs (file an issue or fix it), security holes (fix immediately), feature requests (those go in dated docs).

## Local development loop

```bash
# Bring up backends.
make vm-up

# Iterate on Go-only changes (no npm rebuild needed).
make build-no-web && ./bin/monitor-server --config ./config/monitor.yaml

# Iterate on the SPA (live-reload via Vite dev server).
cd web && npm run dev      # http://localhost:5173 — proxies API to :8080

# Full integration loop (rebuild SPA + bake into binary).
make build && make run
```

For SPA work, the Vite dev server (`:5173`) hot-reloads on save and proxies `/api/*` to the running monitor binary on `:8080`. That's the fast path. The embedded build is the slow path used for releases and smoke.

## Cutting a release

1. Verify smoke passes: `./scripts/smoke.sh`.
2. Bump the version in `scripts/embed.go` if applicable.
3. Tag: `git tag -a vX.Y.Z -m "..."`.
4. Build: `make build`. The resulting `bin/monitor-server` is the artifact.
5. On each target host: `git pull && sudo ./deploy/install.sh`.

Phase 7 will add a proper release pipeline (signed binaries, changelog generation). Until then, this is enough.
