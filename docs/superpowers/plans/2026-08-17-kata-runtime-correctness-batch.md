# Kata Runtime-Correctness Batch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix five verified kata defects (96cp, g422, sc17, tbqd, fbmy) in one worktree, one commit series, one PR.

**Architecture:** Five independent fixes with no shared files: a session-meta persistence bug, a provider capability-detection rewiring, a CLI double-print dedupe, a Makefile delete-safety sweep, and a durable-store release-retry mirror of an existing acquire-side fix.

**Tech Stack:** Go (workspace root module `primeradiant.com/evener`), make, table-driven Go tests with fault injection.

**Spec:** The five kata bodies, quoted verbatim inside each task below. Kata refs: 96cp, g422, sc17, tbqd, fbmy in the `serf` kata project.

## Global Constraints

- Out-of-scope integration actions, listed up front per docs/conventions/agent-fleets.md: do NOT merge, push, rebase, cherry-pick, delete branches, or touch any branch other than the current worktree branch. Naming a base commit or branch anywhere in this plan is context, not permission.
- Forbidden git commands: `git stash`, `git reset`, tree-wide `git checkout`, `git clean`, `git add -A`, `git add .`. Stage explicit paths only. Commit after every task.
- The controller runs the full gates centrally. You run ONLY the tests you wrote or directly touched, plus that one package's test run. Never launch `make merge-approval-gate`, `make test`, or any full suite.
- TDD per superpowers:test-driven-development: the new test must fail against unfixed code for the stated reason before the fix, and pass after. Capture both outputs.
- Mutation evidence: for every test you add, record the exact one-line source change that makes it fail, and actually run it. Your final report includes a table: test name → mutation → ran? → observed result.
- Kata premise correction: if you find a task's premise already fixed or wrong in its specifics (it happened once in this batch already), STOP that task, report the evidence, and do not force the named change through.
- Smallest reasonable change; match surrounding style; comments say WHAT/WHY, never history.
- Never touch `node_modules`; never run `npm ci`.

---

### Task 1 (kata 96cp): Meta() erases is_subagent on every autosave of a resumed delegate

**Kata body (verbatim):**

> Session.Meta() computes the subagent flag from spawn config alone:
>
>     isSubagent := s.cfg.spawn.parentSessionID != ""     (agent/session_state.go)
>
> It does not consult s.restoredMetaIsSubagent (set in agent/session_init.go on the resume path). The codebase's real predicate is isSubagentSession() (agent/session_tools_ask.go):
>
>     s.cfg.spawn.parentSessionID != "" || s.restoredMetaIsSubagent
>
> whose doc comment exists precisely to warn about this: spawn is never persisted (json:"-"), so a bare 'serve --resume <delegate-id>' leaves cfg.spawn.parentSessionID empty.
>
> maybeAutoSave (agent/session.go) writes Meta() on every autosave. So resuming a delegate writes is_subagent:false back into its meta.json, and the flag is gone for every later reader and every subsequent resume. It is a one-way erasure of durable lineage.
>
> Impact today: anything keying on the persisted flag misclassifies a resumed delegate as a root session. serf-doctor sessions reads it; so would any future gate on "is this session someone's delegate".
>
> Found independently by two reviewers auditing a separate design. Neither reproduced it at runtime -- this is a code reading of Meta(), isSubagentSession() and maybeAutoSave. VERIFY BY REPRODUCTION FIRST: resume a delegate with 'serve --resume', let an autosave land, and diff meta.json. If a guard elsewhere prevents the rewrite, close this with that evidence.
>
> Fix direction: Meta() should use isSubagentSession(), not re-derive from spawn. Check the fork case too -- Meta() currently forces the flag false for forks, which may or may not be intended.

**Files:**
- Modify: `agent/session_state.go` (Meta(), around lines 104-164; the buggy line is `isSubagent := s.cfg.spawn.parentSessionID != ""` and the fork override `if s.fork.divergence > 0 { ... isSubagent = false }`)
- Test: `agent/session_state_test.go` or a new focused file in package `agent` (in-package test; the harness uses unexported fields)

