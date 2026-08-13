# Final Requirements Audit Experiment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Test whether one runtime-injected final verification task makes a non-interactive Luna root agent catch an unmet requirement before delivering its result.

**Architecture:** An opt-in CLI experiment gate reaches a session-owned finalization guard. Before the result tool commits terminal side effects, the guard turns the first eligible final result into one ordinary verify task containing the original assignment verbatim, then lets the existing task steering and model loop continue. The Harbor adapter exposes the experiment as an explicit per-agent kwarg so treatment runs remain isolated and auditable.

**Tech Stack:** Go, Serf scripted-provider session tests, Python, pytest, Harbor 0.20.0, Terminal-Bench 2.1, GPT-5.6-Luna max, Docker on Magic Kingdom.

## Global Constraints

- This is an experiment, not a product merge proposal.
- Apply the treatment only to fresh non-interactive root sessions when explicitly enabled.
- Interactive sessions, subagents, empty task lists, and unfinished task lists retain existing finalization behavior.
- Inject the exact audit wording from the approved design; add no task-specific coaching or instruction to fix findings.
- The first attempted final result emits no terminal communication event and is not returned to the caller.
- Do not change task schemas, dependency semantics, system prompts, Terminal-Bench assets, task resources, or Harbor limits.
- Default tests remain deterministic and make no live provider calls.
- Run only failed tasks. Do not rerun a clean reward-one task.
- Use one attempt, zero Harbor retries, no upload, and no Terminal-Bench submission.
- Preserve all treatment trajectories and track total reported provider cost against Jesse's $500 ceiling.

## Experiment Outcome (2026-08-13)

The treatment was a null result and this branch should remain unmerged.

- `sqlite-with-gcov`: reward 0 without exception. The audit lifecycle worked,
  but Luna checked coverage in the separate build tree, ignored that its own
  gcov-symbol subcheck did not pass, and left `/app/sqlite` without runtime
  coverage data.
- `count-dataset-tokens`: reward 0 without exception. Before injection, Luna
  computed the required `79586` candidate but wrote `63841`. The injected audit
  performed no new inspection or computation, immediately asserted compliance,
  and returned the unchanged artifact.

Both trials completed all ordinary tasks, received exactly one audit containing
the original assignment verbatim, and delivered only the post-audit terminal
response. Neither audit changed the deliverable or reward. The exact prompt is
therefore insufficient to induce evidence-based manual checking. Complete
configs, trajectories, verifier evidence, hashes, and RCAs are recorded on the
runner branch `wip/final-requirements-audit-experiment` through commit
`fd5521a`.

Harbor reported no dollar cost through the OAuth path (`cost_usd: null`). The
two trials used 144,640 input, 1,259,904 cached, and 30,177 output tokens. The
campaign stopped after the two preselected failures rather than spending the
remaining authorized budget on unsupported repetitions.

---

### Task 1: Implement the opt-in session finalization guard

**Files:**
- Create: agent/session_final_requirements_audit_experiment.go
- Create: agent/session_final_requirements_audit_experiment_test.go
- Modify: agent/session.go
- Modify: agent/session_config.go
- Modify: agent/session_lifecycle.go
- Modify: agent/session_tool_registry.go
- Modify: agent/session_tools_communicate.go

**Interfaces:**
- Consumes: SessionConfig.ExperimentalFinalRequirementsAudit, the first accepted root user input, the existing TaskStore, formatCurrentTaskSteering, and the result tool's pre-commit path.
- Produces: (*Session).maybeStartFinalRequirementsAudit() (bool, error), where true means the terminal result was deferred and the audit task was started.

- [ ] **Step 1: Write the failing end-to-end session test**

Use a scripted provider to create task 1, start it, complete it, attempt a first terminal result, observe the audit task on the next request, complete task 2, and deliver a second terminal result:

~~~go
func TestFinalRequirementsAudit_DefersFirstResultAndRunsAsFinalTask(t *testing.T) {
    const original = "compile in /app/sqlite\nkeep this 'quoted' requirement"
    // Script: append #1 -> start #1 -> done #1 -> first communicate ->
    // assert next request contains original unchanged -> done #2 -> final communicate.
    // Assert ProcessInput returns only "audited final".
    // Assert the store has exactly two done tasks and task #2 is type verify.
    // Assert only one EventCommunicate was emitted and it is "audited final".
}
~~~

- [ ] **Step 2: Write the failing exclusion table**

