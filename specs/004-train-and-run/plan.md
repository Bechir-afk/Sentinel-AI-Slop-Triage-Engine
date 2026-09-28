# Implementation Plan: Train Model & Bring Up Stack

**Branch**: `004-train-and-run` | **Date**: 2026-09-28 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/004-train-and-run/spec.md`

## Summary

Produce a trusted model artifact and bring the two-service stack to a running
state on a developer workstation, without changing any serving-path code. The
serving path (gateway `cmd/`+`internal/`, model service `model/`, and the ML
pipeline `ml/*.py`) already exists and works; the deliverable is the *executed*
operational path — establish a Python 3.11 environment, collect PRs, build a
leakage-checked dataset, fine-tune on the ~4 GB GPU with a reduced batch size,
clear the 0.85 SLOP-precision gate, then `docker compose up` and verify health —
plus the helper tooling and documentation that make that path repeatable. All
new code lives outside the serving path (`scripts/`, `ml/tests/`, docs).

## Technical Context

**Language/Version**: Python **3.11** for the offline pipeline (default 3.14 has
no wheels for the pinned ML deps); Go 1.26 and Python 3.11 serving code are used
as-shipped, not modified. Helper scripts target PowerShell 7 + POSIX `sh`.

**Primary Dependencies**: Offline only — `torch==2.5.1`, `transformers==4.48.0`,
`datasets==3.2.0`, `pandas==2.2.3`, `pyarrow==18.1.0`, `scikit-learn==1.6.0`,
`requests==2.32.3` (all from `ml/requirements.txt`). Docker + Compose to run the
stack. No new Go dependencies (constitution V); no new serving-path Python deps.

**Storage**: Files only — `raw_prs.jsonl`, `ml/dataset/{train,val,test}.parquet`,
and the artifact directory `model/model/` (`config.json`, `model.safetensors`,
tokenizer files, `threshold.json`). No database.

**Testing**: `ml/tests/` (stdlib `unittest`/pytest-style, already present:
`test_build_dataset.py`, `test_feedback_roundtrip.py`, `test_thresholds.py`);
`ml/evaluate.py` is the precision honesty gate; `docker compose` health probes
and manual `/healthz` + `/stats` checks for bring-up. No new test framework.

**Target Platform**: Windows 10 developer workstation (RTX 3050 Laptop, ~4 GB
VRAM) for training; Linux containers via Docker Compose for the running stack.

**Project Type**: Operational/procedural feature over an existing web-service +
ML-pipeline monorepo. No new runtime component.

**Performance Goals**: Fine-tuning fits in ~4 GB VRAM at batch size 4 (fallback
2, or CPU); stack reaches healthy within the compose `start_period` (model 30s,
gateway 5s); single 512-token forward pass sub-second on CPU.

**Constraints**: ZERO serving-path change (FR-015) — no edits under `cmd/`,
`internal/`, `model/`, or `ml/*.py`; `ml/tests/` and new `scripts/`+docs are the
only writable areas. Never log/persist the diff body, GitHub token, or webhook
secret (FR-016). The 0.85 precision bar MUST NOT be lowered (FR-010).

**Scale/Scope**: One developer, a handful of source repos, a few thousand PRs +
synthetic slop, one artifact, two containers. Single-node; no HA.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Impact of this feature | Status |
|-----------|------------------------|--------|
| I. Never Auto-Close | No serving-path change; bring-up cannot add close behavior. Story 3 verifies observe-only writes nothing. | PASS |
| II. Fail-Open | Not modified. Bring-up verifies the gateway comes up even when the model is absent (`service_started`). | PASS |
| III. Precision Over Recall (0.85, NON-NEGOTIABLE) | Central acceptance bar. `ml/evaluate.py` must exit 0 before the artifact is deployed; leakage guard (FR-005) protects the gate; bar not lowered (FR-010). | PASS |
| IV. Classify Value Not Provenance | Uses the shipped pipeline as-is; no provenance signal introduced. | PASS |
| V. Zero Third-Party Go Deps | No Go code touched; helper scripts are shell/PowerShell + Python stdlib. | PASS |
| VI. Self-Hosted Model | Training/collection are offline (this feature); serving stays local HTTP. No external AI API. | PASS |
| VII. Trust Boundary Verification | Not modified. `.env` carries the webhook secret; no verification logic changes. | PASS |
| VIII. Docs Match Shipped Code | Deliverable includes docs describing the actual shipped path; must not add stale references. | PASS |

**Quality Gates**: `go build/vet/test ./...` unaffected (no Go change);
`ml/evaluate.py` MUST exit 0 before deploy (this feature's core gate); model
service MUST NOT crash-loop on absent artifact (verified in Story 2 / edge
cases). No violations — Complexity Tracking left empty.

## Project Structure

### Documentation (this feature)

```text
specs/004-train-and-run/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output (command + endpoint contracts)
│   ├── pipeline-cli.md
│   └── service-endpoints.md
├── checklists/
│   └── requirements.md  # from /speckit-specify
└── tasks.md             # /speckit-tasks output (NOT created here)
```

### Source Code (repository root)

Only non-serving-path areas are written to. Existing serving-path files are
inputs, shown for orientation and marked read-only here.

```text
scripts/                          # NEW — helper tooling (this feature)
├── setup-python311.ps1           # create/verify the Python 3.11 venv, install ml/requirements.txt
├── train-and-eval.ps1            # run collect→build→train→evaluate with VRAM-safe defaults
└── verify-stack.ps1              # curl /healthz + /stats, assert threshold parity

ml/                               # READ-ONLY serving/pipeline code (unchanged)
├── collect_prs.py                #   step 1
├── build_dataset.py              #   step 2
├── train.py                      #   step 3  (--batch-size 4 for 4GB VRAM)
├── evaluate.py                   #   step 4  (0.85 gate)
├── thresholds.py
├── encoding.py
├── requirements.txt
└── tests/                        # WRITABLE — offline checks may be added here
    ├── test_build_dataset.py
    ├── test_feedback_roundtrip.py
    └── test_thresholds.py

model/                            # READ-ONLY (unchanged)
├── app.py  inference.py  encoding.py  Dockerfile  requirements.txt
└── model/                        # ARTIFACT OUTPUT — produced by step 3, mounted :ro
    ├── config.json  model.safetensors  tokenizer files
    └── threshold.json

cmd/ internal/                    # READ-ONLY Go gateway (unchanged)
docker-compose.yml  .env.example  # READ-ONLY config (operator fills .env)
docs/                             # WRITABLE — bring-up/training runbook additions
```

**Structure Decision**: An operational feature, so the code surface is a new
top-level `scripts/` directory plus documentation, with `ml/tests/` available
for an offline check. The serving path is used as an input and never edited,
satisfying FR-015 and constitution principles I–VIII. Helper scripts are thin
wrappers over the existing CLIs — they encode the VRAM-safe batch size, the
correct ordering, and the environment guard, so a developer does not have to
remember them, but they add no new logic to the pipeline itself.

## Complexity Tracking

> No constitution violations. Section intentionally empty.
