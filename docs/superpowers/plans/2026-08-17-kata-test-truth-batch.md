# Test-Infrastructure Truth Batch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close five claimed katas that each concern the test infrastructure lying — false greens, uncovered guards, audits that pin claims instead of facts, stale citations, and deferred test-quality nits.

**Architecture:** Five independent tasks, one kata each, executed sequentially in one worktree. Each kata is its own spec: it states evidence and a stop condition, deliberately not a solution (per `docs/conventions/agent-fleets.md`, naming the mechanism in a brief is harmful — the implementer verifies the evidence and finds the mechanism). Tasks touch disjoint files.

**Tech Stack:** Go (multi-module workspace, see `go.work`), vitest/React for `cmd/evener-hub/frontend`, bash dev-tooling under `scripts/`.

**Spec:** The kata ledger entries 72k9, e2wm, vxz3, yj52, qz3e (bodies reproduced verbatim in each task below). Normative decision records: `docs/testing.md`, `docs/conventions/agent-fleets.md`, `docs/conventions/go-workspace.md`.

## Global Constraints

- Branch: `worktree-kata-test-truth`, worktree `/Users/jesse/prime-radiant/toil-suite/evener/.claude/worktrees/kata-test-truth`. All work happens there.
- FORBIDDEN: `git stash`, `git reset`, tree-wide `git checkout`, `git clean`, `git add -A`, `git add .`. Stage by explicit path only. For before/after comparisons use `cp` round-trips to scratch.
- FORBIDDEN: `npm ci` (node_modules is one real install symlinked into the worktree; `npm ci` empties it for every worktree). FORBIDDEN: `git add` of any directory containing that symlink.
- FORBIDDEN: any integration action — push, merge, rebase, branch deletion, cherry-pick. Naming a branch or base is context, not permission. The controller integrates.
- No subagents. Review arrives from the controller after your report.
- TDD per superpowers:test-driven-development. Test output must be pristine to pass; expected errors are captured and asserted.
- Run only the tests you wrote or directly affected, plus the narrowest package run that covers your change. The controller runs the full suites centrally. Never launch `make merge-approval-gate` or other long gates.
- Mutation-list contract: for every test you write or repair, report the exact one-line source change that must make it fail, and state which of those mutations you actually ran. A test whose mutation was never executed is a test nobody has shown can fail.
- evenerfuzz-tagged tests only run under `-tags evenerfuzz`; `make test` filters `-run '^(Test|Example)'` and never sees `check*` families. Before concluding a thing is untested, remember the other build tag exists.
- Decision records are normative. If your fix would contradict `docs/testing.md`, `docs/job-control.md`, or a `docs/superpowers/specs/*-design.md`, stop and report BLOCKED with the citation instead of "fixing" it.
- If you find a kata premise wrong, say so in your report with evidence — the correction is the most valuable output. Do not silently work around it.
- Commit frequently, conventional-commit style (`fix(agent): …`, `test(fakellm): …`), each commit scoped to one coherent change, staged by explicit path.

---

### Task 1: Kata 72k9 — two fuzz-family transcript tests fail under `-tags evenerfuzz`

**Files:**
- Investigate: `agent/transcript_read_differential_fuzz_test.go` (failure at :112), `agent/transcript_structured_fuzz_test.go` (failures at :639-648), and whatever transcript code the diagnosis implicates.
- Modify: to be determined by the diagnosis — either the tests (if they assert something no longer true) or the transcript code (if it regressed).

**Interfaces:** none consumed from or produced for other tasks.

**Controller-verified baseline (2026-08-17, this worktree, commit 9c6a6f1e4):**

```
$ cd agent && go test -tags evenerfuzz -run 'TestTranscriptReadersAgreeSanity|TestStructuredTranscriptReachesDeeper' -count=1 .
--- FAIL: TestTranscriptReadersAgreeSanity (0.00s)
    transcript_read_differential_fuzz_test.go:112: clean transcript reported skips: readTranscript=1 full=1 strict=1
--- FAIL: TestStructuredTranscriptReachesDeeper (0.36s)
    transcript_structured_fuzz_test.go:639: header decode: raw=0.0% (0)  structured=88.4% (2652)
    transcript_structured_fuzz_test.go:641: entry yield (>=1 decoded entry): raw=0.0% (0)  structured=79.0% (2370)
    transcript_structured_fuzz_test.go:648: structured generator should produce a decodable header almost always, got 88.4%
FAIL
```

