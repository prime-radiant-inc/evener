# Agent-Core Whole-Module Fuzz Coverage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Establish an honest all-workspace fuzz-coverage gate, then raise the agent module above 95.0% deterministic fuzz-reachable statement coverage.

**Architecture:** The existing target manifest remains authoritative, but a Go registry checker validates it against AST-discovered native and explicitly marked stateful/property fuzz surfaces. The global reporter replays each local fuzz surface deterministically, unions package-local profiles, and enforces raw per-module thresholds. Agent coverage comes from real offline Session and subsystem front doors over scripted-provider, fake-clock, filesystem, and process/network boundaries.

**Tech Stack:** Go 1.25.6, native Go fuzzing, pgregory.net/rapid, github.com/spf13/afero, fuzz/fault, Bash orchestration, Go AST parsing.

## Global Constraints

- Governing design: docs/superpowers/specs/2026-07-10-whole-module-fuzz-coverage-design.md at commit 2e5b0126. The spec wins on conflict.
- Measure every workspace module: ., agent, llm, auth, envvars, fuzz, invariant. Each raw fuzz-reachable statement ratio must be strictly greater than 95.0%.
- Count only registered native Fuzz targets and registered rapid/stateful fuzz tests replayed with a fixed seed bank and check count. Do not credit ordinary, live, E2E, or integration tests.
- Default tests must be offline and deterministic. Script the LLM boundary; fake only HTTP, filesystem, clock, and process-launch boundaries. Never fuzz arbitrary shell, network, provider, or user-repository Git effects.
- Every production package needs a local registered fuzz surface before the final gate. Do not omit packages or fabricate zero profiles.
- Global profiles are package-local self-coverage profiles. Never use a cross-package -coverpkg=./... merge.
- Exclusions are whole-file only and limited to generated or unavailable-platform source. No business, orchestration, package, directory, or function exclusions.
- The controller serializes edits to scripts/run-fuzz.sh, scripts/fuzz-coverage-global.sh, scripts/fuzzcov-global-floors.txt, Makefile, and cmd/serf-fuzzcov.
- Do not bless floors until raw coverage already exceeds 95.0%. Floors never decrease.
- Read docs/testing.md before changing tests. Use TDD: focused red, smallest green change, scoped verification, then the package gate.

---

## File Map

| Path | Responsibility |
| --- | --- |
| agent/fork.go | Public ForkSession and its internal Afero-backed persistence seam |
| agent/schema/snapshot.go | Session-meta persistence over the provided filesystem |
| agent/transcript/transcript.go | Transcript-writer construction over the provided filesystem |
| cmd/serf-fuzzregistry/main.go | Parse target manifest, discover local fuzz declarations, validate identity |
| cmd/serf-fuzzregistry/main_test.go | Synthetic workspace and registry validation tests |
| cmd/serf-fuzzcov/global.go | Package-profile union, file exclusions, raw threshold accounting |
| cmd/serf-fuzzcov/global_test.go | Global accounting and exclusion validation tests |
| scripts/run-fuzz.sh | Authoritative native and rapid target manifest |
| scripts/fuzz-registry-check.sh | Thin wrapper around serf-fuzzregistry |
| scripts/fuzz-coverage-global.sh | Replay validated local targets and emit package profiles |
| scripts/fuzz-coverage-global-selftest.sh | Stubbed deterministic runner tests |
| scripts/fuzzcov-global-exclusions.txt | Initially empty file-only exclusion manifest |
| agent/lifecycle_covfuzz_test.go | Native mutation bridge for real offline Session sequences |
| agent/lifecycle_seqfuzz_test.go | Stateful Session model and semantic oracles |
| agent/job_watch_delegate_fuzz_test.go | Durable job/watch/delegate behavioral coverage |
| agent/session_tools_worktree.go | Worktree tool orchestration |
| agent/execenv and agent/sandbox | Filesystem, process, and sandbox boundaries |

---

### Task 1: Repair the Failing Fork Baseline

**Files:**
- Modify: agent/fork.go, agent/schema/snapshot.go, agent/transcript/transcript.go
- Modify: agent/cov_w3init_fork_test.go

**Interfaces:**
~~~go
func forkSessionFS(fs afero.Fs, stateDir, parentID string, divergenceTurn int, editedMessage, parentForkLabel string) (string, error)
func SaveSessionMetaWithFS(fs afero.Fs, dir string, meta SessionMeta) error
func LoadSessionMetaWithFS(fs afero.Fs, dir, id string) (SessionMeta, error)
func NewWriterWithFS(fs afero.Fs, path string, header Header) (*Writer, error)
~~~