Exercise the real session loop and assert the first terminal result is delivered without another provider call for: experiment disabled, interactive root, non-interactive subagent, enabled root with no task list, and enabled root with an in-progress task.

- [ ] **Step 3: Run the focused tests and verify they fail**

Run:

~~~bash
go test ./agent -run 'TestFinalRequirementsAudit_' -count=1 -v
~~~

Expected: FAIL because ExperimentalFinalRequirementsAudit and the guard do not exist.

- [ ] **Step 4: Add minimal session-owned experiment state**

Add the fresh-only config switch and two Session.mu-guarded values:

~~~go
// ExperimentalFinalRequirementsAudit enables the fresh non-interactive root
// treatment that inserts one final verification task. It is intentionally not
// persisted because this switch exists only for controlled experiment runs.
ExperimentalFinalRequirementsAudit bool `json:"-"`

originalTaskText                string
finalRequirementsAuditInjected bool
~~~

When acceptUserInput accepts the first EntryUserInput for an eligible root, copy input into originalTaskText exactly once before later compaction can remove it.

- [ ] **Step 5: Implement the guard as a focused unit**

Create:

~~~go
const finalRequirementsAuditDescription = "Audit original task requirements"

func finalRequirementsAuditPrompt(original string) string {
    return "The original task assigned to you was '" + original + "' - Manually check and confirm that each requirement was met and report that in your final response"
}

func (s *Session) maybeStartFinalRequirementsAudit() (bool, error)
~~~

The method must return false unless the opt-in is enabled on a fresh non-interactive root, an audit has not already been injected, and the existing task list is nonempty and exhausted. It then claims the once-only injection, appends one verify task, transitions it to in_progress, emits ordinary task progress, queues formatCurrentTaskSteering, and returns true only after the audit is active.

- [ ] **Step 6: Invoke the guard before terminal result side effects**

Add this closure to toolDeps and wire it to the session method:

~~~go
startFinalRequirementsAudit func() (bool, error)
~~~

For end_turn=true, call it after result arguments are validated but before EventCommunicate, steering drain, result commit, or watch callback. When it starts the audit, return a nonterminal acknowledgement:

~~~json
{"accepted":false,"end_turn":false,"reason":"final requirements audit started"}
~~~

- [ ] **Step 7: Format and run focused tests**

~~~bash
gofmt -w agent/session_final_requirements_audit_experiment.go agent/session_final_requirements_audit_experiment_test.go agent/session.go agent/session_config.go agent/session_lifecycle.go agent/session_tool_registry.go agent/session_tools_communicate.go
go test ./agent -run 'TestFinalRequirementsAudit_' -count=1 -v
~~~

Expected: PASS.

- [ ] **Step 8: Commit the session treatment**

Stage only the named agent files, inspect the staged diff, and commit with the hypothesis, exclusions, and scripted-provider evidence in the body.

---

### Task 2: Expose the treatment on the one-shot CLI

**Files:**
- Modify: cmd/serf/main.go
- Modify: cmd/serf/main_test.go
- Modify: cmd/serf/run.go
- Modify: cmd/serf/run_test.go

**Interfaces:**
- Consumes: --experimental-final-requirements-audit on fresh serf invocations.
- Produces: runConfig.experimentalFinalRequirementsAudit forwarded to SessionConfig.ExperimentalFinalRequirementsAudit.

- [ ] **Step 1: Write failing flag and forwarding tests**

Add this local test helper, then use mainWithDeps with a capture-only run
dependency:

~~~go
func deterministicMainDepsForTest(t *testing.T) mainDeps {
    t.Helper()
    return mainDeps{
        stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard,
        stdinMode: func() (os.FileMode, error) { return os.ModeCharDevice, nil },
        exit: func(code int) { t.Fatalf("unexpected exit %d", code) },
        dispatch: func([]string, io.Reader, io.Writer, io.Writer) (bool, string, error) {
            return false, "", nil
        },
        startCPU: func(string) (func(), error) { return func() {}, nil },
        startTrace: func(string) (func(), error) { return func() {}, nil },
        notify: func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
            return context.WithCancel(ctx)
        },
        run: func(context.Context, runConfig) error { return nil },
    }
}

