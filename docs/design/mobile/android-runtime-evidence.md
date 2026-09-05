# Android running-turn verification

5 September 2026, approximately 15:24–15:27 Pacific. Manual interaction through Android input events with accessibility-tree observations and screenshot inspection. No application source changes.

## Tested artifact and environment

- Android 35 emulator `emulator-5554`; package `com.primeradiant.evener.mobile`, standalone release APK.
- Installed `base.apk` and local `mobile-native/android/app/build/outputs/apk/release/app-release.apk` SHA-256 matched: `a93f8084c1e9d712018b52049d5f563c941dd4a6fe50f2e1312151f54908bd57`.
- APK timestamp: 5 September 2026 15:11:48. It contains the running-turn fixes and transcript disclosure experiment. This check identifies the actual binary; it does not establish a reproducible exact-commit build. Later documentation commits do not change that binary.
- Saved Native E2E profile used `http://10.0.2.2:56491`, the Android emulator route to the isolated local hub. Both the hub listener on 56491 and scripted provider listener on 55873 were confirmed live before testing.
- Isolated workspace `/tmp/evener-native-runtime.8rqzxU/workspace`; real Evener session `034JlVxdyY11DDFNjgDibK`. No production session mutations.

## Observations

1. Opened the installed app, selected the persisted Native E2E profile, reached Connected and opened its existing Android-created session.
2. Entered `Android runtime check. Read NOTES.md.` and tapped Send. Composer cleared, state became active, and Stop/Steer/Queue appeared without reopening the conversation.
3. Entered `Android steering verification.` and tapped Steer. Composer cleared, state stayed active, and no action error appeared.
4. Entered `Android queued verification.` and tapped Queue. Composer cleared and status showed `active · 1 queued`.
5. Tapped Stop. Status changed to `idle · 1 queued`, Stop/Steer/Queue disappeared and Send returned. The queue was preserved.
6. Tapped Latest messages. The sent text appeared in the transcript followed by an interruption notice.

The real hub’s session transcript file contains the exact sent text at line 34. Its mutation record contains the exact steering and queued strings. This corroborates receipt, not model compliance with steering: the scripted provider was deliberately held and then interrupted.

The queue screenshot is retained locally at `.superpowers/sdd/2026-09-05-native-session-creation/android-running-queue.png`. It shows the current prototype, not approved visual design.

## Limits and resulting work

- This closes the specific Android basic running-turn interaction gap for the identified installed artifact. It does not prove physical-device performance, distribution readiness, reconnect races, all keyboard behavior or the full app objective.
- The retained queue cannot be inspected, cancelled or promoted from the current native UI. Those controls remain required and are represented in workflow study 3.
- The transcript still exposes internal reminder text and gives routine content excessive visual weight. The design review and content-presentation work remain necessary.
- No visual implementation was resumed while the proposed philosophy and lookbook await review.
