---
description: "Task list for Sentinel Observability & Enhancements"
---

# Tasks: Sentinel Observability & Enhancements

**Input**: Design documents from `/specs/002-enhancements-observability/`

**Prerequisites**: `001-remediate-and-harden` implemented (async ack-then-process,
delivery-ID ledger, body cap, non-root hardened containers, leakage-free
evaluation, CI build/vet/test). 002 extends those files; it does not reintroduce
their changes.

**Tests**: Included for every serving-path change — the constitution (Quality
Gates) requires success-path AND fail-open tests for serving-path changes, and
SC-003/004/005/007/008 are test-verified outcomes.

**Organization**: Grouped by user story (spec.md P1–P5) for independent
implementation.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1–US5)

## Path Conventions

Two-service repo at root: Go gateway (`cmd/`, `internal/`, `deploy/`), model
service (`model/`), offline ML (`ml/`), compose + CI at root.

---

## Phase 1: Setup

**Purpose**: Confirm the 001 baseline before layering enhancements.

- [x] T001 Baseline check: `go build ./...`, `go vet ./...`, `go test ./...`, and
  `docker compose config` all green; confirm 001's async pipeline + dedup ledger
  are present (this feature builds on them). Record results.
  <!-- 2026-09-22: baseline green. go build ./... OK; go vet ./... OK; go test
  ./... -race all pass (config, github, triage, verify, webhook cached-ok);
  docker compose config VALID (with env set). 001 async pipeline present
  (internal/webhook/webhook.go worker pool + fresh 45s ctx) and dedup ledger
  present (internal/webhook/dedupe.go). No 002 markers on disk yet — clean start. -->


---

## Phase 2: Foundational (config surface)

**Purpose**: Config flags US1/US2/US3 read.

- [x] T002 [P] Extend `internal/config/config.go`: add `SHADOW_MODE` (bool, default
  false), `MODEL_THREADS` (int, default 4, range 1–64, passed to the model service),
  `GITHUB_MAX_RETRIES` (int, default 2, range 0–5), and `BUILD_VERSION` (string, set
  via `-ldflags -X`); extend `internal/config/config_test.go` with parse/validation
  cases (bad bool, out-of-range int).
  <!-- 2026-09-22: added ShadowMode, ModelThreads, GitHubMaxRetries to Config +
  Load() with a shared envInt helper (range-validated). BUILD_VERSION is carried
  as `var version` in main.go via -ldflags -X (T003), not an env var — a build
  stamp is not runtime config. Tests: TestLoadShadowMode (bad bool),
  TestLoadModelThreads + TestLoadGitHubMaxRetries (out-of-range int), defaults
  asserted in TestLoadDefaults. go test ./internal/config green. -->
- [x] T003 [P] Add a version variable to `cmd/sentinel/main.go` (`var version = "dev"`)
  set at build time with `-ldflags "-X main.version=$(git describe --tags --always)"`;
  document the build flag in the README quickstart.
  <!-- 2026-09-22: added `var version = "dev"` to main.go; verified injection with
  `go build -ldflags "-X main.version=test123"`. README "Build version" subsection
  documents the flag. Reported on /healthz + /stats in T006. -->


**Checkpoint**: Config + version compile and pass tests.

---

## Phase 3: User Story 1 - Operational visibility (Priority: P1) — MVP

**Goal**: Structured logs with a correlation ID threaded gateway → model, a stats
endpoint, and reported build version.

**Independent Test**: Deliver signed events; assert structured log lines carry the
delivery ID (and no diff/secret), the ID reaches the model log, and stats counters
advance.

- [x] T004 [US1] Replace `log.Printf` with `log/slog` (stdlib, JSON handler) across
  `cmd/sentinel/main.go`, `internal/webhook/webhook.go`, `internal/github/github.go`,
  `internal/triage/triage.go`; attach a per-delivery logger carrying the
  `X-GitHub-Delivery` ID (generate a fallback ID when absent). Ensure the diff body,
  token, and secret are never logged (FR-001, FR-002).
  <!-- 2026-09-22: main.go installs slog JSON handler as the default; all
  log.Printf/Fatalf → slog (config-load + server-fatal now slog.Error + os.Exit).
  webhook.process/act build a per-delivery `slog.With("delivery", id, owner, repo,
  pr)` logger; only safe fields logged (author, title, is_slop, confidence, err) —
  never the diff body, token, or secret. github.go/triage.go had NO log calls (they
  surface errors to webhook, which logs them under the delivery logger), so nothing
  to replace there. Fallback-ID note: a pull_request event missing X-GitHub-Delivery
  is rejected 400 (001 trust-boundary behavior, TestMissingDeliveryIDRejected) rather
  than fabricating an ID — process only ever runs with a non-empty delivery ID, so no
  generator is dead-added. go build/vet/test all green. -->
