"""Inference core: tokenize -> forward pass -> softmax -> Result.

Loads the fine-tuned CodeBERT classifier (LEGIT=0, SLOP=1) once at import
and scores a (title, diff) pair. The model gives the score; `reason` is a
lightweight heuristic label built from the diff for the PR comment.
See PROJECT_MAP MODEL section for the contract.
"""

import json
import logging
import os
import re
import sys

import torch
from transformers import AutoModelForSequenceClassification, AutoTokenizer

import encoding

logger = logging.getLogger("sentinel.model")

MODEL_PATH = os.getenv("MODEL_PATH", "./model")
MAX_TOKENS = encoding.MAX_TOKENS
SLOP = 1
# Conservative fallback when the artifact ships no threshold.json (older
# artifact): only very high-confidence slop acts. train.py writes the real,
# validation-selected value.
DEFAULT_THRESHOLD = 0.95

# A usable artifact directory has a model config, a safetensors weights file, and
# tokenizer files. We require safetensors (not the legacy pickle .bin): the model
# is loaded with use_safetensors=True (FR-011), so a .bin-only artifact can't
# serve — catch it here with an actionable message rather than a load-time trace.
_REQUIRED_ANY = {
    "weights": ("model.safetensors",),
    "tokenizer": ("tokenizer.json", "vocab.json", "tokenizer_config.json"),
}


def _verify_artifact(path: str) -> None:
    """Fail fast with ONE actionable line if the artifact is missing/incomplete.

    Without this, from_pretrained raises a long Hugging Face stack trace on a
    missing artifact and — with the container's restart policy — crash-loops with
    no explanation (FR-008). Here we check first and exit(1) with the path and the
    command that produces the artifact, so an operator sees exactly what to do.
    """
    problems = []
    if not os.path.isdir(path):
        problems.append(f"directory {path!r} does not exist")
    else:
        if not os.path.isfile(os.path.join(path, "config.json")):
            problems.append("missing config.json")
        for kind, names in _REQUIRED_ANY.items():
            if not any(os.path.isfile(os.path.join(path, n)) for n in names):
                problems.append(f"missing {kind} file (one of: {', '.join(names)})")
    if problems:
        logger.error(
            "model artifact unusable at MODEL_PATH=%s: %s. Produce it offline with "
            "`python ml/train.py --data ml/dataset --out model/model` (see README "
            "§Training), then mount that directory at this path.",
            path,
            "; ".join(problems),
        )
        sys.exit(1)


# Verify before loading so a missing artifact is one clear message, not a
# crash-loop. This runs at import (i.e. model-service startup, via app.py).
_verify_artifact(MODEL_PATH)

# Loaded once at import; a forward pass reuses these.
_tokenizer = AutoTokenizer.from_pretrained(MODEL_PATH)
# use_safetensors=True: load model.safetensors only, never unpickle a
# pytorch_model.bin. An artifact carries weights we did not necessarily produce
# (it's mounted at MODEL_PATH), so refusing the pickle format closes an
# arbitrary-code-execution path at load time (FR-011). train.py writes
# safetensors (save_safetensors=True); a legacy .bin artifact now fails loudly
# here rather than silently deserializing.
_model = AutoModelForSequenceClassification.from_pretrained(MODEL_PATH, use_safetensors=True)
_model.eval()
logger.info("loaded model from %s", MODEL_PATH)


def _load_threshold(path: str) -> tuple[float, float]:
    """Read (threshold, val_precision) from threshold.json beside the artifact
    (written by ml/train.py). Falls back to DEFAULT_THRESHOLD if absent so an
    older artifact still serves conservatively."""
    fp = os.path.join(path, "threshold.json")
    if not os.path.isfile(fp):
        logger.warning("no threshold.json at %s; using default %.2f", fp, DEFAULT_THRESHOLD)
        return DEFAULT_THRESHOLD, float("nan")
    try:
        with open(fp, encoding="utf-8") as fh:
            data = json.load(fh)
        return float(data["threshold"]), float(data.get("val_precision", float("nan")))
    except (ValueError, KeyError, OSError) as e:
        logger.warning("threshold.json unreadable (%s); using default %.2f", e, DEFAULT_THRESHOLD)
        return DEFAULT_THRESHOLD, float("nan")


THRESHOLD, VAL_PRECISION = _load_threshold(MODEL_PATH)
# Artifact version: the directory name is the operator-facing artifact tag.
ARTIFACT_VERSION = os.path.basename(os.path.normpath(MODEL_PATH))
logger.info("serving at threshold %.2f (artifact %s)", THRESHOLD, ARTIFACT_VERSION)


def configure_threads(n: int) -> None:
    """Bound intra-op CPU parallelism to n threads. Called once at startup from
    MODEL_THREADS (app.py) so the container's CPU limit isn't oversubscribed —
    torch otherwise spawns one thread per core, thrashing under compose's cpus
    cap (FR-007). No-op for n < 1."""
    if n and n >= 1:
        torch.set_num_threads(n)
        logger.info("torch intra-op threads set to %d", n)


def warmup() -> None:
    """Run one forward pass at startup so the first real request doesn't eat the
    lazy-init cost (kernel autotuning, allocator warmup) — that latency spike is
    the difference between a sub-second first triage and a multi-second one
    (FR-007). Best-effort: a warmup failure must not stop the service."""
    try:
        predict("warmup", "diff --git a/x b/x\n+noop\n")
        logger.info("warmup forward pass complete")
    except Exception as e:  # noqa: BLE001 — warmup is advisory, never fatal
        logger.warning("warmup pass failed (continuing): %s", e)


def _encode(title: str, diff: str):
    """Encode (title, diff) as a CodeBERT sentence pair (real separator, diff-only
    truncation) — see encoding.encode_pair. Same code path train.py uses."""
    return encoding.encode_pair(_tokenizer, title, diff, return_tensors="pt")


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
