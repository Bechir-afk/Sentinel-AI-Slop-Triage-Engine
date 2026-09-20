"""Task 5: evaluate the fine-tuned model on the held-out test set.

Loads ./model/model + dataset/test.parquet, runs inference, and gates on the
acceptance bar at the SELECTED threshold (from threshold.json, written by
train.py — FR-010/FR-012). Applying the same data-chosen threshold the model
service will use makes the test-set number honest: it is the precision the
deployed system will actually operate at, not the precision at an arbitrary
0.5 argmax. Reports precision/recall/F1 + confusion; exits non-zero below bar.

One-time, offline. Not in serving path.

Usage:
    python evaluate.py --model ../model/model --test dataset/test.parquet
"""

import argparse
import json
import logging
import os
import sys

import pandas as pd
import torch
from sklearn.metrics import classification_report, confusion_matrix, precision_recall_fscore_support
from transformers import AutoModelForSequenceClassification, AutoTokenizer

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("sentinel.evaluate")

MAX_TOKENS = 512
SLOP = 1
PRECISION_BAR = 0.85


def _load_threshold(model_dir: str) -> tuple[float, float]:
    """Read (threshold, val_precision) from threshold.json; fall back to a
    conservative 0.95 argmax-equivalent if the file is absent (older artifact)."""
    path = os.path.join(model_dir, "threshold.json")
    if not os.path.isfile(path):
        logger.warning("no threshold.json at %s; falling back to 0.95", path)
        return 0.95, float("nan")
    with open(path, encoding="utf-8") as fh:
        data = json.load(fh)
    return float(data["threshold"]), float(data.get("val_precision", float("nan")))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="../model/model")
    ap.add_argument("--test", default="dataset/test.parquet")
    ap.add_argument("--bar", type=float, default=PRECISION_BAR)
    args = ap.parse_args()

    threshold, val_precision = _load_threshold(args.model)
    logger.info("evaluating at selected threshold %.2f (val precision %.3f)", threshold, val_precision)

    tokenizer = AutoTokenizer.from_pretrained(args.model)
    model = AutoModelForSequenceClassification.from_pretrained(args.model)
    model.eval()

    df = pd.read_parquet(args.test)
    slop_probs = []
    with torch.no_grad():
        for title, diff in zip(df["title"], df["diff"]):
            text = f"{title}\n[SEP]\n{diff}"
            inputs = tokenizer(text, truncation=True, max_length=MAX_TOKENS, return_tensors="pt")
            logits = model(**inputs).logits
            slop_probs.append(float(torch.softmax(logits, dim=-1)[0][SLOP].item()))

    y_true = df["label"].tolist()
    preds = [SLOP if p >= threshold else 0 for p in slop_probs]

    print(classification_report(y_true, preds, target_names=["LEGIT", "SLOP"], zero_division=0))
    print("confusion matrix (rows=true, cols=pred):")
    print(confusion_matrix(y_true, preds))

    p, _, _, _ = precision_recall_fscore_support(
        y_true, preds, labels=[SLOP], average=None, zero_division=0
    )
    slop_precision = float(p[0])
    logger.info("test-set SLOP precision @ %.2f = %.3f (bar %.2f)", threshold, slop_precision, args.bar)
    if slop_precision < args.bar:
        logger.error("precision below bar — do NOT trust this model in the gateway")
        sys.exit(1)
    logger.info("precision clears the bar")


if __name__ == "__main__":
    main()
