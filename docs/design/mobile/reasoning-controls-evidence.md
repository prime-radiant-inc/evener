# Native reasoning controls — 5 September 2026

The Session sheet exposes the current session’s advertised reasoning levels as native radio choices. It does not invent a default or tiers when the server provides no ladder. Selection uses the existing shared conversation service and refreshes the authoritative projection. The controller validates the latest supported levels at dispatch, serializes session actions and retains its existing binding/disposal checks. No automatic mutation replay is added. The current model is labeled Model: the wire field named modelProvider contains the model label rather than reliably identifying a provider instance.

Native creation and session settings share Choice. Its iOS label measurement uses the same explicit live font scaling as Action/Copy, with Android native scaling preserved. Review identified this omission; changing Dynamic Type with the sheet mounted reproduced clipped radio labels before the correction. The final iOS build retained readable low/medium/high labels after live accessibility-large scaling and scrolling, and selecting low still succeeded. This is a focused visual check, not complete VoiceOver certification.

## Verification

- Native tests: 61 across 12 files, TypeScript and targeted Biome pass. Two new controller tests first failed because the reasoning action did not exist; they cover actual shared service dispatch, duplicate suppression, authoritative refresh and rejection of unsupported/stale choices. They script the network request boundary, not a complete daemon.
- Final Release builds passed and were installed on iPhone 17 Pro / iOS26.5 and Pixel7 / AndroidAPI35.
- Real isolated hub at port56491, session local:034Jpw6Pu89wMXd5BusUvs (Session controls verified). Its local scripted provider was configured with explicit low/medium/high effort values for fake-test-model. This is test configuration, not a claim about an actual model’s reasoning behavior. Production was untouched.
- iOS selected high; Android selected medium. Independent thread/read calls returned the chosen values. After the final scaling correction/rebuild, iOS selected low at accessibility-large size and Android selected high; independent reads again confirmed each value. The iOS restarted app also displayed the medium value previously set by Android.
- iOS accessibility tree reported each radio label and checked/unchecked state. Final screenshots: [iOS large text](assets/reasoning-controls/ios-large.png), [Android](assets/reasoning-controls/android.png). iOS text size restored to large afterward.

The isolated setup exposed a lifecycle timing boundary: issuing model/set immediately after shutdown encountered the previous daemon address before its registry entry disappeared. A later explicit model/set resumed the runtime and loaded the test configuration. The app does not automatically replay this failure. Immediate stop-and-mutate recovery remains a broader lifecycle acceptance item.

Existing-session model/vision selection, catalog scoping by harness and working directory, provider-request consumption of the chosen effort, native acknowledgement loss/reconnect tests, full screen-reader checks and physical-device performance remain open. No full repository merge gate was run for this native-only increment.
