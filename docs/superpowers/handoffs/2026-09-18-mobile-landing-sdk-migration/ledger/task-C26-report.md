# Task C26 — move the message display formatters into the protocol package

Status: DONE. PR #1226 (non-draft), branch `claude/sdk-c26-message-format`.
Commit: `0cfc402d8` (single commit). Pushed head: `0cfc402d8`.
Base: `origin/main` `e2c77cc72`; main had not moved at push time, no merge needed.

## What landed
- `git mv` of `panes/session/transcript/messages/format.{ts,test.ts}` to
  `protocol/displayFormat.{ts,test.ts}`. Brief's name kept: it states the domain
  (counts, durations, clock times, first lines formatted for display) and
  `format` is unpublishable at a package root. Test is byte-identical but for
  its specifier.
- Plumbing: `tsconfig.build.json` `files`; `index.ts` re-export of all nine
  functions, alphabetical between clientLike and docContent (no `rootTypes` —
  the module declares no exported type); `qualify-package.mjs` `shippedModules`
  + nine `rootValues` + nine real root smoke calls.
- 9 web importers + 1 native importer rewritten to deep relative paths.
  4 prose citations of the old path repointed (chrome/activityFormat.ts,
  chrome/statusFormat.{ts,test.ts}, widgets/timestamp/index.tsx).
  `protocol/README.md` export prose gained the module.

## Falsification
Per #1224: manifest entry present, `index.ts` re-export deleted →
`shipped module unreachable from every published specifier: displayFormat`.
Restored; green.

## Gates
test-api-package PASS · test-web PASS (typecheck/test/lint) · test-native PASS
(777 tests + tsc) · test-web-browser PASS (5 guards) ·
`go test -run TestScenarioSource .` PASS. Biome over touched
`cmd/evener-hub/frontend/src` files only; it sorted rewritten imports and split
`turnMeta.ts`'s header comment from its imports with a blank line.

## Native importer — the brief's open question
The plan row is correct. `mobile-native/src/ActivityDelegateDetails.tsx:13`
imports `formatElapsed` and `splitMandate` through
`../../cmd/evener-hub/frontend/src/panes/session/transcript/messages/format`.
It is a multi-line import, so the specifier sits alone on line 13 and a grep
anchored on the symbol names or on the import keyword misses it. One site, as
the plan says.

## Done differently, and why
1. Rewrote the file's opening comment. It read "shared by transcript
   renderers", naming the directory the file has left, and it was already false
   for `chrome/DetailsPanel.tsx` and for native. Now "shared by the web and
   native renderers". Nothing else in the file changed.
2. First smoke-call draft failed the gate with `SyntaxError: Invalid or
   unexpected token`: `rootSmokeCalls` is a JS template literal, so a `\n`
   written there becomes a real newline in the generated consumer program and
   breaks the string it sits inside. Escape as `\\n`. Worth knowing for any
   later row whose smoke input needs a newline.
3. No scenario card cites this file, so the citation audit was never at risk;
   ran it anyway. Dated `docs/superpowers/plans/` records left alone per A3.