Both failures are deterministic in this worktree (not load flakes — the first run reproduced them exactly as the kata recorded).

**Kata body (verbatim):**

> Two evenerfuzz-tagged tests in ./agent/ fail on main and nobody has noticed, because the default gate never runs that tag.
>
>   TestTranscriptReadersAgreeSanity
>   TestStructuredTranscriptReachesDeeper
>
> Found by lane bmgz while working an unrelated kata, and proven not to be its own: it reverted its three agent/ files to base 64a891865 and reproduced byte-identical failures with identical percentages, then restored.
>
> ## What is NOT known
>
> Nobody has diagnosed either failure. So this kata begins at zero: no root cause, no reproduction beyond "run the agent package with -tags evenerfuzz", no judgement on whether the tests or the code are wrong.
>
> Both names suggest transcript decoding agreement - one that two readers agree, one that a structured reader reaches deeper than a plain one. A plausible first question is whether a transcript format change landed without updating them, but that is a guess and should be treated as one.
>
> ## Stop condition
>
> Both tests pass under `-tags evenerfuzz`, or they are shown to be asserting something no longer true and are corrected with the reasoning recorded. Do not close by deleting or skipping them without establishing which side is wrong.

**Filer's correction (verbatim, later comment — this narrows the scope):**

> 1. 'No gate watches that tag' is FALSE. lint-evenerfuzz is a member of LINT_TARGETS (Makefile:607), and 'lint: $(LINT_TARGETS)' is step 1 of merge-approval-gate.
> 2. These two tests are EXCLUDED FROM THE GATE BY NAME, deliberately. scripts/gate-surface-lib.sh:17 sets GATE_FUZZ_TEST_SKIP, which contains both 'TranscriptReadersAgreeSanity' and 'Structured.*Reach'. Their absence from the gate is by design, not an oversight, and they are expected to run only under make fuzz / make test-fuzz.
> 3. Kata 9e6r ALREADY OBSERVED BOTH OF THESE RED, with file and line.
>
> What SURVIVES as a real question: do these two pass under 'make fuzz' / 'cd agent && go test -tags evenerfuzz'? If they do not, that is a defect in a test family the gate deliberately does not run, which is a legitimate thing to fix - but it is a fuzz-family failure, not an unwatched-tag problem.
>
> The second question I posed - 'should anything watch the evenerfuzz tag' - is ANSWERED and should not be re-derived.

**Constraints specific to this task:** Do NOT remove the tests from `GATE_FUZZ_TEST_SKIP` — the skip is a recorded Jesse ruling (no fuzz-family test belongs in the default suite). Do NOT answer the "should anything watch the tag" question — it is answered. The deliverable is: both tests green under `-tags evenerfuzz`, with the diagnosis recorded (which side was wrong and why).

- [ ] **Step 1:** Reproduce both failures with the command above; confirm determinism (run twice).
- [ ] **Step 2:** Diagnose using superpowers:systematic-debugging. `git log --oneline -- agent/transcript*.go` and the failing assertions' own history are the obvious first probes. Establish which side is wrong: the tests' expectations or the transcript code.
- [ ] **Step 3:** Fix the wrong side. If the tests are stale, correct their expectations with the reasoning in a comment ONLY where the code cannot say it; if the code regressed, TDD the fix (failing test exists already — these two).
- [ ] **Step 4:** `cd agent && go test -tags evenerfuzz -run 'TestTranscriptReadersAgreeSanity|TestStructuredTranscriptReachesDeeper' -count=2 .` → PASS. Also run the narrowest untagged package tests covering any production file you touched.
- [ ] **Step 5:** Report the mutation list (for changed assertions: the one-line change that must make each red again; state which you ran).
- [ ] **Step 6:** Commit by explicit path.

### Task 2: Kata e2wm — fakellm's two stale-round guards are uncovered

