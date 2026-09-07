# iOS reader continuity evidence

## Observed 6 September 2026, Pacific time

The iPhone 17 Pro simulator (iOS 26.5, device
`F9170898-2B92-4420-BD12-9B54B3FC4AE0`) ran a Release build from the authoritative
integration worktree. The final build in this record was produced over
`f0aeb8e6d` plus the reader changes committed with this document. It is scoped
reader evidence, not final release acceptance.

Final JavaScript bundle SHA-256:
`d6d70cd71bc21a49c3934c027656cac7501ca7d21618cbfc39fc4b3cef2b108b`.
Reader source fingerprints at build/test time:

- `screens.tsx`: `320a5da696281122b30cee242866144af740475d39ecbf07086ce4e28339c830`
- `readerPosition.ts`: `843f1fdce9c939800e9e14c78c7a2730c069d2ad86efd9cd460985eeb7e454f7`

The direct authenticated owned v4 hub on port 54211 used a scripted provider.
A fresh session named **Reader continuity fixture**, ref
`local:034KVwAjXBf0IyAcKKEYU3`, contains 30 separately started/interrupted turns
with varied-length text. Creation and turn requests were issued once, with
instance checks and completion notification barriers. The initial native
40-item window begins at reader marker 11; older history is available.

## Executed journeys

1. Scroll into marker 11 with marker 12 below it, return to Sessions and reopen.
   The same content and within-message offset returned. The first before/after
   images predate the additional header-drag fix; they prove only this journey.
2. Pull toward the top to reach Load older messages. The initial reader code
   snapped the first message back to the viewport top after the gesture and hid
   the header. Recording the user-selected geometry as already applied fixed
   the issue; the rebuilt app kept the header reachable.
3. Load older messages. After page insertion the app retained marker 11 rather
   than jumping to marker 1. The snapshot briefly did not settle; a fresh
   snapshot and screenshot confirmed the settled result.
4. Scroll into the newly loaded history, leaving marker 10 below the tail of
   marker 09. Return to Sessions and reopen. The app loaded the earlier page
   again and restored that reading position (approximately one screenshot pixel
   of vertical difference). This exercises an anchor outside the initial page.

![Before returning](assets/reader-continuity/reader-before.jpg)
![After returning](assets/reader-continuity/reader-after.jpg)
![After older history was inserted](assets/reader-continuity/after-prepend.jpg)
![Older-page position before leaving](assets/reader-continuity/older-before.jpg)
![Older-page position after reopening](assets/reader-continuity/older-after.jpg)

## Deterministic and integration checks

Twelve reader-position tests cover measured offsets, exact protocol identity,
missing measurements, older-window resolution, persistence isolation, failed
reads/writes, cached newer positions and storage bounds. `make test-native`
passed 385 tests across 51 files plus typecheck after integration. The full
`make merge-approval-gate` also passed during this continuation; concurrent
uncommitted work means that run is integration evidence, not a final exact-head
release certification.

## Still required

Exercise long heterogeneous Markdown/images, image reflow, streaming while
reading, background/process death, hub switching with overlapping refs, sheet
return, largest Dynamic Type, iPad/landscape, VoiceOver and physical-device
performance. Verify bounded virtualization recovery when a target is far from
mounted variable-height rows. Header space is not itself persisted as a
semantic transcript item. The current screenshots do not qualify these cases.
Android delivery remains deferred beyond v1 by Jesse's explicit decision.

## Dynamic Type follow-up, 6 September 2026

The later Release build with hub settings (bundle
`b786cdbe7d203332ba31a74a3a30385ef568e06a055e90f99902a3b5fc1bc01a`)
showed a real failure when returning from
`accessibility-extra-extra-extra-large` to `large`: the visible transcript
moved to reader marker 05. The saved semantic anchor still identified the
interruption after marker 09. This case is **open**, not accepted.

![Observed drift after reducing text size](assets/reader-continuity/size-change-drift.jpg)

Subsequent builds with temporary diagnostic probes did not reproduce the drift
in the same font-size round trips. One probe recorded the same anchor with
within-item offset 26.667 points, its row at y=4874/height=52 at ordinary size
and y=37178/height=92.332 at the largest size. The corresponding observed scroll
offsets were 4900.667 and 37204.667, matching those particular restorations.
These successful runs do not explain or invalidate the earlier failure.

The probes were removed from source and a clean Release build was installed.
Continue with a reproducible uninstrumented case and event-order evidence.
Inspect automatic scroll offset changes, virtualized cell measurements and
restoration suppression before choosing a fix. Do not treat an inferred race
mechanism as a demonstrated root cause. The simulator's content size was
restored to `large`.

## Reproduced round-trip drift, 7 September 2026

The clean Release used for shortcut acceptance reproduces the defect. Its
installed JavaScript bundle is
`b0d1e4067fda6e6e5b63ffa3290861f64d0b18658c6134addc42dd4d292cdb64`,
from native source `d65d234f5`; subsequent `93e817cd3` and `fda2b9c53` do not
change native code. Device: iPhone 17 Pro simulator, iOS 26.5. Source hashes:

- `screens.tsx`: `121f94a6980b4ee4a72424aaaac1e707ba3f0745d3eff3fe1120fe0f6e4a8488`
- `readerPosition.ts`: `4e473319feaa00736417c65595562c53cf6fab52d1a2875917f15278b39f2bdf`

With the same 30-turn fixture open at ordinary `large` text size, scroll upward
to Load older messages, load the earlier page, and scroll to the end of marker
09 with marker 10 below. The persisted anchor is the user message
`apptranscript-item-v1:turn_m9:9:0`, position `{entry: 9, item: 0}`, within-item
offset 332 points. This differs from the interruption-row anchor in the earlier
failure.

Changing to `accessibility-extra-extra-extra-large` keeps marker 09 visible.
Returning to `large` moves the settled viewport to marker 16 with marker 17
below. The accessibility snapshot also contains marker 15 near the viewport.
The SQLite anchor remains exactly marker 09 / offset 332 / the same touch time
through both transitions. No drag, send or session mutation occurs during the
round trip. The final screenshot records the failed ordinary-size state.

![Ordinary size before the round trip](assets/reader-continuity/size-roundtrip-before-20260907.jpg)
![Ordinary size after the round trip](assets/reader-continuity/size-roundtrip-drift-20260907.jpg)

This proves a rendered-position mismatch with retained semantic identity. It
does not identify the cause. The next diagnostic must distinguish native
content-size clamping, stale virtualized measurements and a queued approximate
restore running after a newer exact restore. Capture bounded in-memory event
order and flush outside the layout/scroll callbacks so logging does not mask
the failure. The simulator is back at `large`; the defect remains open.
