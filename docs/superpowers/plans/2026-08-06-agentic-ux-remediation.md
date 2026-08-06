# Agentic-UX Remediation Plan

> **For agentic workers:** This is a program-level plan. Each workstream below
> is a branch-sized unit with its own test cycle — execute a workstream via
> superpowers:subagent-driven-development on its own worktree, treating the
> workstream's Fix/Tests/Acceptance sections as the task list. Workstreams
> marked **[design gate]** need a short brainstorm with Jesse before
> implementation because they have real design freedom.

**Goal:** Eliminate the failure patterns found by the 2026-08-05 study of all
464 serf sessions (`docs/research/2026-08-05-agentic-ux-session-study.md`),
fixing root causes at every layer: the defective tool, the runtime guardrail
that should have contained the blast, and the forensic tooling that should
have made it visible.

**Architecture:** Defense in depth, same as the output-caps work. Example:
the use_browser incident gets three independent fixes — the MCP tool's error
is upstream (plugin), but serf's dispatch layer gains a failure-aware breaker
so *no* tool can fail identically 300 times (WS2), and serf-doctor gains a
mechanical loop detector so the next study finds survivors in minutes (WS9).

**Tech stack:** Go (agent runtime, llm providers, serf-doctor), existing test
conventions per `docs/testing.md`. All root causes below were verified against
the code on 2026-08-06; file:line references are to current main.

**Provenance:** Every symptom cites session ids from the study; re-check any
with `serf-doctor transcript <sid>`.

## Global Constraints

- Await behavior, not timeouts: no fix may widen or add a timeout to absorb
  awaitable work; gates check exit codes.
- Smallest reasonable change per workstream; no backward-compatibility shims
  without Jesse's explicit approval.
- Every behavior change lands with a test that fails first (TDD per
  workstream).
- Error messages must name the actual failing field/invariant and the
  corrective action — that is the acceptance bar for every message touched.
- Already fixed, do not re-do: output-cap truncation of tool-call args
  (liberal-output-caps, merged 2026-08-05); session survival of terminal
  provider errors (recoverable-provider-failures, `ff859dbbe`/`00dd82602` —
  sessions settle at SessionIdle and queued input survives).

---

## WS1 — Responses-API response recording + apilog throughput

**Was reported as:** "serf-doctor's apilog decoder is blind to OpenAI"
(study §0, ~248 sessions). **Verified root cause is different:** serf-doctor
never parses bodies. `text_length`/`tool_call_count` are computed once at
call time — `llm/api_attempt.go:519-520` from `result.Response.Text()` /
`.ToolCalls()` — and the doctor only copies them
(`agent/doctor/apilog.go:464-492`). The zeros are real properties of the
recorded `llm.Response`: `decodeResponsesStream`
(`llm/providers/openai/responses.go:309`) builds the final Response from the
terminal `response.completed` payload via `fromResponses()`
(`responses.go:1118`), and that event's `output` array arrives **empty** on
this provider family even when earlier `response.output_item.done` events
carried the real content (raw-SSE confirmation: 03410RPBSoDZIffktXxI9i,
0340WP5AwkXSbcQo6TNMA1). The live agent works because it consumes the
accumulated stream; only the persisted record sees the empty terminal shape.