**Investigation facts (verified against this tree at main@9c6a6f1e4):**
- `isSubagentSession()` is at `agent/session_tools_ask.go:47-48`; its doc comment's claim that "Session.Meta already keeps the two concepts apart" is stale — Meta() does not consult `restoredMetaIsSubagent`. If your fix makes that comment true again, leave it; if not, correct it.
- `restoredMetaIsSubagent` is set at `agent/session_init.go:813-818` (resume path), declared at `agent/session.go:399-407`. Set once before the session goes live, read lock-free.
- `maybeAutoSave` is at `agent/session.go:1493-1512`; it writes `s.Meta()` via `schema.SaveSessionMeta` from ~15 call sites.
- Fork children get meta written directly by `writeForkChild` (`agent/fork.go:311-326`) with IsSubagent zero-valued false, and a plain fork-resume leaves `cfg.spawn.parentSessionID` empty, so the documented invariant (docs/superpowers/specs/2026-07-03-ask-user-question-tool-design.md §7: "A forked root stays a root: fork lineage lives in meta.ParentSessionID with IsSubagent == false") holds with or without Meta()'s explicit fork override for the ordinary cases. The only case the override changes is a session that is simultaneously a live spawn descendant AND has fork.divergence > 0 — no doc or test covers that combination. Preserve the current fork-override behavior; note it in your report rather than changing it.
- Existing regression test to keep green: `TestForkSession_ChildLineagePreservedAcrossMetaRewrite` (`agent/fork_test.go:349-378`).

**Recommended failing-test shape (synthesized from `agent/session_origin_test.go`, `TestAskUser_RestoredSubagentStaysInvisible` in `agent/session_ask_test.go:191-220`, and the direct `maybeAutoSave()` call pattern in `agent/session_set_model_test.go:607-689`):**

```go
// TestResumedSubagentMetaSurvivesAutosave pins that a bare resume (empty
// RestoreSessionConfig, the `serve --resume <delegate-id>` shape) does not
// erase the persisted is_subagent flag on the next autosave.
func TestResumedSubagentMetaSurvivesAutosave(t *testing.T) {
	dir := t.TempDir()
	meta := schema.SessionMeta{
		ID:         "sess-resumed-delegate",
		ProfileID:  "openai",
		Model:      "gpt-5.2",
		IsSubagent: true,
		Config:     (SessionConfig{NoProjectPrompts: true, StateDir: dir}).toSnapshot(),
	}
	sess, err := RestoreSessionFromMetaWithConfig(t.Context(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := sess.Meta().IsSubagent; !got {
		t.Error("Meta().IsSubagent = false immediately after resume, want true")
	}
	sess.maybeAutoSave()
	saved, err := schema.LoadSessionMeta(dir, sess.ID())
	if err != nil {
		t.Fatalf("load autosaved meta: %v", err)
	}
	if !saved.IsSubagent {
		t.Error("autosave rewrote is_subagent to false for a resumed delegate")
	}
}
```

Adjust constructor/argument details to match what the neighboring tests in this package actually compile against — the three named model tests are the authority on the harness, not this sketch. Both assertions matter: the first pins Meta() directly, the second pins the end-to-end disk erasure the kata describes.

- [ ] **Step 1:** Write the failing test. Run it: `go test ./agent/ -run TestResumedSubagentMetaSurvivesAutosave -v`. Expected: FAIL on both assertions (Meta() returns false; autosaved meta has is_subagent false). If it PASSES, the premise is dead — stop and report per Global Constraints.
- [ ] **Step 2:** Fix Meta() to derive the flag from the same predicate isSubagentSession() uses, preserving the existing fork-override behavior. Run the test again. Expected: PASS.
- [ ] **Step 3:** Mutation check: revert your Meta() change (re-apply the old `isSubagent := s.cfg.spawn.parentSessionID != ""` line locally, without committing), confirm the test goes red, restore the fix. Record it.
- [ ] **Step 4:** Run the neighbors: `go test ./agent/ -run 'TestForkSession|TestAskUser|TestSessionOrigin|TestResumedSubagent' -v`. Expected: all PASS.
- [ ] **Step 5:** Commit `agent/session_state.go` and the test file by explicit path, message `fix(agent): Meta() keeps is_subagent for resumed delegates (kata 96cp)`.