- [x] T005 [US1] `internal/triage/triage.go`: send the correlation ID to the model
  service on `/predict` (e.g. an `X-Correlation-ID` header); `model/app.py` +
  `model/inference.py`: read it and include it in the model's structured log line for
  that request (FR-002).
  <!-- 2026-09-22: triage.go adds WithCorrelationID(ctx,id) + unexported ctxKey;
  Analyze sets the X-Correlation-ID header when present. webhook.worker stamps
  j.deliveryID onto the worker ctx via triage.WithCorrelationID. app.py /predict
  reads `x_correlation_id: str = Header(default="")` and logs it (correlation_id)
  on the verdict line; missing header → "-", never fails. inference.py needs no
  change (it has no request-scoped logging). -->
  <!-- NOTE: model structured (JSON) logging + version-on-/healthz (T007) landed
  here too, same file (model/app.py) — see T007 for details. -->

- [x] T006 [US1] Add a stats collector using `sync/atomic` counters (received,
  triaged, flagged, skipped, failed) + a simple latency summary; increment it at the
  pipeline stages in `internal/webhook/webhook.go`; expose `GET /stats` (JSON) and the
  build version on `/healthz` in `cmd/sentinel/main.go` (FR-003, FR-004). New file:
  `internal/webhook/stats.go` (+ `stats_test.go`).
  <!-- 2026-09-22: new internal/webhook/stats.go — Stats with sync/atomic.Int64
  counters (received/triaged/flagged/skipped/failed) + latency sum/count/max for a
  mean_ms/max_ms/samples summary; lock-free CAS loop for max. Handler gains a Stats
  field + Stats() Snapshot accessor. process() increments incReceived (enqueue),
  incFailed (diff/model error), incSkipped (verdict not actionable), incFlagged
  (acted) and observeLatency around fetch+model. main.go: /healthz now JSON
  {status,version}; new /stats → statsWithVersion{Snapshot, version} via writeJSON
  helper. stats_test.go: zero-snapshot, counter semantics, latency mean/max, and a
  -race concurrent-increment test. README curl comments updated. -->

- [x] T007 [US1] `model/app.py`: switch to structured logging and report the model
  build/artifact version on `/healthz` (FR-004).
  <!-- 2026-09-22: app.py replaces basicConfig with a stdlib _JSONFormatter
  (StreamHandler, force=True) emitting {time, level, logger, msg} plus a safe-fields
  allowlist (correlation_id, is_slop, confidence) — never the diff. /healthz now
  reports "version" from MODEL_VERSION env (default "dev"); "artifact" already
  reported. py_compile green. -->

- [x] T008 [US1] Tests: `internal/webhook/webhook_test.go` (or a new `stats_test.go`)
  assert counters advance correctly across flagged/skipped/failed paths and that a
  processed delivery's log carries the delivery ID; assert no secret/diff leaks into
  the captured log output (SC-001, SC-003).
  <!-- 2026-09-22: webhook_test.go adds TestStatsAdvanceAcrossPaths (routingGitHub
  fails one PR number, routingTriager flags title=="slop"; asserts Received=3,
  Flagged=1, Skipped=1, Failed=1, Triaged=2, TriageSamples=2) and
  TestLogsCarryDeliveryIDAndNeverLeakDiff (captureLogs redirects slog to a
  mutex-guarded syncBuffer; asserts the delivery ID is present and the diff marker
  is absent). All webhook tests green under -race. -->


**Checkpoint**: A single PR is traceable end to end; stats reflect reality.

---

## Phase 4: User Story 2 - Safe rollout controls (Priority: P2)

**Goal**: Shadow mode + a versioned, confidence-bearing comment.

**Independent Test**: Shadow on → verdict logged, zero writes; shadow off → label +
comment posted with confidence + version in the text.

