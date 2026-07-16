"""FastAPI wrapper for the model service (SYSTEM_FLOW model box).

One route: POST /predict {title, diff} -> {is_slop, confidence, reason},
the exact contract the Go gateway's triage.Result expects. GET /healthz for
container liveness. No auth: this service binds to the compose network only,
never exposed publicly (see docker-compose.yml).
"""

import logging

from fastapi import FastAPI
from pydantic import BaseModel

import inference

logging.basicConfig(level=logging.INFO)
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
    return {"status": "ok"}


@app.post("/predict", response_model=PredictResponse)
def predict(req: PredictRequest):
    result = inference.predict(req.title, req.diff)
    logger.info("verdict is_slop=%s confidence=%.2f", result["is_slop"], result["confidence"])
    return result
