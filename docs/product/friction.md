# Product friction punchlist

This is the open product queue for places where Evener makes the user manage
its machinery, loses the ability to recover, or imposes a restriction that needs
a product justification. Use the [principles](principles.md) to assess the user
cost and the [subsystem map](subsystems.md) to find the owning code.

Cases with agreed behavior changes remain open until implemented and verified.
**Decision** records an agreed product outcome; **Discuss** describes an unresolved
proposal. **Deferred** cases retain their evidence and any agreed direction but
are not selected for implementation unless their deferral condition changes or
the user explicitly resumes the work. None is an implementation plan
or a claim that behavior has changed.
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
| [C12](#c12-remote-credential-transfer-outcomes) | Medium | Reconnecting hides a completed credential-transfer report; uncertain transfers require manual investigation | S02, S05, S07, S15 |
| [R01](#r01-transcript-durability-stop) | High | A healed storage problem leaves the chat permanently stopped | S08, S09, S10 |
| [R02](#r02-finished-turn-ownership) | High | A finished turn continues blocking new messages | S09, S11 |
| [R03](#r03-watch-intent-after-restart) | High | Restart can end monitoring without informing the owning agent | S08, S12 |
| [R04](#r04-idle-child-blocks-parent-restore) | High | One idle child's IO problem prevents its healthy parent from restoring | S08, S12, S13 |
| [R05](#r05-goal-blocking-after-transient-failure) | Medium | A temporary execution failure leaves an authorized goal blocked | S09, S15 |
| [R06](#r06-read-only-goal-progress) | Medium | Useful research can count as no progress while attempted writes count | S09, S14 |
| [R07](#r07-restored-goals-without-a-wake) | Medium | An active restored goal can remain idle until another turn arrives | S08, S09 |
| [R08](#r08-compaction-with-a-pending-question) | Low (deferred) | A pending question prevents explicit compaction | S09, S10 |
| [H01](#h01-credential-store-failure-scope) | High | A credential-file problem prevents the entire hub from starting | S01, S06, S15 |
| [H02](#h02-live-daemons-after-api-key-repair) | High | Saving a corrected API key leaves running sessions using the old key | S08, S15 |
| [H03](#h03-host-journal-failure-scope) | High | Incomplete host-journal quarantine prevents unrelated local work | S06, S07 |
| [H04](#h04-retaining-explicit-connect-intent) | High | A transient first Connect failure has no continuing retry owner | S02, S03, S07 |
| [H05](#h05-connection-versus-build-synchronization) | High | Build synchronization can block an otherwise compatible remote host | S07, S20 |
| [H06](#h06-provider-file-repair-discovery) | Medium | An invalid provider edit hides retained working configuration from new launches | S06, S15 |
| [H07](#h07-proven-no-op-teardown-remnants) | Medium | Already-harmless teardown remnants still require manual recovery | S07 |
| [H08](#h08-oauth-refresh-across-processes) | Medium | Shared rotating credentials can race and provoke another sign-in | S08, S15 |
| [H09](#h09-issued-credentials-awaiting-persistence) | Medium | A local save failure discards successfully issued credentials | S06, S15 |
| [H10](#h10-update-outcome-after-the-response) | Medium | An update says restarting before a failure that only reaches logs | S06, S20 |
| [H11](#h11-remote-bootstrap-diagnostics) | Low | First-start output is discarded when it would explain a failed connection | S07, S20, S21 |
| [H12](#h12-dormant-host-notices) | Low | An unused disconnected host becomes a recovery notice | S03, S06, S07 |
| [T01](#t01-tool-call-parking) | High | Repeated failure parks an operation even after the dependency heals | S14 |
| [T02](#t02-mcp-initial-discovery-recovery) | High | An initially unavailable MCP server never contributes tools to the session | S18, S19 |
| [T04](#t04-read-only-directory-symlinks) | Medium (deferred) | A derived read-only delegate cannot browse an allowed target through a symlink | S12, S14 |
| [T05](#t05-approval-scope-and-lifetime) | Medium (deferred) | Narrow one-call approval paths repeatedly interrupt authorized work | S12, S14 |
| [U01](#u01-tui-uncertain-submission) | High | A lost send acknowledgement becomes a manual new submission | S04, S05, S11 |
| [U02](#u02-tui-failed-submission-attachments) | High | Preserving a newer draft discards the older failed submission's images | S04, S11 |
| [D01](#d01-diagnostic-repair-authority) | Medium (deferred) | Bundled repair instructions prohibit product fixes and require byte-identical voting | S21 |

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

**Decision.** The browser automatically recovers a failed first connection.
Retain one startup retry owner with paced, capped backoff and no attempt or
elapsed-time limit for recoverable failures. Preserve the route and draft while
the hub is unavailable; online or focus events can advance recovery without
creating competing clients. Replacement or explicit cancellation ends the old
owner's attempts. Show quiet connection progress rather than requiring Retry to
restart recovery. An actual authentication or protocol requirement must remain
accurate and actionable; a transport failure alone does not require user
intervention. Implementation remains pending.

**Acceptance.** Open the browser while the hub is unavailable, then restore the
hub without touching the page, including after a prolonged outage. Exactly one
client becomes usable and the user's work remains intact. Repeated focus events
and late results from a replaced client cannot create duplicate connections or
overwrite the active client's state. Successful connection retires recovery.

### C02 Uncertain session creation

**Current behavior.** If session creation commits but its response is lost,
native holds an unconfirmed draft and suggests checking the Board or changing the
draft. Browser reports an error and permits another Start. Neither can identify
the result by a durable creation request ID. The user must investigate or risk a
duplicate; editing the draft does not resolve the first attempt.

**Evidence.** [ThreadStartParams](../../appwire/types.go#L2316) has no client
creation key. The hub
[detaches creation from peer cancellation](../../cmd/evener-hub/app_threadlifecycle.go#L82).
Native [submit](../../mobile-native/src/newSession.ts#L429) persists uncertainty;
browser [creation failure](../../cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx#L2257)
clears the busy state. Native `newSession.test.ts` tests the held unchanged draft.

**Decision.** Treat each Start submission as one durable request. Preserve its
original prompt and attachments and retain its identity across reconnection and
retry. Reconcile the authoritative creation outcome: recover the existing chat
if it was created, or continue trying to complete the request without creating a
duplicate. Keep the request pending until success, explicit cancellation or a
specific problem that genuinely requires user input. Editing another draft does
not discard or silently replace the unresolved submission. A pending state with
cancellation during an outage is preferable to asking the user to hunt for a
chat or press Start again. Implementation remains pending.

**Acceptance.** Drop the response after creation commits. Reconnecting recovers
exactly one chat with its original input; repeated attempts retain the same
identity and do not duplicate the initial turn. Lose a request before acceptance
and recover automatically when service returns. Preserve the submitted text and
images independently of any newer draft. Cancellation stops further creation
attempts and reconciles any already-created result; a genuine input problem
preserves the request and identifies the needed correction.

### C03 Transcript repair rereads

**Current behavior.** Browser hydration retries ordinary failures, but suppresses
its retry for a structured history failure when a base transcript already exists.
A quiet session can remain incomplete until a later event, reopen or reconnect.
Native open-session rehydration also retains open status on failure, outside its
initial-read retry loop. The server can repair a projection on a later read.

**Evidence.** Browser
[hydrateAndSubscribe](../../cmd/evener-hub/frontend/src/stores/threads.ts#L1818),
native [rehydrate](../../mobile/src/state/conversation.ts#L2017) and
[useReadRetry](../../mobile-native/src/session/useReadRetry.ts#L46), and server
[recoverForRead](../../server/thread_history.go#L355) expose the ownership gap.
Shared `reducer.history.test.ts` specifies that a later success clears history failure.

**Decision.** Keep the retained transcript, reading position and draft while
automatic recovery remains owned by the open or otherwise needed chat, including
after structured history failures and failed refreshes of an already-open chat.
Retry with backoff without an attempt limit. Arm recovery only after a failed
read and retire it on authoritative success: a healthy chat incurs no additional
polling, network requests or disk reads for this mechanism. A server read can
rebuild a failed projection, so deduplicate concurrent recovery and pace
expensive rebuilds as well as network retries. Restore missing content and clear
the error on success. Classify persistent source damage separately; preserving
its diagnostic need not prohibit a later appropriate health check or justify
discarding the recorded history. Implementation remains pending.

**Acceptance.** Fail an already-open transcript read, repair its backing
condition, and leave the session idle. Content recovers without reopening or
losing the reader's position. A healthy idle chat emits no recovery reads or
rebuilds; after a failure recovers, advancing the retry clock produces no further
recovery requests.

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

**Decision.** Keep reconciling queued and unconfirmed input automatically until
it is delivered, confirmed already applied, explicitly stopped, or genuinely
rejected and needs correction. Own recovery for unresolved targets while the
app can communicate with the hub, including sessions behind other screens.
Backoff bounds the request rate without an attempt limit; a failed proof read
must not abandon the original send intent. Reconcile before dispatch and retain
the original mutation identity: settle an already-applied operation without
resending, and retry the original operation when authoritative state establishes
that it was not accepted. Preserve stop epochs, stale-client rejection and
explicit Stop or cancel holds. Unknown outcomes require reconciliation, not
blind replay. Empty or fully settled outboxes need no added recovery polling.
Implementation remains pending.

**Acceptance.** Queue input, leave for Board, reconnect, fail the first proof
read through a prolonged outage, then let reads succeed without another event.
The original operation sends or settles exactly once, with its text and
attachments intact. An already-applied operation settles without resending;
Stop-held input stays held. A stale connection cannot release newer holds or
dispatch input, and no recovery reads continue after the outbox settles.

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

**Decision.** Make a genuinely rejected message recoverable as a complete
editable draft: preserve its text, images, attachment order and references to
images in the text. Preserve any newer draft independently. Keep the original
recovery record until the restored draft is durably saved, so interruption
during restoration cannot lose the composition. If an image is genuinely
unavailable, retain everything else and identify the missing attachment so the
user can replace or remove it. Do not silently omit it. Transient failures and
uncertain delivery follow C05's automatic reconciliation; this editing path is
for a confirmed rejection that requires correction. Implementation remains
pending.

**Acceptance.** Reject input containing text and two images, correct the cause,
and restore it. The exact composition survives restart and can be sent once.
Interrupt restoration before the durable draft save: the original remains
recoverable. Restore while a newer draft exists: both compositions survive.
Make one image unavailable: the rest remains editable and the missing image is
represented until the user replaces or removes it.

### C07 Model discovery and default launch

**Current behavior.** Native includes model loading in the form's busy gate even
for Hub default, and an open picker has no continuing retry after discovery
fails. A previously selected model also cannot start after a listing failure;
the form tells the user to choose Hub default. Refresh clears the displayed
catalog before its replacement arrives. Hub startup warms only the
empty-working-directory cache entry, while web and native project pickers
request their project directory. Those first reads can still wait for discovery;
later stale entries already refresh in background.

**Evidence.** Native [form busy state](../../mobile-native/src/newSession/NewSessionForm.tsx#L74),
[loadModels/submit](../../mobile-native/src/newSession.ts#L321), and
[ModelPicker](../../mobile-native/src/newSession/ModelPicker.tsx#L23); hub
[fetchLaunchModels](../../cmd/evener-hub/app_models.go#L136) and
[warmLaunchModels](../../cmd/evener-hub/app_models.go#L316).
`app_models_test.go` separately tests directory-scoped caching and startup warmup;
that does not establish a warm first project picker. The native form's
[chosen-model test](../../mobile-native/src/newSession/NewSessionForm.test.tsx#L545)
pins the requirement to change to Hub default after a listing failure.

**Decision.** Let a valid default or already-chosen model launch proceed without
requiring a successful catalog lookup first; the hub still resolves and
validates the requested launch configuration. Retain usable catalogs during
refresh and keep retrying failed discovery while the picker needs it, with
backoff and no attempt limit. Preserve an explicitly selected provider/model;
discovery failure must not substitute the default. Keep cached choices scoped
to the selected host and project configuration. Use existing caches rather than
adding a broad prefetch loop across projects; retire failure recovery after a
successful lookup. A genuine launch rejection preserves the draft and selected
configuration for correction. Implementation remains pending.

**Acceptance.** Delay model listing: Hub default still launches promptly. Fail
listing with a specific model already chosen: submit that same configuration
without switching models. Keep the picker open through an outage: retained
choices stay usable and discovery recovers automatically. Host/project changes
never reuse another context's default or configuration. An actual launch
rejection leaves the draft and model choice intact, and successful discovery
retires the failure retry timer.

### C08 Remote provider setup at launch

**Current behavior.** Browser launch on a remote host without a provider says to
configure one on that host and offers a retry check. Existing host-scoped
credential push and remote Codex sign-in live in Settings, so the user must find
that route and return to the draft.

**Evidence.** [Spawn provider gate](../../cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx#L2650)
differs from the available
[remote sign-in](../../cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsHostScope.tsx#L188)
and [credential transfer](../../cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsHostScope.tsx#L404)
actions. `useProviderSetup` already preserves exact-host scope. The
[transfer implementation](../../cmd/evener-hub/app_host_credentials.go#L38)
copies eligible API keys to matching remote instances; it does not create
missing custom provider instances or transfer stored credential JSON.

**Decision.** Connect the launch blockage to supported setup for the selected
host, preserving prompt, images, project and model choice. Offer the existing
remote sign-in and credential-transfer actions in that flow, clearly naming
the destination and what a transfer copies. Refresh readiness automatically
after setup. If Start has already been requested, continue that same pending
launch once its prerequisites are satisfied, subject to cancellation or changed
intent; otherwise leave the preserved form ready. Some custom providers still
require setup on the host, which needs a specific next step. A failed readiness
read is an unresolved check to retry, not proof that setup must be repeated.
Implementation remains pending.

**Acceptance.** Complete a supported remote sign-in or chosen credential transfer
from the blocked launch without route hunting or accidental local-host
substitution. The original composition and settings survive. A pending Start
continues once on the selected host after readiness is confirmed; cancellation
prevents it, and a form without a pending Start remains ready for submission.

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

**Decision.** Recover a failed task-list read automatically for as long as the
panel remains open. Pace retries with backoff and no attempt limit. Retain the
last list, expanded rows and reading position, with a quiet indication that the
display is out of date and recovery is underway. Avoid duplicate error toasts or
requests for intervention when the product can perform the recovery. If no list
has loaded, show that retrieval is still pending rather than claiming there are
no tasks. Clear the recovery indication and retire retries after success;
healthy panels continue using their existing notifications without added
polling. Actual failed tasks retain their normal status. A confirmed unsupported
source or deleted session gets an accurate explanation. Implementation remains
pending.

**Acceptance.** Fail a task snapshot, then restore reads without task updates or
reconnect, including after a prolonged outage. The open panel recovers by itself
and keeps previously expanded rows and reading position. An initial read failure
does not present an empty task list, repeated attempts do not produce repeated
alerts, and successful recovery retires the retry timer. A healthy panel adds no
polling, and closing the panel ends its recovery reads.

### C11 Successful recovery looks like failure

**Current behavior.** Successful MCP reconnect emits an EventWarning saying no
action is needed. Its missing informational code makes it a critical warning in
shared projection and gives it attention or failure styling on web and native.
The recovery event is emitted before the retried tool call returns, so connection
recovery does not establish that the tool operation succeeded.

**Evidence.** [reconnectRecoveryWarning](../../agent/session_init.go#L2600),
[warning classification](../../appwire-client/typescript/warnings.ts#L27), web
[WarningItem](../../cmd/evener-hub/frontend/src/panes/session/transcript/messages/WarningItem.tsx#L67),
and native [warning projection](../../mobile-native/src/projectedRows.ts#L447).
The [MCP tool callback](../../agent/internal/mcp/manager.go#L405) emits recovery
before making the retry and separately returns its actual result.
This is a source-traced presentation path; visual/device qualification remains.

**Decision.** Give successful repair structured informational meaning and a quiet
diagnostic record, available in full/detail views without attention styling in
the normal conversation. State only what recovered: a restored connection does
not prove that the retried operation succeeded. Keep an actual failed tool call,
ongoing interruption or required sign-in accurately represented in its own
result or status. Implementation remains pending.

**Acceptance.** After a dropped MCP connection recovers, both clients show normal
operation with inspectable recovery history and no failure styling for success.
Verify both direct warning items and their system-notice representation: normal
views do not draw attention to the success, and full/detail views render it
quietly.
Make the retried operation fail after reconnect: its real failure remains visible
and the connection-recovery record does not claim the operation succeeded.

### C12 Remote credential transfer outcomes

**Current behavior.** The remote Settings action retains a credential-transfer
report in component state but hides it whenever the browser's connection
generation changes. Even a completed successful transfer becomes a warning that
the replacement connection never saw its outcome. A timed-out or interrupted
transfer also has no automatic reconciliation owner; the user is told to inspect
the host and decide whether to send the credentials again.

**Evidence.** [PushCredentials](../../cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsHostScope.tsx#L404)
ties reports to the transport generation;
[staleOutcome](../../cmd/evener-hub/frontend/src/panes/settings/sections/credentials/CredentialsHostScope.tsx#L437)
hides a settled report after replacement, and the unknown-outcome branch asks
for manual investigation. `CredentialsHostScope.push.test.tsx` pins timeout,
in-flight connection replacement and settled-then-replaced presentation. The
server's [conditional credential writes](../../cmd/evener-hub/app_host_credentials.go)
check the remote source and configuration revision before changing an entry.

**Decision.** Keep completed results available with their actual originating hub
and host identity through transport reconnection. Give genuinely unconfirmed
transfers a recovery owner that checks authoritative remote state and preserves
the transfer identity and per-entry progress independently of the socket.
Establish what landed and complete any still-needed part of the original request
when authoritative state establishes that doing so is appropriate. Respect newer
credentials, changed host registration and cancellation. A socket replacement
alone must not turn known success into an unresolved operation. Prioritize
everyday chat recovery ahead of this narrower workflow. Setup navigation is
covered separately by C08. Implementation remains pending.

**Acceptance.** Complete a transfer and reconnect to the same hub and host: its
report remains available. Lose the response after a remote write: recovery
recognizes the applied result without requiring manual investigation or
rewriting it. A partially applied transfer preserves its completed entries and
finishes still-needed entries once their outcomes are established. A genuinely
changed destination is represented accurately, and newer remote credentials are
not overwritten by recovery of an older operation. Cancellation prevents further
transfer while retaining the record of what already happened.

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

**Decision.** Restore the parent and healthy delegates while the affected idle
child remains temporarily unavailable. Preserve that child's identity, files,
history and unfinished obligations, and tell the parent agent which delegate is
unavailable and why. The system owns recovery and retries recoverable failures;
independent parent work can continue. When a step needs the unavailable child's
result, the parent agent can wait, investigate or choose an alternative within
the user's task. Temporary unavailability does not mean the child is finished,
deleted or forgotten. Distinguish an idle child from an active process that may
still change the workspace, and preserve required authoritative scratch.
Implementation remains pending.

**Acceptance.** Make one idle child's storage unreadable. The parent opens and
can continue independent work, other children remain usable, and the parent
agent receives the affected child's status and reason. Its identity, data and
unfinished obligations remain intact. After storage heals, the child becomes
available with its original state without requiring the user to repair the
parent chat. An unavailable dependency is never reported as completed.

### R05 Goal blocking after transient failure

**Current behavior.** Provider calls already retry, but a final execution error
can change an active goal to blocked. A temporary outage that outlasts those
attempts can therefore end authorized continuation. Later ordinary messages do
not reactivate the goal; a new goal action is required.

**Evidence.** [Model-call retry exhaustion](../../agent/session_model_call.go#L820)
feeds [goal failure handling](../../agent/session_goal.go#L408); continuation
requires active status. User interrupt and root shutdown already have distinct
handling and should retain their meaning.

**Decision.** A temporary provider failure leaves the authorized goal waiting
rather than blocked. Continue retrying with backoff and honor provider retry
timing; a fixed attempt count or elapsed time must not abandon the objective.
Preserve the goal and completed work, show a quiet and accurate waiting status,
and automatically continue when the provider recovers, even after a long outage.
Waiting alone does not expire the user's instruction. Recheck cancellation,
applicable budgets and newer instructions before resuming, so recovery continues
the user's current intent. Implementation remains pending.

**Acceptance.** Let a temporary scripted provider failure persist well beyond
normal retries, then heal it. Requests remain paced during the outage,
and the authorized goal continues with its preserved progress without a repair
click or re-entering the objective. Canceled and budget-exhausted goals do not
resume; newer instructions take precedence over the objective that was waiting.

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

**Decision.** Useful investigation, reasoning, planning and review qualify as
progress toward the user's objective. An agent does not need to change files to
keep a goal running, and attempting a mutation does not establish progress by
itself. Suspected loops prompt reassessment of the evidence, obstacle and
available approaches before a decision to block the goal. A blocked outcome
identifies a concrete impediment the agent cannot resolve. Preserve explicit
resource limits and cancellation. Implementation remains pending.

**Decision — evaluator authority.** A progress evaluator may eventually block a
goal, but its first supported finding of stalled work goes to the working agent
as specific evidence and an opportunity to change approach. A later assessment
examines progress since that intervention. Blocking requires a persistent,
concrete impediment after the agent has had an opportunity to recover. An
unavailable or inconclusive evaluator leaves authorized work running, subject
to explicit resource limits and cancellation.

**Discuss — progress evaluator.** A side LLM call that reviews recent turns and
returns a structured assessment is a candidate for evaluating useful progress.
The evaluator needs the current objective and relevant instructions as well as
observed actions and results; a short transcript window alone can omit what the
work is meant to accomplish. Existing [auxiliary call
routing](../../agent/internal/cheapmodel/caller.go#L60) and [schema-validated
output](../../llm/generate_object.go#L32) provide implementation references.
Context selection, evaluation cadence and model choice remain open design
questions. The proposed assessment distinguishes progressing, waiting, stalled
and uncertain work, cites evidence from the supplied turns and suggests a next
step. A bounded context window should preserve complete action/result pairs and
enough earlier progress and dependency context to distinguish new learning from
repetition. Choosing the evaluator mechanism remains implementation design work.

**Acceptance.** A read-only investigation producing useful evidence can finish
without false no-progress blocking. Repeated ineffective or failed mutating
calls prompt reassessment rather than resetting progress solely because of the
tool category. A blocked outcome explains the unresolved impediment; explicit
resource limits and cancellation still take effect. When an evaluator flags a
stall, an agent that changes approach and makes useful progress continues. A
persistent, evidenced impediment can lead to blocking after the recovery
opportunity. An unavailable or inconclusive evaluator does not stop the goal.

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

**Decision.** Automatically resume an unfinished active goal when its runtime
finishes restoring and is ready. Schedule one continuation, accounting for work
already queued or running, without requiring another user message. Preserve a
legitimate wait for a necessary answer or dependency, and honor cancellation,
applicable limits and newer instructions. Give the agent restored history and
actual job outcomes so it can reconcile interrupted work before repeating an
action and decide what work needs restarting. Restoring the goal continues the
objective; session-owned shell jobs retain their agreed shutdown lifetime.
Implementation remains pending.

**Acceptance.** Restore an idle active goal with no queue or external event. It
continues once without a user nudge. Necessary answer/dependency waits remain
intact, canceled and budget-exhausted goals do not resume, and an already-pending
continuation is not duplicated. The agent receives actual job outcomes and
reconciles uncertain results before repeating interrupted work.

### R08 Compaction with a pending question

**Current behavior.** Explicit Compact refuses while an ask is pending, telling
the user to reply or clear it first. The user cannot reduce older context while
preserving an unanswered question.

**Evidence.** [Compact](../../agent/session_compaction.go#L30) checks pending asks
before compaction, and the [explicit command
callback](../../cmd/evener/serve.go#L1430) uses that entry point. Automatic
[context management](../../agent/internal/contextmgr/context_strategy.go#L91)
calls the context manager separately. Existing `session_ask_test.go` tests the
explicit-compaction refusal.

**Deferred.** Keep the existing behavior. This narrow explicit-compaction case
does not justify implementation work without evidence of material user impact.
Revisit if a concrete workflow needs to reclaim context before answering a
pending question and the refusal obstructs useful work.

**Acceptance if revisited.** Compact a large session while a question is pending,
answer it afterward, and continue once with the same question identity and
preserved required context.

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

**Decision — file permissions.** Automatically repair over-permissive modes on
Evener's own, user-owned credentials file and continue. If permission repair
cannot succeed but the file remains readable and valid, keep the hub and its
credentials usable. Retain responsibility for retrying the repair and expose the
condition in credential diagnostics. A file-mode problem alone must not disable
the product or prevent valid credentials from being used. Implementation remains
pending.

**Acceptance — file permissions.** Start with an owned over-permissive credentials
file containing valid keys. Repair its mode without changing its contents and
continue normally. When mode repair fails, the hub and credentials remain usable;
repair retries converge after the filesystem condition heals without a restart.

**Decision — unreadable or malformed data.** Keep the hub running when the
credentials file cannot be read or parsed. History, navigation and work with
usable authentication remain available. The product owns recovery: retry
temporary read failures, recover available credential data while preserving the
original file, and adopt recovered credentials without requiring a hub restart.
Do not replace an unreadable or malformed store as though it were empty. Only
work that lacks usable authentication waits, retaining its task and pending
requests. Request replacement authentication when required credential material
cannot be recovered. Implementation remains pending.

**Acceptance — unreadable or malformed data.** Start with an unreadable or
malformed credentials file. History, navigation and independently authenticated
work remain usable, and existing credential data is preserved. Heal a temporary
read failure or repair the credential data: recovery discovers the usable
credentials and lets affected pending work resume without a hub restart. A
request that still needs replacement authentication retains its original task.

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

**Decision.** Saving a corrected API key updates running sessions configured to
use that credential at their next model request. Work waiting on the affected
authentication failure retries automatically in the same chat, without a manual
Retry or runtime replacement. Preserve active work and session-owned shell jobs;
credential repair must not require restarting their execution runtime. Continue
to respect each session's selected credential source and explicit project
configuration. Implementation remains pending.

**Acceptance.** Start a daemon with a rejected scripted API key and let work wait
on authentication. Save its replacement through provider settings. The same
session automatically retries with the new key at the next eligible request,
without a user nudge or runtime replacement. Existing shell jobs remain intact,
and a session configured to use a different credential retains that selection.

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

**Decision.** Keep the hub usable during host-journal recovery. Local work,
history, navigation and other capabilities independent of the damaged records
remain available. Preserve the journal and recovery records while the product
automatically investigates, reconciles actual host state and repairs the
bookkeeping. Only operations that depend on unresolved state wait; recovery
resumes their intent according to the reconciled outcome. If the affected hosts
cannot be identified, the temporary limitation may cover remote deployment or
restart operations more broadly, while independent hub capabilities remain
usable. Implementation remains pending.

**Acceptance.** Start with a truncated journal or incomplete custody. Local
navigation and unrelated work remain available, and original/recovery bytes are
preserved. Operations whose prior outcomes are uncertain wait for reconciliation
without being blindly repeated. Once recovery establishes the relevant state,
affected work proceeds according to that outcome without a user repair click.

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

**Decision.** An explicit Connect establishes continuing intent until it succeeds
or the user cancels it. Retry temporary failures with backoff, even through a
long outage, and show an accurate waiting/connecting state with cancellation
available. Navigating away from host settings does not cancel the request, and
one attempt's timeout does not discard it. If required authentication is missing,
retain the request while obtaining it and continue automatically when available.
Explicit Disconnect or cancellation ends the request; intentionally disconnected
hosts remain dormant. Implementation remains pending.

**Acceptance.** Click Connect once while SSH is unavailable, navigate away, then
restore SSH after a prolonged outage. Attempts remain paced and the host attaches
without another click. Disconnect or cancellation during recovery prevents later
attachment. Supplying required authentication resumes the retained request
without another Connect, while an intentionally disconnected host stays dormant.

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

**Decision.** When matching-build synchronization cannot complete, Connect uses
the healthy, compatible remote build already running. Verify the live host's
connection protocol and required capabilities, then let the user work and show
the actual local and remote versions in host details. Preserve an explicitly
requested exact-build requirement; connecting to a different build does not
satisfy it. Actual protocol incompatibility still requires an update or another
compatible connection path. Implementation remains pending.

**Acceptance.** Connect a development controller to a compatible different-target
host without an available matching artifact. The connection succeeds using the
verified running build, and version details identify what each side actually
runs. A genuinely incompatible host is accurately identified, and a requested
exact-build synchronization is not reported complete merely because connection
to another build succeeds.

### H06 Provider-file repair discovery

**Current behavior.** The hub's notice watcher detects external providers.toml
edits and retries failed loads. A valid repair clears the error and invalidates
client settings and model listings automatically. Invalid edits retain the
previous usable hub registry and preserve the edited bytes. Child launches and
launch-model discovery still disable the user layer whenever the source file
cannot load, so they cannot use the explicit providers retained by the hub.
In-app writes acknowledge their persisted bytes, so the watcher does not repeat
the reload and client invalidation for the same write.

**Evidence.** [refreshProviderFile](../../cmd/evener-hub/app_provider_reload.go#L14)
owns file observation and retries;
[watchRead](../../cmd/evener-hub/app_notices.go#L258) invalidates clients when the
registry or diagnostic changes.
[ProviderRegistry.Reload](../../cmd/evener-hub/internal/hubcore/registry.go#L121)
retains the previous registry on failure. The remaining launch gap is in
[childNoUserLayer](../../cmd/evener-hub/spawn.go#L89), which excludes the user
layer when [WritesRefused](../../cmd/evener-hub/internal/hubcore/registry.go#L695)
reports a load error. [Instance writes](../../cmd/evener-hub/app_instances.go#L65)
update the watcher's signature from the exact persisted representation under
the same lock used by observation.

**Decision.** Detect provider-configuration changes and automatically adopt a
valid repair. Clear the stale load error and update open settings and launch
forms without requiring a restart, manual reload or unrelated save. While an
edit is invalid, keep usable previous configuration active where available,
preserve the edited file and show the specific problem. Make clear when running
configuration differs from an invalid edit, then converge as soon as the
replacement validates. Failed loads retain a recovery owner rather than
remaining cached indefinitely. Retained configuration must also remain usable
by child launches and model discovery. Avoid redundant invalidations after
in-app writes. The remaining child-configuration gap keeps the case open.

**Acceptance.** Start with malformed provider configuration, repair it on disk,
and keep the same settings/launch surface open. The repaired instances appear
and the stale error clears without another user action. Also make an invalid
intermediate edit to a previously usable configuration: working providers remain
available, the edited bytes are preserved, and a valid replacement is adopted
automatically with the updated settings accurately reflected.

### H07 Proven no-op teardown remnants

**Current behavior.** A teardown record can fence Connect before its already
harmless remnant is reconciled. Recovery can prove certain paths are a no-op, yet
the user must select retry/recover to clear the obstruction. Ambiguous live
workers and provably absent/already-restored artifacts are distinct cases.

**Evidence.** [Attach fencing](../../cmd/evener-hub/app_host_attach.go#L86),
[teardown no-op handling](../../cmd/evener-hub/app_host_teardown_ops.go#L585), and
[cleanup handle resolution](../../cmd/evener-hub/app_host_teardown_ops.go#L747) show the
ordering. Provenance checks still protect unrelated or foreign cleanup targets.

**Decision.** Automatically reconcile teardown leftovers as part of Connect
before refusing the connection. When the recovery evidence establishes that the
old target is gone or the relevant state is already restored, settle the
bookkeeping and continue the original connection request without a separate
retry/recover action. A stale record alone does not justify stopping a new
process, removing unrelated files or treating uncertain cleanup as complete;
those cases still require investigation. Implementation remains pending.

**Acceptance.** Leave a teardown journal whose artifacts are demonstrably
already restored/absent. One normal Connect establishes the completed state,
settles the record and succeeds without another user action. Ambiguous live
ownership and foreign artifacts remain preserved while recovery investigates;
the reconciliation does not stop a replacement process or repeat finished work.

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

**Decision.** Coordinate sign-in renewal across all session processes sharing the
same credential record. One process refreshes while other callers reuse its
result. Re-read the record after acquiring refresh authority and check for newer
usable credentials before treating a rejection as requiring sign-in. Recover
coordination when its owner crashes, and honor cancellation while callers wait.
Preserve the selected account identity and accurately request sign-in for
genuinely revoked credentials. Implementation remains pending.

**Acceptance.** Two subprocesses use one expired record and a scripted rotating
issuer. One redemption occurs and both requests receive the committed replacement
without a sign-in prompt. A rejected older attempt discovers a newer usable
record automatically. A process exiting while holding refresh coordination does
not leave other callers waiting permanently; cancellation remains effective. A
genuinely revoked unchanged record still requests sign-in.

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

**Decision.** Retain successfully issued credentials in memory and make them
usable while valid, even when the local save fails. Automatically retry saving
that issued result rather than redeeming the original code or token again.
Distinguish usable authentication with saving pending from durably saved sign-in.
A pending save must respect sign-out, account changes and newer credentials;
it must not restore removed credentials or overwrite their replacement. If the
holding process exits before persistence succeeds, the unsaved credentials may
be lost and sign-in may be needed again. That limitation is acceptable.
Implementation remains pending.

**Acceptance.** A scripted issuer accepts one exchange and the first save fails.
The issued credentials remain usable while valid; storage repair persists the
same result without a second redemption. Preserve the previous durable record
until its replacement is saved atomically. Sign-out, account changes and newer
credentials prevent a pending older save from restoring or overwriting them.
Pending persistence is never reported as durably saved, including when the
holding process exits before saving succeeds.

### H10 Update outcome after the response

**Current behavior.** Install succeeds and the hub replies `Restarting: true`.
A later install-lock, digest or exec failure only reaches stderr; the old hub
keeps serving and the user lacks a precise final update result. The browser stops
waiting after 30 seconds and asks the user to check logs, even when the old hub
never stopped. Repeating apply begins a fresh update operation.

**Evidence.** [Update response](../../cmd/evener-hub/app_update.go#L198) precedes
[scheduleHubRestartAfterResponse](../../cmd/evener-hub/app_update.go#L344), whose
abort path logs without publishing an operation outcome. Browser
[restart polling](../../cmd/evener-hub/frontend/src/stores/hubUpdate.ts#L71)
and [timeout presentation](../../cmd/evener-hub/frontend/src/panes/settings/sections/hubUpdates.tsx#L99)
do not identify the failed restart step.
`TestHubUpdateApplyReleasesLockWhenRestartExecFails` protects lock
release, not client-visible final status.

**Decision.** Keep an update request pending until the requested version is
confirmed running. Keep the working old hub available and accurately distinguish
completed installation from a pending restart. Automatically retry recoverable
restart failures, even when recovery occurs later than the initial attempt; the
original update request authorizes that continuation. Retry the outstanding
restart without downloading or installing again when the installed artifact
still matches. Retain digest and installation-lock validation. If another update
has replaced the file, reconcile the newer operation before restarting.
Cancellation or a superseding request takes precedence over the pending update.
Implementation remains pending.

**Acceptance.** Fail delayed restart after a successful response. The client
learns the installed-but-not-restarted outcome and retains access to the working
hub. Recovery after the initial polling window automatically completes the same
request without a redundant download when the installed artifact still matches.
Only confirmation of the requested running version completes the update. An
artifact replaced by another update is reconciled before execution; cancellation
or a newer request prevents stale restart attempts.

### H11 Remote bootstrap diagnostics

**Current behavior.** First attach without an existing log path/supervisor can
start a detached hub with output redirected to `/dev/null`. A startup failure
then loses the original explanation before the health check reports failure.

**Evidence.** [bootstrapHub](../../cmd/evener-hub/internal/sshconn/manager.go#L2182)
passes an empty log path to
[relaunchCommand](../../cmd/evener-hub/internal/sshconn/version.go#L1862).
Manual host access or a separate supervisor/log setup is the remaining diagnosis
path; an existing configured log path does not have this issue.

**Decision.** Retain bounded startup diagnostics with automatic cleanup or
rotation. When remote startup fails, automatically retrieve the relevant output
and associate it with that connection attempt. Make the actual cause available
to Evener's recovery logic and the agent so they can investigate and pursue
repair without requiring the user to find logs manually. Any user-facing
explanation describes the specific unresolved problem. Avoid recording
credential material. Implementation remains pending.

**Acceptance.** Fail remote startup with a malformed configuration. Connect
automatically retrieves the startup explanation for the failed attempt and makes
it available for recovery, rather than reporting only a readiness failure or
requiring manual log collection. Retained output and its diagnostic location are
accessible, bounded and free of credential material. Cleanup or rotation keeps
repeated attempts from accumulating unbounded logs.

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

**Decision.** Keep unused offline hosts quietly listed in Hosts, with their
availability discoverable there. Host-offline notices require affected work or a
pending Connect request; being registered and offline alone does not warrant an
attention row or a general warning. Show relevant connection and recovery status
alongside the affected work, continue automatic recovery, and request input only
when the user needs to supply information or make a decision. Implementation
remains pending.

**Acceptance.** A registered unused host causes no attention row or offline
banner. Its availability remains visible in Hosts. Losing a host needed by work,
or awaiting an explicit Connect request, produces relevant recovery status that
clears on recovery or cancellation. Determine affected work independently of
which session pages the client has loaded, so an incomplete local view cannot
suppress a relevant notice. Routine recovery requires no user action.

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

**Decision.** Retain failure evidence and prompt diagnosis and reassessment when
equivalent calls repeatedly fail. Pace retries while keeping the still-needed
operation recoverable. A relevant repair permits another attempt with identical
inputs; temporary external failures permit later attempts after backoff. Old
failure history must not require the agent to invent different arguments or ask
the user to unlock an otherwise authorized operation. Suspected loops follow
the [goal progress decision](#r06-read-only-goal-progress): allow an opportunity
to recover and establish a concrete unresolved impediment before declaring work
blocked. Preserve explicit resource limits and cancellation. Implementation
remains pending.

**Acceptance.** Fail the same operation twice, heal its dependency, and request
the still-needed operation with identical inputs. The tool executes and succeeds
without changed arguments or user intervention. Continued temporary failure
paces further attempts rather than creating a tight loop or permanently refusing
the operation; a later attempt can discover recovery. Unrelated successful work
alone is not proof that the failed dependency healed. Explicit limits and
cancellation remain effective.

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

**Decision.** Automatically retry discovery for enabled MCP servers that were
unavailable at session startup, using backoff. When a server recovers, register
its discovered tools and make the agent aware of the recovered capabilities in
the existing chat. Work waiting on those tools can continue without a new chat,
restart or manual reload. Preserve the selected server identity and keep healthy
tools usable throughout recovery. Disabling or removing the server ends its
recovery attempts. Implementation remains pending.

**Discuss — registration design.** Define how tool names and schemas become
visible to the running session while preserving existing tools and in-flight
operations. The agent-visible tool list must match the callable registrations.

**Acceptance.** Start with one MCP server down, then restore it. Automatic
discovery makes its tools visible and callable in the same session, and the
agent can continue work waiting on them. Existing tools remain available
throughout. A prolonged outage paces discovery without abandoning recovery;
disabling or removing the server stops retries and prevents late registration.

### T04 Read-only directory symlinks

**Deferred.** Restricted-mode and permission refinements are outside the active
work queue. Everyday use is predominantly unsandboxed, so prioritize recovery and
usability in that mode. Preserve the agreed direction below for resumed work.

**Current behavior.** An automatically derived read-only delegate refuses
directory symlink traversal even when the resolved target is inside its allowed
tree. Browsing can map that refusal to not-exist and omit usable content. The
agent may fail to discover ordinary linked project data.

**Evidence.** [Derived delegate scope](../../agent/sandbox_delegate.go#L230),
[secure path traversal](../../agent/execenv/securepath_fdops_unix.go#L107), and
[browse error translation](../../agent/execenv/securepath_browse_fdops_unix.go#L80).
This is separate from the existing useful fallback when an automatic derived
scope lacks an OS sandbox backend.

**Decision.** Read-only agents can follow directory symlinks when the resolved
destination is within their existing allowed read scope. Judge the destination
against those permissions and preserve the read-only constraint. Handle loops
and concurrent link changes without redirecting reads into prohibited content.
When traversal cannot proceed, report the actual access limitation rather than
presenting omitted content as nonexistent. The additional filesystem handling
needed to support ordinary permitted links is an accepted cost. Implementation
remains pending.

**Acceptance.** Read and browse permitted content through a directory symlink,
including an in-project alias to another allowed directory. Retain the allowed
read scope and read-only constraint under loops and concurrent retargeting to a
prohibited destination. A refused or incomplete traversal accurately identifies
the limitation and does not imply that the unexamined content is absent.

### T05 Approval scope and lifetime

**Deferred.** Approval-system work is outside the active work queue. Everyday use
is predominantly unsandboxed, so retain the decisions below without scheduling
their implementation or further approval-design discussion.

**Current behavior.** Sandbox escalation grants one invocation and only supports
read_file, write_file and edit_file on a root session with an active subscriber.
Batch patch, browsing and shell operations have no equivalent path here;
delegates cannot escalate to the parent through it. Repeated approved file work
can require repeated decisions. A batch patch applies operations sequentially,
so a permission error on a later operation can follow successful earlier edits.

**Evidence.** [Invocation grant](../../agent/session_escalation.go#L58),
[eligibility/tool gates](../../agent/session_escalation.go#L103), and
[rerun](../../agent/session_escalation.go#L206) define the limit.
The [ApplyPatch loop](../../agent/internal/tool/apply_patch.go#L18) applies each
operation before checking the next and returns on the first error.
This is a scoped mechanism, not a claim that every Evener permission flow has
these restrictions.

**Decision — scope and lifetime.** When approval is needed, default to a
revocable grant for a named file or directory and an explicit access level,
lasting for that chat. A read grant supports the required investigation; a
read-and-edit grant supports the ordinary edit-and-verify sequence. Retain the
grant across ordinary reconnects and resumes of the same chat. The approval UI
states the resource, access level and duration before approval. Keep a single-use
option, and preserve the original meaning of existing single-use decisions.
Later turns reusing the named access until revocation is an accepted tradeoff.
Implementation remains pending.

**Decision — delegation.** Route a delegate's unmet access request through its
owning chat. Reuse an existing grant when its resource, access level and the
delegate's assigned role permit the operation. Otherwise, present the necessary
approval in the owning chat and automatically continue the delegate when access
is granted. Preserve the child's assignment: a parent's edit grant does not
implicitly authorize a read-only child to edit. Implementation remains pending.

**Decision — unattended managed chats.** An absent client does not reject an
access request or grant new permission. Reuse applicable existing access;
otherwise, retain the request as waiting for access across disconnects and
ordinary chat resumes, and present it when the user returns. Continue independent
work within its existing access and resume the affected work automatically after
approval. Honor cancellation and newer instructions. Waiting for the user when
new permission is genuinely needed is an accepted tradeoff. Implementation
remains pending.

**Decision — file-tool coverage.** Apply a named file or directory grant
consistently across file tools, including listing, search and batch patches. A
read grant supports browsing and searching; a read-and-edit grant supports the
corresponding mutations within the named resource. Identify a batch patch's
required access before writes and request missing permissions together, then
proceed automatically after approval. If an operation has already partially
applied, inspect and reconcile its effects before retrying completed work.
Implementation remains pending.

**Decision — shell tools.** Apply the same named resource and access level to
subsequent shell commands and their child processes. Request newly needed access
through the chat. After approval, inspect the command's output and resulting
state, then continue or repair the operation without blindly repeating completed
effects. Arbitrary commands may discover their needed paths while running, so
file-patch permission preflight does not establish safe whole-command replay.
Implementation is deferred with the rest of this case.

**Discuss when resumed — one-shot commands.** Decide how a one-shot command
without a channel for receiving approval reports an unmet access need. The
managed-chat decision does not require such a command to wait indefinitely.

**Acceptance.** A grant with clearly presented scope permits the intended
read/edit/read sequence without repeated prompts, including after reconnect or
resume of the same chat. Revocation ends subsequent use. Access outside the
named resource or beyond the approved operation is not implicitly granted. A
single-use approval still applies only to its original invocation. A delegate
reuses applicable access without another prompt, routes a genuinely new request
to its owning chat and resumes automatically after approval. A read-only child
retains that constraint even when its parent can edit. An access need first
encountered without an attached client remains pending, is visible on return and
survives an ordinary chat resume. Independent permitted work can continue;
approval resumes the affected work without a new user instruction. Cancellation
or superseding instructions prevent obsolete work from restarting. Listing,
search and patches honor the same resource and access level. A batch with missing
permission requests the necessary access together before making changes; recovery
does not blindly replay its already-completed edits. Shell commands and their
children receive the stated filesystem access; recovery reconciles partial
effects before resuming or retrying. One-shot command acceptance criteria depend
on the remaining product decision.

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

**Decision.** Treat each Send as one retained intent: preserve its exact text,
attachments, target identity and mutation ID independently of the current
composer before transmission. An interrupted response leaves the submission
pending while the client automatically establishes the authoritative outcome.
An accepted request settles without another turn; a proven unaccepted, still
valid request retries with its original identity and payload. An unknown outcome
keeps paced reconciliation active instead of becoming a new submission. A
definitive rejection retains the complete composition for correction. Explicit
Stop or cancellation prevents further automatic dispatch, and any newer draft
remains independent. Reuse the existing mutation receipt and deduplication
contracts; one user send must not become two accepted turns because a response
was lost. Implementation remains pending.

**Acceptance.** Lose a send response after acceptance, reconnect, and obtain one
turn with correct composer state. Also recover a send interrupted before
acceptance, retain both the original composition and a newer draft, and stop
automatic dispatch when canceled. No manual comparison or new mutation identity
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

**Decision.** Preserve failed submissions independently of the currently edited
draft, including their text, image order, image references and ownership of the
original media. Keep each composition recoverable until accepted, safely
restored or explicitly discarded. Restoring an older submission must preserve
any newer draft; retaining the newer draft must not delete the older
submission's temporary image files. Release media only when its recovery owner
has completed or another owner has safely retained it. Implementation remains
pending.

**Acceptance.** Send A with an image, type draft B before A fails, and recover A.
Both compositions and the exact original image bytes remain available through
the chosen recovery flow.

### D01 Diagnostic repair authority

**Deferred.** Diagnostic repair authority is outside the active work queue.
Keep the bundled repair policy unchanged and retain this evidence for a later
discussion. Do not schedule changes to the doctor's authority or validation
workflow unless the user resumes this case.

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

**When revisited.** Keep diagnosis inspectable while allowing a clear handoff to a
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
by that evidence. Agreed product outcomes remain open gaps until their acceptance
scenarios are exercised for an implemented change. Unresolved proposals and
deferred cases retain their separate decision status.

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
