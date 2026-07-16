"""Task 4: fine-tune CodeBERT + classification head, save the artifact.

Loads the parquet splits from build_dataset.py, encodes '<title>\\n[SEP]\\n<diff>'
to 512 tokens, and fine-tunes microsoft/codebert-base with a 2-class head
(LEGIT=0, SLOP=1). Saves weights + tokenizer to --out so the model service can
AutoModelForSequenceClassification.from_pretrained(MODEL_PATH).

One-time, offline. Fits a single consumer GPU or CPU (slow). Not in serving path.

Usage:
    python train.py --data dataset --out ../model/model --epochs 3
"""

import argparse
import logging
import os

import pandas as pd
from datasets import Dataset
from transformers import (
    AutoModelForSequenceClassification,
    AutoTokenizer,
    Trainer,
    TrainingArguments,
)

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("sentinel.train")

BASE_MODEL = "microsoft/codebert-base"
MAX_TOKENS = 512


def _to_dataset(path: str, tokenizer) -> Dataset:
    df = pd.read_parquet(path)
    ds = Dataset.from_pandas(df, preserve_index=False)

    def encode(batch):
        texts = [f"{t}\n[SEP]\n{d}" for t, d in zip(batch["title"], batch["diff"])]
        out = tokenizer(texts, truncation=True, max_length=MAX_TOKENS, padding="max_length")
        out["labels"] = batch["label"]
        return out

    return ds.map(encode, batched=True, remove_columns=ds.column_names)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--data", default="dataset")
    ap.add_argument("--out", default="../model/model")
    ap.add_argument("--epochs", type=float, default=3)
    ap.add_argument("--batch-size", type=int, default=8)
    ap.add_argument("--lr", type=float, default=2e-5)
    args = ap.parse_args()

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
        logging_steps=50,
    )

    trainer = Trainer(model=model, args=targs, train_dataset=train_ds, eval_dataset=val_ds)
    trainer.train()

    os.makedirs(args.out, exist_ok=True)
    trainer.save_model(args.out)
    tokenizer.save_pretrained(args.out)
    logger.info("saved fine-tuned artifact to %s", args.out)


if __name__ == "__main__":
    main()
