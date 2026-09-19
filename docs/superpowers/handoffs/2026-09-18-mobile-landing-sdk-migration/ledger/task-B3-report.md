# Task B3 — One usage and cost accounting: NEEDS_CONTEXT

Status: NEEDS_CONTEXT. Nothing written, nothing committed. Worktree
`sdk-b3-usage` is clean at `f2599d1ed`; `npm ci` ran in all three packages.

## The twin does not exist

B3 assumes web `detailsAccounting.ts` and native `projectUsage` encode one
rule. They do not share a single line of arithmetic.

| | web `sessionTokens` (`cmd/evener-hub/frontend/src/panes/session/chrome/detailsAccounting.ts:69-89`) | native `projectUsage` (`mobile/src/services/activity.ts:437-451`) |
| --- | --- | --- |
| Input | `ThreadModel` — the post-reducer client model (`.usage`, `.turns`, `.olderCursor`) | `Thread["evener"]` — the raw wire object |
| Output | `SessionTokens \| null` = `{inputTokens, outputTokens, scope: "session"\|"loaded"}` | `UsageSummary` (never null) = 10 optional fields |
| Fields | inputTokens, outputTokens only | + cacheReadTokens, totalTokens, cost, contextUsed, contextWindow, contextRemaining, contextPressure, durationMs |
| Arithmetic | prefers thread cumulative; else sums per-turn usage; all-zero → `null`; `olderCursor` → scope `"loaded"` | none — straight field copy, `undefined` and `0` both passed through |

Neither side has rounding or a context-pressure threshold: web reads no cost
and no context pressure at all, native does no arithmetic. The plan's flagged
risk is not the blocker; the absence of a shared rule is.

## The conflict a merged module would have to resolve

The two sides hold **documented opposite positions** on whether per-turn usage
may be aggregated into a session figure.

- Web derives it: `detailsAccounting.ts:77-88` sums `turnUsageTokens` over
  loaded turns when the cumulative is absent. Pinned by
  `detailsAccounting.test.ts:26-29` (`{6961,73}+{1276,47}` → `8237/120`,
  scope `"session"`) and `:41-44`.
- Native forbids it: `mobile/src/state/activity.ts:23-27` and `:337-344` —
  "the store must NOT overwrite the activity usage aggregate with per-turn
  values", returning `"rehydrate"` for one authoritative reread instead.

Picking either is a behavior change on the other side. Per the task rules I did
not pick a winner.

## Native's real web counterpart is already shared

`projectUsage`'s web analog is `protocol/reducer.ts:797-805`, which maps the
same `evener` fields into `ThreadModel` and is already in the package. It
differs on absence handling (`contextUsed: … ?? 0`, `usage: … ?? null` vs
native leaving them `undefined`), so even that pairing is a behavior decision,
and it belongs to a D-phase reducer row, not B3.

## Real duplication found, native-internal

`mobile/src/conversation/project.ts:774-787` is a second byte-level copy of
`projectUsage` (same ten lines minus `durationMs`, returning `MobileUsage`
instead of `UsageSummary`; `MobileUsage` is declared at
`mobile/src/conversation/model.ts:173-183`). That is a genuine collapse worth
doing — native↔native, no web twin, no behavior question. It is not B3 as
written.

## Options for Jesse

1. Drop B3, like B7. Update the plan row and the inventory line
   (`docs/design/2026-09-12-sdk-migration-inventory.md:148`), which claims
   DUPLICATED on this pair.
2. Rescope B3 to the native-internal collapse above (~40 lines, no behavior
   question, oracle `mobile/src/services/activity.test.ts` +
   `conversation/project.test.ts`).
3. Rule on the aggregate-per-turn-usage question first, then re-plan a real
   shared module. This is a product decision, not a refactor.

## Gates

Not run — no code changed.

---

## Addendum — rescoped to option 2, DONE

B3 as written is withdrawn. Lane rescoped to the native-internal duplication.

Status: DONE. SHA `a439d161a` on `claude/sdk-b3-usage` (not pushed).
Commit: `refactor(mobile): one usage projection for the conversation and the activity store`.

Deleted:
- `mobile/src/services/activity.ts:437-451` — the second copy of the ten-field
  pass-through. `projectUsage` in `mobile/src/conversation/project.ts:774` is
  now exported and is the single implementation; the activity service's
  `projectUsageSummary` is a two-line wrapper adding only `durationMs`.
- `UsageSummary`'s restatement of MobileUsage's nine fields
  (`activity.ts:72-83`) — now `Readonly<MobileUsage> & { readonly durationMs?: number }`.
- The now unused `EvenerUsage` import in `activity.ts`.

Home chosen: `conversation/project.ts`. `services/` already imports values from
`conversation/project` (`services/conversation.ts:42`), and `conversation/model.ts`
is type-only, so this keeps the existing dependency direction and adds no cycle.

No new test. `activity.test.ts:519-525` already pins absence handling
(inputTokens/cost/contextPressure undefined) and `project.test.ts:1445-1455`
pins `cacheReadTokens: undefined` in a full `toEqual`. Neither test file changed.

Gate: `make test-native` green — 737 native tests / 78 files, 777 shared tests
/ 7 files, `tsc --noEmit` clean. No package change, no biome over `mobile/`.

Lines: 2 files, +13 / -29.

Concerns: none blocking. `durationMs` is the only thing separating the two
shapes; if the conversation view ever needs work duration, the wrapper and the
`UsageSummary` alias both collapse into `MobileUsage`.
