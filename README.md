# 🛡️ Sentinel — AI-Slop Triage Engine

<p align="center">
  <b>A Go webhook microservice that intercepts GitHub Pull Requests, sends the diff to Gemini AI for slop detection, and automatically labels + comments on flagged PRs — without ever auto-closing.</b>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.26.5-00ADD8?logo=go" />
  <img src="https://img.shields.io/badge/Gemini-2.5_Flash-4285F4?logo=google" />
  <img src="https://img.shields.io/badge/Docker-Distroless-2496ED?logo=docker" />
  <img src="https://img.shields.io/badge/GitHub_Webhooks-REST_v3-black?logo=github" />
  <img src="https://img.shields.io/badge/Dependencies-Zero_(stdlib_only)-brightgreen" />
</p>

---

## 📖 Overview

**Sentinel** is a lightweight, production-ready Go microservice that acts as an automated gatekeeper for open-source repositories. It listens for GitHub `pull_request` webhook events, fetches the PR diff via the GitHub REST API, and submits it to **Google Gemini 2.5 Flash** for AI-powered triage.

If Gemini determines the PR is low-effort AI-generated "slop" with confidence above a configurable threshold, Sentinel applies a label (`needs-human-review`) and posts a polite comment on the PR — keeping a human in the loop at all times. It **never auto-closes** a PR.

The entire service is a single static Go binary with **zero third-party dependencies** (one exception: the Gemini SDK). It ships as a **distroless Docker image** under 20MB.

---

## ✨ Features

- 🔐 **HMAC-SHA256 signature verification** — constant-time validation of `X-Hub-Signature-256` on every webhook request; mismatches return `401` and are dropped immediately
- 🤖 **Gemini 2.5 Flash triage** — sends PR diff + title to Gemini with a strict JSON schema; returns `{ is_slop, confidence, reason }`
- 🏷️ **Auto-label** — applies a configurable label to flagged PRs (default: `needs-human-review`)
- 💬 **Auto-comment** — posts a polite, human-readable explanation on flagged PRs
- ⚖️ **Confidence threshold** — only acts when `confidence >= CONFIDENCE_THRESHOLD` (default `0.90`); low-confidence verdicts are no-ops
- 🚫 **Fail-open** — if the AI call fails, Sentinel logs the error and responds `200` without touching the PR
- 🐳 **Distroless image** — `gcr.io/distroless/static:nonroot`, non-root, <20MB, includes CA certs for HTTPS
- ✅ **Zero-network tests** — full pipeline tested with `httptest` fake servers; no live credentials required

---

## 🔄 System Flow

```
GitHub PR opened / reopened / synchronized
        │  HTTP POST  (pull_request event)
        ▼
┌─────────────────────────────────────────────────────────────┐
│  POST /webhook                                               │
│                                                             │
│  1. HMAC middleware  (internal/verify)                      │
│     HMAC-SHA256(body, WEBHOOK_SECRET) vs X-Hub-Signature-256│
│     Mismatch → 401, drop.                                   │
│                                                             │
│  2. Parse payload  (internal/webhook)                       │
│     action == "opened" | "reopened" | "synchronize" only    │
│     extract owner, repo, PR number, title, author           │
│                                                             │
│  3. Fetch diff  (internal/github)                           │
│     GET /repos/{owner}/{repo}/pulls/{n}                     │
│     Accept: application/vnd.github.v3.diff                  │
│                                                             │
│  4. Triage  (internal/triage)                               │
│     diff + title → gemini-2.5-flash                        │
│     returns { is_slop: bool, confidence: float, reason }    │
│                                                             │
│  5. Act  (internal/github)                                  │
│     if is_slop && confidence >= threshold:                  │
│       → POST label: "needs-human-review"                   │
│       → POST comment: polite explanation                    │
│     else: no-op                                             │
│                                                             │
│  Respond 200. Never auto-close.                             │
└─────────────────────────────────────────────────────────────┘
```

---

## 🗂️ Repository Structure

```
Sentinel-AI-Slop-Triage-Engine/
├── cmd/sentinel/main.go     # Entry point: config load, wiring, ListenAndServe, /healthz
├── internal/
│   ├── config/              # Env-var config load & validation
│   ├── verify/              # HMAC-SHA256 constant-time middleware
│   ├── webhook/             # Payload parsing + pipeline orchestration
│   ├── github/              # REST client: fetch diff, add label, post comment
│   └── triage/              # Gemini client + JSON schema + prompt
├── deploy/
│   └── Dockerfile           # Multi-stage build → distroless static:nonroot
├── go.mod                   # Zero third-party dependencies (pure stdlib)
└── PROJECT_MAP.md           # Architecture reference & design decisions
```

