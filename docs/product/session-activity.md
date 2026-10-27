# Session activity

Session activity describes the delegates, shell jobs and watches owned by a
conversation. The primary navigation rail reduces each navigation summary to a
one-line status and title; its title HoverCard exposes the summary's project,
host, branch, running-job, subagent, watch, pin-section, tier and age context. This remains a
navigation-domain read. Hovering the title or focusing its tree row reveals that
context; on a hoverless device, a long press on the title reveals it, and a tap
activates the session. The card floats beside the sidebar, aligned with the row,
with viewport-aware above/below fallback in the sessions drawer or a narrow
window. Scroll and resize cancel visible and pending reveals; a fresh gesture
opens it at the new position. See the [placement contract](../web-ui/design-system.md).
Reading a session's activity is a separate operation, so
browsing navigation does not load every session's work tree. A session's seen
mark lives on the hub: a live navigation row carries its `seen_through`, and
the activity read carries when the session's tree last moved (`lastMovedAt`).
Opening a session (on the web, a pane opening or the page becoming visible
with it open) marks it seen through the later of an unseen turn end and motion
after that mark. The web reads that session's activity once per open for this,
and a read whose connection was replaced is read again through the new one.

The phone Board's pulse meter and quiet presentation, and the `lastMovedAt`
behind the web's seen mark, come from a different, hub-level read:
`evener/activity/read` — see the [session pulse meter](pulse-meter.md). It is
not one of the `evener/thread/*` activity reads below and carries no delegate,
job or watch rows.

A working session is stuck after ten minutes without transcript motion, and
never while one of its subagents runs. The web session pane, the phone tray and
the phone Board all read that one rule from `@evener/appwire-client`
(`isStuck`). The web's short quiet hint at twenty seconds is a separate, softer
state that is not called stuck.

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

## Navigation session status

A top-level session's Broken/Failed status comes from its own `errored` state.
Retained failed subagents remain in the web title context, phone activity history
and Agents/Subagents detail views without making a healthy parent look broken.

The existing live compact navigation summary supplies running-subagent work.
The web rail shows its Running spinner; the phone Board classifies the parent
as Working, using its existing pulse meter in Live and static mark elsewhere.
This applies to quiet parents and nonblocking warnings, including mixed running
and failed children. Questions and approvals keep their attention presentation,
and so does an awaiting rest after a turn that ended on `needs_response`, on the
web rail and the phone Board alike, since it waits on a person the same way.
Warnings carrying a question or approval remain attention states. Own failure,
restart-required and existing unavailable-session presentations remain unchanged.
Background attention paused by a permanent provider failure is not live work
while no fresh wake or paced retry is armed: the session keeps its own failure
presentation until a new delegate event, a sent message, a model switch, or
another re-engagement re-arms delivery.

Question resolution returns to working presentation while children run. Once
the last running child settles, each client resumes its normal parent-state and
quiet/completion presentation, even with retained failures. Refresh and reconnect
apply the same rule to newly delivered summaries without clearing failure history
or changing session lifecycle state.

The phone's [alert feed](../../mobile-native/src/alerts/alertEvents.ts) follows
Board bands. A new nonblocking Warning waits while live children run. A warning
that remains alerts when the last child settles; one that clears first never
alerts. Warnings carrying a pending question or approval remain immediately
eligible. A `needs_response` rest alerts like a question, under the same In-app
alerts switch as questions and approvals. First-read and offline rules still apply. Combined banners, Needs you
counts and Next navigation use the same Board attention membership.

The browser's [`effectiveSessionState`](../../cmd/evener-hub/frontend/src/shell/rail/RailRow.tsx)
and the phone's [`boardState`](../../mobile-native/src/board/attention.ts) own
these projections. The browser uses the shared navigation store; the phone keeps
its existing BoardController/NavigationPages delivery and shared decoding.
Both reuse the [shared live tally selector](../../appwire-client/typescript/state/navigation/selectors.ts).
Status does not reconstruct counts from loaded child rows or acquire activity
collection demand. Hub aggregation and domain activity authority stay independent.

The attention class itself comes from one precedence order, `attentionState` in
the [shared client](../../appwire-client/typescript/sessionAttention.ts): offline
or ended, failed, restart needed, a blocking warning, a question, an approval,
awaiting, working, a nonblocking warning, then idle. The phone Board marks and
state line, Board search marks, the browser's Cadence family and the
notification level all read it. A pending approval never masks a failure, a
restart need, an ended or not-loaded session; the hub states the same rule in
`hubapi.AttentionState`, and both run the cases in `testdata/attention`.

## APIs and ownership

Daemon and hub AppWire expose four typed reads:

| Method | Responsibility |
| --- | --- |
| `evener/thread/activity/read` | Session context and independent counts for delegates, jobs and watches |
| `evener/thread/delegates/list` | Compact stable delegates, including children without a live runtime |
| `evener/thread/jobs/list` | Background shell-job metadata; output remains behind the existing output API |
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
Remote adapters qualify structural session refs and preserve opaque job transcript
anchors such as `job:<id>`. A job's qualified `ownerRef` scopes its output actions;
the anchor is not a session routing ref.

