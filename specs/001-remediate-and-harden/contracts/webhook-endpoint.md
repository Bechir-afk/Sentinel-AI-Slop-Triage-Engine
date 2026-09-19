# Contract: Gateway Webhook Endpoint

Phase 1 output. The gateway's public interface, unchanged in shape, tightened
in behavior.

## `POST /webhook`

### Request headers (required)

| Header | Rule |
|--------|------|
| `X-Hub-Signature-256` | `sha256=` + HMAC-SHA256 of the raw body with `GITHUB_WEBHOOK_SECRET`. Checked in constant time. Missing/mismatch → **401**, nothing else happens. |
| `X-GitHub-Event` | Must be `pull_request` for processing; any other value (or missing) → **200**, ignored, zero API calls. |
| `X-GitHub-Delivery` | Unique delivery ID. Missing → **400**. Already-seen ID → **200**, suppressed (idempotent). |

### Request body

Raw GitHub event JSON, ≤ `MAX_BODY_BYTES` (default 25 MiB, env-configurable).
Larger → **413** before HMAC or parsing.

### Response

**200** immediately after validation, dedup claim, and enqueue — always well
under GitHub's 10-second delivery timeout; triage continues in the background
(FR-001, FR-002). Error codes: **401** bad signature, **413** oversized,
**400** malformed payload, **405** non-POST.

### Processing contract (post-ack, async)

1. Action must be `opened` / `reopened` / `synchronize`; else drop.
2. `pull_request.user.login` matching bot convention (`[bot]` suffix) → drop.
3. Fetch diff → score with model service → act only if verdict is slop AND
   confidence ≥ threshold. Any error at any step → log, drop, no PR mutation
   (fail-open, constitution II). Never close, merge, or dismiss (I).

## `GET /healthz`

**200** `ok` — used by the compose healthcheck (FR-015).

## Configuration surface

| Env | Default | Meaning |
|-----|---------|---------|
| `GITHUB_WEBHOOK_SECRET` | required | HMAC key. |
| `GITHUB_TOKEN` | required | GitHub API bearer. |
| `MODEL_URL` | required | Model service base URL. |
| `CONFIDENCE_THRESHOLD` | artifact default (0.95 fallback) | Action threshold; artifact-selected value (FR-010) is the new default. |
| `SLOP_LABEL` | `needs-human-review` | Label applied. |
| `PORT` | `8080` | Listen port. |
| `MAX_BODY_BYTES` | `26214400` | Request body cap (FR-006). |
| `WORKER_COUNT` | `8` | Background triage workers. |
