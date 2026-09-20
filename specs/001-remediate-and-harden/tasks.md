---
description: "Task list for Sentinel Remediation and Hardening"
---

# Tasks: Sentinel Remediation and Hardening

**Input**: Design documents from `/specs/001-remediate-and-harden/`

**Prerequisites**: plan.md, spec.md, research.md (R1–R10), data-model.md, contracts/, quickstart.md (V1–V8)

**Tests**: Included for every serving-path change — the constitution (Quality
Gates) requires success-path AND fail-open tests for serving-path changes, and
SC-002..SC-005 are test-verified outcomes.

**Organization**: Grouped by user story (spec.md P1–P5) for independent implementation.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1–US5)

## Path Conventions

Two-service repo at root: Go gateway (`cmd/`, `internal/`, `deploy/`), model
service (`model/`), offline ML (`ml/`), compose + CI at root.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Verified baseline before any change.

- [x] T001 Baseline check: run `go build ./...`, `go vet ./...`, `go test ./...`, and `docker compose config` from repo root; record results (all must be green before starting)
  - **Result (2026-09-19)**: `go build ./...` OK · `go vet ./...` OK · `go test ./...` all green (`github`, `triage`, `verify`, `webhook` pass; `cmd/sentinel` + `internal/config` have no test files) · `docker compose config` structurally valid (the failure without `.env` is the intended `${VAR:?}` guard — validated with dummy env). Go 1.26.5, Docker 29.6.1. Baseline green.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Config surface + dedup ledger that US2 builds on.

- [x] T002 [P] Extend `internal/config/config.go` with `MAX_BODY_BYTES` (default 26214400, min 1 KiB) and `WORKER_COUNT` (default 8, range 1–64) parsing + validation; add `internal/config/config_test.go` (package currently has no tests)
  - **Done (2026-09-19)**: added `MaxBodyBytes int64` (default `25<<20`, rejects `<1024`) and `WorkerCount int` (default 8, rejects outside 1–64) to `config.go`. New `config_test.go` covers defaults, each missing required var, and valid/invalid/out-of-range cases for `CONFIDENCE_THRESHOLD`, `MAX_BODY_BYTES`, `WORKER_COUNT`. `internal/config` went from no tests → passing.
- [x] T003 [P] Create `internal/webhook/dedupe.go`: bounded delivery-ID ledger (map + mutex, 1024 entries, oldest-eviction) with atomic `Claim(id) bool`; add `internal/webhook/dedupe_test.go` (claim-once, concurrent claims, eviction)
  - **Done (2026-09-19)**: `dedupe.go` — `Ledger` (map + ring, mutex-guarded) with `NewLedger(capacity)` and `Claim(id) bool` (oldest-first eviction; process-local, `ponytail:` note on the restart ceiling). `dedupe_test.go` covers claim-once, eviction, 100-goroutine concurrent race (`-race`, exactly one winner), and zero-capacity clamp. Wired into the handler in T009.

**Checkpoint**: Config + ledger compile and pass tests; user stories can proceed.

---

## Phase 3: User Story 1 - System runs locally (Priority: P1) — MVP

**Goal**: `docker compose up` either reaches healthy (artifact present) or reports one actionable error (artifact absent); gateway starts independently of model health.

**Independent Test**: quickstart.md V1 + V2.

- [x] T004 [US1] `model/app.py` + `model/inference.py`: at startup, before loading, verify artifact directory exists and contains expected files (`config.json`, tokenizer files); if missing, log ONE actionable line naming the path and the producing command (`ml/train.py`, see README §Training) and `sys.exit(1)` — no HF stack trace (FR-008, research R7)
  - **Done (2026-09-19)**: added `_verify_artifact(MODEL_PATH)` to `model/inference.py`, called before the `from_pretrained` loads. Checks the dir exists and holds `config.json` + a weights file (`model.safetensors`|`pytorch_model.bin`) + a tokenizer file; on failure logs one line with the path and `python ml/train.py … --out model/model` and `sys.exit(1)`. Runs at model-service startup via `app.py`'s `import inference`, so `app.py` needed no change. Verified: `py_compile` clean; guard logic unit-checked (missing dir / empty dir / complete artifact) without needing torch installed.