**Fix:**
1. In `decodeResponsesStream`, retain the accumulated output items
   (text parts and `function_call` items assembled from
   `response.output_item.done` / `response.function_call_arguments.*`)
   and, when the terminal `response.completed` payload's `output` is empty
   but accumulated items exist, synthesize the recorded Response from the
   accumulated items. When both exist, the terminal payload wins (it is the
   provider's settled truth); log a mismatch counter if they disagree in
   count.
2. Fixture-driven regression test: extract a real SSE body from one of the
   affected sessions' `api.jsonl` (0340WP5AwkXSbcQo6TNMA1) into a testdata
   fixture; assert the decoded Response has the correct nonzero
   text/tool-call content. A second fixture with a populated terminal
   `output` asserts the terminal payload still wins.
3. apilog throughput: `--errors`/`--empty` on a 608MB log is O(bytes) with
   every request/response body base64-decoded **twice** —
   `llm/apilog/body.go:98` (during unmarshal) and again in
   `validateRecord` (`llm/apilog/record.go:143,197`) — though the doctor
   never reads body content. Add a metadata-only decode mode to
   `apilog.Decoder` (skip body materialization and the byte-count
   revalidation) and use it from `doctor.APILog` summarization paths;
   `--validate` keeps the strict full decode.
4. serf-doctor forensics for historical logs: where a record has
   `text_length=0 ∧ tool_call_count=0` and a stored body, `apilog` gains a
   `--recompute` flag that re-extracts via `openai.fromResponses` /
   the chat-completions parser against the stored body, so pre-fix sessions
   remain diagnosable. Rows whose stored counts and recomputed counts
   disagree are labeled `recorded=0 recomputed=N`.

**Acceptance:** affected fixture decodes with real counts; a fresh session on
gpt-5.4-mini shows nonzero `tool_call_count` in `serf-doctor apilog`;
`apilog --summary` on a ≥500MB log completes in single-digit minutes;
`--recompute` corrects a known-bad historical session (0340eCRdIZ5UJ4oIgD2Jrw:
150 calls, currently 150 "empty").

**Size:** ~300 loc + fixtures. **Priority: first** — restores forensic ground
truth everything else is verified with.

**Status: DONE 2026-08-06** — merged to main as 812eb5c15 (branch
ws1-responses-recording, 3 tasks + fix rounds, final review "Ready to
merge"). Two follow-ups surfaced during execution, tracked here:
- apilog large-log cost is dominated by a **double JSON structural parse**
  per record (kind-sniff + struct decode, ~85% CPU) — the base64 double
  decode was ~1%. The "summary in minutes on 500MB" acceptance needs that
  parse-once refactor; fold into a future apilog perf item.
- `openai_compatible_chat_completions` **SSE** bodies are not recomputable
  (`--recompute` reports an explicit not-supported error; fields stay nil,
  never zero) — needs openaicompat's `decodeStream` factored into a shared
  accumulator the way the openai package's decoders were.

## WS2 — Failure-aware tool-dispatch breaker **[design gate]**

**Symptoms:** ~300 identical failing `use_browser set_viewport` calls through
two loop warnings and three user interventions (034163AU8MmLapfXKT7nMu); 50
and 27+ in two more sessions; 15 consecutive `write_file {}` calls
(033zFXmYORubh6iyhBQUF9); 28+ identical timed-out `go test` reruns.

**Verified root cause:** loop detection
(`agent/session_loopdetect.go:48-70`, window 10, signatures =
name+arghash at `agent/session_tool_round.go:325-326`) is **content-only and
blind to success/failure**, its escalation caps at a third-tier message that
repeats forever (`session_loopdetect.go:8-33`), and it only ever injects
steering — `Registry.ExecuteCall` (`agent/internal/tool/registry.go:480-509`)
is fully stateless, so nothing can refuse a dispatch. MCP tools register into
the same registry (`agent/internal/mcp/manager.go:351-441`); an MCP
application-level error (`result.IsError`, `manager.go:423-437`) passes
straight through as a tool result with zero cross-call bookkeeping — the only
MCP circuit breaker is transport-level reconnect, never application errors.

**Fix (decided with Jesse, 2026-08-06):**
1. Session-level failure ledger keyed by tool name + args hash + error class:
   consecutive identical *failures* only (successes reset; distinct errors
   reset). Read-only polling patterns (job_status returning success) never
   trip it by construction.
2. At **2** consecutive identical failures, the second failure's tool result
   gains a system notification line: "You just ran the same tool twice with
   the same arguments and got the same failure. Consider an alternate
   approach."
3. At **3**, the call is parked: the dispatch result is replaced with a
   structural intervention — the error digest of all 3 failures plus "this
   exact call has now failed 3 times with the same error; it will not be
   executed again until you change the arguments or the approach" — and the
   call is **not executed**. A changed args-hash or a different tool clears
   the parked state.
4. **No session-level tool fencing** (considered, declined): the
   per-signature park is the only enforcement tier. An agent varying args
   against a broken tool is left to the loop detector plus WS9's health
   metrics.
5. Loop-detector integration: when `detectLoop` fires and the looping
   signatures are *failures*, skip straight to the structural intervention
   instead of tier-1 advice. Content-only loops of successes keep today's
   steering-only behavior (legitimate repetition exists).
6. Applies uniformly at the registry/dispatch layer so native, MCP, and
   future tools are all covered. MCP `IsError` results count as failures.
   Parked calls are recorded as ordinary error tool results (no new turn
   kind); WS9's `--health` counts them.
7. **Second trigger (decided 2026-08-06, replaces a proposed MCP-only
   heuristic):** consecutive identical calls returning **byte-identical
   result bodies** nudge at 2 and park at 3 regardless of error status —
   repetition itself is the signal, generic at the dispatch layer, no text
   sniffing, nothing MCP-specific. Motivating case: the chrome MCP plugin
   reports failures as `isError:false` with an "Error:" body (serf records
   them faithfully as successes), so an IsError-only ledger missed the
   300-call set_viewport loop. The plugin bug is filed upstream
   (obra/superpowers-chrome#44); serf's breaker must not depend on tools
   signaling errors correctly.

**Tests:** unit tests on the ledger (reset semantics, error-class equality);
an integration test driving a fake tool that always fails identically and
asserting the nudge at 2 and the park at 3 with the intervention text; an
MCP-path test using a stub server returning IsError.

**Acceptance:** no tool call with identical args and identical error class
executes more than 3 times in a session; the 300-call pattern becomes
impossible by construction.

**Size:** ~400 loc. **Priority: second** — this is the guardrail that
contains every other tool's future bugs.

## WS3 — Job ergonomics: wait primitive, timeout attribution, watch and notification delivery

Verified root causes, one fix each:

1. **No blocking wait exists** (confirmed: only job_status/job_list/job_stop/
   job_watch; `delegate.max_wait_ms` is the only bounded wait,
   `agent/internal/tool/definitions.go:129`). Sessions hand-rolled sleep+poll
   loops — 394 polls in 03413eQFCoJXQsB5SnIsBk. **Fix:** new `job_wait` tool:
   `job_id` (or list), `max_wait_ms` (required, clamped like delegate's),
   returns on terminal state or timeout with the job's status + output tail +
   durable-output pointer. Implementation reuses the existing terminal
   notification hooks (`armFinalizedJob`) rather than polling internally.
2. **`max_runtime_ms` already exists but is undiscoverable** — parsed at
   `agent/session_tools_shell.go:256-264` but absent from `DefShell`'s schema
   (`agent/internal/tool/definitions.go:78-93`). **Fix:** add it to the
   schema with prose ("per-call runtime cap; default 120s"), and document the
   auto-background behavior in the same paragraph.
3. **Watchdog kills are unattributed and the durable output is invisible.**
   The kill maps to `status=stopped, reason=run_timeout`
   (`agent/job_shell.go:620-634`); the model-facing footer says only
   "timed out · running in background as job_X"
   (`agent/session_tools_shell.go:443-473`), and full output *is* durable at
   the job OutputPath (`agent/jobs.go:1570-1637`) readable via
   `read_transcript(transcript_ref="job:<id>")` — agents never learn either
   fact. **Fix:** rewrite the footer for the timeout/kill cases:
   "serf's runtime cap (Xs) stopped this command — the command did not fail
   on its own. N bytes of output are preserved; read them with
   read_transcript job:<id>. Raise max_runtime_ms or run with
   background:true." Zero-output kills add "the process produced no output
   before the cap (a Go build may still have been compiling)."
4. **output_match silently skips lines over 4KB** —
   `maxOutputMatcherLineBytes=4096`; `appendLineFragment`
   (`agent/internal/jobstore/output.go:202-215`) latches `overlong` and
   drops the line from matching with no signal. This — not a registration
   race (registration is correctly locked, `agent/job_watch.go:670-719`) —
   explains 0/18 fired watches (03410Qj0SmX9L46Iv1Gb41). **Fix:** match
   within overlong lines by scanning bounded overlapping chunks of the
   oversized line (window 4096, overlap = maxima of expected match length,
   proposed 512); if that's rejected as too clever, minimum viable: mark the
   watch `degraded=overlong-lines` in job_watch list/inspect output and emit
   a one-time notification, so silence is never unexplained. Same for the
   `run.output == nil` quiet no-scan (`agent/job_watch.go:2549-2551`).
5. **Stale/duplicate/undead notifications.** Verified: an idle session's
   pending notifications wait for an external kick on a droppable 1-slot
   channel (`server/server.go:667-676`), the watch event and terminal owner
   event are enqueued separately with I/O between them
   (`agent/jobs.go:1807-1810` vs `1857-1870`, no coalescing), and
   `job_status` never marks anything consumed
   (`agent/session_tools_jobs.go:391-412`). **Fixes, smallest first:**
   (a) when `projectJobStatus` returns a terminal state, mark that job's
   pending owner notification consumed so it is never re-announced;
   (b) coalesce in `armFinalizedJob`: build both the watch-expiry and
   terminal notices, enqueue once;
   (c) idle-wake correctness: when `s.pendingJobNotifs` transitions
   non-empty and the notify kick fails to land (channel full), arm a
   retry (resend on next channel availability) instead of relying on an
   unrelated future wake. This is await-the-behavior, not a timer cadence.
6. **end_turn while verification jobs run** (premature "Merged to main",
   APPROVE-then-retract — 0340osTAgpA4JuYE9yqZgk, 0341mmmMOCHlpyXK483ZRX,
   03419TS38Dz1lp8qEbCps4). **Fix (warn-first, confirmed 2026-08-06):**
   `communicate` with `end_turn=true` while session-launched jobs are still
   running returns a structured warning line in the tool result naming the
   running job ids — the agent may proceed deliberately but can no longer
   end-turn ignorant. Hard refusal only if the warning proves insufficient
   in practice.

**Tests:** job_wait integration test against a real short-lived job (no
mocked job store); footer-text unit tests; an overlong-line fixture proving
a match inside a 10KB line fires (or flags degraded); notification tests
asserting single coalesced delivery and no re-announce after job_status
consumption; communicate warning test with a live background job.

**Size:** ~600 loc across `agent/`. Item 4's chunk-matching variant is the
only algorithmically risky piece; its fallback is 30 loc.

## WS4 — Environment and sandbox provisioning

1. **PATH** (kata 31gh, confirmed unimplemented): the exec env inherits the
   serf *process* PATH — `agent/execenv/local.go:257-263`, shell is plain
   `/bin/bash`, never login (`local.go:1563-1575`) — so daemons launched
   outside a dev shell strip Homebrew. **Fix per the kata:** capture the
   developer PATH at daemon/session launch (resolve the user's login-shell
   PATH once, `$SHELL -lc 'echo $PATH'`, cached) and seed it through
   `filteredEnvWithSource`; secret-name filtering unchanged.
2. **SERF_SCRATCH_DIR/TMPDIR only set when a sandbox scratch was
   provisioned** (`agent/sandbox/env_floor.go:83-99` gated on
   `sessionScratch != ""`). **Fix:** provision a session scratch dir
   unconditionally and always export both vars.
3. **Go telemetry dir missing from cache roots**: `defaultCacheRoots`
   (`agent/sandbox/resolve.go:347`) covers `.cache, go/pkg, .npm, .cargo`
   but not `os.UserConfigDir()/go/telemetry`, which `go` writes on every
   invocation. **Fix (decided 2026-08-06):** set `GOTELEMETRY=off` in
   `ApplyEnvFloor`; keep the sandbox narrow. (A per-session `$HOME` for
   sandboxed sessions — seeded git identity, shared cache mounts — was
   considered and declined; per-path grants stay the model. Revisit only if
   HOME-relative denials keep accumulating.) Also verify `GOMODCACHE` under
   custom GOPATH resolves inside a granted root; extend
   `isRedirectedCacheVar` (`env_floor.go:116-118`) if not.
4. **Shared GOCACHE wedges** (5.7h hang, 033zIWu0M97TPEmlte5j45; ~50min in
   0341OD339bdFXqO2JkqNyK): investigate-then-fix task — reproduce
   concurrent-session GOCACHE contention, then either per-session GOCACHE
   under the session scratch (cold-build cost) or a shared cache with the
   contention fixed; decision needs the reproduction data.
5. **packed-refs.lock denial on successful commits**: `GitLayout.WritablePaths`
   intends `packed-refs` writable (`agent/sandbox/gitdir.go:64-67`); the
   denial suggests the seatbelt/bwrap grant is file-exact and misses the
   `.lock` sibling created by rename-into-place. Verify grant granularity in
   `agent/sandbox/seatbelt_darwin.go` / `bwrap.go`; grant the pattern or the
   directory.
6. **SessionStart hook exit 126** (~35 sessions): reproduce under a sandboxed
   session; the candidates are exec-bit/quarantine on the plugin cache's
   `run-hook.cmd` vs. a sandbox exec-policy denial of that path. Fix
   whichever reproduces; additionally, hook failures should surface as one
   summarized warning line, not raw shell stderr as the session's first
   steering.
7. **xcrun/broken-CLT git** (~14 sessions, agents hand-rolled git in
   Python): investigation task — determine whether the sandbox masks
   `/Applications/Xcode.app`/`DEVELOPER_DIR` (making host git's xcrun shim
   fail only under serf) or the host CLT was genuinely broken during those
   sessions. If sandbox-caused: grant/passthrough. Either way the capability
   preamble (item 8) reports a failed `git --version` probe at session start
   so no agent diagnoses it from scratch again.
8. **Capability preamble**: extend `sandboxPromptLine`
   (`agent/session_prompts.go:223-244`) — `ResolvedPolicy`
   (`agent/sandbox/resolve.go:117-143`) already carries everything needed.
   Render: sandbox mode + network, writable roots (`Spawned.WriteRoots`,
   `Git.WritablePaths`), masked paths, scratch dir vars, cache redirects,
   and probe results for the session's toolchain (git works?; go/node/rg on
   PATH?). One paragraph, values not prose. This converts patterns
   1–7 from per-session discovery into stated facts even before their
   individual fixes land.

**Tests:** env-construction unit tests (PATH seeding, scratch vars always
present, telemetry root granted); a sandboxed integration test running
`go test` on a trivial module and `git commit` in a scratch repo asserting
pristine stderr; preamble snapshot test rendering a ResolvedPolicy.

**Size:** ~500 loc plus two investigation tasks (4, 7) sized ~100 loc each
once diagnosed.

## WS5 — Search, read, and edit tools

1. **The alias collision is the root cause of "unscoped list_dir"** —
   `NewOpenAIProfile`'s `toolNameMap` renames `glob`→`list_dir` and
   `grep`→`grep_files` (`agent/provider/profile.go:831-835`), shadowing the
   real `list_dir` (`agent/session_tools.go:844-858` documents the
   collision). OpenAI-family agents believe they are listing a directory and
   are actually globbing. **Fix (decided 2026-08-06):** stop shadowing —
   alias glob to `find_files`; the real `list_dir` keeps its name and
   behavior. Models' priors for `list_dir` were exactly what caused the
   unscoped calls.
2. **Glob has no excludes at all** — `doublestar.Glob` over `os.DirFS`
   (`agent/execenv/local.go:1008-1043`), no gitignore, no dot-dir skip; grep
   already skips dot-dirs (`local.go:1159-1168`) and rg honors gitignore.
   **Fix:** give glob the same policy: skip dot-prefixed dirs (killing
   `.claude/worktrees` and `.git` noise) and honor `.gitignore`; an
   `include_ignored:true` arg restores the old behavior when genuinely
   needed.
3. **Truncation shape:** glob/grep use `TruncTail` — keep tail, drop front —
   with the warning "First N characters were removed"
   (`agent/internal/tool/registry.go:596,630-633`). For alphabetically
   ordered glob results the front is the valuable half. **Fix:** for glob
   and grep, return a bounded head plus a structural summary line
   ("N total matches; showing first M; narrow the pattern or paginate with
   offset") instead of tail-keeping truncation.
4. **`--`-prefixed patterns break rg dispatch** — no end-of-options
   separator before the pattern (`agent/execenv/local.go:1095`). **Fix:**
   insert `--` (native fallback unaffected). One-line fix, regression test
   with pattern `--font-size-body`.
5. **Symlink denial names only the basename and reads as fatal**
   (`agent/sandbox/denial.go:119-144`). **Fix:** name the symlinked path
   component and state what would work ("component 'frontend/node_modules'
   is a symlink; read through its target path <resolved> or scope the
   search below it").
6. **read_file's offset/limit are schema-only** — no description strings on
   the properties (`agent/internal/tool/definitions.go:9-25`), unlike
   list_dir's. Agents page files via shell `sed -n` instead, losing
   read-tracking and duplicating reads. **Fix:** describe both params ("for
   large files read in slices: offset = 1-based start line, limit = line
   count, default 2000") in the schema.
7. **ENOENT suggestions:** `ReadFile` returns the bare OS error
   (`agent/execenv/local.go:659-673`); the repair layer's fuzzy suggester
   exists but only for tool names (`agent/internal/tool/repair/suggest.go`).
   **Fix:** on ENOENT, list the nearest existing ancestor dir and fuzzy-match
   the missing basename against its entries; append up to 3 candidates
   ("did you mean agent/session_tools.go?"). Also fixes the doubled-worktree-
   segment handoff paths cheaply (the correct suffix usually exists).
8. **grep context lines:** add `context_lines` (0–10) mapping to `rg -C` /
   native equivalent — the affordance agents kept shelling out for.
9. **apply_patch stale-context error points at recovery** (~15 sessions;
   one 16-attempt/66-turn loop, 0340ZcrzRyy2zYtg72GV8F): the
   context-mismatch error gains one sentence — "the file has changed since
   the content this patch was built from; re-read the target region with
   read_file, then rebuild the patch" — so the natural retry is a re-read,
   not another guess. Fuzzy/whitespace-tolerant context matching is a
   separate deliberate decision, not part of this item (exact matching is a
   correctness feature; the loop was a recovery-guidance failure).
10. **edit_file read-tracking credits the session's own writes**
    (13 spurious "file not read in this session" warnings in one TDD
    session, 0342Cv2Nc0NTKhwnO4E1Yq): a file the session just created or
    fully wrote via write_file counts as read for edit_file's staleness
    check. Locate the read-tracking set edit_file consults and add
    write_file's successful writes to it. Shell-heredoc writes stay
    uncredited (serf can't know the resulting content), which is correct.

**Tests:** table-driven glob tests (dot-dir skip, gitignore, include_ignored);
truncation summary tests; the `--` regression; ENOENT suggestion tests;
profile test asserting no tool-name collision remains for the OpenAI profile;
apply_patch message test; edit-after-write test asserting no warning.

**Size:** ~520 loc. Items 2–10 are independent of the naming decision in 1.

## WS6 — Validation errors that tell the truth

1. **One bug produces both flagship bad messages.** `offendingField`
   (`agent/session_tool_repair.go:98-116`) takes the last path segment of
   the deepest jsonschema cause — an array index (`"0"`) or a nested field —
   and `ExplainSchemaError` (`agent/internal/tool/repair/explain.go:15-32`)
   checks presence only in top-level args and appends only the top-level
   required list. Hence task_list's `missing required argument "0"` and
   ask_user's header/questions contradiction. **Fix:** report the full
   instance path (`questions[0].header`), test presence at the actual
   location, and when the failure is inside a nested object, list that
   object's schema requirements, not the top level's. Table-driven tests
   over the exact historical payload shapes.
2. **task_list invariant errors name the blocker:** `updateLocked`
   (`agent/task/task_store.go:527-547`) has the conflicting task in scope
   and `CurrentInProgress()` (`task_store.go:428-436`) already exists,
   unused. Error becomes: "only one task may be in_progress; <id-N>
   '<title>' is currently in_progress — complete or defer it in the same
   updates array." Add the invariant to DefTaskList's description.
3. **update_goal no-op says why** (`agent/session_tools_goal.go:57-59`):
   keep it non-erroring but return "No goal is active for this session
   (none was set at launch); nothing recorded — this tool only updates a
   goal the harness registered." Move `defUpdateGoal` into
   `agent/internal/tool/definitions.go` with the other definitions while
   there (consistency; it's the only stray).
4. **delegate_send description** (`agent/internal/tool/definitions.go:159`):
   first sentence states "sends to a child delegate you created — this is
   not how you deliver your own results; use communicate."
5. **Oversized-args guard:** the 60KB degenerate task_list payload
   (0341XVRm5VLaaZKzzdl9Oq) executed nothing useful. Registry-level cap on
   tool-argument byte size (proposed 256KB, well above any legitimate call)
   with an explicit error, so runaway generation fails fast and legibly.

**Tests:** exact-message assertions for each historical failure payload.
**Size:** ~250 loc. Cheapest high-affection work in the plan.

## WS7 — Launch validation and quota classification

1. **Model membership validation exists but is unwired at the two seams
   that matter.** `validateModelSwitchMembership`
   (`agent/session_set_model.go:117-134`) guards interactive SetModel, and
   `launchcheck` guards the CLI — but `NewSession`
   (`agent/session_init.go:115,128`; `resolveLiveModelProfileWithTimeout`
   silently tolerates everything) and delegate dispatch
   (`selectSubagentModel` → `resolveProfileForRef`,
   `agent/session.go:817-828`, pure string parsing) validate nothing. That
   is why gpt-5.6-mini sessions died on their first call and delegates were
   dispatched onto unsupported models twice in one session. **Fix:** run the
   membership check at both seams; a delegate dispatch with an unavailable
   model fails the `delegate` call with the live-list alternatives named,
   instead of burning a session.
2. **reasoning_effort is never validated** — `ClampReasoningEffort`
   (`llm/types.go:670-704`) passes unknown values through unchanged, so
   config-baked `"ultra"` reached the wire and 400'd mid-review. **Fix:**
   validate against `effortRank` (`llm/types.go:628-635`) wherever config
   is accepted (`NewSession`, delegate args, session_config load); reject
   with the valid vocabulary listed. Keep clamp semantics for *known*
   values unchanged.
3. **Quota-403 misclassification:** usage-limit detection
   (`parseUsageLimit`, `llm/errors.go:307-319`) is wired only for 429s; the
   kimi billing 403 lands in the flat `case 403:` as generic access-denied
   (`llm/errors.go:286-297`). Session survival is already fixed; what
   remains is honesty and delegate-wave behavior. **Fix:** run usage-limit
   body detection on 403s too, classify as `KindQuotaExceeded`, and surface
   "provider usage limit for this billing cycle" (with the reset time when
   the body carries one) instead of "access denied" — so orchestrators stop
   treating quota exhaustion as a per-delegate transient and re-dispatching
   into it.
4. **Codex-backend model compatibility is one hardcoded string** —
   `wireModel` rewrites only `gpt-5.6` → `gpt-5.6-sol`
   (`llm/providers/openai/responses.go:803-814`), and gpt-5.6-mini slips
   through to a 400. **Fix:** table-drive the Codex-backend support map in
   the adapter (supported slugs + rewrites), consult it in `wireModel`, and
   reject unsupported slugs at validation time (item 1 path) with the
   supported list.

**Tests:** NewSession/delegate rejection tests against a fake live-model
list; effort-vocabulary rejection test with `"ultra"`; classification test
with the captured kimi 403 body; wireModel table test.
**Size:** ~300 loc.

## WS8 — Worktree lifecycle and instruction/registry coherence

1. **dispose ownership is exact-match on the original creator forever.**
   `findDelegateLaneRecord` requires descriptor `ParentSessionID == s.id`
   (`agent/session_tools_worktree_dispose.go:66-92`); descriptors preserve
   the *original* creator across forwarding/resume, and the close-time
   sweep filters identically (`agent/session_worktree_close.go:33-52`), so
   a resumed session can never dispose lanes it legitimately inherited —
   and `disposeHintForRetainedIdle` silently gives no hint in exactly those
   cases. **Fix:** ownership follows lineage: a session whose id appears in
   the descriptor's parent chain (or that holds the resumed identity of the
   creator) may dispose; on session resume, re-stamp inherited descriptors
   with the live session id so both the live op and the close sweep see
   them.
2. **The live-work guard has no force path and counts idle retained
   subagents as live** (`agent/session_tools_worktree.go:749-807,1748-1753`).
   **Fix:** `remove force:true` on a tree whose only blockers are
   retained-idle delegates performs the dispose cascade itself (dispose
   each idle lane, then remove); genuinely running jobs still refuse.
3. **No registry adoption for external/inherited worktrees** — enumeration
   is strictly `isUnderManagedDir` (`session_tools_worktree.go:1280-1321`).
   **Fix (minimal):** `manage_worktree list` gains an `unmanaged` section
   showing `git worktree list --porcelain` entries under the managed root
   that lack a sidecar, and `adopt` creates the sidecar for one — killing
   the "registry does not know this worktree" dead end after a raw-git
   fallback (which WS8.1/2 make rarer to begin with).
4. **Steering that names unregistered tools:** the self-compact nudge fires
   on `s.contextMgr != nil` alone (`agent/session_self_compact.go:98-118`)
   with no registry check, while `compact_context` can be pruned by denied
   tools, minimal registries, or delegate policy
   (`agent/session_init.go:999-1019`). **Fix:** gate the nudge (and audit
   the other steering templates for tool mentions) on
   `s.reg.Get(name) != nil`, with the nudge falling back to "summarize and
   drop stale context in your next messages" when the tool is absent. The
   `manage_worktree exit` mandate came from a role prompt outside agent/ —
   audit `internal/bundled` prompts for tool mentions and gate or reword.

**Tests:** lineage-dispose test across a simulated resume; force-cascade
test with a retained-idle delegate; adopt round-trip test; nudge-gating
test with a pruned registry.
**Size:** ~450 loc.

## WS9 — serf-doctor: make the next study mechanical

This study cost ~16M subagent tokens because agents re-derived mechanical
facts per session. The doctor should compute those; LLM judgment then only
reads flagged sessions. (Direct response to Jesse's ask.)

1. **`serf-doctor sessions [--since DUR] [--bucket B|--all]`** — enumerate
   sessions with started/ended, model(s), turn count, transcript bytes,
   outcome hint (final communicate status if present), parent/delegate
   links. The study's enumeration was `find`+`wc` by hand; this is the
   entry point every batch analysis needs. `--json` first-class.
2. **`serf-doctor transcript <sel> --health`** — mechanical per-session
   metrics, all cheap folds over existing canonical readers:
   - tool-call and tool-error counts by tool and error class;
   - longest run of consecutive identical calls (name+argshash) and whether
     they were failures — the loop metric (feeds WS2's telemetry too);
   - truncation-warning count in tool results;
   - steering counts by kind (loop-detected, notification, bare-text);
   - jobs by terminal reason (run_timeout count, zero-output count);
   - notifications delivered after the last end_turn (staleness metric);
   - user STEERING messages that follow a completed communicate
     (the user-correction proxy).
3. **`serf-doctor audit --runbook R --sessions <set|--since DUR>`** — batch
   driver: run a runbook's mechanical checks across a session set, emit
   Finding JSON per the existing contract with `signature` dedup across
   sessions, and a summary table (pattern × session count). The
   doctoring-serf skill already defines runbooks and the Finding contract;
   this adds the fan-out executor so 464 sessions is one command, not 194
   subagents.
4. **apilog fixes from WS1 land here too** (`--recompute`, metadata-only
   fast path), plus `apilog --health` one-line verdict: attempts, empties
   (recomputed), retry storms, unsettled groups, quota/permanent errors.
5. New runbooks codifying this study's detectors so they run standing:
   `error-loop.md`, `stale-notification.md`, `run-timeout-waste.md`,
   `truncation-waste.md` — each written per `writing-runbooks.md`, each
   emitting Findings only on confirmed, actionable problems.

**Tests:** golden-output tests over fixture session dirs (the E2E scratch
shape, `--state-dir`); a fixture reproducing each health metric.
**Size:** ~700 loc. Independent of all other workstreams; can run in
parallel with WS1.

## WS10 — Prompt/skill-layer nudges (behavioral, no runtime change)

Small, evidence-backed prompt and skill edits; each cites its pattern:
1. Acceptance-criteria self-check before end_turn (10 sessions of
   "done-but-deliverable-missing"): one paragraph in the result-tool prompt
   section — before end_turn=true, re-read the task's stated deliverables
   and name each in the output. (The WS3.6 warning covers the running-jobs
   half; this covers the never-started half.)
2. Batching guidance in the tool-use prompt section (29 vs 62 calls for
   identical review work): independent reads/greps go in one round.
3. Delegate-brief hygiene in the delegation prompt templates: never
   `git add -A`; stage named paths (the ~1600-file staging incident).
4. doctoring-serf skill: add WS9's commands to the tool table once they
   exist, and add the "recorded=0 vs recomputed" caveat for pre-WS1 logs.

**Size:** ~80 loc of prose. Ship with, not before, the runtime fixes they
reference.

---

## Sequencing and dependencies

| order | workstream | why this position | size |
|---|---|---|---|
| 1 | WS1 recording fix | everything below gets verified with apilog; unblocks trust | ~300 loc |
| 1 (parallel) | WS9 doctor batch tooling | independent; makes verifying the rest cheap | ~700 loc |
| 2 | WS2 dispatch breaker | contains every future tool bug; thresholds decided (nudge@2, park@3, no fencing) | ~400 loc |
| 3 | WS3 job ergonomics | biggest turn-waste cluster | ~600 loc |
| 4 | WS4 environment | biggest wall-clock cluster; two investigation subtasks | ~500 loc |
| 5 | WS6 validation errors | cheapest, high per-session affection | ~250 loc |
| 6 | WS5 search/read/edit | naming decided (glob aliases to find_files) | ~520 loc |
| 7 | WS7 launch validation | kills the config-death failure class | ~300 loc |
| 8 | WS8 worktree lifecycle | lowest frequency of the code workstreams | ~450 loc |
| 9 | WS10 prompt nudges | rides along with the features it references | ~80 loc |

Each workstream: own worktree, own SDD run. All design gates were resolved
with Jesse on 2026-08-06 (WS2 nudge@2/park@3/no-fencing; WS5 find_files
alias; WS4.3 GOTELEMETRY=off, per-session HOME declined; WS3.6 warn-first).
The one remaining data-driven decision is WS4.4's GOCACHE strategy, taken
after its reproduction task. Everything else is specified above at
implementation-ready precision.

Out of scope, tracked elsewhere: the use_browser MCP server's own
set_viewport/screenshot bugs (superpowers-chrome plugin, not this repo —
WS2 contains the blast radius regardless); host Xcode CLT repair if WS4.7's
investigation shows the host, not the sandbox, was broken.
