---
description: "Task list for Sentinel End-to-End Validation & Operations"
---

# Tasks: Sentinel End-to-End Validation & Operations

**Input**: Design documents from `/specs/003-integration-and-ops/`

**Prerequisites**: `001-remediate-and-harden` and `002-enhancements-observability`
fully landed (async ack-then-process, delivery-ID ledger, body cap, shadow mode,
Check Run, feedback log, structured logs, `/stats`, threshold sourcing). 003
**observes** those behaviors on the assembled stack; it adds **zero serving-path
change** — the gateway binary is pointed at local stubs by env config alone
(`GITHUB_API_BASE`, `MODEL_URL`).

**Tests**: 003 *is* tests + a runbook. Every user story is an automated check
against the real binary; every runbook scenario (FR-011) has a paired check so
docs cannot drift (constitution VIII).

**Organization**: Grouped by user story (spec.md P1–P4) for independent
implementation. US1 (bring-up) is the MVP the others build on.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1–US4)

## Path Conventions

Two-service repo at root. 003 adds ONE new Go tree — `test/integration/` (a
black-box driver: it `go build`s the real binary and runs it via `os/exec`,
never importing `internal/`) — plus one offline Python check under `ml/tests/`,
one `docs/RUNBOOK.md`, and one CI job. Nothing under `cmd/`, `internal/`,
`model/`, or `ml/` (except the new test) changes.

---

## Phase 1: Setup

**Purpose**: Confirm the 001+002 baseline and that the env-override wiring 003
depends on is actually present before writing a harness against it.

- [x] T001 Baseline check: `go build ./...`, `go vet ./...`, `go test ./... -race`
  green; confirm `internal/config/config.go` reads `GITHUB_API_BASE` (default
  `https://api.github.com`) and `MODEL_URL`, and that `cmd/sentinel/main.go`
  sources its threshold from the model `/healthz` when `CONFIDENCE_THRESHOLD` is
  unset. Record results. (No code change — this proves 003 needs none.)
  <!-- done 2026-09-24: baseline green (go1.26.5). config.go:39 GitHubAPIBase =
  envOr("GITHUB_API_BASE","https://api.github.com"); MODEL_URL required. main.go
  sources threshold from tr.FetchHealth when !cfg.ThresholdFromEnv (0.95 fallback).
  Zero serving-path change needed — 003 wires via env alone. -->


---

## Phase 2: Foundational (shared harness — BLOCKS all user stories)

**Purpose**: The reusable stubs, signer, fixtures, and process-boot helper every
scenario consumes. These are plain `.go` files (not `_test.go`) so both the CI
subset and the local full-stack run share them. **No scenario test can be written
until these exist.**

- [x] T002 [P] Create `test/integration/stub_model.go` (Entity: Model Stub): an
  `httptest.Server` implementing `GET /healthz` → `{status:"ok", threshold, artifact,
  version}` and `POST /predict` → a **configurable** `{is_slop, confidence, reason,
  version}` (default: high-confidence slop). Mirror the real contract in
  `model/app.py` exactly so the gateway's sourcing + triage paths run unchanged. No
  torch. Expose knobs for verdict, threshold, and injected 5xx/latency (drills).
  <!-- done 2026-09-24: stub_model.go. Default threshold 0.80 (distinct from 0.95
  fallback), high-confidence slop verdict (0.99, version "stub-model-1.0"). Knobs:
  SetVerdict/SetThreshold/InjectPredictStatus/InjectHealthStatus/InjectPredictDelay;
  accessors PredictCalls/HealthCalls/Threshold. Holds shared writeJSON helper. -->
- [x] T003 [P] Create `test/integration/stub_github.go` (Entity: GitHub Stub): a
  **recording** `httptest.Server` standing in for the GitHub REST API. Record every
  label-add (`POST issues/{n}/labels`), comment (`POST issues/{n}/comments`), Check
  Run (`POST check-runs`), and diff fetch (`GET pulls/{n}`) with method, path, and
  parsed body; return a canned diff for the fetch. Provide accessors (counts + last
  payload per call type) and a 5xx/failure injector per endpoint (drills). It records
  request **bytes**, never a method-call spy.
  <!-- done 2026-09-24: stub_github.go. Records recordedCall{Method,Path,Body} per
  request; path regexes mirror internal/github. cannedDiff const (non-secret, "+new").
  Accessors LabelCalls/CommentCalls/CheckRunCalls/DiffCalls/WriteCalls +
  LastLabelBody/LastCommentBody/LastCheckRunBody; per-endpoint Inject*Status. -->