- [x] T005 [US1] `docker-compose.yml`: change gateway `depends_on` for `sentinel-model` from `condition: service_healthy` to `condition: service_started` (gateway must run and fail open when the model is absent/down — FR-007); set model `restart` policy so a missing artifact stops the container after the one error message rather than looping
  - **Done (2026-09-19)**: gateway `depends_on.sentinel-model.condition` → `service_started` (gateway no longer blocks on model health; fails open per FR-007). Model `restart` → `"no"` so the T004 `exit(1)` on a missing artifact stops with its one message instead of crash-looping (FR-008). Chose `"no"` over `on-failure:N` because `docker compose up` does not honor a max-retry count, so bounded on-failure isn't portable; documented the transient-crash ceiling + orchestrator upgrade path in a `ponytail:` comment. `docker compose config` validates (condition=service_started, model restart="no").
- [x] T006 [US1] `docker-compose.yml`: add gateway `healthcheck` (wget/curl-less: the distroless image has no shell — use the binary itself: add a `-healthcheck` flag or dedicated `/healthz` probe per deploy constraints, simplest: compose `test: ["CMD", "/sentinel", "-healthz"]` with a tiny flag in `cmd/sentinel/main.go` that dials `localhost:$PORT/healthz`) (FR-015)
  - **Done (2026-09-19)**: added a `-healthz` flag to `cmd/sentinel/main.go` (`healthCheck()` dials `http://127.0.0.1:$PORT/healthz`, 3s timeout, exit 0 on 200 else 1; reads PORT directly, no `config.Load`, so the probe is dependency-light). Added a gateway `healthcheck` to compose using `test: ["CMD", "/sentinel", "-healthz"]` (CMD form — no shell needed for distroless). Verified live: probe of a running gateway → exit 0; probe of a dead port → exit 1. `docker compose config` shows both service healthchecks valid.

**Checkpoint**: V1 and V2 pass; the stack is deployable.

---

## Phase 4: User Story 2 - Fast, safe webhook ingestion (Priority: P2)

**Goal**: Ack < 1s, async triage decoupled from the connection, event gate, dedup, bot skip, body cap.

**Independent Test**: quickstart.md V3 + V4 + V5.

- [x] T007 [US2] `internal/verify/verify.go`: wrap body with `http.MaxBytesReader(w, r.Body, cfg.MaxBodyBytes)` before `io.ReadAll`; oversized → 413, no HMAC work (FR-006, research R4); extend `internal/verify/verify_test.go`
  - **Done (2026-09-19)**: `Middleware` signature is now `Middleware(secret string, maxBytes int64, next http.Handler)`; it wraps `r.Body` in `http.MaxBytesReader` before the read and returns 413 on `*http.MaxBytesError` (else 400), so an oversized body is rejected before any HMAC work (constitution VII, FR-006). Call site in `cmd/sentinel/main.go` passes `cfg.MaxBodyBytes` (T002). New `TestMiddlewareBodyTooLarge` proves an over-cap body — even validly signed — gets 413 and the handler never runs; existing signature tests updated to the new signature and still pass. Full suite green.
- [x] T008 [US2] `internal/webhook/webhook.go`: require `X-GitHub-Event == "pull_request"` (missing/other → 200, ignore, zero API calls); reject missing `X-GitHub-Delivery` with 400 (FR-003)
  - **Done (2026-09-19)**: added `eventHeader`/`deliveryHeader` consts and two early gates in `ServeHTTP` (after the method check, before reading the body): non-`pull_request` (or missing) event → 200 "ignored event" with zero API calls; missing `X-GitHub-Delivery` → 400. `deliveryID` captured for the dedup key (T009) and added to the triage log line. Updated the `post` test helper to set both headers so existing pipeline tests stay valid (T012 adds the missing-header cases). Reconciliation: the spec edge case phrases a missing event type as "malformed" — I followed the task's explicit "200, ignore" (GitHub-friendly convention), reserving 400 for the missing delivery ID; both honor the "zero side effects" invariant. Full suite green.
