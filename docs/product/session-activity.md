# Session activity

Session activity describes the delegates, shell jobs and watches owned by a
conversation. The primary navigation rail reduces each navigation summary to a
one-line status and title; its title HoverCard exposes the summary's project,
host, branch, running-job, subagent, watch, pin-section, tier and age context. This remains a
navigation-domain read. Hovering the title or focusing its tree row reveals that
context; on a hoverless device, a long press on the title reveals it, and a tap
activates the session. Reading a session's activity is a separate operation, so
browsing navigation does not load every session's work tree.

Transcript delegate cards and status indicators use the same
owner-qualified delegate entity projection as Activity. Retained transcripts do
not require a live diagnostics roster to display authoritative delegate state.
A receipt identifies the owned delegate and, when present, its child session; it
does not establish current status. Missing or ambiguous entity identity remains
unavailable. Snapshots of the same proven owner/delegate/child are ordered by nonzero run
generation, then terminal settlement within that generation, then comparable
projection revisions. A known generation outranks an unknown generation; two
unknown generations retain revision ordering without inferred settlement. Each
selected entity remains one whole snapshot, so resumed status is not combined
with a previous generation's report. These labels reuse the
transcript's existing activity binding without adding reads or subscriptions.

## APIs and ownership

Daemon and hub AppWire expose four typed reads:

| Method | Responsibility |
| --- | --- |
| `evener/thread/activity/read` | Session context and independent counts for delegates, jobs and watches |
| `evener/thread/delegates/list` | Compact stable delegates, including children without a live runtime |
| `evener/thread/jobs/list` | Shell-job metadata; output remains behind the existing output API |
| `evener/thread/watches/list` | Receiver-owned watches and bounded retained watch history |

Every request names an explicit public session `ref`. The default `session`
scope selects resources logically owned by that session. `subtree` includes
resources owned by its delegate descendants. Neither scope includes fork
originals or unrelated sessions. A watch belongs in its receiver's collection
even when another session's manager observes the source. Watch `ownerRef` and
`receiverRef` identify that logical recipient; required `sourceRef` identifies
the physical source session whose manager and journal own the watch and scope
its resolved job target. Detail rows show “Notifies” with the known recipient
name, an already loaded compact delegate name, or its stable ref. Status overlays
retain compact names only for the same owned delegate; they do not transfer
report or run state between generations. Releasing a child transcript does not
remove a recipient identity still present in the parent’s loaded activity.
Output conditions use a job description only from an
already loaded entity with that exact source ref and job ID; missing metadata
keeps the raw target. Naming never adds collection demand or network reads.

The [typed contracts](../../appwire/session_activity.go) and
[generated API catalog](../appwire-protocol.md) define the fields. The
[domain reader](../../agent/session_activity.go) resolves ownership using the
existing delegate controller and journals. The
[daemon handlers](../../server/appwire_session_activity.go),
[hub handlers](../../cmd/evener-hub/app_session_activity.go) and
[source adapters](../../cmd/evener-hub/internal/appsource/session_activity.go)
carry that contract across local and remote boundaries. A remote reference
cannot borrow a coincidentally named local session when its source is unavailable.

These are read APIs. They do not resume a session, start a provider, repair a
journal or change lifecycle state. Shell managers, delegate controllers and watch
managers remain the mutation authorities described by [job control](../job-control.md).
Derived activity indexes are disposable; losing one does not lose the work.

## Context, counts and incomplete evidence

Each response identifies the resolved session, controller root, parent and
ordered ancestors. `ancestryKnown` distinguishes proven ancestry from a retained
index that is still being reconstructed. An empty ancestor list proves a root
only when that flag is true. Retained children remain addressable without a live
child runtime.

A stable workspace routing reference can resolve to a different session after
a clear. Keep the requested ref as the view and subscription key. Use returned
session identities and authoritative resource refs for reconciliation and
actions. Do not extract IDs by splitting opaque refs or replace a workspace
subscription with the currently resolved session ID.

