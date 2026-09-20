Warning normalization now reuses bounded fields without repeated scans and retains distinct parameter keys that share a truncated prefix. The shared steering predicate, ask resolution, and warning fixtures remove duplication across the package, web, and mobile consumers. The ask-dock fixture uses the accepted `*TestUtils.*` support-file name, allowing the package boundary checker to stay strict.

This completes the #1862 follow-ups. The scan-count regression fails when the production reuse is removed; the fixture rename fixes the original web CI boundary failure.

Validation at `08fa72ce4582b1a304d4d4ae53fe979c6fc65e0c` after integrating main:
- 445 affected reducer/frontend tests, 464 shared mobile tests, and 76 ask-derivation/transcript tests pass.
- Package qualification, native and web typechecks, frontend/package Biome, and package-import/boundary checks pass.
- Independent spec, quality, and full-PR simplification review passed.
- Local RoboRev branch review passed with no findings (job 2542).

Current-head CI and raw remote panel review remain required before merge.


Merge gate at `08fa72ce4582b1a304d4d4ae53fe979c6fc65e0c`: all15 current-head checks passed. All four raw member reviews were read. Luna, Muse, and DeepSeek report no issues; GLM reports only two Low test-cleanup items, tracked in #1939 for a separate follow-up PR. Independent review/simplification and local branch review2542 passed.
