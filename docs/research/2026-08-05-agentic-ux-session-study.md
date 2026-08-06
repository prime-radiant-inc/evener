# Agentic-UX session study: every serf session, 2026-07-31 → 2026-08-05

**Corpus:** 464 sessions (241MB of transcripts), all from the serf-repo bucket.
**Method:** 194 study agents, one per size-balanced session group, each working
through `serf-doctor` per the doctoring-serf skill (outline → targeted markdown
ranges, apilog, jobs, mutations, watches), writing evidence-backed findings per
session; 10 reducer agents clustered the 851 raw findings into cross-session
patterns; this document is the synthesis. Shard aggregations (per-pattern
session lists and evidence) are preserved alongside the raw per-session findings
in the study workspace; every claim below cites example session ids that can be
re-checked with `serf-doctor`.

**Outcomes:** 382 success, 57 partial, 19 failure, 6 unclear. 74 sessions
(16%) were fully clean — zero findings.

The patterns below are ranked by leverage: sessions affected × cost per
incident, biased toward single fixes that close whole classes.

---

## 0. Meta-finding: serf-doctor's apilog decoder is blind to OpenAI-family responses — ~248 sessions (53%)

The single most widespread defect in the corpus, and it corrupts the forensic
layer itself. `serf-doctor apilog` reports `text_length=0, tool_call_count=0,
empty=true` for essentially **100% of calls** from every OpenAI-family
provider (gpt-5.4, gpt-5.4-mini, gpt-5.6-sol, gpt-5.6-luna, and the
openai_codex Responses/continuation endpoints), even on sessions with hundreds
of real, successful tool calls and large `output_tokens`. Anthropic- and
kimi-backed calls in the same sessions classify correctly.

Root cause, pinned by raw-body inspection in several sessions
(0340G88QV6U1kBhFGg3qQ2, 0340WP5AwkXSbcQo6TNMA1, 03410RPBSoDZIffktXxI9i): the
persisted body is an OpenAI **Responses-API SSE stream**
(`response.output_item.done` with `type=function_call`,
`response.function_call_arguments.delta/.done`), and the extractor only walks
the anthropic/chat-completions shape. Worse, it **fails closed to zero** —
"confident empty" — instead of erroring or flagging "unparseable," exactly the
failure mode the doctoring-serf skill exists to prevent. Related: `apilog
--errors` hangs on very large logs (0340PfoBnXbBWnLToQU0DM; 608MB api.jsonl in
03410Qj0SmX9L46Iv1Gb41 blew the 120s shell timeout), and `serf-doctor jobs`
reports "no jobs recorded" for sessions whose activity folded under a different
`parent_session_id` (03426lfveEYC4NbYREX0hY) — another confident zero.

**Fix:** teach the apilog extractor the Responses-API SSE shape; make every
unrecognized response shape report `unparseable`, never `empty`; stream/paginate
`--errors` on large logs. Until then, `--empty`/`--errors` triage is
non-diagnostic for half the fleet — including half of this study's own inputs.

## 1. Execution environment: agents discover the sandbox by failing at it — ~80 sessions

The largest cluster by variety, all one theme: nothing tells the agent what the
sandbox actually provides, so every session pays a discovery tax, and the worst
cases lose the whole task.

- **Stripped PATH** (~17 sessions): exec_command shells start with
  `PATH=/usr/bin:/bin:/usr/sbin:/sbin` — no `/opt/homebrew/bin`, so no go,
  node, npm, rg. Agents burn 4–35 turns on binary archaeology, re-export PATH
  on every subsequent call, and in 0340WP5AwkXSbcQo6TNMA1 wrongly reported
  BLOCKED to the user ("go/node not installed") until corrected. Already
  tracked as kata 31gh.
- **Broken xcrun/Xcode CLT git** (~14 sessions, all high severity): every
  `git` call fails `xcrun: error: invalid active developer path`. Agents spent
  25–87 turns on workarounds, several independently **hand-rolling git in
  Python** (dulwich or raw object writing: 0341kf5CePQrXIyG4hUDZd,
  0341jb8J0d9bz2AscYbkMV, 0341iU257Wd9KaI3tOmd7f); some sessions ended with the
  commit step simply undone.
