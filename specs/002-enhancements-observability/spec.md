# Feature Specification: Sentinel Observability & Enhancements

**Feature Branch**: `002-enhancements-observability`

**Created**: 2026-09-19

**Status**: Draft

**Input**: User description: "Turn the enhancement audit into a plan: give Sentinel
operational visibility (structured logs, metrics, version), safe-rollout controls
(shadow mode, richer comments), serving-quality fixes (model warmup, correct
title/diff encoding, transient-error retry), defense-in-depth security
(path-escaping, safetensors, pinned images, vuln scan), and the higher-value
product features (GitHub Check Run, maintainer feedback loop) — all without
touching a constitution principle."

**Relationship to prior work**: This feature is the second tier on top of
`001-remediate-and-harden`. 001 makes Sentinel deployable, fast, idempotent, and
honestly evaluated. 002 assumes 001 has landed (async ack-then-process, the
delivery-ID ledger, body cap, non-root containers, leakage-free evaluation) and
adds the polish and features that make the running system observable, safe to
roll out, and more useful. Nothing here duplicates a 001 task; where a file 001
already touches is extended, the task says so.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Operator can see what the bot is doing (Priority: P1)

An operator running the two services can answer "what did Sentinel do, and how
long did it take?" from logs and one endpoint — without attaching a debugger.
Every log line is structured (machine-parseable) and carries the GitHub delivery
ID, so a single PR's journey through the gateway and the model service can be
traced end to end. A stats endpoint reports how many PRs were received, triaged,
flagged, skipped, and failed, plus triage latency. Each service reports its build
version.

**Why this priority**: Today logging is unstructured `log.Printf` with no
correlation and there are no metrics, so an operator cannot tell a healthy system
from a silently-failing one. Visibility is the foundation everything else is
debugged against.

**Independent Test**: Send signed deliveries at a locally running gateway; confirm
each log line is valid structured output carrying the delivery ID, the same ID
appears in the model service's log for that request, and the stats endpoint
counters advance correctly.

**Acceptance Scenarios**:

1. **Given** a webhook delivery is processed, **When** its log lines are read,
   **Then** every line is structured (key/value or JSON), includes the delivery
   ID, and no line contains the diff body or any secret.
2. **Given** the gateway calls the model service for a delivery, **When** the
   model service logs its verdict, **Then** its log carries the same correlation
   ID the gateway used for that delivery.
3. **Given** a mix of flagged, skipped, and failed triages, **When** the stats
   endpoint is queried, **Then** it returns counts that match what happened and a
   triage-latency summary.
4. **Given** either service is running, **When** its health/stats endpoint is
   queried, **Then** the response includes the build version of that service.

---

### User Story 2 - Maintainer can roll Sentinel out safely (Priority: P2)

A maintainer enabling Sentinel on a real repository first runs it in shadow mode:
the full pipeline runs and logs the verdict it *would* have posted, but no label
or comment is written. Once the verdicts look trustworthy, they flip a single
switch to enable acting. When Sentinel does comment, the comment states the
model's confidence and version so a maintainer can calibrate trust and report a
bad call precisely.

**Why this priority**: A slop bot that comments wrongly on real contributors is
the expensive failure (constitution III). Shadow mode is the safety valve that
lets a maintainer trust the model on their own traffic before it speaks, and a
versioned comment makes every action auditable.

**Independent Test**: With shadow mode on, deliver a high-confidence-slop event
and confirm the verdict is logged but no label/comment API call is made; turn
shadow mode off and confirm the label + comment are posted and the comment text
contains the confidence and model version.

**Acceptance Scenarios**:

1. **Given** shadow mode is enabled, **When** a PR scores above the threshold,
   **Then** the verdict is logged and no label or comment is posted.
2. **Given** shadow mode is disabled (default), **When** a PR scores above the
   threshold, **Then** the label and comment are posted as today.
3. **Given** a comment is posted, **When** it is read, **Then** it includes the
   model's confidence and the model/artifact version, in addition to the reason.

---