- [ ] **Step 1: Reproduce the permission-dependent failures**

Run:
~~~sh
go test ./agent -run '^(TestW3Init_ForkSession_OpenError|TestW3Init_ForkSession_NewWriterError)$' -count=1 -v
~~~

Expected: a privileged test process bypasses chmod, so the fixture does not create the intended errors.

- [ ] **Step 2: Write deterministic filesystem-boundary tests**

Use a faulting Afero filesystem for open failure and a read-only Afero filesystem for child creation failure.

~~~go
fs := fault.FS(afero.NewMemMapFs(), fault.FromBytes([]byte{0}))
_, err := forkSessionFS(fs, stateDir, id, 1, "x", "")
if !errors.Is(err, fault.ErrInjected) {
    t.Fatalf("err = %v, want injected filesystem failure", err)
}
~~~

Run the focused test before adding the seam. Expected: build failure because the functions do not exist.

- [ ] **Step 3: Implement the real filesystem seam**

Keep ForkSession as the production API and call forkSessionFS with afero.NewOsFs. Thread the filesystem through transcript opening/writing and session-meta load/save. Do not mock or duplicate Serf persistence logic.

- [ ] **Step 4: Verify and commit**

Run:
~~~sh
go test ./agent -run '^(TestW3Init_ForkSession_OpenError|TestW3Init_ForkSession_NewWriterError)$' -count=1 -v
go test ./agent -run '^(TestForkSession_|TestS2Cov_ForkSession_|TestW3Init_ForkSession_)' -count=1 -v
(cd agent && go test ./... -count=1)
~~~

Then:
~~~sh
git add agent/fork.go agent/schema/snapshot.go agent/transcript/transcript.go agent/cov_w3init_fork_test.go
git commit -m "fix(agent): make fork persistence failures deterministic"
~~~

### Task 2: Make the Fuzz Target Registry Machine-Checked

**Files:**
- Create: cmd/serf-fuzzregistry/main.go, cmd/serf-fuzzregistry/main_test.go
- Create: scripts/fuzz-registry-check.sh
- Modify: go.mod, scripts/run-fuzz.sh, Makefile
- Modify: agent/registry_schemafuzz_test.go, agent/lifecycle_seqfuzz_test.go,
  agent/watch_seqfuzz_test.go, agent/delegate_seqfuzz_test.go,
  agent/jobs_fc2_seqfuzz_test.go, agent/internal/jobstore/seqfuzz_test.go,
  agent/internal/contextmgr/compaction_seqfuzz_test.go,
  agent/internal/contextmgr/maybecompact_fc1_seqfuzz_test.go,
  internal/appserver/router_seqfuzz_test.go, and
  internal/appserver/hub_multisession_seqfuzz_test.go

**Interfaces:**
~~~go
type Target struct {
    Kind, Module, Package, Name string
}

func ParseRegistry(r io.Reader) ([]Target, error)
func DiscoverWorkspace(root string) ([]Target, error)
func CheckTargets(registered, discovered []Target) error
func EmitPlan(w io.Writer, targets []Target) error
~~~

- [ ] **Step 1: Write failing registry fixtures**

Create temporary go.work/module fixtures covering a missing native fuzzer, stale package row, duplicate identity, invalid unmarked Rapid test, and valid native/Rapid pairs.

~~~go
want := []Target{{Kind: "native", Module: "agent", Package: ".", Name: "FuzzTurn"}}
if err := CheckTargets(want, want); err != nil { t.Fatal(err) }
~~~

Run:
~~~sh
go test ./cmd/serf-fuzzregistry -run Test -count=1
~~~

Expected before implementation: the package does not build.

- [ ] **Step 2: Implement exact discovery and validation**

Use go/parser and go/ast to discover func FuzzX(*testing.F). Add
golang.org/x/mod as a direct dependency and use modfile.ParseWork to enumerate
modules. Add the serf:fuzz rapid marker immediately above each of the ten
registered Rapid functions in the listed files. Treat only those marked Test
declarations as Rapid and require exactly one rapid manifest row. Existing test
rows remain support checks and do not count toward global coverage.

- [ ] **Step 3: Add the shell and Makefile entry point**

The wrapper writes the run-fuzz manifest to a temporary file and executes:

~~~sh
go run ./cmd/serf-fuzzregistry --repo-root "$repo_root" --registry "$registry" --check --emit-plan
~~~

Add make fuzz-registry-check. It may not run search, ordinary tests, or network traffic.

The --emit-plan output is UTF-8 TSV with no header, sorted by module, package,
kind, and name. Each validated coverage row is exactly:

~~~text
kind<TAB>module<TAB>package<TAB>name
~~~

Emit only native and rapid rows. Do not emit support-only test rows or focus
metadata; Task 4 consumes this exact four-column schema.

- [ ] **Step 4: Verify and commit**

Run:
~~~sh
make fuzz-registry-check
git add go.mod go.sum cmd/serf-fuzzregistry scripts/fuzz-registry-check.sh scripts/run-fuzz.sh Makefile agent internal/appserver
git commit -m "test(fuzz): audit registered fuzz targets"
~~~

Expected: it names the fourteen missing agent entries until Task 5 adds them, then exits zero.

### Task 3: Build Strict Global Profile Accounting And Exclusion Validation

**Files:**
- Create: cmd/serf-fuzzcov/global.go, cmd/serf-fuzzcov/global_test.go
- Modify: cmd/serf-fuzzcov/main.go, cmd/serf-fuzzcov/main_test.go
- Create: scripts/fuzzcov-global-exclusions.txt
- Modify: scripts/fuzzcov-global-floors.txt

**Interfaces:**
~~~go
type GlobalProfile struct { Module, Package, Path string }
type GlobalReport struct { Modules []ModuleReport; RawPass bool }

func ReadGlobalProfiles(r io.Reader) ([]GlobalProfile, error)
func ReportGlobal(profiles []GlobalProfile, exclusions []Exclusion, minimum float64) (GlobalReport, error)
~~~

- [ ] **Step 1: Write failing profile-accounting tests**

Test profile union, exact block deduplication, module totals, no floor lowering, strict threshold behavior, and invalid exclusions.

~~~go
report, err := ReportGlobal(profiles, nil, 95)
if err != nil { t.Fatal(err) }
if report.Modules[0].Pass {
    t.Fatal("95.0000% must not satisfy >95.0%")
}
~~~

Also prove 95.0001% passes; a generated exclusion needs a Code generated header; a platform exclusion names an actual unavailable build-constrained file.

- [ ] **Step 2: Implement accounting and file-only exclusions**

Count each package-local source block once, covered when any target profile has a positive count. Compare with integer arithmetic: covered * 100 > 95 * total.

Create an initially empty manifest:

~~~text
# module<TAB>package-relative-path<TAB>file<TAB>generated|platform<TAB>reason
~~~

Resolve each entry to one compiled production file. Reject duplicate, missing, zero-block, non-generated, and available-platform entries. Print each applied exclusion in text and JSON output.

- [ ] **Step 3: Verify and commit**

Run:
~~~sh
go test ./cmd/serf-fuzzcov -run 'Test.*Global|Test.*Exclusion' -count=1
git add cmd/serf-fuzzcov scripts/fuzzcov-global-exclusions.txt scripts/fuzzcov-global-floors.txt
git commit -m "test(fuzz): enforce strict global coverage accounting"
~~~

Expected: raw 95.0% fails and no normal production code can be excluded.

### Task 4: Replay Every Local Fuzz Surface Across the Workspace

**Files:**
- Modify: scripts/fuzz-coverage-global.sh, scripts/fuzz-coverage-global-selftest.sh
- Modify: Makefile, docs/fuzzing.md

**Consumes:** Validated targets from Task 2 and profile accounting from Task 3.

**Produces:** make fuzz-coverage-global CHECK=1 replays all local native and Rapid targets deterministically and emits profiles for every workspace package.

- [ ] **Step 1: Extend the shell self-test first**

Use a fake go executable and a manifest containing one native and one Rapid row. Assert exact run names, serffuzz build tag, all seven default modules, fixed RAPID_SEED=1,2,3,5,8, fixed RAPID_CHECKS, and failure for a package with no local fuzz surface.

Run:
~~~sh
scripts/fuzz-coverage-global-selftest.sh
~~~

Expected before implementation: the new assertions fail.

- [ ] **Step 2: Implement package-local replay**

For each module in go.work, group validated rows by module/package. Invoke each name as:

~~~sh
go test -tags serffuzz -run "^Name$" -coverprofile="$profile" "$pkg"
~~~

