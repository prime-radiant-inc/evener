# Full 89-Task Eval Failure Analysis

**Run:** `full-89-ef120d4` on gpt-5.4, March 21 2026
**Result:** 56/88 pass (64%), 1 task didn't run (dna-assembly setup timeout)

## Failures by Root Cause Category

### 1. Timeout / Agent Ran Out of Time (6 tasks)

These tasks timed out (AgentTimeoutError) without producing deliverables.

| Task | Verifier | Notes |
|------|----------|-------|
| **chess-best-move** | 0/1 (no move.txt) | 63 vision reads, 39 shells. Agent spent all time on image analysis. Vision prompt working but agent can't close. |
| **compile-compcert** | 0/3 (no binary) | 61 shells, spawned fix agent. CompCert build is slow and complex on modern Ubuntu. |
| **gcode-to-text** | 0/2 (no out.txt) | 48 shells, 35 vision reads. G-code task involves image. Agent burned time on analysis. |
| **make-mips-interpreter** | 0/3 (VM timeout) | 157 shells. Built interpreter but VM execution timed out during verifier. |
| **polyglot-rust-c** | 0/1 (no binary) | 13 shells. Rust compilation failed, agent couldn't fix in time. |
| **protein-assembly** | 0/1 (wrong output) | 37 shells. Produced fusion protein but it didn't match expected gene blocks. |

**Pattern:** These are hard implementation tasks where the agent either ran out of time during building/analysis, or the verifier itself timed out running the deliverable.

**What would help:** Time awareness — the agent doesn't know how much time it has left. A "time remaining" signal could prompt it to write a best-effort deliverable before timeout.

### 2. Close But Wrong — Implementation Bugs (8 tasks)

Agent produced deliverables but they don't quite match verifier expectations.

| Task | Verifier | Root Cause |
|------|----------|------------|
| **db-wal-recovery** | 5/7 | Recovered 5 records but missed encrypted WAL records. Verifier wants all 11. |
| **fix-code-vulnerability** | 390/391 | 390 tests pass! Failed only `test_cwe_id` — wrong CWE classification string. |
| **mcmc-sampling-stan** | 5/6 | Failed `test_stan_model_sampling` — rstan sampling output format mismatch. |
| **mteb-retrieve** | 1/2 | Failed `test_data_matches` — retrieval results don't match expected output. |
| **overfull-hbox** | 3/4 | Failed `test_input_file_matches` — modified input file when it shouldn't have. |
| **query-optimize** | 5/6 | Failed `test_compare_golden_vs_solution_runtime` — query correct but too slow (performance). |
| **regex-chess** | 1/4 | Failed 3 game tests — regex engine produces wrong positions. |
| **sqlite-with-gcov** | 2/3 | Failed `test_gcov_enabled` — no .gcda files (gcov not actually collecting coverage). |

**Pattern:** Most of these pass the majority of tests. The failures are specific: wrong format (fix-code-vulnerability CWE string), performance (query-optimize), or subtle behavior differences (overfull-hbox modifying input file).

**What would help:** Better self-verification. The coordinator's verify-fix loop should catch "modified input file" or "output format wrong." The agent needs to re-read the task description after implementing and check each requirement literally.

### 3. Cleanup / Workspace State (2 tasks)

Deliverable code works but workspace state is wrong.

| Task | Verifier | Root Cause |
|------|----------|------------|
| **polyglot-c-py** | 0/1 | Expected only main.py.c but found cmain binary. Same cleanup issue from earlier experiments. |
| **path-tracing** | 2/5 | Failed `test_no_deps` — linked against /usr/lib. Also no output image produced. |

**Pattern:** Lingering build artifacts or unexpected dependencies.

**What would help:** Coordinator should `ls` deliverable directory and check for unexpected files before submitting.

### 4. Configuration / Environment Issues (3 tasks)

The implementation approach was wrong for the environment.

