# iPhone checkpoint before shared TypeScript extraction

Jesse authorized landing the existing native application in focused reviewable units before moving higher-level web state into the API library. The source development branch `live-concepts-plan2-integrate` and its unrelated unfinished Apple edits remain preserved.

The prerequisite units cover server/protocol correctness, portable activity helpers, an independently installed SDK package, pure composer/catalog/input helpers and invalid shortcut validation. The native unit then contains `mobile-native/`, eight existing shared runtime modules under `mobile/src`, their behavioral tests/fixture, declared dependencies, build controls and CI gates. Existing shared paths are retained to avoid turning this checkpoint into the planned state refactor.

The eight runtime modules are `conversation/model.ts`, `conversation/project.ts`, `services/activity.ts`, `services/conversation.ts`, `services/newSession.ts`, `services/roster.ts`, `state/activity.ts` and `state/conversation.ts`. Legacy Tauri runtime/UI, generated legacy Apple projects, older mobile concepts and unrelated historical artifacts are excluded from this landing, not deleted from the preserved development branch.

Selected sources must retain newer-main contracts and behavior. Copying an old dependency closure over current protocol/keybinding/web files is not an acceptable integration strategy. The native candidate uses narrowly extracted pure helpers and current-main definitions instead.

Each merge requires green current-head CI and a clean current-head RoboRev review; Jesse explicitly authorized skipping the separate human approval under those conditions. A reproducible build and the [joined iPhone acceptance gates](acceptance.md) follow the source checkpoint. An available older TestFlight build does not qualify the new source. Higher-level state extraction starts only after this checkpoint lands.