Jobs pages and summary job counts share one domain-owned eligibility rule:
a shell job must have durable background evidence in its start journal event.
Explicit background launches and foreground jobs handed back while still running
remain eligible through terminal settlement, forwarding and restart. Retained
inline foreground output, foreground runtime-limit settlement and unclassified
historical jobs are excluded. Detached commands create no job record. Live
overlays can update an admitted row's status or output length, but cannot establish
eligibility. The clients consume the same filtered pages and counts without
reconstructing eligibility from notifications or navigation.

Complete diagnostic job history and direct saved output and job transcript reads
remain available. Activity reads neither change historical journals nor infer
background evidence from transcripts. Filtering precedes page admission; excluded
records still consume bounded scan work and advance the cursor. A long excluded
gap can yield a valid empty continuation without claiming completed empty history.

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

Native Board, project/location and pinned session rows use a native-only
running-delegate chip gate over navigation tallies. Settled delegate failures
have no visual or spoken count there; shared navigation selectors and web rail
presentation retain their existing contract. The native session header's neutral
Subagents chip uses the authoritative activity total without a failure suffix.
Unknown counts with an existing delegate roster keep access and announce
“count unknown”; disconnected headers omit the chip. The native header's Tasks
chip, the Session sheet's Tasks row and the shared activity projection all
count settled tasks (done plus cancelled), the same count the web's Tasks chips
read. A session's own errors,
current questions and approvals retain their attention and priority, including
the existing offline and stale-input rules.

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
fields. The browser's expanded delegate activity detail presents a non-empty
preview as a compact message from the delegate's name, with the name resolving
through the standard delegate entity card and its open-conversation control.
Opening the row loads the child's conversation pane directly.

A delegate that reports and then runs more turns in its finalization drain
settles on its latest report, so `reportPreview` shows the newer one while the
earlier report stays in the delegate's transcript. A drain turn that ends
without a terminal `communicate` leaves the earlier report as the preview.

A delegate's mid-work updates (`communicate(end_turn=false)`) never contribute
to `reportPreview`. Each reaches its owner's transcript as its own
`<delegate-notification kind="update">` row, which the browser renders as a
"Delegate update" card and the phone as "<name> sent an update", with the
delegate's message beneath.

A running delegate's row carries `latestIntent`, the intent line of its newest
in-flight tool call: the child session states the line when it launches the
call, the owner's controller journals it per run generation, and the row
shows it while the run is open. The read and the frame apply the same
200-code-point excerpt treatment as `name`, flattening whitespace and keeping
an ellipsis when truncated. Run settlement clears it; a resumed run starts
with none until its child states one; a restored tree shows the journaled
value until the child's next report. The controller publishes the line on the
delegate update notification at most once every five seconds, so a burst of
tool calls costs at most one frame per window while the standing line is
never dropped.

Clients show the line in place of the status word only while the row is
non-terminal and the line is non-empty; every other row state keeps today's
status text on the web, and the status word stays reachable — the sidebar
row's dot label and title, the activity tree's row description, and the
phone's accessibility label keep reading it. The merge rides the existing bounded
preview gate: a client that has not advertised it keeps the read path alone,
and an absent, over-long or malformed value applies as omitted rather than
dropping the row. Older daemons never send the field, and their rows keep
today's status word everywhere.

A subagent row's leading mark is the session status dot on every surface, with
one state the session vocabulary does not have: quiet. An open run shows the
alive dot while fresh, dims it to quiet ink once its latest activity passes
the shared twenty-second threshold, reads amber for attention and red for
failure (a diamond on the web, so failure never depends on hue alone), and
shows no mark once settled, a settled failure excepted: its red mark is its
outcome. One shared client rule computes the state and the
threshold, so the web sidebar, the web activity tree's delegate rows and the
phone's list read the same dot and flip at the same instant: the sidebar times
one threshold wake per running row, the tree's live tick covers its rows, and
the phone reuses its existing quiet wake. Quiet stays truthful, moving from
the word to the dot: the phone's why line shows the intent, else the running
command, else the subagents it waits on, and otherwise stays empty while the
dot alone speaks, its failed xmark and done checkmark retired beside the band
headers that already carry those states. Shell-job rows keep their own marks
and status words everywhere; the timeline, tray and board keep their own
quiet-word contracts.

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

A thread's transcript roster lists the delegates in its session's subtree: a
root's whole tree, or everything below a subagent. Every row still names the
root as its owner. `evener/delegate/updated` reaches the root's thread and the
thread of each live ancestor subagent, so an open subagent conversation keeps its
rows current. A released ancestor gets no live updates; its next thread read
lists the rows afresh. The [status roster](../../agent/status.go) and the
[update routing](../../agent/delegate_runtime.go) define this boundary.

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