- **Go toolchain friction** (~12 sessions): shared/poisoned GOCACHE (one
  backgrounded build hung **5.7 wall-clock hours** — 033zIWu0M97TPEmlte5j45;
  ~50 min of hung `go test` in 0341OD339bdFXqO2JkqNyK), and sandbox-denied
  telemetry-token/module-cache writes failing otherwise-correct `go test`
  (0341incx7sRn5rLhQvFi0X shipped without ever running tests). One env-default
  fix (isolated GOCACHE, `GOTELEMETRY=off` or allowlisted paths) closes this.
- **SessionStart hook exit 126** (~35 sessions): the superpowers
  `run-hook.cmd` fails "Operation not permitted" at launch, so the skill
  primer is silently dropped and raw shell noise is injected as the session's
  first STEERING instead. Correlated with read-only-sandbox subagent sessions.
  100% reproducible where it occurs; one packaging/permission fix.
- **Assorted sandbox surprises** (~15 sessions): `$SERF_SCRATCH_DIR` unset in
  the shell (a `tee` wrote to filesystem root — 0341ren5OUl9mp1vhWQykR);
  `/tmp` blocked; benign-but-scary `packed-refs.lock: Operation not permitted`
  on successful commits (agents re-verify with extra calls); TCP listen blocked
  (breaks httptest and the repo's own CDP driver — 0340RTpGsGRApJQHjYZDF1
  re-discovered and re-fixed a *known, documented* limitation over ~50 turns);
  write_file denied late, in one case **losing a fully-composed review
  report** (0340sqvPw7sFO6pN7oRxMU).

**Fix:** besides the individual provisioning bugs, add a **capability
preamble** — writable roots, available toolchains, env vars, network policy —
stated at session start or in exec_command's description, so constraints are
read once instead of rediscovered 464 times.

## 2. Jobs, timeouts, and watches: the ~120s watchdog plus no way to wait — ~60 sessions

The most expensive cluster by turns and wall-clock:

- **Run-timeout kills with output discarded** (~35 sessions): `go test`/build
  jobs die at ~120s regardless of the tool's own `-timeout`, with
  `output_bytes=0` — everything the process printed is thrown away, so agents
  can't distinguish "hung" from "slow" and **retry the identical command**
  up to 35 times (0341CrVzn6z2CM0sgd87F2: 90 of 145 jobs ended `run_timeout`,
  89 with zero output, ~90 minutes lost; 0340xUxn3kwKp6BNy6J7Tj: 15 timeouts
  then **committed without ever seeing a pass**; 03417s8WcLrKQ6yWUHw5du
  shipped a fix untested after 8 identical timeouts).
- **Silent auto-backgrounding** (~8 sessions): a foreground `exec_command`
  that times out quietly becomes a background job; the conversion notice is
  prose the agent misses, so it concludes "failed/finished," relaunches
  duplicates, or (0341mmmMOCHlpyXK483ZRX) delivers an APPROVE verdict before
  its own regression suite finishes, then retracts it.
- **No blocking wait** (~12 sessions): agents hand-roll `sleep N` +
  `job_status`/`ps` loops — 394 polls in one session
  (03413eQFCoJXQsB5SnIsBk), 16 of 107 turns in another
  (0341QLPKaLXFeDbOSpIJfw) — and sometimes guess wrong and duplicate a
  finished 3-minute gate (0342EBgeVoRxkUE0RHAGwg). This is the agent-side
  mirror of the "await behavior, not timeouts" rule; the primitive is missing.
- **job_watch unreliability** (~8 sessions): `output_match` watches that never
  fire (0/18 deliveries in 03410Qj0SmX9L46Iv1Gb41 despite the matched string
  demonstrably appearing; 6/6 unfired in 0340YXtjqeXd1vZOdype8k), duplicate
  notification turns per completion (0342Cv2Nc0NTKhwnO4E1Yq — invisible to
  `serf-doctor watches`, which showed 0 deliveries), and **stale notifications
  after end_turn** (~14 sessions): completion events arriving up to 54 minutes
  late, after the final answer, each costing a wasted acknowledgment turn.