Run native targets once in seed-replay mode. Run Rapid targets once per fixed seed. Concatenate only profiles from one package and pass module/package/profile rows to serf-fuzzcov global accounting.

- [ ] **Step 3: Make missing local surfaces fatal**

Compare go list ./... against the validated package groups before replay. Fail each missing package with:

~~~text
missing local fuzz surface: <module>:<package>
~~~

Do not invoke the Go 1.25 no-test-binary coverage path.

- [ ] **Step 4: Wire the commands, verify, and commit**

Default global coverage to all go.work modules. Update make fuzz to replay registered Rapid targets with the fixed seed bank. Document the intentionally slow coverage gate.

Run:
~~~sh
scripts/fuzz-coverage-global-selftest.sh
make fuzz-registry-check
make fuzz-coverage-global
git add scripts/fuzz-coverage-global.sh scripts/fuzz-coverage-global-selftest.sh Makefile docs/fuzzing.md
git commit -m "test(fuzz): measure all workspace modules deterministically"
~~~

Expected: self-test/check pass. The real command may fail only with explicit missing-local-surface packages until later tasks add them.

### Task 5: Register Existing Agent Targets And Add Missing Local Surfaces

**Files:**
- Modify: scripts/run-fuzz.sh
- Create local fuzz tests in agent/command, agent/events, agent/internal/agenttest, agent/internal/clock, agent/internal/diagnostic, agent/internal/goal, agent/internal/installid, agent/internal/promptpath, agent/internal/tool/repair, agent/internal/toolname, and agent/mcpprobe

**Consumes:** Task 2 audit and Task 4 missing-surface report.

**Produces:** A valid local native fuzz target for every production agent package, plus exact registration of every existing native agent fuzzer.

- [ ] **Step 1: Register the fourteen known omissions**

Add rows at their real packages for sandbox FuzzResolve, FuzzReRoot, FuzzSeatbeltPolicyNoInterpolation; execenv FuzzSecurePathResolve, FuzzMainRootFromGitdirPointer; and worktree FuzzCUnquote, FuzzClassifyReason, FuzzDecideTotal, FuzzParseGitVersion, FuzzParseMarker, FuzzReadSidecar, FuzzSidecarJSONRoundtrip, FuzzSidecarNameRoundtrip, FuzzValidateName.

Run:
~~~sh
make fuzz-registry-check
~~~

Expected: no missing/stale/duplicate native agent registration remains.

- [ ] **Step 2: Add concrete semantic targets for the missing packages**

Use these package APIs and oracles; each target must be local to its SUT package:

- command: Expand over a recording deny environment; arguments containing shell
  and file-directive syntax must remain inert unless the literal template itself
  contains the directive.
- events: SessionEvent.ToStreamEvent; a constructed typed payload maps to its
  matching stream kind and never yields a mismatched event envelope.
- internal/agenttest: ScriptedAdapter.Complete and Requests; requests are
  recorded in order and the scripted response is replayed without a provider.
- internal/clock: Real.NewTimer, Stop, and Reset with non-positive durations;
  construction and cancellation must not panic or leave a timer usable after a
  successful Stop. Do not wait on wall-clock intervals.
- internal/diagnostic: Classify, FromError, and FromFields; normalized source is
  one of the declared source constants and repeated classification is equal.
- internal/goal: Store Set, SetTerminal, RecordContinuation, Restore, and
  PersistSnapshot; status never reverses from terminal and restore preserves the
  persisted snapshot fields.
- internal/installid: introduce LoadOrCreateInstallationIDWithFS over Afero;
  repeated calls on the same filesystem return one non-empty stable ID.
- internal/promptpath: GlobalPromptsDir and ProjectPromptsDir; project output is
  deterministic and remains rooted below the supplied Git root.
- internal/tool/repair: RepairJSON and RepairArgs; valid JSON is unchanged,
  repaired output parses, and the original args map is not mutated.
- internal/toolname: ClaudeToSerf and SerfToClaude; every known mapping round
  trips and unknown names pass through unchanged.
- mcpprobe: introduce a narrow lookup/transport seam for probeOne; generated
  stdio configs never invoke a real executable and HTTP configs use a fake
  RoundTripper, with result ordering equal to config ordering.

Do not create a target that only calls a mock. The test double must remain at
the process, transport, or filesystem boundary named above.

- [ ] **Step 3: Verify and commit**