Summary counts describe the requested scope independently of downloaded rows.
`known: false` means the counts are not yet established; it does not mean zero.
Summary reads use available controller state or established indexes. A source
whose complete fold has already been established can catch up appended journal
records within the shared read budget, including a first journal appearing after
an established empty source. Current sources need no repeated probe; invalidated
unchanged sources revalidate their file evidence without replaying records.
Cold sources are never created or scanned merely for a badge. Source replacement
or shrinkage retires establishment rather than rebuilding replacement history.
Establishing retained child ancestry may advance the root delegate journal in
bounded steps. Optional summary `refreshPending` identifies bounded recovery of
established source evidence; its absence means no such demand. Cold unknown
counts and retained registrations with unproved armed state do not set it.
Footer and sidebar badges show a dash for unknown counts and name that state
explicitly for assistive technology. Unknown evidence does not imply that a read
is pending; collection views show their own loading progress.

List arrays are always present. An empty array establishes emptiness only when
the page is complete and has no issues. An incomplete page can contain no rows
while reconstruction makes progress. Issues identify incomplete or unavailable
branches without pretending they contain no work. When a job or watch read spans
multiple journals, an unavailable source does not hide healthy siblings. The
current walk excludes that source and carries its issue; a fresh root read can
include it after recovery. Counts remain unknown while a required source is
unavailable. A read with only one source preserves its typed failure.
Summary `issues` identify unavailable physical sources encountered in the current bounded pass using the same ref/code contract as collection issues; they are not a complete source census. Failed source attempts consume the shared work allowance, and issue admission respects the response byte budget. Unvisited established sources retain `refreshPending`; subsequent summary reads continue round-robin after the last attempted owner. A partial summary preserves counts whose requested owners remain authoritative; receiver-watch counts require all contributing physical sources. The shared summary observer retries unavailable issues with its existing bounded exponential backoff, without acquiring collection demand. Successful partial RPCs do not reset that backoff. These coarse multi-source issues include corrupt journals, matching collection semantics; while observed they receive bounded retries. A sole-source corrupt journal retains its typed nonautomatic failure. Release, disposal and disconnection cancel the shared recovery timers. Bounded folding uses `refreshPending` separately, and checks the final source revisions so accepted invalidations during a fold retain summary recovery demand.


Watch state is `armed`, `ended` or `unknown`. A retained registration alone does
not prove that its runtime still has an armed watch. Recorded delivery counts
and end reasons remain useful, and retained watch history has a bounded lifetime.

## Delegate reports and run identity

Delegate rows carry an optional compact `name` from the immutable caller-supplied
name in the durable delegate descriptor. The domain read caps it at 200 Unicode
code points, including an ellipsis when truncated. Browser and native activity
rows prefer this label; genuinely unnamed records keep each view's existing prompt or ID
fallback. Names are display data: owner refs and delegate IDs still identify and
address delegates. Reading a name needs no child transcript or navigation lookup.

`evener/thread/delegates/list` carries `reportPreview` from the durable reported
completion packet of the current settled run. It preserves a useful prefix of
up to 4,096 Unicode code points, including the ellipsis when truncated;
`reportPreviewTruncated` identifies that case. Missing, non-string or empty
reports have no preview. Terminal errors retain their separate reason and error
fields. Opening the child transcript remains the path to the full conversation.

Both the delegate list and transcript roster carry the authoritative
`runGeneration`. The phone joins a report only when owner, delegate identity and
nonzero generation match. A resumed or settling run cannot borrow the previous
run's report, even if their timestamps coincide. Initial live reads, retained
reads and delegate notifications preserve the same counter. Candidate capture
holds generation and immutable packet evidence together; decoding report text
happens outside the controller's mutation lock. Each captured report prefix is
an owned copy bounded independently of the durable packet. A capture batch stops
at 256 KiB of cumulative report-prefix bytes, allowing at most one bounded
prefix overshoot (49,165 bytes). This bound covers report copies, rather than
all candidate metadata or total work under the controller lock. Response-byte
admission keeps excluded and unvisited delegates reachable through the opaque
continuation cursor.

