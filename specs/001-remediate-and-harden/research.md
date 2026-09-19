# Research — Sentinel Remediation and Hardening

Phase 0 output. Every NEEDS CLARIFICATION from the plan's Technical Context is
resolved here with a decision, rationale, and alternatives considered.

## R1. Asynchronous webhook processing without a queue broker

**Decision**: Acknowledge immediately (HTTP 200), then run the triage pipeline
in a goroutine started by the handler, using a `context.Background()` derived
context with its own timeout (45s budget), and a bounded worker pool
(buffered channel, default 8 workers) so a burst cannot spawn unbounded
goroutines.

**Rationale**: GitHub requires a 2xx within 10 seconds and terminates slow
connections; their guidance is to "set up a queue to process webhook payloads
asynchronously". The current handler does everything synchronously on
`r.Context()`, so a client disconnect cancels triage mid-flight and combined
downstream timeouts (15s + 30s) can exceed 10s. A goroutine pool is stdlib,
honors constitution V, and matches the single-node scope.

**Alternatives considered**:
- External queue (RabbitMQ, Redis) — rejected: violates no-database stance and
  adds a service for a minutes-apart event rate (YAGNI).
- Unbounded `go func()` per request — rejected: no backpressure; a webhook
  replay storm would exhaust memory.
- HTTP 202 + GitHub retry of "failures" — rejected as a design lever: GitHub
  redelivers on non-2xx, and we want redelivery to be an idempotent path, not
  the primary transport.

## R2. Idempotency / duplicate-delivery handling

**Decision**: In-memory bounded ledger (mutex-protected map of delivery ID →
timestamp, capped at 1024 entries with oldest-eviction) keyed on the
`X-GitHub-Delivery` header. Present → ack 200, skip. Absent → record, process.
The ledger is checked and claimed atomically at enqueue time so a redelivery
racing an in-flight delivery is also suppressed.

**Rationale**: `X-GitHub-Delivery` is GitHub's documented unique-per-delivery
identifier. Manual redeliveries reuse the original ID, which is exactly the
duplication we want to suppress. In-memory honors the no-database stance.

**Alternatives considered**:
- Persistent dedup store — rejected: adds storage to the serving path.
- Idempotency at the action level (check label presence before posting) —
  rejected as primary mechanism: costs an extra API call per event and still
  races; kept as a possible future refinement, not needed now.
- No dedup — rejected: duplicate comments on redelivery are user-visible spam
  and violate the "polite bot" intent.

**Documented ceiling** (ponytail): a gateway restart forgets the ledger, so a
redelivery after restart can double-act. Upgrade path: persist last-seen IDs
in a small file or check label presence before commenting.

## R3. Event-type and action gating

**Decision**: Require `X-GitHub-Event: pull_request` (missing/other → ack 200,
ignore, zero API calls) before parsing the payload. Keep the existing
actionable-action set {opened, reopened, synchronize}. Bot skip: if
`pull_request.user.login` ends with `[bot]` or equals `dependabot[bot]`-style
names, skip triage.

**Rationale**: GitHub's own best practices say to inspect `X-GitHub-Event` and
`action` first. Today an `issues` event with action `opened` parses to
PR-number 0 and triggers a pointless `pulls/0` fetch. Bot-authored PRs
(renovate, dependabot) are automation, not contributors — triaging them wastes
model calls and risks labeling automated maintenance.

**Alternatives considered**: An allowlist of bot logins — rejected: suffix
check covers the general case with one line.

## R4. Body size cap

**Decision**: `http.MaxBytesReader` (default 25 MiB — GitHub's documented
maximum webhook payload size; configurable via `MAX_BODY_BYTES`) applied in the
HMAC middleware before `io.ReadAll`. Oversized → 413, no HMAC work, no parse.

**Rationale**: The middleware currently reads the body unbounded — a trivial
memory-exhaustion vector on a public endpoint. Constitution VII requires a
size cap at the trust boundary. 25 MiB is GitHub's ceiling, so no legitimate
delivery is rejected.

## R5. ML train/test leakage from synthetic generators

**Decision**: Generate synthetic slop per split. The dataset builder creates
each split's synthetic rows with disjoint random seeds and randomized content
(filenames drawn from per-split pools, titles varied per example), so no
template instance appears in more than one split. A post-build assertion
checks train∩test and train∩val title/diff-hash intersection is empty for
synthetic rows.

**Rationale**: Fixed filenames + the fixed title "Update code" across all
synthetic rows give the classifier a shortcut feature present in both train
and test — the 0.85 precision gate then measures memorization, not
generalization. Randomizing generators per split (with a collision assertion)
is the smallest change that makes the gate honest again.

**Alternatives considered**:
- Real (scraped) slop only in test — rejected: real slop is scarce (the
  documented orphan); the test set must stay large enough to trust.
- Group-split by generator family only — rejected as sufficient alone: still
  shares the fixed title across splits; randomization + assertion is stricter.

## R6. Confidence threshold selection and calibration

