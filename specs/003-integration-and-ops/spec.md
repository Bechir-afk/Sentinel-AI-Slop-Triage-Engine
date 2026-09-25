# Feature Specification: Sentinel End-to-End Validation & Operations

**Feature Branch**: `003-integration-and-ops`

**Created**: 2026-09-24

**Status**: Draft

**Input**: User description: "Everything so far is unit-tested with httptest fakes
and `docker compose config` — nothing has run the two-service stack against a real,
signed webhook. Cover a real integration smoke test, a golden end-to-end path
(signed delivery → flagged PR → label + comment + Check Run) with no live
credentials, the safe-mode paths (shadow + feedback loop) proven end to end, and an
operational runbook with failure drills — all without touching a constitution
principle."

**Relationship to prior work**: This is the third tier on top of
`001-remediate-and-harden` and `002-enhancements-observability`. 001 made Sentinel
deployable, fast, idempotent, and honestly evaluated; 002 made it observable, safe
to roll out, and feature-complete (Check Run, shadow mode, feedback loop). Both were
verified with `net/http/httptest` fakes and `docker compose config` — the units and
the fail-open paths are covered, and the stack *parses*. What has **never** run is
the assembled system: the real gateway binary talking over real HTTP to a real model
service, driven by a genuinely HMAC-signed delivery, all the way to the outward
writes. 003 adds nothing to the serving path. It adds the harness, the drills, and
the runbook that prove the shipped binary behaves as 001+002 specified — closing the
gap between "every part is tested" and "the whole thing works."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Operator can bring the stack up and prove it healthy (Priority: P1)

An operator (or CI) starts both services and, without reading code, gets a
deterministic yes/no answer to "is the assembled system alive and wired correctly?"
Both services reach healthy; the gateway's `/healthz` and `/stats` respond; the
gateway has actually sourced its threshold from the model's `/healthz` over the
internal network. This runs against the real model artifact when present, and against
a documented lightweight model stub when the multi-GB serving stack is unavailable
(CI), so the wiring is provable either way.

**Why this priority**: This is the foundational claim 001+002 never verified —
that the two processes actually talk. Every richer end-to-end scenario (US2–US4)
assumes a booted, wired stack; if bring-up is silently broken, nothing downstream is
trustworthy. It is also the cheapest, most-run check, so it must exist first.

**Independent Test**: Bring up gateway + model (real artifact or stub); poll each
health endpoint until ready or a bounded timeout; assert `/stats` returns zero
counters, `/healthz` reports a build version, and the gateway logged a
threshold-sourced-from-artifact line. Tear down cleanly.

**Acceptance Scenarios**:

1. **Given** the two-service stack is started with a model reachable, **When** each
   service's health endpoint is polled, **Then** both report healthy within the
   startup budget and each response carries that service's build version.
2. **Given** the gateway started with the model reachable and no `CONFIDENCE_THRESHOLD`
   set, **When** its startup logs are read, **Then** they show the operating threshold
   was sourced from the model artifact's `/healthz` (not the fallback).
3. **Given** a freshly booted gateway before any delivery, **When** `/stats` is
   queried, **Then** all counters are zero and the endpoint requires no secret and
   exposes no PR content.

---

### User Story 2 - Golden end-to-end path with no live credentials (Priority: P2)

The maintainer (via a test) proves the headline promise on the real binary: a
genuinely HMAC-signed `pull_request` delivery for a slop PR flows through the real
gateway process, which fetches the diff and scores it against a real model, and then
performs exactly the three outward writes — label, comment, Check Run — against a
**controllable GitHub endpoint** (a local stub the gateway calls over real HTTP).
No live GitHub token or repository is required; the stub records what the gateway
actually sent, so the assertions are on real request bytes, not on a fake's method
calls.

**Why this priority**: This is the single most valuable thing 003 adds — the first
proof that a real signed request produces the correct real API calls. It exercises
HMAC verification, the gate, the async worker pool, the diff fetch, the model round
trip, and all three writes as one integrated flow. It is P2 only because it presumes
US1's booted stack.

