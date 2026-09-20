The first turn can finish while its background session namer still holds an autonomous retirement blocker. Test callers that expect a fully settled session now wait for the existing sender wait group before claiming retirement, through one shared helper.

A regression exercises the real session, scripted namer, and retirement controller inside `testing/synctest`. It proves the claim stays blocked while the namer is held, then succeeds after release. Removing only the helper wait produces the expected autonomous-blocker failure. This PR changes one test file and no production behavior.

Validation at `d10c70c4c400e63d3db427db56f4df7acbf526db`:
- Focused normal, race, and evenerfuzz-tagged regression tests pass; the test deadline audit passes.
- Normal/tagged/Windows vet, Go formatting, and golangci-lint pass after merging current main.
- Independent spec, quality, and simplification review passed.
- Local `roborev review --branch --wait --base origin/main` passed with no findings (job 2537).

Current-head CI and the raw remote review panel remain required before merge. The separate `TestRetirementTreeSettleDrainsPendingRootAttention` failure remains tracked in #1879; this PR does not close that issue.


Merge gate at `d10c70c4c400e63d3db427db56f4df7acbf526db`: all15 current-head checks passed. Raw panel members Luna, Muse, and DeepSeek each report no issues. The GLM member produced no review because its provider exhausted HTTP429 retries; that unavailable member is recorded, not counted as a pass. Independent review/simplification and local branch review2537 passed.
