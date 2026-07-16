# PROJECT_MAP — Sentinel: AI-Slop Triage Engine

Automated gatekeeper for open-source repos. A Go webhook microservice intercepts
new GitHub Pull Requests, sends the code diff to a **self-hosted, self-trained ML
model** for triage, and (on high-confidence "slop") labels the PR and posts a
polite comment. No third-party AI API — the model is ours, runs locally, offline.

**Scope boundary:** Two containers — a Go gateway + a Python inference service —
plus the training/data pipeline that produces the model. Action on flagged PRs is
**label + comment only — never auto-close.**

**Why self-trained (user decision):** owning the model adds project value, removes
per-call API cost and the external dependency, and keeps all code/data on-prem.
The tradeoff — accepted — is a Python ML pipeline + a second service + the burden
of building a labeled dataset. The Gemini implementation remains in git history
(commit `d56cbe6`) as a fallback.

---

## ARCHITECTURE OVERVIEW (two services)

```
                          GitHub (webhook + REST API)
                                     │  ▲
                    pull_request POST │  │ label / comment
                                     ▼  │
        ┌────────────────────────────────────────────┐
        │  sentinel-gateway  (Go, :8080)               │   ← existing code, retargeted
        │  HMAC verify · fetch diff · decide · act     │
        └───────────────┬──────────────────────────────┘
                        │  POST /predict {title, diff}   (localhost / compose network)
                        ▼
        ┌────────────────────────────────────────────┐
        │  sentinel-model  (Python FastAPI, :9000)     │   ← NEW
        │  loads fine-tuned encoder → {is_slop,        │
        │  confidence, reason}                         │
        └────────────────────────────────────────────┘
                        ▲
                        │ loads model artifact (./model/) produced offline by:
        ┌────────────────────────────────────────────┐
        │  ml/  training + data pipeline (offline)     │   ← NEW, run once, not in serving path
        └────────────────────────────────────────────┘
```

The Go gateway and Python model talk over plain HTTP JSON. The contract is
identical to the old Gemini `Result`, so the Go side changes minimally.

---

## TECH_STACK

### Service 1 — Gateway (Go) — mostly unchanged
| Component        | Choice                                | Version (verified 2026-07) | Rationale |
|------------------|---------------------------------------|----------------------------|-----------|
| Language         | Go                                    | 1.26.5                     | Tiny static binary, low-latency webhook listener. |
| HTTP server      | stdlib `net/http`                     | (stdlib)                   | One endpoint; no framework needed. |
| Signature verify | stdlib `crypto/hmac`, `crypto/sha256` | (stdlib)                   | Constant-time SHA256 of `X-Hub-Signature-256`. |
| GitHub API       | plain REST over `net/http`            | REST v3                    | 3 calls only; avoids go-github dep. |
| Model call       | plain REST over `net/http`            | —                          | POST diff to local Python service; same `net/http` pattern as GitHub. |
| Container        | `gcr.io/distroless/static:nonroot`    | —                          | CA certs + non-root, <20MB. |

**Go third-party dependencies: zero (pure stdlib).**

### Service 2 — Model inference (Python) — NEW
| Component     | Choice                          | Version (verified 2026-07) | Rationale |
|---------------|---------------------------------|----------------------------|-----------|
| Language      | Python                          | 3.11+                      | Ecosystem for ML serving. |
| Web framework | FastAPI + Uvicorn               | latest stable              | Async, typed, minimal; one `/predict` route. |
| ML runtime    | PyTorch                         | latest stable CPU build    | CPU inference is plenty for PR-rate load. |
| Model libs    | Hugging Face `transformers`     | latest stable              | Load fine-tuned encoder + tokenizer. |
| Base model    | `microsoft/codebert-base` (~125M) | —                        | Encoder built for classification, not generation; CPU-servable. See MODEL section. |
| Container     | `python:3.11-slim`              | —                          | Small; model weights baked in or mounted. |

