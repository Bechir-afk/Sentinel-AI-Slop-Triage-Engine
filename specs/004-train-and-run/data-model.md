# Phase 1 Data Model: Train Model & Bring Up Stack

This feature is operational, so the "data model" is the set of files and records
that flow through the pipeline and configure the running stack. Fields reflect
the shipped code; nothing here introduces new schema.

## Raw PR record

One collected pull request. Written by `collect_prs.py` as one JSON object per
line in `raw_prs.jsonl`.

| Field | Meaning | Notes |
|-------|---------|-------|
| `title` | PR title | free text |
| `diff` | unified diff body | never logged downstream (FR-016) |
| `source` | `"merged"` → LEGIT, or spam/invalid → SLOP | drives the label |
| `repo` | `owner/name` | provenance for auditing |
| `number` | PR number | provenance for auditing |

**Validation**: ambiguous closed PRs (no spam/invalid label, not merged) are
skipped, not guessed. Collection paces ~0.5 s/PR (FR-003).

## Dataset split

Written by `build_dataset.py` to `ml/dataset/{train,val,test}.parquet` in an
80/10/10 ratio.

| Property | Rule |
|----------|------|
| Real rows | stratified 80/10/10 by class |
| Synthetic slop | added per split from disjoint RNG streams (seed+1/+2/+3) |
| Label | `LEGIT=0`, `SLOP=1` |
| Leakage guard | build **aborts** if any `(title,diff)` hash appears in >1 split (FR-005) |
| Optional feedback | `--feedback-log` folds maintainer-disagreement rows as real LEGIT (needs `GITHUB_TOKEN`) |

**State transition**: `raw_prs.jsonl` → (build + leakage assert) → three
parquet files. A leakage collision is a terminal failure of the build, not a
warning.

## Model artifact

The directory the model service loads (`model/model/`), produced by `train.py`
and mounted read-only into the container.

| File | Meaning |
|------|---------|
| `config.json` | model config (required by `_verify_artifact`) |
| `model.safetensors` | weights — safetensors only, never `.bin` (RCE guard) |
| tokenizer files | `tokenizer.json` / `vocab.json` / `tokenizer_config.json` |
| `threshold.json` | see Threshold record below |

**Validation** (enforced by the *shipped* service at startup, not by this
feature): missing directory / `config.json` / weights / tokenizer → one
actionable log line + `exit(1)`, no crash-loop. The **directory name** is the
operator-facing `artifact` version reported on `/healthz`.

## Threshold record

`threshold.json`, written beside the weights by `train.py` via
`thresholds.select_threshold`.

| Field | Meaning |
|-------|---------|
| `threshold` | validation-selected cutoff — lowest whose val SLOP precision clears the bar |
| `val_precision` | precision at that cutoff on the val split |
| `bar` | the precision bar it was selected against (0.85) |

Fallback: if absent, the service uses a conservative default (0.95) and logs
that it did so (edge case: older artifact).

## Evaluation report

Produced by `evaluate.py` on the held-out test set.

| Output | Meaning |
|--------|---------|
| SLOP precision | measured on `test.parquet` at the selected threshold |
| exit code | `0` iff precision ≥ `--bar` (0.85); non-zero otherwise (FR-009) |

**Gate**: exit 0 is a hard precondition for deploying the artifact
(constitution III + Quality Gates). Non-zero → iterate on data/training, do not
lower `--bar` (FR-010).

## Runtime environment file

`.env` (from `.env.example`) parameterizes the running stack. Consumed by
`docker-compose.yml` and `internal/config/config.go`.

| Variable | Required | Effect |
|----------|----------|--------|
| `GITHUB_WEBHOOK_SECRET` | yes | HMAC verification of webhook deliveries |
| `GITHUB_TOKEN` | yes | GitHub API auth for label/comment/check |
| `MODEL_URL` | yes (compose sets it) | gateway → model service address |
| `CONFIDENCE_THRESHOLD` | no | override; **unset** → gateway sources artifact threshold |
| `SLOP_LABEL` | no | default `needs-human-review` |
| `SHADOW_MODE` | no | `true` → log verdict, write nothing to GitHub (FR-014) |
| `MODEL_THREADS` | no | torch intra-op thread cap (compose default 2) |

**Secrets rule**: the token and webhook secret are never logged or persisted by
any step in this feature (FR-016).