Comic Chat's `useComicActivity` observes `session` scope and the `delegates`
collection only. Its root reader and each explicitly expanded child share this
binding with other consumers. `buildEntityView` chooses the authoritative
generation; the comic projection uses canonical transcript events only to place
the matching owned strip. Missing causal history keeps the strip in a separate
group, and ambiguous ownership never becomes an actionable child link.
Child lifetime components live above virtual rows and release only their demand
on collapse. The explicit full-page route remains available without an inline
composer or approval submission. Activity headings use proven context
`delegateName` fields, not ancestor titles or task descriptions. Transcript
authorship comes separately from shared `ItemModel.authorship`: original authors
survive inherited context, and missing authors stay neutral rather than borrowing
the current child or parent. Neither path acquires a parent activity lease. Human questions
and sandbox approvals remain interactive-root-only; model-facing delegate
attention is not a human request. See [Comic Chat](comic-chat.md#inline-child-readers).

The Comic full-page root uses the shared composer actions, AskDock and sandbox
approval rail. Activity strips do not submit answers, grant approval or start a
daemon. Route changes and late action settlement stay with their captured root
ref; activity status does not transfer that ownership to a newly selected child.

[`SessionActivityStore`](../../appwire-client/typescript/sessionActivityStore.ts)
owns summary and collection demand, request coalescing, cursor recovery,
reconnects and stale-result rejection. Opening a collection observes it; closing
it releases that demand. Paging follows visible demand. Unknown summary counts
alone do not demand collection reads. Useful reconstruction progress refreshes
an observed summary so its counts can become known. A summary with
`refreshPending` uses the same existing paced summary timer until established
sources catch up; closing the last holder or going offline cancels that demand.

Scoped activity consumers refresh the resources named by
`evener/thread/activity/changed`, honoring its logical owner and the selected
session/subtree scope. Delegate field changes are instead applied from the
pushed `evener/delegate/updated` row when the owning source advertises the
bounded report preview and is live (`SessionActivityContext.reportPreview` and
`availability`): the store merges the frame into loaded rows by the delegate's
logical owner. A frame patches a row only where the loaded row set proves its
delegate id names one child; otherwise the store reads the collection, which
bounds those reads to one per id and then one per strictly newer revision,
because a frame that cannot be attributed may still become addressable. It reads
for a delegate it has not loaded, for a delegate id that names more than one
loaded row, and for one no final row set has yet proved unique, since a page the
collection continues past can still serve that id for another child. It reads
none of that while a fresh walk is in flight: that walk settles the frames
it carries at its displayed boundary, so they do not restart it. A frame the
buffer evicts before that is recovered by one read when the walk ends. It also
reads on observe/paging/resync/reconnect, and when an epoch change retires its
push state; a resync and a reconnect both turn the merge off until a refreshed
live context confirms the source again, so a frame racing their re-reads is
recovered by the invalidation read rather than merged and then cleared. A frame
that arrives while a sibling announces the replacement merges
under the announced epoch, and only a row a response served seeds the new epoch's
ordering. A response that first reveals a replacement nobody announced queues one
fresh read, because the clear retires the frames it had merged and that response
alone cannot recount them. An epoch the ordering has left is retired, and so is
an announcement another one replaces, so a
response that still names either is late rather than news: it does not re-retire
the ordering or force a read, and a delegates read settling while such an
announcement stands clears it. A frame the loaded rows cannot be proved to own waits
for the read whose row set is final, which places it on the row the id names,
drops it when
the id turns out to name two, and leaves it waiting while that read does not
serve it. A source that
does not advertise the capability, a retained context
whose released runtime receives invalidations but no frames, or an invalidation
that refreshes no other observed resource (the store observes none, the
notification omits it, its only other demand is an explicit read nobody retries,
or its read has permanently failed) keeps the
invalidation read. A read that was carrying that capability can also fail for
good, and the delegates it stood in for are read then.
Delegate membership is append-only within a source epoch;
jobs and watches are unchanged. Legacy job lifecycle and tree-update
notifications remain available to other consumers but do not trigger these
reads. The domain APIs and scoped invalidations form one contract; resync and
reconnect still refresh observed demand.

A collection refresh preserves every displayed page while a fresh cursor walk
rereads through the last displayed row's stable identity. Only a clean walk
through that boundary, or authoritative completion, replaces membership and
removes absent rows. Explicit paging during recovery extends the displayed
boundary; provisional refresh rows do not. Source issues remain pending across
clean continuation pages until a fresh root walk reconciles them.

When a binding replaces its client object, it can pass its already loaded
`SessionActivitySnapshot` through the store's `retained` constructor option.
Only matching requested refs and scopes retain evidence. Rows, context, issues
and summary survive in memory while the new owner recovers; read flags reset to
pending, and runtime metadata, cursors and in-flight requests never transfer.
The existing fresh walk restores the displayed boundary with new cursors.
Resolved-session replacement still retires the former evidence together.

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
child conversation opens the returned child ref. A rendering key never becomes an
action argument. The [delegate stop handler](../../server/appwire_turn_handlers.go)
checks that root mutation boundary. Optional usage fields stay absent when unknown.
Job output panes keep the logical job ID as their title when descriptive
metadata is unavailable, while independently readable output remains usable.

### Job output pages

`evener/jobs/output` reads through the returned job owner ref and raw job ID.
Omitting `beforeBytes` selects the latest page. A supplied value selects a
backward page ending at that lifetime byte offset; explicit zero stays zero.
The default page limit is 4 KiB, with positive limits capped at 64 KiB.
For storage floor `F`, total `T` and limit `M`, latest selects
`[max(F, T-M), T)` and backward end `B` selects `[max(F, B-M), B)`.
The supplied end must lie in `[F, T]`; negative ends and ends beyond `T`
are invalid. `B == F` succeeds with an empty page. Zero therefore succeeds
empty only while the floor is zero.

Each response contains `offsetBytes`, `bytesReturned`, `totalBytes`,
`retainedStartBytes`, `encoding` and `data`. These describe one coherent raw
snapshot. The storage floor is independent of the displayed page start.
Invalid UTF-8 spans and split scalars travel losslessly as standard base64;
valid UTF-8 pages use `utf8`. The shared `parseJobOutputPage` validates bounds
and counts and supplies raw bytes; `decodeJobOutputText` handles display.
Contiguous pages must be joined as bytes before decoding their shared boundary.

A selector below the current storage floor returns structured
`jobOutputPruned` with `retainedStartBytes` and `totalBytes`. A missing file or
changed output generation remains an ordinary read failure. The owning live or
remote source stays authoritative; only the existing dead-local condition
permits the hub's saved-output fallback.
The returned end equals `totalBytes` at the current EOF; an empty floor page
is EOF only when the floor also equals the total. EOF describes that snapshot,
so a running job can append after an empty read.

### Browser output reading

The [pane-owned reader](../../cmd/evener-hub/frontend/src/panes/transcript/jobLogReader.ts)
retains two contiguous source-byte windows: up to 512 KiB of older reading and
512 KiB of live output, with at most 1 MiB in their union. This is a retained
source-byte budget, not a browser-heap limit. The existing `VirtualList` renders
byte-keyed rows and follows the bottom. Away from the bottom, the pane anchors a
visible complete glyph by its source-byte offset and pixel position within the
pane body. When pages arrive or window trimming repairs a row, restoration uses
the existing virtualizer's row measurement and scroll-offset APIs.

Scrolling loads adjacent pages in either direction. Evicted retained text can
be fetched again. An unloaded interval remains a paging boundary until its
bytes have been read, including pages containing only terminal controls.
Output no longer retained has a separate notice; useful cached text below the
new floor remains readable. UTF-8 and ANSI state join only across contiguous
bytes, with bounded decoder carry across known eviction. A gap starts an
independent decoding range. Long lines render as bounded source-byte fragments.
A page, a trim or a window join decodes only the bytes whose rows it changes;
settled rows carry over, so per-page cost tracks the page rather than the window.

Each open pane permits one output read at a time. A visible running pane reads
the latest output every second while serving visible history demand between
live reads. Hidden or disconnected panes pause reads and retain their windows,
anchor and pending demand. Returning to a readable pane resumes them. Read
failures retry with paced backoff while successful cached output stays visible.
Closing the pane or replacing its owner/job retires the reader and rejects late
replies. This reader shares the existing pane lifetime and connection readiness;
it creates no activity subscription.

Descriptive job metadata is best effort and cannot block independently readable
output. Observing terminal metadata requires a fresh latest read after that
observation, even when an earlier read already saw EOF. Disconnecting during
that drain preserves it for recovery. Finished jobs remain history-pageable.
Refresh requests current output without discarding older reading or its anchor.

Protocol `evener-appwire-v7` requires the same contract in the hub, daemon and
clients. Older running daemons keep their work and require an explicit restart
for current live reads. There is no tail-parser compatibility path. The phone
uses the same portable decoder for its existing latest-output view, retaining
focused two-second rereads and prior successful output through read failures.

See the [store tests](../../appwire-client/typescript/sessionActivityStore.test.ts),
[lease tests](../../appwire-client/typescript/threadSubscription.test.ts) and
[presentation tests](../../appwire-client/typescript/sessionActivityPresentation.test.ts)
for these shared ownership contracts.

## Native activity histories

The phone splits background jobs and subagents into two lists. Agents lists
only subagents, from the top bar's Agents chip or the session menu; Jobs lists
only shell jobs, from the session menu. Each list shows current work outside
one quiet history fold — Done for Agents, Completed for Jobs — which starts
closed under All. The Done filter reveals that list's terminal rows without an
extra disclosure and includes failed outcomes in its count. Each list's
summary counts, strip, chips and header title cover its own kind alone;
counts remain independent of loaded rows, and search narrows loaded labels
without changing those counts. A job still names the subagent that ran it from
the delegates the shared tree has loaded, and the Jobs list advances delegate
pages only to find those names.

Terminal rows keep their true failure, stop and report evidence in ordinary ink.
Running descendants remain visible on the Agents list even when a terminal
parent is folded, with their original navigation and stop identities. Live
questions and approvals keep their attention treatment. A list's active strip
combines its terminal outcomes in its quiet Done segment and disappears when
none of its rows is running.

The native binding retains shared membership across disconnection and client
replacement. Its shared store owns paging, retries and later-page replay; the
screen owns filter, search, independent disclosures and visible page demand.
A successful page that only changes a closed fold's count keeps the observed
end demand, because the native list emits its end callback once per content
length. Each resource's shared loading/error state gates that demand. New visible
rows, changed content geometry or scrolling away require a fresh native edge
observation; an unfocused screen supplies no new demand. Errors recover through
the shared backoff, and disposal rejects delayed pages. Renderer tests exercise
the installed native edge latch, stable qualified row identity and preserved
view intent through recovery.
Physical-device scroll geometry, safe areas, keyboard and VoiceOver behavior
require separate qualification.

## Automatic agent cascade

On desktop, activating a delegate row in Agents opens or reuses the agent
cascade in the secondary pane group. The originating session keeps its ordinary
panel, tab, group and lifetime. A main center source remains
mounted with its composer and reader. A secondary source follows the group's
normal inactive-tab mounting rules while retaining its work.

Further drills reuse that source's inspector and select the actual returned
child ref. Drilling from an ancestor replaces only the path after that owner.
Navigation children remain fork relationships, not delegate ancestry.

Cascade and sidebar breadcrumbs use the same reconciled requested path. When
authoritative delegation proves a saved source alias occupies an ancestor
position, both surfaces pop to that alias without replacing the source binding.
Unknown ancestry stays explicit; cached edges do not establish new ancestors.
The pane has no title and no separate path nav: its tab label carries the
selected conversation, and the compact spines are the clickable breadcrumb.

The origin conversation never renders in the cascade: while a live origin
association exists, its scope is removed from the pane entirely — no readable
column and no spine — and no surface in the pane can pop to it. The origin's
identity is still observed: when the origin's ref resolves to a replacement
session, the cascade retires its stale descendant edges and readers rather
than showing children the replacement did not produce. The pop that follows
lands the selection on the origin itself, which never renders: the pane keeps
the Return path above whatever earlier ancestors remain, and the phone — which
renders the selected leaf alone — keeps only the Return path rather than an
ancestor column under the selected conversation's name. The first
drill shows only the subagent's conversation, and that lone column fills the
pane like an ordinary session pane. Deeper drills keep the selected session
and its immediate parent as readable columns above the removed origin, with
earlier ancestors as live compact spines. Closing the origin pane retires that
association and restores the full rendered path. Each readable scope has an
independent transcript position; collapsed spines retain their reader state
while pausing older-history demand. Pop, reopen and Jump to live use the
existing transcript and history owners. The ordinary source keeps its own pending demand while
inspection is open. A retained source in an existing in-place cascade keeps
that demand even while collapsed. Another same-ref reader cannot adopt or cancel
it. Ordinary navigation can adopt a genuinely disposed predecessor's pending
demand. The inspector's footer and shared sidebar stay scoped to the selected
leaf, including while the reader selects text in a parent. Opening Overview
from an embedded column's own chrome holds that column's scope explicitly —
the sidebar describes that conversation, its checkmarks mark it, and its
breadcrumbs show its own ancestry — until the workspace scope moves, Overview
closes, or another open request names a different scope, and the cascade keeps
focus even when an ordinary pane for that conversation exists. A focus-based
open request (a footer chip, an ordinary pane's Overview) releases the held
scope and leaves the sidebar, its preference saves and its checkmarks with the
focused conversation. Each ordinary pane's footer remains bound to its own
conversation.

Columns and spines expose Agents, Jobs, Watches and Tasks peeks, including empty
Tasks. A column's activity controls render below its transcript content, with
the column's header and controls fixed while the transcript viewport scrolls
internally; the pane body hosting the spines and columns never scrolls as a
unit. About is available in Overview and has no cascade chip.

Ancestor spines reuse the session list's compact indicators: a gray spinner for
active work, an amber dot for attention and a red diamond for failure. Idle and
ended scopes have no glyph. Each spine uses its own runtime metadata from the
existing activity binding, while the list retains its navigation-summary
arbitration. Runtime status uses the wire vocabulary: `systemError` selects failure
and `closed` selects ended. Navigation summaries normalize those values to
`errored` and `ended` before selecting the same indicator families. The exact
runtime label, including unknown states, remains available on hover and to screen
readers. Readable columns keep their full status labels.

Columns and spines share the existing summary and additive subscription leases
by client and requested ref. An ancestor peek observes only that ancestor's
chosen direct collection; Tasks stays inside the peek. Closing it releases its
collection demand. Reconnect recovers the observed page extent without dropping
useful rows. An empty incomplete page remains progress, not proof of no delegates.
Unknown ancestry and runtime metadata stay explicit. Late metadata and status
changes do not move focus or animate column geometry.

A proven missing or deleted readable scope shows its explanation inside that
column. Healthy parents, Return and Open conversation remain usable. The thread
store's deletion fence outranks retained transcript content; other activity
failures preserve useful history. Transient reads keep the existing hydration
and activity recovery owners, so a recovered child becomes readable in place.

Return closes only the new secondary inspector and focuses its exact surviving
original pane. For a session source it also requests keyboard focus in the
original editor. Closing or replacing the source retires that association; Return
then closes inspection without recreating a source or adopting a reused pane ID.
Existing saved cascades without separated-inspection intent retain their recorded
Return behavior.

Inspection preserves source draft, selected skills and commands, text-owned
UTF-16 mention locations, staged attachments, pending encodes, queued inputs and
mutation identity. Opening and closing inspection preserve the visible source
row across viewport width and height changes, including later composer-height
settlement. Ordinary session and cascade browser readers retain the visible entry. A width change keeps approximate progress through its
usable reading depth, and an entry whose tail is showing keeps that tail in
place; a height-only change keeps the reading line where it was. The existing
transcript registration owns that intent;
VirtualList owns measurement and committed geometry. Restoration waits for useful
committed content and scroll read-back. Newer viewport scrolling or an explicit
positioning command supersedes older work through the pane-lifetime read view.
Native Space and Shift-Space paging count as scrolling when the input can move
that viewport. Button or disclosure activation, editable text, IME composition,
prevented defaults and input consumed by a nested scroller preserve pending reflow.
Reflow does not move editor or neighboring-pane focus. An explicit display change
retains current entry focus even while earlier geometry is pending, then restores
it to the entry or its visible proxy. Passive scroll events can enable bottom-follow
only when VirtualList's measured rows, sizer and viewport agree. A temporary
reflow clamp leaves an away reader's intent intact. A reader already following
the end retains that intent through passive measurement corrections in the same
viewport. Newly measurable viewports still require true-end admission. Existing
true-end growth and viewport-shrink corrections remain active. End following, older-page
prepend and history recovery keep their existing owners. Inspection does not
submit or replay input. An explicit Open conversation opens or focuses an
ordinary independent session pane. Unrelated panes keep their identity and placement.

Mounted same-ref composers keep their own text and selection. Image paste and
removal apply to the editor receiving the gesture and can persist that edit.
Submission and failed-decode cleanup update their original mounted editor without
overwriting another pane's newer persisted draft. Detached continuations remain
fenced by ownership of that shared draft.

Closing the source pane or resetting the workspace retires that source lifetime.
A fresh pane with the same ID and ref owns a new lifetime. Late recovery-write
or acceptance completion cannot change its draft, skill/command selections, mention locations, attachments
or active recovery identity. Already durable input still belongs to the existing
mutation dispatcher; retiring its editor does not cancel accepted work.

Saved workspace intent contains the selected ref, validated ordered edges and
the conversation source descriptor. New inspectors also carry a validated origin
locator with the source pane ID and ref. The
[origin owner](../../cmd/evener-hub/frontend/src/panes/zoom/inspectionOrigin.ts)
binds that locator to an exact surviving pane lifetime only during successful
workspace restore, before publishing restored panels. Missing or invalid origins
remain unbound; retiring a source removes its locator from subsequent saves.
Runtime Return never resolves an association by pane ID alone.

Saved inspector focus survives boot and late ancestry or location recovery when
the browser route's ordinary conversation is already satisfied. A fresh pathname
still selects its requested ordinary conversation. Draft text, skill/command
selections and mention locations use their existing stores through the pane-owned
composer source.
On desktop startup, AppShell recognizes a saved legacy cascade's source as the initial
route role before route placement completes. A matching source keeps the saved
edges, pane identities and neighboring panes when the restored workspace meets
the route's existing focus and placement rules. Location and DockHost restoration
may finish in either order. A different route takes precedence; later pathname
changes retain the ordinary route-placement and companion-focus rules.
Processed image bytes and pending encodes belong to the original pane lifetime;
they are excluded from layout JSON and localStorage and are not restored by a
page reload. Phones keep the ordinary Agents row action, which opens the
conversation. A saved cascade
restored on desktop and shown at phone width exposes the selected
conversation and Return without desktop columns or spines. Return closes a new
inspector and leaves its original source useful. The phone's cascade chrome
stays single-layer: the mobile top bar carries the selected conversation's
name, the column's own title stays desktop-only, and the column's runtime
state and Open-conversation action sit in the column's bottom bar with its
activity controls. The pane footer's status bar and scope crumbs are
desktop-only on the cascade: the column's own controls below the transcript
carry the activity, so the phone never stacks a second chips row at the pane
bottom.
The column's own activity controls reserve the home-indicator band on the
phone, with the same safe-area clearance the composer dock uses.

The [real-stack cascade guard](../../cmd/evener-hub/frontend/scripts/cascadeguard/README.md)
and [design-system geometry](../web-ui/design-system.md#agent-cascade)
describe the executable proof and presentation contract.

## Client lifetimes

The [browser binding](../../cmd/evener-hub/frontend/src/stores/sessionActivity.ts)
shares a store by actual client object, requested ref and scope. A committed view
acquires its holder; an abandoned render starts no read. Every visible desktop
session pane has a status footer that observes that pane's summary and its
subtree summary (for the Agents count) by explicit ref, so side-by-side panes
retain independent counts while duplicate consumers of one ref and scope share
the same owner. Agents, Jobs and Watches tabs observe their own
session-scoped collection. A visible page boundary supplies further demand,
while the shared store retains and retries an interrupted continuation. The
boundary control reports only a page it asked for: a background refresh of the
collection changes neither its label nor its disabled state, so a refresh the
user did not request presents nothing. When a
collection's last observer leaves, the store drops that collection's read in
flight and any demand queued or walking behind it, explicit loads included, so
a store another holder keeps alive takes in no late page for a view that has
closed. Its retained rows stay, and the next observer reconciles them with a
fresh walk, as for any retained store.

About is the last sidebar category and has no count or footer chip. Its
[adapter](../../cmd/evener-hub/frontend/src/shell/activitybar/AboutTab.tsx)
holds the shared thread model for the sidebar's explicit public session ref and
renders the existing details body. That model owner supplies hydration,
accounting, subscriptions and reconnect recovery. About adds no collection
demand, lifecycle mutation, provider call, cache or retry owner. Its category
choice uses the same per-session persistence without changing another category's
disclosure or scroll intent.

The composer names its receiving conversation even when a child or output pane
has focus. Job rows and output tabs use the job description when present, and
output details retain the command and exit status. Each output pane publishes
its already loaded title through the [pane chrome store](../../cmd/evener-hub/frontend/src/shell/chromeStore.ts);
tab naming adds no metadata request. Closing Overview returns keyboard focus to
its visible, connected opener or a visible session-actions control for the intended
session, preferring the originating pane. Existing footer categories retain their
owning-pane fallback. After child drilling replaces the phone's session controls,
a visible parent breadcrumb outside Overview is the final fallback. Dismissal
only moves focus; it does not navigate or create a pane.
Same-session pane menus carry their explicit owning pane
identity, so opening Overview preserves that instance.
The [application shell](../../cmd/evener-hub/frontend/src/shell/AppShell.tsx)
ends route deferral when the resolved location confirms an already placed
session. Opening its child conversation then preserves child focus and the parent
URL, rather than reapplying the parent route. A nested conversation's job log
keeps that focus on the settled parent route too, following the conversation
context the child opened under rather than the transcript-type pane walk.

The [activity sidebar view store](../../cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.ts)
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
folds every terminal background job under Completed, including failed,
cancelled, stopped, killed and unknown outcomes. Current jobs remain outside
the fold. Terminal Jobs and Agents glyphs use ordinary quiet color while their
non-success status text retains the actual outcome. Successful settled Agents
and Jobs use one compact title-and-time line; live work, failed, cancelled,
stopped, exhausted and unknown outcomes keep their context line. Live input and
approval requests retain attention color. Closing history leaves the existing
visible page boundary active, so current work on later pages remains discoverable.
Opening output uses the returned job owner and transcript ref in a secondary pane.

The shared
[task panel](../../cmd/evener-hub/frontend/src/panes/session/chrome/TasksPanel.tsx)
starts settled done/cancelled task history folded. Current and Remaining task sections
remain visible. Task details
remain an explicit disclosure choice.

The browser Tasks tab keeps its settled/total count in the tab label (done
plus cancelled tasks; the footer chip and each cascade Tasks chip count the
same) and starts its body with the task groups, without aggregate summary lines
above them. The
standalone Tasks pane and Tasks Sheet retain their aggregate headers and trigger
labels. Loading, unavailable and empty states remain visible; a failed refresh
retains loaded rows and the existing retry control. Only the sidebar uses the
compact task presentation; the standalone Tasks Sheet and pane keep their
existing row appearance.

The sidebar's Agents, Jobs, Watches and Tasks put a relative `<time>` beside
their titles when authoritative timing exists. Running Agents and Jobs use
latest activity or output, falling back to run/job start; settled rows use run
or job end. Tasks use completion/cancellation time, falling back to last update
for legacy rows; unfinished tasks use last update, not a claimed run time.
Watches use their latest delivery, or their armed/creation time when no delivery
timestamp is retained. A resumed Agent chooses the newer of run start and latest
activity, so an older run cannot become the current run's timestamp. Missing or
invalid times are omitted. The source ISO timestamp and its meaning and absolute
time remain available through the time tooltip. One clock mounted with the
visible sidebar body refreshes these labels every 30 seconds, including expanded
Watch details, without adding activity reads or surviving sidebar close.

Agents, Jobs, Watches and Tasks use `var(--space-1)` vertical and
`var(--space-2)` horizontal row padding. Disclosure rows apply that padding once
at their summary boundary; nested watch content adds no inset. The shared
viewport padding and About field layout remain independent of row spacing.

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

Session chrome and the navigation rail expose Overview through their session
action menus. On the phone, that action opens the full-screen shared activity
sidebar, preserving the last selected category for the focused session. Its
optional active count sums the subagents running at every depth and the
session's own running jobs, only when both are known; watches and completed work
are not part of that number. The sidebar's Agents, Jobs, Watches and Tasks tabs use session-scoped
reads on both viewports; About uses the shared session model. The Agents count
is the exception: the footer chip, the sidebar's Agents tab label, each
cascade scope's Agents chip, the Overview and Activity action counts and the
conversation's liveness line all count subagents at every depth, from the subtree
summary
([`useSubagentCounts`](../../cmd/evener-hub/frontend/src/stores/sessionActivity.ts)),
while the Agents tab lists the session's own. The count and an open Activity
sheet share one subtree store (see [Client lifetimes](#client-lifetimes)).
The workspace registers only Tasks and Details session panels. Saved Activity
placements are unknown panes: the existing workspace restore omits them,
preserves registered panes and selects useful surviving focus. A valid primary
route wins before the empty-workspace Welcome fallback. Retirement leaves
Overview's independent per-session open/About choice intact and issues no
deletion or lifecycle mutation. Standalone Details retains its shared accounting
renderer. The recursive Activity Sheet and inline discovery retain subtree
inspection through their existing owners.
Session action menus offer Delete only for a local top-level session whose
lifecycle is stopped, including a confirmed crash identified by an errored
navigation row with `failure.cause_kind: "crashed"`. Live sessions and ordinary
non-crashed errors keep Delete hidden; Force shutdown remains the recovery action
for eligible live sessions.
Expanded watch details reveal the full user note as wrapped text, regardless of
how much fits in the compact collapsed row.
The recursive Activity Sheet groups every settled job and delegate, including
failures, under its existing per-session Inactive folds. Counts include every
entry grouped there. Opening a fold reveals rows in their original order with
details collapsed by default. Active descendants and the ancestors needed to
reach them remain outside the folds, regardless of an ancestor's settled outcome.
Settled jobs keep their glyphs and status text in ordinary quiet ink; a settled
delegate shows the dot's settled vocabulary instead - no mark done, the red
diamond failed. True outcomes remain
available to assistive technology even when duration replaces visible status;
live questions, approvals and current errors retain their attention treatment.
Detail, action and read failures remain recovery information, not settled outcomes.
The shared entity index includes every loaded entry regardless of disclosure and
keeps its fold origin truthful. Folding does not change owner-qualified output or
child transcript actions, watches, paging or shared reconnect recovery. Overview
and native retain their separate history groups and own-session error priority.
The [panel view store](../../cmd/evener-hub/frontend/src/stores/activityPanel.ts)
retains sheet visibility, disclosure choices and each collection's last loaded
row identity by session ref for the lifetime of its retained workspace panes.
Opening a child conversation releases the hidden subtree read demand. Back acquires
fresh demand through the shared binding and restores the inspected extent using
the existing page boundary, yielding 100 ms between pages. Delegates, jobs and
watches restore independently until their saved identity arrives or an
authoritative complete read proves it absent. A partial first page cannot replace
an outstanding boundary. The view stores only identities, not resource rows,
cursors or retry state; the shared store continues to own failed-page recovery.
Explicitly closing the sheet clears extent restoration and keeps the sheet closed
across navigation. A changed resolved session clears the former session's extent
and disclosure choices. Closing the last retaining pane evicts this view state.

Activity delegate rows use the shared opener, which requests the child's
conversation pane — reusing an already open one for that session — and keeps
the child's parent context so an unchanged root URL does not steal focus; a
fresh phone host's Back returns along that recorded context to the retained
parent pane before falling to welcome.
Ancestor buttons reuse an already open conversation pane for the scope. The
opener names the view it opens: the conversation for a session ref, the
transcript for a job log. Saved layouts restore a legacy session-ref
transcript pane as its conversation pane; job-log transcript panes keep
their pane.

The [native binding](../../mobile-native/src/subagents/subagentTree.ts) projects
subtree activity through the same shared store. Summary holders and collection
holders acquire demand separately, and client replacement fences old replies.
Its retained rendering tree does not own a separate network or retry loop.
Client lifetime changes retire pending model replies even when the same client
object later returns; accepted model evidence remains useful during disconnect.
[Stop-request display records](../../mobile-native/src/subagents/stopRequests.ts)
use the authoritative child ref and raw delegate ID, so another source's equal
ID cannot acquire that request's label or suppress its stop control.
[Conversation reads](../../mobile-native/src/conversation/services/conversation.ts) and
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
`evener-appwire-v7`. Clients request version 3 and the hub advertises that
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
