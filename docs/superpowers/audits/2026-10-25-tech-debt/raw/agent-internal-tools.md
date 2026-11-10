# Tech-debt audit: agent-internal-tools

Scope: agent/internal/tool, tool/repair, hooks, mcp, mcphttp, worktree, cheapmodel, modelavailability, installid, globpattern, linecap, runetrim, clock, liveeval, toolname, sysmsg, lineend, frontmatter, agenttest.

## Overview and inventory

Lines of Go (all files / test files), measured with `wc -l`:

| Package | Files | Total LOC | Test LOC |
|---|---|---|---|
| tool | 25 | 11270 | 7368 |
| tool/repair | 20 | 10875 | 6569 |
| hooks | 17 | 4363 | 3224 |
| mcp | 19 | 4664 | 3819 |
| mcphttp | 4 | 797 | 627 |
| worktree | 23 | 5634 | 3967 |
| cheapmodel | 4 | 1549 | 1152 |
| modelavailability | 3 | 824 | 411 |
| installid | 9 | 1146 | 789 |
| globpattern | 5 | 759 | 512 |
| linecap | 4 | 594 | 386 |
| runetrim | 3 | 282 | 224 |
| clock | 3 | 261 | 186 |
| liveeval | 3 | 274 | 224 |
| toolname | 3 | 120 | 71 |
| sysmsg | 1 | 33 | 0 |
| lineend | 2 | 31 | 19 |
| frontmatter | 5 | 601 | 515 |
| agenttest | 15 | 2130 | 1268 |

Roughly 46k LOC, of which about 30k is tests. Largest production files: `tool/repair/explain.go` 3638, `tool/definitions.go` 1389, `tool/registry.go` 1200, `mcp/manager.go` 845, `hooks/hooks.go` 786, `tool/apply_patch.go` 671, `tool/breaker.go` 597, `worktree/lockstate.go` 537.

Overall health: the small leaf packages (sysmsg, lineend, toolname, clock, mcphttp, cheapmodel, runetrim) are tidy and do what they say. The debt concentrates in three places: (1) `tool/repair/explain.go`, a 3638-line hand-rolled JSON-Schema evaluator and example synthesizer that is the clearest runaway-LLM-accretion hotspot; (2) a pervasive pattern of "test seam" production API (a `Foo` that only tests call next to the real `FooWithX`), plus about 4.2k lines of coverage-chasing test files; (3) a long tail of small helpers (bounded line readers, flock, rune truncation, deep clone, jsonschema compile) that this slice either duplicates or fails to be the single home for, even where a shared package was created for exactly that purpose.

Product-model note: nothing here conflicts with recovery-over-lockdown. `repair` is already a recovery feature (heal the call instead of rejecting it); findings 1 and 2 shrink the coaching layer, not the healing layer, and need Jesse's call because they change model-facing error text.

---

## Findings

### agent-internal-tools-1: explain.go is a 3.6k-line mini JSON-Schema engine for error coaching (high)

Locations: `agent/internal/tool/repair/explain.go:55` (ExplainSchemaError), `:998-1600` (combinator/branch prose), `:1836-2350` (nested oneOf), `:2446-3358` (candidate satisfaction, numeric example synthesis), `:3359-3638`.

Evidence:
- 3638 lines, 145 funcs; 1020 comment lines; 88 comment lines cite `issue #NNN` or "review". 24 of its 27 commits are `fix(repair): ... (#NNN review)`, so it is a churn hotspot: `9910b28e43 fix(repair): decline a nested scalar whose property schema has an applicator (#4099)`, `38aacfd1e4 ... (#622 review)`, `d7a8cb2756 ... (#622)`.
- It re-implements JSON-Schema semantics already provided by `santhosh-tekuri/jsonschema`, which the registry compiles for every tool (`tool/registry.go:1115`). Examples: `candidateSatisfies` (2526), `candidateMatchesSchema` (2545), `schemaOwnKeywordsMatch` (2575), `numericConstraintsExact` (2709), `objectCandidateSatisfies` (2746), `arrayCandidateSatisfies` (2812), `rationalMultiple` (3064), `snapMultiple`/`integerMultipleWithin`/`decimalRatio`/`gcdInt`/`ceilDiv`/`floorDiv` (3198-3300), `bindingLower`/`bindingUpper` (3311-3350). That is about 900 lines (2446-3358) whose only purpose is deciding whether a synthesized "Example:" value would be accepted by the schema.
- Three parallel example generators: `exampleObject`/`exampleValue` (1714-1750), `exampleValueFor` (2347), `exampleValueForAny` (3538), plus `scopedExampleValue` (3378) and `numericExample` (3122).
- Defensive posture is "decline and print a generic message" everywhere (`explain.go:96-110`: "arguments did not match the schema."), so the large machinery often yields no better output than the generic fallback.
- Four near-duplicate "required names" helpers: `requiredNames` (903), `requiredList` (1682), `describeRequired` (1572), `effectiveRequired` (3520), and `repair.go:374 missingRequired`.