- [x] T009 [US2] `internal/webhook/webhook.go`: when `cfg.ShadowMode` is true, run the
  full pipeline and log the would-be verdict at the action point but skip `AddLabel`,
  `PostComment`, and (US5) the Check Run entirely (FR-005).
  <!-- 2026-09-22: shadow gate lives in process() after the flag counter increments —
  the pipeline (diff fetch + model call + verdict + Flagged++) runs in full, then when
  cfg.ShadowMode is true we log "shadow mode: would flag" with confidence+reason and
  return BEFORE act(). So stats reflect reality (an operator sees what *would* be
  flagged) while zero outward writes happen. The Check Run (T020) sits behind the same
  gate — it is created in act()/the flagged branch, which shadow returns before. -->
- [x] T010 [US2] `internal/webhook/webhook.go`: extend the comment builder to include
  the model confidence and model/artifact version alongside the reason; the version
  comes from the `/predict` response or the model `/healthz` (FR-006). Thread version
  through `triage.Result` if needed.
  <!-- 2026-09-22: added Version string `json:"version"` to triage.Result; model
  app.py PredictResponse gains version="" and /predict sets it to
  inference.ARTIFACT_VERSION (already loaded from the artifact dir name), so the
  verdict carries its own provenance — no extra /healthz round-trip. comment() now
  takes *triage.Result and appends a "_Model confidence: NN% · artifact: `ver`_" line
  (empty version → "unknown", %.0f%%). act() passes res through. -->
- [x] T011 [US2] Tests: `internal/webhook/webhook_test.go` — shadow-on: high-confidence
  slop produces zero label/comment calls; shadow-off: both calls occur and the comment
  text contains the confidence and version (SC-004).
  <!-- 2026-09-22: TestShadowModeSuppressesWrites (shadow on: 0 label/0 comment, but
  Flagged==1 proving the pipeline still ran) and
  TestShadowOffPostsCommentWithConfidenceAndVersion (shadow off: 1 label/1 comment; the
  comment text contains "93%" and the artifact version "model-2026-09-22"). Both green
  under -race. -->

**Checkpoint**: Operators can observe before acting; every comment is auditable.

---

## Phase 5: User Story 3 - Serving efficiency & input correctness (Priority: P3)

**Goal**: Warmup + thread bound, correct pair-encoding, bounded transient retry.

**Independent Test**: quickstart V-perf (first-request latency), long-title encoding,
GitHub 503→503→200 then 503-forever.

