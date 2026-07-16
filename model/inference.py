"""Inference core: tokenize -> forward pass -> softmax -> Result.

Loads the fine-tuned CodeBERT classifier (LEGIT=0, SLOP=1) once at import
and scores a (title, diff) pair. The model gives the score; `reason` is a
lightweight heuristic label built from the diff for the PR comment.
See PROJECT_MAP MODEL section for the contract.
"""

import logging
import os
import re

import torch
from transformers import AutoModelForSequenceClassification, AutoTokenizer

logger = logging.getLogger("sentinel.model")

MODEL_PATH = os.getenv("MODEL_PATH", "./model")
MAX_TOKENS = 512
SLOP = 1

# Loaded once at import; a forward pass reuses these.
_tokenizer = AutoTokenizer.from_pretrained(MODEL_PATH)
_model = AutoModelForSequenceClassification.from_pretrained(MODEL_PATH)
_model.eval()
logger.info("loaded model from %s", MODEL_PATH)


def _encode(title: str, diff: str):
    """Build the '<title>\\n[SEP]\\n<diff>' input, head-truncated to 512 tokens."""
    text = f"{title}\n[SEP]\n{diff}"
    return _tokenizer(
        text,
        truncation=True,
        max_length=MAX_TOKENS,
        return_tensors="pt",
    )


def _reason(diff: str, is_slop: bool) -> str:
    """Templated, human-readable label from cheap diff signals (not the model)."""
    if not is_slop:
        return "the change looks substantive."

    added = [ln for ln in diff.splitlines() if ln.startswith("+") and not ln.startswith("+++")]
    code_added = [ln[1:].strip() for ln in added]
    comment_only = added and all(
        (not ln) or ln.startswith(("#", "//", "*", "/*", "<!--")) for ln in code_added
    )
    tokens = re.findall(r"\w+", diff)
    unique_ratio = len(set(tokens)) / len(tokens) if tokens else 1.0

    if comment_only:
        return "the change adds only comments or docstrings."
    if unique_ratio < 0.25:
        return "the diff is large with a low unique-token ratio (repetitive/boilerplate)."
    return "the change appears superficial or low-effort."


def predict(title: str, diff: str) -> dict:
    """Return {is_slop, confidence, reason} for a PR."""
    inputs = _encode(title, diff)
    with torch.no_grad():
        logits = _model(**inputs).logits
    probs = torch.softmax(logits, dim=-1)[0]
    pred = int(torch.argmax(probs).item())
    is_slop = pred == SLOP
    confidence = float(probs[pred].item())
    return {
        "is_slop": is_slop,
        "confidence": confidence,
        "reason": _reason(diff, is_slop),
    }
