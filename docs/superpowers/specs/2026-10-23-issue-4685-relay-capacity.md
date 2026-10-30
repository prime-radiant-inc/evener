# Issue #4685: prove the hub relay burst at full pending-delivery capacity

Status: proposed. Freeze this file before the implementation PAR.

## Goal

Close the coverage gap in
[#4685](https://github.com/prime-radiant-inc/evener/issues/4685): the existing
deadlock regression `TestHubRelayPendingTargetBurstMakesProgress` sends
`hubRelayPendingDeliveryLimit+1` real child events but never proves that the
canonical listener's pending buffer reached its 64-delivery bound before
authoritative publication resolved routing. Production still enforces the bound;
this work pins that behavior with a test and proves the test fails when the bound
is removed. It does not change runtime behavior or the limit value.

## Capacity invariant

For one canonical listener in `cmd/evener-hub/app_relay.go`:

> The fan-out retains at most `hubRelayPendingDeliveryLimit` (64) deliveries in
> `pendingDeliveries` at every mutation; the limit is unconditional. In the
> scenario this test exercises, `handle.pendingRoutes != 0` while a read owns
> unresolved route discovery. When at least 65 real targeted deliveries for an
> unresolved route are produced, occupancy reaches exactly 64 before
> authoritative publication resolves the route, and the listener stops reading
> its delivery stream (`deliveryInput` is nil) instead of accepting a 65th
> delivery.

The test pins the contract value as a literal 64 and produces 65 events. It does
not derive its expected capacity from the code under test: a deliberate change to
the limit must update this test.

## Observation and hold points without changing production semantics

`pendingDeliveries` is a local slice in the fan-out goroutine, and authoritative
publication is a closure invoked by the source read, so neither the occupancy nor
the publication instant is observable from the test. Add four unexported
`atomic.Pointer` seams beside the existing `observeHubRelayWait` seam in
`cmd/evener-hub/app_relay.go`:

```go
var observeHubRelayPending atomic.Pointer[func(int)]
var observeHubRelayPendingBackpressure atomic.Pointer[func()]
var observeHubRelayRoutePublication atomic.Pointer[func()]
var observeHubRelayDeliveryRead atomic.Pointer[func(string)]
```

- The `observeHubRelayPending` callback is invoked with
  `len(pendingDeliveries)` after every append to and
  trim of `pendingDeliveries` (the retained daemon-gone append, both targeted
  append sites, and `pendingDeliveries = kept` in `processPending`). It samples
  the exact slice length after each mutation, so the test can assert the maximum
  over the whole run.
- The `observeHubRelayPendingBackpressure` callback runs at the top of the
  fan-out loop in
  the branch where `len(pendingDeliveries) >= hubRelayPendingDeliveryLimit`, i.e.
  exactly when the bound sets `deliveryInput` to nil. The test uses this as the
  deterministic "the buffer is full and the listener has stopped reading" signal.
- The `observeHubRelayRoutePublication` callback runs once at the very start of the
  `RelayRoutePublication.Publish` closure built in `commandFunctions`, before
  any route binding and before `resolveRoutesLocked` decrements
  `handle.pendingRoutes` and signals the route change. The test uses it to hold
  authoritative publication after the response cut. It must not be placed later
  in the closure: route binding and resolution must not begin while the test is
  waiting for capacity.
- The `observeHubRelayDeliveryRead` callback is invoked with the computed
  routing key after the fan-out reads a delivery from its input and before it is
  buffered or published; daemon-gone deliveries return earlier and are not
  observed. The test counts deliveries whose routing key is
  `childRef`: exactly 64 are accepted before publication release, and at least 65
  after it. That proves a 65th real child delivery was offered and refused by the
  bound, not merely that 64 happened to arrive.

Production never stores the pointers. Each site is one atomic load and a nil
check with no behavior change. All callbacks run on the fan-out goroutine or the
read's publication goroutine, so the test's recorder needs only atomics. The test
stays serial. Install the seams before calling `newHubRelayProgressFixture`, and
register their restore `t.Cleanup` before that call: cleanups run last-in,
first-out, so this restore runs **after** the fixture's `stopCanonicalRelay`
cleanup. Atomic stores keep a late fan-out load from racing the restore even
though the fixture does not join that goroutine; the cleanup also releases the
publication and waits for its in-flight read before restoring.

Why the publication hold is required: the relay session buffers every
notification received while a read is in flight in `capture.beforeCut` and queues
them only when `observe` sees the response
(`cmd/evener-hub/internal/appsource/relay_session.go`, `observe`). The existing
deadlock test states the same ordering: "The source publishes it at that
response's cut." Waiting for capacity while the daemon subscription gate is held
therefore waits for frames that cannot be published yet, and that sequence is
prohibited by this spec.

## Hold/release sequence

New test `TestHubRelayPendingTargetBurstAtCapacity` in
`cmd/evener-hub/app_relay_read_progress_test.go`, using
`newHubRelayProgressFixture` (real hub server, real `LocalDaemonSource`, real
`daemonserver.Server`, loopback AppWire transport):

1. Subscribe root and child; unsubscribe the child and await the real
   `AfterIdleDelete` retirement. Confirm the upstream child subscription remains
   (`SubscriberCount(childID) == 1`), as the existing deadlock tests do.
2. Arm the daemon `SetBeforeSubscriptionGate` and start a real root
   `ThreadRead(Subscribe: true)`. Await `gateEntered`. The read owns unresolved
   route discovery, so `handle.pendingRoutes != 0`.
3. Produce 65 distinct real child updates with
   `daemon.RecordDescendantAppEvent` (distinct notes, so the projection does not
   collapse them). They arrive on the retained upstream child subscription while
   the read's capture is open, so the source holds them in `capture.beforeCut`.
4. Arm the route-publication hold seam. Release the daemon subscription gate. The
   daemon returns the read response; the source sees the response cut, queues the
   65 captured notifications, and calls `RelayRoutePublication.Publish`, which
   parks in the seam before any route binding or resolution. The fan-out receives
   the queued deliveries while `pendingRoutes` is still 1.
5. Wait for the backpressure callback. Assert the recorded maximum occupancy is
   exactly 64 and that no sample exceeded 64. Assert exactly 64 child deliveries
   were accepted and the publication hold was entered. The hold entered means the
   response cut happened; because the bound has disabled the delivery input, the
   65th child delivery is blocked upstream rather than accepted.
6. Release the publication hold. Require the root read to return the root thread,
   then a subsequent root read to succeed.
7. Send a root sentinel note and a root fence note through the real projector and
   require each consumer to receive the sentinel exactly once, then the fence, in
   producer order (`assertHubRelayRootNotes`). Every burst delivery is a notes
   frame, and that helper rejects any notes/status frame whose `ThreadID`/`Ref`
   is not the root, so it covers the exact foreign-target leak this burst could
   produce. The helper does not inspect other notification methods; this test
   does not claim broader leak coverage. The fence is the stream fence: the
   source's FIFO publication loop is blocked on the 65th child delivery until the
   fan-out drains, so the fence cannot arrive before all 65 have been processed.
   The retained deliveries resolve to the retired child route and are
   acknowledged without a target after publication. Ordered replay of a retained
   delivery to a route that becomes known is already covered by the unchanged
   `TestHubRelayRoutePublicationUnblocksPendingDelivery`; this test proves the
   known root target remains ordered after the burst.
8. After the fence, assert the current occupancy has returned to 0 and the
   accepted child-delivery count is at least 65. This last count is what proves
   the 65th delivery existed and was refused by the bound during the hold.

Failure paths print the observed occupancy; the capacity timeout also prints the
goroutine dump, as the existing read-progress tests do. The fixture context (10s)
is the tripwire for a missing capacity signal, never the ordering mechanism.
Cleanup always releases both the subscription gate and the publication hold, and
waits for its in-flight read before dropping the observations.

## Negative mutation check

With the test and seams in place:

1. Apply a temporary local mutation in `cmd/evener-hub/app_relay.go` that removes
   the bound: replace the guarded `deliveryInput = deliveries` assignment with an
   unconditional assignment (delete the `if len(pendingDeliveries) <
   hubRelayPendingDeliveryLimit` guard).
2. Run the new test. Expect failure at the capacity check: a recorded sample of
   65 (or more) exceeds 64, and the backpressure callback never fires because the
   fan-out keeps reading while publication is held. The test must fail, not hang:
   the violation sample is awaited alongside the backpressure signal.
3. Revert the exact mutation with a reverse patch (no stash and no `git restore`
   over uncommitted seam work), confirm the file matches the pre-mutation content,
   and rerun the new test green.
4. Run the original deadlock regressions
   (`TestHubRelayRetiredChildDoesNotBlockRootRead`,
   `TestHubRelayRetiredChildReadWithBackgroundConsumer`,
   `TestHubRelayQueuedReadDoesNotHoldPreCutAcknowledgement`,
   `TestHubRelayPendingTargetBurstMakesProgress`) green. Their assertions are
   unchanged; their shared fixture setup is extracted into
   `openRootWithRetiredChild` with no behavior change. Do not weaken or replace
   their assertions.

## Non-goals

- No change to `hubRelayPendingDeliveryLimit`, routing, acknowledgement, or
  backpressure behavior.
- No rewrite or replacement of the existing #4666 deadlock tests, no shared
  assertion helper that changes their semantics.
- No exported API, configuration, protocol, or product-doc change: current
  behavior is unchanged and `docs/product/subsystems.md` S06 already states the
  progress guarantee.
- No new test framework or scripted lease: the test keeps the real hub, source
  and daemon components.
- No full local suite run; CI is the source of truth. Local checks stay targeted
  to the changed package.

## Verification

- `gofmt -l cmd/evener-hub/app_relay.go cmd/evener-hub/app_relay_read_progress_test.go`
  prints nothing.
- `go test ./cmd/evener-hub -run '^(TestHubRelayPendingTargetBurstAtCapacity|TestHubRelayPendingDeliveryLimitCleansUpOnCanonicalStop|TestHubRelayPendingTargetBurstMakesProgress|TestHubRelayRetiredChildDoesNotBlockRootRead|TestHubRelayRetiredChildReadWithBackgroundConsumer|TestHubRelayQueuedReadDoesNotHoldPreCutAcknowledgement|TestHubRelayRoutePublicationUnblocksPendingDelivery|TestHubRelayReadCancellationAllowsNextRead|TestHubRelayReconnectAfterPendingRead)$' -race -count=1 -timeout=300s`
  exits 0.
- The capacity test repeated, e.g. `-race -count=20`, exits 0 with no flake.
- The mutation run above fails with an occupancy sample above 64.
- `go vet ./cmd/evener-hub` exits 0.
- `make build` exits 0 because `app_relay.go` changes (nil-by-default test
  seams).
- Implementation PAR on the frozen base/head diff with two independent competing
  reviewers; legitimate findings resolved with failing-then-passing evidence.
- Simplify-code pass, then shepherd the PR with current-head checks and a roborev
  review whose findings are all low or conclusively refuted before admin merge.

## Risks and limits

- The 65 child updates must each produce a real targeted delivery; distinct note
  values avoid deduplication. If the real projector still coalesces or drops any,
  the test fails because fewer than 65 child deliveries are accepted after
  release; the fix is a different real event kind, not a synthetic frame.
- The publication seam proves the fan-out reached capacity while
  `pendingRoutes != 0` and the read was still in flight. It does not add a new
  production wait: the source's own capture/acknowledgement barrier still bounds
  the read.
- The seams observe the fan-out goroutine's slice and the read's publication
  entry only. They cannot prove network backpressure beyond the source's own
  bounded channels; they prove the listener stopped accepting at the bound, which
  is the contract under test.
- Local execution does not replace CI's race and full-suite lanes.
