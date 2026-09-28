# Contract: Service Endpoints (bring-up verification)

The endpoints this feature queries to prove the stack is up. These are the
**shipped** contracts (confirmed in `model/app.py` and `cmd/sentinel/main.go`);
this feature reads them, it does not change them.

## Model service — `GET /healthz`

Internal only (compose network, port 9000). Response:

```json
{ "status": "ok", "threshold": 0.87, "artifact": "model", "version": "dev" }
```

| Field | Source | Used for |
|-------|--------|----------|
| `status` | fixed `"ok"` when serving | liveness |
| `threshold` | `threshold.json` (or 0.95 fallback) | gateway sources this as its cutoff |
| `artifact` | artifact directory basename | audit — which artifact produced verdicts |
| `version` | `MODEL_VERSION` env (default `dev`) | build identity |

**Check**: `status == "ok"` and `threshold` equals the value in `threshold.json`.

## Model service — `POST /predict`

Request `{title, diff}` (+ optional `X-Correlation-Id` header) →
`{is_slop, confidence, reason, version}`. Not directly exercised by bring-up
verification (the gateway calls it), but it is the contract the gateway depends
on. Never logs the diff body.

## Gateway — `GET /healthz`

Public (port 8080). Returns 200 when the process is up. Also used by the
container healthcheck via the binary's `-healthz` self-probe flag.

**Check**: HTTP 200.

## Gateway — `GET /stats`

Public (port 8080). Response is operational counters plus the build version:

```json
{ "...counters...": 0, "version": "dev" }
```

**Check**: HTTP 200 and JSON parses; counters present. `/stats` needs no auth.

## Threshold parity (SC-006)

With `CONFIDENCE_THRESHOLD` unset in `.env`, the gateway sources its cutoff from
the model's `/healthz` at startup (confirmed in `main.go`: it warns and uses the
default 0.95 only when healthz is unavailable). Verification asserts the
gateway's effective threshold equals the model's advertised `threshold`.

## Observe-only (FR-014 / SC-007)

With `SHADOW_MODE=true`, a processed PR event yields a logged verdict (with its
correlation id) and **zero** GitHub writes. With it unset/false, an above-
threshold verdict applies the `SLOP_LABEL` and a comment. This toggle is read by
`internal/config/config.go`; no code change is required to exercise either mode.