**Files:**
- Investigate/Modify: `test/e2e/fakellm/cmd/main.go` (guards in `endedTurn` and `launchedJob` from commit 585d621df).
- Test: the fakellm cmd package's existing test file(s), same package.

**Interfaces:** none consumed from or produced for other tasks.

**Kata body (verbatim):**

> 585d621df ("fix(fakellm): keep a cancelled round's goroutine off the session's state") added two stale-round guards to test/e2e/fakellm/cmd/main.go - one in endedTurn, one in launchedJob - which no-op when the call is no longer state.lastAnswered. Neither is covered.
>
> ## The measurement
>
>   remove ONLY endedTurn's guard          -> TestACancelledRoundLeavesTheNextTurnItsOwnCount green x5, whole package green
>   remove ONLY `case <-call.Cancelled(): return` from the hold select -> test green x3
>   remove BOTH                            -> test fails x3, "turn 2 round 3: tool \"read_file\", want communicate"
>   remove launchedJob's guard             -> whole package green
>
> ## What that means
>
> The test constrains "at least one of the two mechanisms is present" and isolates neither. Both guards are dead on every path the package exercises.
>
> The reason is structural rather than accidental: with `case <-call.Cancelled()` present, a cancelled goroutine returns from the hold before it ever reaches endTurn, so the guard downstream is unreachable on that path. The guards are defence-in-depth for the hold expiring at the same instant as the cancellation - a race no current test wins.
>
> ## Stop condition
>
> A test that wins the race the guards exist for: the hold expiring at the same instant the call is cancelled, such that the goroutine reaches endTurn or launchedJob with a stale call. Removing either guard alone must then turn something red.
>
> If that race turns out to be unwinnable from a test, say so with the evidence and delete the guards or document them as unreachable - an unreachable guard that reads as protection is its own defect. Do not close this by asserting the guards look correct.

**Constraints specific to this task:** The winning move is usually a deterministic seam (a test hook that holds the goroutine between the select and the state mutation), not a sleep-tuned race — `docs/testing.md`'s flake policy applies; no wall-clock-tuned racing. A seam added to `main.go` must be inert in production use of the fixture.

- [ ] **Step 1:** Read `test/e2e/fakellm/cmd/main.go` around both guards and the hold select; reproduce the kata's measurement for at least the remove-both case to confirm the premise still holds.
- [ ] **Step 2:** Write the failing test: win the race so the goroutine reaches `endedTurn` (and separately `launchedJob`, if reachable) with a stale call. With a guard removed the test must go red; with guards present, green.
- [ ] **Step 3:** Verify the mutation: remove each guard alone, run the new test, confirm red; restore, confirm green. This mutation run is mandatory, not optional — it is the kata's whole point.
- [ ] **Step 4:** If the race is unwinnable, delete the guards or document them unreachable, with the evidence, per the stop condition — report which and why.
- [ ] **Step 5:** Run the whole fakellm cmd package test suite once.
- [ ] **Step 6:** Commit by explicit path.

### Task 3: Kata vxz3 — the EntryKind audit pins that a label exists, not that it is true

**Files:**
- Modify: `agent/entrykind_audit_test.go` (current audit; its own comment at :15-20 concedes the gap).
- Test: same file / sibling test scaffolding in the `agent` package (~150-250 lines expected).

**Interfaces:** none consumed from or produced for other tasks.

**Kata body (verbatim):**

> The EntryKind audit checks that every kind carries a label. It cannot check that the label is TRUE. That is a table of claims with no oracle, and a wrong label survives indefinitely: noProductionProducer's own comment records a label sitting honest-but-unactioned for three months. An audit that pins the presence of a claim rather than its truth is a false green with a test file around it - exactly the class docs/testing.md warns about.
>
> ## Why this is tractable rather than aspirational
>
> The three live labels map onto three distinct, observable behaviours, so a per-kind probe can actually assert them:
>
>   - a StableTurnID on the opening event
>   - an EventTurnStarted announce
>   - a turnNameUnserved refusal
>
> So the audit can move from "a label exists" to "the label matches what the kind does", one probe per kind.
>
> ## The exception, already settled
>
> noProductionProducer needs an exemption rather than a probe: nothing dispatches such a kind by construction, so there is no behaviour to observe. Kata z5fm already ruled that deleting the label is the wrong move, so the exemption has to be recorded rather than argued again.
>
> ## Stop condition
>
> A deliberately wrong label in the table makes the audit fail. Prove it by mutating one - that is the whole point, since the current audit passes with any label at all.

