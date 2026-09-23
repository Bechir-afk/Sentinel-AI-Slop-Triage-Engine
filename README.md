# 🛡️ Sentinel — AI-Slop Triage Engine

<p align="center">
  <b>A two-service system that intercepts GitHub Pull Requests, scores each diff with a self-hosted model, and labels + comments on low-effort "slop" — without ever auto-closing.</b>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go" />
  <img src="https://img.shields.io/badge/Python-3.11-3776AB?logo=python" />
  <img src="https://img.shields.io/badge/Model-CodeBERT_(self--hosted)-FF6F00" />
  <img src="https://img.shields.io/badge/Docker-Compose_(distroless_gateway)-2496ED?logo=docker" />
  <img src="https://img.shields.io/badge/GitHub_Webhooks-REST_v3-black?logo=github" />
  <img src="https://img.shields.io/badge/Gateway_deps-Zero_(stdlib_only)-brightgreen" />
</p>

---

## 📖 Overview

**Sentinel** triages GitHub pull requests for low-effort, superficial "slop"
using two cooperating services:

- **Gateway** — a single static **Go** binary (zero third-party dependencies,
  pure stdlib). It verifies the webhook HMAC, acknowledges GitHub in well under
  a second, and runs triage asynchronously on a worker pool.
- **Model service** — a **Python / FastAPI** wrapper around a fine-tuned
  **CodeBERT** classifier that runs entirely **self-hosted** (CPU-only). No
  external AI API is ever called from the serving path.

When the model scores a PR as slop with confidence above the operating
threshold, Sentinel applies a label (`needs-human-review`) and posts a polite
comment — a human always stays in the loop. It **never auto-closes** a PR.

The gateway ships as a **distroless** image; the model service runs non-root
with a read-only rootfs. The two talk over the internal Compose network only —
the model port is never published.

---

## ✨ Features

- 🔐 **HMAC-SHA256 signature verification** — constant-time check of
  `X-Hub-Signature-256` over the raw body, before any parsing or work; mismatch → `401`.
- ⚡ **Fast ack, async triage** — the handler validates, dedups, enqueues, and
  returns `200` immediately; diff fetch + scoring + writes run on a background
  worker pool with a fresh 45s context, never the request context.
- 🧠 **Self-hosted model** — PR title + diff are POSTed to the internal model
  service `/predict`; returns `{ is_slop, confidence, reason }`. No third-party AI API.
- 🏷️ **Auto-label + 💬 auto-comment** — configurable label and a human-readable
  explanation on flagged PRs.
- 🎚️ **Artifact-sourced threshold** — the operating confidence threshold comes
  from the trained artifact's `/healthz` (validation-selected), with a `0.95`
  fallback; overridable via `CONFIDENCE_THRESHOLD`.
- 🔁 **Idempotent** — a bounded delivery-ID ledger makes a redelivered
  `X-GitHub-Delivery` a no-op (exactly one label + comment).
- 🤖 **Bot + event gating** — non-`pull_request` events and `[bot]` authors are
  acked with zero API calls.
- 🚫 **Fail-open** — any downstream error (diff fetch, model 5xx/timeout,
  label/comment post) → `200` and the PR is left untouched.
- 🛡️ **Hardened containers** — distroless gateway, non-root, `cap_drop: ALL`,
  `no-new-privileges`, read-only rootfs, resource limits.
- ✅ **Zero-network tests** — the full pipeline is covered with `httptest`
  fakes; no live credentials required.

---

## 🔄 System Flow

