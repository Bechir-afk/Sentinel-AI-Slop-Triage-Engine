# Quickstart: Train the Model & Bring Up the Stack

End-to-end validation path. Run from the repo root on the developer workstation
(Windows 10, RTX 3050). Commands assume PowerShell. See
[contracts/pipeline-cli.md](./contracts/pipeline-cli.md) and
[contracts/service-endpoints.md](./contracts/service-endpoints.md) for full
argument and response detail; this guide is the runnable sequence and the
expected outcomes.

## Prerequisites

- **Python 3.11** available (the default 3.14 has no wheels for the ML deps — R1).
- **Docker Desktop** with Compose.
- A **GitHub token** with read access to the source repos, exported as
  `GITHUB_TOKEN` (never hard-coded — FR-016).
- NVIDIA driver present so torch's CUDA build sees the GPU.

## Step 0 — Python 3.11 environment

```powershell
py -3.11 -m venv .venv-ml
.\.venv-ml\Scripts\Activate.ps1
pip install -r ml\requirements.txt
python -c "import torch; print(torch.cuda.is_available())"   # expect: True
```

**Expected**: all deps install from wheels (no source build); CUDA reports True.

## Step 1 — Collect PRs (LEGIT + SLOP)

```powershell
$env:GITHUB_TOKEN = "<your-token>"
cd ml
python collect_prs.py --repos "owner/repoA,owner/repoB" --out raw_prs.jsonl --per-repo 200
```

**Expected**: `raw_prs.jsonl` with merged PRs (LEGIT) and spam/invalid PRs
(SLOP). A low real-SLOP count is normal (synthetic slop compensates next).

## Step 2 — Build the leakage-checked dataset

```powershell
python build_dataset.py --raw raw_prs.jsonl --synthetic 800 --out dataset
```

**Expected**: `dataset/train.parquet`, `val.parquet`, `test.parquet` (80/10/10).
If the build **aborts on leakage**, that is the guard (FR-005) working — fix the
input, do not bypass it.

## Step 3 — Fine-tune (VRAM-safe)

```powershell
python train.py --data dataset --out ..\model\model --epochs 3 --batch-size 4 --lr 2e-5 --bar 0.85
```

**Expected**: `..\model\model\` gains `model.safetensors`, `config.json`,
tokenizer files, and `threshold.json`. If CUDA OOMs, drop to `--batch-size 2`
(R2). Do not switch to `.bin` — the service refuses pickle weights.

## Step 4 — Evaluate against the 0.85 precision gate

```powershell
python evaluate.py --model ..\model\model --test dataset\test.parquet --bar 0.85
echo "exit code: $LASTEXITCODE"
```

**Expected**: reported SLOP precision ≥ 0.85 and **exit code 0**. A non-zero
exit means the artifact is untrusted — iterate on data/training, never lower
`--bar` (FR-010, constitution III). This is the acceptance gate (SC-002).

## Step 5 — Configure and bring up the stack

```powershell
cd ..
Copy-Item .env.example .env
# edit .env: set GITHUB_WEBHOOK_SECRET and GITHUB_TOKEN.
# leave CONFIDENCE_THRESHOLD unset so the gateway sources the artifact threshold.
docker compose up --build
```

**Expected**: `sentinel-model` becomes healthy (its artifact mount
`./model/model:/app/model:ro` is populated by Step 3); `gateway` starts and
reaches healthy. Neither crash-loops.

## Step 6 — Verify readiness

```powershell
# model health (from a shell on the compose network, or temporarily map the port)
# gateway health + stats (public):
curl.exe http://localhost:8080/healthz          # expect HTTP 200
curl.exe http://localhost:8080/stats            # expect 200 + JSON counters + version
```

**Expected** (SC-005/SC-006): model `/healthz` → `status:"ok"` with the
artifact's `threshold`; gateway `/healthz` 200; gateway `/stats` 200; with no
`CONFIDENCE_THRESHOLD` override, the gateway's effective threshold equals the
model's advertised value.

## Step 7 (optional) — Observe-only before acting

```powershell
# in .env: SHADOW_MODE=true, then restart:
docker compose up -d
```

**Expected** (SC-007): processing a PR event logs a verdict with its correlation
id and writes **nothing** to GitHub. Unset `SHADOW_MODE` (or set false) to let
the gateway apply the `needs-human-review` label and comment.

## Failure fast-path (edge cases)

| Symptom | Meaning | Action |
|---------|---------|--------|
| model service exits with one "artifact unusable" line | artifact absent/incomplete | run Steps 3–4; do not restart-loop (SC-008) |
| `pip install` builds from source and fails | wrong Python | use 3.11 (Step 0) |
| CUDA out of memory | batch too large | `--batch-size 2` or CPU |
| build aborts on leakage | cross-split duplicate | fix input data; guard is correct |
| evaluate exits non-zero | precision < 0.85 | improve data/training; never lower `--bar` |
| compose refuses to start (`:?set in .env`) | missing secret | set `GITHUB_WEBHOOK_SECRET` / `GITHUB_TOKEN` |
