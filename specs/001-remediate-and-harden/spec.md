# Feature Specification: Sentinel Remediation and Hardening

**Feature Branch**: `001-remediate-and-harden`

**Created**: 2026-09-07

**Status**: Draft

**Input**: User description: "Make Sentinel deployable and trustworthy: webhook ingestion robustness, valid ML evaluation, container deployability, documentation truth, and CI continuity — while keeping every constitution principle (never auto-close, fail-open, precision over recall, value-not-provenance, zero third-party Go deps, self-hosted model, HMAC verification first, docs match code)."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Maintainer runs the full system locally (Priority: P1)

A maintainer with a GitHub token and a webhook secret runs both Sentinel
services locally and gets a healthy deployment, even before a model artifact
exists. When the model artifact is missing, the model service reports a clear,
actionable startup failure instead of silently restarting forever; when the
artifact exists, both services report healthy.

**Why this priority**: Today the deployment cannot come up at all — the model
service crash-loops without explanation and the gateway never starts because it
waits on the model's health. Nothing else matters until `up` works.

**Independent Test**: With no artifact present, start the stack; the operator
sees one clear error message naming the missing artifact and how to produce it.
With a placeholder artifact present, both services report healthy.

**Acceptance Scenarios**:

1. **Given** no model artifact on disk, **When** the model service starts,
   **Then** it logs a single clear message identifying the missing artifact and
   the command that produces it, and exits (or stays unhealthy with that reason)
   instead of restarting in a loop with a stack trace.
2. **Given** a valid model artifact, **When** the stack starts, **Then** both
   services become healthy and the gateway accepts webhook deliveries.
3. **Given** the model service is unreachable, **When** a valid pull request
   webhook arrives, **Then** the gateway still acknowledges it successfully and
   takes no action on the PR (fail-open).

---

### User Story 2 - GitHub gets a fast, safe webhook response (Priority: P2)

GitHub sends a pull request event. The gateway acknowledges the delivery
quickly — well within GitHub's 10-second limit — and does its work (fetch diff,
consult model, label, comment) without holding the response open and without
losing that work if the connection drops. Duplicate deliveries of the same
event (redeliveries) never produce a second label or second comment. Pull
requests authored by bot accounts are left alone. Events that are not pull
request events are ignored without side effects.

**Why this priority**: This is the core product behavior; without it the bot
times out, duplicates its own comments, and reacts to irrelevant events.

**Independent Test**: Send signed webhook deliveries (real event types and
duplicates) at a locally running gateway and observe labels/comments on a mock
or real repository.

**Acceptance Scenarios**:

1. **Given** a signed `pull_request` event with action `opened`, **When** it is
   delivered, **Then** the gateway responds successfully in under 1 second and
   the PR is triaged in the background.
2. **Given** the same delivery identifier delivered twice, **When** both are
   processed, **Then** exactly one label and one comment result.
3. **Given** a signed event of a different type (e.g. an issue event), **When**
   it is delivered, **Then** it is acknowledged and ignored — no API calls are
   made against any pull request.
4. **Given** a pull request authored by a bot account, **When** the event is
   processed, **Then** no label or comment is posted.
5. **Given** a webhook body larger than the configured maximum, **When** it is
   delivered, **Then** the request is rejected before any processing.
6. **Given** a client disconnects immediately after delivering, **When** the
   gateway processes the event, **Then** triage still completes.

---

### User Story 3 - Maintainer trusts the model evaluation (Priority: P3)

A maintainer training the model sees honest metrics: training and test examples
share no synthetic templates, the decision threshold is chosen from held-out
data to meet the precision bar, and the reported numbers are not inflated by
leakage. The training run itself reports precision/recall/F1 and the confusion
matrix, so a weak model is visible during training, not after.

**Why this priority**: The current evaluation gate can report ~1.0 precision
while generalizing to nothing — the most dangerous defect in the project
because it silently violates the precision-over-recall constitution principle.

