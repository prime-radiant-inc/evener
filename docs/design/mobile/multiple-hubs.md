# Independent hub validation

Observed on native Release builds from `19222d69a`, 6 September 2026.

The app currently saves multiple named hubs and opens one foreground connection.
This is switching support, not simultaneous connected-hub navigation acceptance.

## Fixture and isolation

Two independent hub processes used separate state/config/cache roots and bearer
credentials. The second copied only owned test-project history, without the
first hub's rendezvous entries or index. Both contain exactly the same ref and
instance: `local:034K5AuZE7eFSMnJTuisxL`. The first names it Same session on A;
the second names it Same session on B. Direct reads verified the difference.

- Hub A: isolated runtime `evener-native-runtime.8rqzxU`, port 56491 through
  the existing test proxy on 9199. Native profiles Harness Hub (iOS) and
  Android test hub (Android).
- Hub B: isolated runtime `evener-native-second-v22jd1ct`, port 56501 through
  a separate test proxy on 9200. Native profiles SecondHu (iOS) and SecondHub
  (Android). Each device saved the profile through its native form.
- The proxies supply isolated test credentials to their respective hubs;
  native token entry/authentication failure recovery is not covered here.
- The production hub was not changed.

## Observed native behavior

On both platforms, a draft left on A did not appear in B's composer at the same
ref. After entering a different draft on B, switching back restored A's draft.
Returning to B restored B's draft. Cold-launching each app on B restored its
hub, conversation, and B draft.

- [iOS A draft](assets/multiple-hubs/ios-hub-a-draft.png)
- [Android A draft](assets/multiple-hubs/android-hub-a-draft.png)
- [iOS B after cold launch](assets/multiple-hubs/ios-hub-b-restored.png)
- [Android B after cold launch](assets/multiple-hubs/android-hub-b-restored.png)

Sending IOSDRAFTA on A and IOSDRAFTB on B from iOS reached their respective
sessions. Each turn was stopped from the native UI. Direct thread/read checks
found A's marker only on A and B's marker only on B. Android's distinct unsent
B draft survived this cross-device activity.

## Observing a stopped session resumed elsewhere

Android had opened B while its session was stopped. When iOS resumed it by
sending, Android remained on the old snapshot despite showing Connected.
This is not cross-hub leakage: the correct hub received the message, but the
observer did not receive the live continuation.

Both saved-transcript fallback paths now capture the requested subscription on
its stable local reference. Capture shares the hub's per-session resume lock,
buffers notifications until the read response is queued, and rejects a saved
fallback when a local relay route is already available. Reading a stopped
session does not launch a daemon.

The two-client regression now passes. Reverting the handler calls reproduces
both missing ownership and missed resumed-event failures. Additional network
checks cover no-subscribe reads, additive and replacement subscriptions,
failed replacement rollback, unsubscribe, and refusing saved fallback for a
routable daemon. The hub suite passed (61.195 seconds); targeted tests also
passed under the Go race detector.

### Native retest against the rebuilt hub

Rebuilt the isolated B binary from `01d49719f` and restarted only B at
127.0.0.1:56501. Its existing proxy remains bound to all interfaces on 9200.
The production hub and A were unchanged.

- Shut down the owned B session and verified its run directory contained no
  daemon rendezvous. Opened it on both native Release apps.
- Sent IOSCOLDOBSERVERCHECK from iOS. Android received the message without
  refresh/navigation, exposed Stop/Steer/Queue, and retained ANDROIDDRAFTB.
  Stopping from Android ended the turn on iOS too.
- Shut down the session again, reopened it on both apps, and verified it was
  stopped before sending ANDROIDCOLDOBSERVERCHECK from Android. iOS received
  that message and exposed Stop without refreshing. Stopping from iOS removed
  Android's running controls too.
- Direct reads found both new markers on B and neither on A, despite the
  identical stable session ref on the two hubs.

Screenshots were captured and visually inspected:
[Android receives iOS](assets/cold-observer/android-receives-ios.png) and
[iOS receives Android](assets/cold-observer/ios-receives-android.png).

This closes the original manual failure in both directions on iPhone 17 Pro
(iOS 26.5) and Pixel 7 (API 35) simulators. Deterministic overlap coverage is
described below. The screenshots also
retain the known unfinished composer density and raw-notice presentation;
these are functional evidence, not visual acceptance.

### Saved snapshot and resume ordering

`TestHubRPCColdSnapshotOwnsResumeUntilSubscribed` holds the real saved
projection at a filesystem journal read using a FIFO on Darwin/Linux. Once
the reader has opened the journal, the test verifies the shared lifecycle
lock excludes resume, starts a turn request, releases the journal, and checks
that the observer receives the daemon's event. Only the filesystem and daemon
network/process boundaries are scripted; the hub, saved projection, lifecycle
lock, subscription capture, resume, and relay execute normally.