**Independent Test**: Point the gateway's GitHub base URL at a local recording stub
and its model URL at a stub (or real model) returning high-confidence slop; POST a
delivery signed with the configured secret; wait for async completion; assert the
stub received exactly one label add, one comment (carrying confidence + version), and
one Check Run (`completed`/`neutral`), and that a redelivery of the same delivery ID
adds nothing.

**Acceptance Scenarios**:

1. **Given** a correctly signed slop delivery, **When** the gateway finishes async
   triage, **Then** the GitHub stub recorded exactly one label add, one comment, and
   one Check Run on the PR head, and the webhook response was 2xx and immediate.
2. **Given** the same delivery ID is sent twice, **When** both are processed, **Then**
   the outward writes occur exactly once (idempotent), matching the dedup guarantee.
3. **Given** a delivery whose signature does not match the raw body, **When** it is
   received, **Then** the gateway responds 401 and performs zero GitHub or model
   calls (verification is first at the trust boundary).

---

### User Story 3 - Safe-mode and feedback paths proven end to end (Priority: P3)

The two behaviors that are easy to get subtly wrong under real wiring are proven on
the assembled stack: (a) with shadow mode on, a high-confidence-slop delivery runs
the *full* pipeline — diff fetch, model call, flag counted — yet the GitHub stub
records **zero** outward writes of any kind; and (b) a maintainer removing the slop
label appends exactly one well-formed row to the out-of-band feedback log, off the
request path, which the offline `ml/` pipeline then folds back in as a corrected
LEGIT example — the whole loop, end to end.

**Why this priority**: Shadow mode and the feedback loop are the safety valve and the
only production-learning path; both were unit-tested but never observed on the real
process where a stray write or a missed flush would matter most. They are P3 because
the golden path (US2) is the prerequisite wiring and these are refinements on top.

**Independent Test**: Run US2's harness with `SHADOW_MODE=true` and assert the stub
saw the model call but no label/comment/Check-Run writes while the flagged counter
still advanced; separately, deliver a signed `unlabeled` event removing the slop
label, assert exactly one signal row is appended, then run `ml/build_dataset.py
--feedback-log` against that row (with a stubbed GitHub) and confirm it enters the
dataset as a LEGIT example.

**Acceptance Scenarios**:

1. **Given** shadow mode is enabled, **When** a high-confidence-slop delivery is fully
   processed, **Then** the model was called and the flagged counter advanced, but the
   GitHub stub recorded no label, comment, or Check Run.
2. **Given** the gateway flagged a PR and a maintainer later removes the slop label,
   **When** that `unlabeled` delivery is processed, **Then** exactly one row (PR
   identity, original verdict, disagreement type, timestamp) is appended to the
   feedback log and no GitHub write occurs.
3. **Given** a feedback log with one `label-removed` row, **When** the offline
   `ml/build_dataset.py --feedback-log` pipeline runs against a stubbed GitHub,
   **Then** that PR enters the built dataset as a LEGIT row and the leakage assertion
   still passes.

---

### User Story 4 - Operational runbook and failure drills (Priority: P4)

An operator has a written, verified runbook: how to bring the stack up, how to read
health and stats, and exactly how the system behaves under each failure — model down,
GitHub returning 5xx, a missing model artifact, an oversized body, a bad signature.
Each drill in the runbook has a corresponding automated check proving the documented
behavior is the real behavior, so the docs cannot drift from the code (constitution
VIII).

**Why this priority**: The failure modes are individually unit-tested, but an operator
has no single place that says "when X breaks, Sentinel does Y, and here is how to
confirm it." This is documentation-plus-proof: lowest urgency because the behaviors
already exist, highest longevity because it is what an on-call human actually reads.

**Independent Test**: Execute each drill against the assembled stack and assert the
documented outcome — model unreachable → delivery still acked 2xx, PR untouched
(fail-open), failed counter advances; GitHub 5xx through the retry budget → fail-open
within budget; missing artifact → model exits with one actionable line and the gateway
still boots and fails open; oversized body → 413 before HMAC; bad signature → 401.

**Acceptance Scenarios**:

1. **Given** the model service is unreachable, **When** a signed delivery is processed,
   **Then** the gateway acks 2xx, performs no PR mutation, and the `failed` (or
   equivalent) counter advances — a flaky model never blocks a contributor.
