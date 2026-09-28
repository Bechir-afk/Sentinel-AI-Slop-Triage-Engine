---

description: "Task list for Train Model & Bring Up Stack"
---

# Tasks: Train Model & Bring Up Stack

**Input**: Design documents from `/specs/004-train-and-run/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/, quickstart.md

**Tests**: This is an operational/procedural feature. The "tests" are the shipped
gates — `ml/evaluate.py` (0.85 precision) and the compose healthchecks — plus the
existing `ml/tests/` offline checks. No new test framework is introduced.

**Organization**: Tasks are grouped by user story (P1→P3) so each is independently
executable and verifiable.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files/artifacts, no dependency on incomplete tasks)
- **[Story]**: US1, US2, US3 — maps to the user stories in [spec.md](./spec.md)
- Exact paths/commands included in each description

## ⚠️ Scope guardrail (applies to EVERY task)

ZERO serving-path change (FR-015): do **not** edit any file under `cmd/`,
`internal/`, `model/`, or `ml/*.py`. Writable areas are `scripts/`, `docs/`,
`ml/tests/`, and the produced artifact/dataset outputs. Never log or persist the
diff body, `GITHUB_TOKEN`, or `GITHUB_WEBHOOK_SECRET` (FR-016). Never lower the
0.85 precision bar (FR-010).

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Establish the compatible Python environment and helper-tooling home.

- [ ] T001 Create `scripts/` directory at repo root for helper tooling (no serving-path files)
- [ ] T002 Create Python 3.11 virtual environment `.venv-ml` at repo root (`py -3.11 -m venv .venv-ml`); confirm the default 3.14 interpreter is untouched — see [research.md](./research.md) R1
- [ ] T003 Install pinned ML deps into the venv from [ml/requirements.txt](../../ml/requirements.txt) (`pip install -r ml\requirements.txt`); verify no source-build fallback occurs
- [ ] T004 [P] Verify GPU availability in the venv: `python -c "import torch; print(torch.cuda.is_available())"` returns `True` (RTX 3050 CUDA build) — [research.md](./research.md) R2

**Checkpoint**: A working 3.11 venv with all ML deps and CUDA visible.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The environment guard and secrets scaffolding every story depends on.

**⚠️ CRITICAL**: No story work can begin until this phase is complete.

- [ ] T005 Author `scripts/setup-python311.ps1` that creates/activates `.venv-ml`, installs `ml/requirements.txt`, and asserts `torch.cuda.is_available()` — thin wrapper only, no pipeline logic ([research.md](./research.md) R10)
- [ ] T006 [P] Confirm `.env.example` covers all required/optional vars used by bring-up (`GITHUB_WEBHOOK_SECRET`, `GITHUB_TOKEN`, `MODEL_URL`, `CONFIDENCE_THRESHOLD`, `SLOP_LABEL`, `SHADOW_MODE`, `MODEL_THREADS`) per [data-model.md](./data-model.md) "Runtime environment file"; if a var is missing, note it in docs (do NOT edit serving-path config)
- [ ] T007 Export `GITHUB_TOKEN` into the shell session for collection steps; verify it is never echoed to a file or log (FR-016)

**Checkpoint**: Environment reproducible via script; secrets sourced from env only.

---

## Phase 3: User Story 1 — Produce a trusted model artifact (Priority: P1) 🎯 MVP

**Goal**: Run steps 0–4 to produce `model/model/` with `model.safetensors` +
`threshold.json` and clear the 0.85 SLOP-precision gate.

**Independent Test**: `model/model/` contains config + safetensors + tokenizer +
`threshold.json`, and `ml/evaluate.py … --bar 0.85` exits `0` (SC-002).

- [ ] T008 [US1] Collect PRs: from `ml/`, run `python collect_prs.py --repos "<owner/repo,...>" --out raw_prs.jsonl --per-repo 200` with `GITHUB_TOKEN` set — produces `ml/raw_prs.jsonl` (Raw PR records; merged→LEGIT, spam/invalid→SLOP) per [contracts/pipeline-cli.md](./contracts/pipeline-cli.md) Step 1
- [ ] T009 [US1] Build dataset: `python build_dataset.py --raw raw_prs.jsonl --synthetic 800 --out dataset` — produces `ml/dataset/{train,val,test}.parquet` (80/10/10). Confirm the run does NOT abort on leakage; a leakage abort (FR-005) means fix input data, not bypass the guard
- [ ] T010 [US1] Fine-tune (VRAM-safe): `python train.py --data dataset --out ..\model\model --epochs 3 --batch-size 4 --lr 2e-5 --bar 0.85` — writes `model.safetensors` (never `.bin`), `config.json`, tokenizer files, `threshold.json`. On CUDA OOM, retry `--batch-size 2` ([research.md](./research.md) R2)
- [ ] T011 [US1] Verify artifact completeness: `model/model/` has `config.json`, `model.safetensors`, tokenizer files, and `threshold.json`; confirm `threshold.json` carries `threshold`, `val_precision`, `bar` per [data-model.md](./data-model.md) "Threshold record"
- [ ] T012 [US1] Run the honesty gate: `python evaluate.py --model ..\model\model --test dataset\test.parquet --bar 0.85`; record reported SLOP precision and confirm exit code `0` (SC-002). If non-zero, iterate on data/training (T008–T010) — never lower `--bar` (FR-010)
- [ ] T013 [P] [US1] Author `scripts/train-and-eval.ps1` that runs T008→T012 in order with the VRAM-safe batch size and stops on the first non-zero exit (so the gate can't be skipped) — wrapper only ([research.md](./research.md) R10)

**Checkpoint**: A trusted, honesty-gated artifact exists at `model/model/`.
This is the MVP — the stack can now boot.

---

## Phase 4: User Story 2 — Run the two-service stack (Priority: P2)

**Goal**: Configure `.env` and start both services; confirm health and threshold
parity.

**Independent Test**: With the P1 artifact present, `docker compose up` reaches
healthy for both services; model `/healthz` reports the artifact threshold and
the gateway sources it (SC-005/SC-006).

- [ ] T014 [US2] Create `.env` from `.env.example`; set `GITHUB_WEBHOOK_SECRET` and `GITHUB_TOKEN`; leave `CONFIDENCE_THRESHOLD` unset so the gateway sources the artifact threshold ([contracts/service-endpoints.md](./contracts/service-endpoints.md) "Threshold parity")
- [ ] T015 [US2] Bring up the stack: `docker compose up --build`; confirm `sentinel-model` becomes healthy (artifact mounted `:ro` from T010–T012) and `gateway` reaches healthy, neither crash-looping
- [ ] T016 [US2] Verify model health: query `GET /healthz` → `status:"ok"`, and its `threshold` equals the value in `model/model/threshold.json` ([contracts/service-endpoints.md](./contracts/service-endpoints.md))
- [ ] T017 [P] [US2] Verify gateway endpoints: `curl http://localhost:8080/healthz` → 200 and `curl http://localhost:8080/stats` → 200 with JSON counters + version
- [ ] T018 [US2] Assert threshold parity: with no `CONFIDENCE_THRESHOLD` override, confirm the gateway's effective threshold equals the model's advertised value (SC-006)
- [ ] T019 [P] [US2] Author `scripts/verify-stack.ps1` that probes the three endpoints and asserts `status:"ok"` + threshold parity, exiting non-zero on mismatch — read-only checks, no serving-path change
- [ ] T020 [US2] Confirm absent-artifact behavior (edge case / SC-008): temporarily point the mount away, `docker compose up`, and verify `sentinel-model` exits with ONE actionable message and does NOT crash-loop (`restart: "no"`); restore the mount afterward

**Checkpoint**: A running, health-verified stack backed by the trusted artifact.

---

## Phase 5: User Story 3 — Observe verdicts on live traffic (Priority: P3)

**Goal**: Run observe-only before enabling GitHub writes.

**Independent Test**: With `SHADOW_MODE=true`, a processed PR event logs a verdict
with its correlation id and writes nothing to GitHub (SC-007).

- [ ] T021 [US3] Enable observe-only: set `SHADOW_MODE=true` in `.env`, `docker compose up -d`; confirm config parses the flag (no code change — `internal/config/config.go` already reads it, [research.md](./research.md) R9)
- [ ] T022 [US3] Deliver a sample PR event and confirm the gateway logs the verdict + confidence with its correlation id and makes ZERO writes to GitHub (SC-007)
- [ ] T023 [US3] Disable observe-only (unset `SHADOW_MODE` or set false), restart, and confirm an above-threshold verdict applies the `needs-human-review` label + comment (Story 3 scenario 2) — verify on a test PR you own only

**Checkpoint**: Verdict behavior confirmed in both observe-only and acting modes.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Documentation and repeatability. No serving-path edits.

- [ ] T024 [P] Add a training + bring-up runbook section under `docs/` linking [quickstart.md](./quickstart.md); keep it consistent with shipped behavior (constitution VIII — docs match code)
- [ ] T025 [P] (Optional) Add one offline stdlib check in `ml/tests/` covering a helper-script assumption (e.g. threshold.json shape) — `ml/tests/` is writable; no new framework
- [ ] T026 Run the full [quickstart.md](./quickstart.md) once end-to-end on a clean shell to confirm the documented path reproduces (SC-001)
- [ ] T027 Verify no secret leakage: grep produced files (`raw_prs.jsonl`, logs, docs) confirm no token / webhook secret / diff body persisted (FR-016)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: no dependencies — start immediately
- **Foundational (Phase 2)**: depends on Setup — BLOCKS all stories
- **US1 (Phase 3)**: depends on Foundational — the MVP; **US2 cannot boot without it**
- **US2 (Phase 4)**: depends on US1 (the model service refuses to start without the artifact)
- **US3 (Phase 5)**: depends on US2 (needs a running stack)
- **Polish (Phase 6)**: depends on the stories you intend to ship

### Story Dependencies (this feature is a pipeline, so stories are sequential)

- US1 → US2 → US3. This differs from the usual "independent stories" pattern
  because the artifact is a hard runtime precondition for the stack, and the
  stack is a precondition for observing traffic. Each story is still
  independently *testable* via its own Independent Test.

### Within Each Story

- US1: collect → build → train → verify → evaluate (gate). The gate (T012) is terminal.
- US2: configure → up → verify health → verify parity → verify fail-fast.
- US3: shadow on → observe → shadow off → confirm write.

### Parallel Opportunities

- T004 (GPU check) parallel with T002/T003 finish.
- T013 (train wrapper) parallel with the manual US1 run.
- T017 (gateway probes) parallel with T016 (model probe).
- T019 (verify script) parallel with manual US2 checks.
- T024/T025 (docs + offline check) parallel in Polish.

---

## Parallel Example: User Story 2

```bash
# After `docker compose up` is healthy, probe model and gateway concurrently:
Task: "Verify model GET /healthz status + threshold (T016)"
Task: "curl gateway /healthz and /stats for 200 (T017)"
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Phase 1 Setup → 2. Phase 2 Foundational → 3. Phase 3 US1.
4. **STOP and VALIDATE**: `ml/evaluate.py` exits 0 at bar 0.85 (SC-002).
   At this point a trusted, deployable artifact exists even if the stack never runs.

### Incremental Delivery

1. Setup + Foundational → environment ready.
2. US1 → trusted artifact (MVP — the gate is green).
3. US2 → running, health-verified stack.
4. US3 → observe-only confidence, then enable writes.

### Notes

- [P] = different files/artifacts, no dependency on incomplete tasks.
- The precision gate (T012) is the acceptance bar; a red gate blocks US2, and the
  fix is better data/training, not a lower bar (FR-010, constitution III).
- Commit only when the user explicitly asks; never push to `main` without asking.
- Everything new lives outside the serving path (FR-015).
