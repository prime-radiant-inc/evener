# Terminal-Bench Capability Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one bounded OpenAI transport recovery, derive one general execution-policy improvement from trajectory evidence, and measure it only on Terminal-Bench 2.1 tasks that failed the pinned Serf run.

**Architecture:** Transport recovery remains inside the OpenAI adapter and retries the same Responses request once before the existing Chat fallback. Trajectory analysis produces immutable run artifacts before any prompt change. The policy experiment builds baseline and treatment binaries from adjacent commits so the prompt is the only A/B variable.

**Tech Stack:** Go 1.26, Serf's OpenAI Responses adapter, Harbor 0.20, Terminal-Bench 2.1, `kata` 0.14, shell/JSON trajectory analysis.

## Global Constraints

- Do not change delegate management, delegate cleanup, async tool handles, or lifecycle semantics; another session owns that work.
- Do not submit or upload benchmark results.
- Never rerun a task that passed the pinned 89×1 run while debugging or A/B testing.
- Use `openai/gpt-5.6-luna` at `max` effort and one attempt per A/B arm.
- Keep Serf commit, runner commit, task digest, model, effort, and host identical between A/B arms; only the execution-policy commit may differ.
- Do not use public task solutions. Mark comparisons unavailable when official trajectories cannot be authenticated or accessed.
- Use Terra at low reasoning for extraction and mechanical implementation; use Sol at max reasoning for review and synthesis.
- Tests must exercise behavior. Do not add prompt-wording, rendered-command, or large-string matching tests.
- Preserve OAuth records as mode `0600`; never print their contents.
- Kata parent: `880h`; children: transport `83fk`, comparison `nqeh`, policy `dh25`, A/B `fzah`.

---

### Task 1: Retry One Silent Responses Stream

**Files:**
- Modify: `llm/providers/openai/adapter.go`
- Modify: `llm/providers/openai/adapter_test.go`
- Modify only if attempt accounting changes: `llm/providers/openai/wire_capture_test.go`
- Modify only if its behavioral call count changes: `llm/providers/openai/adapter_runtime_coverage_fuzz_test.go`

**Interfaces:**
- Consumes: `Adapter.streamResponses`, `Adapter.shouldFallbackToChatCompletions`, `errEmptyResponsesStream`, `llm.Stream`
- Produces: `Adapter.decodeStream` behavior that issues at most two consecutive Responses attempts for one request before existing fallback logic

- [ ] **Step 1: Write the failing recovery test**

Add a local `httptest.Server` contract in `adapter_test.go`: the first `/v1/responses` request returns HTTP 200 with an empty event stream, the second returns a text delta and completed event, and `/v1/chat/completions` records any unexpected hit. Assert recovered text, exactly two Responses hits, zero Chat hits, and no stream error.

- [ ] **Step 2: Run the recovery test and observe the existing fallback**

Run:

```bash
go test ./llm/providers/openai -run TestStream_EmptyResponsesStream_RetriesResponsesBeforeFallback -count=1
```

Expected before implementation: failure because only one Responses request occurs and Chat fallback is attempted.

- [ ] **Step 3: Add failure-path behavioral coverage**

Cover two empty Responses streams followed by the existing Chat fallback. Cover an image-bearing tool result where two empty Responses streams surface the empty-stream error without attempting lossy Chat fallback. Assertions are endpoint hit counts, emitted events, and errors—not rendered request strings.

- [ ] **Step 4: Implement one bounded same-endpoint retry**

Update `decodeStream` to close the empty stream, call `streamResponses` once with the unchanged request, and consume that retry before consulting `shouldFallbackToChatCompletions`. Do not retry after any content event. Do not loop beyond the single retry. Keep context cancellation and stream closure behavior intact.

- [ ] **Step 5: Verify focused and package behavior**

Run:

```bash
go test ./llm/providers/openai -run 'TestStream_EmptyResponsesStream|TestStreamFallbackWaitsForResponsesAttemptAppend' -count=1
go test ./llm/providers/openai -count=1
make lint
```

- [ ] **Step 6: Commit and update Kata**

Commit the production code and meaningful tests. Comment on `83fk` with the red/green evidence; close it only after the Sol-max review and full gate pass.

### Task 2: Compare Codex-Pass and Serf-Fail Trajectories

**Files:**
- Create run artifact: `/Users/jesse/git/prime-radiant/harbor-runner/runs/analysis/tb21-codex-serf-delta.json`
- Create run artifact: `/Users/jesse/git/prime-radiant/harbor-runner/runs/analysis/tb21-codex-serf-delta.md`
- Do not modify Serf production code in this task.

**Interfaces:**
- Consumes: pinned run `tb21-luna-max-89-3e42802ce-20260810T224339Z`, locally retained Codex and Serf Harbor attempts, verifier rewards, exception metadata, ATIF trajectories
- Produces: one row per task with outcome evidence, action differences, causal classification, source paths, and contamination/access flags

- [ ] **Step 1: Materialize the comparison set**