- [x] T004 [P] Create `test/integration/sign.go` (Entity: Signed Delivery): an
  HMAC-SHA256 signer — `sign(secret, body) -> "sha256="+hex` via `crypto/hmac` +
  `crypto/sha256` — and helpers to build a signed request with `X-Hub-Signature-256`,
  `X-GitHub-Delivery`, and `X-GitHub-Event`. A `badSign` variant produces a mismatched
  signature (US2 401 drill).
  <!-- done 2026-09-24: sign.go. sign()/signedRequest()/badSignedRequest() — the bad
  variant signs append([]byte{'x'}, body...) to guarantee a mismatch, not a hex literal. -->
- [x] T005 [P] Create `test/integration/fixtures.go` (Entity: Signed Delivery Fixture):
  raw `pull_request` JSON payloads — `opened`/`synchronize` (slop PR, carries head SHA
  for the Check Run) and `unlabeled` (maintainer removes the slop label, for feedback)
  — as byte slices so the signature is over the exact bytes the gateway reads.
  <!-- done 2026-09-24: fixtures.go. []byte payloads (not structs). fx* identity consts
  incl. fxHeadSHA + slopLabel="needs-human-review"; openedPayload/synchronizePayload/
  unlabeledPayload/prIdentity. -->
- [x] T006 Create `test/integration/harness_test.go` (Entity: Integration Harness): a
  `TestMain`/helper that `go build`s `./cmd/sentinel` to a temp path, boots it via
  `os/exec` with env wiring `GITHUB_API_BASE`/`MODEL_URL` at the stubs (T002/T003),
  `GITHUB_WEBHOOK_SECRET`, `GITHUB_TOKEN` (dummy), `PORT` (ephemeral/configurable),
  and optional `SHADOW_MODE`/`FEEDBACK_LOG`. Poll `/healthz` until ready or a bounded
  timeout (clear message on port-in-use); provide a `waitFor(cond)` that polls a stub
  write or a `/stats` counter (**never a fixed `sleep`**, edge case); tear the process
  down cleanly. Runs identically on Windows + Linux (FR-012).
  <!-- done 2026-09-24: harness_test.go. TestMain go-builds ./cmd/sentinel once;
  boot(opts) starts on a free port (net.Listen :0), env-wires the stubs, waits /healthz.
  waitFor() polls to waitBudget (no fixed sleep). syncBuffer guards stderr for -race.
  exeSuffix()/repoRoot() give Windows+Linux parity. -->

**Checkpoint met (2026-09-24)**: `go build ./test/integration/...` compiles; `go vet`
clean; the harness boots the shipped binary against the stubs and both report healthy.

**Checkpoint**: `go build ./test/integration/...` compiles; the harness boots the
real binary against stubs and both report healthy. All scenarios can now be written.

---

## Phase 3: User Story 1 - Bring the stack up and prove it healthy (Priority: P1) — MVP

**Goal**: A deterministic yes/no that the assembled two processes talk and are wired.

**Independent Test**: Boot gateway + model stub; poll each health endpoint to ready
within budget; assert `/stats` counters zero, `/healthz` carries a build version, and
the gateway sourced its threshold from the model artifact `/healthz` over the network.

- [x] T007 [US1] `test/integration/bringup_test.go`: assert both services reach healthy
  within the startup budget and each `/healthz` carries its build version (FR-002,
  SC-001); assert a fresh `/stats` returns all-zero counters and needs no secret and
  exposes no PR content (spec US1 scenario 3).
  <!-- done 2026-09-24: TestBringUpHealthyAndZeroedStats. model.HealthCalls()>0,
  /healthz version non-empty, all five counters zero, no PR-content keys leaked. -->