func TestMain_ExperimentalFinalRequirementsAuditFlagReachesRunConfig(t *testing.T) {
    var captured runConfig
    deps := deterministicMainDepsForTest(t)
    deps.args = []string{"--experimental-final-requirements-audit", "do the task"}
    deps.run = func(_ context.Context, cfg runConfig) error {
        captured = cfg
        return nil
    }
    mainWithDeps(deps)
    if !captured.experimentalFinalRequirementsAudit {
        t.Fatal("experiment flag was dropped")
    }
}
~~~

Add a run-level test that replaces runNewSession, invokes run with the bool set, and asserts the received agent.SessionConfig.ExperimentalFinalRequirementsAudit is true before returning a sentinel construction error.

- [ ] **Step 2: Verify the focused tests fail**

~~~bash
go test ./cmd/serf -run 'Test(Main|Run)_ExperimentalFinalRequirementsAudit' -count=1 -v
~~~

Expected: FAIL because the flag and forwarding fields do not exist.

- [ ] **Step 3: Add and forward the opt-in flag**

Add:

~~~go
flags.experimentalFinalRequirementsAudit = fs.Bool(
    "experimental-final-requirements-audit",
    false,
    "run one final original-requirements audit task before completing",
)
~~~

Thread it through runCLIFlags, runConfig, mainWithDeps, and the fresh agent.SessionConfig. Do not add it to serve or restore configuration.

- [ ] **Step 4: Format and run CLI plus agent tests**

~~~bash
gofmt -w cmd/serf/main.go cmd/serf/main_test.go cmd/serf/run.go cmd/serf/run_test.go
go test ./cmd/serf -run 'Test(Main|Run)_ExperimentalFinalRequirementsAudit' -count=1 -v
go test ./agent -run 'TestFinalRequirementsAudit_' -count=1 -v
~~~

Expected: PASS.

- [ ] **Step 5: Commit CLI activation**

Stage only the four CLI files, inspect the staged diff, and commit the explicit fresh-run, experiment-only activation.

---

### Task 3: Add an explicit Harbor treatment switch

**Files:**
- Modify in /Users/jesse/git/prime-radiant/harbor-runner: src/harbor_runner/serf_agent.py
- Modify in /Users/jesse/git/prime-radiant/harbor-runner: tests/test_serf_agent.py

**Interfaces:**
- Consumes: final_requirements_audit: bool = False in SerfAgent kwargs.
- Produces: --experimental-final-requirements-audit in Serf argv only when enabled.

- [ ] **Step 1: Create a fresh runner WIP branch from origin/main**

Confirm the runner worktree is clean, then create wip/final-requirements-audit-experiment. Do not reuse historical run branches.

- [ ] **Step 2: Write the failing argv behavior test**

Extend make_agent with a keyword-only final_requirements_audit bool defaulting false. Create enabled and disabled agents, capture each real argv with RecordingEnvironment, and assert the flag is present only for the enabled treatment.

- [ ] **Step 3: Verify the focused test fails**

~~~bash
uv run pytest tests/test_serf_agent.py -k final_requirements_audit -q
~~~

Expected: FAIL because the kwarg and conditional argv do not exist.

- [ ] **Step 4: Implement conditional argv**

Store the bool in SerfAgent.__init__. Build the existing argument sequence, inserting --experimental-final-requirements-audit immediately before -- only when enabled. Preserve lifecycle wrapping, redirections, model, effort, round cap, state, and ATIF paths.

- [ ] **Step 5: Run runner tests and commit**

~~~bash
uv run pytest tests/test_serf_agent.py -q
uv run pytest -q
~~~

Stage only the two runner files, inspect the staged diff, and commit the opt-in switch.

---

### Task 4: Verify and build the experimental Serf artifact

**Files:**
- No source files expected.
- Produce locally: serf-linux-amd64

**Interfaces:**
- Consumes: Tasks 1-3 commits.
- Produces: a static Linux/amd64 binary whose help exposes the experiment flag and whose SHA-256 is recorded.

- [ ] **Step 1: Run focused and package tests**

~~~bash
go test ./agent -run 'TestFinalRequirementsAudit_' -count=1 -v
go test ./cmd/serf -run 'Test(Main|Run)_ExperimentalFinalRequirementsAudit' -count=1 -v
go test ./agent ./cmd/serf
~~~

- [ ] **Step 2: Run proportional repository gates**

~~~bash
make test
make lint
make build-linux
~~~

Any sighted failure must be root-caused immediately. Do not disable hooks, skip a failing family, or widen a timeout.

- [ ] **Step 3: Validate the real binary**

