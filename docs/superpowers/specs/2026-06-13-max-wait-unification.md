# `max_wait_ms` — one wait knob, no booleans (design)

**Status:** v2 — /par round folded (reviewer A: 10 findings incl. 1 blocker;
reviewer B: 10 findings; all survivors below) · **Date:** 2026-06-13 ·
**Branch:** `job-control-spec` · **Sequenced before** PRI-2204 so recursion lands
on the final tool surface.

## §0 Decisions (Jesse's, 2026-06-13)

1. The parameter is named **`max_wait_ms`**.
2. **The booleans die everywhere**: `background` (shell, delegate,
   job_send_message) and `block` (job_read_output, job_stop) are removed, not
   deprecated.
3. **Same shape on every wait-capable job tool** — one rule: *`max_wait_ms`
   bounds how long this tool call may wait; each tool documents what unset
   decodes to.* A call that finishes its business sooner than the bound simply
   returns sooner — the bound is a ceiling, never a demand.
4. **No backward compatibility.** Old params are gone from the schemas;
   `additionalProperties:false` rejects them at the registry.
5. Each tool's `max_wait_ms` property carries a one-line description stating its
   unset decode.

## §1 Motivation

Three live model errors combined `block_timeout_ms` with `background=true` —
rejected loudly per contract, but the class is better made *inexpressible* (the
watch-mailbox precedent). Strict-schema providers force every parameter onto
every call, which already forced zero-reads-as-unset (`0c22499d`); with one knob
whose zero IS a meaningful decode, the workaround becomes the semantics.

## §2 The rule and the decode table

`max_wait_ms` (integer). `0` and absent are identical (strict-provider safe) and
mean **unset**. Negative fails `invalid_request: max_wait_ms must be
non-negative` — uniformly. (Behavior change on delegate, which today silently
clamps negatives to 1000; the §6 red test is genuinely red there.)

**Clamps are runtime decode behavior ONLY.** The `max_wait_ms` JSON schemas
carry **no `minimum`, no `maximum`, no `default` keywords** — `0` must validate
on all five tools or strict providers' forced zeros fail every call (today's
`job_stop` schema carries `minimum: 1000` and would recreate the bug this spec
kills; those keywords are deleted).

| Tool | unset (0/absent) decodes to | positive `max_wait_ms` | runtime clamp |
|---|---|---|---|
| shell | wait the **session default command timeout** (120000 in stock provider profiles; `applyShellTimeoutPolicy` is the source of truth), then promote | wait up to N, then promote to a durable background job | session `MaxCommandTimeoutMS`, then 1000..600000 |
| delegate | **no wait** — background job, return `job_id` now | wait inline up to N; still running at N → return the running job | 1000..600000 |
| job_send_message | **no wait** | resume path: wait inline up to N for the resumed job (`action:"resumed"`). Live-steer and alias targets return on delivery — sooner than any bound, per §0.3; the bound never applies to steer/alias outcomes and the description says so | 1000..600000 |
| job_read_output | **no wait** — snapshot now | bounded wait: with `grep`, until match/terminal/N; without, until new output/terminal/N | 1000..60000 |
| job_stop | **no wait** — request stop, return current status | wait up to N for terminal state | 1000..60000 |

Property descriptions (final wording at implementation; Haiku-gated):

- shell: `"Bound on how long this call waits, in ms (0 = the session default, 120s standard). A command still running at the bound is promoted to a durable background job. Use a small bound (e.g. 1000) to launch-and-return."`
- delegate: `"0 (default): return the job_id immediately; you are notified on completion. >0: wait inline up to this many ms; a timeout leaves the job running."`
- job_send_message: `"0 (default): deliver/resume without waiting. >0: for a resumed job, wait inline up to this many ms for its result; steers and alias sends return on delivery regardless."`
- job_read_output: `"0 (default): snapshot now. >0: wait up to this many ms for a grep match (with grep), or for new output / terminal state."`
- job_stop: `"0 (default): request the stop and return. >0: wait up to this many ms for the job to reach terminal state."`

## §3 Semantics fallout (each deliberate)

- **Shell loses immediate-background; fast commands lose forced durability.**
  "Fire and return" is `max_wait_ms: 1000`. A command that finishes inside the
  bound returns inline and stays **ephemeral** — there is no longer any way to
  force a durable record + terminal notification for a sub-bound command.
  Accepted: the caller holds the complete result inline (status, exit code,
  output), so the record and notification would duplicate what it already has;
  foreground ephemerality has been the designed behavior since the contract's
  line 179. Cards that used fast `background=true` commands as durable fixtures
  switch to fixtures that outlive the bound (e.g. `sleep 2`) — assertion
  strength unchanged. The intentional-background result takes the promotion
  shape (`timed_out: true`, `reason: "foreground_timeout"`,
  `running_in_background: true`) — honest, and it merges the old explicit-
  background and promotion result shapes into one.
