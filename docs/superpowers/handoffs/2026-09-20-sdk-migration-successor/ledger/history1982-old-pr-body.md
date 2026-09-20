An older standalone tool result can overwrite a newer completed tool call when history fragments are folded together. The private fold now retains source ownership: fresh result fields beat older results regardless of traversal order, fresh call fields beat older result fields, and undefined fields fall back to the other half. Same-source folding keeps its existing behavior.

Stacked on #1961 and #1968. This prerequisite is separated from the upcoming public history-coverage export; no public API/model change or mobile consumer is included. The own production diff is 46 changed lines. Current-head CI and complete raw reviewer members remain required.

Validation: six route-level regressions exercise mergeOlderItemPage, including both source directions, defined/undefined fields, and ordering. All 239 reducer tests pass; production-only reversal fails the two new precedence bugs. Package build/qualification, native typecheck, package Biome/import lint, and diff checks pass. Independent spec/correctness/simplify and local RoboRev2610 found no actionable issues at `abf021bc1`. Merge-only refresh to main `d25d5afaa` produced `032c03025` with the complete owned patch byte-identical.

