# Observer Job Read Grant Design

Kata: eqs0. Blocks: fd8n.

**Status: implemented** on `wip/kata-eqs0`, all six rulings, per
`docs/superpowers/plans/2026-07-31-observer-job-read-grant.md`. Two corrections
to this document's own text, found while implementing:

- Ruling 5's condition is stated here as "the finished job's `DelegateID` is the
  receiver's own", summarized in the ruling block as "`DelegateID` equals the
  receiver session id". `events.JobFinishedData.DelegateID` is a `dlg_` handle
  and `receiverSessionID` is a session id, so those can never be equal. The
  implemented condition is `DelegateID == watchConfig.receiverDelegateID` —
  the same handle in the same watched session's namespace, and stable across
  observer resume, which is the property the session-keyed grant exists for.
- "Terminal-only" is stated here as a property of the `job.notification`
  payload, which is right, but the doc leaves the impression that the existing
  per-fire mint already had it. It did not: the per-fire mint keyed on the
  watch's *resolved watched identity*, so a concrete-job watch could grant on a
  running job. Rulings 4 and 5 therefore landed as one commit — deleting the
  create mint and making the per-fire mint payload-derived are two halves of the
  same seam, and no green commit exists between them.

`docs/job-control.md`, `docs/agentic-testing.md`, and `test/scenarios/` are
untouched; they are kata fd8n's sweep.

## Problem

An observer sidecar woken by a watch frame can act only on what the frame
carries. `assistant.tool` frames carry the tool's own `output`, which covers
most sidecar work. `job.notification` frames carry `job_id`, `job_type`,
`status`, `reason`, `exit_code`, and `output_bytes` — the size of the result,
never the result. A handoff packager, a quality auditor, or a triage sidecar
that must look at what the finished job actually produced has no path to it.