---

## ⚙️ Configuration

All configuration is via environment variables — no config files.

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITHUB_WEBHOOK_SECRET` | ✅ | — | Shared secret for HMAC-SHA256 signature verification |
| `GITHUB_TOKEN` | ✅ | — | PAT or GitHub App token — used to fetch diffs, write labels and comments |
| `GEMINI_API_KEY` | ✅ | — | Google AI Studio / Vertex AI API key for Gemini |
| `CONFIDENCE_THRESHOLD` | ❌ | `0.90` | Minimum Gemini confidence score (0–1) to trigger label + comment |
| `SLOP_LABEL` | ❌ | `needs-human-review` | Name of the GitHub label applied to flagged PRs |
| `PORT` | ❌ | `8080` | HTTP listen port |

---

## 🚀 Getting Started

### Prerequisites

- **Go 1.22+** (tested on 1.26.5)
- A **GitHub repository** with a configured webhook (see below)
- A **Gemini API key** ([Google AI Studio](https://aistudio.google.com/))
- A **GitHub Personal Access Token** with `repo` scope (or a GitHub App)

---

### Option A — Docker (Recommended)

```bash
# Build the image
docker build -f deploy/Dockerfile -t sentinel .

# Run
docker run -p 8080:8080 \
  -e GITHUB_WEBHOOK_SECRET=your_secret \
  -e GITHUB_TOKEN=ghp_xxxx \
  -e GEMINI_API_KEY=AIza_xxxx \
  -e CONFIDENCE_THRESHOLD=0.90 \
  sentinel
```

### Option B — Run from source

```bash
git clone https://github.com/Bechir-afk/Sentinel-AI-Slop-Triage-Engine.git
cd Sentinel-AI-Slop-Triage-Engine

export GITHUB_WEBHOOK_SECRET=your_secret
export GITHUB_TOKEN=ghp_xxxx
export GEMINI_API_KEY=AIza_xxxx

go run ./cmd/sentinel
```

### Option C — Build binary

```bash
go build -o sentinel ./cmd/sentinel
./sentinel
```

---

### Configure the GitHub Webhook

1. Go to your repo → **Settings → Webhooks → Add webhook**
2. Set **Payload URL** to `https://your-host:8080/webhook`
3. Set **Content type** to `application/json`
4. Set **Secret** to the same value as `GITHUB_WEBHOOK_SECRET`
5. Select **Let me select individual events** → check **Pull requests**
6. Click **Add webhook**

Sentinel also exposes a **health check** at `GET /healthz` → `200 OK`.

---

## 🧪 Running Tests

```bash
go test ./...
```

All tests use `httptest` fake servers — **no live GitHub or Gemini credentials required**. The full pipeline (HMAC verify → parse → fetch diff → triage → label + comment) is covered end-to-end in `internal/webhook/webhook_test.go`.

```bash
go vet ./...    # Static analysis
go build ./...  # Compile check
```

---

## 🧰 Tech Stack

| Layer | Technology | Notes |
|---|---|---|
| Language | Go 1.26.5 | Single static binary |
| HTTP Server | `net/http` (stdlib) | One endpoint — no framework needed |
| HMAC Verification | `crypto/hmac` + `crypto/sha256` (stdlib) | Constant-time comparison |
| GitHub API | Plain REST over `net/http` | 3 calls: fetch diff, add label, post comment |
| AI Inference | Google Gemini 2.5 Flash | Structured JSON output via `ResponseMIMEType` + schema |
| Container | `gcr.io/distroless/static:nonroot` | <20MB, non-root, includes CA certs |
| Tests | `testing` + `net/http/httptest` (stdlib) | Zero network required |
| Dependencies | `google.golang.org/genai` only | Everything else is pure stdlib |

---

## 🎯 Design Decisions

- **No auto-close** — Sentinel labels and comments only. A human always makes the final call to close or merge.
- **Fail-open** — If the Gemini call fails for any reason, Sentinel logs the error, returns `200` to GitHub, and leaves the PR untouched.
- **Distroless over scratch** — The `scratch` base image cannot make HTTPS calls (no CA certs). `distroless/static:nonroot` solves this at the same size.
- **Zero deps** — The GitHub API calls are plain `net/http` REST. Only the Gemini SDK is a third-party dependency.

---

## 📄 License

This project is open for educational and personal use.

---

<p align="center">Built with ❤️ by <b>Bechir Ben Rabia</b></p>
