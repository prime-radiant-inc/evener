# Product friction punchlist

This is the open discussion queue for places where Evener makes the user manage
its machinery, loses the ability to recover, or imposes a restriction that needs
a product justification. Use the [principles](principles.md) to assess the user
cost and the [subsystem map](subsystems.md) to find the owning code.

Cases remain open until their agreed behavior is implemented and verified.
**Decision** records an agreed product outcome; **Discuss** describes an unresolved
proposal. Neither is an implementation plan or a claim that behavior has changed.
Discuss cases individually before changing their behavior. Preserve the user's
work and chosen boundaries while deciding how the product restores capability.

High priority means blocked work, lost content, duplicate-operation risk, or lost
ongoing intent. Medium means repeated intervention or a substantial capability
restriction. Low means avoidable attention or diagnosis overhead. These are
discussion priorities, not a claim about incident frequency.

Evidence comes from traced production paths and the contracts expressed by
existing tests. A named test is a pointer to that contract, not a claim that it
has been executed or that a user journey has been observed. Execution results
belong in the change's validation evidence. The acceptance scenarios describe
the desired evidence for a future change. Live provider, SSH, device, and visual
behavior require the qualification listed under
[coverage](#coverage-and-qualification). Conditional findings name the condition
and should not be presented as reproduced production incidents.

## Case index

| Case | Priority | User-visible consequence | Owning subsystems |
| --- | --- | --- | --- |
| [C01](#c01-browser-first-connection) | High | First browser connection failure needs Retry | S02, S05 |
| [C02](#c02-uncertain-session-creation) | High | Lost creation response means hunting for a session or creating a duplicate | S02, S03, S05, S06 |
| [C03](#c03-transcript-repair-rereads) | Medium | A retained transcript can stay failed after its backing condition heals | S02, S03, S10 |
| [C04](#c04-older-history-and-find) | Medium | One older-page failure stops browsing or Find | S02, S03, S10 |
| [C05](#c05-native-outbox-convergence) | High | A queued message can remain parked after one proof-read failure | S03, S05, S11 |
| [C06](#c06-rejected-image-message-recovery) | Medium | Rejected image messages cannot be restored intact for correction | S03, S11 |
| [C07](#c07-model-discovery-and-default-launch) | Medium | Model discovery delays default launch and stays failed in an open form | S02, S03, S16 |
| [C08](#c08-remote-provider-setup-at-launch) | Medium | Remote setup sends the user away from an otherwise usable setup path | S02, S07, S15 |
| [C09](#c09-following-shell-job-output) | Medium | Job output stays static until Refresh; older output needs clicks | S02, S10, S12 |
| [C10](#c10-quiet-task-panels) | Medium | A quiet Tasks panel remains failed until Try again | S02, S03, S05, S12 |
| [C11](#c11-successful-recovery-looks-like-failure) | Medium | Successful MCP recovery receives warning/error presentation | S02, S03, S05, S19 |
| [R01](#r01-transcript-durability-stop) | High | A healed storage problem leaves the chat permanently stopped | S08, S09, S10 |
| [R02](#r02-finished-turn-ownership) | High | A finished turn continues blocking new messages | S09, S11 |
| [R03](#r03-watch-intent-after-restart) | High | Restart can end monitoring without informing the owning agent | S08, S12 |
| [R04](#r04-idle-child-blocks-parent-restore) | High | One idle child's IO problem prevents its healthy parent from restoring | S08, S12, S13 |
| [R05](#r05-goal-blocking-after-transient-failure) | Medium | A temporary execution failure leaves an authorized goal blocked | S09, S15 |
| [R06](#r06-read-only-goal-progress) | Medium | Useful research can count as no progress while attempted writes count | S09, S14 |
| [R07](#r07-restored-goals-without-a-wake) | Medium | An active restored goal can remain idle until another turn arrives | S08, S09 |
| [R08](#r08-compaction-with-a-pending-question) | Low | A pending question prevents explicit compaction | S09, S10 |
| [H01](#h01-credential-store-failure-scope) | High | A credential-file problem prevents the entire hub from starting | S01, S06, S15 |
| [H02](#h02-live-daemons-after-api-key-repair) | High | Saving a corrected API key leaves running sessions using the old key | S08, S15 |
| [H03](#h03-host-journal-failure-scope) | High | Incomplete host-journal quarantine prevents unrelated local work | S06, S07 |
| [H04](#h04-retaining-explicit-connect-intent) | High | A transient first Connect failure has no continuing retry owner | S02, S03, S07 |
| [H05](#h05-connection-versus-build-synchronization) | High | Build synchronization can block an otherwise compatible remote host | S07, S20 |
| [H06](#h06-provider-file-repair-discovery) | Medium | Fixing provider configuration on disk leaves the hub's cached failure | S06, S15 |
| [H07](#h07-proven-no-op-teardown-remnants) | Medium | Already-harmless teardown remnants still require manual recovery | S07 |
| [H08](#h08-oauth-refresh-across-processes) | Medium | Shared rotating credentials can race and provoke another sign-in | S08, S15 |
| [H09](#h09-issued-credentials-awaiting-persistence) | Medium | A local save failure discards successfully issued credentials | S06, S15 |
| [H10](#h10-update-outcome-after-the-response) | Medium | An update says restarting before a failure that only reaches logs | S06, S20 |
| [H11](#h11-remote-bootstrap-diagnostics) | Low | First-start output is discarded when it would explain a failed connection | S07, S20, S21 |
| [H12](#h12-dormant-host-notices) | Low | An unused disconnected host becomes a recovery notice | S03, S06, S07 |
| [T01](#t01-tool-call-parking) | High | Repeated failure parks an operation even after the dependency heals | S14 |
| [T02](#t02-mcp-initial-discovery-recovery) | High | An initially unavailable MCP server never contributes tools to the session | S18, S19 |
| [T03](#t03-unsandboxed-tool-scope) | Medium | Sandbox-off tools disagree about usable filesystem scope | S14 |
| [T04](#t04-read-only-directory-symlinks) | Medium | A derived read-only delegate cannot browse an allowed target through a symlink | S12, S14 |
| [T05](#t05-approval-scope-and-lifetime) | Medium | Narrow one-call approval paths repeatedly interrupt authorized work | S12, S14 |
| [U01](#u01-tui-uncertain-submission) | High | A lost send acknowledgement becomes a manual new submission | S04, S05, S11 |
| [U02](#u02-tui-failed-submission-attachments) | High | Preserving a newer draft discards the older failed submission's images | S04, S11 |
| [D01](#d01-diagnostic-repair-authority) | Medium | Bundled repair instructions prohibit product fixes and require byte-identical voting | S21 |

## Clients and everyday work

### C01 Browser first connection

**Current behavior.** A transport failure before the first successful browser
handshake closes the client. Established connections retry automatically, but
this startup path waits for Retry to create a new client. Native already owns a
foreground lifecycle that creates subsequent attempts.

**Evidence.** Shared `performHandshake` and `handleSocketLoss` in
[client.ts](../../appwire-client/typescript/client.ts#L536) distinguish first
connection from established reconnect. Browser
[handleRetry](../../cmd/evener-hub/frontend/src/shell/ConnectionBanner.tsx#L159)
constructs the replacement; native
[hubConnection](../../mobile-native/src/hubConnection.ts#L195) retries itself.
`ConnectionBanner.test.tsx` tests the manual replacement path.

**Discuss.** Give browser startup a continuing retry owner with capped backoff,
online/focus triggers, and cancellation when replaced. Decide how authentication
and protocol incompatibility affect retry; preserve route and draft.

**Acceptance.** Open the browser while the hub is unavailable, then restore the
hub without touching the page. Exactly one client becomes usable and the user's
work remains intact.

### C02 Uncertain session creation

**Current behavior.** If session creation commits but its response is lost,
native holds an unconfirmed draft and suggests checking the Board or changing the
draft. Browser reports an error and permits another Start. Neither can identify
the result by a durable creation request ID. The user must investigate or risk a
duplicate; editing the draft does not resolve the first attempt.

**Evidence.** [ThreadStartParams](../../appwire/types.go#L2281) has no client
creation key. The hub
[detaches creation from peer cancellation](../../cmd/evener-hub/app_threadlifecycle.go#L82).
Native [submit](../../mobile-native/src/newSession.ts#L429) persists uncertainty;
browser [creation failure](../../cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx#L2143)
clears the busy state. Native `newSession.test.ts` tests the held unchanged draft.

**Discuss.** Retain a creation identity and reconcile a server receipt before
retrying or asking the user to find the result. Decide receipt lifetime and when
an edit expresses a new creation. Preserve the original text and images.

**Acceptance.** Drop the response after creation commits. Reconnecting locates
and opens exactly one session with its original input. A creation proven not
accepted leaves the unchanged draft immediately usable.

### C03 Transcript repair rereads

**Current behavior.** Browser hydration retries ordinary failures, but suppresses
its retry for a structured history failure when a base transcript already exists.
A quiet session can remain incomplete until a later event, reopen or reconnect.
Native open-session rehydration also retains open status on failure, outside its
initial-read retry loop. The server can repair a projection on a later read.

**Evidence.** Browser
[hydrateAndSubscribe](../../cmd/evener-hub/frontend/src/stores/threads.ts#L1678),
native [rehydrate](../../mobile/src/state/conversation.ts#L2372) and
[useReadRetry](../../mobile-native/src/session/useReadRetry.ts#L46), and server
[recoverForRead](../../server/history_read.go#L85) expose the ownership gap.
Shared `reducer.history.test.ts` specifies that a later success clears history failure.

**Discuss.** Keep the retained transcript and anchor while bounded safe rereads
remain owned by the visible/needed session. Classify persistent damage separately
and avoid expensive rebuild loops; preserving diagnostics need not prohibit a
later health check.

**Acceptance.** Fail an already-open transcript read, repair its backing
condition, and leave the session idle. Content recovers without reopening or
losing the reader's position.

### C04 Older history and Find

**Current behavior.** Native remembers a failed older-page cursor in
`readerPageAttempts`; subsequent top-scroll and Find cannot retry that cursor
until the guard resets. Browser normal history already loads automatically, but
its geometry-driven path stops on an error and offers Retry. Its separate
near-top scroll path swallows a failed read and relies on another scroll event.

**Evidence.** Native
[loadOlderPage](../../mobile-native/src/screens.tsx#L1342) retains the failed
guard; [Find](../../mobile-native/src/screens.tsx#L1403) checks it. Browser
[LoadOlderRow](../../cmd/evener-hub/frontend/src/panes/session/transcript/flow/LoadOlderRow.tsx#L58)
blocks automatic loading on error, while
[useTranscriptScroll](../../cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts#L1487)
catches the raw loader's failure. `LoadOlderRow.test.tsx` pins Retry behavior.

**Decision.** Keep retrying a transient or unresolved older-history failure for
as long as the user is actively waiting for that history or search result. Backoff
bounds the request rate; a fixed attempt count or elapsed-time limit must not
abandon the demand and require another interaction. Preserve demand when the
view is inactive and resume recovery when it becomes active again. Keep loaded
content and reading position usable throughout; Find must distinguish incomplete
search from confirmed absence of matches. A proven permanent condition needs an
accurate explanation rather than repeated identical requests. Implementation
remains pending.

**Acceptance.** Fail an older page both once and through a prolonged outage, then
restore reads without a scroll, click or reconnect event. The page appears and
Find reaches a match or the confirmed end. Attempts slow during failure without
being abandoned; there are no duplicate pages, jumps to live output, or false
absence of matches. Leaving and returning preserves the outstanding demand.

### C05 Native outbox convergence

**Current behavior.** Before sending queued input, native reads authoritative
state. A failed read can return `blocked`, bypass the retry-on-throw path, and
leave the target parked until another record, connection, revisit or Check.
Already-unknown outcomes also lack an automatic proof-read owner. Storage-startup
backoff and ordinary offline flushing already work; this is a narrower gap.

**Evidence.** [settleTarget](../../mobile-native/src/nativeMutationRuntime.ts#L392)
converts read failure to `blocked`;
[outboxFlush](../../mobile-native/src/outbox/outboxFlush.ts#L165) waits for another
event, and its [waiting filter](../../mobile-native/src/outbox/outboxFlush.ts#L288)
excludes `blockedUnknown`. `outboxFlush.test.ts` tests these returned-blocked and
unknown-state paths separately from storage retry.

**Discuss.** Own bounded proof reads for needed unresolved targets, including
sessions behind other screens. Reconcile before dispatch; retain mutation IDs,
stop epochs, stale-client rejection and explicit Stop holds. An unknown result
is a reason to establish the outcome, not to blindly resend.

**Acceptance.** Queue input, leave for Board, reconnect, fail the first proof
read, then let reads succeed without another event. The original operation sends
or settles exactly once. An already-applied operation settles without resending;
Stop-held input stays held.

### C06 Rejected image message recovery

**Current behavior.** Native can restore rejected text-only input to the draft,
but image-bearing rejection offers Discard instead. The recovery record survives,
yet correcting and resending requires rebuilding the composition manually.

**Evidence.** [MutationRecoveryPanel](../../mobile-native/src/MutationRecoveryPanel.tsx#L105)
limits restore to attachment-free text;
[ghost actions](../../mobile-native/src/session/ghosts.ts#L184) explicitly make
image rejection discard-only. `MutationRecoveryPanel.test.tsx` pins the behavior.
Other native draft flows already preserve media; browser attachment parity is
not established by this case.

**Discuss.** Transfer text and attachments together into a durable editable
draft, retaining the recovery record until that transfer commits. Decide how to
represent a genuinely unavailable image without losing the rest of the message.

**Acceptance.** Reject input containing text and two images, correct the cause,
and restore it. The exact composition survives restart and can be sent once.

### C07 Model discovery and default launch

**Current behavior.** Native includes model loading in the form's busy gate even
for Hub default, and an open picker has no continuing retry after discovery
fails. Hub startup warms only the empty-working-directory cache entry, while web
and native project pickers request their project directory. Those first reads
can still wait for discovery; later stale entries already refresh in background.

**Evidence.** Native [form busy state](../../mobile-native/src/newSession/NewSessionForm.tsx#L74),
[loadModels/submit](../../mobile-native/src/newSession.ts#L321), and
[ModelPicker](../../mobile-native/src/newSession/ModelPicker.tsx#L23); hub
[fetchLaunchModels](../../cmd/evener-hub/app_models.go#L136) and
[warmLaunchModels](../../cmd/evener-hub/app_models.go#L299).
`app_models_test.go` separately tests directory-scoped caching and startup warmup;
that does not establish a warm first project picker.

**Discuss.** Let valid default launch proceed without optional discovery. Retain
usable catalogs, retry failed reads, and reuse provider inventory or warm likely
project contexts without mixing project configuration. An explicitly selected
model must retain its meaning.

**Acceptance.** Delay model listing: Hub default still launches promptly. Fail
listing once with the picker open: choices recover automatically. Opening a
project after warmup shows useful choices promptly with the correct project
default and selected host.

### C08 Remote provider setup at launch

**Current behavior.** Browser launch on a remote host without a provider says to
configure one on that host and offers a retry check. Existing host-scoped
credential push and remote Codex sign-in live in Settings, so the user must find
that route and return to the draft.

**Evidence.** [Spawn provider gate](../../cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx#L2537)
differs from the available
[CredentialsHostScope](../../cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsHostScope.tsx#L190)
actions. `useProviderSetup` already preserves exact-host scope.

**Discuss.** Connect the launch blockage to supported setup for the selected
host, preserving the draft. Some providers still require external setup;
credential transfer remains a deliberate user action with an explicit destination.

**Acceptance.** Complete a supported remote sign-in or chosen credential transfer
from the blocked launch and return to the same draft with Start ready, without
route hunting or accidental local-host substitution.

### C09 Following shell-job output

**Current behavior.** The browser job transcript reads one tail. A running job
can remain at No output yet or old content until Refresh. Refresh replaces the
tail and discards earlier pages; older output requires repeated clicks and a
failed older read is silent. Reconnect does not reset the effect's started flag.

**Evidence.** [JobLog](../../cmd/evener-hub/frontend/src/panes/transcript/JobLog.tsx#L49)
owns the single read, [loadEarlier](../../cmd/evener-hub/frontend/src/panes/transcript/JobLog.tsx#L99)
only clears loading on failure, and the
[controls](../../cmd/evener-hub/frontend/src/panes/transcript/JobLog.tsx#L141)
require Refresh/Load earlier output. No focused live-view test establishes
automatic following for this component.

**Decision.** An open job-output viewer stays current automatically. Follow new
output while the reader is at the bottom; when they scroll into earlier output,
continue receiving new output without moving their reading position. Returning
to the live end shows the latest available output. Load retained older output on
scroll demand, recover temporary read failures automatically, and obtain final
output when the job finishes. Use bounded memory and fetch activity appropriate
to the visible viewer; this does not require monitoring every unopened job.
Subscription or polling mechanics remain an implementation choice.
Implementation remains pending.

**Acceptance.** Open before first output and observe output through completion
without clicking. While reading older output, verify new output continues to
arrive without moving the reading position; returning to the live end shows it.
Older paging and new output coexist without gaps or duplication, and a failed
read recovers automatically. A high-volume job does not require unbounded
client memory.

### C10 Quiet task panels

**Current behavior.** Web and native task surfaces retain rows on snapshot
failure but wait for a task notification, reconnect, reopen or Try again. An idle
session supplies no such event. Browser can also toast the same error shown
inline.

**Evidence.** Shared [taskPanelState](../../appwire-client/typescript/taskPanelState.ts#L256)
owns snapshots and notifications without a failed-read timer; browser
[TasksPanel](../../cmd/evener-hub/frontend/src/panes/session/chrome/TasksPanel.tsx#L388)
and native [TasksSheet](../../mobile-native/src/TasksSheet.tsx#L75) provide manual
retry. `TasksPanel.test.tsx` explicitly tests the quiet-session Try again path.

**Discuss.** Retry safe snapshots with bounded backoff while the surface remains
visible. Retain rows and expansion state; decide when stale status is enough
without a duplicate toast. Unsupported or deleted scopes remain distinct.

**Acceptance.** Fail a task snapshot, then restore reads without task updates or
reconnect. The open panel recovers by itself and keeps previously expanded rows.

### C11 Successful recovery looks like failure

**Current behavior.** Successful MCP reconnect emits an EventWarning saying no
action is needed. Its missing informational code makes it a critical warning in
shared projection, an attention chip on web and a failure row on native.

**Evidence.** [reconnectRecoveryWarning](../../agent/session_init.go#L2592),
[warning classification](../../appwire-client/typescript/warnings.ts#L27), web
[WarningItem](../../cmd/evener-hub/frontend/src/panes/session/transcript/messages/WarningItem.tsx#L67),
and native [projectedRows](../../mobile-native/src/projectedRows.ts#L736).
This is a source-traced presentation path; visual/device qualification remains.

**Discuss.** Give successful repair structured informational meaning and a quiet
diagnostic record. Keep actual failed reconnect and required sign-in visible;
changing this success event need not weaken other warnings.

**Acceptance.** After a dropped MCP connection recovers, both clients show normal
operation with inspectable recovery history and no failure styling for success.

## Runtime and continuing intent

### R01 Transcript durability stop

**Current behavior.** Certain authoritative transcript failures stop a served
session. Even recorded-but-unsynced output gets only five short durability
retries before `failClosed` latches for that Session's lifetime. Later input
checks that latch first, so healing the filesystem does not restore the chat;
the diagnostic instructs a restart.

**Evidence.** [failClosed and retryDurabilityUntilSettledOrExhausted](../../agent/session_fail_closed.go#L54)
set the cause without a reset;
[refuseOnUnhealthyTranscript](../../agent/session_lifecycle.go#L1804) checks it
before current writer health. Existing
`TestRetainedCompletionFailsAServedSessionClosedAfterExhaustingRetries` and
`TestRetainedCompletionSettlesAfterRetryingTheBarrier` distinguish
exhaustion from successful short recovery.

**Decision.** Treat recoverable persistence failure as an interruption of the
affected chat, with owned recovery and backoff rather than a permanent refusal.
Keep available history readable and preserve drafts and pending input. Revalidate
or replace the affected runtime under the same chat identity as needed. Once
persistence works and the interrupted state is reconciled, automatically resume
the already-authorized task, including after a prolonged outage. Honor existing
budgets, explicit Stop and newer instructions. Distinguish absent records,
retained unsynced records and ambiguous completion; inspect actual results
instead of blindly replaying uncertain tool effects. Preserve damaged evidence
when a remaining uncertainty needs resolution. Implementation remains pending.

**Acceptance.** Hold fsync failure beyond the retry window, then heal storage.
The same chat resumes authorized work without a restart or another user message;
retained output appears once and uncertain completed actions are not duplicated.
Explicit Stop, exhausted budgets and superseding instructions remain effective.
Genuinely damaged authoritative history remains preserved for resolution.

### R02 Finished turn ownership

**Current behavior.** If persisting a finished turn's release fails through eight
retries, its `ActiveTurnID` can keep rejecting new messages after storage heals.
Snapshot loading already removes demonstrably orphaned running-only IDs, but
normal admission does not. Restart or an interrupt-style clear can recover it.

**Evidence.** [release and scheduled retry](../../agent/session_active_turn.go#L242),
the [retry budget](../../agent/session.go#L1056), and load-time
[forgetRunningTurnNoOneOwns](../../agent/session_client_mutation_persist.go#L100).
Existing `TestReleaseRunningTurnIDRecoversFromARefusedWrite`,
`TestReleaseRunningTurnIDGivesUpLoudlyWhenTheStoreNeverRecovers` and
`TestReleaseRunningTurnIDResetsRetryBudgetOnExhaustion` specify those recovery
and exhaustion contracts.

**Decision.** Recover a finished turn's stale ownership automatically, continuing
the release repair with backoff and checking ownership on ordinary admission.
A message submitted during repair stays pending with its original identity and
runs once the chat is ready, unless canceled or superseded. Do not require the
user to resend, interrupt or restart. Serialize repair with accepted queue/start
claims and keep compare-and-clear semantics; a late release must not clear a
newer live turn. Silence or elapsed time alone does not establish that an owner
has finished. Implementation remains pending.

**Acceptance.** Fail release persistence past eight retries, submit a message,
then heal the store without another user action. The pending message starts
exactly once with its original identity. Cancellation during recovery prevents
later execution, and a genuinely running or accepted pending turn retains its
ownership.

### R03 Watch intent after restart

**Current behavior.** The job manager restores pending watch sends, then ends
every active saved registration with `runtime_lost`. Future monitoring stops,
including declarative repeating timers whose configuration is saved. A repeating
send watch that has already spoken can also skip an individual restart-end
notice. Saved pending deliveries and future registrations have different lifetimes.

**Evidence.** [newJobManagerWithStoreOpen](../../agent/jobs.go#L647) and
[clearUnrestoredActiveWatches](../../agent/jobs.go#L1526) clear all active records;
[watchConfigSnapshot](../../agent/job_watch.go#L2174) retains filters, identities
and intervals. [noticeUnrestoredWatchEnds](../../agent/job_watch.go#L3830) handles
the end notices. The [job-control contract](../job-control.md) distinguishes
durable registration and delivery records from process-local observation,
timers and callbacks.

**Decision — shell-job lifetime.** Ordinary session-owned shell jobs stop when
their owning execution runtime shuts down. Watch recovery must not keep those
processes alive or automatically rerun commands merely to recreate their watches.
Retain the stopped/interrupted outcome and available output; a watch tied to that
ended execution settles rather than continuing to wait for it. A hub-only restart
does not shut down the independent session runtimes. Explicitly detached processes
have a separately chosen lifetime outside session-owned job supervision.

The normal shutdown path already marks and signals owned jobs in
[closeRuntimeState](../../agent/jobs.go#L757), followed by tracked process-group
[cleanup](../../agent/execenv/local.go#L1330). After an abrupt loss, the restored
job record's `runtime_lost` status describes lost supervision; it does not by
itself prove that every OS process was terminated.

**Decision — recovery outcome.** Either automatically restore a continuing timer
or watch, or reliably notify its owning agent session that it was canceled and
requires explicit re-registration. The agent can decide whether to restart it
while continuing the user's task. Cancellation with that handoff is acceptable;
silent loss of monitoring is not. A watch having fired before does not remove
the need to communicate that future observation ended. Preserve enough watch
identity and intent for the session to act on the notification, and respect
explicit Stop and the watched target's lifetime. Automatic restoration is not
required where reliable cancellation notification provides the simpler recovery.
Implementation remains pending.

**Acceptance.** Restart with active timers and watches, including a repeating
watch that has already delivered a result. Each continuing intent either resumes
or reaches its owning agent as an actionable cancellation requiring
re-registration. Already-pending deliveries retain their identities and are not
duplicated. Watches of ended jobs settle with the job outcome; canceled work or
replaced target generations are not silently rearmed.

### R04 Idle child blocks parent restore

**Current behavior.** Operational IO errors such as EACCES or EIO while preparing
an idle retained delegate can abort restoration of its otherwise healthy parent.
The child's resumable data is preserved, but unrelated parent work becomes
unavailable until the child storage problem is fixed.

**Evidence.** Parent [initialization](../../agent/session_init.go#L1202) calls
[delegate restoration](../../agent/delegate_runtime.go#L2905), whose
[resource error handling](../../agent/delegate_runtime.go#L3069) propagates the
operational error. `delegate_resource_bootstrap_test.go` tests parent failure
while leaving the child resumable, including EACCES/EIO scenarios.

**Discuss.** Isolate an idle child's recoverable problem and retain a deferred
repair owner while allowing independent parent work. Distinguish an idle child
from a required active dependency or missing authoritative scratch; do not retire
the child or erase its obligations to make restore succeed.

**Acceptance.** Make one idle child's storage unreadable. The parent opens and
works, other children remain usable, and the affected child resumes with its
original state after storage heals.

### R05 Goal blocking after transient failure

**Current behavior.** Provider calls already retry, but a final execution error
can change an active goal to blocked. A temporary outage that outlasts those
attempts can therefore end authorized continuation. Later ordinary messages do
not reactivate the goal; a new goal action is required.

**Evidence.** [Model-call retry exhaustion](../../agent/session_model_call.go#L820)
feeds [goal failure handling](../../agent/session_goal.go#L408); continuation
requires active status. User interrupt and root shutdown already have distinct
handling and should retain their meaning.

**Discuss.** Separate waiting for a recoverable dependency from an objective that
needs a user decision. Choose a bounded recovery policy and visible waiting state
that respects budgets, explicit Stop and genuine authentication or capability
loss. Avoid turning every terminal error into endless provider calls.

**Acceptance.** Let a temporary scripted provider failure outlast normal retries,
then heal it. An authorized goal continues under the selected policy without
re-entering the objective; stopped and budget-exhausted goals do not resume.

### R06 Read-only goal progress

**Current behavior.** Goal progress accounting treats eligible mutating or unknown
tool-call attempts as progress, excluding result/communicate and task_list,
rather than measuring successful contribution. Useful read-only investigation
can accumulate no-progress turns while an attempted mutation resets them.
Thresholds differ before and after mutating activity.

**Evidence.** [callsMadeProgress](../../agent/session_goal.go#L173), its
[lifecycle call site](../../agent/session_lifecycle.go#L2403), and
[goal progress state](../../agent/internal/goal/goal.go#L18) encode the rule.
Root daemon sessions with continuation and notification wiring already exempt
waiting for dependents that can guarantee a wake. Child and one-shot sessions
do not share that exemption.

**Discuss.** Define credible progress for research, review, planning and coding
goals, including completed evidence gathering and meaningful dependent results.
Retain loop/cost control without making writes a proxy for accomplishment.

**Acceptance.** A read-only investigation producing useful evidence can finish
without false no-progress blocking. Repeated ineffective or failed mutating
calls do not keep an unproductive goal alive indefinitely.

### R07 Restored goals without a wake

**Current behavior.** A restored active goal loads its persisted state but has no
independent startup continuation kick. A pending input or notification may wake
it; absent those, it can remain idle until another turn arrives. Active goals
already pin against ordinary idle retirement, so this concerns runtime
replacement/crash restoration rather than every idle transition.

**Evidence.** [Goal restoration](../../agent/session_init.go#L1271),
[continuation callback wiring](../../agent/session_goal.go#L18), and daemon
[wiring](../../cmd/evener/serve.go#L1268) load and wire without a restore wake.
Existing goal/ask tests distinguish persisted state from pending-question holds.

**Discuss.** Decide whether restoration should schedule a deduplicated wake for
an eligible active goal. Keep pending questions, explicit force-stop, budget
limits and uncertain in-flight execution from becoming accidental restarts.

**Acceptance.** Restore an idle active goal with no queue or external event. It
continues once under the selected policy; held/stopped goals remain held and an
already-pending continuation is not duplicated.

### R08 Compaction with a pending question

**Current behavior.** Explicit Compact refuses while an ask is pending, telling
the user to reply or clear it first. The user cannot reduce older context while
preserving an unanswered question.

**Evidence.** [Compact](../../agent/session_compaction.go#L30) checks pending asks
before compaction. Existing `session_ask_test.go` tests this refusal.

**Discuss.** Determine whether older context can be compacted while retaining
the exact pending ask, invocation/result relationship, and later answer routing.
The question's continuity matters more than clearing a gate.

**Acceptance.** Compact a large session while a question is pending, answer it
afterward, and continue once with the same question identity and preserved
required context.

## Hub, hosts and provider setup

### H01 Credential-store failure scope

**Current behavior.** Credential-store permission, read or parse failure prevents
the whole hub from starting. A permissive file mode alone can make healthy
history, local navigation and unrelated provider instances unavailable. Provider
configuration has a separate degraded path, so failure scope is inconsistent.

**Evidence.** [Credential loading](../../internal/credentials/store.go#L58) rejects
the file; [hub startup](../../cmd/evener-hub/main.go#L385) returns the error.
`TestStore_PermissionsEnforced` specifies the store's permission refusal.
Separately, `TestAuth_UnreadableCredentialsStoreRefusesInsteadOfPanicking` tests
an auth controller that keeps reads working while refusing writes; it does not
test whole-hub startup.

**Discuss.** Isolate affected credential use; determine when permissions on an
owned file can be repaired automatically and when bytes must be preserved for
diagnosis. Keep unaffected read/navigation capability without pretending a
missing or malformed key is usable or replacing the store with empty data.

**Acceptance.** Start with an owned over-permissive credentials file, then with a
malformed one. The chosen repair/isolation policy keeps unrelated work available,
preserves bytes, and makes the repaired provider usable without a hub restart.

### H02 Live daemons after API-key repair

**Current behavior.** Saving a corrected API key updates the hub's credential
store and registry, but existing daemons retain a store/registry/client snapshot
loaded at startup. A live session can continue failing with the old key after
the settings UI reports the repair. OAuth has a different per-request refresh
path; this case concerns API-key snapshots.

**Evidence.** [LoadRegistry](../../cmdutil/registry.go#L64),
[Store.Get](../../internal/credentials/store.go#L86),
[daemon client construction](../../cmd/evener/serve.go#L588),
[session client retention](../../agent/session_init.go#L1116), and
[hub credential save](../../cmd/evener-hub/app_auth.go#L613) have different
lifetimes. A new runtime reloads configuration; a running one lacks that repair
propagation path.

**Discuss.** Propagate authoritative credential changes to live consumers or
resolve the selected credential at an appropriate request boundary. Preserve
account/instance identity, active work and explicit project configuration.

**Acceptance.** Start a daemon with a rejected scripted API key, save its
replacement through provider settings, and retry normal work in the same
session. The next eligible request uses the new key without runtime replacement.

### H03 Host-journal failure scope

**Current behavior.** A host-operations journal whose corrupt state cannot reach
complete quarantine custody aborts hub startup. Remote host administration can
therefore block unrelated local sessions and history. Complete quarantine and
some other IO failures already have degraded recovery paths.

**Evidence.** [openHostOpsStore](../../cmd/evener-hub/main.go#L952) and its
[startup handling](../../cmd/evener-hub/main.go#L606);
`TestOpenHostOpsStoreRefusesStartupOnIncompleteCustody` pins the global
refusal. This does not justify discarding the journal or following untrusted
stash paths.

**Discuss.** Quarantine the smallest affected host-management scope while
retaining exact evidence and preventing ambiguous destructive operations. Decide
which independent hub capabilities can remain available during custody repair.

**Acceptance.** Inject incomplete custody. Local navigation and unrelated work
remain available under the chosen policy; ambiguous host cleanup stays isolated,
and original/custody bytes remain intact for subsequent repair.

### H04 Retaining explicit Connect intent

**Current behavior.** Established remote attachments have automatic supervision.
A transient failure in the first explicit Connect happens before that supervisor
starts, and both clients issue only one attach request. A user who has already
asked to connect must click again.

**Evidence.** [Manager.Ensure](../../cmd/evener-hub/internal/sshconn/manager.go#L537)
starts supervision after attachment; [host attach](../../cmd/evener-hub/app_host_attach.go#L108)
returns the initial failure. Browser
[connect](../../cmd/evener-hub/frontend/src/stores/hosts.ts#L627) and native
[hostsController](../../mobile-native/src/hosts/hostsController.ts#L136) add no
continuing first-attempt retry owner.

**Discuss.** Retain explicit desired connection with cancellable backoff and
meaningful status. Distinguish transient transport from required credentials or
incompatible protocol. Honor Disconnect and intentionally dormant hosts.

**Acceptance.** Click Connect once while SSH is temporarily unavailable, then
restore it. The host attaches without another click; Disconnect during recovery
prevents later attachment.

### H05 Connection versus build synchronization

**Current behavior.** Default attach synchronizes builds and tries to deploy the
controller's binary when builds differ or a development/dirty build cannot prove
its identity. A development/unpublished controller on another target OS or
architecture can fail to supply that binary even when the already-running host
speaks a compatible protocol. A no-deploy path permits compatible connection,
but the default does not choose it.

**Evidence.** [Default synchronization policy](../../cmd/evener-hub/main.go#L533),
[version/deploy handling](../../cmd/evener-hub/internal/sshconn/manager.go#L2207),
and [deployment resolution](../../cmd/evener-hub/internal/sshconn/deploy.go#L378).
This requires a build difference or unverifiable development identity, plus an
unavailable target artifact. It does not describe every release-to-release attach.

**Discuss.** Separate the user's intent to connect from an explicit requirement
to synchronize builds. Decide whether compatible connection can proceed with
accurate version status. Preserve actual wire incompatibility checks and any
user-requested exact build requirement.

**Acceptance.** Connect a development controller to a compatible different-target
host without an available matching artifact. Usable connection follows the
chosen policy; actual incompatibility remains accurately identified.

### H06 Provider-file repair discovery

**Current behavior.** The hub caches a providers.toml load error. Repairing the
file externally does not clear that error until a separate reload path runs.
Refreshing model choices need not reload the file, so the user's repair can
appear ineffective.

**Evidence.** [ProviderRegistry.Reload](../../cmd/evener-hub/internal/hubcore/registry.go#L121)
records the failure; [WritesRefused](../../cmd/evener-hub/internal/hubcore/registry.go#L689)
reads it. [RefreshModels](../../cmd/evener-hub/app_instances.go#L2227) fetches into
the held registry without reloading configuration. Startup, selected auth paths
and successful writes provide other reload triggers.

**Discuss.** Revalidate changed source configuration or own bounded retry for a
failed load. Keep usable prior state where valid, surface precise parse context,
and preserve malformed bytes rather than silently replacing user configuration.

**Acceptance.** Start with malformed provider configuration, repair it on disk,
and use the same open settings/launch surface. The repaired instances appear
without restarting or invoking an unrelated save.

### H07 Proven no-op teardown remnants

**Current behavior.** A teardown record can fence Connect before its already
harmless remnant is reconciled. Recovery can prove certain paths are a no-op, yet
the user must select retry/recover to clear the obstruction. Ambiguous live
workers and provably absent/already-restored artifacts are distinct cases.

**Evidence.** [Attach fencing](../../cmd/evener-hub/app_host_attach.go#L86),
[teardown no-op handling](../../cmd/evener-hub/app_host_teardown_ops.go#L585), and
[cleanup handle resolution](../../cmd/evener-hub/app_host_teardown_ops.go#L747) show the
ordering. Provenance checks still protect unrelated or foreign cleanup targets.

**Discuss.** Run the same evidence-based no-op reconciliation automatically
before rejecting ordinary connection. Do not turn an uncertain abandoned worker
into permission to delete or overlap ownership.

**Acceptance.** Leave a teardown journal whose artifacts are demonstrably
already restored/absent. One normal Connect reconciles it and succeeds;
ambiguous live ownership and foreign artifacts remain preserved.

### H08 OAuth refresh across processes

**Current behavior.** Refresh coalescing is process-local. Two daemons sharing
one expired auth file can redeem the same rotating refresh token concurrently;
the loser may report sign-in required while another process has saved valid new
credentials. Record-bound rejection markers already avoid overwriting newer
auth, and a later retry can reread it, so this is not necessarily a permanent
false failure.

**Evidence.** [Refresh synchronization](../../auth/openai/service.go#L45) and
[refresh path](../../auth/openai/service.go#L507),
[atomic auth save](../../auth/openai/storage.go#L125), and
[rejection binding](../../auth/openai/refresh_rejection.go#L46).
`TestResolveRuntimeCredentialsCoalescesConcurrentRefresh` exercises one
process; cross-process rotation remains a conditional source finding.

**Discuss.** Coordinate by canonical credential-file identity across processes,
reread after acquiring authority and before surfacing rejection, and bound waits.
Keep genuinely revoked credentials and user-selected account identity accurate.

**Acceptance.** Two subprocesses use one expired record and a scripted rotating
issuer. One redemption occurs and both requests receive the committed replacement;
a genuinely revoked unchanged record still requests sign-in.

### H09 Issued credentials awaiting persistence

**Current behavior.** Authorization or refresh can succeed at the issuer and
then fail while saving locally. The issued result is discarded; retry repeats
redemption of a code/token that may already be consumed, turning a temporary disk
problem into another sign-in.

**Evidence.** Hub [LoginComplete](../../cmd/evener-hub/app_auth.go#L463) and
[DevicePoll](../../cmd/evener-hub/app_auth.go#L1142), and
[runtime refresh save](../../auth/openai/service.go#L554) return save failure
without retaining the issued result for a persistence retry.
`TestResolveRuntimeCredentialsSaveFailureAfterRefresh` verifies error
propagation, not successful continuation after storage repair.

**Discuss.** Represent exchange-complete/persist-pending as a bounded private
operation. Retry the durable write, accurately describe the remaining step, and
decide secret lifetime and crash behavior without claiming durable sign-in early.

**Acceptance.** A scripted issuer accepts one exchange; first save fails and the
next succeeds. Storage repair completes that same operation without a second
redemption or loss of the previous durable record.

### H10 Update outcome after the response

**Current behavior.** Install succeeds and the hub replies `Restarting: true`.
A later install-lock, digest or exec failure only reaches stderr; the old hub
keeps serving and the user lacks a precise final update result. Repeating apply
begins a fresh update operation.

**Evidence.** [Update response](../../cmd/evener-hub/app_update.go#L198) precedes
[scheduleHubRestartAfterResponse](../../cmd/evener-hub/app_update.go#L344), whose
abort path logs without publishing an operation outcome.
`TestHubUpdateApplyReleasesLockWhenRestartExecFails` protects lock
release, not client-visible final status.

**Discuss.** Track installed/restart-pending/restart-failed state and make a safe
restart-only retry available. Retain digest/lock validation and the working old
hub; an update promise must not justify executing changed replacement bytes.

**Acceptance.** Fail delayed restart after a successful response. The client
learns the exact installed-but-not-restarted outcome, retains access, and retries
without a redundant download when the installed artifact still matches.

### H11 Remote bootstrap diagnostics

**Current behavior.** First attach without an existing log path/supervisor can
start a detached hub with output redirected to `/dev/null`. A startup failure
then loses the original explanation before the health check reports failure.

**Evidence.** [bootstrapHub](../../cmd/evener-hub/internal/sshconn/manager.go#L2182)
passes an empty log path to
[relaunchCommand](../../cmd/evener-hub/internal/sshconn/version.go#L1862).
Manual host access or a separate supervisor/log setup is the remaining diagnosis
path; an existing configured log path does not have this issue.

**Discuss.** Retain bounded bootstrap diagnostics and return the useful cause or
an accessible diagnostic reference. Choose log lifetime/rotation and avoid
recording credential material.

**Acceptance.** Fail remote startup with a malformed configuration. Connect
identifies the cause and retained diagnostic location instead of only a readiness
failure, with bounded output and no secrets.

### H12 Dormant host notices

**Current behavior.** The notice backend emits `hostOffline` for every offline
nonlocal source, including one with zero affected sessions and no pending Connect
intent. Native independently derives the same condition: an unused offline host
gets an attention row on Board, and a newly appearing offline notice can produce
a temporary banner elsewhere. Initial baseline and held-alert rules suppress
some banners. This is additional attention, not a blocking connection wall.

**Evidence.** Backend [hostNotices](../../cmd/evener-hub/app_notices.go#L123) and
native [notice derivation](../../mobile-native/src/board/notices.ts#L35) do not
require affected work. [BoardNotices](../../mobile-native/src/board/BoardNotices.tsx#L59)
renders attention; [alert events](../../mobile-native/src/alerts/alertEvents.ts#L63)
and [alert policy](../../mobile-native/src/alerts/alertCenter.ts#L345) determine
temporary presentation. Device/visual prominence remains unqualified. This
native path does not establish browser banner behavior.

**Discuss.** Keep dormant state discoverable in Hosts while reserving recovery
attention for interrupted work or pending intent. Decide how informative status
differs from an actionable interruption.

**Acceptance.** A registered unused host causes no blocked-work alert. Losing a
host needed by active work produces a relevant notice that clears on recovery.

## Agent capabilities and terminal workflow

### T01 Tool-call parking

**Current behavior.** After two actual failures of the same semantic tool call
and failure class, dispatch parks the third and subsequent attempts. The parked
result cannot discover that a file, server or dependency has healed. Elapsed
time and successful different calls do not clear it. Repeated successful calls
are explicitly never parked.

**Evidence.** [Registry dispatch](../../agent/internal/tool/registry.go#L719) and
[breaker state](../../agent/internal/tool/breaker.go#L280) own the fingerprint and
park. The production bypass is a
[sandbox-approved rerun](../../agent/session_tools.go#L950). Existing
`TestBreakerDispatch_IdenticalFailureNudgesThenParks`,
`TestBreakerDispatch_ParkedResultDoesNotUnparkTheNextCall` and
`TestBreakerDispatch_IdenticalSuccessLoopIsNeverParked` specify those contracts.

**Discuss.** Allow a bounded probe or relevant dependency-change invalidation
without encouraging tight repeated failure loops. The recovery signal should
relate to the failed operation; unrelated successful work is not proof of health.

**Acceptance.** Fail the same operation twice, heal its dependency, and attempt
the still-needed operation with identical inputs. It can succeed under a bounded
attempt policy without inventing arguments to evade the fingerprint.

### T02 MCP initial discovery recovery

**Current behavior.** A server unavailable during session MCP initialization has
no registered tools. Lazy reconnect lives inside previously registered tool
closures, so it cannot recover that initial absence. Other servers continue
working; a new session discovers the repaired server, but the current session
has no hot discovery/registration owner.

**Evidence.** [Manager initialization](../../agent/internal/mcp/manager.go#L108),
[RegisterTools](../../agent/internal/mcp/manager.go#L351), and
[lazy reconnect](../../agent/internal/mcp/manager.go#L394) establish the gap.
Session [initMCP](../../agent/session_init.go#L2545) runs at construction.
`TestIntgMCP_NewManager_SiblingSurvivesFailure` is a useful isolation
control, not an initial-recovery test.

**Discuss.** Own bounded rediscovery and safe registration while preserving
healthy siblings and selected server identity. Define tool-schema/name changes
and session visibility without requiring a new chat for transient startup loss.

**Acceptance.** Start with one MCP server down, then restore it. Its tools become
usable in the same session; existing tools remain available throughout.

### T03 Unsandboxed tool scope

**Current behavior.** Sandbox off still restricts built-in file writes/edits and
the shell working-directory parameter to the execution root, apart from scratch
exceptions. The unsandboxed shell body can access other paths. Authorized
multi-repository or home-configuration work can therefore require switching tools
to get around an inconsistent capability boundary.

**Evidence.** [WriteFile](../../agent/execenv/local.go#L1661),
[ensureWritePath/EnsureUnderRoot](../../agent/execenv/local.go#L2885), and shell
[working-directory validation](../../agent/session_tools_shell.go#L362).
Existing `TestLocalExecutionEnvironment_WriteFile_OutsideRoot_Rejected`,
`TestLocalExecutionEnvironment_EditFile_OutsideRoot_Rejected` and
`TestLocalExecutionEnvironment_ExecCommand_WorkingDirOutsideRoot_Rejected` specify
the root restriction.

**Discuss.** Define what off and the selected working scope mean consistently
across tools. This is a product policy choice, not a recommendation to silently
widen a sandbox the user chose. Ordinary off-path errors also lack the typed
sandbox-approval route.

**Acceptance.** A user-authorized edit and command outside the launch directory
behave consistently with the chosen mode, without tool substitution. Explicit
scoped sandbox behavior remains consistent with its stated contract.

### T04 Read-only directory symlinks

**Current behavior.** An automatically derived read-only delegate refuses
directory symlink traversal even when the resolved target is inside its allowed
tree. Browsing can map that refusal to not-exist and omit usable content. The
agent may fail to discover ordinary linked project data.

**Evidence.** [Derived delegate scope](../../agent/sandbox_delegate.go#L230),
[secure path traversal](../../agent/execenv/securepath_fdops_unix.go#L107), and
[browse error translation](../../agent/execenv/securepath_browse_fdops_unix.go#L80).
This is separate from the existing useful fallback when an automatic derived
scope lacks an OS sandbox backend.

**Discuss.** Determine whether target-aware traversal within the selected scope
can preserve useful read-only access. Keep loops, symlink replacement races and
out-of-scope targets explicit; an omitted directory should not masquerade as
evidence it does not exist.

**Acceptance.** Read an allowed directory through a symlink while retaining the
selected boundary under loops and concurrent retargeting. Unavailable traversal
has an accurate consequence the agent can act on.

### T05 Approval scope and lifetime

**Current behavior.** Sandbox escalation grants one invocation and only supports
read_file, write_file and edit_file on a root session with an active subscriber.
Batch patch, browsing and shell operations have no equivalent path here;
delegates cannot escalate to the parent through it. Repeated approved file work
can require repeated decisions.

**Evidence.** [Invocation grant](../../agent/session_escalation.go#L58),
[eligibility/tool gates](../../agent/session_escalation.go#L103), and
[rerun](../../agent/session_escalation.go#L206) define the limit.
This is a scoped mechanism, not a claim that every Evener permission flow has
these restrictions.

**Discuss.** Choose meaningful grant scope and lifetime, revocation, and a usable
delegate/batch path. Preserve what the user actually approved; neither automatic
universal approval nor repeated identical prompts follows from their intent.

**Acceptance.** A deliberate grant permits the intended read/edit/read sequence
and supported batch work under an understandable lifetime. Revocation and
out-of-scope requests retain their stated behavior.

### U01 TUI uncertain submission

**Current behavior.** TUI sends create a mutation ID, but error handling restores
the composer and asks the user to retry without retaining that ID for outcome
reconciliation. The next submission creates a new identity. Losing a success
acknowledgement can therefore induce duplicate work; even proven-unsent input
requires manual retry. TUI transport reconnect itself already works.

**Evidence.** [sendHubInput](../../cmd/evener-tui/hub_commands.go#L882),
[hubSendMsg failure handling](../../cmd/evener-tui/hub_update.go#L493), and
[reconnection](../../cmd/evener-tui/hub_reconnect.go#L103).
The Go [TurnStart client](../../appwire/client.go#L589) has no retained pending
mutation coordinator for this path. Existing steer-notification reconciliation
does not establish safe resend of turn/start.

**Discuss.** Retain input and identity, establish the server outcome, and retry
automatically when acceptance is proven absent. Align terminal behavior with the
same exactly-once user intent used by durable client queues.

**Acceptance.** Lose a send response after acceptance, reconnect, and obtain one
turn with correct composer state. No manual comparison or new mutation identity
is needed to recover the original submission.

### U02 TUI failed submission attachments

**Current behavior.** If newer composer content exists when an older submission
fails, TUI preserves the new draft but clears the old submission's attachments.
Clipboard-image and WSL temporary image files can be deleted. The remaining
notice retains text or an image count, not the failed image payload.

**Evidence.** [restoreFailedComposerPayload](../../cmd/evener-tui/hub_attachments.go#L119)
calls cleanup, and [cleanupPendingAttachmentFile](../../cmd/evener-tui/hub_attachments.go#L175)
removes those origins. Existing
`TestHubModelFailureRestorePreservesNewerComposerDraft` and
`TestHubModelFailureRestorePreservesNewerAttachments` protect the newer
draft rather than recovery of the older failed composition.

**Discuss.** Retain failed submissions independently of the currently edited
draft, including media ownership, until sent, restored or deliberately discarded.
Preserving new work must not consume older unresolved work.

**Acceptance.** Send A with an image, type draft B before A fails, and recover A.
Both compositions and the exact original image bytes remain available through
the chosen recovery flow.

### D01 Diagnostic repair authority

**Current behavior.** The bundled doctor repair guide prohibits product-code
fixes within its healing authority. Even proposed fixes to the doctor's own Go
tools require independently generated edits with at least two byte-identical
normalized sources. An authorized repair can become a report/handoff or redundant
voting exercise instead of a concrete tested patch.

**Evidence.** [Doctor repair guardrails](../../internal/bundled/skills/doctoring-evener/references/repair-guardrails.md#L36)
define the voting gate and
[product-code exclusion](../../internal/bundled/skills/doctoring-evener/references/repair-guardrails.md#L61).
The read-only forensic APIs remain useful; the question concerns repair workflow
authority and validation, not mutating evidence during diagnosis.

**Discuss.** Keep diagnosis inspectable while allowing a clear handoff to a
repair-capable workflow within the user's authorization. Decide whether behavioral
evidence and review provide a better acceptance criterion than byte agreement
between generated candidates. These bundled instructions remain unchanged until
the policy is decided.

**Acceptance.** An authorized, diagnosed defect yields a concrete reviewable
repair with meaningful validation and preserved source evidence, without
unnecessary repeated permission or duplicate implementation rituals.

## Recovery that already works

These are useful patterns and preservation checks, rather than new fix requests.

- Browser chat history already demand-pages, and
  [stale cursors trigger a fresh read](../../cmd/evener-hub/frontend/src/stores/threads.ts#L4014).
  Native first connection and initial transcript reads already have retry owners.
  A fallback button by itself does not establish a manual-only experience.
- Browser [IndexedDB recovery](../../cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.ts#L717)
  bounds opening and transaction stalls, retires stale handles and rejects late
  owners. Browser unknown mutations also trigger discovery/reconciliation. Native
  storage-startup backoff and ordinary offline flush exist. Preserve these while
  addressing C05; uncertain commit outcome still needs reconciliation.
- Server [history reads](../../server/history_read.go#L85) can rebuild a failed
  display projection. [Transcript indexing](../../internal/transcriptindex/index.go#L797)
  quarantines one unreadable display entry and continues. Authoritative transcript
  corruption has different requirements from a broken projection.
- [Task-tool loading](../../agent/session_tools_task.go#L340) retries after a
  transient load failure without replacing malformed source with an empty plan.
  [Retained scratch restoration](../../agent/session_scratch_retention.go#L1594)
  repairs specific directory/pin problems and validates the ownership graph.
- [History repair](../../agent/history_repair.go#L127) records interrupted tool
  execution honestly; [lost-job reconciliation](../../agent/jobs.go#L1457) ends
  lost owned runtime with retained output and delivery. Neither blindly restarts
  commands with unknown effects. Explicit Force stop has deliberate resume rules.
- Tool dispatch repairs supported malformed arguments before execution, and
  repeated successful tool calls are never parked. [Plugin loading](../../agent/plugin)
  isolates broken instances; [plugin storage](../../internal/plugins) recovers
  interrupted marketplace rename state and isolates auto-upgrade failures.
  Fresh skill invocations can [recover collected source revisions](../../agent/session_skill_source.go).
- Established remote connections and TUI connections own reconnect loops.
  Host-scoped provider operations do not substitute local credentials for a
  failed remote read. Installed-artifact digest checks and update rollback protect
  working software. Improve recovery ownership without removing those guarantees.

## Coverage and qualification

The map covers the repository's major product responsibilities. Review depth is
recorded here so a subsystem inventory is not mistaken for line-by-line proof of
every branch. A gap is not evidence that the subsystem is correct or broken.

| Map entries | Source paths and behavior reviewed | Further qualification |
| --- | --- | --- |
| S01 Launch and S06 hub | Executable/module wiring, startup state, RPC routing, auth/config loads, host notices and update responses | Whole-hub fault injection and observing unaffected user journeys |
| S02 Browser | Startup/reconnect, creation, transcript paging/projection, mutation recovery, tasks, job log, warnings, launch/setup; settings/navigation sampled through owners | Browser outage journeys, actual IndexedDB lifecycle, keyboard/accessibility, responsive layout and difficult content |
| S03 Native | Connection lifecycle, durable draft/outbox, read/page/Find, rejection actions, model gate, task sheets, warning projection; Board/Projects recovery controls | iOS/Android lifecycle and storage on devices, media retention, touch/accessibility, long-content scroll and complete settings parity |
| S04 TUI | Connection and subscription recovery, turn input identities, pending state, attachment ownership and failure restoration | PTY journeys with dropped responses and actual clipboard/WSL images |
| S05 AppWire/shared clients | Request/notification ownership, connection states, mutation authority, history/task reducers, shared warning projection and creation schema | Cross-client end-to-end lost-response, stale-generation and reconnect qualification |
| S07 Hosts | Attach/supervision, deployment compatibility, scoped config, teardown journals, quarantine and bootstrap | Real SSH interruptions, OS/architecture combinations and exact client notice severity |
| S08 Daemons and S09 sessions/goals | Restore/admission, persistence failure, turn release, goal failure/progress/wake, pending asks and compaction | Crash/restart schedules, concurrent fault/stress testing and chosen goal recovery semantics |
| S10 History | Authoritative transcript versus derived projection, rebuild/quarantine, reader demand and job-log access | Very large histories, rebuild cost, scroll stability and complete read-model corruption matrix |
| S11 Input queues | Accepted/unknown/failed input, durable native/browser recovery and TUI identity/media gaps | Cross-process/device durability, accepted-but-unacknowledged outcomes and stop barriers under races |
| S12 Jobs/delegates/watches | Constructor recovery, watch journal/config/delivery, child restore, terminal attention and task snapshots | Missed-tick/cursor policies, long-running monitors and delegate-generation races |
| S13 Workspace/scratch | Retention repair, ownership graph, missing directory/pin and idle-child interaction; host workspace cleanup seams | Full mount/platform topology and every concurrent workspace allocation/cleanup path |
| S14 Tools/sandbox | Dispatch breaker, argument repair, file/shell scope, read-only symlink handling and escalation lifetime | Native sandbox backend matrix, race-resistant traversal and real multi-repository workflows |
| S15 Providers/credentials | Snapshot lifetime, API-key repair, OAuth process coordination, save failure and malformed configuration | Scripted subprocess rotation and live provider-specific API/authentication behavior |
| S16 Model catalog | Directory-scoped cache, warmup, stale refresh and client loading gates | Cold project latency and project overrides with slow/live catalog sources |
| S17 Plugins and S18 skills/hooks | Fail-soft loading, interrupted store recovery, auto-upgrade isolation, source revision recovery, hook deny/error distinction | Every marketplace/schema form and extension-specific runtime; no claim that all extensions recover |
| S19 MCP | Initial discovery, registered-tool reconnect, sibling isolation and recovery presentation | Real transport/server-schema changes and hot-registration lifecycle |
| S20 Install/update | Single executable, archive/build resolution, remote bootstrap, installed-file versus running-process state | Real install/restart/rollback on each supported platform |
| S21 Diagnostics | Read-only doctor construction, source evidence and operative repair instructions | A complete authorized diagnosis-to-repair journey and diagnostic presentation |
| S22 Shared foundations and S23 development | Workspace/module inventory, process/identity primitives and owning callers, documented gates and meaningful existing tests | Full lint/vet/test, race/fuzz/stress suites and generated/platform qualification when implementing changes |

Existing deterministic tests cover selected failure contracts and recovery
controls; no live account, provider, SSH, install, UI or device failure is implied
by that evidence. The cases remain source-backed product questions until their
acceptance scenarios are exercised for an approved change.

## Maintaining the punchlist

Keep one case per meaningful product decision, with the trigger, current owner,
existing recovery, evidence, proposed direction and acceptance scenario together.
Merge duplicate symptoms when they share a cause; keep different owners distinct
when their recovery choices differ. Recheck source links and named symbols as
code moves. Do not infer a current defect from a historical design document or a
search hit containing "fail closed".

Record a decision only after discussion. When a case is implemented and verified,
move its lasting behavior into the owning guide and remove the resolved case and
index row. Preserve other IDs; Git retains the history. Keep this an open product
queue, not a dated audit log or a second implementation plan.
