# Serf Optimization Notebook

Living document tracking the current experimental state. Read this first when starting
a new session.

**Methodology:** The `benchmark-driven-improvement` skill defines the experimental
process — hill-climbing protocol, commit-on-branch-before-deploy, root-cause-from-transcripts.
Invoke it before starting work.

## Current State (March 25, 2026)

**Model:** gpt-5.4 for evals
**Baseline:** 56/88 = 64% on full 89-task terminal-bench (job: `full-89-ef120d4`)
**High water mark:** 75/89 (84%) tasks ever passed across all runs

### Shipped on main (commit 0977af1)

- Vision side-channel (off-loop API call, LLM-driven purpose parameter)
- detail:"original" for GPT-5.4+ images
- WithModel provider/model resolution (fixes explorer and subagent models)
- Vision section rewrite (no read_file mention in core.md)
- "Do the work, then verify" workflow guidance in core.md
- Write-early reinforcement ("If you haven't written your output files, you haven't started")
- Use-defaults: reasoning_effort on spawn_agent + stuck escalation
- Verify-depth: coordinator reads contents, not just checks file existence
- Implementer: web_fetch + task_list tools added
- Implementer: "Do not assume — verify" in How to Work
- Coordinator: "Do NOT re-derive or recompute the answer"

### Uncommitted, ready to test

1. **Workspace tree fix** (`agent/profile.go`): Shows workspace tree instead of parent
   tree in the environment block. Fixes the 78K `/tmp/` dump that was confounding all
   local tests. The parent tree scan (`scanParentTree`) picked up thousands of experiment
   files when the workspace was under `/tmp/`, inflating subagent prompts from ~10K (AWS)
   to ~88K (local). This was a massive confounder — local implementers appeared to read
   READMEs because the filesystem dump included HuggingFace cache paths.

2. **SystemPromptAsUser flag** (`session.go`, `main.go`, `run.go`, `serf_agent.py`):
   Delivers system prompt as user message. Tested 0/3 on AWS, but the implementation
   put the system prompt BEFORE the task message. This doesn't help — GPT-5.4 follows
   the instruction closest to the end of context. The system prompt needs to come AFTER
   the task, or be combined into one message with the task.

3. **Variant A coordinator** (`/tmp/coord-variants/A-artifact-only.md`): Artifact-only
   verification — coordinator inspects files, formats, and workspace state but never
   reruns computation. Tested 8/9 on AWS regression (only adaptive-rejection-sampler
   failed, known nondeterministic). Ready to ship.

## Active Experiment

**SystemPromptAsUser combined message:** The next test is combining the system prompt
and task delegation into ONE user message, so the persona instructions are adjacent to
(not separated from) the task. The current implementation delivers them as separate
messages with system prompt first, which GPT-5.4 ignores. Not yet implemented or tested.

## Key Learnings

### GPT-5.4 instruction following

- **GPT-5.4 follows instructions closest to end of context.** The `instructions`
  parameter goes at the beginning; user messages at the end. The model follows whichever
  is last. 0/45 across 15 system prompt variants on AWS.
- **System prompt instructions are ignored when user message implies routine work.**
  The implementer sees the task as "pure execution" and skips research/prerequisite
  steps regardless of system prompt wording.
- **XML-tagged prerequisites in user messages work.** H28 (`<mandatory_prerequisites>`
  + numbered steps + "Follow the documented usage instructions, not your assumptions"):
  6/6 locally. But local results were contaminated by the /tmp/ dump, so this needs
  AWS validation.
- **XML tags + numbered steps + "follow docs" is the minimum formula.** Remove any
  one element and the implementer stops reading documentation. The XML tags create a
  semantic block the model processes with higher authority than prose.
- **Graphviz doesn't work with GPT.** 0/7 compliance. Use prose with CRITICAL markers.
- **Prohibitions don't work with GPT.** Use positive framing.
- **Prompts don't change GPT-5.4 vision behavior.** "Trust what you see", "describe
  before coding" — all ignored. Only code-level changes (tool_choice, detail parameter)
  affect behavior.

