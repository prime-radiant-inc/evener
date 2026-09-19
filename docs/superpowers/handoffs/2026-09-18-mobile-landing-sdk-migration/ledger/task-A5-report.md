# Task A5 — Delete dead native conversation fixtures, typecheck all of mobile/src

**Status:** done, committed, not pushed. **SHAs:** `f3b25ea2f` (delete),
`0ef79b451` (include) on `claude/sdk-a5-dead-fixtures-1183` (off `92561dbe3`),
worktree `.claude/worktrees/sdk-a5-dead-fixtures`. Worktree had no
`node_modules`; ran `npm ci` in `mobile-native` first.

**Dead-proof grep** (`conversationFixtures`, repo-wide, node_modules excluded):
no hits — zero importers anywhere. Two of its four type imports are dangling:
`../services/nativeProfiles` (no `.ts`/`.tsx`/index anywhere) and
`../state/connection` (same). The other two (`../conversation/model`,
`../services/activity`) do resolve.

**RED (before include):** restored the 774-line file from git, ran
`npm run check` in `mobile-native` — exit 0, clean, file never reached.
Removed it again.

**GREEN (after include):** with `conversationFixtures.ts` still absent (real
state), `npm run check` stayed green. Restoring the same file with the
include in place produced the expected `TS2307: Cannot find module
'../services/nativeProfiles'` / `'../state/connection'` failures, then
removed it again for good.

**Include added** to `mobile-native/tsconfig.check.json`:
`"include": ["**/*", "../mobile/src"]` — `"**/*"` reproduces the previous
implicit default (everything under `mobile-native`, so `scripts/*.mts`,
`plugins/*.js`, `App.tsx`, etc. stay covered) and `"../mobile/src"` is the new
addition, covering every shared file whether or not `mobile-native` imports it.

**Surfaced by the include** — seven `mobile/src/**/*.test.ts` files never
reached by this program before (vitest runs them; nothing in `mobile-native`
imports them):
1. All seven failed `TS2307: Cannot find module 'vitest'` — plain node
   resolution walks up from `mobile/src/...` and never reaches
   `mobile-native/node_modules`. Fixed with a `"vitest"` path mapping in
   `tsconfig.check.json`, same pattern as the existing `anser`/`react`/
   `tinykeys`/`zustand` entries. This alone also cleared ~25 downstream
   `TS7006`/`TS7031` implicit-any errors (vitest's mock typings failing to
   resolve had been erasing contextual types for `.mockImplementation`
   callbacks).
2. `mobile/src/state/conversation.test.ts` had 3 fixture literals
   (`outputImages: [{ id: "new", url: ... }]`) with a stray `id` field
   `OutputImage` doesn't have, missing the type's required `source` field —
   changed `id` to `source`.
3. Same file, 2 `Array#find` calls read `.items` off the result without a
   type-guard predicate, so TS kept the full `MobileTimelineItem` union
   (`Property 'items' does not exist on type ... "user" ...`) — added
   `(item): item is Extract<MobileTimelineItem, { kind: "attachments" }> =>`
   predicates.

None of the above needed excluding a file.

**Gates:** `make test-native` — 737 native + 777 shared tests pass, `tsc
--noEmit` clean.

**Biome:** did not run `npx biome check --write`. There is no biome config
anywhere under `mobile/` or `mobile-native/` (only
`cmd/evener-hub/frontend/biome.jsonc` exists), and `AGENTS.md` scopes biome's
enforced gate to that `src/` only. A bare `npx biome check --write` on these
files falls back to biome's own defaults and reformatted
`conversation.test.ts` wholesale (2-space to tabs, ~14k lines touched) —
reverted that and left formatting untouched, matching existing file style
instead.

**Concerns:**
1. Fixing #2/#3 above is a small step past "delete + include" — flagging in
   case Jesse wants those genuine fixture bugs split out, though they're only
   a few lines and were required to keep the gate green without excluding a
   file.
2. The biome instruction doesn't fit this pair of directories as written; see
   above rather than a silent skip.
