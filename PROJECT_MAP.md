# PROJECT_MAP — Sentinel: AI-Slop Triage Engine

Automated gatekeeper for open-source repos. A Go webhook microservice that
intercepts new GitHub Pull Requests, sends the code diff to Gemini for triage,
and (on high-confidence "slop") labels the PR and posts a polite comment.

**Scope boundary:** Working Go service + Docker image. The image is the unit of
deployment — runs on any Docker host with the four env vars set. Action on
flagged PRs is **label + comment only — never auto-close.**

---

## TECH_STACK

| Component        | Choice                                   | Version (verified 2026-07) | Rationale |
|------------------|------------------------------------------|----------------------------|-----------|
| Language         | Go                                       | 1.26.5                     | Tiny static binary, low-latency, ideal for a webhook microservice. |
| HTTP server      | stdlib `net/http`                        | (stdlib)                   | One endpoint; a framework would be unnecessary complexity. |
| Signature verify | stdlib `crypto/hmac`, `crypto/sha256`    | (stdlib)                   | Constant-time SHA256 verification of `X-Hub-Signature-256`. |
| GitHub API       | plain REST over `net/http`               | REST v3                    | Only 3 calls (fetch diff, add label, post comment); avoids the go-github dependency. |
| AI inference     | Google Gen AI SDK for Go                 | `google.golang.org/genai`  | `gemini-2.5-flash` (stable), structured JSON output via ResponseMIMEType + schema. |
| Container        | Distroless (`gcr.io/distroless/static:nonroot`) | —                   | Ships CA certs (required for HTTPS), runs non-root, still well under 20MB. |

**Sole third-party dependency:** `google.golang.org/genai`. Everything else is stdlib.

---

## SYSTEM_FLOW

```
GitHub PR opened
      │  HTTP POST (pull_request event)
      ▼
┌─────────────────────────────────────────────────────────────┐
│ POST /webhook                                                 │
│                                                               │
│ 1. HMAC middleware (internal/verify)                          │
│    - HMAC-SHA256(body, WEBHOOK_SECRET) vs X-Hub-Signature-256 │
│    - hmac.Equal (constant-time). Mismatch → 401, drop.        │
│                                                               │
│ 2. Parse payload (internal/webhook)                           │
│    - action == "opened" | "reopened" | "synchronize" only     │
│    - extract owner, repo, PR number, title, author            │
│    - (payload has NO diff — only a diff_url)                  │
│                                                               │
│ 3. Fetch diff (internal/github)                               │
│    - GET /repos/{owner}/{repo}/pulls/{n}                       │
│      Accept: application/vnd.github.v3.diff                   │
│      Authorization: Bearer GITHUB_TOKEN                       │
│                                                               │
│ 4. Triage (internal/triage)                                   │
│    - send diff + title to gemini-2.5-flash                    │
│    - ResponseMIMEType application/json + strict schema        │
│    - returns { is_slop: bool, confidence: float, reason: str }│
│                                                               │
│ 5. Act (internal/github) — only if is_slop &&                 │
│    confidence >= CONFIDENCE_THRESHOLD:                        │
│    - POST issues/{n}/labels  → "needs-human-review"          │
│    - POST issues/{n}/comments → polite explanation            │
│    - else: no-op                                              │
│                                                               │
│ Respond 200 quickly; log outcome. Never auto-close.           │
└─────────────────────────────────────────────────────────────┘
```

---

## CONFIGURATION (all via environment variables)

| Var                    | Required | Default              | Purpose |
|------------------------|----------|----------------------|---------|
| `GITHUB_WEBHOOK_SECRET`| yes      | —                    | Shared secret for HMAC verification. |
| `GITHUB_TOKEN`         | yes      | —                    | PAT/app token to fetch diff + write label/comment. |
| `GEMINI_API_KEY`       | yes      | —                    | Auth for the Gen AI SDK. |
| `CONFIDENCE_THRESHOLD` | no       | `0.90`               | Minimum confidence to act. |
| `SLOP_LABEL`           | no       | `needs-human-review` | Label applied to flagged PRs. |
| `PORT`                 | no       | `8080`               | Listen port. |

---

## REPOSITORY LAYOUT

```
cmd/sentinel/main.go        Config load, dependency wiring, ListenAndServe
internal/verify/            HMAC-SHA256 constant-time middleware
internal/webhook/           Payload parsing + pipeline orchestration
internal/github/            REST client: fetch diff, add label, post comment
internal/triage/            Gemini client + JSON schema + prompt
deploy/Dockerfile           Multi-stage build → distroless static:nonroot
go.mod / go.sum
```

---

## CORRECTIONS TO ORIGINAL BRIEF (resolved during planning)

1. **Diff is not in the webhook payload.** GitHub's `pull_request` event carries
   only metadata + `diff_url`. Sentinel must fetch the diff via the API, which
   requires a `GITHUB_TOKEN` (not mentioned in the original brief).
2. **`scratch` image cannot make HTTPS calls** (no CA certs). Using
   `gcr.io/distroless/static:nonroot` instead — same size goal, actually works,
   runs non-root.
3. **No auto-close.** Original brief mentioned closing PRs; decided against it to
   keep a human in the loop and minimize false-positive blast radius.

---

## OUT OF SCOPE (do not build)

- Live OpenShift/Kubernetes deployment (manifests only).
- Auto-closing PRs.
- Persistence / database / queue.
- Retry/backoff beyond a single attempt (add later if rate limits bite).
- Any certificate/badge generator (unrelated project from the source brief; excluded).
```