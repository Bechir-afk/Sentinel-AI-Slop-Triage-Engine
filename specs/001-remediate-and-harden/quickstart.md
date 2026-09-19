# Quickstart — Validating Sentinel Remediation and Hardening

Phase 1 output. Runnable end-to-end validation guide. Prerequisites: Docker,
Go 1.26, a GitHub token + webhook secret for the live smoke test (optional).

## V1. Missing artifact fails fast (FR-008, SC-001)

```bash
# ensure no artifact exists
rm -rf model/model
docker compose up sentinel-model
sleep 5 && docker compose logs sentinel-model | tail -3
```

**Expected**: one clear line naming the missing artifact path and the command
that produces it; container exits (not restarting with HF stack traces); no
log spam repeating the error.

## V2. Stack reaches healthy with an artifact (SC-001)

```bash
# produce or copy a trained artifact (full pipeline: README §Training), then:
docker compose up -d
docker compose ps
```

**Expected**: both services `healthy` (gateway healthcheck now exists,
FR-015); `curl localhost:8080/healthz` → `ok`.

## V3. Fast ack + async triage (FR-001, FR-002, SC-002)

```bash
# with a signed pull_request payload:
go test ./internal/webhook/ -run TestAckLatency -v   # (shipped with feature)
```

**Expected**: handler returns 200 in < 1s even when the model service hangs;
triage completes/aborts in the background independent of the client
connection.

## V4. Dedup, event gate, bot skip, body cap (FR-003..FR-006, SC-003)

```bash
go test ./internal/webhook/ ./internal/verify/ -v
```

**Expected**: same delivery ID delivered 3× → exactly one label + one comment
(fake client asserts call count); non-`pull_request` events → zero API calls;
bot author → no calls; 30 MiB body → 413 before HMAC work.

## V5. Fail-open holds (FR-007, SC-004)

```bash
go test ./internal/webhook/ -run TestFailOpen -v
```

**Expected**: injected diff-fetch / model / action errors → PR untouched, no
error surfaced to the sender.

## V6. Leakage-free dataset + honest gate (FR-009..FR-012, SC-005)

```bash
cd ml && python build_dataset.py   # asserts zero cross-split collisions
python train.py                     # prints precision/recall/F1 + confusion
python evaluate.py                  # exits 0 only if test precision ≥ 0.85
                                     # at the validation-selected threshold
```

**Expected**: build passes the collision assertion; training reports metrics;
evaluate reports the selected threshold with its validation precision and
gates the artifact accordingly.

## V7. Hardened, slim containers (FR-013, FR-014, SC-006)

```bash
docker compose build
docker image ls | grep sentinel   # model image ≥ 60% smaller than before
docker compose up -d && docker inspect \
  --format '{{.Config.User}} {{.HostConfig.CapDrop}} {{.HostConfig.ReadonlyRootfs}}' \
  sentinel-gateway-1 sentinel-model-1
```

**Expected**: non-root user, ALL capabilities dropped, read-only rootfs on
both; model image contains no `nvidia-*` packages (`docker run --rm <model-image> pip list | grep -c nvidia` → 0, or equivalent since slim).

## V8. Docs truth + CI (FR-016, FR-017, SC-007, SC-008)

```bash
grep -ri -e gemini -e GEMINI_API_KEY README.md AGENTS.md ; echo $?   # → 1 (no matches)
cat .env.example                                                      # lists every required var
git push origin HEAD                                                  # CI runs build+vet+test(-race)
```

**Expected**: search finds nothing; `.env.example` covers all required vars
(copy → fill → `docker compose up` works); the Actions check appears on the
push and passes.