### Delegation architecture

- **The coordinator should NOT pre-research domain problems.** Domain research and
  implementation are interleaved — separating them loses the feedback loop where
  "I need to know X" emerges from "I'm trying to build Y." In every passing
  protein-assembly run, the implementer did its own research. In every failure,
  the coordinator split research into separate subagents that consumed the budget.
- **Explorer = workspace scout** (files, tools, tests). NOT domain research.
- **Coordinator plans, delegates whole problems, verifies.** Does NOT decompose
  into phases.
- **Implementer owns the full problem** including domain research.
- **Time-box:** explorer=5, implementer=50.
- **What failed:** coordinator-does-implementation (0/9), coordinator-explores-itself
  (3/9), budget-aware framing (3/9), hard ban on research (0/3).

### Coordinator behavior

- **Coordinator override: caused by HARD GATE conflicting with no-rederive rule.**
  The HARD GATE demanded "run verification commands yourself," so the coordinator
  ran Python to re-derive answers. Fixed by variant A: artifact-only verification
  where both step 4 and the HARD GATE say "inspect files, not recomputation."
- **"Inspect" and "verify" are too vague.** The coordinator interprets them as
  running Python. Must be specific: "file existence, format, workspace state."

### Vision

- **Vision side-channel architecture works.** chess-best-move: 6/6 locally, 2-3/3
  on AWS. Off-loop API call with no tools forces native vision. LLM-driven `purpose`
  parameter ensures task-relevant descriptions.
- **GPT-5.4 reads chess boards perfectly** at medium+ effort with detail:original
  when no tools are available. The entire problem was the agent pipeline, not the
  model's vision capability.
- **Vision section mentioning read_file causes GPT to call read_file** instead of
  using its native vision on images already in context.
- **system_prompt_append only reaches root session.** Changes for implementers must
  go in core.md.

### Infrastructure and methodology

- **Parent tree scan dumped 78K of /tmp/ into subagent prompts.** This invalidated
  all local test results before the workspace tree fix. Local implementers appeared
  to read READMEs because the filesystem dump gave them breadcrumbs.
- **Local testing diverges from AWS for prompt compliance.** All 12 local implementers
  read the README regardless of prompt. 0/6 AWS implementers did with the same prompt.
- **Local repro with --agent implementer:** Fast iteration (~3 min per run) for testing
  implementer behavior. Sends AWS-style delegation message directly, bypassing coordinator.
- **Debrief technique:** Resume a completed session and ask the model WHY it did
  something. It reliably identifies which instructions it noticed and how it
  prioritized them. Two breakthroughs came from this: coordinator HARD GATE
  contradiction and implementer "pure execution" mental model.
- **Verify your binary.** `strings binary | grep "expected text"`. Check transcript
  headers after run to confirm correct prompt.
- **harbor-runner run IDs collide at minute granularity.** Space launches by at least
  1 minute.
- **detail:"original" is GPT-5.4-specific.** Older models use "high". Set in adapter
  based on model name prefix.
- **WithModel must resolve provider/model strings.** Agent frontmatter uses canonical
  `openai/gpt-5.4-mini` format, but WithModel stored it verbatim. Fixed with provider
  prefix parsing.
- **Agent frontmatter model names must be bare** (e.g., `gpt-5.4-mini` not
  `openai/gpt-5.4-mini`). The provider is inherited from the parent session's profile.
  Provider-prefixed names get sent verbatim to the API and fail.

### Image pipeline architecture

- **read_file on images:** `env_local.go` detects image → `parseImageResult()` extracts
  bytes + MIME type → `ToolExecResult.ImageData` → adapter includes in API call
- **Native OpenAI adapter** adds `input_image` item — model sees images correctly
- **OpenAI-compatible adapter** (third-party providers) was missing image support —
  fixed in the vision side-channel work
