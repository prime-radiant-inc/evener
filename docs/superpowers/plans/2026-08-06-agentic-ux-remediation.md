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
   heuristic; amended same day — nudge-only):** consecutive identical calls
   returning **byte-identical result bodies** are nudged from 2 onward,
   regardless of error status — repetition itself is the signal, generic at
   the dispatch layer, no text sniffing, nothing MCP-specific. **This
   trigger never parks** (Jesse, 2026-08-06): implementation showed
   repetition-parking assumes same-args-means-same-result forever, which is
   false for every tool observing mutable state — three identical
   `communicate` turns parked the session's only exit door, and a third
   `read_file` after an external change was refused though it would have
   returned new content. Parking stays exclusive to the failure trigger
   (point 3). Motivating case: the chrome MCP plugin reports failures as
   `isError:false` with an "Error:" body (serf records them faithfully as
   successes), so an IsError-only ledger missed the 300-call set_viewport
   loop; that shape now draws a persistent nudge rather than a park. The
   plugin bug is filed upstream (obra/superpowers-chrome#44); serf's breaker
   must not depend on tools signaling errors correctly.

**Tests:** unit tests on the ledger (reset semantics, error-class equality);
an integration test driving a fake tool that always fails identically and
asserting the nudge at 2 and the park at 3 with the intervention text; an
MCP-path test using a stub server returning IsError.

**Acceptance:** no tool call with identical args and identical error class
executes more than 3 times in a session; the 300-call `set_viewport` shape
draws the repetition nudge from call 2 onward (nudge-only per the
2026-08-06 amendment — the failure trigger still parks genuine identical
failures).

**Size:** ~400 loc. **Priority: second** — this is the guardrail that
contains every other tool's future bugs.

## WS3 — Job ergonomics: promotion legibility, watch scanning, notification delivery

**Rewritten 2026-08-06 after a decision-record audit with Jesse.** The
original draft was specced from code exploration without reading
`docs/job-control.md` (the normative contract) and proposed two things that
contradicted it. Corrected scope:

1. **No `job_wait` tool — decision reversed.** `docs/job-control.md`
   explicitly rejects a separate wait tool ("Waiting, when needed, is
   bounded ... not a separate wait tool"; "Notifications replace waiting");
   delegates follow the same notification contract as jobs (no
   synchronous-parent case). The 394-poll sessions are agents rationally
   distrusting a notification channel that was late/duplicated/stale —
   fix the channel (item 4), don't add the tool the design already
   rejected. If poll storms persist after delivery is fixed, reopen the
   contract question with data.
2. **No `max_runtime_ms` exposure — decision reversed.** The parameter was
   deliberately removed from the shell schema on 2026-08-05 because agents
   set it constantly and inappropriately, killing jobs. The server-side
   parse remnant stays as-is; nothing re-exposes it. (The study window
   predates the removal, so its "watchdog killed my test" incidents were
   largely that abuse — already fixed by the removal.)
3. **Promotion legibility and stop attribution.** The 120s foreground wait
   does not kill anything — it promotes to a durable background job
   (`agent/job_shell.go:214-244`) — but the footer
   (`agent/session_tools_shell.go:443-473`) buries that in a bracket, so
   agents read promotion as failure and relaunch duplicates. **Fix:**
   rewrite the promotion footer to state plainly: the command is STILL
   RUNNING as job_X; output accumulates durably and is readable with
   `read_transcript(transcript_ref="job:<id>")`; completion arrives by
   notification — do not relaunch or poll. For jobs that genuinely end
   `stopped/run_timeout` through remaining paths, `job_status` output
   attributes the stop to serf's limit, not the command; zero-output cases
   note the process may still have been compiling.
4. **Byte-window watch scanner (replaces the line scanner).** Today
   `output_match` scans line-by-line and silently never scans any line over
   4KB (`maxOutputMatcherLineBytes`, `agent/internal/jobstore/output.go:202-215`)
   — the verified cause of 0/18 fired watches in 03410Qj0SmX9L46Iv1Gb41
   (registration is correctly locked; not a race). **Fix (Jesse's design):**
   scan a rolling byte window over the raw stream — carry the last 4KB plus
   each new chunk, run the pattern per window, no line semantics at all.
   Details that must be pinned by tests: multiline anchor mode with
   documented `^`/`$` semantics at window edges; the documented match-length
   bound (a match longer than the window cannot fire — a stated limit, not
   a silent class); offset-based dedup (only matches ending past the
   previously scanned offset fire); a match spanning a window seam fires
   exactly once. Also fix the `run.output == nil` quiet no-scan
   (`agent/job_watch.go:2549-2551`) to be loud.
5. **Notification delivery — all three fixes approved 2026-08-06:**
   (a) terminal state returned by `job_status` marks that job's pending
   owner notification consumed — with its own recorded state ("caller
   learned via status read") so the jobstore's told-the-caller invariant
   and serf-doctor's diagnostics stay truthful;
   (b) coalesce in `armFinalizedJob`: build the watch-expiry and terminal
   notices together, enqueue once (today they enqueue at two moments with
   I/O between — the two-turns-per-completion bug);
   (c) guaranteed idle-wake: when `pendingJobNotifs` goes non-empty and
   the 1-slot notify kick is dropped (`server/server.go:667-676`), arm a
   resend on channel availability — delivery becomes guaranteed, not
   best-effort. No timers, no polling cadence.
6. **end_turn while verification jobs run** (premature "Merged to main",
   APPROVE-then-retract — 0340osTAgpA4JuYE9yqZgk, 0341mmmMOCHlpyXK483ZRX,
   03419TS38Dz1lp8qEbCps4). **Fix (warn-first, confirmed 2026-08-06):**
   `communicate` with `end_turn=true` while session-launched jobs are still
   running returns a structured warning line in the tool result naming the
   running job ids — the agent may proceed deliberately but can no longer
   end-turn ignorant. Hard refusal only if the warning proves insufficient
   in practice.

**Tests:** promotion-footer unit tests; byte-window scanner suite (seam
match fires once, long-line match fires, anchor semantics pinned,
match-length bound documented and asserted, offset dedup); notification
tests asserting single coalesced delivery, no re-announce after job_status
consumption, and guaranteed idle delivery under a saturated kick channel;
communicate warning test with a live background job. No mocked job store.

**Size:** ~500 loc across `agent/`. The byte-window scanner is the
load-bearing piece; its tests are the majority of the work, deliberately.

## WS4 — Environment and sandbox provisioning

**Audited against `docs/sandboxing.md` and `docs/environment.md` 2026-08-06;
two open questions ruled by Jesse the same day.**

1. **PATH** (kata 31gh, recorded intent): resolve the user's login-shell
   PATH once at daemon/session launch (`$SHELL -lc 'echo $PATH'`, cached)
   and seed it through `filteredEnvWithSource`
   (`agent/execenv/local.go:257-263,1684-1743`); secret-name filtering and
   the sandbox env floor unchanged. Rejected: hardcoding /opt/homebrew/bin
   (host-specific), per-command login shells (slow, side-effectful).
2. **Scratch dir**: `SERF_SCRATCH_DIR` is documented
   (`docs/environment.md`) as per-session with no sandbox-only caveat; the
   unset cases were unsandboxed sessions. Provision a session scratch dir
   and export `SERF_SCRATCH_DIR`/`TMPDIR` unconditionally — reality up to
   the documented contract.
3. **Go telemetry** (amended 2026-08-06, superseding same-day
   `GOTELEMETRY=off` ruling): implementation proved `GOTELEMETRY` is a
   NON-SETTABLE Go env var — it only mirrors the persisted mode file under
   the user config dir, and setting it in a real Seatbelt sandbox changed
   nothing (telemetry still stats its upload-token file and emits
   "operation not permitted" on stderr; exit codes unaffected). Jesse's
   ruling: **accept + document**. No telemetry code change; the pristine-
   stderr acceptance allows exactly this known noise line, pinned as
   expected in the integration test; the capability preamble states it as
   a resolved fact. The `GOMODCACHE`-outside-granted-roots gap found in
   the same task was real and is fixed via `isRedirectedCacheVar`
   (per-session HOME remains considered-and-declined).
4. **GOCACHE wedge — investigation reframed by the audit.** Sandboxed
   sessions already get contained caches by design (`docs/sandboxing.md`:
   "a sandboxed session can never poison a cache that a later build
   consumes" — overlay or private). So the wedged sessions
   (033zIWu0M97TPEmlte5j45's 5.7h hang cited GOCACHE on an external
   volume) were likely unsandboxed or host-config issues. Investigate
   first: classify each wedged session's sandbox mode and resolved cache
   strategy; only then decide whether serf changes anything. Any change
   preserves the never-poison invariant.
5. **packed-refs.lock**: the sandbox contract explicitly promises
   packed-refs is writable (commit/add/checkout succeed) while git config
   and hooks stay read-only (load-bearing: no hook planting). The `.lock`
   denial is grant granularity — verify in
   `agent/sandbox/seatbelt_darwin.go`/`bwrap.go` and grant exactly the
   rename-into-place `.lock` sibling. Nothing wider; config/hooks
   protection untouchable.
6. **Hooks and MCP servers work in every sandbox mode (ruled 2026-08-06).**
   Exit-126 was plausibly restricted mode working as designed — the plugin
   cache under `~/.claude/plugins` is outside restricted-mode read roots.
   Ruling: hooks and MCP servers are session infrastructure; the sandbox
   read/exec surface includes the hook and MCP-server paths in all modes.
   Independently: a hook that still fails surfaces as one summarized
   warning line, never raw shell stderr as the session's first steering.
7. **Developer toolchain readable in restricted mode (ruled 2026-08-06).**
   macOS `git` is an xcrun shim needing the Xcode CLT under
   `/Library/Developer` (or Xcode.app); absent from restricted read roots,
   git cannot work at all — the verified shape behind sessions hand-rolling
   git in Python. Ruling: add the developer-tools directories, read-only,
   to restricted mode's system read roots. The investigation shrinks to
   confirming this mechanism against those sessions before the grant
   lands.
8. **Capability preamble — extends the existing designed banner.**
   `sandboxPromptLine` (`agent/session_prompts.go:223-244`) already renders
   a deliberately honest one-liner from resolved policy ("never
   overstates"). Extend it with writable roots
   (`Spawned.WriteRoots`, `Git.WritablePaths`), masked paths, scratch
   vars, cache mode, and session-start toolchain probe results (git
   works?; go/node/rg on PATH?) — every line from resolved facts or real
   probes, never intentions.

**Tests:** env-construction unit tests (PATH seeding, scratch vars always
present); sandboxed integration tests running `go test` on a trivial module
and `git commit` in a scratch repo asserting pristine stderr, in both
workspace-write and restricted modes; a restricted-mode test executing a
hook script from the plugin-cache path; preamble snapshot test rendering a
ResolvedPolicy with probe results.

**Size:** ~500 loc plus the (shrunken) investigations in items 4 and 7.
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

**Audited against `docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md`
and walked with Jesse 2026-08-06; items 1-3 confirmed, item 4 expanded.**

1. **Dispose ownership follows resumed identity — chain-walk cut.** The
   design record's ownership scoping is deliberate ("defaults to not
   destroying other sessions' work"); an ancestor disposing a descendant's
   lane would violate it, so no lineage walk. The actual bug: descriptors
   keep the original creator id forever
   (`agent/session_tools_worktree_dispose.go:66-92`,
   `agent/session_worktree_close.go:33-52`), so a **resumed** session is
   treated as "another session" and cannot dispose lanes it itself
   created. Fix: on resume, inherited lane descriptors re-stamp to the
   resumed identity; genuinely-other sessions stay refused.
2. **`remove force:true` runs the sanctioned dispose cascade for
   retained-idle lanes.** The record scopes `force` to provenance/merge
   gating and `force_dirty` to the dirty-tree refusal; the live-work
   refusal is deliberately not force-overridable and stays that way for
   running jobs. Retained-*idle* delegates are not live execution and
   `dispose` is their sanctioned exit — so when the only live-work
   blockers are retained-idle lanes (`agent/session_tools_worktree.go:749-807,
   1748-1753`), `remove force:true` disposes each lane first (each dispose
   keeping its own unmerged-work gate, force-overridable per the existing
   dispose contract) and then removes. Running jobs still refuse, always.
3. **Explicit adoption for unmanaged worktrees under the managed root.**
   The record keeps destructive ops managed-only ("never removed or pruned
   by this tool") and already contains an idempotent-adopt concept for
   locks. `manage_worktree list` gains a labeled `unmanaged` section for
   raw-git worktrees under the managed root; a new explicit `adopt`
   operation creates the sidecar, converting one to managed on request.
   Nothing auto-adopts; nothing unmanaged is ever removable. (Items 1-2
   shrink this class at the source — agents fell back to raw git after the
   tool failed them.)
4. **Tool-instruction coherence (expanded per Jesse 2026-08-06).** Two
   parts:
   (a) **Subagents should have `compact_context`** — the affected delegate
   sessions lacking it was itself the bug. Investigate which pruning path
   removed it (`agent/session_init.go:999-1019`,
   `registerMinimalWorktreeTools`) and fix the default subagent surface so
   they get it.
   (b) **Generalized validate-before-instruct:** any steering template or
   canned prompt that names a tool checks the session registry first
   (`s.reg.Get(name) != nil`); if absent, skip or use tool-free fallback
   wording (the self-compact nudge falls back to "summarize and drop stale
   context in your next messages"). The unconditional nudge at
   `agent/session_self_compact.go:81-118` is the first fix; then audit
   `internal/bundled` role prompts for tool mentions (the `manage_worktree
   exit` mandate came from one) and gate or reword each.

**Tests:** lineage-dispose test across a simulated resume; force-cascade
test with a retained-idle delegate (and a refusal test with a genuinely
running job); adopt round-trip test; subagent-surface test asserting
compact_context present; nudge-gating tests for present/absent registry
states.

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
| 3 | WS3 job ergonomics | biggest turn-waste cluster; rescoped 2026-08-06 per job-control contract | ~500 loc |
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
