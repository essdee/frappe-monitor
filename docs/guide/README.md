# Frappe Monitor — Operator Guide

Everything you need to deploy, run, and operate `frappe-monitor` in production.

These docs are **evergreen** — kept current with the code. Dated decisions and design discussions live in `docs/YYYY-MM-DD/N.md`; long-term operator-facing reference lives here.

## Where to start

| You want to… | Read |
|---|---|
| Try it on a Mac/Linux laptop in 5 minutes | [`local-test.md`](local-test.md) |
| Production setup between two laptops (real Frappe bench) | [`two-laptop-setup.md`](two-laptop-setup.md) |
| Deploy on a fresh server, end-to-end | [`deployment.md`](deployment.md) |
| Add a bench server and use the dashboard | [`usage.md`](usage.md) |
| Start, stop, upgrade, see logs, take backups | [`operations.md`](operations.md) |
| Tune YAML-only runtime settings | [`configuration.md`](configuration.md) |
| Hit the HTTP API directly | [`api.md`](api.md) |
| Understand what runs where + why | [`architecture.md`](architecture.md) |
| Diagnose something broken | [`troubleshooting.md`](troubleshooting.md) |
| Contribute or extend the project | [`contributing.md`](contributing.md) |

## What's in the box (current state)

| Phase | Scope | Status |
|---|---|---|
| 1 | HTTP foundation: server CRUD + SSH probe | done |
| 2 | Per-server pull → metrics in VictoriaMetrics | done |
| 3 | Bench + site collector → VictoriaMetrics | done |
| 4 | Dashboard foundation: SPA, query proxies, timeline filter | done |
| 5 | Monitoring UI: fleet, servers, benches, sites, services, workers | done |
| 6 | Telegram alerting + durable incident history | done |
| 7 v1 | Cookie login + API basic auth | done |
| 8 | Agentless auto-discovered log streaming + global Log Explorer | done |
| 9 | Read-only SSH boundary; remove remote operation surface | done |
| v2 | Multi-tenant scoping | backlog |

The single-tenant monitoring product is complete. Multi-tenant access and deeper optional request instrumentation are tracked in `docs/hardening-backlog.md`.

## Convention

When you add or change a feature, **update the relevant doc in this folder in the same commit**. The dated `docs/YYYY-MM-DD/N.md` files capture the *decision*; the files here capture *how to operate it*. If the operator-facing surface changes, the user-facing doc has to change with it.