~~~bash
go version -m serf-linux-amd64
file serf-linux-amd64
sha256sum serf-linux-amd64
./serf-linux-amd64 --help
~~~

Require GOOS=linux, GOARCH=amd64, CGO_ENABLED=0, and the experiment flag in help. Smoke the binary in a Linux container when it cannot execute locally.

- [ ] **Step 4: Record revisions and hashes**

Capture the clean Serf commit, runner commit, binary SHA-256, and runner test status for preflight.

---

### Task 5: Run the real sqlite-with-gcov treatment

**Files:**
- Create in the runner repository: runs/analysis/tb21-final-requirements-audit-sqlite-20260813T201105Z.harbor.json
- Create in the runner repository: runs/analysis/tb21-final-requirements-audit-sqlite-20260813T201105Z-preflight.md
- Create in the runner repository: runs/analysis/launch-tb21-final-requirements-audit-sqlite-20260813T201105Z.sh
- Preserve ignored raw output: runs/tb21-final-requirements-audit-sqlite-20260813T201105Z/

**Interfaces:**
- Consumes: the Task 4 binary and runner with final_requirements_audit true.
- Produces: one immutable Harbor trial with complete Serf state, trajectory, verifier output, result, and provider cost.

- [ ] **Step 1: Prepare and validate the one-task config**

Use one attempt, one concurrent trial, zero retries, Docker delete false, Luna max, max_rounds zero, final_requirements_audit true, the pinned Terminal-Bench 2.1 dataset ref, and only terminal-bench/sqlite-with-gcov. Fill explicit Magic Kingdom paths and a unique run ID.

Validate resolved JobConfig, binary metadata, OAuth mode without reading its contents, task checksum, absence of active Harbor jobs, absence of the target run directory, and absence of upload or submit commands.

- [ ] **Step 2: Copy exact commits and binary to Magic Kingdom**

Use a dedicated remote runner checkout or worktree at the recorded runner commit. Copy the binary by explicit path, verify its remote SHA-256, and verify the runner commit before launch.

- [ ] **Step 3: Launch and observe**

Launch under a named detached controller. Check progress at meaningful intervals of up to ten minutes and record only state transitions. Do not rerun while the first trial is active.

- [ ] **Step 4: Sync and validate completed evidence**

Require exactly one result. Record reward, exception, checksum, model, effort, cost, root session ID, task state, trajectory, verifier output, and file hashes. Verify cumulative experiment spend is below $500.

- [ ] **Step 5: Prove whether treatment executed**

From root transcript and task state, establish whether ordinary tasks were terminal before the first final attempt, exactly one audit was injected and started, its prompt preserved the original assignment, the agent performed concrete post-injection inspection or modification, the audit was marked done, and only the later final response was delivered.

- [ ] **Step 6: Root-cause any zero, exception, or vacuous audit**

Classify the first causal break as lifecycle, model compliance, requirement interpretation, inspection adequacy, corrective action, task resource/provider, or verifier. Do not add another treatment until the RCA explains the outcome.

- [ ] **Step 7: Commit compact treatment evidence**

Commit only config, preflight, launcher, outcome/RCA, and provenance hashes. Keep raw artifacts ignored.

---

### Task 6: Make the evidence-gated next decision

**Files:**
- Update the Task 5 outcome/RCA.
- Conditionally create a second one-task config for count-dataset-tokens.

**Interfaces:**
- Consumes: the complete sqlite-with-gcov causal record.
- Produces: either a justified second treatment or a documented stop.

- [ ] **Step 1: Apply the decision gate**

Run count-dataset-tokens only if the first experiment proves the audit lifecycle worked and the RCA leaves the semantic-scope hypothesis meaningfully open. Reward 1 is sufficient. Reward 0 may justify the second shape only if the audit was substantive and its remaining failure is specific to filesystem-layout correction rather than a general failure to inspect or act.

- [ ] **Step 2: If justified, run only count-dataset-tokens**

Use identical model, effort, official resources, one attempt, zero retries, no upload, no submission, and the Task 5 evidence checks. Track cumulative cost.

- [ ] **Step 3: Compare with archived failures**

Report whether each post-audit trajectory found a previously missed requirement, changed the deliverable, changed reward, or merely asserted compliance.

- [ ] **Step 4: State the verdict without overclaiming**

Classify the experiment as positive signal, behavioral-only signal, null, or harmful. One or two treatment runs cannot justify a product merge. Recommend matched confirmation only for a positive causal signal; otherwise leave the branch unmerged.