- **Premature success declarations** (~12 sessions): "Merged to main" while
  the merge-approval gate was still running and later found wedged 2+ hours
  (0340osTAgpA4JuYE9yqZgk); DONE while the only test run showed FAIL
  (03419TS38Dz1lp8qEbCps4, 0341dlgkedceALcNvf7mLv); a reviewer that
  **cancelled its own verification job and approved anyway**
  (0341fGUxl8r3sxnOabufKp).

**Fix:** (a) preserve and return partial output on run_timeout, and say
explicitly "serf's job watchdog killed this, not your tool"; (b) make
auto-backgrounding a structured, unmissable field; (c) add a blocking
`job_wait` (or equivalent) primitive; (d) root-cause the output_match
non-delivery and duplicate/stale notification delivery; (e) consider warning
(or refusing) `end_turn` while verification-class jobs the session launched are
still running.

## 3. Search and read tools: unscoped globs, silent failures, front-truncation — ~55 sessions

- **MB-scale truncated output** (~26 sessions): `list_dir`/`grep_files` with
  `**/*`-style globs over the repo root return multi-megabyte results that get
  truncated **from the front** — dropping the top-level, usually most relevant
  entries — with only a prose warning. Worst cases: 24.6M chars removed
  (0340oRyGDTI7qtCLHVJr5o), 9.36M (0340YYXw1fsh1Yi4jVMW4w), 6MB including
  paths from an unrelated repo (03410RPBSoDZIffktXxI9i). Results also sweep in
  `.claude/worktrees/*` — ~30 parallel checkouts multiplying every match. Some
  of these are **templated first moves** ("Scan workspace"), so this fires
  reliably, not occasionally.
- **grep_files silent failures** (~15 sessions): brace-expansion globs
  (`*.{ts,tsx}`) silently return empty instead of erroring
  (0340TIJjrAlhnUF6qSDgF7, 0341TF01aSQtDbEfp5gEIc); patterns starting with
  `--` are rejected as flags (no `--`/`-e` separator); symlinked path
  components denied with no hint that read_file would work; no context-lines
  option — so capable agents abandon grep_files for shell `rg`, which then
  isn't on PATH (pattern 1). In 0340amtcn30VP2nXxeenzt grep_files returned
  empty 4/4 times where `rg` found matches immediately — a correctness bug in
  a core tool.
- **Path guessing** (~40 sessions, cheap each, biggest raw count): read_file
  on a plausible-but-wrong path, ENOENT, self-correct next turn. Two levers: a
  "did you mean" fuzzy suggestion on ENOENT, and fixing the recurring
  **doubled-worktree-segment** paths handed to subagents (a template bug, seen
  across shards) plus stale task-brief paths in SDD dispatches (3 sessions).
- **read_file has no batching**, and its range affordance is
  under-discovered: agents fall back to `exec_command` cat/sed for multi-file
  or paged reads, which loses read-tracking and causes literal duplicate reads
  (same files cat'd in full 5+ times in ~75s — 03417bYCXHDdt9SyTXlYxO; the
  same git blob fetched 8× via `git show` — 0341r9pq78HXthBFOqPweJ).

**Fix:** default `.gitignore`-aware excludes plus `.claude/worktrees` exclusion;
cap results with "N matches, first M shown — narrow your glob" instead of
front-truncation; make unsupported glob syntax an error, accept leading-dash
patterns, add context lines; ENOENT suggestions; fix the worktree path
templating.

## 4. Edit tools — ~20 sessions

- **apply_patch stale-context brittleness** (~15 sessions): exact-match
  context fails after any earlier edit shifts the region; usually a 1–2 turn
  re-read-and-retry tax, but one session spiraled into a 16-attempt, ~66-turn
  loop of guessed retries (0340ZcrzRyy2zYtg72GV8F). The error doesn't say
  "re-read the file first," so the natural retry is another guess — and
  fallback editors hit the same stale state (0342CkyUizCiWeQovXWHGZ).
- **edit_file read-tracking doesn't credit write_file or shell writes**: 13
  spurious "file not read in this session" warnings in one standard
  red-green TDD session (0342Cv2Nc0NTKhwnO4E1Yq).
