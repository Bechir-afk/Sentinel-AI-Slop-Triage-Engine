# Implementation Plan: Sentinel End-to-End Validation & Operations

**Branch**: `003-integration-and-ops` | **Date**: 2026-09-24 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `/specs/003-integration-and-ops/spec.md`

## Summary

001+002 verified every *part* of Sentinel with `net/http/httptest` fakes and
`docker compose config`, but never ran the *assembled* system: the real gateway
binary talking over real HTTP to a real (or stubbed) model, driven by a
genuinely HMAC-signed delivery, all the way to the outward writes. 003 closes
that gap with **zero serving-path change**. An integration harness (Go stdlib
`os/exec` + `net/http` + `net/http/httptest` + `testing`) boots the shipped
`sentinel` binary, points its `GITHUB_API_BASE` and `MODEL_URL` at local
recording/response stubs via env, signs deliveries with `crypto/hmac`, and
asserts on the **real request bytes** the gateway sent. A Python-stdlib offline
check proves the `ml/build_dataset.py --feedback-log` round trip. A written
runbook documents bring-up + each failure drill, and every documented behavior
has a paired automated check so docs cannot drift (constitution VIII). A CI
integration job runs the stub-model subset with no torch and no third-party Go
dependency.

## Technical Context

**Language/Version**: Go 1.26 (harness + stubs, matching `go.mod module sentinel`);
Python 3.11 (offline feedback round-trip check).

**Primary Dependencies**: **None new.** Harness is Go stdlib only —
`os/exec` (boot the real binary), `net/http` + `net/http/httptest` (stubs +
signed POSTs), `crypto/hmac`+`crypto/sha256` (sign deliveries), `testing`.
Offline check is Python stdlib + the existing `ml/` modules (no torch/transformers).

**Storage**: N/A for the harness. It exercises the one existing persistence
(the append-only `FEEDBACK_LOG` JSONL) via a temp file; no new store.

**Testing**: `go test ./test/integration/...` boots the real binary over real
HTTP against local stubs; `python ml/tests/test_feedback_roundtrip.py` for the
offline fold-in. Waits on observable completion signals (a stub write landing or
a `/stats` counter advancing), never a fixed `sleep`.

**Target Platform**: Windows developer host **and** Linux CI runner (the repo
already targets both). Harness binds ephemeral/configurable ports and boots the
binary via `os/exec`, both cross-platform.

**Project Type**: Two-service repo (Go gateway + Python model/ML), unchanged.
003 adds a top-level `test/integration/` tree and one runbook doc — nothing else.

**Performance Goals**: Bring-up returns a deterministic healthy/unhealthy result
within a bounded startup budget (health-poll with timeout). Golden path asserts
the webhook ack is immediate (sub-second) and async completion is observed on a
signal, not a timer.

**Constraints**: No live GitHub token, no live repository, no outbound internet
(all GitHub + CI-model interactions are local stubs). Log-hygiene invariant
(002 FR-001) holds in the harness's *own* output too: it never logs/records the
diff body, GitHub token, or webhook secret. The gateway binary is observed, not
modified — 003 needs only env config (`GITHUB_API_BASE`, `MODEL_URL`,
`SHADOW_MODE`, `FEEDBACK_LOG`, `PORT`, `GITHUB_WEBHOOK_SECRET`, `GITHUB_TOKEN`,
`WORKER_COUNT`), all already present in `internal/config/config.go`.

**Scale/Scope**: ~6–8 integration test files under `test/integration/`, two
reusable stubs (GitHub recorder, model responder), one HMAC signer + delivery
fixtures, one offline Python check, one runbook, one CI job. Dedup is
process-local (a 002 known ceiling) — drills assert within a single process and
the runbook names the cross-restart caveat rather than pretending otherwise.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

003 is pure verification-and-documentation on top of the shipped system; it
touches no serving code. Each principle is satisfied by construction:

| Principle | Status | How 003 complies |
|---|---|---|
| I. Never Auto-Close | ✅ PASS | Harness only *observes*; it never closes/merges. Drills assert the gateway performs exactly label + comment + Check Run and no more. |
| II. Fail-Open | ✅ PASS | US4/FR-009–010 drills *prove* fail-open on model-down, model-5xx, GitHub-5xx, missing-artifact: 2xx ack, no PR mutation, correct counter movement. |
| III. Precision Over Recall | ✅ PASS | Untouched. FR-008 asserts the feedback round trip keeps the leakage invariant; no evaluation change. |
| IV. Classify Value Not Provenance | ✅ PASS | No classifier change; stubs return fixed verdicts. |
| V. Zero Third-Party Go Deps | ✅ PASS | FR-013: harness is stdlib-only (`net/http`, `net/http/httptest`, `os/exec`, `crypto/hmac`, `testing`). CI job adds no Go module. |
| VI. Self-Hosted Model | ✅ PASS | Model is the real artifact (local full-stack run) or a local stub (CI). No external AI API. |
| VII. Trust Boundary Verification | ✅ PASS | FR-005 drills prove 401 on signature mismatch (zero downstream calls) and 413 on oversized body *before* HMAC — on the real binary. |
| VIII. Docs Match Shipped Code | ✅ PASS | FR-011: every runbook scenario has a paired automated check; the runbook cannot claim a behavior the harness doesn't prove. |

