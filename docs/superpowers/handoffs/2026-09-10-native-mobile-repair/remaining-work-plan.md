# Remaining branch landing plan — 10 September 2026

Source remains live-concepts-plan2-integrate at f6614d6cc. Preserve original work, protected Apple edits and existing evidence. Current six PRs remain governed by current-head CI plus an actual clean current-head RoboRev verdict. Jesse authorizes additional small PRs and conditional merges.

Ruling: Exclude the Tauri application, native plugin/build integration and concept-app runtime from landing — Jesse explicitly said we do not want the Tauri app. Preserve the source archive; do not delete it as part of extraction. Shared framework-independent modules already used by the React Native app remain supported even when their path begins mobile/.
Ruling: Curate design sketches as design documentation, with provenance and historical labels. Do not import the executable Tauri app merely to retain visual studies. Jesse expressed tentative interest, so prepare a small useful selection and preserve the rest rather than discard it.

| Task | Input / output | Ownership and overlap ruling |
|---|---|---|
| Fork capability correction | Compare existing original fix to main; minimal behavior and regression | Isolated hub candidate; root owns startup-ordering work and shared integration |
| SDK discovery examples | Existing SDK landing sequence first read-only recipe batch | Own protocol example/package qualification paths in isolated candidate; no higher-level state extraction |
| Design sketches | Read-only source research, curated static assets and index | Own docs/design/mobile only in isolated candidate; no Tauri/runtime/config files |
| Existing reviews | Close #1091/#1096/#1098/#1100; land main prerequisites | Root serializes all shared refs and merge decisions |
| Residual audit | Classify every meaningful remaining source change as landed, superseded, next PR, or excluded | Root maintains inventory; path absence alone is not proof a feature is missing |
| Distribution | Main-based CI on #1039 then actual signed build and internal delivery | Root alone owns Apple, credentials, simulator and owned backend state |

The new tasks extract existing designed behavior. No new product architecture is introduced. Tests follow docs/developing-evener/testing.md and exercise behavioral contracts; use real package-installed examples and scripted external boundaries. Root reviews worker claims and commits; workers do not push or merge. New baseline is main2664cc881. Preserve updated main behavior instead of copying entire files from the old branch.

## Concrete residual dispositions

- Static sketches: PR #1104, head 7bf30e12d, twelve inspected browser captures plus provenance. Excludes the executable concept lab and all Tauri application/plugin/package files.
- Fork capability: candidate 92d237e7e in mobile-land-fork-capability. Package tests/vet/lint passed; independent review is checking whether closed subagent capability stamping bypasses the exclusion. Not published yet.
- SDK discovery: Luna implementing the first installed-package read-only recipe batch in mobile-land-sdk-discovery.
- Shutdown boundary: original source adds CloseForShutdown and calls it from serve shutdown, because an earlier idle SessionEnd otherwise suppresses the final closed boundary. This is a separate remaining behavior, not covered by the current six PRs. Candidate will preserve main's newer close cascade and notification ownership fixes.
- Round timing replay: owner-only correction remains insufficient. Explicit compaction emits two transient ContextCompaction events before durable checkpoint/summary/steering, so live timing8:8 differs from replay12:0. A mere group rejoin yields8:3. Do not publish or qualify the owner-only fix as resolved; investigate a durable presentation record or consistent coordinate design before changing schema/grouping.
- Hub startup ordering: original creation relay admission/announcement buffering is still a separate unlanded slice; do not mix it into fork capabilities.

## Parallel PR shepherding — Jesse clarified 10 September

- Rawls (Luna medium): #1100 compaction persistence and model-history exclusions. Root integrates the completed projection/index changes and full-sequence regression.
- Mendel (Luna medium): #1091 activity, #1096 native, #1098 environment; current-head review and CI findings, focused behavioral fixes in isolated PR worktrees.
- Carson (Luna medium): #1105 fork, #1106 project deletion, #1108 installed SDK discovery, #1109 shutdown; current-head findings and focused fixes in isolated PR worktrees.
- Root: integration, independent validation, PR metadata and exact-head merge gates; #1039 TestFlight and #1102 documentation after native prerequisites. No worker pushes or merges. Reassign idle capacity to actionable findings across the queue.

#1104 is merged at eae0750534f625a69acbe5e549b08b3329e6960a. #1105 full canonical local gate passed at f17d16e85143a9c648dc64bb1da40acc0f93bd6f. Published #1106/#1108/#1109 carry independent review and behavioral validation; actual current-head CI and RoboRev still determine landing. Source tree, unfinished Apple edits and stash remain preserved.
