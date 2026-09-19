# A4 brief (dispatch once #1241 (A3) has merged; base on the main that contains it)

You are the implementer for SDK migration row A4 in the evener repo: rewrite every deep relative
import of the AppWire TypeScript package to the package name and delete A3's re-export seam. Step 0:
from /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/issue-1116-merge-review-1fc3f3 run
`git fetch origin main` and `git worktree add /Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/sdk-a4-package-imports -b claude/sdk-a4-package-imports origin/main`,
then work ONLY there. Never cd to the main checkout. Never bare `git stash`. Never `--no-verify`.
You report to the coordinator; the human is Jesse.

Read first, in this order:
1. `.superpowers/sdd/2026-09-12-mobile-landing-queue-cont/task-A3-report.md` and `a3-scout-report.md`
   (coordinator worktree): what A3 did and measured.
2. The A4 row, the "Temporary seams" section and the "Where test support lives" section of the plan,
   which is on main since #1182 merged (8eb1a94c8): `docs/superpowers/plans/2026-09-12-sdk-migration.md`,
   with the inventory beside it at `docs/design/2026-09-12-sdk-migration-inventory.md`.
3. Repo AGENTS.md / CLAUDE.md, cmd/evener-hub/frontend/AGENTS.md, and the package's package.json,
   README.md, scripts/qualify-package.mjs at `appwire-client/typescript/`.

Facts A3 established (verify each on your base before relying on it):
- The package lives at `appwire-client/typescript/`. Name resolution already exists: web
  `cmd/evener-hub/frontend/tsconfig.json` `paths` and `vite.config.ts` `resolve.alias` map
  `@evener/appwire-client`, `@evener/appwire-client/docContent` and `@evener/appwire-client/testing/*`;
  `mobile-native/tsconfig.check.json` `paths` maps the same three; `mobile-native/metro.config.js`
  resolves them through `resolveRequest`. `mobile-native/tsconfig.json` has no `paths` (tsx reads none).
- A3's seam: `cmd/evener-hub/frontend/src/protocol/` holds 27 one-line re-export stubs plus a `testing/`
  directory of stubs, all headed "Temporary re-export seam for SDK migration row A3". The web tree
  still imports through them (~690 statements at five depths). The mobile trees were sed'd to the new
  RELATIVE path (`../../appwire-client/typescript/...`, ~130 mobile-native + ~22 mobile/src lines).
- `mobile-native/scripts/*.mts` (six lines) keep RELATIVE imports by decision (tsx reads no `paths`);
  A4 does not touch them, and the zero-old-path grep must still find no `src/protocol` in them.
- Two package test files import the web app (#1242); leave them as A3 left them unless the rewrite
  forces a change, and say so.

Task, as reviewable commits:
1. Web: rewrite every `.../protocol/<module>` import (source and test, `vi.mock` paths, `import type`)
   to `@evener/appwire-client` (root exports), `@evener/appwire-client/docContent` (the one subpath)
   or `@evener/appwire-client/testing/<x>` (test support, never a runtime import). Use a script
   (checked in under `scripts/` with a name and help text, per the repo's automation preference) that
   rewrites by the resolved module and symbol, not by string replacement of prefixes, and prints the
   count per specifier. Symbols that are not root exports must not be imported from the root: if the
   rewrite finds a deep import of a module or symbol the package does not publish, STOP and report it
   (that is a missing A3b-style export, not something to paper over with a deep path).
2. mobile-native and mobile/src: rewrite the relative `appwire-client/typescript/...` imports the same
   way, with the same script.
3. Delete `cmd/evener-hub/frontend/src/protocol/` entirely (stubs and `testing/`), and the biome/vitest
   ignore or include entries that named it.
4. Acceptance: a grep for `src/protocol/` and `appwire-client/typescript/` in import specifiers across
   `cmd/evener-hub/frontend/src`, `mobile-native`, `mobile/src` returns ZERO lines, except the six
   `mobile-native/scripts/*.mts` relative lines (which must not contain `src/protocol`); wire that grep
   into `make lint` (or the existing lint-repository target) so it cannot regress silently.
5. Runtime resolution proof: add `resolve-check.mjs` to the qualification runner's packed consumer
   (plain `.mjs`, run with `node`), importing by name every runtime value the native tree takes from
   the package (root 27 and `./docContent` 2 as measured at eeff54b70; re-derive on your base) and
   asserting the list equals the grep that produces it, per the plan's A4 row.
6. Docs: the AGENTS.md and README sentences that describe imports (deep path vs package name), and
   `docs/web-ui` active citations that spell `src/protocol/`.

Gates (all must pass; report each): `make test-api-package`, `make test-web` (typecheck/test/lint,
including A3's test-file-count assertion), `make test-web-browser` (all five guards), `make test-native`,
`make lint` incl. `lint-generated`, `WEB=0 make test` (both audits), `make build-web`. Biome ONLY over
cmd/evener-hub/frontend/src and the package; NEVER over mobile/ or mobile-native/. Fixtures and cleanup
only within your own process group; no `pkill -f`; no kills by numbers scraped from a global `ps`.
#1244 (Metro bundle gate) is a DEPENDENCY of A4 per the plan: `make test-native-bundle` (from the
#1244 PR) must exist on your base and pass after your rewrite; run it and report its result. If it is
not on main yet, stop and say so rather than checking by hand.

Push with `git push -u origin claude/sdk-a4-package-imports`; `gh pr create` (NOT draft) titled
`refactor(sdk): import the AppWire package by name and delete the protocol/ seam (SDK A4)`; body: counts
per specifier per tree, the script's name, what was deleted, the acceptance grep and its make target,
the resolve-check fixture, gates, and "Plan row: A4 in docs/superpowers/plans/2026-09-12-sdk-migration.md
(#1182); follows A3 (#1241)". Do not edit the plan docs.

Write `.superpowers/sdd/2026-09-12-mobile-landing-queue-cont/task-A4-report.md` (coordinator worktree)
and reply with: status, commit SHAs, pushed head, PR number, counts per specifier per tree, gate results,
anything the A3 report or this brief got wrong, and concerns. Under 30 lines. If the rewrite finds an
unpublished symbol or needs a logic change, stop and reply NEEDS_CONTEXT with the list.