Serf has the machinery for that path and cannot reach it.
`EventWatchReadGrant`, `lookupGrantedJobRead`, `mintWatchCreateReadGrant`, and
`mintWatchSendReadGrant` all exist, are unit-tested, are folded durably by
`jobstore.FoldGrants`, and are described in `docs/job-control.md` as a live
capability (`:1211` "Serf grants the observer `job_read_output` for that
watched job"). No model can mint a grant and no model could spend one:

- both mint paths return early when the watch target is a session alias
  (`agent/job_watch.go:3271`, `:3317`), and `source:"parent"` — the only
  cross-session observation shape that survived June — always has target
  `caller`;
- the only public configuration that produced a concrete job target plus a
  delegate delivery target was `job_watch(target=<job_id>, send={to:<delegate_id>})`,
  deleted by `9d0d777c6`;
- the capability the grant extends, `job_read_output`, was unregistered from
  the model-facing tool registry by `cf84923c6`.

Verified live on an isolated hub (kata f9gn, commit `ba2987fe4`): a
`watch_parent` observer woken by a `job.notification` frame gets
`unknown tool: "job_read_output"`, and `job_status` on the referenced job
fails `job "job_..." not found`.

Jesse's ruling (2026-07-31): re-expose a scoped read path. An observer granted
access to a job referenced in a delivered frame should be able to read that
job's output. This design says how, and how narrowly.

## What June removed, and why

Three commits, not two. The design must answer all three.

| Commit | Removed | Stated reason |
| --- | --- | --- |
| `9d0d777c6` (Jun 22) | `job_watch` `target`/`send` routing; `delegate_send(to="caller")` | Watching becomes source-owned; delivery is implicit to the watch's creator. `delegate.watch_parent` becomes the explicit, non-transitive parent-observation grant. |
| `cf84923c6` (Jun 23) | `job_read_output` from the tool registry | "the public tool surface moves off the overloaded read API"; supervision splits onto `job_status`, evidence onto `read_transcript`, readiness onto `wait_for_transcript_match`. Handler kept "reachable from tests only". |
| `64a3bcb21` (Jun 23) | `wait_for_transcript_match` | "Serf should not add a blocking transcript wait primitive; one future readiness signal belongs on `job_watch(output_match)`, and raw evidence stays on `read_transcript`." |

Read together those reasons are narrower than "observers may not read". They
are three statements about *shape*:

1. the model does not configure where a frame goes (`9d0d777c6`);
2. one tool should not be status, evidence, paging, grep, and a wait
   (`cf84923c6`);
3. reads do not block (`64a3bcb21`).

This design keeps all three. Delivery stays implicit and runtime-chosen; the
observer's read lands on `read_transcript`, which is already the evidence tool;
and the granted read is snapshot-only by construction rather than by an error
string.

## Decision

**Frame-referenced grant, minted by the runtime at delivery, spent through
`read_transcript(transcript_ref="job:<job_id>")`.**

When Serf delivers a watch frame whose event payload structurally names a
concrete job, it durably records that the receiving observer session may read
that one job's output. The observer spends the grant by passing the `job_id`
it read out of the frame's `event:` block to the evidence tool every session
already has.

Three properties fall out of the mechanism rather than being enforced on top of
it, and they are the reason this is a small change:

- **Terminal-only.** The only model-facing event kind whose payload carries a
  structured job id is `job.notification`, which maps to
  `events.EventJobFinished` (`modelEventKinds`, `agent/job_watch.go:37`).
  `communicate` and `assistant.tool` payloads carry no job reference. So every
  frame-minted grant names a job that has already finished.
- **Push-only naming.** The observer never names the job. The watched session's
  own watch configuration decides which completions are delivered, and the
  grant covers exactly what was pushed.
- **Non-transitive.** The lookup seam a child holds is
  `spawn.parentGrantedJobRead = <direct parent>.lookupGrantedJobRead`. A
  grandchild consults the observer's store, which mints nothing. A grant
  cannot propagate downward.

## Capability model

### Who may read what

A session S may read job J's output when either
(a) S owns J or reaches it through the live-descendant ownership walk — today's
rule, unchanged — or
(b) the durable grant table in S's **direct parent's** job store contains the
pair `(S.session_id, J.job_id)`.

Rule (b) is populated by exactly one path: a `source:"parent"` watch whose
event set includes `job.notification` fires, and the resolved delivery names
S as the receiver.

### Granted how

At `recordWatchSend` (`agent/job_watch.go:3043`), which already calls
`mintWatchSendReadGrant` before persisting the pending send. The change is to
the mint's early return. Today:

```go
if cfg == nil || resolvedSendTo == runtimeMessageAliasCaller || isWatchSessionTarget(watchedIdentity) {
    return
}
```

`watchedIdentity` for a session-source watch is the alias `caller`
(`watchEventWatchedIdentity`, `agent/job_watch.go:2421`), so every parent-source
delivery bails here. The mint instead derives the grantable job id from the
delivered event payload:

- `events.JobFinishedData` (and its pointer form) → `data.JobID`;
- every other payload → no grant, unchanged early return.

`cfg.receiverSessionID` is already the observer's session id for a
parent-source watch (`installParentSourceWatchForChild`,
`agent/subagents.go:811`), so no new plumbing resolves the observer. The
existing `cfg.grantsMinted` dedup keys on `(sendTo, watchedJobID)` and needs no
change once `watchedJobID` is the concrete id.

The grant is minted from the payload the runtime constructed, never from a job
id the model supplied. That is already true of the mint site and must stay
stated as an invariant.

### Revoked when

**Never.** Grants stay append-only, matching today's fold
(`jobstore.FoldGrants`, "Grants are append-only capabilities, so the fold is
order-insensitive") and today's comment at `agent/job_watch.go:593` ("grants are
append-only read capabilities, never revoked ... so an orphan grant is
harmless").

That was cheap to assert while the door was unreachable. It is a real decision
now, so here is the argument for keeping it: a frame-minted grant covers a
terminal job's retained output. The bytes are immutable, already bounded by the
per-job retention cap, and already subject to eviction. The observer session
holding the grant is a delegate whose lifetime is bounded by its parent. A
revoke event would be new durable machinery, would make `FoldGrants`
order-sensitive, and would let a parent's `job_watch(operation="clear")` yank a
capability out from under an observer that is mid-turn on a frame it already
received.

Alternatives are presented under [Open decisions](#open-decisions-for-jesse);
this is a recommendation, not a settled call.

### What the grant does not confer

- **No enumeration.** The grant is a capability on one id handed to the
  observer, not a directory. `job_list` stays session-local.
- **No status lookup.** `job_status` on a granted job keeps failing
  `not found`. The frame already carries `status`, `reason`, `exit_code`, and
  `output_bytes`, so a grant-aware `job_status` would add a second way to learn
  what the observer was just told — and it would leak far more than status.
  `jobTranscriptRef` (`agent/session_tools_jobs.go:2633`) projects a delegate
  job's **session** transcript ref, and session refs are not access-controlled
  (non-goal 4). A grant-aware `job_status` on a delegate job would therefore
  hand the observer the key to the whole child conversation. It stays denied.
- **No transcript access.** See non-goal 4 — this is the sharpest edge in the
  design.
- **No control.** `job_stop` on a granted job is unaffected and still fails.

## Tool surface

**Recommendation: extend `read_transcript`'s existing `job:<job_id>` path with
the grant seam. Do not re-register `job_read_output`.**

`read_transcript` is registered unconditionally in every session, delegates
included — `transcriptTools` (`agent/session_tools_transcript.go:29`) puts it in
the base set before the `stateDir` gate, with the comment "read_transcript is
always available for `job:<job_id>` refs", and it appears on no root-only
removal list. So the observer already has the tool. Nothing is added to any
tool set; one resolution path inside a tool the model already knows learns one
more way to succeed.

This engages the June reasons rather than reversing them:

- `cf84923c6` moved evidence onto `read_transcript`. The observer reads
  evidence with `read_transcript`. Same tool, same call shape, same mental
  model as every other session.
- `cf84923c6`'s objection was to the *overloaded* read API — status plus
  evidence plus paging plus grep plus a wait in one call. The granted read
  inherits `read_transcript`'s narrow shape: a ref in, a bounded markdown
  envelope out.
- `64a3bcb21` forbade blocking reads. `read_transcript` has no `max_wait_ms`.
  The snapshot-only constraint that `job_read_output` enforces with an error
  string (`grantedReadBlockUnsupportedErr`, `agent/session_tools_jobs.go:57`)
  becomes structural: there is no parameter to reject.
- `9d0d777c6` made delivery implicit. Grants are minted by the delivery path.
  The model configures nothing about them.

It also lands the fix where a second, unrelated hole already needs it. See
[the collateral gap](#the-collateral-gap-descendant-reads).

### What has to change

1. **Grant fallback in `readJobTranscript`** (`agent/session_tools_transcript.go:374`).
   It resolves only `deps.jobManager`. Add the same fallback shape
   `jobReadOutputTool` already uses: on a local miss, consult an injected
   `deps.grantedJobRead(deps.sessionID, jobID)`; on a grant miss, preserve the
   original error unchanged. `toolDeps` already carries `sessionID` and
   `jobManager` (`agent/session_tool_registry.go:107,110`), so this is one new
   field populated from `s.cfg.spawn.parentGrantedJobRead` where the rest of
   `toolDeps` is built (`agent/session_tool_registry.go:250`).

2. **A delegate branch in the job renderer.** `renderShellJobTranscript`
   (`agent/session_tools_transcript.go:412`) emits `# Shell Job <id>` with a
   `command:` line. The jobs named in a `job.notification` frame are usually
   delegate jobs, and a delegate has no command and does have a report and a
   `structured_result`. Rendering a delegate result under a "Shell Job" heading
   would be a lie in the model's evidence stream. This is genuinely new work
   that the granted path is the first caller to need: today delegate
   notifications hand out the child's session ref, not a `job:` ref
   (`notificationTranscriptRef`, `agent/job_notify.go:119`), so no live caller
   passes a delegate `job:` ref. Estimate ~40 lines including the
   `structured_result` rendering.

3. **A `job.notification` frame footer** naming the call. The frame is the
   observer's only teaching surface for a capability it acquires mid-run.
   One line after `output_bytes`, emitted only when a grant was minted for this
   delivery, e.g. `read with: read_transcript(transcript_ref="job:job_...")`.
   Without it the observer has to be told about the mechanism in its task
   prompt, which is how sidecar cards currently paper over gaps.

### Alternatives rejected

**(a) Re-register `job_read_output` for every session.** Rejected. It is a
straight revert of `cf84923c6` for the 99% of callers who never needed it, and
it hands every session two ways to read its own job output. It also drags back
`max_wait_ms`, `grep`, `from_line`/`line_count`, and the "Advanced output
paging" contract — the overload the commit named.

**(b) Register `job_read_output` only for sessions holding a grant.** Rejected.
Tool sets are built at registration and cached (`s.cachedToolDefs`); grants are
minted at delivery, mid-session. The tool would have to materialize between
turns. That is mechanically possible but teaches a tool that exists only
sometimes, and fd8n's doc sweep would have to explain a conditionally present
tool to a reader who cannot see the condition.

**(c) A new narrowly-named tool** (`job_read_granted_output` or similar).
Rejected under YAGNI: more surface, another name in every prompt and card, for
a call the model can already make.

**(d) Put the delegate's `transcript_ref` in the frame** and let the observer
use `read_transcript` with no grant at all. Rejected, and it is the most
important rejection in this document — see non-goal 4.

`DefJobReadOutput` (`agent/internal/tool/definitions.go:259`) and
`jobReadOutputTool` stay where they are: unregistered, test-only, and now with
a design record saying so. fd8n's doc sweep should move them into
`docs/job-control.md`'s "Removed / intentionally absent tools" section rather
than describe them as live.

### The collateral gap: descendant reads

`readJobTranscript` resolves `deps.jobManager` and nothing else: one
`findJobRecord` against the caller's own store, then a `readJobWindow` on the
same manager. It never learned the ownership walk. `job_status` does one hop
(`nestedOrLocalJobManager`, `agent/jobs_nested.go:272`) and returns status, not
output.

So `cf84923c6` did not only close the observer's door. It left **depth ≥ 2
descendant output reads with no model-facing tool at all**, while
`docs/job-control.md:1140` still promises in detail that "`job_read_output`
resolves a descendant job at depth ≥ 2 ... through the recursive owner path",
and `test/scenarios/job-nested-visibility.md` still instructs a parent to read
a nested shell job it does not own. `jobReadOutputTool` is the only caller that
ever wired `resolveDescendantJobOwner` into a read, and it is unregistered.

That matters here because it is the same twenty lines. Teaching
`readJobTranscript` the resolution chain — local, one-hop, descendant walk,
grant, then the original error — closes the observer gap and the descendant gap
in one place, and gives fd8n somewhere to send its one DESCENDANT-READ card.
Whether to take both at once is [open decision 6](#open-decisions-for-jesse);
the observer path alone would need the same seam anyway, just with one fewer
step in the chain.

## Interaction with the ownership walk

`resolveDescendantJobOwner` (`agent/jobs_nested.go:312`) recurses **down** the
live subtree: DFS through `liveSubagentSessions`, applying the single-hop
`ownerJobManagerFor` at each node until an owner store holds the job id. It
never looks up.

This design does not change that walk, widen it, or add an upward variant. The
grant is a strictly-later fallback in the same resolution order
`jobReadOutputTool` already establishes (`agent/session_tools_jobs.go:479-504`):

```text
local store
  → one-hop owner (direct children)
  → resolveDescendantJobOwner (live subtree, downward)
  → grant table lookup            <- the only new step
  → original target_not_found preserved on a miss
```

A grant miss returns the error the walk already produced. The grant is a
lookup keyed on `(observer_session_id, job_id)`, never a search: nothing
iterates, nothing enumerates, and no session is visited that the walk would not
already have visited.

### Why this is not the ancestor-`output_match` door

Jesse ruled on 2026-07-30 that ancestor-job `output_match` access is not to be
added. That ruling is untouched here, and the two are different doors:

| | ancestor `output_match` (ruled out) | frame-referenced read grant |
| --- | --- | --- |
| Who names the job | the observer, at will, by id | the watched session's watch config, by delivering a completion |
| When | before or while the job runs | after the job is terminal |
| What is installed | a live subscription on another session's output stream | nothing; one row in a lookup table |
| Cost to the watched session | its job manager runs the observer's regex on every append | none; a read of retained bytes |
| Discovery | the observer must enumerate or guess ancestor job ids | the id was handed to it |
| Failure if abused | an unbounded upward-reaching capability | the observer reads a job it was told about |

The ruling forbade *reaching up*. This design does not let an observer reach
up; it lets an observer hold on to what was handed down. The direction of
initiation is the entire distinction, and it is structural rather than
policed: every row in the grant table was written by the watched session's own
delivery path, from an event payload the runtime constructed.

The 2026-07-30 ruling's own suggested repair was "wake on `job.notification`,
then read the referenced job through a frame-granted `job_read_output`"
(quoted in `test/scenarios/sidecar-test-triage-shell-frame.md:22-24`). That
repair was unreachable for the two reasons in the Problem section. This design
makes exactly that repair reachable, which is why it is consistent with the
July 30 ruling rather than a reversal of it.

The concrete-job watch source is unaffected: `job_watch(source=<job_id>)` still
resolves owner-or-live-descendant only, and an observer naming an ancestor's
job still fails `target_not_found`. That falsification in
`sidecar-test-triage-shell-frame.md:92-97` stays valid after this lands.

## Failure modes

**Grant outlives the job record.** `lookupGrantedJobRead` returns `ok=false`
when `findJobRecord` misses, and the caller preserves its original
`target_not_found` — "the caller preserves its original target_not_found
instead of inventing a new failure mode for a read the observer was never
promised" (`agent/job_watch.go:3230`). Correct as-is; the granted
`read_transcript` path must adopt the same rule.

**Grant outlives the parent session.** `lookupGrantedJobRead` returns
`ok=false` on an unreadable or closed store. `grantedJobRead.snapshot` has no
closed-store fallback chain by design — "a failed parent-side read is a real
error" (`agent/session_tools_jobs.go:843`). Keep that. An observer outliving its
parent is a diagnostic situation, not a case to paper over with a forwarded
copy.

**Output truncation and eviction.** The granted snapshot carries
`total_bytes`, `dropped_bytes`, and `truncated` through the same window reader
as a local read. The `read_transcript` job envelope already surfaces
`total_bytes`/`dropped_bytes` in its header and `Truncated` in its meta, so a
granted read inherits honest accounting.

One pre-existing gap to *not* fix here and to *not* let fd8n teach as live:
`docs/job-control.md` documents an `output_unavailable=true` /
`retention_pruned` response for pruned content, and it is not implemented —
`agent/internal/jobstore/output.go:21` says the signal is "for a later phase".

**Concurrent stop.** Structurally impossible for frame-minted grants. The
grant is minted from `EventJobFinished`, so the job is terminal before the
grant exists; an in-flight `job_stop` resolves into the terminal status the
frame reports. This is the failure mode a live-subscription design would have
had to solve, and the terminal-only property retires it.

**Frame delivery fails after the mint.** `recordWatchSend` mints before
persisting the pending send, deliberately: "so a durable pending send always
implies its grant was at least attempted (restore re-delivers pendings without
re-running this path)". A frame that is then dropped leaves an orphan grant for
a completion the observer was never told about. Harmless — the observer has no
way to learn the id — but it is a genuine over-grant relative to what was
delivered. Document it; do not add compensation.

**Coalescing.** Frames coalesce latest-wins per durable key while the recipient
is busy. A superseded frame's grant survives (append-only), so an observer can
hold grants for completions it never saw. Same shape and same disposition as
the previous item.

**Budget exhaustion.** A watch that spends its 50-delivery budget is
auto-cleared. Grants already minted survive, consistent with no revocation.

**Self-grants from callback jobs.** `events:["job.notification"]` on
`source:"parent"` fires for **every** job the parent completes, including the
observer's own resumed callback jobs — the sharp edge already documented in
`test/scenarios/sidecar-handoff-packager-job-notification.md`. Each callback
round therefore mints a grant for the observer on its own delegate job.
Harmless (it is the observer's own output) but it means grant rows accumulate
per round, and it has one visible consequence: the hub's observer auto-open
projection (`LoadSessionObserverGrants`, `agent/observer_grants.go:103`) inverts
grants into worker→observers and will emit a self-observation row for the
observer's session. Cosmetic, but it will show up in the workspace, so it should
be a known consequence rather than a surprise. If it is judged unacceptable,
the narrow fix is to skip the mint when the finished job's `DelegateID` is the
receiver's own — one condition at the mint site, not a new concept.

## What fd8n's docs will teach

An occurrence-level sweep of the current tree finds 50 mentions across 17
scenario cards plus `INDEX.md`, 48 in `docs/job-control.md`, 3 in
`docs/agentic-testing.md`, and 1 in `docs/architecture.md`. fd8n's kata comment
says the sweep should teach the new mechanism rather than scrub the references.
This design fixes what "the new mechanism" means, and the references sort by
what each was actually doing:

| Shape | Cards | Mentions | Repair |
| --- | --- | --- | --- |
| Self-read (caller reads its own shell job or direct delegate) | 13 | 34 | `job_status` for orientation, `read_transcript(transcript_ref)` for evidence. No grant, no mention of one. |
| Descendant read (caller reads a job owned by a session below it) | 1 | 2 | Blocked on open decision 6 — there is no live tool for this today. |
| Observer read (sidecar reads a job in another session's store) | 2 | 5 | Both are boundary pins owned by f9gn; their assertions invert. |
| Tool-under-test | 1 | 6 | Retire the card. |

Two corrections to the assumptions in fd8n's kata text, both of which change
the sweep:

**There are no cards that actively teach an observer to read.** Both
OBSERVER-READ cards (`sidecar-test-triage-shell-frame.md`,
`sidecar-handoff-packager-job-notification.md`) are f9gn's boundary pins:
they mention `job_read_output` to assert it is *absent*. So the sweep's
observer work is not "preserve these references" but "invert one of them" —
see the acceptance test below.

**The contract doc never learned about the replacements at all.**
`cf84923c6` touched 19 Go files and zero docs. `docs/job-control.md` today
contains **zero** mentions of `job_status` and zero of
`wait_for_transcript_match`, while describing `job_read_output` across 21
sections as the live, only, model-facing output tool. The doc is not drifting
from the June work; it predates it entirely. fd8n is therefore not a
find-and-replace pass — it is writing the post-June output contract for the
first time, which is exactly why it should wait for this design.

**The tool-under-test card** (`job-read-output-blocking-grep.md`) retires.
It covers `max_wait_ms` + `grep`, and both the tool and the blocking read are
gone by `cf84923c6` and `64a3bcb21`. The behaviour it guards survives in
`agent/session_tools_jobs_read_output_test.go` (31 tests, 1113 lines), which is
the retained-output/windowing coverage `cf84923c6` deliberately kept.

**`docs/job-control.md`** needs, at minimum:

- `### job_read_output` (`:641`) and `#### Advanced output paging` (`:699`)
  retired out of "Model-facing tools" and reduced to a
  `### No job_read_output` entry in "Removed / intentionally absent tools"
  (`:913`), next to "No `wait_job`" / "No `job_ack`";
- `### No wait_job` (`:915`), which currently offers
  `job_read_output(max_wait_ms=...)` as the sanctioned bounded wait — that
  primitive is gone; it must point at `job_watch(output_match)`;
- the V1 tool availability matrix (`:903-904`), which lists `job_read_output`
  in both the root and the delegate row;
- the observer bullet (`:1211`), rewritten from "Serf grants the observer
  `job_read_output` for that watched job" to name the concrete frame kind, the
  concrete call, and the terminal-only scope;
- the "Relationship to transcript tools" decision table (`:1235-1236`), whose
  "Shell stdout/stderr" and "Delegate invocation final report/log" rows both
  say `job_read_output(job_id)`;
- the retention bullet at `:928` asserting `output_unavailable`/`retention_pruned`,
  which is not implemented;
- the notification bodies at `:1050`, `:1057`, `:1063`, and `:1104`, which
  quote a canonical terminal-notification text saying "Use job_read_output to
  inspect output" — the code has said `read_transcript` since `cf84923c6`
  (`formatJobNotificationBlock`, `agent/job_notify.go`);
- `## Nested jobs` (`:1140`, `:1178`) and `## Output storage` (`:1038`), which
  promise recursive descendant reads through a tool that is not registered
  (see [the collateral gap](#the-collateral-gap-descendant-reads));
- `### job_watch` (`:595`) and `## Observer and sidecar composition` (`:1191`),
  which describe the read grant as extending `job_read_output` — these become
  the doc home for the mechanism this spec defines.

**`docs/agentic-testing.md`** needs its two audit recipes revisited:
`## Auditing sidecar scenarios` (`:336`) greps
`serf-doctor transcript --count job_read_output` and asserts the count is zero
before an observer callback, treating any such call as an impatience smell;
`## Inspecting watch sidecars` (`:867`) lists parent-side `job_read_output`
polling as a fluency anti-pattern. Both are about *polling*, and both are still
right in substance — but after this lands, one granted `read_transcript` call
between the frame and the callback is the sanctioned path, not a smell. The
counted tool name and the threshold both change.

**Cards this design breaks by design.** eqs0's coverage note flagged one:
`test/scenarios/sidecar-handoff-packager-job-notification.md` currently pins the
boundary as a falsification — "if either call SUCCEEDS, cross-session observer
reads have been added". After this lands the granted read succeeds, so the
card's assertion inverts: the observer reads the worker's output through the
frame's `job_id`, and the falsification becomes the read *failing*. The
`job_status` half of that falsification stays valid under the recommendation
(open decision 2). That card, not fd8n's sweep, is the acceptance test for this
work.

`test/scenarios/sidecar-test-triage-shell-frame.md` survives unchanged: its
premise is the ancestor-`output_match` ruling, which this design does not touch.

## Non-goals

1. **No ancestor `output_match`.** The 2026-07-30 ruling stands.
   `watchSourceConcreteJob` resolution stays owner-or-live-descendant, and an
   observer naming an ancestor's job still fails `target_not_found`.

2. **No return of model-configured delivery.** `job_watch(target=..., send={to:...})`
   stays deleted. Grants are minted by the runtime at delivery; the model never
   configures a grant, names a grantee, or asks for one.

3. **No grants from `communicate` or `assistant.tool` frames.** Those payloads
   carry no structured job reference. Parsing one out of `arguments_json` or
   `output` text would be guessing, and a capability minted from a guess is
   worse than no capability.

4. **No `transcript_ref` in the `job.notification` frame.** This is the sharpest
   edge in the design and the reason option (d) was rejected. Session transcript
   refs are **not** access-controlled: `resolveTranscript`
   (`agent/transcript_lookup.go:23`) resolves any `local:<id>` in the current
   bucket and any `proj:<project>:<id>` sibling bucket, for any caller. The only
   thing keeping an observer out of a worker's full conversation today is that
   the frame withholds the ref. Adding `transcript_ref` to the frame would hand
   the observer the entire child transcript — tool calls, arguments, reasoning —
   through a path with no grant check at all. That is a far larger capability
   than this design grants, acquired silently. The frame keeps carrying `job_id`
   only, and the granted read goes through the `job:` path precisely because
   that path resolves through a gated job manager.

5. **No `job_list` visibility.** A capability on one id, not a directory.

6. **No blocking cross-session reads.** No `max_wait_ms`, no readiness wait, no
   polling helper. `64a3bcb21` settled this and the chosen surface has no
   parameter to add.

7. **No hub or web-UI change.** `LoadSessionObserverGrants` reads the same
   durable grants and gains rows without a code change. The self-observation row
   noted under failure modes is a consequence to accept or fix at the mint site,
   not a projection change.

8. **No backward-compatibility shim.** Nothing in the repo can be holding a
   grant it could spend today, because no path could mint one for a reachable
   watch shape. There is no old behaviour to preserve.

## Open decisions for Jesse

**Ruled by Jesse, 2026-07-31** (walkthrough; details on kata eqs0):
1 = extend `read_transcript` (as recommended). 2 = `job_status` stays
denied; fix the error text (as recommended). 3 = never revoked (as
recommended). 4 = delete the unreachable mint (as recommended).
5 = **skip self-grants at the mint** (against the accept-for-now
recommendation): with never-revoked grants, accepted junk rows would be
permanent, and the table should hold only rows that confer real access —
condition `finished job's DelegateID == receiver session id`, two-case
test. 6 = take both: close the descendant gap in the same seam. The
original decision texts below are kept for the reasoning.

**1. Tool surface.** Recommend extending `read_transcript`'s `job:` path.
The rejected alternatives are re-registering `job_read_output` unconditionally
(a), registering it only for grant-holding sessions (b), a new tool (c), or
putting the transcript ref in the frame (d). The recommendation costs a
delegate branch in the job renderer (~40 lines) that (a) and (b) would not
need, and buys no new tool name in any prompt, card, or provider tool list.

**2. `job_status` on a granted job.** Recommend keeping it denied, and this is
the one recommendation I would push back on if you wanted it the other way.
The obvious argument for allowing it is coherence: `job "job_..." not found` is
a confusing answer about a job the runtime named to this session one turn ago,
and the fix looks like six lines. But `jobTranscriptRef` projects a **delegate**
job's session transcript ref, and `resolveTranscript` gates nothing (non-goal 4).
A grant-aware `job_status` would therefore convert a one-job output grant into
full read access to the child's conversation, silently, through a field nobody
was thinking about. If the confusing error is the real problem, fix the error
message ("this job belongs to another session; read its output with
read_transcript(transcript_ref=\"job:...\")"), not the capability.

**3. Revocation.** Recommend none — grants stay append-only, matching today's
fold and today's stated contract. Alternatives:
(i) `job_watch(operation="clear")` revokes the grants that watch minted, which
needs a revoke event kind, makes `FoldGrants` order-sensitive, and can yank a
capability out from under an observer mid-turn;
(ii) time-bounded grants, which need a clock in the fold and a new expiry
failure mode for a read the observer was legitimately promised.
Neither buys much against terminal, immutable, retention-capped bytes, but
"never revoked" is a contract worth affirming deliberately now that it is
reachable.

**4. The unreachable create-time mint.** `mintWatchCreateReadGrant`
(`agent/job_watch.go:3270`) requires a concrete job target plus a delegate
delivery target — exactly the shape `9d0d777c6` deleted. Under this design it
stays unreachable. Delete it now, or keep it as the natural mint site if a
concrete-job sidecar shape ever returns? I lean delete: eqs0 exists precisely
because unreachable machinery rots into documentation that lies. Deleting it
also removes the one path that could grant on a *running* job, which is what
makes the terminal-only property hold by construction rather than by
convention. It is a deletion, so it is your call.

**5. Self-grants from callback jobs** (failure modes, last item). Accept the
extra grant rows and the hub's self-observation row, or skip the mint when the
finished job's `DelegateID` is the receiver's own? Recommend accepting for now
and revisiting if the workspace row is ugly in practice.

**6. Scope: close the descendant gap in the same change?** `cf84923c6` left
depth ≥ 2 descendant output reads with no model-facing tool, and
`docs/job-control.md:1140` still promises them in detail. It is the same seam,
about twenty extra lines (one `resolveDescendantJobOwner` step and the
closed-store fallback `jobReadOutputTool` already has). Taking it means fd8n's
`job-nested-visibility.md` has somewhere to go; leaving it means fd8n has to
either delete that card or file a third kata. I lean toward taking it, because
splitting one seam across two changes is how a half-wired resolution chain gets
shipped. It is scope beyond eqs0's ruling, so it is your call.

## Verification

Unit, in `agent/`:

- a `source:"parent"` watch with `events:["job.notification"]` mints a grant for
  the receiver session on the concrete finished job id, and the durable event
  folds through `FoldGrants`;
- the same watch delivering a `communicate` or `assistant.tool` frame mints
  nothing;
- the grant is non-transitive: a delegate of the observer gets no grant for the
  same job;
- `read_transcript(transcript_ref="job:<granted id>")` from the observer returns
  the terminal job's output with honest `total_bytes`/`dropped_bytes`;
- a grant miss preserves the original `target_not_found` unchanged;
- a closed parent store fails the granted read rather than falling back;
- a delegate job renders as a delegate (report and `structured_result`), not as
  a shell job;
- `job_status` on a granted job still fails, and its failure does not disclose
  the job's `transcript_ref`;
- if open decision 6 is taken: a depth ≥ 2 descendant job reads through
  `read_transcript(transcript_ref="job:<id>")`, and the read is served from the
  resolved owner's store.

Live, on an isolated hub with a scripted backend, following the f9gn method:

- rerun `test/scenarios/sidecar-handoff-packager-job-notification.md` with its
  inverted assertion — the observer reads the worker's result through the
  frame's `job_id` and packages from the real output rather than from
  `output_bytes`;
- confirm `sidecar-test-triage-shell-frame.md`'s dead-trigger falsification
  still fires: an `output_match` watch on an ancestor-owned job still fails
  `target_not_found`.

The second one is the guard that this design did not quietly become the door
the 2026-07-30 ruling closed.