- **detail parameter:** "original" for GPT-5.4+ (full fidelity spatial tasks),
  "high" for older models. Set in `openai/adapter.go` based on model name prefix.
- **Despite images reaching the model,** GPT-5.4 defaults to code analysis in
  tool-calling mode. The vision side-channel (off-loop no-tools API call) is the fix.

## Experiment Log

| Date | Experiment | Tasks | Result | Notes |
|------|-----------|-------|--------|-------|
| 3/20 | H1-H9: delegation experiments | polyglot, ars | H9 best (3/3 delegation) | Prose > graphviz |
| 3/21 | Full discriminator gpt-5.4 | 56 tasks ×1 | 35/53 (66%) | Model upgrade helps |
| 3/21 | Full 89-task eval | 89 tasks ×1 | 56/88 (64%) | Baseline for fix work |
| 3/21 | Vision v1-v12 overlays | chess ×1 each | 3/5 pass (n=1 noise) | system_prompt_append trap |
| 3/21 | Vision v13-v15 contract | chess ×3 each | 0/6 | Prompt didn't reach implementer |
| 3/21 | fix-read-tests | polyglot 3/3, sqlite 2/3 | **Shipped** | Coordinator reads /tests/ |
| 3/21 | fix-workspace-clean | git-multi 2/2, polyglot 2/3 | **Shipped** | Coordinator cleans up |
| 3/21 | fix-escalate | fix-code-vuln 0/3 | Reject | "Report contradictions" insufficient |
| 3/21 | fix-check-environment | qemu 1/3, mteb 0/3 | Weak | Not shipped |
| 3/21 | fix-write-early | query 1/1, chess 0/2, gcode 0/3 | Weak | Not shipped |
| 3/21 | fix-verify-literal | regex 1/2, dna 0/2, mcmc 0/1 | Weak | Not shipped |
| 3/21 | fix-vision-coordinator | chess 0/2, gcode 0/2 | Reject | |
| 3/22 | Combination validation | 3 targets + 8 regression | 8/9 pass | Shipped combo works |
| 3/22 | fix-vision-core (AWS, correct binary) | chess 0/3, gcode 1/3 | gcode first pass! | kv-store-grpc regressed once |
| 3/22 | Local v4 "not code — text" | chess ×5 local | 2/5 wrote file, 0/5 correct | Behavior changed, accuracy bad |
| 3/22 | fix-vision-section (prompt) | chess ×3 AWS | 0/3 | Removed read_file mention, neutral for vision |
| 3/22 | fix-vision-section regression | 8 regression ×1 AWS | 8/8 pass | Safe to ship |
| 3/22 | fix-detail-high | chess ×3 AWS | **1/3** | detail:"high" helps |
| 3/22 | fix-explorer-model | chess ×3 AWS | **1/3** | WithModel fix, explorer works |
| 3/22 | Combined (no write-first) | chess ×3 AWS | **1/3** | All 3 fixes, same rate |
| 3/22 | Combined + write-first | chess ×3 AWS | 0/3 | "Do the work then verify" didn't help |
| 3/22 | Combined + trust-vision | chess ×3 AWS | **1/3** | "Trust what you see" ignored by GPT |
| 3/22 | force-text (tool_choice=none) | chess ×3 AWS | 0/3 | Eliminated rabbit hole but hallucinated positions |
| 3/22 | Direct API vision test | chess ×1 each | 5/5 move correct | medium/high perfect, low/none close |
| 3/22 | force-text (tool_choice=none) | chess ×3 AWS | 0/3 | Empty text + hallucination |
| 3/22 | Vision side-channel v1 | chess ×3 local | **3/3** | LLM-driven purpose, chess-specific suffix |
| 3/22 | Vision side-channel v2 | chess ×3 local | **3/3** | Generic suffix — still works |
| 3/22 | Side-channel AWS validation | chess 2/3, gcode 1/3 | **Shipped** | chess 0→2/3, gcode holds, regression 7/7 |
| 3/22 | install-windows-3.11 | windows ×3 AWS | 0/2 | NOT vision — socket path mismatch |
| 3/23 | Fix A: Write-early | tune-mjcf 1/3→3/3, ptr 1/3→0/3 | **Shipped** | "If you haven't written output, you haven't started" |
| 3/23 | Fix B: Interface conventions | sam 0/3→0/3, caffe 0/3→0/3 | Reject | Coordinator guidelines don't change implementer |
| 3/23 | Fix C: Verify depth | sanitize 1/3→2/3, sqlite 1/3→1/3 | **Shipped** | Marginal on sanitize |
| 3/23 | Failure rerun with shipped fixes | 45 non-reliable tasks | 6 improved, 0 regressed | count-dataset-tokens, sqlite-gcov, tune-mjcf now reliable |
| 3/23 | Eval v2 (gpt-5.4-mini) | 70+ tasks | 45/70 reliable (64%) | 7 tasks improved vs v1, 0 regressed |
| 3/24 | Coordinator override + impl research | mteb ×3 AWS | **1/3** (first pass) | web_fetch + "do not re-derive" shipped |
| 3/24 | Coordinator debrief | mteb rep 3 | N/A | HARD GATE contradicted no-rederive rule |
| 3/24 | Coord variant A (artifact-only) | mteb ×3 local | **3/3** | Aligned step 4 + HARD GATE on artifact inspection |
| 3/24 | Coord variant B (inspect) | mteb ×3 local | 0/3 | "Inspect" too vague, coordinator ran Python |
| 3/24 | Coord variant C (trust-implementer) | mteb ×3 local | 1/3 | No verification breaks other tasks |
| 3/24 | Coord variant D (test-only) | mteb ×3 local | 0/3 | Most tasks lack test suite |
| 3/24 | Coord variant A AWS regression | 9 regression ×1 | 8/9 | Only ARS failed (nondeterministic). Ready to ship |
| 3/25 | Impl V-series (12 variants) | mteb ×3 AWS each | 0/36 | System prompt step 4 wording: zero effect |
| 3/25 | Impl J-series (authority framing) | mteb ×3 AWS each | 0/9 | "Mandatory," "non-negotiable": zero effect |
| 3/25 | Impl D-series (prerequisite framing) | mteb ×3 AWS each | 2/6 | First signal but unreliable |
| 3/25 | Impl A2 (coord delegation) | mteb ×3 AWS | **2/3** | Best sys prompt approach, still stochastic |
| 3/25 | Impl H28 (XML user msg) | mteb ×6 local | **6/6** | `<mandatory_prerequisites>` + numbered + "follow docs" |
| 3/25 | Impl H29M (no XML tags) | mteb ×2 local | 0/2 | Same content without XML: fails |
| 3/25 | Impl H33 (no "follow docs") | mteb ×2 local | 0/2 | XML + numbered but no "follow docs": fails |
| 3/25 | SystemPromptAsUser (AWS) | mteb ×3 + regression | 0/3 | System prompt placed BEFORE task — wrong order |

### Detailed experiment writeups

- `2026-03-17-gepa-prompt-optimization.md` — Phase 1 GEPA prompt optimization
- `2026-03-21-full-89-failure-analysis.md` — Root cause analysis of all 22 non-too-hard failures
- `2026-03-21-failure-inventory.md` — Fix plan and execution order
- `2026-03-22-vision-breakthrough.md` — Vision prompt causes vision failure (bisect proof)
- `2026-03-22-vision-side-channel.md` — Side-channel architecture and results
- `2026-03-23-gpt54mini-eval-v2.md` — gpt-5.4-mini eval v2 results
- `2026-03-23-failure-root-causes.md` — Root causes from tuning round
- `2026-03-24-coordinator-override-experiments.md` — HARD GATE debrief and variant A/B/C/D
- `2026-03-25-implementer-research.md` — System prompt vs user message, XML prerequisites

## What's Next

See [backlog.md](backlog.md) for the prioritized queue of next experiments.