---

### Task 2 (kata g422): rewire isClaude5OrNewer to a catalog flag

**Kata body (verbatim):**

> llm/providers/anthropic/request.go:250: the Claude-5+ capability detection (adaptive thinking only, no sampling params, thinking display:summarized) is 'keyed off the model ID locally for now; once the catalog grows a flag for this generation (e.g. thinking-always-on), this helper can be rewired to it'. String-prefix capability detection silently misses renamed/aliased models. Done: the model catalog carries an explicit generation/capability flag and isClaude5OrNewer reads it.
>
> [Jesse, priority comment:] Priority 3: latent capability-detection bug - a renamed or aliased Claude 5 model silently gets the wrong request shape (sampling params, thinking config). Nothing misfires today and the fix is small, so it is below the live defects.

**Files:**
- Modify: `llm/model_catalog.go` (ModelInfo struct, lines 17-48; overlay parse if the flag is Serf-authored), `llm/model_catalog_embedded.go` (`applyOverlayFields`, lines ~146-154), `llm/data/serf_model_catalog_overrides.json` (Claude 5 entries, lines ~91-205), `llm/providers/anthropic/request.go` (isClaude5OrNewer at 246-265 and the `claude5 :=` computation at line 35)
- Test: `llm/providers/anthropic/claude5_test.go`, `llm/model_catalog_test.go`

**Investigation facts (verified against this tree):**
- `buildRequestBody` already holds a full catalog entry `mi` (`request.go:135-144`, via `llm.EmbeddedModelCatalog().LookupModelInfo(apiModel)`) near the point where the three claude5 decisions apply: omit temperature/top_p (`request.go:65-69`), force adaptive thinking, add `thinking.display = "summarized"` (`request.go:146-157`). But note `claude5` is computed at line 35 and the sampling-param decisions at 65-69 run BEFORE the line-135 lookup — you will need the catalog entry earlier in the function.
- **Trap 1:** the comment's suggested flag `thinking-always-on` already exists as `ThinkingAlwaysOn` and means something DIFFERENT and narrower: `claude-sonnet-5` is explicitly `ThinkingAlwaysOn == false` (pinned by `TestEmbeddedModelCatalog_ClaudeSonnet5AndFable5`, `llm/model_catalog_test.go:284-286`) while `isClaude5OrNewer("claude-sonnet-5") == true`. Do NOT rewire to ThinkingAlwaysOn. A new flag is needed; name it for what it means in the domain (the Claude 5+ request generation/shape), not its history.
- **Trap 2:** the vendored upstream data (`llm/data/litellm_model_catalog.json`) carries `"supports_sampling_params": false` on the Claude 5 family — but ALSO on `claude-opus-4-7` and `claude-opus-4-8`, which `isClaude5OrNewer` deliberately returns false for today. `parseLiteLLMCatalog` never reads that key and no `SupportsSamplingParams` field exists on ModelInfo. Changing opus-4-7/4-8's request shape is OUT OF SCOPE for this kata (nothing misfires today; widening the set is a separate ruling). Recommended: a Serf-authored overlay flag on exactly the models the current helper matches, not a parse of the upstream key. Note the unconsumed upstream key in your report so it is on the record.
- Which entries need the flag: overrides entries exist for `claude-sonnet-5` and `claude-fable-5`; `claude-opus-5` exists only in the base catalog (you may add an overrides entry for it). Dated variants (`claude-sonnet-5-20260901` etc.) resolve through `LookupModelInfo`'s family-ID fallback (`llm/model_catalog.go:114-143`) — verify with a test rather than assuming.
- Fallback semantics: models with no catalog entry at all (e.g. hypothetical `claude-fable-6`) currently get claude5=true via generation parsing, and `TestIsClaude5OrNewer` (`llm/providers/anthropic/claude5_test.go:15-36`) pins `claude-fable-6 → true`. Keep the generation parse as the fallback when the catalog has no entry for the model, so an unknown-but-newer model keeps the safe request shape; the catalog flag takes over whenever an entry resolves. That is what "rewired to it" can mean without regressing the pinned future-family behavior. If you find evidence this fallback is wrong, stop and report rather than deleting the pinned cases.
- Existing request-shape tests that must stay green as-is: `TestBuildRequestBody_Claude5_AdaptiveWithDisplay`, `TestBuildRequestBody_Claude5_NoEffort_StillAdaptiveDisplay`, `TestBuildRequestBody_OlderAdaptiveModels_NoDisplayField` (opus-4-6/sonnet-4-6 byte-identical contract), `TestBuildRequestBody_Claude5_OmitsSamplingParams`, `TestBuildRequestBody_Claude5_1MSuffixStripped` (all in `llm/providers/anthropic/claude5_test.go`).
- **Fuzz oracle note:** `FuzzApplyCatalogOverrides` (`llm/model_catalog_fuzz_test.go:78-81`) checks merge determinism over an explicit field tuple `(ID, ContextWindow, ThinkingAlwaysOn, level-count)`. If you add an overlay field, add it to that tuple.

