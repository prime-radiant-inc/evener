# Native goals

Product authority: current web GoalControl.tsx, the goal entry in shell/palette/commands.ts, composer/builtinCommand.ts, and the goal/set and evener/goal/updated wire contracts. Goals have an objective, status and iteration count. Empty objective clears the goal. There is no budget, pause or invented goal lifecycle control in this surface.

Native presents an existing goal as a quiet composer-adjacent status control. Its Session sheet shows the full objective, status and iterations, with Edit in composer and Clear goal. With no goal, Set goal prepares an editable /goal command in the composer. Replacing a nonempty draft requires a concrete replacement confirmation; cancellation preserves text and images. Submitting /goal follows the existing durable draft checkpoint so uncertain delivery is retained and never replayed automatically. Clear retains the ordinary draft. Live notifications and hydration belong to the current hub/session; a notification arriving during a read must survive that older read.

Verification: transport-boundary set/clear and capability rejection, real draft storage and uncertainty, notification identity and hydration races, then native simulator/emulator set/edit/clear against the isolated real hub. This slice does not complete tasks, jobs, the full slash-command catalog, or release-wide acceptance.

## Implementation and evidence

The shared conversation projection includes goal state and applies evener/goal/updated only to the current session. A goal ownership revision preserves notifications received during an older rehydrate. The service checks the goal capability before goal/set. The composer recognizes the current web /goal grammar only without images; attached messages retain ordinary message routing. Empty commands offer Clear goal only when a goal exists. Native goal submission uses the existing SQLite uncertainty checkpoint, while clear preserves the ordinary draft. Replacing a draft retires text and image references atomically. Pending goal errors remain visible in the Session sheet.

Automated verification passes: 145 native tests, 432 targeted shared projector/service/state tests, native TypeScript and touched-file formatting. Focused boundary tests cover initial goal projection, another session's notification rejection, a newer notification surviving an older read, live clear, set/clear wire parameters, unavailable capability, durable lost-acknowledgement recovery, newer-draft retention and uncertainty preventing replay.

Manual iOS 26.5 iPhone 17 Pro and Android API 35 Pixel 7 checks set an objective through the Session-to-composer action, then observed the real isolated hub report blocked with six iterations. The scripted image provider repeats its response, so that terminal state verifies engine-driven updates, not successful autonomous goal execution. Both cleared the goal, and fresh thread/read responses confirmed absent goals for local:034K0xcRUdVXB4a529zcCs and local:034K15lO0VfSdITfzQSnBh. Android also reopened the objective for editing and retained that composer draft after clearing the goal. Draft replacement cancellation and confirmation were exercised on Android. A keyboard handoff issue was fixed by waiting for Android window focus to return after modal dismissal; the installed app then showed the keyboard with the composer above it. Both Release builds pass after these changes.

Remaining acceptance includes iOS goal-edit replacement, native lost-acknowledgement and storage-failure injection, physical devices, VoiceOver/TalkBack, large text, and full command discovery. Successful set/clear and live status observation do not establish those cases. Production hub 9180 was not used.

## v4 acknowledgment validation and SDK recipe — 7 September

The shared native service now validates the boolean started response before
clearing its durable goal-command checkpoint. Malformed acknowledgments retain
unconfirmed delivery without replay; clear preserves the ordinary draft. Five
service cases reproduced the previous false-success behavior. The 114-test
service suite and two real-SQLite checkpoint regressions now pass.

The independently packaged goals recipe adds explicit ownership and complete
reviewed-goal/instance preflight, exact wire set/clear parameters, validated
acknowledgment and same-instance readback after success or failure. goal/set has
no server CAS, expected-instance or mutation-ID fields: preflight cannot prevent
another writer racing the change, and the hub may resume an exited daemon.
No goal result authorizes automatic replay or proves execution. The 112 SDK
contracts and package gate pass; see the package README for runnable examples.
Real successful autonomous goal continuation on the current iOS/SDK build and
the remaining device/fault matrix are still unqualified.