Run:
~~~sh
(cd agent && go test -tags serffuzz -run '^Fuzz' ./command ./events ./internal/agenttest ./internal/clock ./internal/diagnostic ./internal/goal ./internal/installid ./internal/promptpath ./internal/tool/repair ./internal/toolname ./mcpprobe)
make fuzz-coverage-global FUZZ_ARGS='--modules agent'
git add scripts/run-fuzz.sh agent
git commit -m "test(agent): register and cover local fuzz surfaces"
~~~

Expected: agent advances past missing-surface preflight for those packages.

### Task 6: Expand the Real Job, Watch, And Delegate Front Door

**Files:**
- Modify: agent/lifecycle_covfuzz_test.go, agent/lifecycle_seqfuzz_test.go, agent/job_watch_delegate_fuzz_test.go, agent/delegate_seqfuzz_test.go, agent/watch_seqfuzz_test.go
- Modify only when a real boundary is missing: agent/jobs.go, agent/job_watch.go, agent/job_delegate.go, agent/session_lifecycle.go, agent/session_queue.go

**Consumes:** agenttest.ScriptedAdapter, agenttest.FakeClock, agenttest.DenyEnv, fuzz/fault, and the actual Session/jobManager.

**Produces:** Structured native and Rapid programs that reach durable job/watch/delegate behavior with semantic state invariants.

- [ ] **Step 1: Add failing operation-program cases**

Encode create, output append, terminalization, watch install/clear/replace, coalesced send, delegate completion, restore, re-arm, and notification drain in the existing artifact/model.

~~~go
type jobOp struct { Kind uint8; JobID string; Payload []byte }
type jobModel struct { Terminal map[string]bool; Delivered map[string]bool }
~~~

Run a focused seed first and confirm the new oracle fails for the intended state transition.

- [ ] **Step 2: Add only the external seam that blocks real behavior**

Thread the existing fake clock or Afero/faulting persistence opener through construction when needed. Production defaults remain real clock, OS filesystem, and real jobstore opener. Never mock Session, jobManager, or delegate logic.

- [ ] **Step 3: Assert semantic checks after every operation**

~~~go
// Folded durable state agrees with the live job record.
// A terminal state never becomes nonterminal.
// A delivery ID is accepted at most once.
// No running job remains after deterministic quiescence or close.
~~~

Use the fake clock; do not use sleeps or polling races.

- [ ] **Step 4: Verify and commit**

Run:
~~~sh
(cd agent && go test -tags serffuzz -run '^(FuzzLifecycleSeq|FuzzLifecycleSequence|TestLifecycleSeqFuzz|TestWatchSeqFuzz|TestDelegateSeqFuzz)$' . -count=1)
git add agent/lifecycle_covfuzz_test.go agent/lifecycle_seqfuzz_test.go agent/job_watch_delegate_fuzz_test.go agent/delegate_seqfuzz_test.go agent/watch_seqfuzz_test.go agent/jobs.go agent/job_watch.go agent/job_delegate.go agent/session_lifecycle.go agent/session_queue.go
git commit -m "test(agent): fuzz durable job and watch lifecycles"
~~~

### Task 7: Cover Worktree, Sandbox, And Execution Boundaries Safely

**Files:**
- Modify: agent/session_tools_worktree.go, agent/session_tools_dispatch_fuzz_test.go, agent/execenv/local_fuzz_test.go, agent/sandbox/fuzz_resolve_test.go
- Create: agent/session_tools_worktree_fuzz_test.go, agent/execenv/sandbox_frontdoor_fuzz_test.go
- Modify only when execution is concrete: agent/execenv/execenv.go, agent/execenv/local.go

**Consumes:** LocalExecutionEnvironment.SetFs, sandbox Resolve, internal worktree targets, and a scripted process/Git boundary.

**Produces:** Behavioral fuzzers that execute real validation/tool orchestration against fake filesystem/process effects, never host Git or shell.

- [ ] **Step 1: Write failing containment and differential properties**

Generate validated worktree programs through a fake Git runner. Assert rejected programs do not mutate the filesystem and accepted programs produce identical in-memory and temp-root state. For sandbox/execenv, assert each resolved root stays under its permitted base and each fake process request is a vetted form.

- [ ] **Step 2: Add the smallest process boundary and seeds**

Use a function field or minimal discovered runner interface at the command launch point. Production uses the current implementation; fuzzers use a recording fake. Seed invalid names/refs, foreign locks, clean/dirty/remove states, platform host facts, symlink/path escapes, and injected filesystem errors.

- [ ] **Step 3: Verify and commit**