- [ ] **Step 1:** Write the failing test first: a catalog test asserting the new flag is set for `claude-sonnet-5`, `claude-fable-5`, `claude-opus-5` and unset for `claude-opus-4-6`, `claude-opus-4-7`, `claude-sonnet-4-5` (model it on `TestApplyOverrides_ThinkingAlwaysOn`, `llm/model_catalog_test.go:868-884`); plus a request-shape test asserting an ALIAS or catalog-resolved ID that the prefix parser alone would misclassify gets the claude5 shape. Run: `go test ./llm/ -run <newname> -v` and `go test ./llm/providers/anthropic/ -run <newname> -v`. Expected: FAIL (flag/field does not exist yet — likely a compile failure first; that counts, record it).
- [ ] **Step 2:** Add the ModelInfo field, overlay parse, overrides entries, and rewire the claude5 computation in buildRequestBody to consult the catalog entry with generation-parse fallback. Run the new tests. Expected: PASS.
- [ ] **Step 3:** Run the full affected packages: `go test ./llm/... -count=1`. Expected: all PASS, including the untouched claude5_test.go table and both fuzz seeds.
- [ ] **Step 4:** Mutation checks, run each: (a) remove the flag from claude-sonnet-5's overrides entry → the new catalog test must fail; (b) make the rewired computation ignore the catalog and return only the prefix parse → the new alias/resolved-ID request-shape test must fail. Restore.
- [ ] **Step 5:** Commit the four modified files plus tests by explicit path, message `feat(llm): Claude 5 generation flag in catalog; anthropic reads it (kata g422)`.

---

### Task 3 (kata sc17): non-interactive CLI prints the answer twice

**Kata body (verbatim):**

> cmd/evener/run.go's event printer has two independent print sites for the same content:
>
>   EventAssistantTextEnd -> fmt.Fprintf(w, "[assistant] %s\n", d.Text)
>   EventCommunicate      -> fmt.Fprintf(w, "[communicate:end_turn] %s\n", d.Message)
>
> When a model streams assistant text and then calls communicate with the same (or overlapping) content, both fire and the user sees the answer twice.
>
> The appwire projector already solves this: matchesLastAssistantMessage (internal/appprojector/appwire_projection.go) exists specifically to suppress the duplicate on that surface. The CLI printer has no equivalent.
>
> Pre-existing and independent of the tool_choice work; found while auditing which surfaces actually display bare assistant text. Worth confirming how often a model does both in practice before deciding whether to port the projector's dedupe or leave it.
>
> Fix direction: reuse the projector's comparison rather than writing a second one, so the two surfaces cannot drift on what counts as a duplicate.