### Offline — Training & data pipeline (`ml/`) — NEW, not in serving path
| Component      | Choice                                   | Rationale |
|----------------|------------------------------------------|-----------|
| Data scraping  | Python + GitHub REST API (`requests`)    | Pull merged PRs (legit) + closed-as-spam PRs (slop). |
| Human-code src | CodeSearchNet / The Stack (HF datasets)  | Abundant, permissive "legit" examples. |
| Synthetic slop | Python generators                        | comment-only / rename-only / boilerplate diffs. |
| Training       | `transformers` Trainer + PyTorch         | Fine-tune CodeBERT + classification head. |
| Metrics        | `scikit-learn`                           | precision / recall / F1, confusion matrix. |

---

## SYSTEM_FLOW (runtime path)

```
GitHub PR opened
      │  HTTP POST (pull_request event)
      ▼
┌──────────────────────────────────────────────────────────────┐
│ GATEWAY (Go)  POST /webhook                                    │
│                                                                │
│ 1. HMAC middleware (internal/verify)                           │
│    HMAC-SHA256(body, WEBHOOK_SECRET) vs X-Hub-Signature-256    │
│    hmac.Equal (constant-time). Mismatch → 401, drop.           │
│                                                                │
│ 2. Parse payload (internal/webhook)                            │
│    action ∈ {opened, reopened, synchronize} only               │
│    extract owner, repo, PR number, title, author               │
│    (payload has NO diff — only a diff_url)                     │
│                                                                │
│ 3. Fetch diff (internal/github)                                │
│    GET /repos/{owner}/{repo}/pulls/{n}                          │
│    Accept: application/vnd.github.v3.diff                       │
│    Authorization: Bearer GITHUB_TOKEN                          │
│                                                                │
│ 4. Triage (internal/triage) — CHANGED                          │
│    POST {MODEL_URL}/predict  {title, diff}                     │
│    → { is_slop: bool, confidence: float, reason: str }         │
│    (fail-open: on timeout/error → skip, no action)             │
│         │                                                      │
│         ▼                                                      │
│   ┌────────────────────────────────────────────────┐          │
│   │ MODEL SERVICE (Python)  POST /predict            │          │
│   │  a. concat title + diff, truncate to 512 tokens  │          │
│   │  b. tokenize (CodeBERT tokenizer)                │          │
│   │  c. forward pass → logits → softmax              │          │
│   │  d. is_slop = argmax==SLOP; confidence = P(slop) │          │
│   │  e. reason = templated string from top signals   │          │
│   └────────────────────────────────────────────────┘          │
│                                                                │
│ 5. Act (internal/github) — only if is_slop &&                  │
│    confidence >= CONFIDENCE_THRESHOLD:                         │
│    POST issues/{n}/labels   → "needs-human-review"            │
│    POST issues/{n}/comments → polite explanation               │
│    else: no-op                                                 │
│                                                                │
│ Respond 200 quickly; log outcome. Never auto-close.            │
└──────────────────────────────────────────────────────────────┘
```

---

## MODEL (technical detail)

**Task framing.** Binary sequence classification: `LEGIT (0)` vs `SLOP (1)`.
We classify *value* (is this change superficial / boilerplate / hallucinated),
NOT *provenance* (was AI involved) — legit PRs are often AI-assisted, so
provenance is the wrong target and inflates false positives.

**Base model.** `microsoft/codebert-base`, a RoBERTa-style encoder pretrained on
code+NL. We add a classification head (linear layer over the pooled `[CLS]`
representation). ~125M params. Encoder, not generator → right-sized for scoring.