- **Output-cap truncation of tool-call args**: the trigger for this whole
  effort (0342840HEyE9jiql6OxEeZ — write_file JSON cut at a 4096-token cap,
  then blind `{}` retries; 15 consecutive empty-args write_file calls in
  033zFXmYORubh6iyhBQUF9; the k3 4096-cap turn failures in shard 8). **Root
  cause already fixed this week** (liberal output caps + truncation-aware
  prevalidation, merged 2026-08-05); the study confirms it was hitting more
  sessions than the one that reported it.

## 5. Session provisioning and provider config — ~15 sessions, most of the corpus's outright failures

Of 19 failed sessions, at least 11 died to configuration, not agent behavior:

- **Invalid config at launch**: `model: gpt-5.6-mini` rejected outright by
  Codex/ChatGPT accounts (≥6 sessions, zero work done);
  `reasoning_effort: "ultra"` baked into session config, guaranteed 400 on the
  next call, mid-review (0340dIcAGWOPUPqJbKRWZm, 0340ecEJl1bgnzPlpVtcV1;
  also `'ultra'` killing 0340edlL3scWrnu7nz01Ua). No pre-flight validation of
  model/effort against the provider account, including for delegate dispatch
  (0341iQrntxRFrX27PiMc14 picked the unsupported model twice for delegates).
- **Quota/billing 403s are session-fatal** (~6 sessions): kimi-anthropic-api
  "usage limit for this billing cycle" kills sessions and delegate waves with
  no backoff, no checkpoint, no fallback — discarding completed review work
  (0341b6i2buZ1DtAqRbbtXr, 0341YlLBUnS9RkNgqwAnZ9); one provider 400 silently
  dropped a live user instruction ("commit them") that was never retried
  (033zRC3kTaRFe5K699ZO8z).

**Fix:** validate model + reasoning-effort against the provider/account at
session and delegate launch; treat quota 403s as retriable-with-backoff or at
minimum checkpoint state and surface a clear resumable error.

## 6. Result-tool and bookkeeping contracts — ~30 sessions

- **communicate** (~16 sessions): schema rejections (missing `output` /
  `message`, one session 4 rejections in a row) and models delivering final
  verdicts as bare assistant text requiring a steering nudge and a
  regeneration turn.
- **task_list** (~9 sessions): the only-one-in-progress invariant and
  non-empty-updates rule are discovered by error, repeatedly *within the same
  session* (3–4 identical rejections) — the errors don't state the invariant
  or name the currently-in-progress task. One rejection reads
  `missing required argument "0"`.
- **ask_user**: rejects for a missing `header` while the error text says
  "Required arguments: questions (array)" — self-contradicting; two sessions
  abandoned the tool for communicate after consecutive baffling rejections.
- **update_goal** with no prior set_goal silently no-ops ("No active goal") —
  agents use it as a generic completion marker (2 sessions).
- **delegate_send** used by standalone reviewers to deliver verdicts (2
  independent sessions) — the name reads as generic "send my result."

**Fix:** an error-message audit pass — every rejection should name the actual
failing field or invariant and the corrective action. Cheap, and this class is
pure self-service once messages are right.

## 7. Worktree/delegate lifecycle — ~15 sessions

`manage_worktree` fails at its own core job often enough that agents route
around it with raw git: remove/dispose blocked by "live work" from delegates
that are actually idle, then disposing those same delegates also rejected
(033zFXmYORubh6iyhBQUF9); `dispose` rejecting the session's *own* delegate ids
as unknown (0340YXtjqeXd1vZOdype8k, 03417kpud7CACxsHfbtF6o); `force` flags that
don't force; no cleanup path for worktrees inherited via resume/lineage
(03426lfveEYC4NbYREX0hY); worktrees created via raw git invisible to the
registry, breaking later edit_file/switch (03417O5koiRlFFwxcpiaCe); and several
sessions where a mandated `manage_worktree` operation (`exit`) simply wasn't
registered in the session's toolset. Same shape as the "compact_context"
steering nudge naming an unregistered tool (2 sessions): instructions and
tool registry disagree.

**Fix:** reconcile the ownership model (registry adoption for externally
created/inherited worktrees, dispose accepting the ids the session actually
holds, force meaning force), and make steering templates/tool registry
consistent per session config.

