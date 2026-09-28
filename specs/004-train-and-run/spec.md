# Feature Specification: Train Model & Bring Up Stack

**Feature Branch**: `004-train-and-run`

**Created**: 2026-09-28

**Status**: Draft

**Input**: User description: "Train the Sentinel CodeBERT slop classifier and bring the two-service stack to a functional, running state on a developer workstation. The repo ships the full training pipeline (ml/), the model service (model/), and the Go gateway (cmd/, internal/), but NO trained artifact exists: model/model/ is absent, there is no dataset, no weights, no threshold.json, and the model service exits(1) at startup because _verify_artifact fails. This feature covers the end-to-end operational path a developer follows to produce the artifact and reach a working system, from establishing a compatible Python environment through collecting data, building the dataset, fine-tuning, evaluating against the precision gate, and running the stack. Scope is operational/procedural only: NO changes to serving-path code (cmd/, internal/, model/, ml/); the deliverable is the executed path plus any helper tooling and documentation needed to run it reliably, and the acceptance bar is a booted stack backed by a real artifact that clears the precision gate."

## User Scenarios & Testing *(mandatory)*

<!--
  User journeys ordered by importance. Each is independently testable and delivers
  standalone value. P1 is the minimum path to a trusted, running system.
-->

### User Story 1 - Produce a trusted model artifact (Priority: P1)

A developer with no trained model runs the offline pipeline end to end — establishes a compatible Python environment, collects real pull requests as the legitimate class, builds a leakage-checked dataset with synthetic slop, fine-tunes the classifier on their GPU, and evaluates the result against the precision honesty gate — producing a weights file and a validation-selected threshold that the evaluation step confirms is trustworthy.

**Why this priority**: Without a trusted artifact nothing else is possible. The model service refuses to start when the artifact is absent, so this story is the prerequisite for every other capability. It is also the only story that touches the non-negotiable precision gate, which is the project's core promise to real contributors.

**Independent Test**: Run steps 0–4 on a clean workstation and confirm `model/model/` contains `model.safetensors`, `config.json`, tokenizer files, and `threshold.json`, and that the evaluation step exits with success (SLOP precision ≥ 0.85 on the held-out test set). Delivers a deployable, honesty-gated artifact even if the stack is never started.

**Acceptance Scenarios**:

1. **Given** a workstation whose default Python has no wheels for the pinned ML dependencies, **When** the developer establishes the supported Python 3.11 environment and installs `ml/requirements.txt`, **Then** all dependencies install without a build-from-source failure and the training scripts import successfully.
2. **Given** a valid GitHub token and a list of source repositories, **When** the developer runs the collection step, **Then** a raw dataset file of merged (legitimate) and spam/invalid (slop) pull requests is produced without exceeding GitHub rate limits.
3. **Given** the raw dataset, **When** the developer runs the dataset build step, **Then** an 80/10/10 train/validation/test split with synthetic slop is written and the build aborts if any identical example appears across splits (leakage guard).
4. **Given** the built dataset and a GPU with roughly 4 GB of memory, **When** the developer runs fine-tuning with a memory-appropriate batch size, **Then** training completes without running out of GPU memory and writes the weights and `threshold.json` to the artifact directory.
5. **Given** a freshly trained artifact, **When** the developer runs the evaluation step against the held-out test set, **Then** it reports SLOP precision and exits successfully only if precision is at or above 0.85, and exits with failure otherwise.

---

### User Story 2 - Run the two-service stack against the artifact (Priority: P2)

With a trusted artifact in place, the developer configures environment secrets and starts both services together, then confirms the gateway and model service are healthy and that the gateway is sourcing the artifact's selected threshold.

**Why this priority**: This turns a validated artifact into a running system a developer can exercise. It depends on P1 (the model service will not boot without the artifact) but delivers the visible "it works" outcome: a live endpoint that reports health and threshold.

**Independent Test**: With a P1 artifact present and a configured environment file, start the stack and confirm the model service reports healthy with the artifact's threshold, and the gateway reports healthy and reachable. Delivers a running system independent of any live GitHub traffic.

**Acceptance Scenarios**:

1. **Given** a trusted artifact and a configured environment file, **When** the developer starts the stack, **Then** both services reach a running state and neither crash-loops.
2. **Given** the running model service, **When** its health endpoint is queried, **Then** it reports status ok and advertises the threshold read from the artifact.
3. **Given** the running gateway, **When** its health and statistics endpoints are queried, **Then** they respond successfully and the gateway's effective threshold matches the artifact's advertised value (no environment override set).
4. **Given** the artifact directory is absent or incomplete, **When** the developer starts the stack, **Then** the model service exits with a single actionable message naming the artifact path and the command to produce it, and does not crash-loop.

---

### User Story 3 - Observe verdicts on live traffic before acting (Priority: P3)

Before allowing the system to write labels and comments to real pull requests, the developer runs it in an observe-only mode so verdicts are logged for inspection but no changes are made to GitHub.

**Why this priority**: A safety and confidence step. It lets a developer confirm the model behaves sensibly on their real traffic before it is trusted to act, but it is optional and depends on both prior stories, so it is lowest priority.

**Independent Test**: With the stack running and observe-only mode enabled, deliver a sample pull request event and confirm a verdict is logged with its correlation identifier while no label, comment, or check is written to GitHub. Delivers pre-production confidence independent of enabling write actions.

**Acceptance Scenarios**:

1. **Given** the stack running in observe-only mode, **When** a pull request event is processed, **Then** the verdict and its confidence are recorded in the logs and no write to GitHub occurs.
2. **Given** observe-only mode is disabled, **When** a pull request event scored above threshold is processed, **Then** the configured review label and comment are applied to the pull request.

---

### Edge Cases

- **Precision gate fails**: evaluation reports SLOP precision below 0.85. The artifact must be treated as untrusted and not deployed; the developer iterates on data quantity/quality or training settings rather than lowering the bar. This is expected to sometimes require more than one pass.
- **GPU out of memory**: the default batch size is too large for a ~4 GB GPU. The developer reduces the batch size (or falls back to CPU) rather than changing model architecture.
- **No slop-labeled PRs found during collection**: the legitimate class is populated from merged PRs but the collected slop class is sparse; the dataset build compensates with synthetic slop, and the developer is warned if the real slop count is low.
- **Missing or incomplete artifact at startup**: the model service must fail fast with one actionable message and must not crash-loop (restart policy honors this).
- **Missing environment secrets**: starting the stack without the required webhook secret or token must fail with a clear message identifying the missing variable, not start a half-configured gateway.
- **GitHub rate limiting during collection**: the collection step must pace requests so a normal run does not exhaust the token's rate budget.
- **Threshold file absent (older artifact)**: the model service falls back to a conservative default threshold and logs that it did so, rather than failing.

## Requirements *(mandatory)*

<!-- Operational/procedural requirements. NO serving-path code changes are in scope. -->

### Functional Requirements

- **FR-001**: The process MUST establish a Python environment compatible with the pinned ML dependencies, because the developer's default interpreter has no compatible wheels for them.
- **FR-002**: The process MUST collect real pull requests from configurable source repositories, classifying merged PRs as legitimate and spam/invalid-labeled closed PRs as slop, using a provided GitHub token.
- **FR-003**: The collection step MUST pace its requests so a normal run stays within the GitHub token's rate limits.
- **FR-004**: The process MUST build a train/validation/test dataset in an 80/10/10 split, augmented with synthetic slop examples drawn from disjoint random streams per split.
- **FR-005**: The dataset build MUST abort if any identical example (by title and diff) appears in more than one split (leakage guard), so the precision gate cannot be gamed by leakage.
- **FR-006**: The process MUST fine-tune the pre-trained base classifier into a two-class (legitimate/slop) model and write the weights in the safetensors format plus a threshold file to the artifact directory.
- **FR-007**: Fine-tuning MUST support a configurable batch size so it fits within the developer's GPU memory (~4 GB), and MUST be able to use the GPU when present.
- **FR-008**: The process MUST select a confidence threshold on the validation split (the lowest cutoff whose SLOP precision clears the bar) and persist it alongside the weights.
- **FR-009**: The process MUST evaluate the trained artifact on the held-out test set and MUST report failure if SLOP precision is below 0.85, blocking deployment of an untrusted artifact.
- **FR-010**: The precision bar of 0.85 MUST NOT be lowered to force a pass; a failing gate is resolved by improving data or training, not by weakening the gate.
- **FR-011**: The process MUST configure the required runtime secrets (GitHub webhook secret, GitHub token, model URL) via an environment file before starting the stack.
- **FR-012**: The process MUST start both services together such that the model service serves the artifact and the gateway can reach it.
- **FR-013**: The process MUST verify system readiness after startup by confirming the model service health endpoint reports ok with the artifact threshold, and the gateway health and statistics endpoints respond successfully.
- **FR-014**: The process MUST support running in an observe-only mode in which verdicts are logged but no writes are made to GitHub, as a pre-production confidence step.
- **FR-015**: The deliverable MUST NOT modify serving-path code (the gateway, the model service, or the training pipeline beyond tests/tooling); it consists of the executed operational path plus any helper tooling and documentation needed to run it reliably.
- **FR-016**: No step MUST log or persist the diff body, GitHub token, or webhook secret.