**Decision**: After training, sweep candidate thresholds on the validation
split, pick the lowest threshold whose SLOP precision ≥ 0.85, and persist it
with the artifact (e.g. `threshold.json` next to the weights). The evaluation
gate then reports test-set precision at that selected threshold. The gateway
keeps its `CONFIDENCE_THRESHOLD` env override; default becomes the artifact's
selected value (the model service exposes it; gateway falls back to 0.95 —
conservative — when unknown).

**Rationale**: Softmax confidence is uncalibrated, so the hardcoded 0.90 has
no probabilistic meaning. Selecting the threshold on held-out data for a
target precision is the standard minimal calibration technique (decision
thresholding) and directly serves constitution III. A separate test split
still gives the honest final number.

**Alternatives considered**:
- Temperature scaling / Platt scaling — deferred: useful when the threshold
  must be portable across model versions; plain threshold selection on
  validation data is sufficient at this dataset size. Upgrade path noted.
- Keep 0.90 — rejected: arbitrary and unmeasured.

## R7. Model service fail-fast on missing artifact

**Decision**: At startup, before loading, check the artifact path exists and
contains the expected files; if missing, log one actionable message
("model artifact not found at /app/model — produce it with
`python ml/train.py` (see README §Training) or mount it") and exit non-zero.
Compose `restart: unless-stopped` combined with a non-zero exit yields a
stopped container with the message in `docker logs` — no stack-trace loop.

**Rationale**: The service currently imports `inference.py`, which calls
`from_pretrained` at import time — a missing artifact produces a raw HF stack
trace, the container restarts forever, and because the gateway's
`depends_on: service_healthy` never resolves, `docker compose up` hangs with
no explanation. One explicit check converts that into an actionable failure.
This honors the Quality Gate "model service MUST NOT crash-loop when its
artifact is absent" with ~10 lines.

**Alternatives considered**: Serve `/healthz` but return 503 until artifact
appears (hot-swap) — rejected: nobody mounts artifacts into a running
deployment here; fail-fast is simpler and matches the Quality Gate wording
("clear, logged startup failure").

## R8. CPU-only PyTorch in the model image

**Decision**: In `model/Dockerfile`, install torch from the CPU index in a
separate step with `--index-url https://download.pytorch.org/whl/cpu` (this
replaces PyPI as the index for that step, so no CUDA wheel can win), then
install the rest from PyPI.

**Rationale**: pip gives `--extra-index-url` no priority over PyPI — the CUDA
build of torch is selected and the image balloons by GBs of nvidia packages.
`--index-url` (not extra) on the dedicated torch step is the documented way to
force the CPU wheel. Expect well over the SC-006 60% size reduction.

**Alternatives considered**: `--no-deps torch` + manual deps — rejected:
fragile across versions. Uninstalling nvidia packages after install —
rejected: still downloads GBs and leaves layers.

## R9. Container hardening specifics

**Decision**: Both services: `user:` non-root, `cap_drop: [ALL]`,
`security_opt: [no-new-privileges:true]`, `read_only: true` with a writable
tmpfs for `/tmp`. Model image: create and `USER` a dedicated non-root account.
Gateway: add compose `healthcheck` hitting `/healthz`. Resource limits:
`mem_limit` 64m gateway / 2g model, `cpus` 0.5 / 2.0.

**Rationale**: Standard minimal Docker hardening for a public-facing endpoint
(gateway) and its internal dependency. The model needs the larger budget for a
~125M-param encoder + tokenizer; 2g leaves headroom. Distroless gateway already
runs non-root; compose-level settings make the model match it.

**Alternatives considered**: Rootless podman/K8s manifests — rejected:
out of scope per PROJECT_MAP ("do not build" list).

## R10. CI

**Decision**: Single GitHub Actions workflow (`.github/workflows/ci.yml`) on
push + PR: `go build ./...`, `go vet ./...`, `go test ./... -race` on
`ubuntu-latest` with Go 1.26.

**Rationale**: Zero CI exists today; the Quality Gate demands build/vet/test
before merge, which requires automation to enforce. `-race` is free on the
worker pool + ledger. Python checks stay out of CI for now: the ml/ pipeline
needs torch (heavy) and is a documented offline, human-judged activity — the
collision assertion (R5) runs locally at dataset build time. Ceiling noted;
upgrade path is a separate job with cached torch CPU wheels.

**Sources consulted** (2026-09-07): GitHub docs —
[Best practices for using webhooks](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks),
[Best practices for integrators](https://docs.github.com/en/rest/guides/best-practices-for-integrators?apiVersion=2022-11-28);
[Svix webhook timeout practices](https://www.svix.com/resources/webhook-university/reliability/webhook-timeout-best-practices/);
[pip index priority (Stack Overflow)](https://stackoverflow.com/questions/67253141/pip-priority-order-with-index-url-and-extra-index-url);
[PyTorch CPU wheel index](https://download.pytorch.org/whl/cpu);
[PyTorch docker size discussion](https://discuss.pytorch.org/t/reducing-docker-size-with-pytorch-model/78991).