Run:
~~~sh
(cd agent && go test -tags serffuzz -run '^(Fuzz.*Worktree|FuzzSecurePathResolve|FuzzMainRootFromGitdirPointer|FuzzResolve|FuzzReRoot|FuzzSeatbeltPolicyNoInterpolation)$' ./ ./execenv ./sandbox ./internal/worktree -count=1)
git add agent/session_tools_worktree.go agent/session_tools_worktree_fuzz_test.go agent/session_tools_dispatch_fuzz_test.go agent/execenv agent/sandbox
git commit -m "test(agent): fuzz worktree and sandbox boundaries"
~~~

Expected: no host repository, shell, or network activity; containment and differential oracles pass.

### Task 8: Close the Remaining Agent Gap Map And Enforce Its Threshold

**Files:**
- Modify/create local fuzz tests in agent/internal/contextmgr, agent/internal/jobstore, agent/internal/mcp, agent/plugin, agent/mcpconfig, agent/provider, agent/transcript, agent/task, and agent/doctor
- Modify root-package fuzz tests for agent/session_tools_jobs.go, agent/transcript_render.go, agent/jobs.go, and agent/session_tools_shell.go
- Modify: scripts/fuzzcov-global-floors.txt, docs/fuzzing.md

**Consumes:** Task 4 package report and existing package-local fault/codec/differential patterns.

**Produces:** agent raw deterministic fuzz-reachable coverage strictly above 95.0% and an upward-only agent floor.

- [ ] **Step 1: Capture and rank the live agent gap report**

Run:
~~~sh
make fuzz-coverage-global FUZZ_ARGS='--modules agent --format json' > /tmp/agent-fuzzcov.json
~~~

Address groups in this order: job_watch.go, session_tools_worktree.go, session_tools_jobs.go, job_delegate.go, transcript_render.go, jobs.go; then sandbox, internal/mcp, internal/tool, execenv, internal/contextmgr, and internal/jobstore.

- [ ] **Step 2: Red-green each selected group**

Add a codec fixed point, persistence differential, bounded-rendering property, or state-machine invariant over its real API. Reuse Afero, fuzz/fault, fake transport, fake clock, or the Task 7 runner seam. Extract a pure core only when it removes actual effect entanglement.

- [ ] **Step 3: Verify every agent package and the strict raw target**

Run:
~~~sh
(cd agent && go test ./... -count=1)
(cd agent && go test -tags serffuzz -run '^Fuzz' ./... -count=1)
make fuzz-registry-check
make fuzz-coverage-global CHECK=1 FUZZ_ARGS='--modules agent --minimum 95'
~~~

Expected: no credential/network/live-provider requirement and covered * 100 > 95 * total.

- [ ] **Step 4: Raise only the agent floor and commit**

Use the documented bless path only after Step 3 passes. Restore non-agent floor rows if a partial bless rewrites them, then add exact covered/total output to docs/fuzzing.md.

~~~sh
git add agent scripts/run-fuzz.sh scripts/fuzzcov-global-floors.txt docs/fuzzing.md
git commit -m "test(agent): require over 95 percent fuzz reachability"
~~~

## Follow-On Program Gates

This plan ends with the agent milestone. The design commits to the same target for root, llm, auth, envvars, fuzz, and invariant; Task 4 supplies their actual package gap maps so later plans are evidence-driven rather than guessed.

1. Complete envvars, fuzz, and invariant with local registry, codec, fault, and property surfaces.
2. Complete llm and auth with fake HTTP, clock, and filesystem front doors, preserving stream/non-stream and OAuth state-machine differentials.
3. Complete root through AppWire, hub, TUI, CLI, and tooling front doors over fake transports, filesystem roots, process launchers, and deterministic Bubble Tea messages.
4. Run make fuzz-coverage-global CHECK=1 across all seven modules. Only after every module clears the strict target, optimize runtime without weakening the target set, denominator, or seed bank.

## Plan Self-Review

- Spec coverage: Tasks 2-4 implement all-module measurement, deterministic Rapid replay, strict threshold, local-package denominator, registry audit, and narrow exclusions. Tasks 5-8 implement the agent-first behavioral coverage strategy and its >95% gate.
- Placeholder scan: follow-on module plans are explicitly deferred until Task 4 produces their package reports; this prevents guessed source edits from masquerading as implementation detail.
- Type consistency: Target identity is Kind/Module/Package/Name from discovery through replay. GlobalProfile is the exact package profile consumed by ReportGlobal.
