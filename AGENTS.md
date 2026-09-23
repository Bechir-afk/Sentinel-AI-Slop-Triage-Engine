# Sentinel — Project Instructions for coding agents

You are working on **Sentinel**, a two-service system that triages GitHub Pull
Requests for low-effort "slop". A stdlib-only **Go gateway** verifies and
ingests webhooks; a **self-hosted Python model service** (fine-tuned CodeBERT)
scores each PR. Flagged PRs get a label and a comment. Sentinel **never
auto-closes**.

## The 8 constitution principles (`.specify/memory/constitution.md`)

1. **Never Auto-Close (non-negotiable)** — only ever add a label + post a
   comment. Never close, merge, or dismiss a PR.
2. **Fail-Open** — any downstream failure (diff fetch, model 5xx/timeout,
   label/comment post) → no PR mutation + a 2xx webhook response.
3. **Precision Over Recall (non-negotiable)** — SLOP precision must clear 0.85
   on a leakage-free test set before an artifact is trusted. False positives on
   real contributors are the expensive error.
4. **Classify Value, Not Provenance** — classify whether a change is
   superficial/boilerplate, never whether AI wrote it (legit PRs are often
   AI-assisted).
5. **Zero Third-Party Go Dependencies** — the gateway is stdlib-only
   (`net/http`, `crypto/*`, `encoding/*`, `log/slog`, `sync/*`). Adding a Go
   module is rejected unless the constitution is amended.
6. **Self-Hosted Model** — the serving path never calls an external AI API. The
   model runs locally; the gateway talks to it over internal-network HTTP JSON.
7. **Trust Boundary Verification** — HMAC-SHA256 over the raw body, constant-time,
   before any parsing or action; body size-capped; event/action validated.
8. **Docs Match Shipped Code** — README, AGENTS.md, PROJECT_MAP describe what is
   actually shipped. A doc that contradicts the code is a defect.

## Architecture

- **Gateway entry point:** `cmd/sentinel/main.go` — config, worker pool, HTTP
  server, graceful shutdown, `-healthz` probe.
- **Gateway packages (`internal/`):**
  - `config` — env-var load & validation. Keys: `GITHUB_WEBHOOK_SECRET`,
    `GITHUB_TOKEN`, `MODEL_URL`, `CONFIDENCE_THRESHOLD` (optional; sourced from
    the model artifact when unset, 0.95 fallback), `SLOP_LABEL`, `PORT`,
    `MAX_BODY_BYTES`, `WORKER_COUNT`.
  - `verify` — HMAC-SHA256 constant-time middleware (`crypto/hmac`,
    `crypto/sha256`); size-caps the body via `http.MaxBytesReader` (413).
  - `webhook` — validates + gates (event/action/bot/dedup) synchronously, then
    **acks 200 immediately** and processes on a worker pool. `dedupe.go` is the
    bounded delivery-ID ledger; the pipeline runs on a fresh 45s context.
  - `github` — plain REST client: `GET pulls/{n}` (diff), `POST issues/{n}/labels`,
    `POST issues/{n}/comments`.
  - `triage` — HTTP JSON client to the model service `POST /predict` and
    `GET /healthz` (threshold sourcing).
- **Model service (`model/`):** FastAPI wrapper (`app.py`) over `inference.py`
  (CodeBERT classifier, LEGIT=0/SLOP=1). `/predict` → `{is_slop, confidence,
  reason}`; `/healthz` → `{status, threshold, artifact}`. Reads `threshold.json`
  shipped with the artifact.
- **Offline ML (`ml/`):** `collect_prs.py` → `build_dataset.py` (leakage-free
  splits) → `train.py` (fine-tune + threshold selection) → `evaluate.py`
  (precision gate). Never in the serving path. Self-checks in `ml/tests/`
  (stdlib+numpy only).

## Serving-path rules

- Every serving-path change needs tests for BOTH the success path and the
  fail-open path.
- Keep the gateway dependency-free: no `go get`. Ledger/dedup/workers use
  `sync` + channels; logging is `log`/`log/slog`.
- HMAC verification stays first, before parsing.

## Tech stack & constraints

- **Go 1.26**; gateway container `gcr.io/distroless/static:nonroot` (never
  `scratch` — no CA certs).
- **Python 3.11**, CPU-only torch; model container runs non-root, read-only
  rootfs + tmpfs.
- **Tests:** `testing` + `net/http/httptest`; zero-network — mock all external
  HTTP.

## Commands

- `go build ./...` · `go vet ./...` · `go test ./... -race`
- `python ml/tests/test_build_dataset.py` · `python ml/tests/test_thresholds.py`
  (offline invariants, no torch needed)
- `docker compose config` to validate the stack.

Update `PROJECT_MAP.md` when you change architecture or add packages.