The [delegate projection](../../agent/session_activity_delegates.go),
[recorded producer outcomes](../../agent/testdata/subagentwire/outcomes.json)
and [phone outcome adapter](../../mobile-native/src/subagents/subagentModel.ts)
define this boundary. The recordings include real done, failed, stopped and
resumed runs; regenerate them with `make fuzz-goldens` when the producer changes.

## Pagination and recovery

Rows are ordered by creation time and stable identity. Equal logical job or watch
IDs in different source sessions remain distinct throughout pagination. An opaque
cursor fixes the initial membership boundary for a walk; new creations appear after a fresh
root read even when timestamps tie or the clock moves backward. Status fields
reflect the state read for each page, so a job can finish
while its collection is being paged. This is not a transaction across the three
collections.

Cursors bind the resolved session, source epoch, resource and scope. They are
not offsets or paths. A lost derived cache or replaced journal produces a typed
stale-cursor result. Clients restart the affected collection and retain useful
displayed rows until the replacement is ready. Never combine pages from different
epochs or refresh attempts.

Pages default to 50 rows and accept a maximum of 200. Activity responses are
bounded to 256 KiB, including summaries after remote reference qualification.
If an intact ancestry context or a single projected row cannot
fit, the read returns a typed unavailable error instead of truncating identity or
returning a continuation that cannot advance. Remote ref qualification can
increase the encoded size; an oversized page is reread from the same input
cursor with a smaller limit and uses that read's own continuation. Summaries
retain their complete context and return unavailable if it cannot fit.

Cold reads share a budget of 2,000 event or projection work
units and 4 MiB of newly read journal bytes across their sources. A stored batch
is decoded one event at a time. One event and its existing atomic fold count as
one work unit under the journal's record-size limit; cancellation is checked
between events. These are input and work bounds, not a hard CPU or wall-clock
deadline. A failed job-journal scan yields the remaining allowance because no
accepted cursor reports its attempted usage; other sources continue on the next
page. Healthy reads do not incur that extra continuation. See the [domain behavior tests](../../agent/session_activity_test.go)
and the [job](../../agent/internal/jobstore/read_page.go) and
[delegate](../../agent/internal/delegatestore/read_page.go) journal scanners.

Local caller cancellation closes the request's owned daemon connection. Remote
reads use a shared connection: cancellation stops waiting, while an already
dispatched read may finish its bounded work. It does not disconnect other
subscriptions or claim to cancel remote execution.

## Shared client responsibilities

[`SessionActivityStore`](../../appwire-client/typescript/sessionActivityStore.ts)
owns summary and collection demand, request coalescing, cursor recovery,
reconnects and stale-result rejection. Opening a collection observes it; closing
it releases that demand. Paging follows visible demand. Unknown summary counts
alone do not demand collection reads. Useful reconstruction progress refreshes
an observed summary so its counts can become known. A summary with
`refreshPending` uses the same existing paced summary timer until established
sources catch up; closing the last holder or going offline cancels that demand.

A collection refresh preserves every displayed page while a fresh cursor walk
rereads through the last displayed row's stable identity. Only a clean walk
through that boundary, or authoritative completion, replaces membership and
removes absent rows. Explicit paging during recovery extends the displayed
boundary; provisional refresh rows do not. Source issues remain pending across
clean continuation pages until a fresh root walk reconciles them.

Refresh continuations yield for 100 ms between pages and stop when their view
releases demand or the connection goes offline. The usual refresh rereads the
displayed extent plus new rows and the final page's overhang. If the old boundary
row was removed, the existing opaque cursor contract requires walking to the end
to establish its absence. That worst case can read the whole collection; each
page remains bounded and observed demand controls the walk.

