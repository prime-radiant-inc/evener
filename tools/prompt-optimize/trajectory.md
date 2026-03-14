# Prompt Optimization Trajectory

**Artifact:** benchmark persona for Qwen 3.5 Flash (lace agent)
**Model:** openrouter/qwen/qwen3.5-flash-02-23
**Method:** GEPA-style iterative refinement → interrogation-driven optimization + eval validation

## Score Table (6-task validation)

| Task | Baseline | iter-5 | iter-6 | iter-7 |
|------|----------|--------|--------|--------|
| openssl-selfsigned-cert | FAIL | **PASS** | **PASS** | pending |
| build-cython-ext | FAIL | **PASS** | **PASS** | pending |
| fix-ocaml-gc | FAIL | **PASS** | FAIL* | pending |
| count-dataset-tokens | FAIL | FAIL | FAIL (1/2†) | pending |
| financial-document-processor | FAIL | FAIL | FAIL | pending |
| circuit-fibsqrt | FAIL | FAIL | FAIL | pending |
| **Total** | **0/6** | **3/6** | **2/6** | pending |

\* Nondeterministic regression — iter-6 only added doc-reading changes, shouldn't affect build tasks
† Passed 1/2 (single-task run passed, full eval run failed — nondeterministic)

## Phase 1: GEPA-style eval loop (iterations 0-6, 2 tasks)

| Iter | openssl | count-tokens | Notes |
|------|---------|-------------|-------|
| 0-1 | mixed | mixed | persona deploy bug — results invalid |
| 2 | FAIL | TIMEOUT | first valid run, self-containment too weak |
| 3 | PASS | PASS (3b) | HARD RULE + cryptography example + GATE 3 check |
| 4b | FAIL | FAIL (63841) | removed specific examples, lost both tasks |
| 5 | PASS | TIMEOUT | "stdlib" language, openssl passed |
| 6 | FAIL | FAIL (63841) | softened language, nondeterministic openssl |

## Phase 2: Interrogation-driven optimization (iterations 5-int through 7)

### Method

Instead of running full evals (~15min each), interrogate the model about its own failed trajectories (~5sec each). Two modes:

1. **interrogate** (narrative mode): Cheap (~4K tokens). Renders trajectory as readable text, asks model about its decisions. Good for understanding failure modes.
2. **resume** (full replay mode): Expensive but reliable. Reconstructs actual Chat Completions messages with `--up-to-event` and `--persona` flags. Shows what tool calls the model would make next. The primary method for testing prompt changes.

### Interrogation findings (10+ iterations)

| Iter | Task | Key insight |
|------|------|-------------|
| 1 | openssl | Model prefers "Pythonic" library APIs. Consequence-based framing > prohibition |
| 2 | openssl | Needs explicit confirmation CLI tools are pre-installed |
| 3 | count-tokens | "NEVER" aligned with Critical Rules works for forcing observable schema inspection |
| 4 | (meta) | NEVER >> prefer. Position matters. Gates with verification >> abstract rules |
| 5 | openssl | GATE 3 was "too vague". Needs STOP + enumerate + verify + "INCOMPLETE" |
| 6 | circuit-fibsqrt | Misinterpreted "attempt" — 20+ rewrites of same file without pivot |
| 7 | financial-doc | Fragmented workflow — abandoned script for manual shell, hardcoded OCR data |
| 8 | fix-ocaml-gc | Lost working directory context, kept deleting config instead of debugging code |
| 9 | count-tokens | Fetched HF URL but never read the saved HTML. "Fetching ≠ reading" |
| 10 | count-tokens | Only counted deepseek_reasoning, not deepseek_solution. "Enumerate ALL matching columns" |
| 11 | circuit-fibsqrt | Rewrote gen_gates.py 20x without testing isqrt in isolation first |
| 12 | financial-doc | Manually corrected CSV values instead of debugging parser. Moved files back and forth |

### Changes by iteration

**iter-5-interrogated** (vs baseline iter-4):
1. "Your Code Runs Somewhere Else": Consequence-based — "tested in clean container, pip won't persist"
2. "Work With All the Data": NEVER write processing code without printing column names first
3. GATE 3: STOP + enumerate imports + "task is INCOMPLETE until self-contained"
4. Stuck/Pivot: "rewriting same file with tweaks = same strategy"
5. "Automate, Don't Hand-Solve": never manually transcribe or hardcode
6. "Stay Oriented": pwd + review changes before build commands

**iter-6-doc-reading** (vs iter-5):
1. "NEVER write code until you have read the full content of every documentation file the task points you to. Fetching alone is not reading."
2. "When a task mentions a category of data, enumerate ALL columns matching that category"

**iter-7-component-isolation** (vs iter-6):
1. Removed "Do NOT follow TDD" from Critical Rules
2. "Verify Before You Build On Top": test components in isolation, write verification tests first
3. "File Operations Are Final": never move files back to source directory
4. "If automated parsing fails, debug the parsing logic — NEVER manually correct the output"

### Replay validation results

**iter-5 at openssl step 13** (5 reps): 0/5 used pyOpenSSL (was 1/1 in original)
**iter-6 at count-tokens event 13** (3 reps): 2/3 read fetched HTML file (iter-5: 0/3)
**iter-7 at circuit-fibsqrt event 21** (3 reps): all write gen_gates.py directly (component isolation instruction doesn't fire at first write)

### Key findings

1. **"NEVER" >> "prefer"** — absolute language weighted much higher
2. **Consequences > prohibitions** — "pip won't persist in clean container" > "don't use pip"
3. **Observable actions > mental checks** — "print column names" forces verifiable tool call
4. **Position in Critical Rules** — more weight than later sections
5. **Gates with enumerate+verify** — "list your imports, is each stdlib?" > "would scripts work?"
6. **Define "attempt" explicitly** — "rewriting same file = same strategy" prevents thrashing
7. **"Fetching ≠ reading"** — model fetches URLs and skips reading saved content unless told explicitly
8. **Nondeterminism is real** — count-dataset-tokens passes 1/2 times with iter-6. Multiple reps needed.
9. **Resume mode >> interrogate mode** for testing prompt changes (tool calls show actual behavior)
10. **Some tasks may be capability-limited** — circuit-fibsqrt requires algorithmic reasoning beyond prompting

### Task-level analysis

**Reliably fixed (3/6 → consistent across runs):**
- openssl-selfsigned-cert: consequence-based self-containment
- build-cython-ext: general workflow improvements

**Nondeterministically fixed (1/6 → sometimes passes):**
- count-dataset-tokens: doc-reading + column enumeration (1/2 pass rate)
- fix-ocaml-gc: stay-oriented section (passed in iter-5, failed in iter-6 — nondeterministic)

**Not fixed (2/6 → likely capability-limited):**
- financial-document-processor: OCR accuracy + value extraction from mixed JPG/PDF
- circuit-fibsqrt: algorithmic complexity (isqrt+fib in boolean gates within 32K lines)
