# Attention navigation

Needs you is the triage tier for sessions a person must act on, and Next
needs you is how the web moves a reader through it. This page owns the web's
attention surfaces: the floating control, its controller and history
retention, and the composer controls that guard acting on a session (Stop,
and Retry after a failed turn). Which sessions belong in the tier is a
membership rule shared with [session activity](session-activity.md); this
page owns what the client does with that membership.

## Membership

Only an actionable condition puts a session in Needs you: a pending question,
a pending approval, a warning, or a restart. A turn that merely ended on
`needs_response` is finished, not blocked on a person, so it rests Idle and
never enters the tier. A failed session stays in the tier too, under its own
error level, ordered first. The hub owns the rule once:
[`hubapi.NeedsAction`](../../hubapi/attention.go) decides the actionable
conditions, and the hub's
[`attentionLevel`](../../cmd/evener-hub/internal/hubcore/attention.go) adds
the error level, so the needs-you tier and the summary derive from one shared
answer, not two. The shared client states the same rule for the browser and
the phone in
[`attentionNeedsYou`](../../appwire-client/typescript/sessionAttention.ts).
See [navigation session status](session-activity.md#navigation-session-status)
for the full attention-class precedence order.

A delegate row never drives a waiting-for-human state. Only a top-level
session carries Needs-you membership: `tierEligible`
([attention.go](../../cmd/evener-hub/internal/hubcore/attention.go)) excludes
any session its persisted meta marks a subagent, any session a live entry
reports as its running child, and any fork-superseded parent nested under its
active continuation. A subagent waiting on its parent is internal bookkeeping;
surfacing it as human attention would create false alarms and duplicate
notifications. Client delegate rows follow the same rule: the wire's delegate
phase vocabulary is `idle`, `running`, `settling`, `stopping` and `closed` —
it has no human-attention value — and a delegate whose delivered input is not
yet consumed reads as running work, not attention
([`delegateDotState`](../../appwire-client/typescript/delegateDetails.ts)).

## The floating control

The Next-needs-you control floats above the composer at every web width —
phone, tablet and desktop — in the measured overlay stack
([`FlowOverlay`](../../cmd/evener-hub/frontend/src/panes/session/transcript/flow/FlowOverlay.tsx)),
so it never consumes transcript space or disturbs the composer's own controls.
A dormant transcript docks it in the pane's attention dock instead. The
control renders per focused conversation: the focused session pane, or a
cascade's active leaf.

The control stays visible while the composer has focus. Only three things
suppress it: an in-progress IME composition in the focused conversation, a
pending question or approval there, and an open blocking modal or the command
palette. Editor focus never hides it — a session open autofocuses its
composer, so a focus-based rule would take the control away before the reader
could ever use it — and a saved draft never hides it either.
[`ConversationBody`](../../cmd/evener-hub/frontend/src/panes/session/ConversationBody.tsx)
owns that gate: its `hidden` input is the composer's IME-composition signal
(`onEditingChange`, reported by
[`Composer`](../../cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx)),
and the shared `blocked` snapshot from the controller
([`nextNeedsYouVisible`](../../cmd/evener-hub/frontend/src/panes/session/NextNeedsYou.tsx))
covers the palette and blocking modals. There is one shared blocked snapshot,
not one observer per consumer; the palette reads the same controller through
its own subscription and defers its needs-you actions until the snapshot
unblocks.

A long press on the Next button opens the list of other needs-you sessions;
the button beside it — a More affordance (an ellipsis icon labelled "More
needs-you sessions"), not a down arrow — opens the same list with a click.
Rows that cannot act yet (a page still refreshing or failed) render disabled
with their state as the row's meta, and the list offers a Load more control
bounded by the queue's remaining pages.

## Controller ownership

One shared needs-you controller owns the queue for the whole shell:
[`createNeedsYouController`](../../cmd/evener-hub/frontend/src/shell/needsYouActions.ts)
builds the snapshot from the shared navigation store's `needs_you` section,
and every activation surface — the floating control, the Mod+J chord, the
palette's next-needs-you command and its needs-you result rows — enters this
one controller, so each produces the same paging, URL and placement from the
same state. The controller resolves the next session from the focused reader:
after the focused one in server order, wrapping to the first, except while
more of the queue is unloaded — the last loaded row has no known successor
until the next page arrives, so a wrap there would skip unread rows
([`resolveNextRef`](../../cmd/evener-hub/frontend/src/shell/needsYouActions.ts)
and [`nextNeedsYouRef`](../../cmd/evener-hub/frontend/src/shell/rail/needsYouCycle.ts)).

The provider builds the controller during render so the shell's content
commits on the first pass, and starts observation on the first subscriber's
effect, never in an effect that gates children
([`NeedsYouProvider`](../../cmd/evener-hub/frontend/src/shell/NeedsYouProvider.tsx)).
Building is pure: a controller whose render React discards under StrictMode
registers nothing, and the effect replaces a disposed controller rather than
handing consumers a released owner. `dispose()` releases every subscription,
the popstate listener and the modal observer exactly once. Additional
consumers share the one controller and add no store subscriptions of their
own.

A deferred navigation (a queue page still loading, a modal closing) cancels
when the world moves: a popstate, a route change, a navigation
client-generation change, the focused reader changing identity (including a
Clear rebinding or a cascade rebind), a palette state flip, or a modal
transition. The visible
control's own unmount and the control's visible-to-hidden flip cancel the
controller's pending intent the same way, so a late reply cannot act for a
control the reader can no longer see.

## Hops and retention

Activating a session from the needs-you queue — the floating control, the
chord or the palette — is one hop through
[`openAttentionSession`](../../cmd/evener-hub/frontend/src/shell/attentionHistory.ts).
A hop retains the exact source reader invisibly under workspace ownership
rather than disposing it or exposing another pane: the source pane moves to
the `parked` slot, keeps its pane lifetime, its transcript read view, its
composer draft and its staged work, and Back restores that exact reader —
same lifetime, same reading position — without adding visible panes. Forward
returns to the destination the same way. Only attention substitution parks a
source; ordinary pane placement adopts no hidden retention. Closing the
attention target retires its history entry instead of reopening the closed
conversation over the parked source, and closing the source retires the entry
that owns it. An entry survives a mobile remount: restoration runs before the
shell publishes any stale focused URL. A drill inside a cascade an entry owns
refreshes that entry's placement rather than invalidating it.

Attention navigation preserves structured work, not new caret state. A hop
preserves draft contents, selected skills and commands, staged images, pending
attachment processing and transcript reading position — the retained reader's
own state. It adds no new caret or selected-text-range retention across
editor remounts: selected skill and command metadata is one thing, a text
selection range is another, and attention navigation owns no restoration
promise for the latter. Separate views of one conversation within a tab (an
ordinary pane and a cascade column embedding it) do not synchronize their
drafts live; each reader keeps its own pane-lifetime composer source, and
navigation preserves each through the hop rather than introducing an editor
collaboration protocol between them.

## Composer Stop

While a stoppable turn runs and the draft is empty, Stop occupies Send's slot
in the composer, at every web width. A sendable draft restores Send, with
Stop still reachable through the session menu. "Sendable" is the composer's
full content rule — text, attachments, selected skills and commands all count
([`hasContent`](../../cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx)),
so an image-only or selection-only draft keeps Send in the slot. The phone
states the same rule its own way in
[`composerShowsStop`](../../mobile-native/src/session/composerSlot.ts), where
the screen owns the whole-draft test.

Only a Send-to-Stop swap needs protection, and the web gives it a brief
activation delay: when the slot flips from Send to Stop in one session, the
new Stop button stays inert for 500 ms
([`useInlineStopReady`](../../cmd/evener-hub/frontend/src/panes/session/composer/useInlineStopReady.ts)),
so an accidental double-tap that began on Send cannot stop the turn. The
delay is that swap only: a composer that opens already showing Stop acts at
once, leaving Stop does not arm a later delay, and the session menu's
deliberate Stop action has no delay. Distinct element keys replace the
button across the swap, so a press begun on one cannot finish on the other.

## Failed-turn Retry

A turn that fails offers **Retry** from its failure end-cap
([`TurnFailureEndCap`](../../cmd/evener-hub/frontend/src/panes/session/transcript/TurnFailureEndCap.tsx));
a reconnect-class failure (a daemon or session that went away) labels it
**Reconnect & retry**. Retry is a continuation, not a replay: it submits the
shared continuation text
([`TURN_CONTINUATION_TEXT`](../../appwire-client/typescript/turnContinuation.ts))
as a new message and leaves the previous prompt where it is, in both the
browser and the phone. It does not reconstruct or resend the earlier user
input, and it is not the delivery retry a queued or unaccepted message has —
that Retry belongs to [message recovery](message-recovery.md).

One shared decision answers every Retry question, and both clients run it:
[`retryEligibility`](../../appwire-client/typescript/retryEligibility.ts).
The action appears only on the latest canonical turn; an older failed turn
followed by a newer one offers nothing. A running turn that is not the failed
one, an active session with no running identity, and a history view whose
window is invalidated are all vetoes
([`isLatestRetryTurn`](../../appwire-client/typescript/turnContinuation.ts)).
At activation the decision is re-read synchronously against live state, so a
button captured before the model advanced submits nothing. Not-latest and
not-failed hide the action; the pending answers keep it visible but disabled.
Service availability is a client-specific gate, not a shared one: the native
client derives its Retry availability from whether the service can send
(`serviceReady: canSend` in
[`errorAction.ts`](../../mobile-native/src/session/errorAction.ts)), so a
session with no service offers no Retry, while the browser reports the
service as ready and lets submission surface any connection failure itself.

At most one pending continuation exists per presser: the shared submission
guard (`beginSharedSubmission` in the browser's
[`threadsStore.continueTurn`](../../cmd/evener-hub/frontend/src/stores/threads.ts))
refuses a second Retry while the first is awaiting acceptance or definitive
refusal, including through lost-reply reconciliation — a missing reply is not
permission to send again, because any unresolved submission of this client's
blocks a second Retry from acting on a stale failure
([`ownPendingTurnInput`](../../appwire-client/typescript/state/mutation/pendingEntries.ts)).

That protection is per client, not global: the browser keys its mutation
identity to the tab (sessionStorage; see
[`mutationClientIdentity`](../../cmd/evener-hub/frontend/src/stores/mutationClientIdentity.ts)),
so the guard spans a tab's mounted views and its reloads, while a separate
tab or device holds its own guard. Simultaneous presses across tabs and
devices are not deduplicated server-wide; expanding that boundary would be a
separate server-side contract.

## Opening position

A session opens at the live end, with one exception: a finished reply the
reader has not seen opens at that reply's start, so they read it downward
instead of scrolling back from its end to find where it began. Both clients
make that exception, each with its own memory of what was seen.

The web remembers, per session and per browser, the latest finished turn at
the moment the reader left the pane
([`useSeenDivider`](../../cmd/evener-hub/frontend/src/panes/session/transcript/flow/useSeenDivider.ts));
a turn still running then has not been read to its end, so the reply it goes
on to finish is unseen. The first turn after that mark carries the "New since
your last visit" divider, placed after the messages that opened the turn and
before its reply. When that turn has finished, the pane opens there: at the
top of the turn when the reader's message is short enough to keep the divider
in the upper part of the viewport, and at the divider itself when it is not,
so a long pasted prompt never pushes the reply out of view. The jump pill
offers the live end from there. A reply short enough to share the viewport
with the end is simply the live end.

The live end keeps the opening in every other case
([`unseenOpeningTurnId`](../../cmd/evener-hub/frontend/src/panes/session/transcript/flow/useSeenDivider.ts)):
nothing unseen, an unseen turn that is still running (the reader follows it
live, and the divider still marks where it began), a pending question or
approval (the live end is where the dock is), and a mark that is not among the
loaded turns. A reader with a retained reading position up the transcript - a
hidden tab, a parked attention hop - returns to that position, not to the
unseen reply. The landing is the opening's alone: a transcript that reloads
under an open pane does not send the reader back, and the reader's own scroll
cancels a landing still in flight
([`useTranscriptScroll`](../../cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts)).
There is no setting for this on the web: the live end is one click on the
pill away, and a preference would only choose between two places that are
both already reachable.

The mark is local to the browser, so it is independent of the hub's
cross-device seen state: opening still marks the session seen through the
rules in [session activity](session-activity.md), and a reply already read on
another device still opens at its start here.

The phone's Display page owns an open-at-unread-replies preference
([`openAtUnreadReplies`](../../mobile-native/src/display/displayPreferences.ts)),
on by default: with it, a reply that finished since the reader last reached
the end opens at that reply's start, past the reader's own message, its time
marker and its attachments; a pending question or approval still wins.