- [x] T009 [US2] `internal/webhook/webhook.go`: ack-then-process — handler validates, claims the delivery ID via the ledger (T003), enqueues a job on a buffered channel, returns 200 immediately; worker goroutines (count from T002 config) run the existing pipeline with `context.Background()` + 45s timeout — never `r.Context()` (FR-001, FR-002, research R1/R2)
  - **Done (2026-09-20)**: `ServeHTTP` now validates + parses + gates (event/action/bot/dedup) synchronously, then non-blocking-enqueues a `job` on a `queueCapacity=256` buffered channel and acks 200. Extracted stages 3-5 into `process(ctx, job)`; each worker runs it on a fresh `context.Background()` with a `triageTimeout=45s` deadline (never the request ctx). Ledger (`ledgerCapacity=1024`) wired via `Claim` before enqueue so a redelivery is acked-not-reprocessed. Queue-full path fails open (logs + 200) with a `ponytail:` note on the drop-not-retry ceiling.
- [x] T010 [US2] `internal/webhook/webhook.go`: skip triage when `pull_request.user.login` has the `[bot]` suffix (FR-005, research R3)
  - **Done (2026-09-20)**: `isBot(login)` (`strings.HasSuffix(login, "[bot]")`) gate in `ServeHTTP` before the dedup claim → bot-authored PRs ack 200 with zero API calls and don't consume a ledger slot.
- [x] T011 [US2] `cmd/sentinel/main.go`: wire worker pool + graceful shutdown (signal context, `srv.Shutdown`, drain queue) and full server timeouts (Read/Write/Idle) in addition to existing ReadHeaderTimeout
  - **Done (2026-09-20)**: `handler.Start()` launches the pool; `signal.NotifyContext(SIGINT/SIGTERM)` triggers `srv.Shutdown` (stop accepting) then `handler.Shutdown` (close queue, `wg.Wait` bounded by a 50s ctx) so accepted deliveries drain. Added `ReadTimeout`/`WriteTimeout`/`IdleTimeout`; `ListenAndServe` now tolerates `http.ErrServerClosed`. `Start`/`Shutdown` are `sync.Once`-guarded.
- [x] T012 [US2] Extend `internal/webhook/webhook_test.go` (constitution: success + fail-open): ack latency < 1s with a hanging fake model; same delivery ID ×3 → exactly one label + one comment; `issues` event → zero GitHub calls; bot author → zero calls; diff/model/action errors → 200 + PR untouched (fail-open preserved)
  - **Done (2026-09-20)**: rewrote the suite for the async lifecycle (`newStarted` + `drain` via `Shutdown`); `fakeGitHub` now uses a mutex + call counters (workers are concurrent). New cases: `TestAckIsFastWhileTriageBlocks` (blockingTriager, ack <500ms), `TestDuplicateDeliveryProcessedOnce` (×3 → 1 label/1 comment), `TestNonPullRequestEventIgnored`, `TestBotAuthorSkipped`, `TestMissingDeliveryIDRejected`; existing fail-open + gating cases retained. Full suite green under `-race`.

**Checkpoint**: V3–V5 pass; ingestion is fast, idempotent, and fail-open.

---

## Phase 5: User Story 3 - Honest ML evaluation (Priority: P3)

**Goal**: Leakage-free splits, validation-selected threshold, training metrics, honest gate.

**Independent Test**: quickstart.md V6.

- [x] T013 [US3] `ml/build_dataset.py`: generate synthetic slop per split with disjoint random seeds and randomized filenames/titles (per-split pools, varied titles — no fixed "Update code"); add post-build assertion that no synthetic (title, diff) hash appears in more than one split; build fails on collision (FR-009, research R5)
  - **Done (2026-09-20)**: rewrote to split REAL rows first (`_stratified_split_real`, stdlib), then generate synthetic slop PER split from disjoint seeds (`seed+1/+2/+3`) with randomized titles (`_TITLES` pool), filenames (`_FILES`), and per-row nonces. `assert_no_leakage` SHA-256-hashes each (title,diff) and raises on any cross-split collision (within-split dups allowed). Leakage logic is pandas-free (pandas import deferred to `_write`) so it runs offline. New `ml/tests/test_build_dataset.py` (stdlib-only): clean build, planted-collision caught, within-split dup allowed, seed reproducibility — all pass.
