# Task C15 — move catalogCommands into the protocol package

Status: DONE. PR #1230 (non-draft), branch `claude/sdk-c15-catalog-commands`.
Commit: `20ed3f884` (single commit). Pushed head: `20ed3f884`.
Base: `origin/main` `2b1e02939`; main had not moved at push time, no merge.

## What landed
- `git mv` of `shell/palette/catalogCommands.ts` to `protocol/catalogCommands.ts`.
  Inside the file: `./types.gen`, plus one stale comment word (see below). No
  test file exists for it; `shell/palette/commands.edge.test.ts` stays put as
  the oracle with its specifier repointed.
- Plumbing: `tsconfig.build.json` `files`; `index.ts` re-export of both
  functions, alphabetical between attachmentMarkers and client (no `rootTypes` —
  the module declares no exported type); `qualify-package.mjs` `shippedModules`
  + two `rootValues` + two real root smoke calls (plugin command qualifying to
  `/acme:plan`; the same command filtered out against an empty active set). No
  `\n` in the smoke inputs, so C26's template-literal trap did not bite.
- Importers: web slashCompletion.ts, Spawn.tsx, commands.ts, commands.edge.test.ts;
  native `mobile-native/src/commandCatalog.ts`. Deep relative paths matching
  neighbours. Brief's list was exactly right; repo-wide grep over web, mobile/,
  mobile-native/, test/scenarios, docs/web-ui found no other live reference —
  no vi.mock, no prose citation, no scenario card. Only hit left is
  `docs/superpowers/plans/2026-08-04-...md`, a dated record (not swept, per A3).
- `protocol/README.md` export prose gained the module.

## Falsification
Two-step per #1224: `shippedModules` kept, `index.ts` re-export and both
`rootValues` names deleted → `shipped module unreachable from every published
specifier: catalogCommands`. Restored; green.

## Gates
test-api-package PASS · test-web PASS (typecheck/test/lint) · test-native PASS
(777 tests + tsc) · test-web-browser PASS (5 guards) ·
`go test -run TestScenarioSource .` PASS. Biome over the six touched
`cmd/evener-hub/frontend/src` files only; it sorted four import lines.

## Done differently, and why
1. Fixed one comment clause: "Shared verbatim by catalogCommands below" pointed
   at the `catalogCommands()` function in `shell/palette/commands.ts`, which was
   never in this file and is now in another directory. Now "the palette's
   catalogCommands", with the following line's "the palette's activateCommand"
   shortened to "its" to avoid the repeat. Left the second comment's "Keep this
   filter at the palette boundary" alone: it says where the filter is applied
   (not in the store), not where the file lives, and that is still true.
2. `mobile-native/src/commandCatalog.ts`'s two protocol imports were hand-sorted
   (catalogCommands before errors) to keep the file's existing sorted order —
   biome must never run over mobile-native.
3. README's added clause reflowed the paragraph tail at width 78 (its existing
   width), so the diff is two lines.