**Independent Test**: Run the dataset builder and training; verify test-set
synthetic rows are generated from disjoint templates/seeds than train rows, and
that the reported threshold comes with a validation-set precision at or above
the bar.

**Acceptance Scenarios**:

1. **Given** the dataset is built, **When** train and test synthetic examples
   are compared, **Then** no template collision exists between them.
2. **Given** a trained model, **When** the threshold is selected, **Then** it
   is the lowest confidence that meets the precision bar on held-out validation
   data, and the selection is logged with its measured precision.
3. **Given** a training run, **When** it completes, **Then** precision, recall,
   F1, and the confusion matrix are reported.
4. **Given** a model that cannot meet the precision bar, **When** evaluation
   runs, **Then** the gate fails loudly and the artifact is marked unfit for
   the gateway.

---

### User Story 4 - Operator deploys hardened containers (Priority: P4)

An operator reviewing the deployment finds both containers run as non-root
with dropped Linux capabilities, a read-only root filesystem where feasible,
resource limits, CPU-only runtime dependencies (no multi-GB accelerator
packages), and healthchecks on both services.

**Why this priority**: Hardening and image size are real but secondary to the
system actually running and behaving correctly.

**Independent Test**: Inspect the running containers' user, capabilities,
mounts, and image size; both report healthy.

**Acceptance Scenarios**:

1. **Given** the built images, **When** inspected, **Then** both run as a
   non-root user and the model image contains no GPU-accelerator packages.
2. **Given** the running stack, **When** health is queried, **Then** both
   services expose a working healthcheck.

---

### User Story 5 - Newcomer reads docs that match reality (Priority: P5)

A newcomer reading the README and agent instructions sees the architecture
that is actually shipped (self-hosted model, no external AI API key), a list of
required environment variables with an example file, and no claims the code
contradicts. Every push runs the automated checks.

**Why this priority**: Truthful docs and CI prevent regression of everything
above; they are the cheapest stories and the last line of defense.

**Independent Test**: A reviewer diffs doc claims against the code and finds no
stale architecture references; a push to the repository runs build, vet, and
tests automatically.

**Acceptance Scenarios**:

1. **Given** the rewritten documentation, **When** searched, **Then** no
   reference to the removed external AI provider or its API key remains.
2. **Given** the example environment file, **When** a maintainer copies it and
   fills in real values, **Then** the stack starts.
3. **Given** any push, **When** CI runs, **Then** build, vet, and the full
   gateway test suite execute and report.

### Edge Cases

- Webhook delivered with an invalid signature → rejected with 401 before any
  parsing or processing (constitution VII).
- Two different PR events processed concurrently → both complete independently;
  duplicate detection must not suppress distinct events.
- Model service slow (near timeout) → gateway gives up within its budget and
  fails open; the PR is untouched.
- Model returns malformed or out-of-range confidence → treated as an error,
  fail-open, no action.
- A redelivery (same delivery ID) arrives while the first is still processing →
  still exactly one label/comment result.
- Empty or missing event-type header → request rejected as malformed, no side
  effects.
- Very large diffs → truncated for scoring as today; no change in action.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST acknowledge each authenticated webhook delivery
  in under 1 second, independent of how long triage takes.
- **FR-002**: The system MUST process pull request events in the background,
  decoupled from the client connection, so a client disconnect does not cancel
  triage.
- **FR-003**: The system MUST validate the event type header and only act on
  pull request events with an actionable action; all other events MUST be
  acknowledged and ignored with no API side effects.
- **FR-004**: The system MUST treat the delivery identifier as an idempotency
  key: a repeated delivery MUST NOT produce a second label or comment.
- **FR-005**: The system MUST skip triage for pull requests authored by
  automation (bot) accounts.
- **FR-006**: The system MUST reject request bodies larger than a configurable
  maximum before signature verification processing.
- **FR-007**: On any error in the triage pipeline (diff fetch, model call,
  action posting), the system MUST take no PR action and MUST NOT report an
  error to GitHub (fail-open, constitution II).
