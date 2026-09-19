# Contract: Model Service `/predict` + `/healthz`

Phase 1 output. Internal compose-network interface; shape preserved so the Go
client is unchanged except for threshold sourcing.

## `POST /predict`

### Request

```json
{ "title": "Fix race in session store", "diff": "diff --git a/..." }
```

Diff truncated to 50 KiB by the caller, then to 512 tokens internally.

### Response

```json
{ "is_slop": true, "confidence": 0.93, "reason": "comment-only diff" }
```

| Field | Rule |
|-------|------|
| `is_slop` | bool — argmax verdict. |
| `confidence` | float [0,1] — softmax probability of the predicted class. |
| `reason` | string — templated human-readable signal. |

Malformed input → **422** (FastAPI default). Server error → non-200; the
gateway treats any non-200/decode failure as fail-open drop (constitution II).

## `GET /healthz`

**200** with:

```json
{ "status": "ok", "threshold": 0.87, "artifact": "model" }
```

`threshold` is the artifact-selected confidence threshold (FR-010), read from
`threshold.json` shipped with the artifact. The gateway may source its default
from here; its env override still wins.

**503** if serving despite a degraded state (not the missing-artifact case —
that exits at startup per FR-008).

## Startup contract

- Artifact present → load, serve.
- Artifact absent → ONE log line naming the path and the producing command,
  exit non-zero. No stack-trace crash loop.

## Configuration surface

| Env | Default | Meaning |
|-----|---------|---------|
| `MODEL_PATH` | `./model` | Artifact directory (weights + tokenizer + `threshold.json`). |
| `PORT` | `9000` | Listen port. |

Network: `expose` only — never published to the host; no auth by design
(compose-internal, unchanged).