```
GitHub PR opened / reopened / synchronized
        │  HTTP POST  (pull_request event)
        ▼
┌──────────────────────────── Gateway (Go, stdlib) ────────────────────────────┐
│  POST /webhook                                                               │
│                                                                              │
│  1. Body-size cap  (internal/verify)   oversized → 413, no HMAC work         │
│  2. HMAC verify    (internal/verify)   HMAC-SHA256(body, secret) mismatch→401 │
│  3. Gate           (internal/webhook)  event==pull_request, action opened/   │
│                                        reopened/synchronize, skip [bot],      │
│                                        dedup X-GitHub-Delivery                │
│  4. Enqueue + ACK 200  ── returns immediately (< 1s) ─────────────────────┐  │
│                                                                           │  │
│  ── worker pool (WORKER_COUNT), fresh context.Background()+45s ───────────┘  │
│  5. Fetch diff     (internal/github)   GET pulls/{n}, Accept: …diff           │
│  6. Triage         (internal/triage)   POST {MODEL_URL}/predict ───────────┐  │
│  7. Act            (internal/github)   if is_slop && conf ≥ threshold:      │  │
│                                          → add label + post comment         │  │
│                                        else / any error: no-op (fail-open)  │  │
└─────────────────────────────────────────────────────────────────────────┼──┘
                                                                            ▼
                                            ┌──── Model service (Python) ────┐
                                            │  POST /predict → {is_slop,      │
                                            │    confidence, reason}          │
                                            │  GET  /healthz → {status,       │
                                            │    threshold, artifact}         │
                                            │  Fine-tuned CodeBERT, CPU-only  │
                                            └─────────────────────────────────┘

Respond 200. Never auto-close.
```

---

## 🗂️ Repository Structure

```
Sentinel-AI-Slop-Triage-Engine/
├── cmd/sentinel/main.go     # Entry: config, worker pool, server, graceful shutdown, -healthz
├── internal/
│   ├── config/              # Env-var config load & validation
│   ├── verify/              # HMAC-SHA256 middleware + body-size cap (413)
│   ├── webhook/             # Gate + ack + async worker pool; dedupe.go ledger
│   ├── github/              # REST client: fetch diff, add label, post comment
│   └── triage/              # Model client: POST /predict, GET /healthz
├── model/                   # Self-hosted inference service
│   ├── app.py               # FastAPI: /predict, /healthz
│   ├── inference.py         # CodeBERT classifier + artifact/threshold loading
│   └── Dockerfile           # CPU-only torch, non-root
├── ml/                      # Offline training pipeline (never in serving path)
│   ├── collect_prs.py       # Gather PRs → raw dataset
│   ├── build_dataset.py     # Leakage-free splits + synthetic slop
│   ├── train.py             # Fine-tune + validation threshold selection
│   ├── evaluate.py          # Test-set precision gate (≥ 0.85)
│   └── tests/               # Offline self-checks (stdlib + numpy)
├── deploy/Dockerfile        # Gateway → distroless static:nonroot
├── docker-compose.yml       # Two-service stack
├── .env.example             # Config template
├── .specify/memory/constitution.md   # 8 project principles
├── specs/                   # Feature specs, plans, tasks
└── PROJECT_MAP.md           # Architecture reference & design history
```

---

## ⚙️ Configuration

All configuration is via environment variables — no config files ship in the
image. Copy `.env.example` → `.env` and fill in the required values.

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITHUB_WEBHOOK_SECRET` | ✅ | — | Shared secret for HMAC-SHA256 signature verification |
| `GITHUB_TOKEN` | ✅ | — | PAT or GitHub App token — fetch diffs, write labels & comments |
| `MODEL_URL` | ✅ | `http://sentinel-model:9000` | Base URL of the self-hosted model service |
| `CONFIDENCE_THRESHOLD` | ❌ | artifact value (`0.95` fallback) | Min confidence to act; sourced from the model artifact via `/healthz` when unset |
| `SLOP_LABEL` | ❌ | `needs-human-review` | Label applied to flagged PRs |
| `PORT` | ❌ | `8080` | Gateway HTTP listen port |
| `MAX_BODY_BYTES` | ❌ | `26214400` (25 MiB) | Max webhook body size; oversized → `413` before HMAC |
| `WORKER_COUNT` | ❌ | `8` | Background triage worker-pool size (1–64) |

---

## 🚀 Getting Started

### Prerequisites

