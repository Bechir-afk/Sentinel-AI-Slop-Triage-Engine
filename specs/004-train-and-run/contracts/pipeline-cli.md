# Contract: Pipeline CLIs

The offline commands this feature drives, in order. These are the **shipped**
contracts (confirmed against the scripts' argparse); this feature invokes them,
it does not change them. All run under the Python 3.11 venv (R1).

## Step 0 — Environment (helper: `scripts/setup-python311.ps1`)

- **Precondition**: Python 3.11 available on the workstation.
- **Action**: create/activate a 3.11 venv, `pip install -r ml/requirements.txt`.
- **Postcondition**: `python -c "import torch, transformers, datasets"` succeeds;
  `torch.cuda.is_available()` is `True` on the RTX 3050.

## Step 1 — Collect (`ml/collect_prs.py`)

```
python collect_prs.py --repos <owner/repo,owner/repo,...> --out raw_prs.jsonl --per-repo 200
```

| Arg | Required | Default | Meaning |
|-----|----------|---------|---------|
| `--repos` | yes | — | comma-separated `owner/repo` list |
| `--out` | no | `raw_prs.jsonl` | output JSONL |
| `--per-repo` | no | `200` | cap per repo |

- **Env**: `GITHUB_TOKEN` required. **Output**: JSONL of Raw PR records.
- **Failure modes**: rate limiting (paced ~0.5 s/PR); sparse SLOP class (handled
  in Step 2 by synthetic augmentation).

## Step 2 — Build dataset (`ml/build_dataset.py`)

```
python build_dataset.py --raw raw_prs.jsonl --synthetic 800 --out dataset
```

| Arg | Required | Default | Meaning |
|-----|----------|---------|---------|
| `--raw` | yes | — | input JSONL from Step 1 |
| `--synthetic` | no | `800` | synthetic slop count |
| `--out` | no | `dataset` | output dir for parquet splits |
| `--seed` | no | `42` | RNG seed |
| `--feedback-log` | no | — | fold maintainer-disagreement rows as LEGIT (needs `GITHUB_TOKEN`) |

- **Output**: `dataset/{train,val,test}.parquet` (80/10/10).
- **Hard failure**: any cross-split `(title,diff)` collision aborts the build
  (leakage guard, FR-005) — this is a feature, not an error to work around.

## Step 3 — Fine-tune (`ml/train.py`)

```
python train.py --data dataset --out ../model/model --epochs 3 --batch-size 4 --lr 2e-5 --bar 0.85
```

| Arg | Required | Default | Meaning |
|-----|----------|---------|---------|
| `--data` | no | `dataset` | dataset dir from Step 2 |
| `--out` | no | `../model/model` | artifact output dir |
| `--epochs` | no | `3` | training epochs |
| `--batch-size` | no | `8` | **use 4** for ~4 GB VRAM (R2); 2 on OOM |
| `--lr` | no | `2e-5` | learning rate |
| `--bar` | no | `0.85` | precision bar for threshold selection |

- **Output**: `model.safetensors` + tokenizer + `config.json` + `threshold.json`.
- **Invariant**: safetensors only (service refuses pickle `.bin`).

## Step 4 — Evaluate / honesty gate (`ml/evaluate.py`)

```
python evaluate.py --model ../model/model --test dataset/test.parquet --bar 0.85
```

| Arg | Required | Default | Meaning |
|-----|----------|---------|---------|
| `--model` | no | `../model/model` | artifact dir from Step 3 |
| `--test` | no | `dataset/test.parquet` | held-out test split |
| `--bar` | no | `0.85` | precision bar (do NOT lower — FR-010) |

- **Exit code**: `0` iff SLOP precision ≥ 0.85; non-zero blocks deploy (FR-009).
- **This is the gate**: the artifact is untrusted until this exits 0.
