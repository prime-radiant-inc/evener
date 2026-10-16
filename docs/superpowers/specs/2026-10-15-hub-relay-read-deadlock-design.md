# Hub relay read progress and ordered recovery

Status: proposed, awaiting Jesse's approval before implementation.

## Goal

Find and fix the cause of the hub-side session-read hang reported in
[Hub session reads hang](https://github.com/prime-radiant-inc/evener/issues/4666).
Opening a responsive session must finish loading and continue receiving updates
without force-shutting down the session, disabling background streaming, or
losing saved or live work.

The deliverable is a verified, reviewed, merged repair with regression evidence.
Deployment and a production hub restart require separate authorization.

## Evidence and uncertainty

The observed session daemon answered direct metadata and one-item history reads
in 12–24 milliseconds, including `subscribe:true`. The same reads through the
hub did not return within a 12-second diagnostic deadline. Unrelated sessions
loaded through the same hub. This establishes a hub-side failure; it does not
establish the exact goroutine wait in the running hub.

The leading hypothesis is a circular dependency between a read's pending route,
an unbound notification's acknowledgement, and the source relay's pre-response
acknowledgement barrier. Route identity is published before that barrier, but
`handle.pendingRoutes` remains charged until the read's downstream handoff
releases it. An unbound notification is retained while any route is pending.
The real relay serializes reads, so a queued read may also keep route resolution
pending while waiting behind the blocked read.

Code inspection identifies a reachable source of an unbound notification: a root
and read-only child first subscribe through the same canonical relay. The child's
downstream subscriber then leaves, and the hub retires that child's route while
the root keeps the shared upstream connection alive. The upstream connection
retains its child subscription. A later real descendant event can therefore reach
the relay after the corresponding hub route is gone. A root-only subscription
does not, by itself, receive every child's notifications.

Hypotheses to check in order:

1. An unbound pre-response notification waits for pending-route release while the
   read waits for its acknowledgement, including a second queued read.
2. A route replacement or departure waits for a publication or handoff that
   cannot drain because the same read owns it.
3. A disconnected or canceled read leaves an owner, gate, or delivery outstanding
   and prevents healthy subsequent requests from progressing.

The existing `TestHubRelayUnknownTargetDoesNotHeadOfLineBlockKnownDelivery`
exercises hub routing with a scripted lease. It does not exercise the real
source acknowledgement barrier. Its success does not disprove the hypothesis.

A reproduction of a reachable bug must be distinguished from proof that it
caused the reported incident. Trace the real notification producer and match the
reproduced event/read conditions to incident evidence where available. If that
link remains uncertain, say so explicitly; do not report the live stack as known.
If all three hypotheses fail, gather new boundary evidence before choosing a fix.

## Scope and ownership

Primary code owners:

- `cmd/evener-hub/app_relay.go`: canonical handle, authoritative target routing,
  pending reads, downstream publication and handoff ownership.
- `cmd/evener-hub/internal/appsource/relay_session.go`: serialized upstream reads,
  response cut, ordered delivery acknowledgements and connection recovery.
- Real integration tests under `cmd/evener-hub` and source tests under
  `cmd/evener-hub/internal/appsource`.

Inspect upstream `server`/`internal/appserver` producers and downstream shared
clients to establish the full flow. Change them only when the reproduced cause
requires it; a public protocol change or wider ownership redesign requires a
spec amendment and Jesse's approval first.

All clients reading through the hub share this boundary: browser, native phone,
TUI and other AppWire clients. The browser's background transcript subscriptions
are a suspected trigger to test, not a feature to remove. No client layout,
cache format, background budget, subscription capacity accounting, or mutation
behavior change is planned. Remote clients must retain existing forwarding
behavior; noncanonical sources must keep their existing behavior.

## Required behavior

1. **Progress:** metadata and history reads through a healthy upstream connection
   complete even when unrelated, unbound target notifications occur around the
   snapshot cut. Later reads sharing the canonical relay also complete.
2. **Correct routing:** an unrelated valid target is never delivered to a root or
   sibling. A pending legitimate child/alias receives its eligible events through
   its authoritative route once that route is established.
3. **Ordered handoff:** existing subscribers receive eligible updates in producer
   order. A newly joining client gets a coherent snapshot followed by its live
   suffix, with no missing or duplicate represented items. Preserve authoritative
   ref precedence, identity-generation fencing, and target-specific enrichment.
4. **Independent consumers:** concurrent reads, separate clients/tabs, and a
   background subscriber do not prevent a foreground open or disrupt a healthy
   sibling. Unsubscribing one consumer leaves the others useful.
5. **Recovery:** cancellation, disconnection, failed publication, and teardown
   release their owned work. Reconnection rehydrates authoritative state and
   resumes useful updates. A daemon departure announced during the read-to-client
   subscription handoff remains observable; fixing ordinary event progress must
   not erase that recovery signal.
6. **Preservation:** keep saved history, accepted input, drafts, and running daemon
   work. A read must not launch or stop a daemon. Do not introduce silent event
   dropping, broad acknowledgement-barrier removal, client-side deduplication,
   deadline widening, or repeated retries as substitutes for fixing the cause.

## Investigation and implementation approach

Build a deterministic regression with the production hub, real LocalDaemonSource,
real relay actor and real AppWire transport. Control only network/provider input
and synchronization barriers. Drive the pre-response notification/read ordering
explicitly rather than relying on sleeps. A test ceiling is a deadlock tripwire,
not the ordering mechanism.

The first fixture should subscribe root and child, unsubscribe the child, await
the hub's existing `AfterIdleDelete` completion hook, and verify that the upstream
child subscription remains. Gate a new root read with the daemon appserver's
`SetBeforeSubscriptionGate`, publish a typed child event through the real
`RecordDescendantAppEvent` producer, then release the read. This orders the child
notification before the response on the real connection without inventing a
foreign frame. Await the hub's pending-delivery observation only after releasing
the daemon gate, since the source publishes captured frames at the response cut.

Prove the failure on the unchanged implementation. Record the events and owned
waits that create it, then make the smallest source change that removes that
confirmed dependency while preserving the guarantees above. The implementation
choice is deliberately conditional on the failed test; this spec does not assume
that decrementing `pendingRoutes` earlier is safe.

Keep a single ownership model. Distinguish route discovery from downstream
handoff retention if the evidence shows they have been conflated. Any such split
must account for buffered ordinary events and departure/recovery signals, plus
queued commands, cancellation, and route replacement. Do not add a watchdog,
parallel stream, second cache, or broad relay rewrite.

## Acceptance evidence

### Deterministic regression

Use real hub and source relay components, not a scripted `RelaySessionLease`.
The regression must fail before the source fix and pass after it. It must show:

- the notification crosses the real transport before the read's response cut;
- the targeted read returns and a subsequent read also returns;
- a known-target live update still reaches the correct subscribed client;
- unknown-target content does not leak into another session;
- the fixture's clients, handlers and relay goroutines terminate on cleanup.

Extend the regression matrix to pending child routes, authoritative aliases,
multiple queued reads, cancellation and disconnect at the relevant barriers,
and daemon departure during handoff. Reuse existing tests where they already
prove those cases through the real boundary; add missing cases rather than
replicating a broad test framework.

### User journey

Use real daemons driven by a scripted provider, a real hub and the production
browser entry route. Preserve the existing background-transcript journey:
background production, sidebar open, continued output, older-page reading,
reopen and reconnect. Check produced item identities/content against independent
producer evidence, including no duplicates and useful progress after recovery.
Add the regression trigger to a real-stack journey if a legitimate producer can
exercise it there; a manually injected impossible frame cannot prove the live
incident's cause.

Use two concurrent real clients to prove independent subscriptions and the
foreground/background interaction. Exercise a second browser tab for the
reported cross-tab symptom. Keep native/TUI wire contracts unchanged and run
impacted shared-client/transport tests; do not claim native or TUI visual testing
unless actually performed.

Compare otherwise equivalent event/read schedules with and without a background
subscriber. Report whether background streaming is necessary to trigger the
reproduced bug, merely increases overlap, or remains unlinked.

### Checks and review

Baseline command, already passing on the starting revision:

```sh
go test ./cmd/evener-hub/internal/appsource ./cmd/evener-hub \
  -run 'Test(Relay|HubRelay)' -count=1 -timeout=180s
```

Run the new regression red and green, affected Go packages and race tests,
targeted lint/vet, and the real background-transcript browser guard. Read the
actual scripts before running them. The existing browser entry point is:

```sh
go test -tags browserguard ./cmd/evener-hub \
  -run '^TestBackgroundTranscriptsEntrySliceBrowser$' -count=1
```

Run frontend gates if their files change. Full repository checks run through CI;
keep heavy local gates sequential. A timeout or skipped browser fixture is not a
passing check.

Freeze the approved spec and execute PAR with two independent competing reviewers
using the installed command's contract. Review the implementation against an
immutable base/head diff, including new files and behavioral evidence. Run the
required simplify and shepherd workflows. Merge only with current-head checks
and reviews settled under repository policy.

## Documentation and delivery

Update S06 in `docs/product/subsystems.md` and the owning routing guide
`docs/evener-hub-web-routing.md` if the fix changes routing/recovery ownership or
clarifies its progress guarantee. Update linked protocol text only if its stated
behavior needs correction. Keep evergreen docs about current behavior, not this
incident's history.

Report the confirmed cause and its proof, the remaining limits of incident
attribution, red/green and journey results, PR/merge SHA, and cleanup. Update the
issue with reproducible evidence. Preserve unrelated checkout changes. Remove
only owned disposable scratch and the merged worktree/branch after verifying
squash content. Do not deploy or restart production as part of this approval.