- [x] T008 [US1] `test/integration/bringup_test.go`: boot with `CONFIDENCE_THRESHOLD`
  unset and the model stub advertising a distinct threshold on `/healthz`; assert the
  gateway's operating threshold was **sourced from the model artifact**, not the `0.95`
  fallback (FR-002, spec US1 scenario 2) — observed via the gateway's startup log line
  and/or the acted-on threshold. Confirm harness output leaks no secret/diff (FR-014).
  <!-- done 2026-09-24: TestBringUpThresholdSourcedFromArtifact. SetThreshold(0.73),
  asserts log has "threshold sourced from model artifact" + "0.73", not "using default".
  assertNoSecretsInLog helper (secret/token/diff) added here, shared by all scenarios. -->

**Checkpoint met (2026-09-24)**: `go test ./test/integration/ -run BringUp -race
-count=1` → `ok` (2.7s). The 001+002 foundational claim is now verified on the binary.

**Checkpoint**: `go test ./test/integration/ -run BringUp` green on Windows + Linux —
the foundational claim 001+002 never verified is now proven.

---

## Phase 4: User Story 2 - Golden end-to-end path, no live credentials (Priority: P2)

**Goal**: One signed slop delivery → exactly label + comment + Check Run, on real bytes.

**Independent Test**: Point the booted gateway at the recording GitHub stub + a slop
model stub; POST a correctly signed delivery; wait on completion signal; assert exactly
one label, one comment (confidence + version), one Check Run (`completed`/`neutral`),
immediate 2xx, idempotent redelivery, 401 on bad signature, 413 on oversized body.

- [x] T009 [US2] `test/integration/golden_test.go`: POST a correctly-signed slop
  `opened` delivery; assert the webhook response is 2xx and immediate (sub-second),
  then `waitFor` async completion and assert the GitHub stub recorded **exactly** one
  label add, one comment (text contains confidence + model/artifact version), and one
  Check Run (`completed`/`neutral`) on the PR head SHA (FR-003, SC-002).
  <!-- done 2026-09-24: TestGoldenPathExactlyThreeWrites. 200 in <900ms; waitFor the
  Check Run (last write in act()); exactly 1/1/1; label body=[slopLabel]; comment has
  "99%" + "stub-model-1.0"; check run head_sha=fxHeadSHA, completed/neutral. -->
- [x] T010 [US2] `test/integration/golden_test.go`: send the **same** `X-GitHub-Delivery`
  twice within the one process; assert the outward writes occur exactly once — idempotent
  redelivery (FR-004, SC-003).
  <!-- done 2026-09-24: TestGoldenPathIdempotentRedelivery. First delivery fully
  processed (waitFor Check Run); same ID again → 200; received stays 1; writes stay 1/1/1. -->
- [x] T011 [US2] `test/integration/golden_test.go`: POST a delivery whose signature does
  not match the raw body → assert 401 and **zero** GitHub/model calls at the stubs; POST
  a body over `MAX_BODY_BYTES` → assert 413 returned **before** any HMAC/parse work
  (FR-005, SC-004). Trust boundary, on the real binary.
  <!-- done 2026-09-24: TestGoldenPathTrustBoundary. MAX_BODY_BYTES=1024. Bad sig → 401,
  zero GitHub+model calls. 4096-byte body, correctly signed → 413 (size gate before HMAC). -->

**Checkpoint met (2026-09-24)**: `go test ./test/integration/ -run Golden -race
-count=1` → `ok` (3.3s). The headline promise is proven on real request bytes.

**Checkpoint**: The headline promise is proven end to end on real request bytes.

---

## Phase 5: User Story 3 - Safe-mode and feedback paths proven end to end (Priority: P3)

**Goal**: Shadow = full pipeline, zero writes; label-removal → one feedback row that the
offline pipeline folds back in as LEGIT.

**Independent Test**: Rerun the golden harness with `SHADOW_MODE=true` (model called,
flagged++, zero writes); separately deliver a signed `unlabeled` event and assert one
feedback row + zero GitHub writes; then run `ml/build_dataset.py --feedback-log` against
that row (stubbed GitHub) and confirm it enters the dataset as a LEGIT example.