Transient failures retain useful collection rows and context and retry after 1, 2, 4, 8, 16 and then 30
seconds, continuing at that cap while observed. Reconnect and explicit refresh
can wake recovery. Proven missing resources, invalid requests and unsupported
methods do not spin. Incomplete ancestry progresses at a paced interval instead
of using failure backoff. A failed summary revokes its old count authority so
stale known totals are not presented as current. Disposal cancels timers and
ignores late results.

Temporary source-access failures and an unfinished journal append carry
`actionUnavailable` with `retryDisposition: "automatic"`. They use that same
retry owner; generic unavailable results do not automatically acquire this
meaning. A failed read preserves the last accepted journal cursor and fingerprint,
so recovery can continue without accepting changed source data as cached history.
Confirmed absence, source replacement and corrupt terminated records retain
their distinct missing, stale or unavailable results. The
[source-incarnation checks](../../agent/session_activity_cursor.go) and
[public recovery tests](../../agent/session_activity_test.go) pin this boundary.

A workspace alias can resolve to a replacement session. Resync fences pending
responses, and a changed resolved session ID retires the former session's summary,
rows and cursors together before publishing replacement evidence. A changed opaque
epoch for the same session does not erase useful rows. The requested alias remains
the routing and subscription key throughout recovery.

The [thread subscription lease](../../appwire-client/typescript/threadSubscription.ts)
shares membership by actual client object and requested ref. A first transcript
read can acquire membership while obtaining its rich response. Membership
acquisition and final release are ordered; reads with established membership
remain independent. Only the last holder unsubscribes. Transcript and activity
owners must both use this seam, because a raw unsubscribe or replacing read can
otherwise remove another view's subscription.

A successful subscribed saved-history read keeps that connection’s future event
membership even when no daemon is live. The hub buffers events before reading
the bounded saved response and releases them after it enters the connection’s
send queue. A separate connection can resume the session without making existing
transcript and activity consumers reload. Failed hydration, unsubscribe and
connection closure withdraw pending membership. Reading saved history alone does
not launch a daemon.

The [presentation adapter](../../appwire-client/typescript/sessionActivityPresentation.ts)
builds rendering models without network calls, retry timers or lifecycle
authority. Loaded descendants whose parents have not arrived remain visible
with incomplete relationship evidence. Rendering keys qualify resource IDs with
their authoritative refs. Actions keep the raw logical ID: job output uses the
returned job owner ref, delegate stopping addresses its controller root, and a
child transcript opens the returned child ref. A rendering key never becomes an
action argument. The [delegate stop handler](../../server/appwire_runtime.go)
checks that root mutation boundary. Optional usage fields stay absent when unknown.
Job output panes keep the logical job ID as their title when descriptive
metadata is unavailable, while independently readable output remains usable.

See the [store tests](../../appwire-client/typescript/sessionActivityStore.test.ts),
[lease tests](../../appwire-client/typescript/threadSubscription.test.ts) and
[presentation tests](../../appwire-client/typescript/sessionActivityPresentation.test.ts)
for these shared ownership contracts.

## Client lifetimes

The [browser binding](../../cmd/evener-hub/frontend/src/stores/sessionActivity.ts)
shares a store by actual client object, requested ref and scope. A committed view
acquires its holder; an abandoned render starts no read. The focused status
surface observes the summary. Agents, Jobs and Watches tabs observe their own
session-scoped collection. A visible page boundary supplies further demand,
while the shared store retains and retries an interrupted continuation.

The composer names its receiving conversation even when a child or output pane
has focus. Job rows and output tabs use the job description when present, and
output details retain the command and exit status. Each output pane publishes
its already loaded title through the [pane chrome store](../../cmd/evener-hub/frontend/src/shell/chromeStore.ts);
tab naming adds no metadata request. Closing Activity from within the sidebar
returns keyboard focus to its opener or the matching footer control.