## 8. use_browser — few sessions, catastrophic when hit

`set_viewport` rejected well-formed `{width,height}` payloads in unbroken
loops — **~300 consecutive failures** in 034163AU8MmLapfXKT7nMu (through two
loop-warnings and three user interventions), ~50 in 0341kLv6jX5dIknhqA5ud7,
27+ in 03426lfveEYC4NbYREX0hY, whose `screenshot` also corrupted its own output
path by appending the payload JSON. The error text ("missing width/height")
describes fields the agent is visibly sending, so the loop never converges.
Separately, headless-Chrome CDP is blocked by the sandbox with no diagnostic
pointing at the sandbox (12+ launch variations tried in 03402K3mDAHjaLBsbBgF9Q),
and after a *hook* failure one agent never tried the browser tool at all,
inferring unavailability and failing the task (0340PTK8a9kW5jJ12jPXWF, plus
0340KO0LQZaJunFhMrbEqw where "use Chrome DevTools" was an explicit
instruction). The runtime's stuck-loop STEERING interventions demonstrably do
not break these loops.

## 9. Behavioral patterns worth prompt/harness nudges (not tool bugs)

- **Self-reported done vs. acceptance criteria** (~10 sessions): agents
  declare DONE with a required artifact unwritten or a criterion unmet; users
  correct. A "diff your output against the stated acceptance criteria before
  communicate" nudge — or communicate refusing report formats the task
  contract forbids (counts-without-findings reports needed user demands in 3
  sessions) — would close most of these.
- **Batching discipline**: an A/B pair of sibling reviewer sessions doing the
  same review showed 29 vs 62 tool calls, purely from one-call-per-turn vs
  batched reads (0341bbvPkSYV7JJrnnpKBJ vs 0340pQ0S5H30ASAQdNra7w).
- **`git add -A` in delegates**: one delegate staged ~1600 unrelated paths and
  cost the orchestrator ~15 turns of git surgery (0341iQrntxRFrX27PiMc14).
- **Runaway argument generation**: one task_list call emitted a degenerate
  60KB+ JSON payload of synthetic keys (0341XVRm5VLaaZKzzdl9Oq) — worth a
  size guard on tool-argument payloads.

---

## Recommended attack order

1. **apilog Responses-API decoder + "unparseable ≠ empty"** (§0) — one fix,
   restores forensic ground truth for half the fleet and everything below
   becomes cheaper to verify.
2. **Environment provisioning batch** (§1): PATH (kata 31gh), xcrun/CLT git,
   GOCACHE/GOTELEMETRY isolation, hook exec permission, SERF_SCRATCH_DIR
   export, packed-refs allowance — plus the capability preamble so the rest
   are at least *known* constraints.
3. **Job ergonomics** (§2): partial-output preservation on watchdog kill +
   kill attribution; blocking wait; loud auto-background; output_match and
   notification-delivery bugs.
4. **Search tool defaults** (§3): worktree/gitignore excludes, bounded results
   instead of front-truncation, glob syntax errors instead of silent empties.
5. **Error-message audit** (§6, plus set_viewport and manage_worktree dispose)
   — cheapest per-item work in the list.
6. **Launch validation + quota handling** (§5).
7. **manage_worktree ownership model** (§7).

Fixed already, confirmed by this corpus: output-cap truncation of tool-call
arguments (§4, liberal-output-caps, merged 2026-08-05).

## Study artifacts

- Per-session findings JSON (464 files) and shard aggregations (10 files):
  session scratchpad `study/` directory
  (`/private/tmp/claude-501/-Users-jesse-prime-radiant-toil-suite-serf/cc78eb94-b80a-4a06-a9c9-865a1b25eb66/scratchpad/study/`);
  ephemeral — re-derivable from the durable session state via `serf-doctor`.
- Every session id in this document is a selector:
  `serf-doctor transcript <sid> --format outline` reproduces the evidence.

Caveat on self-reference: the study's forensic layer itself leaned on the
tools it indicts. Where apilog was blind (§0), study agents fell back to
transcript-level evidence, so tool-behavior findings rest on transcripts, not
apilog stats; apilog-derived *waste* metrics for OpenAI-family sessions should
be treated as unavailable rather than zero.
