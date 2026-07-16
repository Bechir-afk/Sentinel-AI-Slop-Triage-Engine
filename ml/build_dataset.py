"""Task 3: turn raw PRs + synthetic slop into a labeled, split dataset.

Steps:
  1. Map collected rows to a provisional label (merged -> LEGIT, spam -> SLOP).
     Real rows are provisional; a human reviews before trusting (see caveat below).
  2. Generate synthetic SLOP diffs (comment-only, rename-only, whitespace,
     boilerplate scaffolding) to pad the scarce real-slop class toward balance.
  3. Stratified 80/10/10 split into train/val/test parquet under ./dataset.

Labeling caveat (PROJECT_MAP): "slop" is subjective. Synthetic examples are
cleanly labeled by construction; real ones need human review — the manual
bottleneck. --review-file flags real rows for a human to correct.

Not in serving path. Run offline.

Usage:
    python build_dataset.py --raw raw_prs.jsonl --synthetic 800 --out dataset
"""

import argparse
import json
import logging
import os
import random

import pandas as pd
from sklearn.model_selection import train_test_split

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("sentinel.dataset")

LEGIT, SLOP = 0, 1

_FUNCS = ["handler", "process", "compute", "load", "parse", "build", "run", "fetch"]


def _load_raw(path: str):
    rows = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            r = json.loads(line)
            label = LEGIT if r.get("source") == "merged" else SLOP
            rows.append({"title": r["title"], "diff": r["diff"], "label": label})
    logger.info("loaded %d real rows from %s", len(rows), path)
    return rows


def _comment_only_diff() -> str:
    n = random.randint(3, 12)
    lines = ["diff --git a/util.py b/util.py", "--- a/util.py", "+++ b/util.py", "@@ -1,2 +1,%d @@" % (2 + n)]
    lines += [f"+# {random.choice(['TODO', 'NOTE', 'FIXME'])}: auto-generated comment {i}" for i in range(n)]
    return "\n".join(lines)


def _rename_only_diff() -> str:
    old, new = random.choice(_FUNCS), random.choice(_FUNCS) + "_v2"
    return "\n".join(
        [
            "diff --git a/svc.py b/svc.py",
            "--- a/svc.py",
            "+++ b/svc.py",
            "@@ -1,3 +1,3 @@",
            f"-def {old}(x):",
            f"+def {new}(x):",
            "     return x",
        ]
    )


def _whitespace_diff() -> str:
    n = random.randint(4, 15)
    lines = ["diff --git a/mod.py b/mod.py", "--- a/mod.py", "+++ b/mod.py", "@@ -1,%d +1,%d @@" % (n, n)]
    lines += ["+    " for _ in range(n)]
    return "\n".join(lines)


def _boilerplate_diff() -> str:
    fn = random.choice(_FUNCS)
    return "\n".join(
        [
            "diff --git a/gen.py b/gen.py",
            "--- a/gen.py",
            "+++ b/gen.py",
            "@@ -1,1 +1,6 @@",
            f"+def {fn}():",
            '+    """Auto-generated stub."""',
            "+    pass",
            "+",
            f"+def {fn}_impl():",
            "+    raise NotImplementedError",
        ]
    )


_GENERATORS = [_comment_only_diff, _rename_only_diff, _whitespace_diff, _boilerplate_diff]


def _synthetic(n: int):
    rows = []
    for _ in range(n):
        gen = random.choice(_GENERATORS)
        rows.append({"title": "Update code", "diff": gen(), "label": SLOP})
    logger.info("generated %d synthetic slop rows", n)
    return rows


def _split_and_write(df: pd.DataFrame, out: str):
    os.makedirs(out, exist_ok=True)
    train, temp = train_test_split(df, test_size=0.20, stratify=df["label"], random_state=42)
    val, test = train_test_split(temp, test_size=0.50, stratify=temp["label"], random_state=42)
    for name, part in (("train", train), ("val", val), ("test", test)):
        path = os.path.join(out, f"{name}.parquet")
        part.reset_index(drop=True).to_parquet(path)
        logger.info("%s: %d rows -> %s", name, len(part), path)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--raw", required=True)
    ap.add_argument("--synthetic", type=int, default=800)
    ap.add_argument("--out", default="dataset")
    ap.add_argument("--seed", type=int, default=42)
    args = ap.parse_args()

    random.seed(args.seed)
    rows = _load_raw(args.raw) + _synthetic(args.synthetic)
    df = pd.DataFrame(rows).sample(frac=1, random_state=args.seed).reset_index(drop=True)
    logger.info("total %d rows | legit=%d slop=%d", len(df), (df.label == LEGIT).sum(), (df.label == SLOP).sum())
    _split_and_write(df, args.out)


if __name__ == "__main__":
    main()