**Files:**
- Modify: `cmd/evener/run.go` (`drainEventsHuman`, lines 349-438: EventAssistantTextEnd case at 363-380, EventCommunicate case at 401-408), plus wherever the shared comparison ends up living (see below)
- Possibly modify: `internal/appprojector/appwire_projection.go` (matchesLastAssistantMessage, 1865-1869; recordAssistantMessage, 1856-1863), `internal/apptranscript/apptranscript.go` (the mirrored inline dedupe, 408-414 and the comparison at 472)
- Test: `cmd/evener/run_test.go` (model on `TestDrainEventsHuman`, line 504, driven by a `testEvents()`-style slice)

**Investigation facts (verified against this tree):**
- `drainEventsHuman` is stateless today; events carry no turn ID (`events.SessionEvent` has none, nor do the two payloads). The projector's dedupe is turn-scoped via its own `activeTurnID`; the CLI cannot be. "Last printed assistant text" tracked inside the drain loop is the achievable equivalent — same TrimSpace-equality semantics, scoped to most-recent-assistant-text, which for this stream (assistant text immediately followed by its communicate echo) is the same practical contract.
- The comparison is three unexported members on the stateful `*AppEventProjector`; `cmd/evener` cannot call them. There is ALREADY a second hand-rolled copy: `internal/apptranscript/apptranscript.go:410-414` mirrors the dedupe with a comment pointing at matchesLastAssistantMessage, and `TestProjectTurnDedupsCommunicateEcho` (`internal/apptranscript/apptranscript_test.go:1064-1069`) pins it. So the drift the kata warns about has already happened once. Import direction: `appprojector` imports `apptranscript` (leaf); the reverse would cycle. `cmd/evener` may import either (same module, `primeradiant.com/evener/internal/...` is visible to `cmd/evener`; `cmd/evener-hub` already imports appprojector).
- The right home for one shared, exported, pure comparison is therefore `internal/apptranscript` (the leaf all three surfaces can reach). Rewire all three call sites — projector, apptranscript's inline copy, and the new run.go use — through it, so there is exactly one definition of "counts as a duplicate". Keep the projector's turn-ID scoping where it is (that part is projector state, not comparison semantics).
- Existing tests that must stay green: `TestDrainEventsHuman` (`cmd/evener/run_test.go:504`), `TestDrainEventsHuman_CommunicateAndSkillActivated` (`cmd/evener/main_test.go:795` — two communicates with DIFFERENT text from any assistant text; must still both print), `TestProjectTurnDedupsCommunicateEcho`, and the appprojector communicate tests (`TestAppEventProjectorProjectsCommunicateAsAssistantMessage`, `TestAppEventProjectorSuppressesCommunicateToolEvents`).
- Semantics to preserve exactly (from the projector): compare `strings.TrimSpace(text)` for equality; empty communicate messages already print nothing meaningful — mirror the projector's early-return on empty. A communicate with different text still prints. A `[communicate]` (EndTurn=false) with matching text: the projector's dedupe does not distinguish EndTurn; match its behavior and pin whichever way it lands in the test with a comment.

- [ ] **Step 1:** Write the failing test in `cmd/evener/run_test.go`: feed `drainEventsHuman` an `EventAssistantTextEnd{Text: "the answer"}` followed by `EventCommunicate{Message: "the answer", EndTurn: true}`, assert the output contains `the answer` exactly once and still contains the `[usage]` line; add a companion case where the communicate text differs and both lines must appear. Run: `go test ./cmd/evener/ -run TestDrainEventsHuman -v`. Expected: FAIL (answer printed twice).
- [ ] **Step 2:** Extract the shared comparison into `internal/apptranscript` (exported, pure, with a doc comment naming all three consumers), rewire appprojector's matchesLastAssistantMessage text comparison and apptranscript's line-472 inline comparison through it, and add last-assistant-text tracking + suppression to `drainEventsHuman`. Run the new test. Expected: PASS.
- [ ] **Step 3:** Run all four affected packages: `go test ./cmd/evener/ ./internal/appprojector/ ./internal/apptranscript/ -count=1`. Expected: PASS.
- [ ] **Step 4:** Mutation checks, run each: (a) make the shared comparison always return false → your new dedupe test fails; (b) make it always return true → `TestDrainEventsHuman_CommunicateAndSkillActivated` and/or your different-text case fails. Restore.
- [ ] **Step 5:** Commit the touched files by explicit path, message `fix(cli): suppress communicate echo of assistant text in run printer (kata sc17)`.

