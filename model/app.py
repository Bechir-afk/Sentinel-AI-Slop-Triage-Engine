"""FastAPI wrapper for the model service (SYSTEM_FLOW model box).

One route: POST /predict {title, diff} -> {is_slop, confidence, reason},
the exact contract the Go gateway's triage.Result expects. GET /healthz for
container liveness. No auth: this service binds to the compose network only,
never exposed publicly (see docker-compose.yml).
"""

import json
import logging
import os

from fastapi import FastAPI, Header
from pydantic import BaseModel

import inference

# MODEL_VERSION is the model-service build tag, reported on /healthz (FR-004).
# Defaults to "dev"; set via the container build/env for a real deployment.
MODEL_VERSION = os.getenv("MODEL_VERSION", "dev")

# Structured (JSON) logs so a single PR is machine-traceable across both services
# by correlation ID, matching the gateway's slog JSON handler (FR-001). Only safe
# fields are ever logged here — never the diff body.
_SAFE_EXTRAS = ("correlation_id", "is_slop", "confidence")


class _JSONFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        payload = {
            "time": self.formatTime(record, "%Y-%m-%dT%H:%M:%S%z"),
            "level": record.levelname,
            "logger": record.name,
            "msg": record.getMessage(),
        }
        for key in _SAFE_EXTRAS:
            if hasattr(record, key):
                payload[key] = getattr(record, key)
        return json.dumps(payload)


_handler = logging.StreamHandler()
_handler.setFormatter(_JSONFormatter())
logging.basicConfig(level=logging.INFO, handlers=[_handler], force=True)
logger = logging.getLogger("sentinel.model")

app = FastAPI(title="sentinel-model")


class PredictRequest(BaseModel):
    title: str = ""
    diff: str = ""


class PredictResponse(BaseModel):
    is_slop: bool
    confidence: float
    reason: str


@app.get("/healthz")
def healthz():
    # threshold + artifact let the gateway source its default cutoff and log
    # which artifact produced a verdict (predict-endpoint contract).
    return {
        "status": "ok",
        "threshold": inference.THRESHOLD,
        "artifact": inference.ARTIFACT_VERSION,
        "version": MODEL_VERSION,
    }


@app.post("/predict", response_model=PredictResponse)
def predict(
    req: PredictRequest,
    x_correlation_id: str = Header(default=""),
):
    # The gateway threads its per-delivery ID here (FR-002); echo it into this
    # request's log line so a single PR is traceable across both services. A
    # non-GitHub caller may omit it — never fail on a missing header.
    result = inference.predict(req.title, req.diff)
    logger.info(
        "verdict",
        extra={
            "correlation_id": x_correlation_id or "-",
            "is_slop": result["is_slop"],
            "confidence": round(result["confidence"], 4),
        },
    )
    return result