The [desktop sidebar view store](../../cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.ts)
retains open/category choices and a semantic row anchor per public session ref
and category. The browser disclosure binding opts Activity into retaining explicit
fold choices, including task and watch details. These are bounded, best-effort UI
preferences: 100 recent session views and 2,000 disclosure choices. Loading them
does not rewrite storage or acquire a collection for a closed sidebar. These
preferences never contain activity rows or continuation/retry state. A committed
desktop revisit preserves the session's recency through the existing coalesced
save, so a reload does not make that session an older eviction candidate. Focus
changes while the sidebar is unmounted do not persist inherited view intent.

Without a retained choice, the desktop [Jobs tab](../../cmd/evener-hub/frontend/src/shell/activitybar/JobsTab.tsx)
starts successful completed job history folded, and the shared
[task panel](../../cmd/evener-hub/frontend/src/panes/session/chrome/TasksPanel.tsx)
starts settled done/cancelled task history folded. Running and unsuccessful jobs
remain visible, as do the Current and Remaining task sections. Task details
remain an explicit disclosure choice.

The [Activity viewport](../../cmd/evener-hub/frontend/src/shell/activitybar/ActivityViewport.tsx)
restores the retained row after its collection and disclosures render. A row
that is already loaded can still need trailing page extent to reach its saved
viewport offset; restoration remains pending until that position is reachable
or the collection is authoritative complete. For cold
pages it positions the existing page boundary in view; each new visibility
observation supplies demand through `ActivityPageBoundary`. It starts no fetch
or retry loop. Closing the sidebar pauses positioning and page admission while
it is closed. Reopening during its exit animation can resume the same mounted
viewport's pending intent; a completed exit or scope/category replacement retires
that viewport's pending work. Reader scroll/navigation gestures and deliberate
control activation cancel pending positioning. Focus-only keys
such as Tab, modifier keys and text keys preserve it when the viewport does not
scroll. Scroll anchors update in memory immediately; the existing view store
coalesces their storage writes after a gesture. Closing, changing category or
session, and page departure flush the latest position. Other explicit view
choices remain immediately durable; unavailable storage never blocks use.
Partial results keep a missing anchor; an authoritative
complete collection can prove it absent. A row inside a closed fold does not
authorize opening that fold. A new child scope keeps an ongoing sidebar
inspection open on its current category with fresh child-specific view choices;
returning to a visited session restores that session's choices.

The [visible transcript](../../cmd/evener-hub/frontend/src/panes/session/transcript/useEntityView.ts)
observes session-scoped jobs and delegates for inline entity links and controls.
It shares those reads with other holders of the same binding. If neither
collection is already observed, mounting the transcript starts up to two bounded
initial collection reads. Closing it releases that demand. Transcript watch
references use the transcript's own watch evidence. The
[recursive activity panel](../../cmd/evener-hub/frontend/src/panes/session/chrome/ActivityPanel.tsx)
observes subtree collections while its body is open. These view lifetimes do not
cause an activity read for every session in the navigation rail.

The narrow composer exposes Activity beside its receiving conversation. Its
optional active count sums
the session's authoritative job and delegate active counts only when both are
known; watches and completed work are not part of that number. The mobile sheet
starts with compact job, delegate and watch details, each one disclosure away.
Expanded watch details reveal the full user note as wrapped text, regardless of
how much fits in the compact collapsed row.
Failed entries remain visible outside the inactive fold, including parent rows
needed to expose failed descendants. The fold count covers only the other
inactive entries grouped beneath it.
The [panel view store](../../cmd/evener-hub/frontend/src/stores/activityPanel.ts)
retains sheet visibility, disclosure choices and each collection's last loaded
row identity by session ref for the lifetime of its retained workspace panes.
Opening a child transcript releases the hidden subtree read demand. Back acquires
fresh demand through the shared binding and restores the inspected extent using
the existing page boundary, yielding 100 ms between pages. Delegates, jobs and
watches restore independently until their saved identity arrives or an
authoritative complete read proves it absent. A partial first page cannot replace
an outstanding boundary. The view stores only identities, not resource rows,
cursors or retry state; the shared store continues to own failed-page recovery.
Explicitly closing the sheet clears extent restoration and keeps the sheet closed
across navigation. A changed resolved session clears the former session's extent
and disclosure choices. Closing the last retaining pane evicts this view state.

