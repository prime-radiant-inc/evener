# A3 brief (dispatch once #1234 lands; main must contain C5, C9, C11a/b, C12, C15)

You are the implementer for SDK migration row A3 in the evener repo: move the TypeScript
package from `cmd/evener-hub/frontend/src/protocol/` to a new top-level
`appwire-client/typescript/` (Jesse's ruling 2026-09-13, decision 3: "typescript should be in
the path"). Step 0: from /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/issue-1116-merge-review-1fc3f3
run `git fetch origin main` and `git worktree add /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/sdk-a3-package-move -b claude/sdk-a3-package-move origin/main`,
then work ONLY there. Never cd to the main checkout. Never bare `git stash`. Never `--no-verify`.
You report to the coordinator; the human is Jesse.

Read first, in this order:
1. `.superpowers/sdd/2026-09-12-mobile-landing-queue-cont/a3-scout-report.md` (coordinator worktree) — the
   verified checklist: 9 build/tool config files, 2 make targets, 3 CI lines, 5 Go files with functional
   path strings (+6 comment-only), 12 audit-enforced scenario-card citations across 8 cards, 847 import
   sites (689 web at five depths, 130 mobile-native, 22 mobile/src, 6 `.mts`), nothing resolving
   `@evener/appwire-client` by name today, the hybrid seam recommendation, and three risks
   (vite `fs.allow`, the package's 25 test files outside the vitest root, biome scope + coverage floor).
2. The A3 and A4 rows and the "Temporary seams" section of the plan:
   `git fetch origin refs/pull/1182/head` then `git show FETCH_HEAD:docs/superpowers/plans/2026-09-12-sdk-migration.md`.
3. Repo AGENTS.md / CLAUDE.md, cmd/evener-hub/frontend/AGENTS.md, the package's package.json,
   tsconfig.build.json, scripts/qualify-package.mjs, README.md.

Rules:
- ONE PR, but commit in reviewable steps: (1) `git mv` the directory; (2) build/tool config + make + CI;
  (3) the hybrid seam — re-export stubs at the old `protocol/` path for every module the web imports
  (deleted by A4), three seds for mobile-native / mobile/src / `.mts` (their paths get shorter; the six
  `.mts` lines are named in the plan's A3 row); (4) Go path strings, scenario cards, docs, AGENTS.md
  biome sentence; (5) the three risk fixes. No logic changes anywhere.
- Every gate that exists today must pass at the end: `make test-api-package`, `make test-web`
  (typecheck/test/lint), `make test-native`, `make test-web-browser` (all five guards — this is where
  the `fs.allow` risk bites), `make lint-generated`, `go test ./...` at the root for the scenario
  citation audit and the makefile-targets audit, and the CI workflows must still reference paths that
  exist. If a gate's own definition must move (biome scope, vitest include, coverage denominator),
  change the definition in the same commit as the move and say what the new baseline is.
- The package's 25 test files must still RUN (prove it: vitest reports them by count before and after).
  Decide and state where their dev dependencies come from — a vitest `include` plus resolver from the
  web root, or the package's own devDependencies + vitest config; pick the one that keeps the
  qualification runner's offline tarball install unchanged.
- Biome: the package must stay linted; extend the scope explicitly (NEVER run biome over mobile/ or
  mobile-native/).
- Coverage: if the web floor (`web 96.1`) moves because ~45 well-tested files leave the denominator,
  re-baseline it in the same PR with the measured number and say so.
- `#1224` (runner passes silently when a dist module is missing from shippedModules) stays out of
  scope; do not touch the manifest semantics.
- Known agent-hygiene rules: fixtures and cleanup only within your own process group; no `pkill -f`;
  no kills by numbers scraped from a global `ps`.

Push with `git push -u origin claude/sdk-a3-package-move`; `gh pr create` (NOT draft) titled
`refactor(sdk): move the AppWire TypeScript package to appwire-client/typescript (SDK A3)`; body lists
each commit's scope, the seam mechanism and stub count, the three risk fixes with before/after
evidence, gates, and "Plan row: A3 in docs/superpowers/plans/2026-09-12-sdk-migration.md (#1182);
decision 3 settled 2026-09-13". Do not edit the plan docs.

Write `.superpowers/sdd/2026-09-12-mobile-landing-queue-cont/task-A3-report.md` (coordinator
worktree) and reply with: status, commit SHAs, pushed head, PR number, gate results including the
test-file counts and the five browser guards, anything the scout report got wrong, and concerns.
Under 30 lines. If any step turns out to need a logic change or exceeds the scout's sizing by more
than half, stop and reply NEEDS_CONTEXT with the specifics.
