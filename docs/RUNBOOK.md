# Sentinel Operational Runbook

On-call reference for the Sentinel two-service stack (Go gateway + self-hosted
Python model). Every failure scenario below names the **automated drill that
proves it** — a paired check in `test/integration/` (or, for the offline path,
`ml/tests/`). `TestRunbookScenariosHaveChecks` in
[test/integration/drills_test.go](../test/integration/drills_test.go) fails the
build if any scenario named here loses its paired test, so this page cannot
drift from the code (constitution VIII, FR-011).

> Run the drills yourself: `go test ./test/integration/...`. The local run
> exercises the real gateway binary against in-process stubs; CI runs the same
> tests as a torch-free subset. See [Running Tests](../README.md#-running-tests).

---

## 1. Bring-up

**Prerequisites**: a trained artifact at `model/model/` (or the model service
refuses to start — see the missing-artifact drill), `GITHUB_WEBHOOK_SECRET`, and
`GITHUB_TOKEN`. Copy `.env.example` → `.env` and fill those two in.

```bash
docker compose up -d
docker compose ps                 # both services → healthy
curl localhost:8080/healthz       # → {"status":"ok","version":"<build>"}
curl localhost:8080/stats         # → all counters zero on a fresh boot
```

**Healthy looks like**: the gateway answers `/healthz` with `200` and a non-empty
build `version`, and its startup log carries `threshold sourced from model
artifact` (the operating threshold came from the model's `/healthz`, not the
`0.95` fallback). If the model was down at gateway start, you'll instead see
`model healthz unavailable; using default threshold` — the gateway still boots
and serves; it just uses the fallback threshold.

| Scenario | Expected behavior | Paired check |
|---|---|---|
| Both services boot; fresh `/stats` is all-zero and leaks no secret/PR content | gateway + model reach healthy within the startup budget; `/healthz` carries a build version | `TestBringUpHealthyAndZeroedStats` |
| Threshold is sourced from the model artifact, not the fallback | with `CONFIDENCE_THRESHOLD` unset, the operating threshold is the model's advertised value | `TestBringUpThresholdSourcedFromArtifact` |

---

## 2. Reading `/healthz` and `/stats`

Neither endpoint requires a secret, and neither exposes any PR content.

**`GET /healthz`** → `{"status":"ok","version":"<build>"}`. Liveness only; a
`200` means the gateway process is up and serving. Compose uses it as the
healthcheck.

**`GET /stats`** → the in-memory operational counters (they reset on restart —
see the caveat in §4):

| Field | Meaning |
|---|---|
| `received` | deliveries accepted and enqueued for triage (dupes are **not** counted) |
| `triaged` | model returned a verdict — this equals `flagged + skipped` |
| `flagged` | verdict was slop above threshold → label + comment written |
| `skipped` | verdict was not actionable (legit, or below threshold) |
| `failed` | pipeline errored on the **diff fetch** or **model call** → failed open |
| `triage_mean_ms` / `triage_max_ms` / `triage_samples` | triage-latency summary |

**Counter semantics that matter on-call**: a write-side error (a failed
label/comment/Check Run) is logged but does **not** advance `failed` — only a
stage-3 (diff fetch) or stage-4 (model) error does. So `failed` is your honest
"a downstream we depend on to reach a verdict is broken" signal; a rising
`flagged` with a Check Run error in the logs is the far milder "we spoke, but the
observational check didn't post" case (see the Check-Run drill below).

---

## 3. Failure drills

Sentinel's core promise under failure is **fail-open**: a broken downstream never
makes Sentinel speak wrongly and never blocks a contributor. Every ack is still
an immediate `2xx`; the PR is simply left untouched. Each row is proven by the
named paired check.

### 3.1 Downstream failures (fail-open)

For all three, the observable signal is the same: the webhook still acks `200`
immediately, the `failed` counter advances by 1, and **zero** GitHub writes land.

| Scenario | What you see | Expected behavior | Paired check |
|---|---|---|---|
| **Model unreachable** | model service down / `MODEL_URL` unroutable; logs `triage failed; failing open` | `200` ack, `failed`++, no label/comment/Check Run | `TestFailOpenDrills` (`model unreachable`) |
| **Model 5xx** | model reachable but `/predict` returns `500` through the retry budget; logs `triage failed; failing open` | `200` ack, `failed`++, no writes | `TestFailOpenDrills` (`model 5xx`) |
| **GitHub 5xx (diff fetch)** | `GET pulls/{n}` returns `500` through `GITHUB_MAX_RETRIES`; logs `fetch diff failed; failing open` | `200` ack, `failed`++, no writes (model never even called) | `TestFailOpenDrills` (`github 5xx`) |

> **Why the GitHub drill injects on the diff *read*, not a write**: a write-side
> 5xx (label/comment/Check Run) is logged non-fatally and never advances
> `failed`. The diff fetch is stage 3 — a read — so a `500` there is the honest,
> observable fail-open signal. See §3.3 for the write-side (Check Run) behavior.

### 3.2 Missing model artifact (fail-fast, with one actionable line)

Point the model service at an artifact directory with no weights and it must
**refuse at import** with exactly one actionable line — never a crash-looping
Hugging Face stack trace:

```bash
rm -rf model/model
docker compose up sentinel-model
docker compose logs sentinel-model | tail -3
# → model artifact unusable at MODEL_PATH=…: … Produce it offline with
#   `python ml/train.py --data ml/dataset --out model/model`
```

The line names both the offending `MODEL_PATH=` and the exact command that
produces the artifact, then the container exits non-zero (no crash-loop). To the
gateway, a model that exited this way is simply an unreachable `MODEL_URL`, so
the gateway still boots and fails open on deliveries — the same behavior as
`TestFailOpenDrills` (`model unreachable`), not re-asserted separately.

| Scenario | Expected behavior | Paired check |
|---|---|---|
| **Missing artifact** | model exits non-zero with ONE line naming `MODEL_PATH=` + the produce command; no stack trace | `TestMissingArtifactFailsFastWithOneLine` |

### 3.3 Check Run failure must not block the label/comment

The Check Run is the least critical of the three writes — observational, never
gating. If it `5xx`s, the label and comment must still land, and the delivery
must **not** count as a failed triage (a write-side error is logged, not
failed-open):

| Scenario | Expected behavior | Paired check |
|---|---|---|
| **Check Run 5xx** | label + comment still written; `flagged`++, `failed` stays 0 | `TestCheckRunFailureDoesNotBlockLabelComment` |

### 3.4 Trust-boundary rejections

Both fire **before** any triage work, at the edge, and make zero GitHub/model
calls:

| Scenario | Expected behavior | Paired check |
|---|---|---|
| **Oversized body** | body over `MAX_BODY_BYTES` → `413`, before any HMAC/parse work (even a validly-signed body) | `TestGoldenPathTrustBoundary` |
| **Bad signature** | `X-Hub-Signature-256` mismatch → `401`, zero GitHub/model calls | `TestGoldenPathTrustBoundary` |

### 3.5 Safe-mode and feedback (not failures, but operationally load-bearing)

| Scenario | Expected behavior | Paired check |
|---|---|---|
| **Shadow mode** (`SHADOW_MODE=true`) | full pipeline runs, model called, `flagged`++ — but **zero** outward writes | `TestShadowModeFlagsButNeverWrites` |
| **Label removed** (maintainer disagreement) | one identity-only feedback row appended off the request path; zero GitHub writes; no diff body in the row | `TestFeedbackRowOnLabelRemoval` |
| **Offline fold-in** | that feedback row resolves to a LEGIT `(title, diff)` example in the retraining dataset, leakage-free | `test_feedback_roundtrip.py` |

---

## 4. Caveats & assumptions

- **Dedup is process-local — redelivery after a gateway restart is NOT deduped.**
  The delivery-ID ledger is an in-memory, bounded set; it resets when the process
  restarts (the same restart that zeroes `/stats`). If GitHub redelivers a
  webhook whose ID the gateway saw only in a previous process lifetime, that
  delivery **will** be triaged again — one extra label + comment on an
  already-flagged PR. This is deliberate (a stateless serving path is worth more
  than perfect cross-restart idempotency), and it is bounded: the label add is
  idempotent on GitHub's side, so the visible cost is at most a duplicate
  comment. The idempotency drill (`TestGoldenPathIdempotentRedelivery`) proves
  dedup **within one process** only; it does not claim more.
- **`/stats` counters are not persisted.** They are a live operational gauge, not
  an audit log. A restart is the reset.
- **Fail-open is a policy, not a bug.** Sentinel never auto-closes and never
  blocks a merge on its own failure. When `failed` climbs, contributors are
  unaffected — you are losing *coverage* (PRs going un-triaged), not correctness.
  Fix the downstream (model or GitHub reachability); no PR is ever harmed by the
  outage itself.