- [x] T012 [US3] `model/inference.py`: replace the literal `f"{title}\n[SEP]\n{diff}"`
  with the tokenizer's pair API — `tokenizer(title, diff, truncation="only_second",
  max_length=512)` — so the real separator token is used and the diff (second segment)
  is the one truncated; cap the title's contribution so it cannot consume the whole
  window (FR-008). Apply the SAME encoding in `ml/train.py` and `ml/evaluate.py` so
  train/serve stay identical.
  <!-- 2026-09-22: new model/encoding.py is the ONE encode_pair (real separator via
  the pair api, truncation="only_second" so only the diff gives, title pre-capped to
  TITLE_MAX_TOKENS=64 so a pathological title can't starve the diff). ml/encoding.py
  is its twin — code body byte-for-byte identical (verified with diff; only docstrings
  differ, each naming the other as the twin) because model/ and ml/ are separate build
  contexts with no shared import path. encode_pair accepts a single string (serve/eval)
  or equal-length lists (batched train). inference._encode, ml/train.py's batched
  encode closure, and ml/evaluate.py's inference loop all call it. model/Dockerfile
  COPY includes encoding.py. py_compile green on all five files. -->
- [x] T013 [US3] `model/inference.py` + `model/app.py`: run one warmup forward pass at
  startup and call `torch.set_num_threads(MODEL_THREADS)` from config (FR-007).
  <!-- 2026-09-22: inference.configure_threads(n) → torch.set_num_threads (no-op n<1)
  and inference.warmup() → one predict() pass, best-effort (a warmup failure logs a
  warning, never stops the service). app.py @app.on_event("startup") calls both, in
  order (threads bound before the warmup pass runs). MODEL_THREADS read from env
  (default 4) in app.py; docker-compose.yml sets it to 2 to match the model service's
  cpus limit. -->
- [x] T014 [US3] `internal/github/github.go`: add bounded retry with backoff on 5xx and
  on 403/429 carrying a rate-limit/retry signal, capped by `GITHUB_MAX_RETRIES` and the
  remaining triage budget from the request context; never sleep past the budget
  (FR-009). Keep the method signatures unchanged so `webhook`/`triage` wiring is
  untouched.
  <!-- 2026-09-22: new doRetrying(ctx, mk) drives all three calls — mk builds a fresh
  request per attempt (re-readable POST body). Retries transport errors, 5xx, and
  403/429 (retryable()); backoff starts at baseBackoff=500ms and doubles. retryAfter()
  honors Retry-After (secs) then x-ratelimit-reset (unix). Budget guard: if
  ctx.Deadline() can't cover the next wait, return the last result now so the caller
  fails open promptly rather than sleeping past the 45s worker budget. Only New() changed
  signature (now takes maxRetries); FetchDiff/AddLabel/PostComment signatures unchanged,
  so webhook/triage wiring untouched. main.go passes cfg.GitHubMaxRetries. -->
- [x] T015 [US3] Tests: `internal/github/github_test.go` — 503×2 then 200 → success
  after retry; 503 for the whole budget → error returned so the caller fails open
  (SC-007). Python: a small `model/tests/test_inference.py` asserting a long title
  still yields diff tokens and the separator is the tokenizer's real token (SC-006).
  <!-- 2026-09-22: github_test.go adds TestFetchDiffRetriesThenSucceeds (2×503→200,
  3 server calls), TestFetchDiffFailsOpenAfterExhausting (503 forever → error, exactly
  initial+2 retries), TestRetryNeverSleepsPastDeadline (50ms budget < 500ms backoff →
  1 call, no sleep, returns <300ms) and TestRetryHonorsRetryAfter (429 w/ Retry-After:1
  → waits ≥1s). All green under -race (github pkg 5.08s). model/tests/test_inference.py
  drives encode_pair with a faithful fake tokenizer (no torch): long title capped to 64
  tokens with diff tokens surviving, real separator inserted (no literal [SEP]),
  truncation="only_second", batched-list shape; an extra pass loads a real roberta-base
  tokenizer when transformers is installed (skips cleanly otherwise). Wired into
  ci.yml's ml-selfchecks job. All checks pass. -->

**Checkpoint**: No cold-start penalty; encoding is correct; brief GitHub blips recover.

---

## Phase 6: User Story 4 - Defense-in-depth security (Priority: P4)

**Goal**: Path-escaping, safetensors, digest-pinned images, vuln scan.

**Independent Test**: quickstart V-sec (artifact load format, crafted owner/repo, CI
scan on a bad pin).

- [x] T016 [P] [US4] `internal/github/github.go`: `url.PathEscape` owner and repo in
  every URL builder (`FetchDiff`, `AddLabel`, `PostComment`) (FR-010); add a
  `github_test.go` case with a `/`-bearing owner asserting the escaped path.
  <!-- 2026-09-23: added an unexported esc(s) = url.PathEscape(s) helper and wrapped
  owner+repo with it in all three URL builders (FetchDiff/AddLabel/PostComment). The
  values arrive from the webhook payload (a trust boundary), so a crafted "../" or
  slash-bearing segment can no longer reshape the REST path. github_test.go adds
  TestFetchDiffEscapesOwnerRepo (owner "octo/../evil", repo "re po" →
  /repos/octo%2F..%2Fevil/re%20po/pulls/7). Public method signatures unchanged. Green
  under -race. -->

- [x] T017 [P] [US4] `ml/train.py`: save the artifact as safetensors
  (`save_model(..., safe_serialization=True)` / ensure `.safetensors` output);
  `model/inference.py`: load with `use_safetensors=True` so no pickle is deserialized
  at serve time (FR-011).
  <!-- 2026-09-23: ml/train.py sets save_safetensors=True in TrainingArguments — chosen
  over save_model(..., safe_serialization=True) because it also governs per-epoch
  checkpoint serialization, not just the final save, so no pickle .bin is ever written.
  model/inference.py loads with use_safetensors=True (closes the pickle
  arbitrary-code-execution path at load — the artifact is mounted at MODEL_PATH, i.e.
  not necessarily ours) AND _REQUIRED_ANY["weights"] is now ("model.safetensors",) only,
  so a legacy .bin-only artifact fails _verify_artifact with one actionable line instead
  of load-time deserialization. py_compile green. -->
- [x] T018 [P] [US4] Pin every base image by digest: `deploy/Dockerfile`
  (`golang:1.26@sha256:…`, `gcr.io/distroless/static:nonroot@sha256:…`) and
  `model/Dockerfile` (`python:3.11-slim@sha256:…`) (FR-012).
  <!-- 2026-09-23: all three base images digest-pinned with the tag kept for humans +
  @sha256 for reproducibility. deploy/Dockerfile: golang:1.26@sha256:6c2a5538… (build),
  gcr.io/distroless/static:nonroot@sha256:e2e927ec… (runtime). model/Dockerfile:
  python:3.11-slim@sha256:da047cb8…. Used the MULTI-ARCH INDEX digest (from `docker
  buildx imagetools inspect`), not the amd64-only manifest digest, so arch selection
  still works. Each pin carries a comment + a refresh note (`docker buildx imagetools
  inspect <tag>`) to keep tag and digest in sync on a bump. -->
- [x] T019 [P] [US4] `.github/workflows/ci.yml`: add `govulncheck ./...` (Go) and a
  Python dependency audit (e.g. `pip-audit -r model/requirements.txt -r
  ml/requirements.txt`); fail the build on a known advisory (FR-013). (Confirm
  `.dockerignore` already excludes `.env` and heavy non-Go paths — done in this repo;
  add a CI assertion if desired.)
  <!-- 2026-09-23: go job gains a govulncheck step (go install
  golang.org/x/vuln/cmd/govulncheck@latest; govulncheck ./...) — call-graph aware, so it
  only fails on advisories reaching code we actually run. New dep-audit job runs
  pip-audit -r model/requirements.txt -r ml/requirements.txt against the pinned serving +
  training deps, auditing the requirement files directly (no multi-GB torch install in
  CI). Both fail the build on a known advisory (FR-013). .dockerignore already excludes
  .env/.env.*/ml//model/ — left as-is; no CI assertion added (a redundant grep on a file
  already reviewed is boilerplate nobody asked for). go build/vet/test -race all green;
  pair-encoding self-check passes. -->


**Checkpoint**: Supply chain reproducible; last input edges escaped; scan gating.

---

## Phase 7: User Story 5 - Check Run + feedback loop (Priority: P5)

**Goal**: A Check Run alongside the label, and an out-of-band maintainer-feedback log.

**Independent Test**: quickstart V-ux (Check Run appears; label-removed appends one
signal row; no serving-path state survives restart).

- [x] T020 [US5] `internal/github/github.go`: add `CreateCheckRun(ctx, owner, repo,
  headSHA, conclusion, summary)` (POST `/repos/{o}/{r}/check-runs`); `internal/webhook`
  extracts the PR head SHA from the payload and, on a flagged verdict (and when not in
  shadow mode), creates a neutral/observational Check Run reporting the verdict +
  confidence — never a required/blocking status (FR-014). Fail-open on Check Run error;
  it MUST NOT block the label/comment path. Add tests (success + fail-open).
  <!-- 2026-09-23: github.CreateCheckRun posts status=completed + conclusion="neutral"
  (never failing/blocking) with a fixed name so GitHub keys by (name, head_sha) and a
  redelivery UPDATES rather than stacks; owner/repo path-escaped via esc() (FR-010);
  goes through postJSON → doRetrying so it shares the budget-aware retry. webhook.go:
  event.PullRequest.Head.SHA parsed, threaded onto job.headSHA; GitHub interface gained
  CreateCheckRun; act(job,res) does label → comment → check run, each error logged not
  fatal (check run last + least critical, so its failure can't block label/comment —
  FR-014). Empty head SHA → log warn + skip (no fabricated SHA). Check run sits behind
  the SAME shadow-mode gate as label/comment (shadow = zero outward writes). Added
  checkRunSummary helper (reason/confidence/artifact + "observational, not required").
  Tests: TestFlaggedCreatesNeutralCheckRun (1 check, headSHA abc123, conclusion neutral),
  TestCheckRunFailureDoesNotBlockLabelComment (checkErr set → label+comment still 1 each),
  TestFlaggedWithoutHeadSHASkipsCheckRun (no head sha → label+comment but 0 checks),
  TestShadowModeSuppressesWrites extended to assert 0 checks. go build/vet OK; go test
  ./... -race all green (github 5.1s, webhook 1.1s). -->

## Feedback-loop decision (T021–T023)

Per the Notes above and spec FR-015, T021–T023 introduce the project's ONLY
persistence — an out-of-band, append-only feedback log. This was a maintainer
call (serving-adjacent persistence). Resolved 2026-09-23: **build it** — async,
off the request path, `FEEDBACK_LOG` unset disables it entirely.
- [x] T021 [US5] Accept the additional webhook actions needed for feedback
  (`pull_request` `unlabeled`, and reaction/comment events if used) in
  `internal/webhook/webhook.go`; on a maintainer removing the slop label (or a 👎), map
  it to a feedback signal. Reject/ignore feedback for PRs Sentinel never flagged
  (no fabricated verdict) — see spec edge cases.
  <!-- 2026-09-23: `event` gained a `Label{Name}` field; `ServeHTTP` intercepts
  `action=="unlabeled" && Label.Name==cfg.SlopLabel` AFTER parse but BEFORE the
  actionable gate and bot-author skip (the actor is the maintainer, not the PR
  author), dedups via the same ledger.Claim, and calls recordDisagreement. Keying
  on the CONFIGURED slop label — which in normal operation only Sentinel applies —
  makes a removal evidence Sentinel flagged the PR without any per-PR memory, so
  the stateless gateway never fabricates a verdict for a PR it didn't flag (spec
  edge case). Any other removed label is a normal non-actionable event (ignored).
  Confidence is NOT persisted at flag time, so the signal omits it (never
  fabricated); ponytail upgrade path = a persistent flag record if confidence-in-
  signal is ever required. FEEDBACK_LOG unset → capture disabled, event still
  acked. Config gained FeedbackLogPath (env FEEDBACK_LOG, "" disables). -->
- [x] T022 [US5] Add an out-of-band, append-only signal writer (new file
  `internal/webhook/feedback.go`): append one JSON row {pr, original_verdict,
  confidence, disagreement_type, ts} to a configured path OUTSIDE the request path
  (buffered/async write; the request still acks fast and holds no state) (FR-015).
  Add `feedback_test.go` (one row per event; well-formed; no serving-path state).
  <!-- 2026-09-23: feedback.go adds Signal{PR, OriginalVerdict, Confidence
  (omitempty), DisagreementType, TS} and FeedbackLog — a buffered channel
  (cap 64) drained by ONE background goroutine that appends each Signal as a JSON
  line (json.Encoder → JSONL) to the configured path. Record does a non-blocking
  select-send (full buffer drops the signal, fail-open); the request path never
  touches the file. Start()/Close() are idempotent; Close drains buffered signals
  before returning (wired into Handler.Shutdown AFTER workers drain). An
  unopenable file logs once and keeps draining the channel so senders never block.
  ponytail: append-only local file, no rotation/fsync-per-write; ceiling = single-
  host unbounded log; upgrade path = rotation or a durable store. feedback_test.go
  covers: exactly one well-formed row per removal (PR/verdict/type/ts, zero GitHub
  writes), unrelated-label removal records nothing, redelivery ×3 → 1 row, disabled
  (FEEDBACK_LOG unset) still acks, and a normal flagged triage writes no feedback
  row. go build/vet/test -race all green. -->
- [x] T023 [US5] `ml/build_dataset.py`: add an optional `--feedback-log <path>` source
  that folds signal-log rows into the labeled dataset for retraining (FR-016);
  document the loop in the quickstart.
  <!-- 2026-09-23: build_dataset.py gained --feedback-log. _parse_signal (pure,
  network-free) resolves one JSONL signal to (repo, number) ONLY when
  disagreement_type=="label-removed" and pr parses as owner/repo#n; every other
  type or a malformed ref returns None (skipped, never guessed — no fabricated
  label). _load_feedback fetches title+diff by PR identity (the log is identity-
  only: the gateway never persists diff bodies, FR-001), reusing collect_prs.
  _session/_fetch_diff rather than a second GitHub client, and labels each removed-
  slop PR LEGIT (a maintainer overturning the slop verdict). Feedback rows join the
  real pool, so they get the same stratified split + leakage check. Requires
  GITHUB_TOKEN (raises SystemExit otherwise). ponytail: one blocking REST round-
  trip per signal (fine for low-volume human feedback); upgrade path = batch/
  GraphQL. test_build_dataset.py gained test_feedback_signal_parsing (6 skip/keep
  cases). README Training section documents the loop; py_compile + self-checks
  green. -->

**Checkpoint**: Verdicts are first-class in the PR UI; maintainer judgment feeds back.

---

## Phase 8: Polish & Cross-Cutting

- [x] T024 Update `README.md`: document shadow mode, the `/stats` endpoint, the build
  version flag, retry behavior, the Check Run, and the feedback loop + how to enable the
  extra webhook events.
  <!-- 2026-09-23: README Features section gained Check Run (observational/neutral),
  shadow mode, bounded retry, and feedback-loop bullets; Configuration table gained
  SHADOW_MODE, GITHUB_MAX_RETRIES, MODEL_THREADS, FEEDBACK_LOG rows; System Flow
  "Act" step now shows label+comment+Check Run; Webhook section notes the Pull
  requests event already delivers the unlabeled action (no extra event needed) and
  that FEEDBACK_LOG enables capture; Training section documents closing the loop via
  build_dataset --feedback-log; Design Decisions gained a stateless-serving-path
  note. /stats + build version flag were already documented (T004/T008). -->
- [x] T025 Final sweep: `go vet ./...`, `go test ./... -race`, `docker compose config`,
  and a log scan confirming no diff/secret leakage; record outcomes.
  <!-- 2026-09-23: go vet ./... OK; go test ./... -race all green (config, github,
  triage, verify, webhook); docker compose config OK (with placeholder secrets —
  it correctly requires GITHUB_WEBHOOK_SECRET/GITHUB_TOKEN). Log-leak scan: grep of
  every slog./log. call across internal/ + cmd/ for diff|token|secret|body|
  signature → NONE. The feedback Signal persists identity + verdict + type + ts
  only (no diff body, FR-001). py_compile ml/build_dataset.py OK; ml self-checks
  pass. -->

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: none — start immediately.
- **Foundational (Phase 2)**: after Phase 1 — provides config for US1/US2/US3.
- **User Stories**: US1 is the MVP (visibility). US2 depends on T002 (SHADOW_MODE) and
  reuses US1's version plumbing. US3, US4 are independent of US1/US2 and each other.
  US5 depends on US2's shadow gate (T009) and US1's logging.
- **Polish (Phase 8)**: after all desired stories.

### User Story Dependencies

- **US1 (P1)**: Foundational (T002/T003). MVP.
- **US2 (P2)**: T002 (SHADOW_MODE); comment version reuses US1 plumbing.
- **US3 (P3)**: independent (T014 Go retry ∥ T012/T013 Python serving).
- **US4 (P4)**: independent; all four tasks parallelizable.
- **US5 (P5)**: after US1 (logging) + US2 (shadow gate); best last.

### Parallel Opportunities

- T002 ∥ T003 (Phase 2); T016 ∥ T017 ∥ T018 ∥ T019 (all of US4); US3 ∥ US4 across
  stories; within US1, T007 (Python) ∥ the Go tasks.

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. T001 baseline → 2. T002/T003 (config + version) → 3. T004–T008 (US1) → STOP and
   VALIDATE: the running system is now observable and traceable. Everything past this
   is safety, efficiency, and features.

### Incremental Delivery

1. US1 → observable  2. US2 → safe rollout  3. US3 → efficient + correct
4. US4 → hardened supply chain  5. US5 → Check Run + feedback  6. Polish.

### Parallel Team Strategy

- Dev A: US1 → US2 (Go observability + rollout track)
- Dev B: US3 Python serving (T012/T013) + US4 T017 (ML/artifact track)
- Dev C: US3 T014 + US4 T016/T018/T019 (Go/security/supply-chain track)
- US5 folded in after US1/US2 land.

---

## Notes

- Constitution guardrails apply to every task: never auto-close (the Check Run is
  observational, never a required status), fail-open (retry, Check Run, and feedback
  all degrade to no-action on error), zero third-party Go deps (slog + atomic are
  stdlib; retry is stdlib), self-hosted model, classify value not provenance.
- FR-015 introduces the ONLY persistence in the project, and it is deliberately
  out-of-band and off the serving path (append-only log read solely by `ml/`). If the
  maintainer rejects any serving-adjacent persistence, defer T021–T023 and ship the
  Check Run (T020) alone.
- Commit after each task or logical group; run `go test ./...` before each commit.