- [x] T014 [US3] `ml/train.py`: add `compute_metrics` (SLOP precision/recall/F1 + confusion matrix), fixed seed, and post-training threshold selection — sweep candidates on the validation split, pick the lowest whose SLOP precision ≥ 0.85, write `threshold.json` `{"threshold": t, "val_precision": p}` beside the artifact (FR-010, FR-011, research R6)
  - **Done (2026-09-20)**: added `compute_metrics` (SLOP precision/recall/F1) wired into `Trainer` with `metric_for_best_model="slop_f1"`; `set_seed(42)` + `seed`/`data_seed` in `TrainingArguments`. Post-train: `trainer.predict(val_ds)` → softmax → `select_threshold` sweeps 0.50–0.99 and picks the lowest cutoff clearing the 0.85 bar (fallback: most-precise, warned); writes `threshold.json {threshold, val_precision, bar}` beside the artifact + logs the validation confusion matrix. Pure-numpy `softmax`/`select_threshold` extracted to `ml/thresholds.py` (no torch import) so `ml/tests/test_thresholds.py` runs offline — 4 cases pass (lowest-clearing, most-permissive tie, unreachable-bar fallback, softmax normalization).
- [x] T015 [US3] `ml/evaluate.py`: gate on test-set SLOP precision at the selected threshold; report threshold + val precision + confusion matrix; exit non-zero below bar (FR-012)
  - **Done (2026-09-20)**: reads `threshold.json` via `_load_threshold` (0.95 fallback if absent); scores the test set at the SELECTED threshold (softmax SLOP prob ≥ t) rather than a bare 0.5 argmax, so the reported precision is the one the deployed system operates at. Prints classification report + confusion matrix, logs test SLOP precision vs bar, `sys.exit(1)` below bar.
- [x] T016 [US3] `model/app.py`: read `threshold.json` with the artifact and expose it on `/healthz` per contracts/predict-endpoint.md; `internal/config/config.go` + `internal/triage/triage.go`: `CONFIDENCE_THRESHOLD` default becomes artifact value via `/healthz` fetch with 0.95 fallback when unavailable (research R6)
  - **Done (2026-09-20)**: `model/inference.py` loads `threshold.json` at startup (`THRESHOLD`, `VAL_PRECISION`, `ARTIFACT_VERSION` from dir name; 0.95 default); `/healthz` now returns `{status, threshold, artifact}` per contract. Go: `config.ThresholdFromEnv` records an explicit `CONFIDENCE_THRESHOLD` (default raised to 0.95 to match the model fallback); `triage.Client.FetchHealth` decodes `/healthz`; `main.go` sources the threshold from the model at startup (5s budget) ONLY when not set via env, falling back to the built-in default if the model is down (gateway still starts — FR-007). Tests: config default+ThresholdFromEnv; `TestFetchHealth` + `TestFetchHealthErrorOn503`. Full Go suite green under `-race`.

**Checkpoint**: V6 passes with honest numbers.

---

## Phase 6: User Story 4 - Hardened containers (Priority: P4)

**Goal**: Non-root, cap-dropped, read-only, resource-limited, CPU-only slim images.

**Independent Test**: quickstart.md V7.

- [x] T017 [P] [US4] `model/Dockerfile`: install CPU-only torch in a dedicated step with `pip install torch==<pin> --index-url https://download.pytorch.org/whl/cpu`, then the rest from PyPI; add a dedicated non-root user and `USER` it (FR-013, FR-014, research R8)
  - **Done (2026-09-20)**: dedicated `pip install --index-url https://download.pytorch.org/whl/cpu torch==2.5.1` step (CPU wheels only, no CUDA pull), then `-r requirements.txt` from PyPI. Added `useradd --uid 10001 appuser` + `USER appuser`, and `ENV HF_HOME=/tmp/hf` so transformers has a writable cache under the read-only rootfs + tmpfs /tmp (T018).
- [x] T018 [US4] `docker-compose.yml`: for both services add `cap_drop: [ALL]`, `security_opt: [no-new-privileges:true]`, `read_only: true` + writable tmpfs `/tmp`, and resource limits (gateway 64m/0.5 cpu; model 2g/2.0 cpu) (FR-013, research R9)
  - **Done (2026-09-20)**: both services get `cap_drop: [ALL]`, `security_opt: [no-new-privileges:true]`, `read_only: true`, and `deploy.resources.limits` (gateway 64m/0.5, model 2g/2.0). Model adds `tmpfs: [/tmp]` for the HF cache + uvicorn scratch; the distroless gateway needs no writable FS. Also switched compose `CONFIDENCE_THRESHOLD` default to empty so the gateway sources it from the model artifact (T016) unless overridden. `docker compose config` validates; rendered fields confirmed.
