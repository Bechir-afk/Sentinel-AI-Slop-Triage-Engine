"""Task 5: evaluate the fine-tuned model on the held-out test set.

Loads ./model/model + dataset/test.parquet, runs inference, and reports
precision / recall / F1 and the confusion matrix. Gates on the acceptance bar:
SLOP-class precision must clear ~0.85 (PROJECT_MAP MODEL). Below that, false
positives insult real contributors — keep the threshold high / fail-open, or
fall back to the Gemini branch in git history. Exits non-zero if the bar is missed.

One-time, offline. Not in serving path.

Usage:
    python evaluate.py --model ../model/model --test dataset/test.parquet
"""

import argparse
import logging
import sys

import pandas as pd
import torch
from sklearn.metrics import classification_report, confusion_matrix, precision_score
from transformers import AutoModelForSequenceClassification, AutoTokenizer

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("sentinel.evaluate")

MAX_TOKENS = 512
SLOP = 1
PRECISION_BAR = 0.85


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="../model/model")
    ap.add_argument("--test", default="dataset/test.parquet")
    ap.add_argument("--bar", type=float, default=PRECISION_BAR)
    args = ap.parse_args()

    tokenizer = AutoTokenizer.from_pretrained(args.model)
    model = AutoModelForSequenceClassification.from_pretrained(args.model)
    model.eval()

    df = pd.read_parquet(args.test)
    preds = []
    with torch.no_grad():
        for title, diff in zip(df["title"], df["diff"]):
            text = f"{title}\n[SEP]\n{diff}"
            inputs = tokenizer(text, truncation=True, max_length=MAX_TOKENS, return_tensors="pt")
            logits = model(**inputs).logits
            preds.append(int(torch.argmax(logits, dim=-1).item()))

    y_true = df["label"].tolist()
    print(classification_report(y_true, preds, target_names=["LEGIT", "SLOP"]))
    print("confusion matrix (rows=true, cols=pred):")
    print(confusion_matrix(y_true, preds))

    slop_precision = precision_score(y_true, preds, pos_label=SLOP, zero_division=0)
    logger.info("SLOP-class precision = %.3f (bar %.2f)", slop_precision, args.bar)
    if slop_precision < args.bar:
        logger.error("precision below bar — do NOT trust this model in the gateway")
        sys.exit(1)
    logger.info("precision clears the bar")


if __name__ == "__main__":
    main()
