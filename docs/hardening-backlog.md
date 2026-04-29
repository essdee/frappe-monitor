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

### Storage / runtime

| # | Item | Source | Phase target | Notes |
|---|---|---|---|---|
| R1 | `client.Schema.Create(ctx)` runs on every store open. | Task 4 review | Phase 7+ | Switch to versioned migrations (e.g. ent's `migrate.Diff` workflow) once schema mutations become regular events. |

## Done

_(empty — items move here with a commit ref when addressed)_

## How to use this file

- A reviewer who finds a non-blocking quality item adds a row in the relevant section, citing the review that surfaced it.
- A worker who clears an item moves the row to Done with a `Cleared in <commit-sha>` annotation.
- Items targeted at Phase 7+ stay open; this is the canonical list for the Phase 7 hardening pass.
- Don't put feature requests here. Features belong in the master plan or a phase plan.