| Task | Verifier | Root Cause |
|------|----------|------------|
| **configure-git-webserver** | 0/1 | Web server returned HTTP 000. Service not running when verifier checks. |
| **qemu-alpine-ssh** | 0/1 | SSH connection failed (sshpass error). VM probably not fully booted or SSH not configured. |
| **dna-insert** | 0/1 | Invalid number of primers. Implementation doesn't match the biology spec. |

**Pattern:** These require services to be running or domain-specific correctness.

**What would help:** For service tasks: coordinator should verify the service is actually responding before submitting. For domain tasks: need to research the domain requirements more carefully.

### 5. Never-Passed Discriminator Tasks (3 tasks)

These have never passed in any serf run.

| Task | Verifier | Root Cause |
|------|----------|------------|
| **filter-js-from-html** | 0/14 | HTML parser normalizes everything (whitespace, entities, attributes). Needs byte-level approach. |
| **install-windows-3.11** | 3/4 | QEMU monitor socket path wrong. Verifier expects `/tmp/qemu-monitor.sock`. |
| **gcode-to-text** | 0/2 (timeout) | No output file. Vision-heavy task, agent ran out of time. |

### 6. Nondeterministic Regressions (2 tasks)

These passed in previous runs but failed here — likely nondeterministic.

| Task | Verifier | Previous | Notes |
|------|----------|----------|-------|
| **git-multibranch** | 0/1 | Passed in batch 2 | Too-easy task. HTTPS deploy test failed. Possible container networking issue. |
| **break-filter-js-from-html** | 0/1 | Passed in first gpt-5.4 run | XSS bypass test failed. Agent approach varies per run. |

## Vision-Related Task Analysis

Tasks that involve image files:

| Task | Vision reads | Result | Notes |
|------|-------------|--------|-------|
| **chess-best-move** | 63 | FAIL | Used vision extensively but spent all time on analysis, never wrote move.txt. The "describe then verify" prompt is in core.md but doesn't prevent the pixel-extraction rabbit hole. |
| **gcode-to-text** | 35 | FAIL (timeout) | Similar to chess — vision used heavily but no output produced. |
| **path-tracing** | 1 | FAIL | Only 1 vision read — barely used vision. Failed on no-deps and no output image. |
| **install-windows-3.11** | 38 | FAIL | Used vision for VNC screenshots. 3/4 pass. Failed on keyboard visual feedback — monitor socket path issue. |

**Vision prompt assessment:** The core.md vision section successfully triggers vision usage (63 and 35 reads on chess/gcode). But it doesn't prevent the agent from also doing extensive pixel analysis code alongside. The model uses vision as one input among many rather than as the primary analysis tool.

**Key insight:** For chess-best-move, the overlay experiments (system_prompt_append) showed 3/5 pass rate, but core.md showed 1/4 pass rate. The overlay only reached the coordinator, and somehow the coordinator's visual description + delegation to implementer was MORE effective than having the implementer see the vision prompt itself. Possible explanation: when the coordinator describes what it sees and passes that to the implementer, the implementer gets a text description of the position and can focus on chess analysis. When the implementer has the vision prompt and sees the image itself, it falls into the pixel-analysis trap.

## Summary of Fixable Improvements

1. **Cleanup enforcement** — polyglot-c-py keeps failing on cmain. Coordinator verify step should check directory state.
2. **Literal requirement checking** — fix-code-vulnerability (wrong CWE), overfull-hbox (modified input), mcmc-sampling-stan (format) — agent needs to re-read requirements and check each one.
3. **Service verification** — configure-git-webserver, qemu-alpine-ssh — coordinator should curl/ssh to verify services before submitting.
4. **Performance self-check** — query-optimize — agent should benchmark its solution.
5. **Vision delegation** — for image tasks, coordinator should describe the image and pass the description to the implementer (as text), rather than having the implementer analyze the image itself.
6. **install-windows-3.11** — just needs the QEMU monitor socket at `/tmp/qemu-monitor.sock`.