- [x] T019 [P] [US4] `deploy/Dockerfile`: drop the redundant `COPY go.mod ./` (the following `COPY . .` supersedes it)
  - **Done (2026-09-20)**: removed the redundant `COPY go.mod ./`; `COPY . .` brings in go.mod + sources in one step (no third-party deps means no cache-priming layer to preserve).

**Checkpoint**: V7 passes; images hardened and ≥ 60% smaller (SC-006).

---

## Phase 7: User Story 5 - Docs truth + CI (Priority: P5)

**Goal**: Documentation matches the shipped architecture; every push runs checks.

**Independent Test**: quickstart.md V8.

- [ ] T020 [P] [US5] Create `.env.example` listing every required + optional variable from contracts/webhook-endpoint.md with safe placeholder values
- [ ] T021 [P] [US5] Rewrite `README.md` to the shipped two-service self-hosted-model architecture: correct stack description, zero-Gemini, quickstart (V1/V2 commands), env table, training section (artifact production), badges fixed (FR-016, SC-007)
- [ ] T022 [P] [US5] Rewrite `AGENTS.md` to match: remove Gemini prose and `GEMINI_API_KEY`, state the 8 constitution principles, keep the zero-third-party-Go-deps rule unconditional (FR-016)
- [ ] T023 [US5] Create `.github/workflows/ci.yml`: on push + pull_request, ubuntu-latest, Go 1.26 — `go build ./...`, `go vet ./...`, `go test ./... -race` (FR-017, research R10)

**Checkpoint**: V8 passes; docs truthful, CI green.

---

## Phase 8: Polish & Cross-Cutting Concerns

- [ ] T024 Run full quickstart.md validation V1–V8 and record outcomes in the tasks checklist
- [ ] T025 Final sweep: `grep -ri gemini README.md AGENTS.md PROJECT_MAP.md` (expect only the git-history note in PROJECT_MAP), `go vet ./...`, `go test ./... -race` — all green

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: no dependencies — start immediately.
- **Foundational (Phase 2)**: after Phase 1 — blocks US2 only (T002/T003); US1/US3/US4/US5 do not depend on it.
- **User Stories**: US1 → US2 recommended (compose edits touch the same file); US3, US4, US5 independent and parallelizable.
- **Polish (Phase 8)**: after all desired stories.

### User Story Dependencies

- **US1 (P1)**: none — MVP.
- **US2 (P2)**: depends on T002, T003 (Foundational); composes with US1's compose edits.
- **US3 (P3)**: independent (Python side) except T016's gateway default, which follows T009's wiring.
- **US4 (P4)**: independent; T017 can run any time.
- **US5 (P5)**: independent; best last so docs describe final behavior.

### Parallel Opportunities

- T002 ∥ T003 (Phase 2); T007 ∥ T008 within US2; T017 ∥ T019; T020 ∥ T021 ∥ T022 (three different doc files); US3 ∥ US4 ∥ US5 across stories.

---

## Parallel Example: User Story 5

```bash
Task: "Create .env.example"                    # T020
Task: "Rewrite README.md"                      # T021
Task: "Rewrite AGENTS.md"                      # T022
# three different files — run together
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. T001 baseline → 2. T004–T006 (US1) → 3. STOP and VALIDATE V1+V2 — the
   stack now deploys. Everything beyond this is hardening, not viability.

### Incremental Delivery

1. US1 → deployable (MVP)  2. US2 → safe ingestion  3. US3 → honest ML
4. US4 → hardened  5. US5 → truthful docs + CI  6. Polish sweep.

### Parallel Team Strategy

- Dev A: US1 → US2 (Go + compose track)
- Dev B: US3 (Python ML track)
- Dev C: US4 → US5 (containers → docs/CI track)

---

## Notes

- Constitution guardrails apply to every task: never auto-close, fail-open,
  zero third-party Go deps, HMAC stays first, classify value not provenance.
- T006's healthcheck probe: distroless has no shell or wget — the `-healthz`
  binary flag is the stdlib-only path (no new deps).
- Commit after each task or logical group; run `go test ./...` before each commit.
