# Data Model — Sentinel Remediation and Hardening

Phase 1 output. Entities are implementation-shaped only where they exist in
memory or on disk; the serving path has no database (constitution).

## E1. Webhook Delivery *(in-memory, transient)*

One GitHub event delivery accepted at the boundary.

| Field | Type | Rules |
|-------|------|-------|
| DeliveryID | string | From `X-GitHub-Delivery`; empty → malformed request, rejected (edge case EC6). Idempotency key. |
| EventType | string | From `X-GitHub-Event`; only `pull_request` proceeds past the gate (FR-003). |
| Action | string | Must be in {opened, reopened, synchronize} to be actionable. |
| Body | []byte | Raw payload, ≤ MAX_BODY_BYTES (default 25 MiB) enforced before HMAC (FR-006). |
| Signature | string | `X-Hub-Signature-256`; HMAC-SHA256, constant-time compare, checked first (constitution VII). |

**Validation order** (state machine): body cap → signature → event type →
parse → action → bot check → dedup claim → enqueue. Failure at any step is
terminal for that delivery; only cap failure (413) and signature failure (401)
produce error codes — everything else acks 200 and drops (fail-open).

## E2. Delivery Ledger *(in-memory, bounded)*

| Field | Type | Rules |
|-------|------|-------|
| seen | map[DeliveryID]time.Time | Bounded at 1024 entries; oldest evicted on insert. |
| mu | mutex | Claim(DeliveryID) bool is atomic: returns true exactly once per ID per process lifetime. |

**State transitions**: absent → claimed (first delivery, process) |
claimed → suppressed (redelivery, ack 200, no work).
**Ceiling** (ponytail): restart forgets the ledger; a redelivery after restart
may act twice. Upgrade: persist last-seen IDs or pre-check label presence.

## E3. Triage Job *(in-memory, on the worker queue)*

| Field | Type | Rules |
|-------|------|-------|
| PR identity | owner, repo, number | From parsed payload. |
| Title, Author | string | Author already bot-filtered (FR-005). |
| Ctx | context | `context.Background()` + 45s timeout — never `r.Context()` (FR-002). |

**State transitions**: enqueued → processing → (diff fetched → scored → acted) |
any state → dropped (fail-open on any error, FR-007).

## E4. Triage Outcome *(log line only, no persistence)*

| Field | Type |
|-------|------|
| PR identity | owner/repo#number |
| Verdict | slop \| not-slop \| error-dropped |
| Confidence | float [0,1] |
| Threshold used | float [0,1] |
| Action | label+comment \| none \| suppressed-duplicate |
| DeliveryID | string |

## E5. Model Artifact *(on disk, offline-produced)*

| Field | Location | Rules |
|-------|----------|-------|
| Weights + tokenizer | `model/model/` (mounted read-only) | Presence checked at service startup; missing → one actionable log + exit non-zero (FR-008). |
| Selected threshold | `model/model/threshold.json` `{ "threshold": 0.87, "val_precision": 0.88 }` | Produced by training from validation data (FR-010); loaded by the model service and reported on `/healthz`. |

**State transitions**: absent → trained → gated (pass: fit for gateway; fail:
marked unfit, evaluation non-zero exit — FR-012) .

## E6. Evaluation Report *(printed + gates; metrics.json beside artifact)*

| Field | Type |
|-------|------|
| Precision / Recall / F1 (SLOP class) | float, at the selected threshold |
| Confusion matrix | 2×2 ints |
| Selected threshold + val precision | float |
| Pass/fail vs 0.85 bar | bool, drives exit code |

## E7. Dataset Splits *(parquet, offline)*

Rows `{title, diff, label(0=LEGIT,1=SLOP), source(merged|spam|synthetic)}` in
train/validation/test. Invariant (FR-009): no synthetic row's (title, diff)
hash appears in more than one split — asserted at build time; build fails on
collision.

## Relationships

- E1 (accepted) → E3 (enqueued once per E2 claim) → E4 (one outcome per job).
- E5 is produced offline by the E7 → train → gate chain; consumed read-only by
  the serving path, whose verdicts land in E4.