- **FR-008**: The model service MUST NOT silently restart forever when its
  artifact is missing; it MUST produce one clear, actionable error identifying
  the artifact and how to produce it.
- **FR-009**: The dataset builder MUST generate synthetic examples so that no
  template is shared between training and evaluation splits.
- **FR-010**: The confidence threshold MUST be selected from held-out
  validation data as the lowest value meeting the precision bar, and the
  measured precision at the chosen threshold MUST be reported.
- **FR-011**: Training MUST report precision, recall, F1, and the confusion
  matrix at the end of each run.
- **FR-012**: The evaluation gate MUST fail (non-zero exit, artifact marked
  unfit) when precision at the selected threshold is below the bar.
- **FR-013**: Both services MUST run as non-root in their containers, with
  dropped capabilities and a read-only root filesystem where feasible.
- **FR-014**: The model image MUST NOT include GPU-accelerator packages; the
  runtime dependency set MUST be CPU-only.
- **FR-015**: Both services MUST expose a working healthcheck.
- **FR-016**: Documentation MUST describe the shipped architecture with no
  references to removed external AI providers or their credentials, and MUST
  include an example environment file covering every required variable.
- **FR-017**: Every push MUST run the automated checks (build, vet, gateway
  test suite) via continuous integration.

### Key Entities *(include if feature involves data)*

- **Webhook Delivery**: one GitHub event delivery; attributes: event type,
  action, delivery identifier (idempotency key), raw payload, signature.
- **Triage Outcome**: the decision record for one PR; attributes: PR
  identity, verdict (slop or not), confidence, threshold used, action taken
  (label/comment/none), deduplicated flag.
- **Delivery Ledger**: the record of recently seen delivery identifiers used
  for idempotency; bounded size, in-memory (no persistence — out of scope per
  the no-database stance).
- **Model Artifact**: the trained model bundle; attributes: version, selected
  threshold, evaluation metrics, fitness-for-gateway flag.
- **Evaluation Report**: metrics for one artifact; attributes: precision,
  recall, F1, confusion matrix, selected threshold, pass/fail against the bar.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: With a model artifact present, both services report healthy and
  `docker compose up` reaches a ready state; without one, the operator sees
  exactly one actionable error within the first startup attempt.
- **SC-002**: 100% of authenticated webhook deliveries are acknowledged in
  under 1 second under normal load (target p99 < 1s, hard ceiling well below
  GitHub's 10-second limit).
- **SC-003**: Zero duplicate labels or comments result from redelivered events
  in testing (same delivery identifier delivered up to 3 times).
- **SC-004**: Zero pull requests are mutated as a result of any injected
  pipeline failure (fail-open holds in 100% of failure tests).
- **SC-005**: The reported test-set precision is measured on a leakage-free
  split, and the deployed threshold is documented with its validation-set
  precision at or above 0.85.
- **SC-006**: Model container image is at least 60% smaller than the current
  build once GPU packages are excluded.
- **SC-007**: Documentation contains zero references to the removed external
  AI provider or its API key (verifiable by search).
- **SC-008**: Every push to the repository runs build + vet + tests with
  results visible on the commit within CI.

## Assumptions

- The single-node, no-database deployment stance stands; idempotency uses an
  in-memory bounded ledger, accepting that a gateway restart can reprocess a
  redelivery (documented ceiling, per the project's simplicity principle).
- GitHub remains the only source of events and actions; other forges are out
  of scope.
- A GPU is not available or required for serving; CPU inference is sufficient
  for the expected PR arrival rate.
- Human labeling capacity for real (non-synthetic) slop examples is limited;
  the dataset will remain mostly synthetic, so leakage control matters more
  than dataset growth.
- The 0.85 precision bar and the "label + comment only" action set are fixed
  by the constitution and out of scope for change.
- Training and data collection remain offline, one-time activities outside the
  serving path.