2. **Given** the model artifact is absent at startup, **When** the model service
   starts, **Then** it exits with a single actionable line naming the path/command,
   and the gateway still starts and fails open on deliveries.
3. **Given** a body larger than the cap, **When** it is received, **Then** the gateway
   responds 413 before any HMAC or parsing work.
4. **Given** the runbook documents a behavior, **When** its paired drill runs, **Then**
   the observed behavior matches the runbook (docs match shipped code).

### Edge Cases

- Model-stub vs real-artifact selection → the harness must run identically against
  either; the stub returns a fixed, high-confidence slop verdict and a threshold on
  `/healthz` so the gateway's sourcing path is exercised without the torch stack.
- Async completion timing → the harness must wait on an observable completion signal
  (a stub write landing, or a `/stats` counter advancing), never a fixed `sleep`, so
  the test is deterministic and not flaky under load.
- Port already in use → bring-up binds ephemeral/configurable ports and fails with a
  clear message rather than hanging.
- Windows vs POSIX runner → the harness and any scripts must run on the developer's
  Windows host and on the Linux CI runner (the repo already targets both).
- Stub GitHub receiving a Check Run failure injection → the label + comment path must
  still complete (a Check Run failure must not block the other writes, FR-014 of 002).
- No live network available in CI → every 003 check must pass with zero outbound
  internet: all GitHub and (in CI) model interactions are local stubs.
- Redelivery across a gateway restart → documented as a known limitation (the dedup
  ledger is process-local); the drill asserts within a single process only and the
  runbook names the restart caveat rather than pretending otherwise.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The project MUST provide an automated integration harness that starts the
  real gateway binary and a model service (real artifact or a documented stub),
  drives them over real HTTP, and tears them down cleanly, with no live GitHub
  credentials and no outbound internet required.
- **FR-002**: The harness MUST verify stack bring-up: both services reach healthy
  within a bounded startup budget, each health/stats endpoint responds, and the
  gateway has sourced its threshold from the model artifact over the network.
- **FR-003**: The harness MUST drive the golden path with a genuinely HMAC-signed
  `pull_request` delivery and assert the gateway performs exactly one label add, one
  comment (including confidence and model/artifact version), and one Check Run
  (`completed`/`neutral`) against a recording GitHub stub, with an immediate 2xx.
- **FR-004**: The harness MUST assert idempotency end to end: a redelivered
  `X-GitHub-Delivery` within one gateway process produces the outward writes exactly
  once.
- **FR-005**: The harness MUST assert the trust boundary end to end: a signature
  mismatch yields 401 with zero GitHub/model calls, and an oversized body yields 413
  before HMAC.
- **FR-006**: The harness MUST assert shadow mode end to end: with shadow mode on, a
  high-confidence-slop delivery runs the full pipeline (model called, flagged counter
  advances) yet produces zero label/comment/Check-Run writes.
- **FR-007**: The harness MUST assert the feedback loop end to end: a slop-label
  removal appends exactly one well-formed row to the out-of-band feedback log off the
  request path, and no GitHub write occurs.
- **FR-008**: The offline check MUST prove the round trip: a feedback-log row produced
  by the gateway is consumable by `ml/build_dataset.py --feedback-log` (against a
  stubbed GitHub) and enters the dataset as a LEGIT example without breaking the
  leakage invariant.
- **FR-009**: The harness MUST assert fail-open end to end for each downstream failure:
  model unreachable, model 5xx, and GitHub 5xx through the retry budget each result in
  a 2xx ack, no PR mutation, and the correct counter movement within the time budget.
- **FR-010**: The harness MUST assert the missing-artifact behavior: the model service
  exits with a single actionable line and the gateway still boots and fails open.
- **FR-011**: A written operational runbook MUST document bring-up, health/stats
  reading, and each failure drill, and every documented behavior MUST have a paired
  automated check so the runbook cannot drift from the code (constitution VIII).
- **FR-012**: All 003 checks MUST run on both the Windows developer host and the Linux
  CI runner, and the CI-appropriate subset (stub model, no torch) MUST run in CI as a
  distinct job without the multi-GB serving stack.
