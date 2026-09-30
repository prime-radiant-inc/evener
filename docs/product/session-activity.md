# Session activity

Session activity describes the delegates, shell jobs and watches owned by a
conversation. The primary navigation rail lists sessions and their compact
summaries. Reading a session's activity is a separate operation, so browsing
navigation does not load every session's work tree.

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
even when another session's manager observes the source.

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
Summary reads use available controller state or warm indexes. An unknown badge
does not cause a scan of descendant job journals. Establishing retained child
ancestry may advance the root delegate journal in bounded steps.

List arrays are always present. An empty array establishes emptiness only when
the page is complete and has no issues. An incomplete page can contain no rows
while reconstruction makes progress. Issues identify incomplete or unavailable
branches without pretending they contain no work.

Watch state is `armed`, `ended` or `unknown`. A retained registration alone does
not prove that its runtime still has an armed watch. Recorded delivery counts
and end reasons remain useful, and retained watch history has a bounded lifetime.

## Pagination and recovery

Rows are ordered by creation time and stable identity. An opaque cursor fixes
the initial membership boundary for a walk; new creations appear after a fresh
root read even when timestamps tie or the clock moves backward. Status fields
reflect the state read for each page, so a job can finish
while its collection is being paged. This is not a transaction across the three
collections.

Cursors bind the resolved session, source epoch, resource and scope. They are
not offsets or paths. A lost derived cache or replaced journal produces a typed
stale-cursor result. Clients restart the affected collection and retain useful
displayed rows until the replacement is ready. Never combine pages from different
epochs or refresh attempts.

Pages default to 50 rows and accept a maximum of 200. Collection responses are
bounded to 256 KiB. If an intact ancestry context or a single projected row cannot
fit, the read returns a typed unavailable error instead of truncating identity or
returning a continuation that cannot advance. Remote ref qualification can
increase the encoded size; an oversized page is reread from the same input
cursor with a smaller limit and uses that read's own continuation.

Cold reads share a budget of 2,000 event or projection work
units and 4 MiB of newly read journal bytes across their sources. A stored batch
is decoded one event at a time. One event and its existing atomic fold count as
one work unit under the journal's record-size limit; cancellation is checked
between events. These are input and work bounds, not a hard CPU or wall-clock
deadline. See the [domain behavior tests](../../agent/session_activity_test.go)
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
an observed summary so its counts can become known.

Transient failures retain useful data and retry after 1, 2, 4, 8, 16 and then 30
seconds, continuing at that cap while observed. Reconnect and explicit refresh
can wake recovery. Proven missing resources, invalid requests and unsupported
methods do not spin. Incomplete ancestry progresses at a paced interval instead
of using failure backoff. Disposal cancels timers and ignores late results.

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

The [presentation adapter](../../appwire-client/typescript/sessionActivityPresentation.ts)
builds rendering models without network calls, retry timers or lifecycle
authority. Loaded descendants whose parents have not arrived remain visible
with incomplete relationship evidence. Rendering keys qualify resource IDs with
their authoritative refs. API actions still use the raw logical ID and returned
owner ref, not a rendering key. Optional usage fields stay absent when unknown.

See the [store tests](../../appwire-client/typescript/sessionActivityStore.test.ts),
[lease tests](../../appwire-client/typescript/threadSubscription.test.ts) and
[presentation tests](../../appwire-client/typescript/sessionActivityPresentation.test.ts)
for these shared ownership contracts.

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

When changing this boundary, update the [subsystem map](subsystems.md), public
contracts, domain producers, routing tests and actual client lifetimes together.
The APIs are useful independently of a particular panel or navigation layout.
