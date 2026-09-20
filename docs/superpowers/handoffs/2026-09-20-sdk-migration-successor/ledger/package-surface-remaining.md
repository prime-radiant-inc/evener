# Remaining A1 package surface work

Inspected P9 98cb711 and P10c c660aee. Predecessors already own these exports/build entries:
- P8: settingsHubGeneration in root index and tsconfig build.
- P9: checkpointedDraftEditor in root index and tsconfig build.
- P10b: transcriptDisplayStore in root index and tsconfig build.

Do not redo those exports as a new feature. The retained A1 completion obligation is
packed external-consumer behavior qualification of the assembled surface. Current
appwire-client/typescript/scripts/qualify-package.mjs references transcript display config utilities but has no
createTranscriptDisplayStore / settings generation / checkpointed editor usage.
After P9 and P10 package slices land, add minimal real installed-package smoke behavior
for these public entries and run qualification plus consumer-value-import checks.
Use the existing qualification harness; no new test framework or scripts. No change to
package runtime dependencies. Do not claim A1 complete based solely on barrel exports.
