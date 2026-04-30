# Frappe Monitor — Operator Guide

Everything you need to deploy, run, and operate `frappe-monitor` in production.

These docs are **evergreen** — kept current with the code. Dated decisions and design discussions live in `docs/YYYY-MM-DD/N.md`; long-term operator-facing reference lives here.

## Where to start

| You want to… | Read |
|---|---|
| Try it on a Mac/Linux laptop in 5 minutes | [`local-test.md`](local-test.md) |
| Deploy on a fresh server, end-to-end | [`deployment.md`](deployment.md) |
| Add a bench server and use the dashboard | [`usage.md`](usage.md) |
| Start, stop, upgrade, see logs, take backups | [`operations.md`](operations.md) |
| Tune the YAML config, env-var overrides | [`configuration.md`](configuration.md) |
| Hit the HTTP API directly | [`api.md`](api.md) |
| Understand what runs where + why | [`architecture.md`](architecture.md) |
| Diagnose something broken | [`troubleshooting.md`](troubleshooting.md) |
| Contribute or extend the project | [`contributing.md`](contributing.md) |

## What's in the box (current state)

| Phase | Scope | Status |
|---|---|---|
| 1 | HTTP foundation: server CRUD + SSH probe | done |
| 2 | Per-server pull → metrics in VictoriaMetrics | done |
| 3 | Bench + site collectors → metrics + logs (Loki) | done |
| 4 | Dashboard foundation: SPA, query proxies, timeline filter | done |
| 5 | Dashboard full coverage: benches, sites, log feeds | done |
| 6 | Telegram alerting (rules engine + state + fan-out) | done |
| 7 v1 | HTTP basic auth, in-dashboard add/edit/delete | done |
| 7 v2 | Deploy markers, multi-tenant scoping, search UI | post-v1 backlog |

The single-tenant production-shippable v1 is complete. v2 items (multi-tenant access, deploy timeline annotations) are tracked in `docs/hardening-backlog.md`.

## Convention

When you add or change a feature, **update the relevant doc in this folder in the same commit**. The dated `docs/YYYY-MM-DD/N.md` files capture the *decision*; the files here capture *how to operate it*. If the operator-facing surface changes, the user-facing doc has to change with it.
