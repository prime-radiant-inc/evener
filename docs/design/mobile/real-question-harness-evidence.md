# Real harness question acceptance

Verified 5 September 2026 on native commit 8bd34913b, using the installed iOS Release app and the real isolated Evener hub/daemon. This supplements the controlled AppWire fixture tests in question-evidence.md.

## Boundary and successful run

The existing fakellm package served a scripted provider on loopback port 58875. The isolated model-provider proxy routed only requests containing NATIVE-ASK-HARNESS to it; ordinary test traffic retained its previous provider. AppWire traffic on port 9199 was forwarded without question injection. Production was untouched.

A fresh session was created through the iOS native New session flow against the test workspace and Fake Test Model. Session local:034JxVIyO9oM0UMdFCoVfM advertised ask_user to the provider. The provider returned a valid ask_user call. The real harness executed it and the native app exposed one unanswered question.

- Before answering: thread/read projected askPending true, send true, clear false and a completed ask_user item.
- In the native sheet, selected the advertised fallback and tapped Send answers once.
- The subsequent provider request contained exactly the structured fallback answer. The successful driver log contains one answer_received request.
- The provider returned communicate with end_turn true and the required output envelope. The resumed turn completed successfully.
- Afterward: thread/read projected askPending false, clear true, one answer user message and the completed communicate preview. The native sheet disappeared and the final response rendered. The thread's generic awaiting status remains after completion; that status alone is not evidence of a pending ask_user.

[Trimmed actual snapshots and projection results](assets/real-question-ios.json) exclude system prompts and retain relevant turn identities, statuses and items.

![Completed real iOS question turn](assets/real-question-ios-completed.png)

## Corrected test-provider error

The first run, local:034JxOVYtIHAXfjosUH1gY, proved delivery of a selected option but ended with a failed turn: the scripted provider emitted bare text repeatedly, while this harness requires communicate. That was a test-provider defect. The driver was corrected using the repository's communicate response shape, and the successful run above used a fresh session. The first run is not counted as clean completion or as an app delivery failure.

## Android and remaining acceptance

Android real-harness acceptance remains open. Before creating its session, emulator-5554 stopped responding to uiautomator, basic shell and screenshot commands. A host sample of the live QEMU process was captured at /tmp/evener-android-freeze.sample.txt. It shows the main loop waiting on its I/O-thread mutex for much of the sample but does not establish the root cause. An earlier restart restored service temporarily; repeated restarts are not proof of a fix. The stalled Android creation attempt is not question acceptance.

Earlier both-platform controlled-fixture send checks and question-draft restoration checks remain separate evidence. Real Android completion, cross-device resolution with a real question, storage fault injection, largest text and screen-reader traversal remain required. No full-repository or release-ready claim is made.
