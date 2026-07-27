# SDD ledger — plan: docs/superpowers/plans/2026-07-27-r0yf-favorite-revalidation-implementation.md

Approved by Jesse on 2026-07-27.
Branch baseline after controller merge and implementation-plan commit: `defb8e336`.

Task 1: complete — implementation `defb8e336..ee5844e0f`; scoped re-review passed spec and quality with no findings.
Task 2: complete — implementation `ee5844e0f..12f548868`; final scoped re-review passed spec and quality with no findings.
Task 3: complete — implementation `040240d04..a46cfbadc`; the first scoped review rejected a stale tree/Past snapshot race, the deterministic correction was added, and a fresh scoped re-review passed spec and quality with no findings.

Fresh whole-branch review at `7349c9ea3`: REJECTED with two Important
findings. Root cause 1 was loading InputsVersion after navigation/archive reads
and returning live/authority from the request snapshot beside a cache that only
stored Tree and AttentionSummary; this allowed old data under a new key and
old-tree/new-authority results during Past/Roster publish-before-bump gaps.
Root cause 2 was last-row-wins project/source aggregation, including empty IDs
being treated as complete local evidence. RED was proven by the deterministic
memo interleaving and project-claim tests: the old-tree/new-key, rebuild-gap,
reversed malformed-row, and empty-ID cases failed as specified. Code/tests
correction `6958ff887` captures the version first, caches one composite
Tree/Attention/live/authority generation, and monotonically aggregates
complete/incomplete/ambiguous project claims. GREEN evidence: new memo races
`-race -count=20`, project aggregation `-count=20`, existing concurrent cache
race `-race -count=10`, TreeFavorite `-count=5`, hubcore Favorite/TreeCache/
Remote `-count=5`, full hubcore, full cmd/serf-hub, vet, lint, and diff check
all passed; exact evidence is recorded in `task-2-report.md`.
