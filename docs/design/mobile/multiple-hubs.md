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
(iOS 26.5) and Pixel 7 (API 35) simulators. A deterministic overlap test for
snapshot capture versus resume is still required; sequential native success
and Go race-detector success do not prove that ordering. The screenshots also
retain the known unfinished composer density and raw-notice presentation;
these are functional evidence, not visual acceptance.

## Further work

- Prove deterministic snapshot/resume overlap and audit relay lifetime across restarts.
- Concurrent hub connections and deliberate per-hub navigation continuity.
- Disconnect/reconnect and credential-rotation isolation, delayed requests and
  uncertain writes during switching, hub-removal recovery, physical networks.
- Saved-hub presentation: four profiles already push Add hub below the screen;
  large cards and repeated Edit/Remove controls make switching cumbersome.
  Compact rows and a separate add/edit surface should be evaluated against the
  existing design guide. Current screenshots are evidence, not visual approval.