### Key Entities *(include if feature involves data)*

- **Raw PR record**: one collected pull request — its title, diff, class source (merged → legitimate, spam/invalid → slop), and originating repository and number.
- **Dataset split**: the train, validation, and test partitions (80/10/10), each a mix of real and synthetic-slop examples, guaranteed leakage-free across splits.
- **Model artifact**: the directory the model service loads — model config, safetensors weights, tokenizer files, and the threshold file. Its directory name is the operator-facing artifact version.
- **Threshold record**: the validation-selected confidence cutoff, the validation precision at that cutoff, and the precision bar it was selected against.
- **Evaluation report**: the test-set SLOP precision (and related metrics) plus the pass/fail decision against the 0.85 bar.
- **Runtime environment file**: the configured secrets and options (webhook secret, token, model URL, optional threshold override, label, observe-only flag) that parameterize the running stack.

## Success Criteria *(mandatory)*

<!-- Measurable, technology-agnostic outcomes. -->

### Measurable Outcomes

- **SC-001**: Starting from a clean workstation, a developer can produce a trusted artifact and a running stack by following the documented path, with no edits to serving-path code required.
- **SC-002**: The evaluation step exits successfully with reported SLOP precision at or above 0.85 on the leakage-free held-out test set before the artifact is deployed.
- **SC-003**: The dataset build fails closed on any cross-split leakage, so a passing precision result cannot be produced from a leaked dataset.
- **SC-004**: Fine-tuning completes on a ~4 GB GPU without an out-of-memory failure at the documented batch size.
- **SC-005**: After startup, the model service health endpoint reports ok and advertises the artifact's threshold, and the gateway health and statistics endpoints respond successfully within a few seconds of the services reaching running state.
- **SC-006**: With no threshold override configured, the gateway's effective threshold equals the value advertised by the artifact.
- **SC-007**: In observe-only mode, processing a pull request event produces a logged verdict and zero writes to GitHub.
- **SC-008**: When the artifact is absent or incomplete, the model service emits exactly one actionable message and does not crash-loop.

## Assumptions

- The developer's default Python interpreter (3.14) has no wheels for the pinned ML dependencies; a supported Python 3.11 environment is established for the offline pipeline. The serving containers already pin their own compatible runtime and are unaffected.
- The developer has a GitHub token with permission to read the chosen source repositories and their pull requests, and the token is supplied via environment, never hard-coded.
- The developer's workstation has a GPU with roughly 4 GB of memory (an RTX 3050 class device); the default batch size is reduced to fit, and CPU is an acceptable but slower fallback.
- The pre-trained base classifier is fine-tuned (not trained from scratch); "training" here means fine-tuning plus threshold selection.
- Collected slop examples may be sparse; synthetic slop augmentation is expected and acceptable, and the real-slop count may be low.
- The stack runs on a single developer workstation via the existing container composition; high-availability and orchestration concerns are out of scope.
- The existing gateway, model service, and training pipeline are correct and unchanged; this feature only executes and documents the operational path and adds helper tooling/tests outside the serving path.
- Docker (with the existing composition) is available on the workstation to run the two services.
