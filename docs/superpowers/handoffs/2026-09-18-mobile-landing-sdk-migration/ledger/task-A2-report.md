# Task A2 — Move `AppwireClientLike` out of `protocol/testing/`

**Status:** done. **Branch:** `claude/sdk-a2-client-like` (worktree
`.claude/worktrees/sdk-a2-client-like`), one commit `d161ffca9` on top of A1's
`30b8c585e`. Not pushed.

## What moved

`AppwireClientLike` now lives in `cmd/evener-hub/frontend/src/protocol/clientLike.ts`,
is listed in `tsconfig.build.json` `files`, and is re-exported from `index.ts`
as `export type`. `testing/fakeClient.ts` imports it from `../clientLike`. No
helper types moved with it: the interface references only `AppwireClient`,
`ConnectionState` and `TerminalReason`, all already in `client.ts`.
`RequestHandler`, `ConnectHandler` and `RecordedCall` belong to the fake and
stayed there.

## Importer counts rewritten

| App | Files |
| --- | --- |
| `cmd/evener-hub/frontend/src` | 25 |
| `mobile/src` | 0 |
| `mobile-native/src` | 0 |

The plan's "136 importers" counts every file importing anything from
`testing/fakeClient` (139 today, almost all `FakeClient` in tests). Only 25
files import the *type*, all web; neither native tree references it. Confirmed
after the rewrite: zero occurrences of `AppwireClientLike` sourced from
`testing/fakeClient` outside `protocol/testing/`, 25 from `protocol/clientLike`.

## RED

With the declaration deleted from `fakeClient.ts` and before the rewrite,
`tsc --noEmit --incremental false` in the web app reported **70 errors across
29 files**: 25 × TS2305 (`has no exported member 'AppwireClientLike'`, one per
importer) plus 45 cascade errors (27 TS7006 implicit-any, 12 TS18047, 4 TS18048,
1 TS2322, 1 TS2552). Restored, then landed properly.

## Gates

- `make test-web` — PASS (typecheck, unit tests, lint).
- `make test-native` — PASS (78 files / 737 tests, 7 files / 777 tests, `tsc`).
- `make test-api-package` — PASS. `clientLike` added to `shippedModules` (so the
  tarball-listing check requires `dist/clientLike.js` + `.d.ts`) and
  `AppwireClientLike` added to `typeExports`, so both the ESM and CommonJS
  declaration consumers now import the type from the installed package.
- `npx biome check --write` run on all 29 touched TS files (it re-sorted the
  new import into alphabetical position in 14 of them).

## Non-import diff

Six things beyond the mechanical rewrites, ~45 lines total:

1. New `clientLike.ts` — 11-line header comment plus the interface verbatim.
2. Deleted the 12-line declaration from `fakeClient.ts`; its header comment's
   "per AppwireClientLike below" became "(../clientLike.ts)".
3. `index.ts`: one `export type` line.
4. `tsconfig.build.json`: one `files` entry.
5. `qualify-package.mjs`: two list entries (`shippedModules`, `typeExports`).
6. **Drift check** — `client.test.ts` gains an 11-line `describe` that assigns
   `new AppwireClient(...)` to an `AppwireClientLike` and asserts the initial
   `state`/`terminalReason`. Eight of the ten members are declared by lookup
   (`AppwireClient["connect"]`), so a rename already fails at the declaration;
   `state` and `terminalReason` are hand-spelled and this is the only thing
   that catches them drifting. Constructing a client does not dial — `connect()`
   is explicit — so the test is inert.
7. `protocol/README.md`: the export-list sentence names the seam (paragraph
   reflowed).

## Concerns

- `clientLike.ts` is a type-only module, so it contributes no runtime export
  and has no smoke call in the qualification runner, unlike every other entry
  in `shippedModules`. The comment above `smokeCalls` says "one call per shipped
  module"; that is now true of every module that has a runtime half. I did not
  reword it — tell me if you want it adjusted.
- Nothing else. The type is structural, the rewrite is complete by grep, and all
  three gates are green.
