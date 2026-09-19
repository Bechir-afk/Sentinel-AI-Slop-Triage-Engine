# Implementation Plan: Sentinel Remediation and Hardening

**Branch**: `001-remediate-and-harden` | **Date**: 2026-09-07 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `/specs/001-remediate-and-harden/spec.md`

## Summary

Make Sentinel deployable and trustworthy in five slices: (1) a deployability fix
so `docker compose up` either reaches healthy with an artifact or reports one
actionable error without it; (2) webhook ingestion rebuilt around
acknowledge-then-process — respond in <1s, work in a background goroutine with
a detached context, gated on event type, deduplicated by delivery ID, bot
authors skipped, body capped; (3) an honest ML pipeline — leakage-free
synthetic splits and a validation-selected confidence threshold; (4) container
hardening (non-root, cap-drop, read-only rootfs, CPU-only torch); (5) truthful
docs plus CI. Every change preserves the constitution: never auto-close,
fail-open, precision ≥ 0.85 on leakage-free test data, zero third-party Go
dependencies, self-hosted model, HMAC verification first.

## Technical Context

**Language/Version**: Go 1.26.5 (gateway, stdlib-only) + Python 3.11 (model
service and ml/ pipeline, containerized).

**Primary Dependencies**: Gateway: none (constitution V). Model service:
FastAPI, Uvicorn, PyTorch CPU-only wheels, transformers. ml/: pandas,
pyarrow, scikit-learn, datasets, transformers, torch.

**Storage**: N/A in the serving path (constitution: no database). Idempotency
is an in-memory bounded map. Offline artifacts: parquet splits under
`ml/dataset/`, trained artifact under `model/model/`.

**Testing**: Go: `go test ./...` (stdlib `testing` + `httptest`), all packages.
Python: none today; this plan adds a tiny artifact-presence check as a runnable
guard, not a framework.

**Target Platform**: Linux containers (Docker Compose); distroless static:nonroot
(gateway), python:3.11-slim non-root (model).

**Project Type**: Two containerized web services + offline ML pipeline.

**Performance Goals**: Webhook ack p99 < 1s (hard ceiling: GitHub's 10s
delivery timeout); triage latency budget 45s worst case (15s diff fetch + 30s
model call) entirely off the request path.

**Constraints**: Zero third-party Go modules; no external AI API; model service
reachable only on the compose network; label + comment only — never auto-close.

**Scale/Scope**: Single-node, minutes-apart PR arrivals; ~2–4k training
examples. No persistence, no horizontal scale (YAGNI, documented ceiling).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| # | Principle | Status | How the plan honors it |
|---|-----------|--------|------------------------|
| I | Never Auto-Close | PASS | No task touches close/merge/dismiss; action set remains {label, comment}. |
| II | Fail-Open | PASS | FR-007 keeps fail-open at every stage; async processing only moves *when* the failure is absorbed, not whether a 2xx is returned (ack happens before triage). |
| III | Precision Over Recall | PASS, strengthened | FR-009/010/012 fix the leakage and threshold selection that currently make the 0.85 gate meaningless. |
| IV | Value Not Provenance | PASS | No change to the classification target; bot-author skip (FR-005) is provenance-neutral (skips automation, does not score humans). |
| V | Zero Third-Party Go Deps | PASS | Async = stdlib goroutines + `context.Background()`; dedup = stdlib `sync.Map`/mutex map; queue brokers explicitly rejected. |
| VI | Self-Hosted Model | PASS | Serving path unchanged: gateway → local model over compose-internal HTTP. |
| VII | Trust Boundary Verification | PASS, strengthened | HMAC stays first; body cap (FR-006) and event-type gate (FR-003) added at the same boundary. |
| VIII | Docs Match Code | PASS | FR-016 makes README/AGENTS.md describe the shipped architecture. |

**GATE RESULT: PASS.** No violations; Complexity Tracking table not needed.

## Project Structure

### Documentation (this feature)

```text
specs/001-remediate-and-harden/
├── plan.md              # This file
├── research.md          # Phase 0 output — decisions and rationale
├── data-model.md        # Phase 1 output — entities
├── quickstart.md        # Phase 1 output — end-to-end validation guide
├── contracts/           # Phase 1 output — /predict + webhook endpoint contracts
│   ├── webhook-endpoint.md
│   └── predict-endpoint.md
└── tasks.md             # Phase 2 output (/speckit-tasks)
```

### Source Code (repository root)

```text
cmd/sentinel/main.go          # wire processor, body cap, graceful shutdown
internal/config/config.go     # + MAX_BODY_BYTES env; remove nothing
internal/verify/verify.go     # http.MaxBytesReader before HMAC read
internal/webhook/webhook.go   # ack-then-process, event gate, dedup ledger, bot skip
internal/webhook/dedupe.go    # bounded in-memory delivery-ID ledger (+ test)
internal/github/github.go     # unchanged contract; optional retry on 5xx
internal/triage/triage.go     # threshold moves to config-with-artifact-default
model/app.py                  # artifact presence check, clear startup error
model/inference.py            # lazy/global load kept, guarded
model/Dockerfile              # --index-url CPU wheels, non-root user
ml/build_dataset.py           # per-split generators (disjoint seeds)
ml/train.py                   # compute_metrics, seed, threshold selection
ml/evaluate.py                # gate at selected threshold on leakage-free test
deploy/Dockerfile             # (minor) drop redundant COPY
docker-compose.yml            # gateway healthcheck, hardening block, limits
.github/workflows/ci.yml      # build + vet + test on push
.env.example                  # every required var
README.md, AGENTS.md          # rewritten to shipped architecture
```

**Structure Decision**: Existing two-service layout is kept — this feature
remediates it rather than restructuring. Only new files: `internal/webhook/dedupe.go`
(+ test), `.github/workflows/ci.yml`, `.env.example`, spec artifacts.

## Complexity Tracking

> Not applicable — Constitution Check passed with no violations to justify.
