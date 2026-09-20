## D6 decomposition plan (Jesse's five-rounds rule, 2026-09-17)

#1549 (round 24, true non-test diff 2206+/517- over 20 files) is not merged as is. It stays open as the design oracle: its rounds 21-24 tests pin the behaviour every piece below must keep. Measured basis: 76% of its keybindingsStore delta is the store-agnostic draft/fence/settle machine; 12 of the 19 new keybindings tests have byte-identical titles in the transcript suite; every rounds 21-24 fix was written twice. So the core is built first, on main's keybindingsStore, and transcript display lands as a thin specialization.

Serial from main, one mechanism each, non-test lines in parentheses:
1. Extract the ready-generation fence: readyGenerationFence.ts + keybindingsStore wired to it (~205). [#1728 merged]
2. Web: a stale client's ready callback cannot begin a generation (~27). [#1729 merged; #1742 applies it to threads.ts]
3. Extract the draft-checkpoint port (load/save/removeIf/discardClassified; no replaceIf; void, pure move) + testing/draftStorage.ts (~205). [#1736]
4. Settles publish even when the local apply throws: thunk extra, applyHubOverridesSettling, settledWrite (~115). [#1739]
5. A settle adopts a checkpoint replaced under it: replaceIf/replaceClassified + the settle paths; the void->boolean widening and the native backend side live here (~140). [#1741]
6. An unreadable record never locks the section, store side only (draftUnreadable, assertDiscardable, cleared uncertainty); the native screen projection moves to #1693 (~80).
7. A draft's generation is stamped by its first authoritative payload (~60).
8. createSettingsHubStore<Payload> core, part 1: payload/support/generation (~510 moved; extracted with #1549's transcriptDisplayStore.ts open as the design oracle so Payload fits a per-layout map on the first try).
9. Core, part 2: the draft editor (~470 moved).
10. transcriptDisplayStore: hub defaults + the direct write (~345).
11. Web transcriptDisplay.ts becomes an adapter (~484, split if the local/viewport half can go first).
12. The transcript store's checkpointed editor (~150).
13. Native: transcriptMobile is a projection of the store (~10).
14-16. claude/sdk-d6-native splits along its own seams on top of 13 (draft backend ~310; nativePreferences onto the core ~520 with the gate extraction first; screens ~300). #1693 stacks on 16.

Each piece: CI + RoboRev clean + simplify, merged on green, next piece refreshed onto main. Tests from #1549 move with the piece that owns them; none are weakened.
