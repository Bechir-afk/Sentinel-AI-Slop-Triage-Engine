<!--
Sync Impact Report
- Version change: (none) -> 1.0.0
- Initial ratification; no prior constitution existed.
- Principles added (8): Never Auto-Close, Fail-Open, Precision Over Recall,
  Classify Value Not Provenance, Zero Third-Party Go Dependencies,
  Self-Hosted Model, Trust Boundary Verification, Docs Match Shipped Code.
- Sections added: Quality Gates, Development Workflow.
- Deferred TODOs: none.
-->

# Sentinel — AI-Slop Triage Engine Constitution

## Core Principles

### I. Never Auto-Close (NON-NEGOTIABLE)
The system MUST NOT close, merge, dismiss, or otherwise block a pull request.
The only permitted actions on a flagged PR are adding a label and posting a
comment. A human maintainer always stays in the loop.

### II. Fail-Open
Any downstream failure — diff fetch error, model service timeout or 5xx,
label/comment post failure — MUST result in no PR mutation and a 2xx webhook
response. The bot MUST NEVER block or break a contributor's flow. Degrading to
"no action" is always the correct fallback.

### III. Precision Over Recall (NON-NEGOTIABLE)
SLOP-class precision MUST clear 0.85 on a held-out, leakage-free test set
before a model artifact is trusted in the gateway. False positives on real
contributors are the expensive error. Improving recall MUST NOT come at the
cost of dropping below the precision bar. Evaluation MUST NOT train and test
on synthetic rows generated from shared templates (shortcut leakage).

### IV. Classify Value, Not Provenance
The system MUST classify the value of a change (superficial, boilerplate,
hallucinated) and MUST NOT attempt to detect whether AI was involved.
Legitimate PRs are often AI-assisted; provenance detection inflates false
positives and is out of scope.

### V. Zero Third-Party Go Dependencies
The gateway MUST remain stdlib-only (`net/http`, `crypto/*`, `encoding/*`).
Any change that adds a Go module dependency MUST be rejected unless the
constitution is explicitly amended.

### VI. Self-Hosted Model
The serving path MUST NOT call an external AI API. The model runs locally
(offline) and the gateway talks to it over plain HTTP JSON on an internal
network. Training and data collection happen offline, outside the serving path.

### VII. Trust Boundary Verification
Every webhook delivery MUST be authenticated with HMAC-SHA256 over the raw
body, compared in constant time, before any parsing or action. Unauthenticated
requests MUST be dropped. Request bodies MUST be size-capped at the trust
boundary. Event type and action MUST be validated before processing.

### VIII. Docs Match Shipped Code
README, AGENTS.md, and PROJECT_MAP MUST describe the architecture that is
actually shipped — no stale references to removed components, credentials,
or dependencies. A doc that contradicts the code is a defect.

## Quality Gates
- `go build ./...`, `go vet ./...`, and `go test ./...` MUST pass before merge.
- `ml/evaluate.py` MUST exit 0 (precision >= bar) before a model artifact is
  deployed into the gateway.
- The model service MUST NOT crash-loop when its artifact is absent; absence
  MUST be a clear, logged startup failure or a graceful degradation.
- No PR may be merged with failing tests or a failing evaluation gate.

## Development Workflow
- One feature per spec; every feature flows through
  `/speckit-specify` -> `/speckit-plan` -> `/speckit-tasks` ->
  (optional `/speckit-analyze`) -> `/speckit-implement`.
- Changes to the serving path require tests demonstrating both the success
  path and the fail-open path.
- Security-relevant changes (verification, auth, container hardening) require
  a second review pass.

## Governance
This constitution supersedes all other practices in this repository. All
changes and reviews MUST verify compliance with the principles above.
Amendments require documentation of what changed, why, and a migration note
for any affected code; version follows semantic versioning (MAJOR on removal
or redefinition of a principle, MINOR on addition, PATCH on clarification).

**Version**: 1.0.0 | **Ratified**: 2026-09-07 | **Last Amended**: 2026-09-07