- **FR-013**: The harness MUST introduce zero third-party Go dependencies (stdlib
  `net/http`, `net/http/httptest`, `os/exec`, `testing` only) and MUST NOT modify the
  serving path; it observes the shipped binary, it does not change it.
- **FR-014**: No 003 check may log or record the diff body, the GitHub token, or the
  webhook secret; the same log-hygiene invariant (002 FR-001) MUST hold in the
  harness's own output.

### Key Entities *(include if feature involves data)*

- **Integration Harness**: the test/driver that boots the gateway (and model or stub),
  signs deliveries, and asserts observed behavior; owns process lifecycle and cleanup.
- **GitHub Stub**: a local recording HTTP server standing in for the GitHub REST API;
  records label/comment/Check-Run calls and can inject 5xx/failures for drills.
- **Model Stub**: a local HTTP server implementing `/healthz` and `/predict` with a
  fixed, configurable verdict and threshold, for CI runs without the torch stack.
- **Signed Delivery Fixture**: a raw `pull_request` payload plus its correct
  `X-Hub-Signature-256` and `X-GitHub-Delivery`, for opened/synchronize and unlabeled.
- **Operational Runbook**: the human-readable document mapping each bring-up and
  failure scenario to its expected behavior and its paired automated check.
- **Drill Result**: the pass/fail outcome of one runbook scenario's automated check.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A single command brings the stack up and returns a deterministic
  healthy/unhealthy result within the startup budget, on both Windows and Linux, with
  no live credentials.
- **SC-002**: The golden-path check proves exactly one label add, one comment (with
  confidence + version), and one Check Run for one signed slop delivery — verified on
  real request bytes captured by the stub, in 100% of runs.
- **SC-003**: A redelivered delivery ID produces the outward writes exactly once in
  100% of trials.
- **SC-004**: A signature mismatch yields 401 with zero downstream calls, and an
  oversized body yields 413 before HMAC, in 100% of trials.
- **SC-005**: With shadow mode on, zero outward writes occur for a high-confidence-slop
  delivery while the flagged counter still advances, in 100% of trials.
- **SC-006**: A slop-label removal appends exactly one well-formed feedback row and
  triggers zero GitHub writes, and that row is consumed by the offline pipeline as a
  LEGIT example with the leakage assertion still passing.
- **SC-007**: Each fail-open drill (model down, model 5xx, GitHub 5xx through budget)
  yields a 2xx ack, no PR mutation, and the expected counter movement within the time
  budget, in 100% of trials.
- **SC-008**: The missing-artifact drill shows the model exiting with one actionable
  line and the gateway still serving and failing open.
- **SC-009**: Every scenario named in the runbook has a paired automated check that
  passes; a grep of harness output for the diff body, token, or secret returns zero
  matches.
- **SC-010**: The CI integration job runs the stub-model subset green without
  installing torch/transformers, adding no third-party Go dependency.

## Assumptions

- Features 001 and 002 have fully landed (async ack-then-process, dedup ledger, body
  cap, shadow mode, Check Run, feedback log, structured logs, `/stats`, threshold
  sourcing). 003 observes those behaviors; it does not reintroduce or modify them.
- The gateway's GitHub API base URL (`GITHUB_API_BASE`, default `https://api.github.com`)
  and model URL (`MODEL_URL`) are already env-overridable, so the real binary can be
  pointed at local stubs with no serving-path change at all — 003 needs no code change
  to the gateway, only env config. Both keep their real-endpoint defaults.
- A "model stub" returning a fixed high-confidence slop verdict and a threshold on
  `/healthz` is an acceptable stand-in for CI; the full artifact path is still
  exercised in the local (non-CI) full-stack run.
- The harness uses only the Go standard library and Python stdlib; the CI integration
  job installs no torch/transformers and pulls nothing over the public internet.
- The dedup ledger is process-local (a 002 known ceiling); 003 documents the
  cross-restart caveat in the runbook rather than treating restart-dedup as in scope.
- The constitution is unchanged: never auto-close, fail-open, precision bar,
  self-hosted model, zero Go deps, HMAC-first, docs-match-code. 003 is pure
  verification-and-documentation on top of the shipped system.