**Input encoding.**
- Input string: `"<title>\n[SEP]\n<diff>"`, diff truncated so total ≤ **512 tokens**
  (CodeBERT's max). Large diffs are head-truncated; the top of a diff carries the
  strongest signal.
- Tokenizer: CodeBERT BPE tokenizer, shipped with the model artifact.

**Output contract (matches the Go `Result` struct exactly):**
```json
{ "is_slop": true, "confidence": 0.94, "reason": "short human-readable string" }
```
- `is_slop` = argmax(logits) == SLOP
- `confidence` = softmax probability of the predicted class
- `reason` = templated (e.g. "large diff with low unique-token ratio"); the model
  gives a score, the reason is a lightweight heuristic label for the comment.

**Inference cost.** CPU-only is fine: a 125M encoder does a single 512-token
forward pass in well under a second on commodity CPU. PR arrival rate is minutes
apart. **No GPU required in production.**

**Training cost (one-time, offline).** Fine-tuning fits on a single consumer GPU
(6–8GB VRAM, small batch) or CPU (slow but possible) or a free Colab GPU.

**Acceptance bar.** The slop-class **precision must clear ~0.85** on a held-out
test set before this model is trusted in the gateway. Below that, false positives
insult real contributors — the expensive error. If unmet, keep threshold high /
fail-open, or fall back to the Gemini branch in git history.

---

## DATA PIPELINE (technical detail)

The dataset is the make-or-break deliverable; no public "AI-slop PR" dataset
exists (verified 2026-07-16 against Hugging Face — see ORPHANS). We build it.

| Class | Source | Method | Est. volume |
|-------|--------|--------|-------------|
| LEGIT | Merged PRs from active real repos | GitHub REST: `state=closed&merged=true`, take diffs | ~1–2k |
| LEGIT | CodeSearchNet / The Stack | Sample real human functions as "normal code" | supplement |
| SLOP  | Closed-as-spam / invalid PRs | GitHub REST: filter closed-unmerged w/ spam/invalid labels | scarce — as many as found |
| SLOP  | Synthetic | Generators: comment-only, rename-only, whitespace, boilerplate scaffolding | pad to balance |

- **Target:** ~2–4k examples, roughly class-balanced.
- **Split:** stratified 80/10/10 train/val/test; test set never seen in tuning.
- **Format:** parquet/JSONL rows `{title, diff, label}`.
- **Labeling caveat:** "slop" is subjective; synthetic examples are cleanly
  labeled by construction, real ones need human review. This is the manual
  bottleneck.

---

## REPOSITORY LAYOUT (target)

```
gateway/  (existing Go code — to be moved under gateway/ or kept at root)
  cmd/sentinel/main.go       Config, wiring, ListenAndServe, /healthz
  internal/config/           Env-var config load + validation
  internal/verify/           HMAC-SHA256 constant-time middleware
  internal/webhook/          Payload parsing + pipeline orchestration
  internal/github/           REST client: fetch diff, add label, post comment
  internal/triage/           Model client (POST /predict)   ← rewritten from Gemini
  deploy/Dockerfile          Multi-stage → distroless static:nonroot
  go.mod                     Zero third-party deps

model/  (NEW — Python inference service)
  app.py                     FastAPI, POST /predict, GET /healthz
  inference.py               tokenize → forward → softmax → Result
  requirements.txt
  Dockerfile                 python:3.11-slim + baked/mounted model artifact
  model/                     saved fine-tuned weights + tokenizer (artifact)

ml/  (NEW — offline training + data, NOT in serving path)
  collect_prs.py             GitHub scraper → raw PRs
  build_dataset.py           label, synth slop, split → dataset/*.parquet
  train.py                   fine-tune CodeBERT + head, save artifact
  evaluate.py                precision/recall/F1, confusion matrix
  requirements.txt

docker-compose.yml  (NEW — runs gateway + model together)
PROJECT_MAP.md
```

---

## CONFIGURATION (environment variables)

### Gateway (Go)
| Var                    | Required | Default                  | Purpose |
|------------------------|----------|--------------------------|---------|
| `GITHUB_WEBHOOK_SECRET`| yes      | —                        | HMAC shared secret. |
| `GITHUB_TOKEN`         | yes      | —                        | Fetch diff + write label/comment. |
| `MODEL_URL`            | yes      | —                        | Base URL of the Python model service (e.g. `http://sentinel-model:9000`). |
| `CONFIDENCE_THRESHOLD` | no       | `0.90`                   | Minimum confidence to act. |
| `SLOP_LABEL`           | no       | `needs-human-review`     | Label applied to flagged PRs. |
| `PORT`                 | no       | `8080`                   | Listen port. |

**Removed:** `GEMINI_API_KEY` — no external AI API anymore.

### Model service (Python)
| Var          | Required | Default        | Purpose |
|--------------|----------|----------------|---------|
| `MODEL_PATH` | no       | `./model`      | Path to the saved fine-tuned artifact. |
| `PORT`       | no       | `9000`         | Listen port. |

---

## DECISIONS & CORRECTIONS (chronological)

1. **Diff not in webhook payload.** GitHub sends metadata + `diff_url` only;
   Sentinel fetches the diff via API → requires `GITHUB_TOKEN`.
2. **No `scratch` image** — no CA certs for HTTPS. Using distroless static:nonroot.
3. **No auto-close** — label + comment only, human stays in the loop.
4. **OpenShift removed** (user decision) — Docker/compose is the deploy unit.
5. **Gemini replaced by a self-trained model** (user decision) — see rationale at
   top. Adds `model/` + `ml/`, removes `GEMINI_API_KEY`, rewrites `internal/triage`
   to call the local service. Go dependency count stays zero.
6. **Classify value, not provenance** — deliberately not an "AI-detector"; those
   have high false-positive rates and legit PRs are often AI-assisted.

---

## OUT OF SCOPE (do not build)

- OpenShift/Kubernetes manifests or live deploy.
- Auto-closing PRs.
- Persistence / database / queue in the serving path.
- Training from scratch (fine-tune only).
- A generative LLM for classification (encoder is the right tool).
- Any certificate/badge generator (unrelated project from the source brief).

---

## STATUS (updated 2026-07-16)

### Done & verified
- **Gateway (Go) core** — commits `53213fe`, `d56cbe6`. HMAC, GitHub client,
  webhook pipeline, fail-open, Dockerfile.
- **Gateway retarget to model service** — `internal/triage` rewritten from the
  Gemini client to a plain POST `{MODEL_URL}/predict`; `Result` struct and
  `Analyze` signature unchanged, so `webhook`/`main` wiring is untouched beyond
  the constructor. `config` drops `GEMINI_API_KEY`/`GEMINI_API_BASE`, adds
  required `MODEL_URL`. `go build`, `go vet`, `go test ./...` all pass on Go
  1.26.5; Go dependency count still zero.

### Pending (the self-trained-model migration)
| # | Task | Owner | Status |
|---|------|-------|--------|
| 1 | Verify/inspect candidate HF dataset (LTPhong) | me (script) + you (judge) | not started |
| 2 | `ml/collect_prs.py` GitHub scraper | me | **code written** — needs token + run |
| 3 | `ml/build_dataset.py` + synthetic slop + split | me build / **you label** | **code written** — needs run + human labeling |
| 4 | `ml/train.py` fine-tune CodeBERT | me build / **you run** | **code written** — needs you to run |
| 5 | `ml/evaluate.py` metrics; check precision ≥ ~0.85 | me build / **you judge** | **code written** — gates on ≥0.85, needs run |
| 6 | `model/` FastAPI `/predict` service | me | **code written** — needs trained artifact to serve |
| 7 | Rewrite Go `internal/triage` → call `MODEL_URL`; drop `GEMINI_API_KEY` | me | **done & verified** |
| 8 | `docker-compose.yml` (gateway + model) | me | **done** — `docker compose config` validates |

**Remaining to reach a running system:** provide `GITHUB_TOKEN`, run the `ml/`
pipeline (2→3→4→5), have a human label real rows in step 3 and judge precision in
step 5. Once the artifact lands in `model/model/`, `docker compose up` serves both.

**Division of labor:** I write all code (scrapers, training, inference, Go rewrite,
Docker). You provide a GitHub token, run the training (needs local Python/ML deps,
GPU helps), **label/judge the data**, and decide if model precision is good enough.

---

## ORPHANS & PENDING

- **No ready-made "AI-slop PR" dataset exists** (verified 2026-07-16 via Hugging
  Face API). Closest candidates: `LTPhong/CSC15011_Detecting_AI-Generated_Code`
  (~1M rows but no dataset card, viewer errors, unvetted) and generic PR dumps
  (`manoelalmeida-io/github-pullrequests`) with no quality labels. Human-code
  corpora (CodeSearchNet, The Stack) usable only for the LEGIT class. Building the
  SLOP class ourselves is unavoidable — and is the project's core value.
- **Web search tool is non-functional in this environment** — could only probe the
  HF dataset API via WebFetch, not do open-ended discovery. Academic benchmarks
  (CodeGPTSensor, MAGECODE) are known by name but unverified live; re-check when
  search works before finalizing the base model choice.
- **Remote push still blocked** — `gh` CLI not installed, no git remote configured.
  Needs `gh auth login` or manual `git remote add` + `git push -u`.
- **No live credentials exercised** — gateway verified only against fake servers;
  first real PR is the final smoke test.
```