- [ ] T012 [US3] `test/integration/safemode_test.go`: boot with `SHADOW_MODE=true`; POST
  a signed high-confidence-slop delivery; `waitFor` the flagged counter to advance and
  assert the model stub **was** called, yet the GitHub stub recorded **zero** label,
  comment, or Check Run writes (FR-006, SC-005).
- [ ] T013 [US3] `test/integration/safemode_test.go`: boot with `FEEDBACK_LOG` set to a
  temp file; POST a signed `unlabeled` delivery removing the slop label; assert exactly
  one well-formed row (PR identity, original verdict, disagreement type, timestamp) is
  appended off the request path and **zero** GitHub writes occur (FR-007, SC-006 first
  half). Assert the row carries no diff body (FR-014).
- [ ] T014 [P] [US3] Create `ml/tests/test_feedback_roundtrip.py` (Python stdlib +
  existing `ml/` modules, **no torch**): take a gateway-shaped feedback row (as T013
  writes) and drive `ml/build_dataset.py --feedback-log` with a **stubbed** GitHub
  (fake title+diff by PR identity); assert the PR enters the built dataset as a LEGIT
  row and the leakage assertion still passes (FR-008, SC-006 second half). Mirror the
  stdlib self-check style of `ml/tests/test_build_dataset.py`.

**Checkpoint**: The safety valve and the only production-learning path are proven on
the real process, and the offline fold-in round-trips.

---

## Phase 6: User Story 4 - Operational runbook and failure drills (Priority: P4)

**Goal**: A written runbook whose every scenario has a paired automated drill.

**Independent Test**: Execute each drill against the assembled stack and assert the
documented outcome; grep the runbook so every scenario names its paired check.

- [ ] T015 [US4] `test/integration/drills_test.go`: **fail-open drills** — for each of
  (model unreachable, model 5xx, GitHub 5xx through the retry budget) POST a signed slop
  delivery and assert a 2xx ack, **no PR mutation** at the GitHub stub, and the `failed`
  (or equivalent) counter advances within the time budget (FR-009, SC-007). Uses the
  T002/T003 injectors.
- [ ] T016 [US4] `test/integration/drills_test.go`: **missing-artifact drill** — start
  the model stub in an "absent artifact" mode (or the real model with no artifact) and
  assert it exits with a **single actionable line** naming the path/command, while the
  gateway still boots and fails open on a delivery (FR-010, SC-008). Also cover the
  Check-Run-failure-must-not-block-label/comment edge case (002 FR-014) via the stub's
  Check Run 5xx injector.
- [ ] T017 [US4] Write `docs/RUNBOOK.md` (Entity: Operational Runbook): bring-up steps,
  how to read `/healthz` + `/stats`, and each failure drill (model down, model 5xx,
  GitHub 5xx, missing artifact, oversized body → 413, bad signature → 401) with its
  **expected behavior** and the **name of its paired check** in `drills_test.go` /
  `golden_test.go` (FR-011, SC-009). Name the process-local dedup **cross-restart
  caveat** honestly (redelivery after a gateway restart is not deduped) rather than
  overclaiming (edge case + Assumptions).
- [ ] T018 [US4] Add a "docs match code" assertion: a small check (in
  `drills_test.go` or a `TestRunbookScenariosHaveChecks`) that greps `docs/RUNBOOK.md`
  for each scenario's paired-check name and fails if a documented behavior has no
  corresponding test function (FR-011, constitution VIII) — the machine half that keeps
  the runbook from drifting.

**Checkpoint**: An on-call human has one verified page; the docs cannot lie about the code.

---

## Phase 7: Polish & Cross-Cutting

- [ ] T019 Edit `.github/workflows/ci.yml`: add a distinct `integration` job — checkout,
  setup-go 1.26, `go test ./test/integration/...` running the **stub-model subset** (no
  torch, no multi-GB serving stack, no outbound internet), adding **no** third-party Go
  dependency (FR-012, FR-013, SC-010). Runs on the Linux CI runner; the same tests also
  pass on the Windows dev host.
