# Timers as job_watch triggers

Date: 2026-09-02. Status: design, revision 20. Awaiting Jesse's review.

## Problem

Waiting is an antipattern. A session that has fanned out work should end its
turn and be woken when something happens, not block inside a tool call. Evener
already does this for the work it tracks: a finished delegate or background
shell job wakes its owning session with a notification turn. What Evener cannot
do is wake a session for state it does not track, such as an external service
the session must check every few minutes, or a deploy that will be done in
about ten minutes. The blocking `delegate_wait` tool (PR #843) was closed
unmerged because it is the antipattern; this design supplies the wake-up path
instead.

Decisions already made:

- One wake per completion. When several reports only make sense together, the
  prompt tells the parent to delegate a coordinator that fans out and reports
  once. No barrier primitive.
- Timers are wanted: wake in N, wake every N, and cancel.
- Fewer tools. The timer extends `job_watch` rather than adding a tool.
- No aggregate cost bound and no lifetime beyond the floor and the cap.
  Deal with runaway cost when it is a problem.

## Target scenario

"Watch for roborev comments on our PR and handle them until it is
merge-ready." The session pushes a branch, opens the PR, then:

1. Creates a recurring timer:
   `job_watch(operation:"create", repeat_seconds:300, note:"PR #123: handle
   roborev comments newer than id 0; clear when merge-ready")`. The result
   echoes the watch id, the interval, and the note.
2. Ends its turn. Nothing is running; nothing is consumed.
3. Every five minutes one block arrives carrying the note. The session runs
   `gh api` for comments newer than the id in the note, addresses them,
   pushes, clears the timer, and creates it again with an updated note
   ("newer than id 456"). The note is the loop's state and it survives
   transcript compaction; the new id comes back in the create result. A
   second PR is a second timer. If a wake's work runs past the next tick,
   the queued ticks fold into one block at the next boundary that says how
   many fired.
4. When the PR is merge-ready it clears the timer and reports.

Limits, stated so the reader can judge them: a timer runs until cleared or
until an Evener restart, which cancels every watch and tells the session
so with the existing generic notice. Timers work in any served session,
root or delegate; a run-mode process refuses them because its lifetime
ends with the turn's work. The only bounds on volume are the 60-second
floor and eight live timers per session, so a session can be woken at most
eight times a minute by its own timers, visibly and by its own doing.

## What exists today (verified in code)

- **Per-completion wakes.** A finished delegate or background shell job
  wakes its owning session with a guaranteed `EntryNotification` turn; a
  busy session sees it at the next turn boundary; an idle delegate is woken
  by its parent's drive path. A queued user message outranks a pending
  notification, and a session awaiting an answer to a question runs no
  notification turns. `SetNotifyFunc` fires at wiring time when job
  notifications are already pending, which is how restore-time
  notifications reach a bridged session.
- **Run mode.** `SessionConfig.TurnEndsProcess` marks a process that exits
  when the turn's work is done; delegates inherit it.
- **`job_watch`.** Operations `create`, `list`, `inspect`, `clear` by
  `watch_id`; clearing an inactive or unknown id is a documented no-op
  success. The model reads the `Output` text of `formatJobWatch`,
  `formatJobWatchList`, and `formatJobWatchInspect`; a `StateResult`'s
  `State` goes to the hub only. `progress_interval_ms` on any session
  source (`self`, `parent`, `dlg_`, which normalize to one alias) already
  fires every interval regardless of anything running, clamped to 1,000 to
  3,600,000 ms, with no note; it is the spin loop this design forbids.
- **Two delivery rails.** `self` and concrete `job_` watches deliver a
  no-send notification into the session's `pendingJobNotifs`; `parent` and
  `dlg_` watches always get a send target and deliver a rendered frame. The
  running-target gate in `decideProgressTick` applies only to concrete
  `job_` sources.
- **Identity.** `watchKey` is (visible session, target, send-to, receiver)
  with no trigger component, built before the watch id exists; a same-key
  create replaces or no-ops. `watchKeyMatchesClearRequest`,
  `watchConfigMatchesWatchKey`, and `watchSendKeyMatchesWatchKey` compare
  keys against requests, configs, and durable send keys, treating empty
  request fields as wildcards; `configureWatch` runs a detached-send sweep
  on every create through the second.
- **Receiver-routed watches.** A session watching a descendant's job
  installs the watch in the descendant's manager with `ReceiverNotify`
  pointing back; the job's terminal notification goes to the owner, so the
  unfired notice is that watcher's only wake when the job ends.
- **Periodic engine.** `startProgressTimer` runs one goroutine per watch
  with `progressIntervalMS > 0`; `decideProgressTick` returns
  `{keepAlive, fire}` per tick and fires session targets unconditionally;
  `fireProgressTick` builds the notification under `jm.mu` and enqueues
  after. `initProgressStop` allocates the stop channel for the same
  condition and `closeWatchConfig` is the sole cancel path, called under
  `jm.mu` by every teardown. The code already separates `conditionFires`
  from `deliveries`.
- **Fire path and locks.** Every fire path builds its notification under
  `jm.mu` and enqueues after releasing it, because `routeWatchNotifications`
  re-acquires `jm.mu`. The established order is `jm.mu` then
  `pendingJobNotifsMu`; the comment in `agent/session.go` asserting the
  opposite is stale.
- **Budget.** A watch auto-clears after 50 deliveries of any kind,
  periodic ticks included, with a notice that names the internal alias
  and recommends `output_match` remedies.
- **Rendering and replay.** `formatJobNotificationBlock` writes a fixed
  attribute list, then for a watch notification with an empty job id
  renders `Watch event triggered: <reason>.`.
  `durableJobNotificationAlreadyInjected` treats any historical
  `<job-notification>` block containing a job id as proof that job's
  terminal notification was shown, unless the block carries `event="watch"`
  or `status="watch"`. `escapeNotificationBody` escapes only `<`;
  `escapeNotificationText` does not escape newlines. `jobNotification` is
  in-memory only and carries no watch id.
- **Hub and TUI.** The hub classifier types a block as a watch only with a
  marker and no job id, gives `event="watch"` blocks a warning tone and
  shows the reason under that tone, and `splitNotificationExcerpt` already
  computes the body prose that both call sites discard. The TUI appends
  every watch block verbatim.
- **Restart.** Every active watch is cleared with `runtime_lost` and each
  receiver gets one generic notice to re-register.
- **Prompt doctrine.** `background-jobs.md` says: end your turn, the
  notification resumes you, never spin on `job_status`, "one future signal
  or a recurring condition → `job_watch`", "to block on one specific future
  signal, create a `job_watch`", never leave "a watcher" running, and
  "for sustained observation prefer an observer delegate". It is rendered
  for a subagent only when it can delegate; `workflow.md.tmpl` renders for
  every subagent and gates its `job_watch` sentence on the tool being
  present. `identity.md` already says never to guess a pending result.
  Tests pin the lead-ins "Pick the waiting primitive by how many answers
  you need:" and "Do not call `job_status` in a loop".
- **Clock.** The job manager clock provides `NewTicker`, `NewTimer`, and
  `AfterFunc`. The fake ticker drops a fire that lands while the previous
  one is unread.
- **Production-integration inventory.** `agent/delegate_tree_controller_test.go`
  pins call sites of inventoried symbols, including in `session_tools_jobs.go`.

## Design

### Three new `create` properties

- **`after_seconds`** (integer, nullable): one-shot. Fires once,
  `after_seconds` after creation, then ends. Bounds 60 to 86,400.
- **`repeat_seconds`** (integer, nullable): recurring, `self` only. Bounds
  60 to 3,600. Named to stay clear of the existing `every`, which counts
  events.
- **`note`** (string, nullable): text delivered with every fire, so the wake
  carries its own reason and, in the target scenario, the loop's state.
  Truncated to `watchMessageMaxChars` (2,048) with the existing
  `limitWatchText` discipline. Stored raw; escaped only at block render
  through `escapeNotificationBody`, like every other body text, so a note
  pasted from a PR comment cannot close or forge a block. The block shows
  `<` as `&lt;`; `inspect` returns the note verbatim, and the description
  says so.

Out-of-range values are rejected, not clamped, in `normalizeWatchArgs`
where bounds live today. A present `0` is rejected with the bounds
message; null reads as absent. There is no `update`: to change a note,
clear and create.

Both time fields are surface names for the existing periodic engine. A
timer config sets `progressIntervalMS` to the seconds times 1,000 and a
`timer` marker; `startProgressTimer`, `decideProgressTick`, and
`fireProgressTick` run it unchanged, `initProgressStop` and
`closeWatchConfig` cancel it unchanged, and a one-shot is the existing
decision struct returning `keepAlive:false, fire:true` on its first tick.
No second ticker, no second fire path.

**`progress_interval_ms` is confined to concrete jobs.** On a session source
it is refused in `validateWatchTriggerShape`, where trigger-shape rules
live today, with one message: `invalid_request: progress_interval_ms is a
job progress trigger; for a timer use repeat_seconds`. Internal callers
that pass a target with no source are untouched. The property text and
the description's "periodic progress uses" sentence name concrete jobs.

### Where timers and notes apply

- Both time fields: `self` only. On any other source: `invalid_request:
  timers apply to source self; delegates and jobs wake you when they
  finish`. There are no timers on job or delegate sources; "nudge me if
  this job is still running in ten minutes" is a self one-shot whose note
  names the job, followed by `job_status` on wake, and the tool
  description says so.
- `note`: only with a time field. Otherwise: `invalid_request: note
  applies to timers`. These rules live in `validateWatchTriggerShape` too.
- A time trigger is refused when `TurnEndsProcess` is set
  (`invalid_request: timers need a session that outlives the turn`) and
  accepted in any other session, root or delegate. A delegate's timer fires
  into its own queue and the parent's drive path wakes it. A delegate that
  reports with a timer still armed is not done: its next tick re-drives it
  and it reports again. Nothing clears timers at a turn boundary; a timer
  ends only by `clear`, a one-shot's fire, or restart.

### Source defaults to self for time triggers

`source` may be omitted or null when the only trigger is a time trigger;
it then means `self`. The schema's `source` becomes nullable like its
siblings and the definition pin test is extended. Without a time trigger,
an omitted source still fails with `invalid_request: source is required`.

### Identity: every timer is its own watch

`watchKey` and `watchConfig` both gain a `slot` string, empty for every
watch that exists today and equal to the watch id for every timer, so the
three existing key predicates compare it like any other field and no
create path skips a step other creates take. `configureWatch` mints the
id before building the key, and only for a timer create, so no id is
burned on a clear or a rejected create.

- Timers never collide with each other or with any other watch. An
  identical repeated create makes a second timer; timers are per-create.
- At most 8 live timers per job manager, counted and enforced inside
  `configureWatch` under `jm.mu`. The ninth is `invalid_request: too many
  timers (8 live); clear one first`.
- `watchArgsHasCondition` counts both time fields as conditions.
- Timer ends enter watch history and lineage like any other watch.
- The clear contract is unchanged: clearing an inactive or unknown id is
  the existing no-op success.

### Fires, budget, and the one-shot end

- The delivery budget bounds condition-driven fires. `recordWatchDeliveryLocked`
  counts `conditionFires`, which the code already separates from
  `deliveries`, so a periodic tick, timer or job progress, never trips the
  50-delivery auto-clear. One rule, no timer branch; the docs say so.
  Every delivered tick still increments `deliveries`.
- A one-shot's first tick fires and ends the watch through the existing
  clear teardown (`clearWatchByIDMatching`'s snapshot, mark-rejecting,
  persist, detach sequence) with end reason `fired`, building its
  notification from the snapshot's config after the detach and enqueuing
  outside `jm.mu`. `fired` joins the documented end-reason set, which is
  corrected to include the existing `runtime_lost`.
- The two time fields are mutually exclusive with each other and with
  `progress_interval_ms`, `output_match`, `events`, `every`, and
  `event_filter`; combining is `invalid_request` naming both.

### Queued ticks fold at the session

`jobNotification` gains `WatchID`, `Fires`, `Note`, `IntervalSeconds`, and
`Terminal` (in-memory only); a timer notification carries what its block
needs, and `Terminal` marks a one-shot's fire so nothing has to key on the
rendered reason string. Folding happens at enqueue, in one locked helper
shared by `enqueueJobNotificationAndNotify` and `requeueJobNotifications`,
under `pendingJobNotifsMu`, with an immediate return when `WatchID` is
empty so the non-timer path stays an append: a timer notification whose
`WatchID` matches a pending entry adds its `Fires` to it instead of
appending, so the queue holds at most one entry per timer however long
the session is busy or parked on an unanswered question. The helper never
takes `jm.mu`. A tick built before a clear can still enqueue after it, so
`filterDeliverableJobNotifications`, which runs before the batch-size
gate, drops a non-terminal timer entry whose watch is gone; because
`slot` equals the watch id, the watch key is reconstructible and the check
is one `jm.watches[key]` lookup under `jm.mu`, taken once per drain with
`pendingJobNotifsMu` already released. A batch that empties this way
produces no turn. There is no purge on clear; the drop covers it.

### Delivered block

Timer notifications render through the existing watch branch of
`formatJobNotificationBlock`, which already emits the watch markers,
`reason`, and body-escapes; it gains `watch_id` in the attribute list and,
when the notification carries a note, the interval and fold count in the
sentence and a `Note:` line after it. Timer blocks carry no job id, so
the replay guard and the hub's watch typing need no change.

```
<job-notification event="watch" status="watch" watch_id="<id>" reason="repeat">
Timer fired (every 300s), 3 times since your last turn.
Note: PR #123: handle roborev comments newer than id 456; clear when merge-ready
</job-notification>
```

One-shots say `Timer fired after 600s.`. The note is body text only,
never an attribute. Non-timer watch blocks render exactly as they do
today.

Hub: keep the prose that `splitNotificationExcerpt` already computes, as
a `prose` field rendered on watch cards through the existing
entity-decoding path, and expose `watch_id` as `watchId`. Typing, tone,
and the reason gate are unchanged. TUI: unchanged; timer blocks append
verbatim like every other watch block.

### Restart

Unchanged. Every watch is cleared with `runtime_lost` and the session gets
the existing generic notice; a timer is re-created by hand.

### Model-facing results

`watchConditionSummary`, the single condition renderer for create, list,
inspect, and history, gains `after_seconds: N` / `repeat_seconds: N` and a
`note:` part rendered through `limitWatchText` the way `output_match:` is;
no new structured fields. `formatJobWatch` renders the job progress
trigger as `progress_interval_ms 300000ms` so the two are distinguishable.

### Tool description

`DefJobWatch`'s description leads with the timer:

> Wake yourself later: `after_seconds` fires once, `repeat_seconds` fires
> every interval, and `note` is delivered with the wake so you know why you
> set it. Source defaults to `self` for these. Use a timer for state Evener
> cannot tell you about, such as an external service; your delegates and
> jobs wake you when they finish, so never set a timer to learn whether
> one finished. To be nudged if a job is still running later, create a
> one-shot on yourself with a note naming the job (`after_seconds:600,
> note:"job_x should be done; check job_status"`) and call `job_status` when
> it fires. Each `create` is a new timer; to change a note, clear and
> create, and clear a timer before you report done. The block shows the
> note with `<` escaped; `inspect` returns it verbatim.
> Then the existing text about observing sources and events, with the
> coalescing sentence scoped to frame watches and the
> `progress_interval_ms` strings naming concrete jobs.

Property descriptions name the unit and bounds, because the sibling time
fields are milliseconds: `after_seconds`: "Fire once this many seconds from
now (60 to 86400)"; `repeat_seconds`: "Fire every this many seconds until
cleared (60 to 3600); source self only"; `note`: "Delivered with every
fire; use it to say why and, for a loop, where you are". New properties
use the nullable types of their siblings. The description avoids the
substrings the definition tests forbid.

### Argument parsing

- `shellIntArg` is tightened into the tool's one integer reader: a string,
  a non-integral float, or a non-finite value is `invalid_request: <field>
  must be an integer`, for `after_seconds`, `repeat_seconds`,
  `progress_interval_ms`, and `every` alike.
- `watchTriggerFieldNames` becomes the create-only field list and gains
  the two time fields and `note`; `watchTriggerArgumentIsNeutral` treats
  null as omitted on every operation and `0` or `""` as omitted on
  `list`/`inspect`/`clear`. That is the whole create-only rule; no
  bespoke `note` check.

### Prompt doctrine

Each sentence lives once. The tool description carries "delegates and jobs
wake you", "deadline nudge, not a poll", and "clear before you report
done", and every session with the tool reads it, leaves included, so no
template gains a copy.

`agent/prompts/sections/background-jobs.md`, with the pinned lead-ins kept
verbatim: the two job_watch sentences become one frame: "One look now →
`job_status`. A future signal from work you started → end your turn; the
completion notification resumes you. A pattern in a running job's output →
`job_watch` with `output_match` on that job; an event from a delegate →
`job_watch` on that `dlg_` source. State Evener cannot tell you about, such
as an external service → a `job_watch` timer: `after_seconds` for "in
about N minutes", `repeat_seconds` for "every N minutes", with a `note`
saying why and, for a loop, where you are; to advance the note, clear and
create." The clause "(a server, a watcher)" in the never-leave-running
sentence becomes "(a server, a polling loop)" and gains "A `job_watch`
timer is not a background job; ending your turn with a timer armed is how
you wait for it." "For sustained observation prefer an observer delegate"
is scoped to self event watches.

`workflow.md.tmpl`, inside its existing `job_watch` tool gate, scopes "Use
`job_watch` only for a real intermediate readiness condition" to job
watches.

`agent/prompts/sections/delegation.md` extends its existing "prefer a
single well-scoped subagent" sentence: when several delegates' reports
only make sense together, delegate a coordinator that fans them out and
reports once.

### Out of scope

- **`delegate_send.max_wait_ms`**: blocking, the antipattern; remove in a
  follow-up PR after checking eval binaries and prompt text.
- **Aggregate cost bounds, lifetimes, per-timer restart blocks, in-place
  update, TUI rendering, hub tone changes.** Designed and reviewed, then
  removed as over-engineering; the git history has the shapes.
- **Notes on the frame rail** (`parent`, `dlg_` watches). Rejected at
  create.
- **Set barriers.** Rejected by doctrine; the coordinator guidance covers
  it.

## Error handling

| Input | Result |
|---|---|
| both time fields set, or either with `progress_interval_ms`, `events`, `output_match`, `every`, or `event_filter` | `invalid_request` naming both fields |
| `after_seconds` outside 60 to 86,400, including a present `0` | `invalid_request: after_seconds must be between 60 and 86400` |
| `repeat_seconds` outside 60 to 3,600, including a present `0` | `invalid_request: repeat_seconds must be between 60 and 3600` |
| any integer argument wrong-typed | `invalid_request: <field> must be an integer` |
| `progress_interval_ms` on a session source | `invalid_request: progress_interval_ms is a job progress trigger; for a timer use repeat_seconds` |
| a time field on any source but `self`; `note` without a time trigger | `invalid_request` as worded above |
| time trigger with `TurnEndsProcess` set | `invalid_request: timers need a session that outlives the turn` |
| ninth live timer in one manager | `invalid_request: too many timers (8 live); clear one first` |
| a create-only field on `list`/`inspect`/`clear` | existing `rejectWatchTriggerFieldsOnNonCreate` message |
| source omitted, no time trigger | existing `invalid_request: source is required` |
| null, `0`, or `""` for a create-only field on `list`/`inspect`/`clear` | neutral, treated as absent |

## Testing

TDD, each test watched to fail first. Timing uses the job manager's
injectable clock; tick tests alternate one `Advance` per interval with an
assertion on the pending entry's `Fires`, because the fake ticker drops a
fire that lands while the previous one is unread.

- Decode: both fields and `note` parse; omitted or null source with a
  time trigger becomes `self`; omitted source without one still errors;
  each row of the error table; `progress_interval_ms:"300"` is now an
  error rather than silently absent; an internal target-only progress
  call is untouched.
- Wakeability: a run-mode root and its delegate refuse a timer; a served
  root and its delegate accept one and keep it across turns ended with
  `communicate(end_turn=true)`; a delegate's timer wakes it through the
  parent's drive path.
- Identity: two one-shots coexist; two recurring timers at the same
  interval coexist; an identical repeated create makes a second timer;
  the ninth timer is refused and two concurrent ninth creates both fail
  under the manager lock; a slot-less internal clear request does not clear
  a timer; a timer create matches no detached config on the same target;
  no watch id is minted for a rejected create; clearing an ended timer id
  is the existing no-op success.
- One-shot: fires once with `reason="after"`, `watch_id`, the note, and
  the two watch markers; history shows `fired` with `deliveries: 1`;
  advancing the clock again does not fire; clearing before the deadline
  leaves no armed timer; the fire path holds no lock while enqueuing.
- Budget: a 50th periodic tick, timer or job progress, does not auto-clear;
  a 50th `output_match` fire still does.
- Recurring: fires every interval with the note; `deliveries` counts
  ticks; eight one-minute timers deliver eight wakes a minute and nothing
  throttles them.
- Folding: three ticks during one long turn leave one pending entry and
  produce one block saying three fired; a tick landing between drain and
  requeue folds into the requeued entry; ticks while the session awaits an
  answer leave one pending entry per timer; a non-timer notification is
  appended without a scan; a tick enqueued after a clear is dropped before
  the batch gate with one `jm.mu` acquisition, so a batch of one stale tick
  produces no turn and no steering entry; a pending one-shot fire survives
  a clear of its id.
- Model-facing text: create, list, and inspect outputs show the condition
  summary with interval or delay and note; the job progress trigger
  renders as `progress_interval_ms`; `inspect` returns a note containing
  `<` verbatim while the block shows `&lt;`.
- Rendering: blocks for a one-shot, a recurring tick, a folded tick, and
  an unchanged concrete-job progress tick; a multi-line note stays in the
  body; a note containing `</job-notification>` is escaped and the replay
  guard still delivers an unrelated job's completion.
- Hub: a timer block types as a watch as today; the card shows the prose
  and the watch id; typing, tone, and the reason gate are unchanged.
- Schema: `DefJobWatch` properties include nullable `after_seconds`,
  `repeat_seconds`, `note`, and `source`; the description begins with the
  timer sentence; the `progress_interval_ms` strings name concrete jobs;
  the pin test is updated.
- Prompts: pinned lead-ins and section shapes hold; the workflow sentence
  stays inside the tool gate.
- Existing job_watch suites stay green; the production-integration
  inventory in `agent/delegate_tree_controller_test.go` is updated for any
  new call site of an inventoried symbol.

## Files

- `agent/internal/tool/definitions.go`: `DefJobWatch` description and
  properties; `definitions_test.go` pin.
- `agent/session_tools_jobs.go`: `watchArgsFromToolArgs`, the tightened
  integer reader, create-only field list, source defaulting,
  `TurnEndsProcess` check, `formatJobWatch`, end-reason vocabulary comment.
- `agent/job_watch.go`: `watchArgs`/`watchConfig` fields and `slot`, id
  minted for timer creates before the key, `slot` in the three key
  predicates, timer cap under `jm.mu`, trigger-shape and bounds rules in
  `validateWatchTriggerShape` and `normalizeWatchArgs`,
  `watchArgsHasCondition`, timer configuration of the periodic engine,
  one-shot end through the clear teardown, `conditionFires`-based budget,
  `watchConditionSummary`.
- `agent/jobs.go`: `jobNotification.WatchID`, `.Fires`, `.Note`,
  `.IntervalSeconds`, `.Terminal`.
- `agent/session.go`: locked fold helper with the non-timer fast path,
  stale lock-order comment fixed; `agent/session_lifecycle.go`:
  `filterDeliverableJobNotifications` drops orphaned timer entries before
  the batch gate.
- `agent/job_notify.go`: watch branch gains `watch_id`, interval, fold
  count, and the `Note:` line.
- `cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts`
  and `NotificationCard.tsx` with their tests: `prose` and `watchId`.
- `agent/prompts/sections/background-jobs.md`,
  `agent/prompts/sections/workflow.md.tmpl`,
  `agent/prompts/sections/delegation.md`: doctrine; `agent/profile_test.go`,
  `agent/section_resolver_test.go`: pins kept.
- `docs/job-control.md`: timers (properties, source default, identity,
  cap), the concrete-job restriction on `progress_interval_ms`, the
  budget's condition-fires rule, the end-reason vocabulary including
  `fired` and `runtime_lost`, the strict integer rule.
- Tests beside each.

Estimated size: about 250 lines of production change and 350 lines of
tests.

## Review record

Eleven steered adversarial rounds, four blind rounds, and one cleanup
pass ran against earlier revisions. The steered rounds corrected the code
facts, reshaped the feature around the scenario, fixed identity, found
the replay-guard hazard, the fire-path deadlock, and the reversed
lock-order claim, and found that the hub discarded block prose. The blind
rounds each found a design-level error the steered rounds had missed or a
fix had introduced. Mechanisms designed and then removed as
over-engineering: a per-timer fire ceiling and a per-manager wake budget;
a 24-hour lifetime; an in-place `update`; per-timer restart blocks; a
root-only rule (dead on one hook, fatal on the other); history and
lineage special cases. The cleanup pass then folded the timers onto the
existing periodic engine, the existing clear teardown, the existing
condition summary, and one integer reader, and removed the purge on
clear, the unfired-notice special case, the hub tone change, and the TUI
rewrite. Their shapes are in this file's git history.

Decisions a reader may want to overturn: the 60-second floor on one-shots;
refusing `progress_interval_ms` on every session source (a behavior change
for anyone using it today); redefining the delivery budget as
condition-driven fires (a 50th job-progress tick no longer auto-clears);
the eight-timer cap.

Recommended next step: an implementation plan from this revision, with
the first task being the fold helper and lock-order contract.