**Quality Gates**: `go build/vet/test` stay green (harness is additive test
code). `ml/evaluate.py` unaffected. Missing-artifact non-crash-loop behavior is
now *drilled* (FR-010), strengthening the existing gate.

**Gate result: PASS — no violations, no serving-path change, no new dependency.**

## Project Structure

### Documentation (this feature)

```text
specs/003-integration-and-ops/
├── spec.md              # Feature spec (already authored)
├── plan.md              # This file
└── tasks.md             # Task breakdown (/speckit-tasks output)
```

> Phase 0/1 note: the spec already carries the resolved research decisions
> (Assumptions §: env-overridable `GITHUB_API_BASE`/`MODEL_URL` ⇒ zero
> serving-path change), the data model (Key Entities §: Integration Harness,
> GitHub Stub, Model Stub, Signed Delivery Fixture, Runbook, Drill Result), the
> interface contracts (the GitHub REST calls in `internal/github/github.go` and
> the model `/predict` + `/healthz` contract the stubs mirror), and the
> quickstart (the runbook + `go test ./test/integration/...`). Splitting those
> into separate research.md/data-model.md/contracts/quickstart.md files would
> duplicate the spec verbatim, so they are folded here rather than re-emitted
> (ponytail: fewest files; no boilerplate nobody asked for).

### Source Code (repository root)

```text
test/integration/                 # NEW — the only Go addition; test-only, never imported by the binary
├── harness_test.go               # boot real ./cmd/sentinel via os/exec; env wiring; health-poll; teardown (US1)
├── stub_github.go                # recording httptest server: records label/comment/check-run; injects 5xx (Entity: GitHub Stub)
├── stub_model.go                 # httptest server: /healthz {threshold,artifact,version} + /predict fixed verdict (Entity: Model Stub)
├── sign.go                       # HMAC-SHA256 signer + X-GitHub-Delivery / X-Hub-Signature-256 helpers (Entity: Signed Delivery)
├── fixtures.go                   # raw pull_request payloads: opened/synchronize + unlabeled (Entity: Signed Delivery Fixture)
├── bringup_test.go               # US1: both healthy in budget, threshold sourced from model artifact, /stats zeroed
├── golden_test.go                # US2: signed slop → exactly 1 label + 1 comment(conf+ver) + 1 Check Run(neutral); idempotent redelivery; 401/413
├── safemode_test.go              # US3: shadow → full pipeline, zero writes, flagged++; unlabeled → 1 feedback row, no GitHub write
└── drills_test.go                # US4: model-down / model-5xx / GitHub-5xx fail-open; missing-artifact; each paired to a runbook scenario

ml/tests/
└── test_feedback_roundtrip.py    # NEW — US3/FR-008: a gateway-written feedback row folds in as a LEGIT example, leakage assertion still passes

docs/
└── RUNBOOK.md                    # NEW — US4/FR-011: bring-up, health/stats reading, each failure drill + its paired check name

.github/workflows/ci.yml          # EDIT — add an `integration` job: stub-model subset, no torch, no new Go dep (FR-012)
README.md                         # EDIT — link the runbook; note `go test ./test/integration/...` and the CI integration job
```

**Structure Decision**: A new top-level `test/integration/` package holds the
harness. No `test/` dir exists yet; the existing unit tests live beside their
code in `internal/*/`, and those stay put. The integration tree is separate
because it is a *black-box* driver — it compiles the real binary with `go build`
and runs it with `os/exec`, rather than importing `internal/` packages, so it
observes exactly what ships. Stubs and the signer are plain `.go` files (not
`_test.go`) so both the CI subset and the local full-stack run can share them;
the scenario files are `_test.go`. The offline check joins the existing
`ml/tests/` self-checks. The runbook lives in `docs/` and is the human-readable
half of FR-011, its machine half being `drills_test.go`.

## Complexity Tracking

> No constitution violations. 003 adds no serving-path code, no Go dependency,
> and no new persistence. Nothing to justify.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| — | — | — |
