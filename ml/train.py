"""Task 4: fine-tune CodeBERT + classification head, save the artifact.

Loads the parquet splits from build_dataset.py, encodes '<title>\\n[SEP]\\n<diff>'
to 512 tokens, and fine-tunes microsoft/codebert-base with a 2-class head
(LEGIT=0, SLOP=1). Saves weights + tokenizer to --out so the model service can
AutoModelForSequenceClassification.from_pretrained(MODEL_PATH).

After training it reports SLOP precision/recall/F1 + confusion on the
validation split, then SELECTS a confidence threshold: the lowest candidate
whose validation SLOP precision clears the bar (default 0.85). That threshold
and its measured precision are written to `threshold.json` beside the artifact
(FR-010/FR-011) so evaluate.py and the model service use an honest, data-chosen
cutoff rather than a hard-coded guess. A fixed seed makes the run reproducible.

One-time, offline. Fits a single consumer GPU or CPU (slow). Not in serving path.

Usage:
    python train.py --data dataset --out ../model/model --epochs 3
"""

import argparse
import json
import logging
import os

import numpy as np
import pandas as pd
from datasets import Dataset
from sklearn.metrics import confusion_matrix, precision_recall_fscore_support
from transformers import (
    AutoModelForSequenceClassification,
    AutoTokenizer,
    Trainer,
    TrainingArguments,
    set_seed,
)

from thresholds import SLOP, select_threshold, softmax

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("sentinel.train")

BASE_MODEL = "microsoft/codebert-base"
MAX_TOKENS = 512
SEED = 42
DEFAULT_BAR = 0.85


def _to_dataset(path: str, tokenizer) -> Dataset:
    df = pd.read_parquet(path)
    ds = Dataset.from_pandas(df, preserve_index=False)

    def encode(batch):
        texts = [f"{t}\n[SEP]\n{d}" for t, d in zip(batch["title"], batch["diff"])]
        out = tokenizer(texts, truncation=True, max_length=MAX_TOKENS, padding="max_length")
        out["labels"] = batch["label"]
        return out

    return ds.map(encode, batched=True, remove_columns=ds.column_names)


def compute_metrics(eval_pred):
    """SLOP-class precision/recall/F1 for Trainer's per-epoch eval (argmax)."""
    logits, labels = eval_pred
    preds = np.argmax(logits, axis=-1)
    p, r, f1, _ = precision_recall_fscore_support(
        labels, preds, labels=[SLOP], average=None, zero_division=0
    )
    return {"slop_precision": float(p[0]), "slop_recall": float(r[0]), "slop_f1": float(f1[0])}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--data", default="dataset")
    ap.add_argument("--out", default="../model/model")
    ap.add_argument("--epochs", type=float, default=3)
    ap.add_argument("--batch-size", type=int, default=8)
    ap.add_argument("--lr", type=float, default=2e-5)
    ap.add_argument("--bar", type=float, default=DEFAULT_BAR)
    args = ap.parse_args()

    set_seed(SEED)  # reproducible weights + eval

    tokenizer = AutoTokenizer.from_pretrained(BASE_MODEL)
    model = AutoModelForSequenceClassification.from_pretrained(
        BASE_MODEL, num_labels=2, id2label={0: "LEGIT", 1: "SLOP"}, label2id={"LEGIT": 0, "SLOP": 1}
    )

    train_ds = _to_dataset(os.path.join(args.data, "train.parquet"), tokenizer)
    val_ds = _to_dataset(os.path.join(args.data, "val.parquet"), tokenizer)

    targs = TrainingArguments(
        output_dir="./checkpoints",
        num_train_epochs=args.epochs,
        per_device_train_batch_size=args.batch_size,
        per_device_eval_batch_size=args.batch_size,
        learning_rate=args.lr,
        eval_strategy="epoch",
        save_strategy="epoch",
        load_best_model_at_end=True,
        metric_for_best_model="slop_f1",
        seed=SEED,
        data_seed=SEED,
        logging_steps=50,
    )

    trainer = Trainer(
        model=model,
        args=targs,
        train_dataset=train_ds,
        eval_dataset=val_ds,
        compute_metrics=compute_metrics,
    )
    trainer.train()

    os.makedirs(args.out, exist_ok=True)
    trainer.save_model(args.out)
    tokenizer.save_pretrained(args.out)

    # Threshold selection on the validation split (never the test split).
    pred = trainer.predict(val_ds)
    probs = softmax(np.asarray(pred.predictions))[:, SLOP]
    y_true = np.asarray(pred.label_ids)

    # Report validation metrics at the chosen operating point.
    threshold, val_precision = select_threshold(probs, y_true, args.bar)
    val_pred = (probs >= threshold).astype(int)
    p, r, f1, _ = precision_recall_fscore_support(
        y_true, val_pred, labels=[SLOP], average=None, zero_division=0
    )
    logger.info("validation @ threshold %.2f: SLOP precision=%.3f recall=%.3f f1=%.3f",
                threshold, p[0], r[0], f1[0])
    logger.info("validation confusion (rows=true, cols=pred):\n%s",
                confusion_matrix(y_true, val_pred))

    threshold_path = os.path.join(args.out, "threshold.json")
    with open(threshold_path, "w", encoding="utf-8") as fh:
        json.dump({"threshold": round(float(threshold), 4),
                   "val_precision": round(float(val_precision), 4),
                   "bar": args.bar}, fh)
    logger.info("saved fine-tuned artifact + %s to %s", os.path.basename(threshold_path), args.out)


if __name__ == "__main__":
    main()
