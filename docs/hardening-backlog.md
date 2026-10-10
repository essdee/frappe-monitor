# Hardening Backlog

This file records known, non-blocking risks after the 2026-08-13 full-stack review. It is intentionally limited to work that remains; completed historical phase notes live under `docs/YYYY-MM-DD/`.

## Open engineering risks

| ID | Risk / limitation | Why it remains | Recommended next step |
|---|---|---|---|
| R1 | Ent auto-migration runs at every startup. | Safe for the current additive schema, but there is no explicit migration version/rollback ledger. | Introduce versioned migrations before the next destructive or data-transforming schema change. |
| R2 | Log batches are durable only after Loki push plus SQLite cursor commit. | Bounded SSH backpressure prevents RAM growth, but a simultaneous Loki outage, monitor crash, and remote rotation can remove uncommitted source bytes. | Add a disk-backed local spool if this failure combination must have zero-loss guarantees. |
| R3 | Partial Telegram fan-out progress is process-local. | Failed chats retry without duplicating successful chats while the process lives; a restart between partial and complete delivery can duplicate a message to an already-successful chat. | Persist per-chat delivery receipts keyed by notification fingerprint. |
| R4 | Backend credentials are not modeled. | VM/Loki URLs support normal HTTP(S), but there are no dedicated bearer/basic-auth or custom-CA config fields. | Add backend transport credentials before using a protected remote metrics tier. |
| R6 | Config reload requires restart. | Scheduler, alert routes, SSH trust, and stream sessions are wired once at boot. | Add transactional reload only if restart-based operations become disruptive. |
| R7 | No automated real-SSH integration job. | Unit and bash integration tests cover protocol, host-key classification, cancellation, parsing, and rotation; they do not exercise a live sshd in CI. | Add a disposable sshd container test with pinned host keys and forced disconnects. |

## Product scope still deferred

| ID | Item | Notes |
|---|---|---|
| V1 | Multi-tenant users, roles, and per-server visibility | Current authentication is one shared password. |
| V2 | Deploy-event timeline annotations | Requires a deploy-event schema and authenticated write endpoint. |
| V3 | Per-server collection cadence | Current cadence is global; concurrency and overlap are bounded. |
| V4 | Dashboard editing of alert rules/routes | Rules and Telegram targets are restart-applied YAML. |

## Resolved in the 2026-08-13 review

- Strict `known_hosts` verification, cancellable TCP/SSH handshakes, key caching, IPv6-safe addresses, and per-host session limits.
- Bounded/strict JSON input, canonical server validation, trusted-proxy parsing, login throttling, signed cookie auth, and safe HTTP server timeouts.
- Unique canonical server labels, legacy duplicate-name repair, SQLite WAL read concurrency, and server-edge cascades.
- Immediate collection, no per-server overlap, bounded site-probe concurrency/budget, explicit unknown health, and cadence-aware query lookback.
- Quote-aware parser/tokenizer behavior, deterministic disk/network output, collector label escaping, and bounded remote command output.
- Versioned absolute-offset streaming with rotation detection, retry-safe cursor commits, hard buffer bounds, reconnect jitter/reset, hot lifecycle handling, and per-server file selection.
- Site alert detail enrichment with constant query count, delivery-success cooldown semantics, recovery retry, positive-evidence recovery gates, managed-server filtering, and safe multi-chat retry behavior.
- Frontend request coalescing, stale-response guards, centralized auth/error handling, auth-disabled mode handling, safe PromQL/LogQL construction, and current dependency/security updates.
- Repository CI enforces shell and Compose validation, frontend tests/build,
  Go race tests and vet, and an embedded release-binary build on every push and
  pull request.

## Review rule

Add an item only when it is a concrete remaining risk with an observable failure mode. When it is fixed, remove it from the open table and summarize the class of fix in the resolved section; dated design files are not an operational backlog.
