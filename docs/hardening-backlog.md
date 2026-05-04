# Hardening Backlog

A living list of non-blocking improvements raised during reviews. Not a feature backlog — these are quality / robustness / security items that don't block the milestone they were flagged in.

Each item has a phase target (when we'd want to address it by) and the review that surfaced it. Items move to "Done" with a commit reference once addressed.

## Open

### Storage layer

| # | Item | Source | Phase target | Notes |
|---|---|---|---|---|
| S1 | `isUniqueHostnameViolation` uses substring match on `"hostname"` (brittle if a future column name contains that substring, e.g. `hostname_alias`). | Task 4 / Task 7 review | Phase 7 | ent does not expose typed per-column constraint errors. Add a regression test if a `hostname`-adjacent column is ever added; consider parsing SQLite error structure if a typed approach becomes available. |

### SSH layer

| # | Item | Source | Phase target | Notes |
|---|---|---|---|---|
| H1 | `Pool.getOrDial` ignores its `ctx` parameter (relies only on `cfg.DialTimeout` for dial deadline). | Task 5 review | Phase 7 | Replace `ssh.Dial` with `(&net.Dialer{Timeout: ...}).DialContext(ctx, ...)` then `ssh.NewClientConn`. Lets parent ctx cancel an in-flight dial. |
| H2 | `classifyDialError` is untested in unit tests (requires a real `sshd`). | Task 5 review | Phase 1 (smoke script) | Will be exercised by Task 10's smoke script against a real bench server. Acceptable to defer beyond unit testing. |
| H3 | `out.String()` is interpolated into command-failure error strings unbounded. | Task 5 review | Phase 7 | A misbehaving remote command could produce a multi-MB error string. Cap to ~4 KiB when interpolating. |
| H4 | `loadKey` re-reads + re-parses the private key on every cache miss; the parsed `ssh.Signer` is not cached. | Task 5 review (master plan §11) | Phase 1 polish | Small refactor: cache the `ssh.AuthMethod` keyed by `KeyPath`. |
| H5 | `Pool` uses `ssh.InsecureIgnoreHostKey` for the host-key callback. | Master plan §11 | Phase 7 | Replace with a `known_hosts` verifier per master plan section 11. Intentional for Phase 1. |

### API layer

| # | Item | Source | Phase target | Notes |
|---|---|---|---|---|
| A1 | `json.NewDecoder(r.Body).Decode(...)` does not use `DisallowUnknownFields`. | Task 7 review | Phase 7 | Typo'd field names silently pass. Set `decoder.DisallowUnknownFields()` and surface the resulting error to the client. |
| A2 | No `http.MaxBytesReader` wrapping request bodies. | Task 7 review | Phase 7 | Pathological large bodies would be parsed. Cap at e.g. 1 MiB project-wide via middleware. |
| A3 | No API-layer range check on `ssh_port` (e.g. negative or > 65535). | Task 7 review | Phase 7 | Storage's `Positive()` catches negative but error message is opaque ent text. Add a clear API-layer check: `ssh_port >= 1 && ssh_port <= 65535`. |
| A4 | `chi.URLParam` + `strconv.Atoi` repeated across handlers. | Task 7 review | When count hits 3+ | Currently 2 sites (`get`, `testConnection`). Extract `idFromURL(r) (int, error)` once a 3rd appears. |
| A5 | `cmd/monitor/main.go` logs `"http listening"` *before* `srv.ListenAndServe()` returns. A bind failure (port in use) emits the optimistic log line followed by the error — confusing. | Task 9 review | Phase 1 polish | Move the "http listening" log into the goroutine *after* the listener binds successfully, e.g. by calling `net.Listen` first then logging then `srv.Serve(l)`. |

### Storage / runtime

| # | Item | Source | Phase target | Notes |
|---|---|---|---|---|
| R1 | `client.Schema.Create(ctx)` runs on every store open. | Task 4 review | Phase 7+ | Switch to versioned migrations (e.g. ent's `migrate.Diff` workflow) once schema mutations become regular events. |

### Parser / metrics

| # | Item | Source | Phase target | Notes |
|---|---|---|---|---|
| P1 | Tokenizer drops per-line line numbers when materializing `Section.KVs`. Errors from `ServerFromSections` cite field names but not file-position. | Task 13 review | Phase 7 | Adequate for Phase 2 triage; revisit if support ever needs to pinpoint torn output to a line. |
| P2 | `splitLabeledKey` doesn't handle backslash-escaped quotes in label values, and depends on the tokenizer's lucky-not-designed behavior for `}` inside quoted regions. | Task 13 review | Phase 3 | Phase 2 collector emits only `mount="/<path>"` and `iface="<name>"` — no quotes, no braces. Phase 3 collectors with richer labels will need a quote-aware splitter before this is exposed to bench/site sections. |
| P3 | Empty `Disks` / `Net` slices are accepted by `ServerFromSections`. | Task 13 review | Phase 7 polish | A real Linux host always has at least `/` mounted and one non-`lo` iface, so empty is itself a torn-read indicator. Cheap to add `len(m.Disks) >= 1` and `len(m.Net) >= 1` checks. Possibly better as a pipeline-layer policy than a parser-layer schema rule. |
| M1 | `tagEscaper`-vs-Influx full spec gap. | Task 14 review | Phase 7 | Already addresses backslash; remaining unhandled chars (e.g. literal newlines in tag values) aren't expected from our controlled inputs. Document any new tag source's escaping requirements before adopting. |
| M2 | No gzip request-body compression on the VM push. | Task 14 review | Phase 7 / scale | Phase 2 emits ~25 lines per server per pull cycle; well below any compression-relevant threshold. Add `Content-Encoding: gzip` + `gzip.Writer` once line counts approach kilobyte territory. |

### Collector pipeline

| # | Item | Source | Phase target | Notes |
|---|---|---|---|---|
| C1 | `ServerFromSections` returns `Disks` / `Net` in non-deterministic order (map iteration). Body bytes therefore vary across pulls. | Task 17 review | Phase 7 polish | Sort `m.Disks` by `Mount` and `m.Net` by `Name` inside `ServerFromSections`. Enables future golden-byte tests of the line-protocol body. |
| C2 | Pipeline success log lacks per-stage timings. | Task 17 review | Phase 7 polish | Add `ssh_ms` / `parse_ms` / `push_ms` to the "pull ok" log line. Useful when scheduler timeouts start firing in production. |
| C3 | Collector tests share a single in-memory SQLite DSN string. | Task 17 review | If/when `t.Parallel()` is added | Per-test DSN via `"file:pipeline-"+t.Name()+"?…"` to avoid cross-test row leakage if parallel testing is ever enabled. |
| C4 | Push errors and probe errors share `last_error` field on the server row. | Task 17 review | Phase 3 (UI) | Distinguish `last_push_error` from `last_error` if/when Phase 3's UI wants to surface push failures separately from server-reachability failures. |

### Phase 7 v2 (deferred from "finish v1")

| # | Item | Source | Phase target | Notes |
|---|---|---|---|---|
| V1 | Multi-tenant access scoping (per-team password / SSO / role labels on servers) | docs/2026-04-30/2.md | Phase 7 v2 | Single-tenant HTTP basic suffices for v1. v2 wants per-user auth + per-server visibility. |
| V2 | Deploy markers (annotation timeline alongside metric charts) | docs/2026-04-30/2.md | Phase 7 v2 | Needs a `deploy_events` table and POST endpoint for the bench's post-merge hook. |
| V3 | Per-server schedule overrides (faster cadence on hot prod, slower on staging) | docs/2026-04-30/2.md | Phase 7 v2 | Phase 6 ships one global cadence; per-server is small but not blocking. |
| V4 | Site-scoped log labels (Loki streams currently tagged only with bench/server) | docs/2026-04-30/2.md | Phase 7 v2 | Frappe writes per-site logs at `sites/<site>/logs/`; collector needs to discover and tail. |
| V5 | p95 / quantile_over_time on site detail (currently `avg_over_time(...[1h])`) | Phase 5 acceptance | Phase 7 v2 | quantile_over_time has higher VM cost; benchmark before flipping. |
| V6 | Search UI for benches + servers (Sites already has substring filter) | docs/2026-04-30/2.md | Phase 7 v2 | Low priority for ≤25 servers. |

### Tooling / smoke

| # | Item | Source | Phase target | Notes |
|---|---|---|---|---|
| T1 | `scripts/smoke.sh` CGO assertion silently passes if `readelf` is missing. | Task 10 review | Phase 7 polish | `readelf -d "$BINARY" 2>/dev/null` produces empty output on systems without binutils, so the `if !grep -q NEEDED` branch incorrectly returns success. Defensive fix: `command -v readelf` check + fallback to `file "$BINARY" \| grep -q 'statically linked'` or `! ldd "$BINARY" 2>&1 \| grep -q '=> /'`. |
| T2 | Smoke script's hardcoded `:18080` blocks parallel runs. | Task 10 review | Phase 7 polish | `PORT` env var already overrides — just needs a one-liner in README about parallel CI shards if that ever becomes routine. |
| T3 | "Bogus host" smoke step actually exercises `loadKey` failure (file-not-found), not a real dial-timeout path. | Task 10 review | Phase 7 polish | Sub-second probe; doesn't validate `DialTimeout` enforcement. If the smoke is ever switched to a real-but-unreachable hostname with a real key, the test will absorb up to `dial_timeout_seconds` per probe — adjust expected timing accordingly. |

## Done

_(empty — items move here with a commit ref when addressed)_

## How to use this file

- A reviewer who finds a non-blocking quality item adds a row in the relevant section, citing the review that surfaced it.
- A worker who clears an item moves the row to Done with a `Cleared in <commit-sha>` annotation.
- Items targeted at Phase 7+ stay open; this is the canonical list for the Phase 7 hardening pass.
- Don't put feature requests here. Features belong in the master plan or a phase plan.