- **Docker** + **Docker Compose** (the recommended path — runs both services)
- A trained model artifact at `model/model/` (see [Training](#-training) — or
  copy one produced offline)
- **Go 1.26** if you want to run the gateway or tests from source
- A **GitHub repository** with a configured webhook and a token with
  Pull requests: read/write, Issues: read/write

### Quickstart (Docker Compose)

```bash
cp .env.example .env          # then fill in GITHUB_WEBHOOK_SECRET + GITHUB_TOKEN

# 1. Artifact present → both services reach healthy
docker compose up -d
docker compose ps
curl localhost:8080/healthz   # → {"status":"ok","version":"dev"}
curl localhost:8080/stats     # → {"received":…,"triaged":…,"flagged":…,…,"version":"dev"}
```

If the model artifact is **missing**, the model service exits with a single
actionable line (naming the path and the command that produces it) instead of
crash-looping — and the gateway still starts and fails open:

```bash
rm -rf model/model
docker compose up sentinel-model
docker compose logs sentinel-model | tail -3   # one clear error, container exits
```

### Configure the GitHub Webhook

1. Repo → **Settings → Webhooks → Add webhook**
2. **Payload URL**: `https://your-host:8080/webhook`
3. **Content type**: `application/json`
4. **Secret**: the same value as `GITHUB_WEBHOOK_SECRET`
5. **Let me select individual events** → check **Pull requests**
6. **Add webhook**

The gateway exposes `GET /healthz` → `200 ok` for the Compose healthcheck.

### Build version (source builds)

The gateway reports a build version on `/healthz` and `/stats`. A plain
`go build` reports `dev`; stamp a real version at link time:

```bash
go build -ldflags "-X main.version=$(git describe --tags --always)" ./cmd/sentinel
```

---

## 🧠 Training

The model artifact is produced **offline** by the `ml/` pipeline and mounted
read-only into the model service (`model/model/`). Nothing here runs in the
serving path.

```bash
cd ml
pip install -r requirements.txt

python collect_prs.py     # gather PRs → raw dataset
python build_dataset.py   # leakage-free train/val/test splits (+ synthetic slop);
                          #   asserts zero cross-split (title, diff) collisions
python train.py           # fine-tune CodeBERT; sweeps the validation split and
                          #   writes threshold.json (lowest cutoff clearing 0.85 precision)
python evaluate.py        # scores the test set at the selected threshold;
                          #   exits non-zero if SLOP precision < 0.85
```

`train.py` writes the artifact (config, weights, tokenizer) plus
`threshold.json` — the model service loads both and exposes the threshold on
`/healthz`, which the gateway uses as its default operating threshold.

Offline invariants have fast, dependency-light self-checks (stdlib + numpy, no
torch):

```bash
python ml/tests/test_build_dataset.py   # leakage assertion
python ml/tests/test_thresholds.py      # threshold selection + softmax
```

---

## 🧪 Running Tests

```bash
go build ./...          # compile check
go vet ./...            # static analysis
go test ./... -race     # full suite, race detector
```

All Go tests use `testing` + `net/http/httptest` fakes — **no live GitHub or
model credentials required**. The async pipeline (gate → ack → worker pool →
fetch diff → triage → label + comment), dedup, bot skip, body cap, and every
fail-open path are covered.

```bash
docker compose config   # validate the two-service stack
```

---

## 🧰 Tech Stack

| Layer | Technology | Notes |
|---|---|---|
| Gateway | Go 1.26 | Single static binary, **zero third-party deps** |
| HTTP server | `net/http` (stdlib) | One endpoint — no framework |
| HMAC verification | `crypto/hmac` + `crypto/sha256` (stdlib) | Constant-time |
| GitHub API | Plain REST over `net/http` | fetch diff, add label, post comment |
| Model service | Python 3.11 · FastAPI · CodeBERT | Self-hosted, CPU-only torch |
| Gateway container | `gcr.io/distroless/static:nonroot` | Non-root, tiny, CA certs included |
| Model container | non-root, read-only rootfs + tmpfs | `cap_drop: ALL`, resource-limited |
| Tests | `testing` + `net/http/httptest` (stdlib) | Zero network |

---

## 🎯 Design Decisions

- **Never auto-close** — Sentinel labels and comments only. A human always makes
  the final call to close or merge.
- **Fail-open** — any downstream failure logs, returns `200`, and leaves the PR
  untouched. A flaky model never blocks a legitimate contributor.
- **Ack-then-process** — GitHub gets a sub-second `200`; triage runs on a worker
  pool with a fresh bounded context, decoupled from the connection.
- **Self-hosted model** — the serving path never calls an external AI API; the
  gateway talks to the model over the internal network only.
- **Precision over recall** — the artifact must clear 0.85 SLOP precision on a
  leakage-free test set; false positives on real contributors are the expensive error.
- **Zero Go dependencies** — the gateway is pure stdlib; adding a module requires
  amending the [constitution](.specify/memory/constitution.md).

---

## 📄 License

This project is open for educational and personal use.

---

<p align="center">Built with ❤️ by <b>Bechir Ben Rabia</b></p>