The [read-only child transcript](../../cmd/evener-hub/frontend/src/panes/transcript/Transcript.tsx)
shows the same proven scope hierarchy above its content while preserving its
chosen transcript title. Its existing session binding supplies summary context;
direct links and restored panes obtain the same context without requiring a Back
target. The Back target alone is not ancestry evidence, and no parent transcript
is read to obtain a name. Root sessions omit redundant ancestry.

Activity delegate rows use the shared transcript opener, which retains the
enclosing conversation and canonicalizes restored variants of the same child
pane before focusing it. Nested drills keep the child’s parent context so an
unchanged root URL does not steal focus. Ancestor buttons reuse an already open
read-only transcript with its exact pane identity and parent context; an existing
live owner regains focus even when its URL is unchanged. The session rail and
Open session actions request the live session route and composer, including when
a read-only transcript of that session is already open.

The [native binding](../../mobile-native/src/subagents/subagentTree.ts) projects
subtree activity through the same shared store. Summary holders and collection
holders acquire demand separately, and client replacement fences old replies.
Its retained rendering tree does not own a separate network or retry loop.
Client lifetime changes retire pending model replies even when the same client
object later returns; accepted model evidence remains useful during disconnect.
[Stop-request display records](../../mobile-native/src/subagents/stopRequests.ts)
use the authoritative child ref and raw delegate ID, so another source's equal
ID cannot acquire that request's label or suppress its stop control.
[Conversation reads](../../mobile/src/services/conversation.ts) and
[session links](../../mobile-native/src/session/sessionMessage.ts) use the same
additive thread lease as activity, preserving another holder when a screen closes.

The [browser ownership tests](../../cmd/evener-hub/frontend/src/stores/sessionActivitySubscriptions.test.ts),
[visible paging and action tests](../../cmd/evener-hub/frontend/src/shell/activitybar/activityApi.test.tsx),
[native binding tests](../../mobile-native/src/subagents/sessionActivityBinding.test.ts)
and [native lease tests](../../mobile-native/src/session/sessionActivityLeases.test.ts)
exercise these boundaries. Browser geometry and native bundling are separate
qualification gates; these contracts do not establish a device or provider run.

## Navigation boundary

Navigation uses representation version 3 independently of connection protocol
`evener-appwire-v6`. Clients request version 3 and the hub advertises that
representation. There is no version 2 emission or fallback path.

Navigation carries own-session running-job and watch counts plus bounded running
command text for compact rows. It carries no job or watch detail arrays and no
separate `subagents` resource. Session activity reads supply those details.
Attention and the existing compact root subagent tally keep their own meanings;
collection downloads do not establish their authority. See the
[navigation schema](../../hubapi/navigation.go) and
[web routing guide](../evener-hub-web-routing.md).

The normalized navigation graph stays flat. The separate
[archived list](../../cmd/evener-hub/navigation_archived_list.go) retains fork
originals as bounded inline conversation rows. They remain independently
openable and revealable through their continuation's disclosure in the
[browser rail](../../cmd/evener-hub/frontend/src/shell/rail/railNodes.ts).
When depth, node or byte limits trim these rows, `omitted_descendants` reports
the excluded fork originals without counting delegate activity.
Fork originals are separate conversations, outside either activity scope; this
archive path does not restore subagent or job/watch detail to navigation.

When changing this boundary, update the [subsystem map](subsystems.md), public
contracts, domain producers, routing tests and actual client lifetimes together.
The APIs are useful independently of a particular panel or navigation layout.