Select every task for which a local Codex attempt has verifier reward `1` and the pinned Serf attempt has reward `0` or an execution exception. Exclude `gpt2-codegolf` from behavioral conclusions because its local Codex pass used a public exact solution. Record unavailable official Codex trajectories as unavailable rather than inferring their actions.

- [ ] **Step 2: Extract structured evidence**

For each selected task, record task name, both artifact paths, rewards, exception types, elapsed time, provider wait time, tool time, first end-to-end verification time, final verification action, background/service use, and the smallest observed action difference. Classify each difference as `harness`, `provider-boundary`, `execution-policy`, `semantic`, `contaminated`, or `insufficient-evidence`.

- [ ] **Step 3: Produce JSON and a concise Markdown synthesis**

Write deterministic JSON ordered by task name. The Markdown report must distinguish repeated patterns from one-offs and must not treat correlation as causation.

- [ ] **Step 4: Sol-max evidence review and Kata update**

Have a Sol-max reviewer trace every claimed difference back to the named trajectory events. Correct or remove unsupported claims. Comment on `nqeh` with artifact paths and the reviewed repeated patterns; close only after review.

### Task 3: Derive One General Execution-Policy Change

**Files:**
- Modify only after evidence review: `agent/prompts/sections/workflow.md.tmpl`
- No prompt-wording unit test
- Record decision artifact: `/Users/jesse/git/prime-radiant/harbor-runner/runs/analysis/tb21-execution-policy-decision.md`

**Interfaces:**
- Consumes: reviewed Task 2 comparison rows
- Produces: either one concise general workflow instruction supported by at least two independent tasks, or a reviewed no-change decision

- [ ] **Step 1: Apply the evidence threshold**

Select a behavior only when at least two independent, uncontaminated tasks show the same missing action and the action applies to ordinary software work. Exclude all delegate-management findings. Reject benchmark-specific task advice, hidden time-budget advice, and duplicated instructions already present in `workflow.md.tmpl`.

- [ ] **Step 2: Write the decision artifact**

Name supporting tasks and exact events, quote the existing workflow rule that is insufficient, state the smallest proposed instruction, and list plausible regressions. If no candidate clears the threshold, record `no-change` and do not edit the prompt.

- [ ] **Step 3: Sol-max design review**

Have a Sol-max reviewer test whether the rule is general, non-duplicative, non-delegate-related, and likely to change the evidenced behavior. The reviewer must approve before implementation.

- [ ] **Step 4: Implement the approved one-line policy**

Add only the approved instruction to `workflow.md.tmpl`. Run `go test ./agent -count=1`, `make lint`, and `make build`. Do not add a string-matching test; live A/B is the model-behavior validation boundary.

- [ ] **Step 5: Commit and update Kata**

Commit the policy separately so its parent is the exact A/B baseline. Comment on `dh25` with the decision artifact and review result. Close it only after the policy commit passes local gates.

### Task 4: A/B Only Failed Tasks

**Files:**
- Create baseline/treatment manifests under `/Users/jesse/git/prime-radiant/harbor-runner/runs/analysis/`
- Create paired result artifact: `/Users/jesse/git/prime-radiant/harbor-runner/runs/analysis/tb21-failed-only-policy-ab.json`
- Create paired report: `/Users/jesse/git/prime-radiant/harbor-runner/runs/analysis/tb21-failed-only-policy-ab.md`

**Interfaces:**
- Consumes: completed pinned 89×1 run, baseline commit immediately before Task 3 policy, treatment policy commit, immutable runner and Terminal-Bench 2.1 digest
- Produces: paired one-attempt results for failed tasks only, with no submission

- [ ] **Step 1: Freeze the failed-task manifest**

After the 89×1 run completes, select only tasks with verifier reward `0` or an execution exception. Preserve scored passes even when Harbor also records an exception, including `password-recovery`. Sort and hash the manifest; assert it contains no reward-1 task.

- [ ] **Step 2: Build exact baseline and treatment binaries**

Build Linux binaries from the parent and child commits around the Task 3 policy. Record commit SHA and binary SHA-256 for each. Both commits must include the image/PDF normalization and Task 1 transport retry.

- [ ] **Step 3: Run paired Harbor arms on Magic Kingdom**

Run each failed task once with the baseline binary and once with the treatment binary using Harbor 0.20, Terminal-Bench 2.1, Luna max, the same runner commit, the same host, and no submission. Alternate arm order by sorted task index to reduce time-order bias. Never enqueue a task outside the frozen manifest.

- [ ] **Step 4: Analyze paired outcomes**

Report pass/pass, fail/pass, pass/fail, and fail/fail counts; execution exceptions; elapsed time; and whether the intended behavior actually changed in each trajectory. Do not attribute a one-run reward change to the prompt without matching trajectory evidence.

- [ ] **Step 5: Sol-max validation and Kata update**

Have a Sol-max reviewer verify manifests, hashes, task exclusion, commit isolation, and claims. Comment on `fzah` with run IDs and artifact paths. Close it only after both arms finish and the reviewed report is complete.

- [ ] **Step 6: Parent issue completion**

Close `880h` only after all four child issues are closed with commit or run evidence. If Task 3 correctly concludes no change, close that child with `--audit-no-change` and the reviewed decision artifact.