- [ ] **Step 1:** Read `agent/entrykind_audit_test.go` and the label table it audits; identify the three live labels and every kind carrying each.
- [ ] **Step 2:** TDD the probes: for each live label, a probe that observes the labelled behaviour (StableTurnID on opening event / EventTurnStarted announce / turnNameUnserved refusal) for kinds carrying it. Record the `noProductionProducer` exemption in the audit with a comment citing kata z5fm's ruling.
- [ ] **Step 3:** Run the mutation that is the stop condition: flip one label in the table to a wrong value, confirm the audit fails, restore, confirm green. Do this for at least one kind per live label; report all three in the mutation list.
- [ ] **Step 4:** Run the audit test plus any agent-package tests touching the probe scaffolding.
- [ ] **Step 5:** Commit by explicit path.

### Task 4: Kata yj52 — a scenario card cites the wrong file entirely and the citation audit passes it

**Files:**
- Investigate: the scenario citation audit (find it: it caught kata 2pm8's seven stale citations and was exercised by PR #84 / commit 4444bda2a; `scripts/scenario-cite-migrate` and the `scenarioneedle` audit are nearby search anchors; `make lint` is the gate surface that wrongly passes today).
- Modify: the audit, to cover the shape it misses; and `test/scenarios/web-steer-in-idle-fails-fast.md:148`, to cite the true location.

**Interfaces:** none consumed from or produced for other tasks.

**Kata body (verbatim):**

> test/scenarios/web-steer-in-idle-fails-fast.md:148 says:
>
>     the toast region contains `Steer failed: no active turn`
>     (`Composer.tsx:641-643`)
>
> That string does not exist in Composer.tsx at any line. It lives in cmd/evener-hub/frontend/src/shell/palette/commands.ts:515, and it is lower-case there ('steer failed: no active turn'), so the card has the wrong file, the wrong line, and the wrong capitalisation of the literal it claims to quote.
>
> THE POINT IS NOT THE STALE CITATION, IT IS THAT make lint PASSES. I ran the full nine-target lint on the rebased result and it reported PASS (7 modules, 56s) with this citation in place. So whatever audit repointed the citations in #84 does not cover this card, this file, or this shape -- which means the batch that PR just fixed can silently rot again, and there is no gate that would say so.
>
> Worth someone establishing which citations the audit actually covers before trusting a green lint as evidence that scenario cards still describe the code. A citation that names the wrong FILE is the easy case; if the audit misses that, it is unlikely to be catching subtler drift.
>
> Related: 2pm8 (seven scenario citations went stale and failed both citation audits) -- that one WAS caught, so the coverage gap is specific rather than total.

**Constraints specific to this task:** First establish, and record in your report, exactly which citation shapes the existing audit covers and why this one escaped (wrong card? wrong file type? wrong citation syntax?). Then close the gap so THIS shape fails the audit, fix the card, and confirm the audit passes. The deliverable includes the coverage answer, not just the two edits. Line numbers in the kata predate a5a931318, which shifted the file by four lines — re-locate, don't trust :148.

- [ ] **Step 1:** Reproduce: confirm the bad citation is present and the relevant lint/audit target passes with it in place.
- [ ] **Step 2:** Find the audit; establish which citations it covers and why this one escaped. Write that answer down for your report before changing anything.
- [ ] **Step 3:** TDD the audit gap: extend the audit (or its coverage list) so the wrong-file citation fails it. Run → red on the current card.
- [ ] **Step 4:** Fix the card: cite the true file/line/casing of the literal. Run the audit → green. Mutation: re-break the citation (wrong file), confirm red, restore.
- [ ] **Step 5:** Run the audit's selftest if it has one, plus the lint target that hosts it.
- [ ] **Step 6:** Commit by explicit path.

### Task 5: Kata qz3e — small test-quality cleanups from the perf-wave reviews

**Files:**
- Modify (locate each precisely): frontend `toolRowGrammar` / `linkifySummary` / `ToolCallItem.tsx` tests under `cmd/evener-hub/frontend/src/`; the make-selftest fixture under `scripts/`-adjacent test tooling; the liveeval fuzz oracle; `jm.openOutput` (jobstore/job-manager area); the delegate retry test using a 2s `time.After`.

**Interfaces:** none consumed from or produced for other tasks.

**PREMISE CORRECTION (controller, 2026-08-17):** the kata cites recorded rulings in `.superpowers/roborev-deferred-findings.md`. That file no longer exists anywhere (git-ignored scratch, since deleted). The item list below is the surviving record. Where the missing ruling's rationale would have mattered, use your judgment and record the reasoning in your report.

**Kata body (verbatim):**

> Batch of next-touch nits: (1) toolRowGrammar: add the failed+status combined summary-less accessible-name test (sound by construction, untested); (2) linkifySummary still anchors by substring indexOf — align to the positional-prefix idiom; (3) ToolCallItem.tsx: repeated arg re-parsing pattern, a documented-safe 'as string' cast, one overlong selector line; (4) make-selftest fixture: <10s promptness assertion framing comment, make_status=137 cosmetic reuse, ~4s margin note on the 6s-delay check; (5) liveeval fuzz oracle: consider a golden-value seed against lockstep doc+impl literal drift; (6) watch-restart tests: add a full-tool-path restart case if the watch-install contract ever changes shape. One kata, one sweep, one commit per area or one combined.

**Later additions (kata comments, verbatim):**

> Addition from the fuzz-oracle repair review: jm.openOutput field is now unreachable from production code (only test fixtures use it after the createOutput/openOutput split repairs) — remove or fold into the test seam explicitly.
>
> Absorbing 424w: add to this cleanup list — replace the 2s time.After in the delegate retry test with awaiting the durable finalize event (the await-not-wall-clock pattern applied across agent tests on 2026-08-07).

**Constraints specific to this task:** Item (6) is conditional on the watch-install contract having changed shape; verify whether it has, and if not, record "condition not met, no change" — that is a complete answer. Item (5) says "consider": decide, do or don't, and record why. Each area gets its own commit. If an item's premise is stale (code already fixed by later work), record that instead of forcing an edit.

- [ ] **Step 1:** Locate every item; for each, confirm the premise still holds at this branch (several are weeks old).
- [ ] **Step 2:** Work the items area by area, TDD where a behavior changes (new accessible-name test red first; idiom alignment covered by existing tests re-run; `jm.openOutput` removal proven by compile + package tests; time.After replacement proven by the retry test staying green with the await and a tightened absence of sleeps).
- [ ] **Step 3:** Mutation list for the new accessible-name test and any other new assertion.
- [ ] **Step 4:** Run each touched package's tests (frontend: the specific vitest files; Go: the touched packages).
- [ ] **Step 5:** One commit per area, by explicit path.

### Task 6: Excise/repair the worthless tests from the 2026-08-17 hunt (small findings only)

Added mid-run at Jesse's direction: hunt and excise tautological tests, tests asserting strings match strings, and other tests that cannot fail — coverage drop explicitly authorized. A read-only hunt of the whole repo produced six findings; the four small confident ones are this task. The cov_* cluster is kata 24te (out of scope here — its own disposition forbids wholesale deletion); the borderline AppWire source-grep guard keeps its KEEP-WITH-REASON (no change).

**Files:**
- Modify: `cmd/evener-hub/app_threadread_decode_fidelity_test.go`, `docs/testing.md:317-319`
- Modify: `cmd/evener-hub/frontend/src/panes/sessionPanels/index.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx`
- Modify: `cmd/evener-hub/spawn_test.go`

**Interfaces:** none consumed from or produced for other tasks.

**The findings (hunt report evidence, verified read-only at 9c6a6f1e4):**

**(a) EXCISE `TestDecodeTranscriptTurnLosesNoField`** (`cmd/evener-hub/app_threadread_decode_fidelity_test.go:25`). Both sides of its `reflect.DeepEqual(got, want.Turn)` are the identical expression — `json.Unmarshal(raw, &transcript.Entry{})` then `.Turn`; production `decodeTranscriptTurn` (`app_threadread.go:554-560`) IS the test's own `want` computation. The mirror type died in commit 9eda7a46e, whose message deleted the sibling `FuzzHubReplayCarryThrough` for exactly this reason and kept this test on grounds that no longer hold. `docs/testing.md:334-336` already mandates deletion ("Delete the round-trip test when the second path dies"). Excise the test plus its now-dead apparatus: `populatedTurnEntryJSON`, `divergentTurnFields`, `fillForJSON`. **KEEP `hubDecodedTurn`** (same file, line 45) — live consumers at `app_threadread_failed_turn_test.go:23,51` and `app_threadread_hook_turn_test.go:24,47`. Then fix `docs/testing.md:317-319`, which still holds this file up as the worked example of the good technique ("verified by adding a synthetic field...") — that verification predates the mirror type's death; the doc currently teaches the pattern with a file that decayed into the failure mode the same section warns about at :334.

**(b) EXCISE the tautological sessionPanels title test** (`cmd/evener-hub/frontend/src/panes/sessionPanels/index.test.ts:17`, "uses the same title derivation for fallback and renamed sessions"). Its expectations are routed through `sessionPanelTitle`, which is exactly what `descriptor.title` calls (`panes/sessionPanels/index.ts:32-33`) — both sides of every assertion are the same call; no mutation of `sessionPanelTitle` can fail it. Its only sliver of teeth (registration wiring) is a strict subset of the `test.each` block at :7-16, which pins the same ids against independent hardcoded literals. Delete the tautological test only; the `test.each` block stays.

**(c) REWRITE the `resolveRowKey` self-assertion** (`cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx:112`): `expect(resolveRowKey(undefined, undefined, "call_1")).toBe(resolveRowKey(undefined, undefined, "call_1"))` — identical call both sides. Replace with a pin against the independent literal `"call:call_1"`. Also add the missing one-line anti-collision case the production comment (`subagentModuleStore.ts:278-280`) claims but nothing tests: `expect(resolveRowKey("x", undefined, "f")).not.toBe(resolveRowKey(undefined, "x", "f"))`. The other three assertions in the test are sound; leave them.

**(d) REWRITE the determinism half of `TestResolveEvenerStateDirNotInRepoFallsBackToWorkDir`** (`cmd/evener-hub/spawn_test.go:1197`): `got`/`want` are two identical calls to `resolveEvenerStateDir(workDir, "")` (pure path derivation) — x == x. Drop the determinism pair, keep the collision half (it has teeth), and add an assertion on the actual promised shape: the returned path is under the resolved state home and carries the project id derived from workDir (`resolveEvenerStateDirWithProject` already returns the `identifier.Project`, so the oracle needs no new plumbing).

**Constraints specific to this task:** These edits delete assertions on purpose; Jesse has authorized the coverage drop. If a coverage floor file (`scripts/covunion-floors.txt`, test-coverage-floor or web-coverage-floor inputs) is violated by these deletions, lower the affected floor in the same commit with a one-line comment saying why — do not restore worthless assertions to satisfy a floor. Mutation-list contract applies to every REWRITTEN assertion (the new literal pins must be shown killable).

- [ ] **Step 1:** For each finding, re-verify the evidence at this branch's HEAD (the hunt read 9c6a6f1e4; confirm nothing moved).
- [ ] **Step 2:** Work findings (a)–(d); for (c) and (d), red-first where a new assertion is added (mutate production in scratch or temporarily to watch the new pin fail, then restore).
- [ ] **Step 3:** Run the touched tests: the two Go test files' packages narrowly, and the two vitest files individually.
- [ ] **Step 4:** Mutation list for every rewritten/added assertion; state which you actually ran.
- [ ] **Step 5:** One commit per finding or one per file, by explicit path — include the docs/testing.md fix in finding (a)'s commit.