---

### Task 4 (kata tbqd): Makefile variable-fed recursive deletes

**Kata body (verbatim):**

> Surfaced by the independent review of the estate-gutting commit: Makefile lines 77, 121, 163 feed variables ($$dir, $(EVENER_DIST_BIN_DIR)) to rm -rf. TestNoScriptFeedsVariableToRecursiveDelete and the docs/testing.md rule are scoped to scripts/*.sh, so these are the nearest unswept weapons if the rule is to hold repo-wide. Work: either convert the recipes to safe shapes (mkdir-owned paths, guard through a script that uses scratch-lib) or extend the audit to Makefile recipe lines with the same count-pinned list. Related katas: jvpe (the audit), yns5 (the estate).

**Files:**
- Read first: the `TestNoScriptFeedsVariableToRecursiveDelete` audit test (locate it by grep; it is the mechanism this kata extends or satisfies), the docs/testing.md rule it enforces, and Makefile lines 60-180 for the three recipes' context.
- Modify: `Makefile` and/or the audit test file, per the option you choose with evidence.

**Verified premise:** the three sites exist exactly as described at Makefile:77 (`rm -rf "$$dir" || finish_status=1`), :121 (`rm -rf "$$dir" || { finish_status=1; ... }`), :163 (`rm -rf "$(EVENER_DIST_BIN_DIR)" "$(EVENER_DIST_ARCHIVE)"`).

**Choosing between the kata's two options:** read the audit test and docs/testing.md rule first, then pick the option that holds the rule repo-wide with the smallest honest change. Two katas' worth of history says a named mechanism can be wrong — if the audit's shape does not extend cleanly to Makefile recipe text, converting the recipes to safe shapes is equally acceptable. Whichever you choose, the stop condition is: **a variable-fed recursive delete added to the Makefile after your change is either impossible (the recipe shape doesn't take variables) or caught by a test.** A conversion that leaves the next contributor free to add a fourth unswept `rm -rf "$$var"` does not meet it.

Also honor Makefile:232's existing comment ("them can be made to delete the checkout (kata 5hs2)") — there is prior art in this file for why these shapes are dangerous; read it before editing.

- [ ] **Step 1:** Read the audit test, the docs/testing.md rule, and the three recipes with full context. Decide the mechanism and record why in one paragraph (it goes in your report and the commit message).
- [ ] **Step 2:** TDD the guard: if extending the audit — write the Makefile-scanning assertion first with the count pinned to the CURRENT three sites, watch it pass, then verify it catches a planted fourth site (add a scratch `rm -rf "$$x"` line locally, watch it fail, remove it — that run is your mutation evidence). If converting recipes — write/extend the audit so the count pins to zero first (fails on the three live sites), then convert until green.
- [ ] **Step 3:** Verify the three recipes still work: run the specific make targets the recipes belong to, or the narrowest exercise of them that exists (`make -n <target>` for shape plus the target's own selftest if one exists). Record exactly what you ran. Do NOT run full gates.
- [ ] **Step 4:** Run the audit test package: `go test ./ -run <AuditTestName> -count=1` (adjust to the test's actual location). Expected: PASS.
- [ ] **Step 5:** Commit by explicit path, message `fix(build): sweep Makefile variable-fed recursive deletes into the delete-safety rule (kata tbqd)`.

---

### Task 5 (kata fbmy): releaseRunningTurnID storage-failure recovery

**Kata body (verbatim):**

> Raised by the worker that fixed `ajg5`, which deliberately did not expand into it.
>
> `ajg5` fixed the *acquire* half of the running-turn-name contract: `mintRunningTurnID` now returns a `turnNameRefusal`, so the notification stand-down can distinguish "someone holds the name" from "the store would not take the write", and retries the second rather than spinning on it.
>
> `releaseRunningTurnID` has the same storage-failure exposure and was left alone.
>
> ## The defect
>
> A failed release leaves `ActiveTurnID` naming a turn that is already dead. Nothing subsequently clears it, so every later `turn/start` on that session is refused against a name whose turn no longer exists. The session is wedged until the daemon restarts.
>
> ## Why it is now sharper, not softer
>
> The two halves interlock. A failed release is exactly what *creates* the ownerless name that `ajg5`'s fix deliberately refuses to wait on — the fix retries a refused *write*, and warn-and-stops on a genuinely stale name. That is the right call in isolation, and it means a session wedged this way stays wedged by design.
>
> So `ajg5` did not make this worse, but it did make it the remaining hole: the acquire path now recovers from a failing store and the release path still does not, and the release path is the one that strands.
>
> ## Fix direction
>
> Give release the same treatment as acquire: a refusal type that separates "the store would not take the write" from ordinary contention, and a paced retry for the former. Then decide what should happen when the retry budget is exhausted — a name that can never be released is a session that can never start a turn again, and failing loudly beats wedging silently.
>
> ## Acceptance
>
> A test with a store that fails writes during release, proving a later `turn/start` on that session still succeeds. It must fail against today's code — a test that passes because the release happened to succeed proves nothing. See `docs/testing.md` on false-green traps; the `ajg5` fix's own control test is a good model, since it was verified to pass *before* the fix and to go red under a deliberate over-application.

**Files:**
- Modify: `agent/session_active_turn.go` (releaseRunningTurnID at 222-240; the ajg5 machinery to mirror is in the same file at 114-196), `agent/session.go` (retry-state fields near 589-595 and delay constants near 624-636), possibly `agent/session_lifecycle.go` (the single production call site, the defer at 1038 inside processOneInput)
- Test: `agent/session_active_turn_test.go` and/or a new in-package test file; harness pattern is `newStandDownHarness` + `failWrites` in `agent/session_notification_standdown_test.go:37-141`

**Investigation facts (verified against this tree):**
- `releaseRunningTurnID` returns nothing; on `mutate` error it emits one un-latched EventWarning and gives up. The durable `ActiveTurnID` stays set. The ONLY thing that clears a dead ActiveTurnID today is daemon restart (`forgetRunningTurnNoOneOwns`, `agent/session_client_mutation_persist.go:61,74-84`, runs at load only).
- Downstream refusals a stuck name causes: `AcceptClientMutationStart` rejects with `Conflict("turn is already active")` (`agent/session_client_mutation.go:258-261`); `mintRunningTurnID` returns turnNameHeld (`session_active_turn.go:95-98`).
- The acquire-side pattern to mirror: `turnNameRefusal` type (`session_active_turn.go:38-66`), `warnStoreUnhealthyOnce`/`clearStoreUnhealthyWarning` latch, `scheduleRunningTurnNameRetry(ceiling)` with generation-counted coalescing on `s.sclock().AfterFunc`, and the consumer switch at `agent/session_lifecycle.go:1104-1113` (storage failure → `turnNameStoreRetryMaxDelay` = 5min ceiling; contention → `jobNotificationRetryMaxDelay`; genuinely stale → warn once, no retry).
- **Retry state must be separate from mint's:** mint and release can be failing independently; the single `turnNameRetryMu`-guarded `turnNameRetry`/`turnNameStoreUnhealthy` set cannot represent both at once. Give release its own fields (same shapes, same file regions) unless you can prove sharing is race-free — in which case prove it in the report.
- **The retry must re-attempt the WRITE, not just notify():** mint's retry works by re-waking the serve loop, which re-enters mint. Nothing re-enters release — it runs once, in a defer, per turn (`session_lifecycle.go:1033-1038`). A paced AfterFunc that retries the release write itself (bounded, backing off, loud on exhaustion) is the shape that fits; wire whatever refusal/result release now returns so the defer can arm it.
- **Control test that must stay green:** `TestNotificationStandDownStillRefusesToWaitOnAnOwnerlessName` (`agent/session_notification_standdown_test.go:227-258`). It seeds exactly the state a failed release leaves behind and asserts warn-once-no-retry from the STAND-DOWN's point of view. Your release-side retry is different machinery — it is armed by the failed release itself, knows its own write failed, and is therefore allowed to retry where the stand-down must not. If your change makes this test fail, the design is wrong; stop and reconsider rather than editing the test.
- Fault injection: `s.clientMutations.faults.BeforeEffectSnapshotRename = func() error { return injected }` (`failWrites`, `session_notification_standdown_test.go:134-141`) — set after `ensureClientMutationStore()`, fails every durable effect write until cleared.
- Decision-record constraints (docs/superpowers/plans/2026-08-16-one-active-turn-identity.md): "ActiveTurnID means the turn that is running, not the turn a client mutation reserved" (Jesse, 2026-08-16); "A running turn is not durable state … reconciled at load, never inherited"; `session_active_turn.go` exists to keep mint/release in one file — keep the new machinery there. Appwire v3 constraint: control is session-scoped; do not reintroduce any turn-scoped precondition — release operates purely on ActiveTurnID inside clientMutationSnapshot, as mint does. docs/job-control.md is NOT the contract for this area (verified: zero mentions).

**Acceptance test shape (must fail against today's code):** using the standDownHarness pattern — mint a name (or drive a turn to where release fires), arm `failWrites`, let release fail, clear the fault, advance the fake clock through the retry delay, then prove `mintRunningTurnID` (or AcceptClientMutationStart) succeeds WITHOUT a store reload. Against today's code the name is still held and the mint refuses — that is the red you must capture. Also pin the exhaustion path: with the fault never cleared, the retry budget ends in a loud warning (assert via the harness's warning capture), not silence and not an unbounded timer.

- [ ] **Step 1:** Write the recovery test (fault → release fails → fault cleared → clock advanced → next mint/start succeeds). Run: `go test ./agent/ -run <newname> -v`. Expected: FAIL — the next mint refuses because ActiveTurnID is still held. Capture the output.
- [ ] **Step 2:** Write the exhaustion test (fault held; retries back off; budget exhausts; exactly one loud terminal warning; timers stop). Expected today: FAIL (no retry exists to exhaust).
- [ ] **Step 3:** Implement: release returns a refusal/result distinguishing store-failure from not-mine/no-op; failed store writes arm a paced, generation-counted, bounded retry of the release write on the session clock; success clears state; exhaustion warns loudly once and stops. Run both new tests. Expected: PASS.
- [ ] **Step 4:** Run the guard set: `go test ./agent/ -run 'TestNotificationStandDown|ActiveTurn|RunningTurn' -count=1 -v`. Expected: all PASS, especially the ownerless-name control test, untouched.
- [ ] **Step 5:** Mutation checks, run each: (a) make the retry AfterFunc a no-op → recovery test fails; (b) make release swallow the store error as success → recovery test fails (name still held) or exhaustion test fails (no warning); (c) delete the exhaustion warning → exhaustion test fails. Restore.
- [ ] **Step 6:** Commit by explicit path, message `fix(agent): releaseRunningTurnID retries a refused store write instead of wedging the session (kata fbmy)`.

---

## Controller gate (not an implementer task)

After all five tasks land: controller runs `make merge-approval-gate` centrally in this worktree, reads each task's mutation table, spot-reads the diffs, then handles kata closes and the PR. Implementers do none of that.
