"""Task 3: turn raw PRs + synthetic slop into a labeled, split dataset.

Steps:
  1. Map collected rows to a provisional label (merged -> LEGIT, spam -> SLOP).
     Real rows are provisional; a human reviews before trusting (see caveat below).
  2. Stratified-split the REAL rows 80/10/10, then generate synthetic SLOP diffs
     PER SPLIT with disjoint seeds and randomized titles/filenames, so no
     synthetic example can leak across splits (a fixed title + templated diff
     would otherwise collide train/val/test and inflate metrics — FR-009, R5).
  3. Post-build assertion: no (title, diff) hash appears in more than one split.
     A collision fails the build.

Labeling caveat (PROJECT_MAP): "slop" is subjective. Synthetic examples are
cleanly labeled by construction; real ones need human review — the manual
bottleneck. --review-file flags real rows for a human to correct.

Not in serving path. Run offline.

Usage:
    python build_dataset.py --raw raw_prs.jsonl --synthetic 800 --out dataset
"""

import argparse
import hashlib
import json
import logging
import os
import random

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("sentinel.dataset")

LEGIT, SLOP = 0, 1

_FUNCS = ["handler", "process", "compute", "load", "parse", "build", "run", "fetch"]
_FILES = ["util", "svc", "mod", "gen", "core", "helpers", "api", "store"]
# Varied synthetic titles so a fixed "Update code" title can't collide across
# splits (the old behavior). Combined with a per-split RNG, each split's
# synthetic rows are drawn from an independent stream.
_TITLES = [
    "Update code", "Minor tweaks", "Refactor", "Cleanup", "Fix", "Improve module",
    "Small changes", "Housekeeping", "Polish", "Adjust", "Tidy up", "WIP",
]


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


def _comment_only_diff(rng: random.Random) -> str:
    fn = rng.choice(_FILES)
    n = rng.randint(3, 12)
    lines = [f"diff --git a/{fn}.py b/{fn}.py", f"--- a/{fn}.py", f"+++ b/{fn}.py", "@@ -1,2 +1,%d @@" % (2 + n)]
    lines += [f"+# {rng.choice(['TODO', 'NOTE', 'FIXME'])}: auto-generated comment {rng.randint(0, 1_000_000)}" for _ in range(n)]
    return "\n".join(lines)


def _rename_only_diff(rng: random.Random) -> str:
    fn = rng.choice(_FILES)
    old, new = rng.choice(_FUNCS), rng.choice(_FUNCS) + f"_v{rng.randint(2, 99)}"
    return "\n".join(
        [
            f"diff --git a/{fn}.py b/{fn}.py",
            f"--- a/{fn}.py",
            f"+++ b/{fn}.py",
            "@@ -1,3 +1,3 @@",
            f"-def {old}(x):",
            f"+def {new}(x):",
            "     return x",
        ]
    )


def _whitespace_diff(rng: random.Random) -> str:
    fn = rng.choice(_FILES)
    n = rng.randint(4, 15)
    lines = [f"diff --git a/{fn}.py b/{fn}.py", f"--- a/{fn}.py", f"+++ b/{fn}.py", "@@ -1,%d +1,%d @@" % (n, n)]
    lines += ["+    " + " " * rng.randint(0, 4) for _ in range(n)]
    return "\n".join(lines)


def _boilerplate_diff(rng: random.Random) -> str:
    fn = rng.choice(_FUNCS)
    tag = rng.randint(0, 1_000_000)
    return "\n".join(
        [
            "diff --git a/gen.py b/gen.py",
            "--- a/gen.py",
            "+++ b/gen.py",
            "@@ -1,1 +1,6 @@",
            f"+def {fn}_{tag}():",
            '+    """Auto-generated stub."""',
            "+    pass",
            "+",
            f"+def {fn}_{tag}_impl():",
            "+    raise NotImplementedError",
        ]
    )


_GENERATORS = [_comment_only_diff, _rename_only_diff, _whitespace_diff, _boilerplate_diff]


def _synthetic(n: int, rng: random.Random):
    """Generate n synthetic SLOP rows from the given RNG (per-split stream)."""
    rows = []
    for _ in range(n):
        gen = rng.choice(_GENERATORS)
        rows.append({"title": rng.choice(_TITLES), "diff": gen(rng), "label": SLOP})
    return rows


def _row_hash(row: dict) -> str:
    return hashlib.sha256((row["title"] + "\x00" + row["diff"]).encode("utf-8")).hexdigest()


def assert_no_leakage(splits: dict) -> None:
    """Fail the build if any (title, diff) hash appears in more than one split.

    splits: {name: list[row]}. Raises ValueError naming the offending hash on
    the first cross-split collision (FR-009). Duplicates WITHIN one split are
    allowed (they cannot leak); only cross-split membership is a leak.
    """
    seen: dict[str, str] = {}
    for name, rows in splits.items():
        for h in {_row_hash(r) for r in rows}:  # per-split unique
            if h in seen and seen[h] != name:
                raise ValueError(
                    f"leakage: identical (title, diff) in splits {seen[h]!r} and {name!r} "
                    f"(hash {h[:12]}…)"
                )
            seen[h] = name


def _stratified_split_real(rows, seed):
    """80/10/10 stratified split of real rows using stdlib only (no sklearn dep
    for the split itself, so the leakage invariant is testable offline)."""
    rng = random.Random(seed)
    by_label: dict[int, list] = {}
    for r in rows:
        by_label.setdefault(r["label"], []).append(r)
    train, val, test = [], [], []
    for label, group in by_label.items():
        g = group[:]
        rng.shuffle(g)
        n = len(g)
        n_test = n // 10
        n_val = n // 10
        test += g[:n_test]
        val += g[n_test : n_test + n_val]
        train += g[n_test + n_val :]
    return train, val, test


def build(raw_rows, synthetic_total, seed):
    """Build the three splits: split real first, then add per-split synthetic
    slop from disjoint RNG streams. Returns {name: rows} and asserts no leak."""
    train, val, test = _stratified_split_real(raw_rows, seed)

    # Divide synthetic budget across splits ~80/10/10, each from its own seed so
    # the streams are disjoint and reproducible.
    n_test = synthetic_total // 10
    n_val = synthetic_total // 10
    n_train = synthetic_total - n_test - n_val
    train += _synthetic(n_train, random.Random(seed + 1))
    val += _synthetic(n_val, random.Random(seed + 2))
    test += _synthetic(n_test, random.Random(seed + 3))

    splits = {"train": train, "val": val, "test": test}
    assert_no_leakage(splits)  # fails the build on any cross-split collision
    return splits


def _write(splits, out):
    import pandas as pd  # deferred: the leakage logic above is pandas-free

    os.makedirs(out, exist_ok=True)
    for name, rows in splits.items():
        df = pd.DataFrame(rows).sample(frac=1, random_state=42).reset_index(drop=True)
        path = os.path.join(out, f"{name}.parquet")
        df.to_parquet(path)
        logger.info("%s: %d rows (legit=%d slop=%d) -> %s", name, len(df),
                    int((df.label == LEGIT).sum()), int((df.label == SLOP).sum()), path)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--raw", required=True)
    ap.add_argument("--synthetic", type=int, default=800)
    ap.add_argument("--out", default="dataset")
    ap.add_argument("--seed", type=int, default=42)
    args = ap.parse_args()

    raw_rows = _load_raw(args.raw)
    splits = build(raw_rows, args.synthetic, args.seed)
    total = sum(len(v) for v in splits.values())
    logger.info("built %d rows across train/val/test; leakage check passed", total)
    _write(splits, args.out)


if __name__ == "__main__":
    main()