Bypassing the fixed handler reproduces failure at the lock-exclusion check.
With the fix restored, ten runs pass under the Go race detector. This covers
saved-read-first ordering for hub-managed resume. It does not establish
ordering for an independently launched daemon outside the hub's lifecycle
lock or every stop/restart interleaving.

## Further work

- Audit relay lifetime across restarts and independently launched daemons.
- Concurrent hub connections and deliberate per-hub navigation continuity.
- Disconnect/reconnect and credential-rotation isolation, delayed requests and
  uncertain writes during switching, hub-removal recovery, physical networks.
- Saved-hub presentation: four profiles already push Add hub below the screen;
  large cards and repeated Edit/Remove controls make switching cumbersome.
  Compact rows and a separate add/edit surface should be evaluated against the
  existing design guide. Current screenshots are evidence, not visual approval.

## iPhone hub form with the keyboard open — 7 September 2026

Observed on iPhone 17 Pro, iOS 26.5, with the Release bundle whose SHA-256 is
`b8708d25356771c3409c8c8b6da09802a69fef91be608c9bf622e25c9a2ad9c9`
(the completed-question and reader lifecycle build recorded in
[reader evidence](reader-continuity-evidence.md)). No hub was added in this check.

With seven existing profiles, scrolling to Add hub and focusing Hub name opened
the keyboard. A drag in the visible form area brought all three inputs and the
disabled Save and connect control above the keyboard. Tapping the visible Hub
origin and then Bearer token fields moved the caret to each field without losing
access to them. Return dismissed the keyboard. The app then returned to the
original v4 acceptance hub and Draft before questions conversation. Both draft
verifiers confirmed all seven retained drafts were unchanged.

Visually inspected captures:
[all fields after scrolling](assets/multiple-hubs/ios-keyboard-all-fields-20260907.jpg),
[origin focused](assets/multiple-hubs/ios-keyboard-origin-20260907.jpg), and
[token focused](assets/multiple-hubs/ios-keyboard-token-20260907.jpg).

This covers blank-field focus and scrolling on this simulator size. It does not
qualify typed credential submission, other text sizes, iPad or physical devices,
nor delayed save/selection ordering. Earlier setup automation targeted inputs
covered by the keyboard; accessibility snapshots may list such inputs even when
they cannot be tapped. This check used the visibly exposed fields and did not
reproduce a focus/scroll defect.

## Delayed saved-hub operations — 7 September 2026

The selection owner separates the latest connection choice from the latest
roster read. A pending save still persists its profile but cannot select it or
navigate after a later save, selection, disconnect or removal. Token updates
retry only the hub currently selected when the write/readback finishes, including
a switch away and back. A readback failure cannot hide a committed credential
change from that retry. Older roster reads cannot overwrite newer reads.
Removal retains the existing partial-failure reconciliation and removes a row
after confirmed deletion even when its follow-up roster read fails.

The storage-boundary regressions use real HubProfiles and deferred reads/writes,
with explicit entry and release promises. Bot observed three failures before
the roster/fallback fix: resurrection after removal, loss of a newer hub, and a
removed row surviving failed readback. A fourth regression proved committed
credentials were not retried when the roster read failed. These cases pass after
the fixes. The 18 selection cases also cover newer/disconnected selections,
overlapping saves, restoration, full versus partial removal failure, ordinary
save while already connected, and token-update switching. No injected delay was
added to the installed app.

The iPhone Release bundle SHA-256 is
`6a5c839fca5e79be53091c43773102cfa21ddca390dbeb5958499ed7ffd2c8df`.
In the actual form, a temporary profile named selection was saved with the owned
hub origin and its credential. It became selected, navigated to Sessions and
showed Connected. Returning to Hubs showed cleared fields; removing selection
removed its row and selection marker. The original v4 acceptance profile was
then selected and its Draft before questions conversation reopened. All seven
retained drafts matched their verifiers. The simulator clipboard was restored
and its temporary backup deleted. No session message was sent.

Visually inspected captures: [saved and connected](assets/multiple-hubs/ios-selection-connected-20260907.jpg)
and [removed with cleared fields](assets/multiple-hubs/ios-selection-removed-20260907.jpg).
Both profiles addressed the same owned direct hub. This proves native save,
selection and removal wiring; it does not qualify distinct-hub routing, native
delayed-storage timing, uncertain network writes or physical-device behavior.
Luna medium implemented the initial selection owner and reviewed interleavings;
Bot added the roster/failure regressions, integrated the fixes and ran native QA.
