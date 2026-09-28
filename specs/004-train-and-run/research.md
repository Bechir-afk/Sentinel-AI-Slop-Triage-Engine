# Phase 0 Research: Train Model & Bring Up Stack

All items below were resolved from the shipped code and repo files (no open
NEEDS CLARIFICATION remain). Each entry: Decision / Rationale / Alternatives.

## R1. Python runtime for the offline pipeline

- **Decision**: Use a dedicated **Python 3.11** virtual environment for `ml/`;
  do not touch the developer's default 3.14 interpreter.
- **Rationale**: `ml/requirements.txt` pins `torch==2.5.1`, `transformers==4.48.0`,
  `datasets==3.2.0`. These publish no cp314 wheels; installing under 3.14 forces
  a source build that fails. 3.11 has prebuilt wheels for all pins. The serving
  containers already pin `python:3.11-slim`, so 3.11 matches production.
- **Alternatives**: (a) 3.12/3.13 — some pins lack wheels, riskier; (b) upgrade
  the pins to 3.14-compatible versions — rejected, that is a serving-path change
  (FR-015) and would drift from the digest-pinned container base.

## R2. GPU vs CPU and batch size

- **Decision**: Train on the RTX 3050 (CUDA) with **`--batch-size 4`**; fall back
  to `2` on out-of-memory, and CPU as a last resort.
- **Rationale**: `train.py` defaults to `--batch-size 8`. CodeBERT-base at 512
  tokens with batch 8 exceeds ~4 GB VRAM and OOMs. Batch 4 halves activation
  memory and fits; the Windows torch wheel is the CUDA build so the GPU is used
  automatically when present. `train.py` sets `SEED=42`, so reducing batch size
  changes optimization dynamics slightly but keeps the run reproducible.
- **Alternatives**: gradient accumulation to emulate batch 8 — more config, not
  needed to clear the gate; CPU-only — correct but much slower; multi-GPU — n/a.

## R3. Data collection strategy (LEGIT vs SLOP classes)

- **Decision**: Run `collect_prs.py --repos <list> --out raw_prs.jsonl
  --per-repo 200` with a `GITHUB_TOKEN`. Merged PRs → LEGIT; closed PRs labeled
  spam/invalid → SLOP; ambiguous closed PRs skipped.
- **Rationale**: This is the shipped collector's contract (confirmed: args
  `--repos` required, `--out`, `--per-repo`). It paces at ~0.5 s/PR to respect
  rate limits (FR-003). Real slop is typically sparse, which R4 compensates for.
- **Alternatives**: hand-labeling — out of scope and slow; public slop datasets —
  none trusted/leakage-free for this task.

## R4. Dataset build, split, and leakage guard

- **Decision**: `build_dataset.py --raw raw_prs.jsonl --synthetic 800 --out
  ml/dataset` produces an 80/10/10 split with synthetic slop from disjoint RNG
  streams per split, and aborts on any cross-split (title,diff) collision.
- **Rationale**: Confirmed args (`--raw` required, `--synthetic` default 800,
  `--out` default `dataset`, `--seed` 42, optional `--feedback-log`). The
  `assert_no_leakage` guard (FR-005) is what makes the precision gate honest
  (constitution III forbids shortcut leakage). Synthetic slop is drawn from
  disjoint streams (seed+1/+2/+3) so templates never span splits.
- **Alternatives**: 90/5/5 — smaller val/test weakens threshold selection and
  the gate; no synthetic — real slop too sparse to train on.

## R5. Fine-tune and artifact format

- **Decision**: `train.py --data ml/dataset --out model/model --epochs 3
  --batch-size 4 --lr 2e-5 --bar 0.85` fine-tunes `microsoft/codebert-base`
  (2-class head), writes `model.safetensors` (never legacy `.bin`), then selects
  the validation threshold and writes `threshold.json`.
- **Rationale**: Confirmed args. `save_safetensors=True` is mandatory — the
  model service loads `use_safetensors=True` and refuses pickle (RCE guard,
  FR-011 of the serving spec). Threshold is chosen on the val split by
  `thresholds.select_threshold` (lowest cutoff clearing the bar).
- **Alternatives**: train from scratch — rejected (fine-tuning a pre-trained
  base is the design, VI); `.bin` weights — refused at load time by the service.

## R6. Precision honesty gate

- **Decision**: `evaluate.py --model model/model --test ml/dataset/test.parquet
  --bar 0.85` must exit 0 before the artifact is deployed. A failure is resolved
  by improving data/training, never by lowering `--bar`.
- **Rationale**: Constitution III (NON-NEGOTIABLE) and Quality Gates require
  `ml/evaluate.py` to exit 0 before deploy. FR-009/FR-010 encode this. Confirmed
  args (`--model`, `--test`, `--bar` default = precision bar).
- **Alternatives**: trust val precision only — rejected, the held-out test set is
  the honest measure; lower the bar — explicitly forbidden.

## R7. Stack bring-up and configuration

- **Decision**: Copy `.env.example` → `.env`, set `GITHUB_WEBHOOK_SECRET` and
  `GITHUB_TOKEN`, leave `CONFIDENCE_THRESHOLD` unset so the gateway sources the
  artifact threshold, then `docker compose up --build`.
- **Rationale**: `docker-compose.yml` requires both secrets (`:?set in .env`)
  and mounts `./model/model:/app/model:ro`; the gateway `depends_on`
  `sentinel-model` with `condition: service_started` so it fails open if the
  model is down. Unset threshold → gateway reads `/healthz` threshold (confirmed
  in `main.go`: warns and uses default only if healthz is unavailable).
- **Alternatives**: publish the model port — rejected, it is internal-only by
  design (VI); set `CONFIDENCE_THRESHOLD` — only to override the artifact.

## R8. Readiness verification

- **Decision**: After `up`, verify model `GET /healthz` → `{status:"ok",
  threshold, artifact, version}`, gateway `GET /healthz` and `GET /stats`
  respond 200, and the gateway's effective threshold equals the artifact's.
- **Rationale**: Endpoints confirmed in `model/app.py` and `cmd/sentinel/main.go`
  (`/healthz`, `/stats` routes present; `/stats` returns counters + version).
  SC-005/SC-006 require this parity check.
- **Alternatives**: rely on compose healthcheck alone — it proves liveness but
  not threshold parity, so an explicit check is added.

## R9. Observe-only (shadow) mode

- **Decision**: Set `SHADOW_MODE=true` in `.env` to log verdicts without writing
  to GitHub; unset (or false) to enable label+comment writes.
- **Rationale**: Confirmed in `internal/config/config.go`: `SHADOW_MODE` env is
  parsed as a bool → `ShadowMode` ("run pipeline, log verdict, write nothing").
  This satisfies FR-014/SC-007 without any code change.
- **Alternatives**: a dry-run flag on the collector — wrong layer; this is a
  serving-time observability toggle that already exists.

## R10. Helper tooling boundary (FR-015)

- **Decision**: New code goes only in `scripts/` (PowerShell wrappers), `docs/`
  (runbook), and optionally `ml/tests/` (offline check). No file under `cmd/`,
  `internal/`, `model/`, or `ml/*.py` is edited.
- **Rationale**: FR-015 + constitution (I–VIII, esp. V zero Go deps, VIII docs
  match code). Wrappers only encode ordering, the VRAM-safe batch size, and the
  environment guard — no new pipeline logic.
- **Alternatives**: a Makefile — Windows-first workstation, PowerShell is the
  native shell; a Python orchestrator — adds an import surface for no benefit.