### User Story 3 - Serving is efficient and correct on real inputs (Priority: P3)

The model service is fast on its first request (not just after warmup) and does
not oversubscribe CPU threads. The (title, diff) it scores uses the tokenizer's
real separator and reserves token budget for the diff, so a pathologically long
PR title can never crowd the diff out of the 512-token window. When GitHub
returns a transient error (5xx or a rate-limit response with a retry hint), the
gateway retries within its time budget before falling back to fail-open, so a
brief GitHub blip no longer silently drops a triage.

**Why this priority**: These are correctness-and-cost refinements to a system
that already works. The encoding fix removes a real modeling foot-gun (a literal
`[SEP]` string that is not the model's separator token); warmup and thread
control remove first-request latency and CPU contention; bounded retry recovers
triages that fail-open would otherwise waste — without weakening fail-open as the
final fallback.

**Independent Test**: Measure first-request latency before/after warmup; send a
PR with a 10 KB title and confirm the diff still contributes tokens to the input;
point the gateway at a GitHub stub that returns 503 twice then 200 and confirm the
diff is fetched after retry; confirm a stub that returns 503 forever still
fails open within the time budget.

**Acceptance Scenarios**:

1. **Given** the model service has just started, **When** the first real request
   arrives, **Then** it is served within the normal latency budget (warmup already
   done at startup).
2. **Given** a request with an extremely long title, **When** it is encoded,
   **Then** the diff still contributes tokens (title is bounded; the real
   separator token is used, not a literal `[SEP]` string).
3. **Given** GitHub returns 503 then 503 then 200 for a diff fetch, **When** the
   gateway processes the event, **Then** it retries within its budget and triages
   the PR.
4. **Given** GitHub returns 503 for the entire retry budget, **When** the budget
   is exhausted, **Then** the gateway fails open (no action, 2xx already sent).

---

### User Story 4 - Defense-in-depth security (Priority: P4)

A security reviewer finds the remaining sharp edges filed down: repository owner
and name from the payload are escaped before they are placed in an API path; the
model artifact is stored and loaded in a format that cannot execute code on load;
container base images are pinned by digest so a rebuild is reproducible; secrets
never enter a build context; and CI scans dependencies for known vulnerabilities.

**Why this priority**: 001 already hardened the containers (non-root, cap-drop,
read-only) and capped the body at the trust boundary. These are the lower-severity,
defense-in-depth items that harden the supply chain and the last input-handling
edges after the high-severity work is done.

**Independent Test**: Inspect the built images for digest-pinned bases; confirm the
artifact loads from a non-pickle format; run the CI vulnerability step against a
known-bad pin and confirm it fails; attempt a crafted owner/repo value and confirm
it is escaped, not injected into the path.

**Acceptance Scenarios**:

1. **Given** the model artifact, **When** it is loaded, **Then** it loads from a
   safe (non-executable) weights format and no pickle is deserialized.
2. **Given** owner/repo values from a payload, **When** an API URL is built,
   **Then** each segment is percent-escaped (a `/` or `..` in a segment cannot
   change the target path).
3. **Given** the Dockerfiles, **When** inspected, **Then** every base image is
   pinned by digest.
4. **Given** a CI run, **When** the dependency scan executes, **Then** a known
   vulnerable dependency fails the build.

---

### User Story 5 - Higher-value UX and a feedback loop (Priority: P5)

A maintainer sees Sentinel's verdict as a first-class GitHub Check Run on the PR
(re-runnable, visible in the checks list) in addition to the label and comment.
When a maintainer disagrees — removing the label or reacting 👎 to the comment —
that disagreement is captured, out of band, into an append-only signal log the
offline training pipeline can later fold back in, so the model improves from real
maintainer judgment. The serving path stays stateless: the signal log is written
outside the request path and read only by the offline `ml/` pipeline.

**Why this priority**: These are the largest-effort, highest-ceiling features.
The Check Run is nicer UX; the feedback loop is the only path to a model that
improves from production. Both are valuable but neither is needed for a correct,
observable, safe system, so they come last and one (feedback) requires a
deliberate constraint decision (below).

**Independent Test**: Deliver a slop event and confirm a Check Run with the verdict
appears alongside the label; simulate a maintainer removing the label and confirm
one row is appended to the signal log with the PR identity and the original
verdict; confirm the serving path holds no state across restarts.

**Acceptance Scenarios**:

1. **Given** a triaged PR, **When** the verdict is high-confidence slop, **Then** a
   Check Run reporting the verdict (and confidence) is created on the PR head,
   alongside the existing label + comment.
2. **Given** a maintainer removes the slop label (or reacts 👎), **When** that
   event is received, **Then** a single row capturing the PR identity, the original
   verdict, and the disagreement is appended to the out-of-band signal log.
3. **Given** the gateway restarts, **When** it comes back, **Then** it holds no
   per-PR state in memory beyond the bounded dedup ledger (the signal log is a
   file/artifact, not serving-path state).

### Edge Cases

- Stats endpoint queried before any delivery → returns zero counters, not an error.
- Correlation ID absent (non-GitHub caller that passed HMAC) → a generated ID is
  used so logs still correlate; never crash on a missing header.
- Shadow mode on AND Check Run enabled → the Check Run is also suppressed (shadow
  means *observe only*, no outward writes of any kind).
- Retry budget vs GitHub `Retry-After` longer than the whole triage budget → do not
  sleep past the budget; fail open instead of blocking a worker.
- Long title AND large diff together → title is capped first, then the diff fills
  the remaining budget; total stays within the model's max tokens.
- Feedback event for a PR Sentinel never flagged → recorded as a no-op or ignored;
  never fabricate an original verdict.
- Check Run API unavailable → fail-open like every other GitHub write; the label +
  comment path is independent and must not be blocked by a Check Run failure.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: All gateway and model-service logs MUST be structured
  (machine-parseable) and MUST NOT contain the diff body, the GitHub token, or the
  webhook secret.
- **FR-002**: Each processed delivery MUST be logged with the GitHub delivery ID as
  a correlation key, and that same ID MUST be propagated to the model service and
  appear in its logs for the corresponding request.
- **FR-003**: The gateway MUST expose a stats/metrics endpoint reporting at least:
  deliveries received, triaged, flagged, skipped, and failed, plus a triage-latency
  summary. The endpoint MUST NOT require the webhook secret and MUST NOT expose PR
  content.
- **FR-004**: Each service MUST report its build version (via its health or stats
  endpoint).
- **FR-005**: The gateway MUST support a shadow (dry-run) mode, controlled by
  configuration, in which the full pipeline runs and the would-be verdict is logged
  but NO label, comment, or Check Run is written. Shadow mode MUST default to off.
- **FR-006**: When Sentinel posts a comment, the comment MUST include the model's
  confidence and the model/artifact version in addition to the reason.
- **FR-007**: The model service MUST perform a warmup inference at startup and MUST
  bound its CPU thread count via configuration.
- **FR-008**: The model service MUST encode the (title, diff) pair using the
  tokenizer's real separator and MUST bound the title's token contribution so the
  diff always receives token budget within the model's maximum length.
- **FR-009**: On a transient GitHub error (HTTP 5xx, or 403/429 carrying a
  rate-limit/retry signal), the gateway MUST retry within its triage time budget
  before falling back to fail-open. Retries MUST NOT extend past the budget and
  MUST honor a `Retry-After` only up to the remaining budget.
- **FR-010**: Repository owner and name taken from the payload MUST be
  percent-escaped before being interpolated into any GitHub API path.
- **FR-011**: The model artifact MUST be stored and loaded in a weights format that
  does not execute code on load (no pickle deserialization at serve time).
- **FR-012**: Every container base image MUST be pinned by digest.
- **FR-013**: CI MUST run a dependency vulnerability scan (Go and Python) and MUST
  fail the build on a known-vulnerable dependency.
- **FR-014**: Sentinel MUST create a GitHub Check Run reporting the verdict
  (and confidence) on a flagged PR, alongside the label and comment. A Check Run
  failure MUST fail open and MUST NOT block the label/comment path.
- **FR-015**: When a maintainer disagrees with a verdict (removes the slop label or
  reacts 👎 to Sentinel's comment), the system MUST append one row — PR identity,
  original verdict, disagreement type — to an out-of-band signal log. This log MUST
  be written outside the request path and MUST NOT introduce serving-path state.
- **FR-016**: The offline `ml/` pipeline MUST be able to read the signal log as an
  additional labeled data source for retraining.

### Key Entities *(include if feature involves data)*

- **Log Record**: one structured log line; attributes: timestamp, level, message,
  correlation (delivery) ID, and safe fields only (never diff/secret).
- **Stats Snapshot**: current counters and latency summary; in-memory, reset on
  restart (no persistence).
- **Correlation ID**: the GitHub delivery ID (or a generated fallback) threaded
  from gateway ingress through the model call.
- **Triage Verdict (extended)**: the existing verdict plus the model/artifact
  version and the confidence, as surfaced in the comment and Check Run.
- **Feedback Signal**: one maintainer-disagreement record; attributes: PR identity,
  original verdict + confidence, disagreement type (label-removed / thumbs-down),
  timestamp. Append-only, out-of-band, consumed only by `ml/`.
- **Model Artifact (extended)**: the trained bundle in a non-pickle weights format,
  carrying its version.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of processed deliveries produce structured log lines carrying the
  delivery ID; a grep for the diff body or a secret in logs returns zero matches.
- **SC-002**: For any single delivery, its correlation ID is present in both the
  gateway and model-service logs (traceable end to end).
- **SC-003**: The stats endpoint's counters match an injected sequence of
  flagged/skipped/failed triages exactly in testing.
- **SC-004**: With shadow mode on, zero label/comment/Check-Run API calls are made
  for a high-confidence-slop event; with it off, all three occur.
- **SC-005**: First-request model latency after startup is within the same budget as
  a warmed request (no cold-start penalty on the first real PR).
- **SC-006**: A PR with a title larger than the token window still yields a nonzero
  number of diff tokens in the encoded input.
- **SC-007**: With GitHub returning 503 twice then 200, the diff is fetched and the
  PR triaged; with 503 throughout, the gateway fails open within its budget in 100%
  of trials.
- **SC-008**: A crafted owner/repo containing a path separator cannot reach a
  different API path (verified by test).
- **SC-009**: CI fails when a dependency with a known advisory is introduced.
- **SC-010**: A simulated maintainer disagreement appends exactly one well-formed row
  to the signal log, and the gateway holds no per-PR serving state across a restart.

## Assumptions

- Feature 001 (async ingestion, delivery-ID dedup ledger, body cap, non-root
  hardened containers, leakage-free evaluation, `.env.example`, CI build/vet/test)
  has landed; 002 extends those files rather than reintroducing their changes.
- The stats endpoint is unauthenticated and safe to expose only on the internal /
  operator network, matching the model service's isolation posture; it exposes
  counts and latencies only, never PR content.
- Structured logging and the stats endpoint use only the Go standard library
  (`log/slog`, `sync/atomic`) — the zero-third-party-Go-dependency principle holds.
- The feedback loop's persistence is an **out-of-band, append-only signal log**
  (a file or artifact written off the request path), NOT a serving-path database or
  queue. This is a deliberate reading of the "no persistence in the serving path"
  stance: the serving path stays stateless; only the offline `ml/` pipeline reads
  the log. If the maintainer rejects any serving-adjacent persistence at all, US5's
  feedback half (FR-015/016) is deferred and the Check Run half still ships.
- Receiving label-removed / reaction events requires the GitHub webhook to also send
  those event types; enabling them is a repo configuration step documented in the
  quickstart, not a code change.
- The 0.85 precision bar, the "label + comment only — never auto-close" action set,
  and self-hosted serving are fixed by the constitution and unchanged here. A Check
  Run reports a verdict; it does not gate or block the PR (no required status).