- **Buffered (non-streaming) exec environments: documented exception.** There
  is no job manager there, so promotion is impossible — today `background=true`
  is rejected on those envs (`session_tools_shell.go:32-35`, a check that dies
  with the field) and `block_timeout_ms` acts as a kill bound
  (`runBufferedShell`). New semantics, stated plainly: on a non-streaming env
  `max_wait_ms` bounds the command's runtime — still running at the bound, it
  is killed and the result says so. The error/result text must name
  `max_wait_ms` (today it names `block_timeout_ms`) and must not promise
  promotion. The schema description's promotion promise is scoped by the
  handler text on this path.
- **The combo rejection dies with the combo.** `blockTimeoutForegroundOnlyErr`
  and contract `:181` are removed; the shell-lifecycle card's rejected-combo arm
  is deleted — nothing left to reject.
- **The 5000ms blocking default dies on job_read_output/job_stop.** Today
  `block=true` with no timeout waits 5000ms; under §2 the wire zero means "no
  wait", so "block with the default" is inexpressible and
  `defaultJobBlockTimeoutMS` is deleted. Models that want the old behavior say
  `max_wait_ms: 5000`.
- **Granted cross-session reads** reject `max_wait_ms > 0`
  (`invalid_request: max_wait_ms is not supported for granted cross-session
  reads`); unset stays a snapshot, which granted reads support.
- **Internal structs keep their fields** (`shellArgs.Background/BlockTimeoutMS`
  remain as plumbing); only the tool boundary changes. Internal callers and
  manager tests are untouched except where they construct tool-layer args.

## §4 Surface inventory (the sweep — grep-driven, not line-cited)

Inventories below were verified by the /par round; the implementing agent MUST
re-grep rather than trust this list as exhaustive.

- **Schemas + handlers:** `DefShell`/`DefDelegate`/`DefJobSendMessage`/
  `DefJobReadOutput`/`DefJobStop` (`agent/internal/tool/definitions.go`);
  decoders/handlers in `agent/session_tools_shell.go`,
  `agent/session_tools_jobs.go`; buffered path `runBufferedShell` text;
  delegate negative check parity.
- **Also model-facing:** `DefJobWatch`'s description ("use `job_read_output`
  with `block` + `grep`") — reworded; the `## Background jobs` prompt section
  (grep `agent/` prompt text for `background`/`block_timeout_ms`/`block=true`);
  **bundled plugin prompts**: `agent/bundled_plugins/coordinator-workflow/
  agents/coordinator.md` instructs `background=false` and relies on the
  implicit 120s foreground wait — gets an explicit `max_wait_ms`.
- **Renderers:** `agent/transcript_render.go` delegate summary reads the
  `background` arg — renders `max_wait_ms` when positive instead.
- **Error constants:** delete `blockTimeoutForegroundOnlyErr`; reword
  `grantedReadBlockUnsupportedErr`; delete `defaultJobBlockTimeoutMS`.
- **Contract (`docs/job-control.md`):** rewrite EVERY grep hit for
  `background`, `block`, `block_timeout_ms` — including the summary banner,
  quick-guidance lines, design principles, request/response examples, the
  `wait_job` replacement mapping, the legacy-mapping table, the anti-patterns
  list, and the state-diagram label. The combo clause and the
  zero-reads-as-unset clause collapse into §2's rule, written once.
- **Scenario cards:** every file matching
  `grep -l 'background\|block_timeout\|block true' test/scenarios/*.md`
  (~19 files at spec time, including `subagent-cancel-runaway.md` and
  `subagent-list-and-output.md` outside the 14-card matrix). Cite cards by
  FILENAME — two conflicting numbering schemes exist in the research docs.
  Durable-fixture rewrites per §3; assertion strength never reduced.
- **Tests:** every pin of the old args and messages (combo rejection, granted
  block, schema shapes, `TestJobList…`/notification/watch tests constructing
  tool args).

## §5 Decisions an implementer must NOT make alone

Any divergence from §2's table, §3's fallout list, or a sweep hit that doesn't
fit these rules → STOP and surface it; do not improvise semantics.

## §6 Test plan (red-first)

1. Schema: each of the five tools rejects `background`/`block` as unknown
   properties; `max_wait_ms: 0` VALIDATES on all five (strict-provider guard).
2. Decode: per-tool unit tests — unset→default per table; positive→clamped
   wait; negative→`invalid_request` (genuinely red on delegate).
3. Shell: `max_wait_ms: 1000` on a long command returns the promotion shape
   ≈1s; unset uses the session-default path (`applyShellTimeoutPolicy`);
   buffered env kills at the bound with `max_wait_ms` named in the text.
4. Send: positive bound on a live-steer target returns on delivery
   (`action:"sent"`, no error); resume path waits.
5. Granted read: positive bound rejected; unset snapshot works.
6. Haiku comprehension gate on the six reworded descriptions (five tools +
   job_watch).
7. e2e: full 14-card matrix re-run live after the sweep (the old matrix is
   invalidated wholesale — 19 card files carried dead params).

## §7 Out of scope

`job_watch` semantics (description text only), `progress_interval_ms` /
`max_runtime_ms`, the unbounded-mailbox appwire redesign, any compat shim.