- [ ] T020 [P] Edit `README.md`: link `docs/RUNBOOK.md`; document `go test
  ./test/integration/...` (local full-stack + CI subset) and the new CI integration job
  under Running Tests.
- [ ] T021 Final sweep: `go build ./...`, `go vet ./...`, `go test ./... -race`
  (including `./test/integration/...`), `python ml/tests/test_feedback_roundtrip.py`,
  and a grep of harness + drill output confirming **zero** matches for the diff body,
  GitHub token, or webhook secret (FR-014, SC-009). Confirm no third-party Go module was
  added (`go.mod` unchanged). Record outcomes.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: none — start immediately.
- **Foundational (Phase 2)**: after Phase 1. **Hard blocker** — the stubs (T002/T003),
  signer (T004), fixtures (T005), and harness (T006) are consumed by every scenario. No
  `_test.go` scenario compiles without them.
- **User Stories**: US1 is the MVP (proves the stack talks). US2 depends on the full
  foundational set. US3 reuses US2's harness (adds `SHADOW_MODE`/`FEEDBACK_LOG` env) plus
  the independent Python check (T014). US4 reuses the stubs' injectors + fixtures.
- **Polish (Phase 7)**: after the desired scenarios exist (the CI job runs them).

### User Story Dependencies

- **US1 (P1)**: Foundational only. MVP — validate here first.
- **US2 (P2)**: Foundational (recording stub + signer + fixtures + harness).
- **US3 (P3)**: US2's harness (T012/T013) + independent Python round-trip (T014).
- **US4 (P4)**: Foundational injectors (T015/T016); runbook (T017) + drift check (T018).

### Parallel Opportunities

- **Phase 2**: T002 ∥ T003 ∥ T004 ∥ T005 (four independent files); T006 after they exist.
- **Across stories**: T014 (Python offline check) ∥ the entire Go track — different
  language, different files, no shared state.
- **Phase 7**: T020 (README) ∥ T019 (CI); T021 last.

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. T001 baseline → 2. T002–T006 (foundational harness) → 3. T007–T008 (US1) → STOP and
   VALIDATE: the assembled stack provably boots, talks, and sources its threshold over
   the network. Everything past this asserts richer behavior on that same booted stack.

### Incremental Delivery

1. US1 → stack proven alive & wired  2. US2 → golden path on real bytes  3. US3 → shadow
   + feedback round-trip  4. US4 → runbook + drills  5. Polish → CI job + README + sweep.

### Parallel Team Strategy

- Dev A: T002/T003 (stubs) → US2 golden path → US4 drills (the recording/injection track).
- Dev B: T004/T005 (signer + fixtures) → US1 bring-up → US3 shadow/feedback harness.
- Dev C: T014 Python round-trip ∥ from the start, then T017 runbook + T018 drift check.
- T006 (harness boot) is the join point; land it early so A/B/C can write scenarios.

---

## Notes

- **Zero serving-path change (FR-013)**: 003 touches no code under `cmd/`, `internal/`,
  `model/`, or `ml/` (beyond the new `ml/tests/` check). If any scenario seems to *need*
  a serving-path edit, that is a real 001/002 gap — stop and raise it, do not patch the
  binary to make a test pass. The harness observes what ships.
- **Zero third-party Go deps (FR-013, constitution V)**: harness is stdlib only —
  `net/http`, `net/http/httptest`, `os/exec`, `crypto/hmac`, `crypto/sha256`, `testing`,
  `encoding/json`. `go.mod` MUST stay dependency-free; the CI job adds no module.
- **Log hygiene in the harness itself (FR-014, 002 FR-001)**: the drills and stubs must
  never log/record the diff body, token, or secret — the same invariant the gateway
  holds, held by its test.
- **Determinism (edge case)**: always `waitFor` an observable signal (a stub write
  landing, a `/stats` counter advancing), never a fixed `sleep`, so the suite is not
  flaky under CI load.
- **Process-local dedup ceiling**: drills assert idempotency **within one process**;
  the cross-restart caveat is documented in the runbook (T017), not pretended away.
- Commit after each phase or logical group; run `go test ./test/integration/...` before
  each commit. Commit only when explicitly asked.
