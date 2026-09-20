== panel 92cc5176-c19f-499b-ab4c-a1af895565e1 head e2b62bb85 outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=22731 verdict=1 chars=165
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=22732 verdict=1 chars=119
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=22733 verdict=1 chars=1406

######## member 0 (codex default)
No issues found.

Summary: Generation replacement now retires pending reads while preserving cached defaults, with deterministic coverage for the late-response case.

######## member 1 (codex default)
No issues found.
Summary: Settles pending transcript reads on generation replacement while preserving cached defaults.


######## member 2 (pi default)
## Summary

The commit fixes the shared SDK transcript-display store so that a **direct** ready-generation replacement (`beginReadyGeneration()` called while a generation is already active) fully retires the previous generation's in-flight state instead of merely flipping `loaded` to `false`. Previously, if a refresh was pending when the generation was replaced, `hubLoading` stayed stuck `true` forever because the late reply is correctly fenced out by the generation check and never runs the code that clears the loading flag.

The new implementation captures whether a generation is being replaced (`fence.generation >= 0`) before calling the shared `beginReadyGenerationCore()`, then calls the store's `retirePayload()`, which supersedes in-flight reads/writes and publishes `loaded: false`, `hubLoading: false` while **retaining** the cached `hub` defaults (the deliberate behavior the new test asserts). A first-ever generation (`generation === -1`) does not retire, preserving initial-start behavior. The added test exercises the exact failure mode end-to-end: a pending refresh is abandoned, loading settles, and the late reply with higher revisions lands nothing while cached defaults remain.

The change is scoped to the wrapper; the shared generation core stays intact, and the disposed/first-begin paths are unaffected because `dispose()`/`end()` leave `generation` at `-1`.

No issues found.