Impact: large maintenance drag and the single biggest risk in the slice. Every new schema keyword requires teaching a second evaluator; each fix ships a regression test pinned to prose. The output only reaches a model after a tool call already failed.

Fix plan (needs Jesse's decision first, because it changes model-facing coaching text):
1. Characterize with behavior tests what must survive: the failing field is named, the constraint keyword and limit are named, a valid minimal example is given when one is cheap. Drop assertions that pin wording (see finding 6).
2. Change `ExplainSchemaError` to take a `validate func(candidate any) bool` (the registry's compiled schema, already in hand at `agent/session_tool_repair.go:209`) and use it to check any synthesized example by actually validating the whole example object. Delete the candidate-satisfaction family (2446-3358) and the numeric snapping code.
3. Keep: the top-level path walk (`resolveSchemaErrorContainer`), `constraintMessage` for maxLength/minLength/enum/min/maxItems, `branchRequirement` for one-of arms, `minimalExample` for top-level required fields. Collapse the four required-name helpers into one.
4. Remove nested oneOf and `$ref`/`not` special prose; fall back to the generic message plus the validator's own cause string.
5. Update `docs/product/subsystems.md` row for tool repair if it lists coaching behaviors.

Size: remove about 2000-2400 production lines and about 4000 test lines; add about 40 lines.
Risk: medium. Model-facing coaching quality could regress for exotic schemas; mitigate by running the recorded tool-fluency corpora in CI and keeping the generic fallback.

### agent-internal-tools-2: repair.go null handling re-evaluates anyOf/oneOf/allOf/not itself (medium)

Locations: `agent/internal/tool/repair/repair.go:216-355` (`dropEmptyOptionals`, `emptyMeansAbsent`, `schemaAcceptsNull`, `branchAcceptsNull`, `definitelyAcceptsNull`), `explain.go:2575` (`schemaOwnKeywordsMatch`, whose doc says "The repair package's null handling reads a schema's own keywords with it and then evaluates anyOf/oneOf/allOf/not itself").

Evidence: `schemaAcceptsNull` comment: "The package resolves no $ref, so a property behind one counts as accepting null". The check is a third schema evaluator beside finding 1's and the compiled `jsonschema.Schema`.

Impact: drift between what repair thinks the schema accepts and what validation actually accepts; any new keyword needs two edits.

Fix plan: give `RepairArgs` an optional per-property "accepts null" probe backed by the compiled schema (compile a tiny wrapper, or validate `nil` against the property sub-schema via `jsonschema`), delete `schemaAcceptsNull`/`branchAcceptsNull`/`definitelyAcceptsNull` and the null path of `schemaOwnKeywordsMatch`. Keep `repair` stdlib-only by passing the probe as a func. Do after finding 1, since both lean on `schemaOwnKeywordsMatch`.

Size: about -120 production, -200 test. Risk: low-medium (existing `repair_test.go` null-drop cases guard behavior; run them, do not rewrite them).

### agent-internal-tools-3: "test seam" production API with no production caller (medium)

Locations and evidence (non-test caller counts from grep):
- `hooks/command_runtime.go:169 executeCommandHook`: 0 production callers (only `hooks.go:474` uses `executeCommandHookWithRuntime`); variadic `wrapper ...*sandbox.Wrapper` is used as an optional arg (`:173-180`).
- `hooks/hooks.go:188 Runner.Summary()`: 0 production callers (production uses `SupportedSummary`, `agent/status.go:290`).
- `hooks/hooks.go:718 Runner.RunSessionStart`: 0 production callers (production uses `RunSessionStartFor`, `agent/session_init.go:2298`).
- `mcp/manager.go:185 NewManagerWithClock`: 0 production callers, 3 test callers.
- `mcp/manager.go:785 transportForConfig` and `:820 mergeEnv`: 0 production callers (6 and 5 test callers); real code uses the `...WithEnv`/`mergeEnvInto` forms.
- `tool/registry.go:639 Registry.Unregister`: 0 production callers, byte-identical to `Remove` (`:599`); `Registry.Restrict` (`:579`): 0 production callers (only `RestrictKeepingResultTool`, 2 callers).
- `tool/apply_patch.go:18 ApplyPatch`: only `ApplyPatchCtx` is called (`agent/session_tools_shell.go:336`).
- `tool/registry.go:948 truncateChars`, `:987 truncateHeadCount`, `:1041 truncateLines`: wrappers that only drop the `bool` of the `...WithStatus` function; only tests call them (5, 4, 3 test uses).
- `tool/registry.go:416 ParseDocumentResult(_ string, readFileOutput string)`: unused first parameter.
- `tool/registry.go:1115 compileSchemaWith`: its injectable closures exist only so `package_union_fuzz_test.go:92-93` can force an error and a panic.

Impact: every wrapper doubles the surface a reader must trace, and tests pin the wrapper instead of the production path.

Fix plan: delete each wrapper; point tests at the real function (or rename the real one to the short name). For `hooks`, make `executeCommandHook` the single entry with a required `*sandbox.Wrapper` arg and drop the variadic. For `compileSchemaWith`, test the panic recovery through a malformed real schema instead of closures. Behavior-preserving; compile errors list every call site.

Size: about -150 production, about 60 test lines retargeted. Risk: low.

### agent-internal-tools-4: `Def<X>` / `Def<X>Named` / `DefWith...` constructor chains (medium)

Locations: `tool/definitions.go:236-253` (DefDelegate -> DefDelegateWithSandbox -> DefDelegateWithResultTool), `:384/390` (DefDelegateSend/Named), `:433/439` (DefJobWatch/Named), `:519/525` (DefJobList/Named), `:1198/1204` (DefAskUser/Named), `:726/730` (DefCommunicate/Named), `:139/166` (DefShell/DefShellWithDetach).

Evidence: each plain form is a one-line call passing `DefaultResultToolName`. `DefAskUser` has 0 production callers (6 test callers). `DefDelegateWithSandbox` has 0 callers outside the chain and tests. Production callers use the plain forms from `agent/provider/profile.go:181-206` and the Named forms from the session.

Impact: three names for one constructor drift (each Named variant restates its doc comment as "X for a session whose result tool is named resultTool").

Fix plan: keep only the fully parameterized constructor per tool; have `profile.go` pass `tool.DefaultResultToolName` explicitly. Update the ~60 test call sites mechanically. No behavior change; definitions tests (`definitions_test.go`) guard output.

Size: about -60 production, about 60 call-site edits. Risk: low.

### agent-internal-tools-5: 4.2k lines of coverage-chasing test files with stale line-number comments (medium)

Locations: `tool/breaker_covtest_test.go`, `tool/cov_s4_tool_test.go` (482), `tool/cov_w2tail_apply_patch_test.go` (107), `tool/cov_w3sub_apply_patch_test.go` (134), `tool/tool_covtest_test.go` (163), `tool/repair/explain_covtest_test.go` (672), `hooks/cov_s5_hooks_test.go`, `hooks/hooks_covtest_test.go`, `mcp/cov_*_test.go` (10 files), `globpattern/expand_covtest_test.go`, `installid/*covtest_test.go`, `agenttest/*cov*_test.go`.

Evidence: `wc -l` over those globs gives 4243 lines. `explain_covtest_test.go` has 33 comments of the form "(line 265)" / "(lines 120-127)" that name source lines of a file that has since changed (e.g. `explain_covtest_test.go:255-259`). Names like `cov_w2tail`, `cov_w3sub`, `cov_s4`, `TestW2Tail_...` encode a past work-batch, not the behavior under test. Many pin one internal helper (`TestArrayIndex`, `TestCovNormalizeAskUserArgs`) rather than a behavior.

Impact: tests are organized by when they were written; reviewers cannot find the test for a behavior; line-number comments mislead; failing messages restate source branches.

Fix plan: for each file, move cases into the test file named after the unit (`hooks_test.go`, `manager_test.go`, `apply_patch_test.go`), drop comments that cite line numbers or batch tags, delete cases that only exist to execute a branch with no distinct behavior assertion. Do not reduce meaningful coverage (Jesse's rule): confirm with `make` coverage tooling in CI before and after. Do it per package in separate PRs.

Size: about 4200 lines moved, roughly 800-1200 deleted. Risk: low (mechanical), but needs CI coverage comparison.

### agent-internal-tools-6: tests assert on human-readable copy (medium)

Locations: `tool/repair/explain_oneof_test.go:70-96` (`strings.Contains(msg, "is not one of the allowed values")`, `"oneOf constraint"`, `"arguments did not match the schema"`), `tool/repair/explain_test.go:27-71` (`"Required arguments:"`, `"Example:"`), `tool/repair/explain_covtest_test.go:15-35` (`want := \`my_tool: argument "name" is below minLength (5). Value "hi" is 2 characters.\``).

Evidence: grep counts of `strings.Contains(` in tests: tool 145, tool/repair 267, hooks 66, mcp 21; AGENTS.md and `docs/developing-evener/testing.md` ("Copy Is Not a Test Oracle") forbid asserting on hardcoded phrasing.

Impact: every wording tweak to coaching breaks tests; this is a major reason finding 1's code is hard to change.

Fix plan: assert structure, not prose. Have the explain functions return a small struct (field, keyword, limit, allowedValues, example JSON) that tests assert on, and render prose from it in one place; or test that the example round-trips through the compiled schema (the behavior: "the suggested example is valid"). Do this together with finding 1.

Size: about 1500 test lines rewritten. Risk: medium (needs care to keep the intended invariants, e.g. "does not claim a supplied argument is required").

### agent-internal-tools-7: "package union" fuzz targets do not fuzz (low)

Locations: `tool/package_union_fuzz_test.go:20-24` (`f.Fuzz(func(t *testing.T, _ uint8) { toolPackageApplyEdges(t); toolPackageRegistryEdges(t) })`), same file shape in `jobstore/package_union_fuzz_test.go` and `sessionlog/package_union_fuzz_test.go`.

Evidence: the fuzzed argument is discarded; the body is a fixed list of helper calls whose results are `_ =` discarded (e.g. `:34-37 _ = hintFromHunk(...)`), i.e. branch-poking for coverage with no assertion.

Impact: gives the appearance of fuzz coverage; asserts nothing.

Fix plan: delete the three files, or turn each case into an ordinary test with an assertion. Covered by finding 5's branch review.

Size: about -300 lines. Risk: low.

### agent-internal-tools-8: bounded newline-reader is implemented five times (medium)

Locations: `agent/internal/linecap/linecap.go:62 ReadLine`, `agent/transcript/transcript.go:331 ReadLine`, `agent/internal/jobstore/output.go:1326`, `agent/retained_output_read.go:356 retainedLineScanner.next`, `llm/apilog/codec.go:110`, plus `linecap/page.go` (journal paging).

Evidence: the linecap package doc says "The same technique already exists as agent/transcript.ReadLine ... rather than each carrying its own copy" and then is not used by transcript or retained-output readers. Both loops are the same ReadSlice/ErrBufferFull/overLimit state machine with slightly different limits and EOF semantics (`transcript.go:372` returns `nil,false,n,nil` on EOF; linecap returns `io.EOF`).

Impact: a bug fix to oversized-line draining (the exact class of bug this code exists for) must be made in five places with subtly different contracts.

Fix plan: make `linecap.ReadLine` the one primitive (it already covers cancellation and drain cap); give transcript a thin adapter that maps `ErrTooLong` to `ErrLineTooLong` and the EOF-tail rule; migrate `retainedLineScanner` and jobstore output scanner next. Keep each migration a separate commit with the existing fuzz tests (`linecap_test.go`) extended with the transcript's cases first (TDD for the contract difference).

Size: about -250 lines. Risk: medium (transcript read path; contract differences at EOF must be pinned by tests before the swap).

### agent-internal-tools-9: root `frontmatter` package duplicates `agent/internal/frontmatter` and has no production user (medium)

Locations: `frontmatter/frontmatter.go:1-61`, `agent/internal/frontmatter/frontmatter.go:1-86`, sole importer `internal/bundled/bundled_test.go:9`.

Evidence: root `Split` carries the comment "It matches agent/internal/frontmatter, which this module cannot import". `grep 'evener/frontmatter'` finds exactly one test file importer. The root package also ships 303 lines of tests (`frontmatter_test.go` 220, fuzz 46, DoS regression 37) for a package nothing in production calls. The agent copy inlines CRLF normalization via `lineend`, the root copy inlines it again (`ReplaceAll(ReplaceAll(...))` at root `frontmatter.go:28`).

Impact: two parsers that must stay in lockstep by comment; only one has production use.

Fix plan: change `internal/bundled/bundled_test.go` to parse via a 5-line local helper or move the shared parser to a public path both modules can import (e.g. `agent/frontmatter`, since root already requires the agent module); delete the root package and its tests. Decide with Jesse which: deletion is simpler (YAGNI).

Size: about -360 lines. Risk: low.

### agent-internal-tools-10: flock-based lock files re-implemented in about 12 places (medium, cross-cutting)

Locations: `agent/internal/installid/lock_unix.go:17-57` (+`lock_windows.go` 74), `agent/skill/lease_unix.go:19-89` (+`lease_windows.go` 116), `agent/sandbox/session_scratch_lock_unix.go` (56) / `_windows.go` (73), `agent/schema/snapshot_flock_{unix,windows,other}.go` (49/57/20), `internal/transcriptindex/lock_{unix,windows}.go` (52/44), `internal/selfupdate/install_lock_unix.go` (87), `rendezvous/ownership_unix.go` (73), `cmd/evener-hub/app_instances_key_{unix,windows}.go` (94/120), `cmd/evener-hub/internal/hostlock/hostlock_unix.go` (36), `internal/plugins/locks_unix.go` (24).

Evidence: `grep -l "unix.Flock\|syscall.Flock\|LockFileEx"` returns 20 production files, 1446 lines. `installid/lock_unix.go:18-40` and `skill/lease_unix.go:19-50` are the same sequence: `unix.Open(O_RDWR|O_CREAT|O_CLOEXEC|O_NOFOLLOW, 0o600)`, `os.NewFile`, a `closeOnError`/`closeWith` closure joining the close error, `Stat` + `IsRegular` check, `Flock(LOCK_NB)`, contended = `EWOULDBLOCK|EAGAIN`.

Impact: security-sensitive code (O_NOFOLLOW, regular-file check) is copy-pasted; fixes to one will not reach the others.

Fix plan: one small module-neutral package (for example under `execsupport`, which both modules already require) exposing `Lock(path, exclusive, nonblock) (Lock, contended bool, err)` for unix and windows. Migrate installid first (smallest), then skill lease, then the rest, one package per commit, each with its existing tests. Needs Jesse's call on the home package (new package vs existing `execsupport`).

Size: about -600 lines across modules; +150. Risk: medium (Windows variants cannot be exercised locally; rely on CI).

### agent-internal-tools-11: jsonschema compile logic with an injectable `addResource` hook copied three times (medium, cross-cutting)

Locations: `agent/internal/tool/registry.go:1108-1160` (`compileSchema`, `compileSchemaWith`), `llm/generate.go:380-400` (`compileSchemaHookMu`, `compileSchemaAddResource`, `compileSchemaUnlocked`), `agent/jobs.go:2000-2020` (`validateStructuredResultWithAddResource`).

Evidence: both `registry.go:1147` and `generate.go:393` use the same `const schemaURI = "urn:evener:tool-schema"` with a comment about avoiding `os.Getwd()`; all three accept a closure only so a test can force failure. `generate.go` additionally guards the hook with an RWMutex and sets `c.Draft = jsonschema.Draft2020` while registry.go leaves the library default, so the two compile the same tool schema under different drafts.

Impact: possible validation drift between `llm` tool execution and the agent registry; test seams in prod code; mutable package-global hook with lock.

Fix plan: expose one `llm.CompileSchema(params)` (it is the lower layer, `agent` already imports `llm`), with panic recovery and the URI/Draft decisions made once; the registry's cache stays on top. Delete the three closure seams; test failure paths with malformed schemas. Decide on Draft2020 deliberately and pin it with a test using a 2020-only keyword.

Size: about -90 lines. Risk: medium (draft change can alter which schemas validate; run the tool schema fuzz corpus in CI).

### agent-internal-tools-12: seven hand-written deep-clone helpers for `map[string]any` (low-medium, cross-cutting)

Locations: `tool/registry.go:189-225 CloneSchemaMap`/`cloneSchemaValue`, `llm/registry/load.go:630-665 cloneAnyMap`/`cloneAnyValue`, `llm/providers/internal/openaichat/strict.go:73 deepCopyAny`, `fuzz/typegen/typegen.go:237 cloneSchema`, `agent/subagents.go:594 cloneMap` (JSON round trip), `agent/provider/profile_overrides.go:152 deepCopyJSONMap`, `agent/internal/jobstore/event_clone.go:55 cloneJSONValue`.

Evidence: each walks `map[string]any`/`[]any`, differing only in which typed slices (`[]string`, `[]int`, `[]float64`, `[]bool`, `[]map[string]any`) are handled; behavior on a typed slice not in its list is to return the shared reference. `cloneSchemaValue` handles four typed slices that JSON-decoded data never contains.

Impact: an aliasing bug waits in whichever helper misses a type.

Fix plan: one `clone.JSONValue` in a leaf package (`llm` or `invariant`), using reflection fallback for typed slices; migrate callers (tool.CloneSchemaMap becomes a call). Test with a table covering each container type and an aliasing check.

Size: about -100 lines net. Risk: low-medium (copy depth semantics: confirm none relies on shallow sharing).

### agent-internal-tools-13: rune/byte truncation helpers duplicated at least nine times (low-medium)

Locations: `tool/breaker.go:591 TruncateRunes` (exported "so the agent package's steering messages truncate the same way"), `runetrim/runetrim.go:49 Cut` (byte-based), `agent/transcript_render.go:1966 truncRunes`, `agent/activitybound/activitybound.go:49 Truncate` + `agent/jobs_activity.go:109 truncateActivityText` (a pass-through), `agent/doctor/transcript.go:459 Truncate`, `agent/internal/contextmgr/fork_summarize.go:104 truncate` (byte cut, can split a rune), `llm/failuremessage.go:71 truncateForMessage`, `llm/registry/credential_json.go:80 clipEcho`, `appwire/delegate_roster.go:33 truncateRosterText`, `cmd/evener-hub/internal/hubcore/tree.go:558 truncateTitle`, `hooks/hooks.go:558 firstLine` (inline rune cut).

Evidence: bodies read `r := []rune(s); if len(r) <= limit {return s}; return string(r[:limit]) + "…"`. `fork_summarize.go:104-108` slices bytes (`s[:limit] + "..."`), a latent mojibake bug of the kind `runetrim` exists to prevent. `TruncateRunes` living in the tool package's breaker file is a layering accident (used by `agent/session_loopdetect.go:68`, `agent/memory_page.go:207`).

Impact: inconsistent ellipsis ("…" vs "..."), inconsistent cut units (runes vs bytes), and one real rune-splitting path.

Fix plan: add `runetrim.Runes(s, n, ellipsis)` (the existing package already is the "agent-wide rune-safe cut"), migrate callers one package per commit, delete `tool.TruncateRunes` and the pass-through wrapper in jobs_activity.go. Add a test for the contextmgr case first (red: splits a multi-byte rune).

Size: about -80 lines, 12 call sites. Risk: low.

### agent-internal-tools-14: sysmsg exists but the agent package still hardcodes the reminder tags (low-medium)

Locations: `agent/internal/sysmsg/sysmsg.go:11-33`; literals at `agent/system_notification.go:57,63`, `agent/task_reminders.go:157,205,209` (11 occurrences in that file), `agent/session_compaction.go:463`, `agent/session_queue.go`, `agent/session_tools_task.go` (2).

Evidence: `grep SYSTEM-REMINDER` outside sysmsg finds 21 non-test occurrences in 5 files. `system_notification.go:46 systemReminderBlock` is a pure pass-through to `sysmsg.ReminderBlock`; `systemReminderBlockBuilder`/`finishSystemReminderBlock` (`:53-65`) re-write the tag strings. `task_reminders.go:157` strips `"</SYSTEM-REMINDER>"` with `TrimSuffix` using its own literal, so `IsReminderBlock` and the producer can disagree.

Impact: the format helper's stated purpose ("so the agent package and the compaction it delegates to write it the same way") is only half met.

Fix plan: replace literals with `sysmsg.ReminderOpen/ReminderClose`; delete the pass-through and builder pair in `system_notification.go` (build the body string and call `sysmsg.ReminderBlock`); have `withCurrentStep` use a sysmsg helper to insert before the closing tag. Existing task reminder tests guard output.

Size: about -40 lines. Risk: low.

### agent-internal-tools-15: liveeval duplicates cmdutil.ProvidersConfigPath and ships only to tests (low)

Locations: `agent/internal/liveeval/paths.go:24-50`, `cmdutil/registry.go:20`, `agent/skills_live_test.go:224` (comment: "liveeval.Paths duplicates cmdutil.ProvidersConfigPath").

Evidence: `Paths` re-implements the EVENER_PROVIDERS_CONFIG tri-state byte for byte, justified by "stay free of a dependency on the cmd helper layer". `Enabled(v)` is `return v == "1"`. All 3 callers are `_test.go` files (`agent/live_eval_registry_test.go`, `memory_eval_live_test.go`, `skills_live_test.go`). The package carries a 190-line fuzz test whose own doc says it "re-derives" the implementation independently, i.e. the test restates the code.

Impact: tri-state rule exists twice; production tree holds a package only tests use.

Fix plan: import `cmdutil.ProvidersConfigPath` (the agent module already requires the root module; check for an import cycle first, otherwise move the 25-line function into `envvars` which both can import). Move `OptInEnv`/`Enabled` into `agenttest`; delete `liveeval`.

Size: about -270 lines. Risk: low (watch for module import cycle).

### agent-internal-tools-16: hooks command runtime is three layers of seam; dead `TerminalSequences` field copied three times (low-medium)

Locations: `hooks/command_runtime.go:36-80` (`commandHookInvocation`, `commandHookRuntime` interface, `commandHookProcess` func type, `commandHookExitCoder` interface, `systemCommandHookRuntime`), `hooks/hooks.go:312-313,326-327,341-342`, `:351-381`, `:617-624`.

Evidence: `commandHookRuntime` has one production implementation (`systemCommandHookRuntime`, `hooks.go:182`); `commandHookExitCoder` re-declares `interface{ error; ExitCode() int }` just to avoid `*exec.ExitError` (and `worktree/predicates.go:30` declares its own `exitCoder` for the same reason). `TerminalSequences []string` is declared on `RunResult`, `PreToolUseResult`, `StopResult` with the identical comment "is parsed but currently has no delivery-site consumer"; grep finds no production reader outside hooks.go.

Impact: indirection for tests only; a parsed-but-undelivered field misleads readers about a feature that does not exist (and violates YAGNI).

Fix plan: delete the `TerminalSequence(s)` parsing and fields (no consumer) after confirming no hook doc promises it; reduce the runtime to one injected `func(ctx, invocation) (hookResult, error)` on Runner (fakes in tests set that field), drop the two interfaces and the `commandHookProcess` type. Keep `prepareCommandHookInvocation` as the pure testable step.

Size: about -90 lines. Risk: low.

### agent-internal-tools-17: history and plan narration in comments (low-medium)

Locations: `tool/repair/explain.go` (88 comment lines cite issues/reviews, e.g. `:5-14` "(issue #624 review; mirrors ...)", `:66-107`), `mcp/manager.go:30,42,44,58,64,130,266,393,472,529` ("Task 8", "Task 13", "before Task 8"), `hooks/hooks.go:38` ("Used by Task 7 to set the CLAUDE_EFFORT env var"), `hooks/hooks.go:430-460` ("07 §Exit-code semantics", "07 §General parsing rules"), `worktree/predicates.go:15` ("rev-6 review: consulting HEAD is how remove step 9 destroyed an unmerged branch"), `mcp/manager.go:266` (a function comment about a past "Task 8").

Evidence: counts of non-test lines matching `#NNN|issue #|spec §|Task N|rev-N`: tool/repair 82, worktree 36, mcp 12, tool 7, hooks 2. CLAUDE.md rule: comments describe what code does, not how it used to work; history goes to commit messages.

Impact: readers must decode references to plans that no longer exist; "Task 8" cannot be resolved from the repo.

Fix plan: strip narration, keep the invariant it protected as one present-tense sentence (for example "never consult the main repo's HEAD: a detached-HEAD review checkout would make it destroy an unmerged branch"). Do it in the same PRs that touch each file, not as a sweep (no whitespace-only churn).

Size: about 300 comment lines edited. Risk: low.

### agent-internal-tools-18: worktree lock-state "spec §N" references point at a dated design doc, not evergreen docs (medium, doc-drift)

Locations: `agent/internal/worktree/lockstate.go:1-50` ("the normative summary of spec §5 ..."), 110 `spec §`/`§N` mentions in the package, 135 more in `agent/` outside the package (`grep -rn "spec §5" agent`). The spec is `docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md`; `docs/developing-evener/worktrees.md` and `docs/product/subsystems.md` row S13 do not contain the lock state table.

Evidence: grep for "Lock state machine" in `docs` finds only the dated spec and its plan. AGENTS.md: product docs must be evergreen, dated narratives out of references.

Impact: the normative table that `Decide` implements lives in a dated document; comments cite section numbers that will drift as the spec is edited.

Fix plan: move the lock table (events x unlocked/own/foreign) and sidecar/sweep rules into `docs/developing-evener/worktrees.md`; reword the 245 comment references to name the rule instead of a section number (do it incrementally when files are touched). Add a link from the S13 row in `docs/product/subsystems.md`.

Size: about +80 doc lines, comment edits as touched. Risk: low.

### agent-internal-tools-19: small in-package duplicates in tool (low)

Locations: `tool/apply_patch.go:599-611 minInt/maxInt` (go 1.27 has builtin `min`/`max`; `registry.go:1034` already uses them), `tool/breaker.go:554 collapseWhitespace` vs `tool/apply_patch.go:614 normalizeWS` (`strings.Join(strings.Fields(s), " ")`), `tool/breaker.go:545 firstNonBlankLine` vs `hooks/hooks.go:558 firstLine`, `tool/registry.go:136-186` four near-identical `appendRequiredField`/`appendRequiredFieldAny`/`removeRequiredField`/`removeRequiredFieldAny`.

Evidence: `errorClass` (`breaker.go:533`) trims, collapses, lowercases, replaces digits, so `collapseWhitespace` is `Fields`-join after a trim. `minInt`/`maxInt` have 8 call sites in apply_patch.go.

Fix plan: use builtins; replace `collapseWhitespace` with `strings.Fields` join; fold the four required-field helpers into two using a small `isString(v, field)` predicate (or normalize `required` to `[]any` once). Existing breaker and intent tests guard behavior.

Size: about -60 lines. Risk: low.

### agent-internal-tools-20: registry.go bundles unrelated concerns; domain code lives in the generic registry (low-medium)

Locations: `tool/registry.go:55-66` (arg validation), `:68-190` (intent parameter injection), `:228-296 normalizeAskUserArgs` (ask_user domain logic), `:298-437` (result types and image/document parsing), `:948-1060` (output truncation, about 120 lines), `:1100-1160` (schema compile cache), `:1198 SynthesizedCallID`; `tool/breaker.go` (597) and `apply_patch.go` (671) are also unrelated to the registry but share the package.

Evidence: `registry.go` is 1200 lines with 55 funcs/types; `normalizeAskUserArgs` is invoked from the registry dispatch path but is specific to one tool; the package mixes tool definition schemas (1389 lines), execution registry, retry breaker and patch engine.

Impact: hard to find things; unrelated edits collide.

Fix plan: split files without changing behavior or package: `truncate.go`, `intent.go`, `results.go`, `schemacompile.go`; move `normalizeAskUserArgs` next to `DefAskUserNamed`. Optional follow-up: `tool/patch` subpackage for apply_patch and breaker only if import fan-in warrants. Pure moves; use `git mv`-style diffs reviewed with `--color-moved`.

Size: about 1200 lines moved, 0 net. Risk: low.

### agent-internal-tools-21: mcp manager test seams create twin entry points (low)

Locations: `mcp/manager.go:137 NewManager` / `:185 NewManagerWithClock`, `:270 productionDial` / `:278 productionDialWithEnv`, `:785 transportForConfig` / `:792 transportForConfigWithEnv`, `:820 mergeEnv` / `:827 mergeEnvInto`, and the `conn.now` nil-means-`time.Now` hook (`:94 clock()`).

Evidence: four of these pairs have an `...` form used only by tests (finding 3 counts); the manager has its own `now func() time.Time` while the repo has `agent/internal/clock.Clock` for exactly this purpose and `agenttest.FakeClock`.

Impact: three injection styles (functional option, extra constructor, nil-able field) for time and environment; also `NewManager` takes a parallel `configs`/`dials` slice pair where `dials[i]` corresponds to `configs[i]` with `i < len(dials)` bounds logic (`:157-161`).

Fix plan: take `clock.Clock` and an `environ func() []string` through `Option`s (`WithClock`, `WithEnviron`); fold `dials` into an option `WithDialer(func(cfg) dial)`; delete `NewManagerWithClock`, `transportForConfig`, `mergeEnv`. Existing `cov_reconnect_test.go` and `dropped_conn_test.go` cover the behavior; retarget them.

Size: about -60 production, about 80 test lines retargeted. Risk: low-medium (public constructor signature; one production caller in the agent).

---

## Recommended fix sequence

1. Deletions with no behavior change, one PR: finding 3 (test-only wrappers), finding 4 (Def chains), finding 19 (min/max, small dups), finding 7 (non-fuzz fuzzers), finding 9 (root frontmatter), finding 15 (liveeval). About -1200 production and test lines, risk low.
2. Finding 16 (hooks runtime and dead `TerminalSequences`) and finding 21 (mcp seams) as one "seams to options" PR, since both use the same replacement style.
3. Finding 14 (sysmsg adoption) and finding 13 (rune truncation) as small cross-package PRs; start with the red test for the byte-slicing truncate in contextmgr.
4. Finding 11 (jsonschema compile) and finding 12 (deep clone): one shared helper each; run schema fuzz corpus in CI for the Draft decision.
5. Finding 8 (bounded line reader) after pinning the EOF contract difference with tests; then finding 10 (flock helper) per package with CI Windows coverage.
6. Discuss with Jesse before starting finding 1, 2 and 6 (they change model-facing coaching); then do 6 first (structure-asserting tests), then 1, then 2, so the test net exists before the deletion.
7. Finding 5 (coverage-named test files) per package, interleaved with the PRs that touch that package; finding 20 (registry.go split) last among code moves; findings 17 and 18 (comments and evergreen worktree docs) ride along with whichever PR touches the file, with the worktree lock table doc written when `lockstate.go` is next edited.
