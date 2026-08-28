# Main integration report

## Merge identity and state

- Review-clean live-concepts base/head: `933d21a6813e8103555d9f6b3bc8cf713429f2f2` (`fix(mobile): align live smoke safety`).
- Main merge head: `720f5a4e944424ef236395ee413c93fc5d2cea49` (`fix(web): remove session vision picker`).
- Operation: explicit merge of `origin/main` into the live-concepts head; the worktree remains in `MERGING` state.
- No abort, reset, rebase, commit, push, or delegation was performed.

## Conflict resolution

The only textual conflict was `cmd/evener-hub/frontend/src/protocol/client.ts`. The staged resolution retains both sides:

- main's `PendingRequest.method: MethodName` ownership and ordinary-request-aware heartbeat suppression; and
- the live-concepts strict initialize decoder, handshake-result publication, terminal protocol-skew handling, reconnect/ready behavior, and mobile handshake consumers.

The resulting shared protocol client was verified by the complete `src/protocol` test directory and frontend typecheck (see Gates).

## Root cause and integration work

### Canonical roster notifications

Main removed obsolete `evener/tree/changed` from generated `AnyNotification`. The live mobile roster still treated it as a canonical roster invalidation event, so Task 4's typed notification helper produced the observed `TS2345` and multiple fixtures still manufactured the removed wire shape.

Roster refresh ownership now uses a generated-name-checked `Set<AnyNotification["method"]>` containing exactly the canonical notifications needed for create, close, status, queue, name, and attention changes:

1. `thread/started`
2. `thread/closed`
3. `thread/status/changed`
4. `thread/queueChanged`
5. `evener/thread/name/changed`
6. `evener/attention/changed`

The existing visibility gate, 300 ms debounce, generation-scoped scheduler key, stale-callback rejection, and late-result rejection remain unchanged. A typed six-row notification matrix plus a canonical non-roster negative row is load-bearing: before the production change, 4 rows failed (`thread/started`, `thread/closed`, `thread/queueChanged`, and `evener/thread/name/changed`); afterward all 28 roster tests passed.

The Rust built-in harness now emits `thread/status/changed` with valid `threadId`, `ref`, and `status` parameters. Its intentionally held stale `thread/closed` event also now has valid canonical parameters. Real bridge assertions, RootShell scope-isolation fixtures, and production-service lease fixtures use the same valid status-change shape.

Task 4's production-AppWire vertical slice and strict manual protocol cases now use `thread/status/changed` and valid parameters. Its exact request list remains unchanged, and one notification still produces exactly one additional `thread/list` request (two total). Existing method, count, privacy, mutation receipt, reconnect, and correlation assertions were preserved.

Audit result: zero `evener/tree/changed` occurrences remain under `mobile/` (excluding ignored build/dependency directories in the command, with a direct tracked-source scan also clean). No string cast or suppression was introduced; the Task 4 notification helper now correlates each method with `NotificationTypes[K]`.

### Main protocol capability addition

The first merged typecheck also exposed main's new required `ThreadCapabilities.changeVisionModel` field across mobile's typed production fallbacks and fixtures. Keeping the old 11-field runtime extraction would compile only after fixture edits but would silently discard the new canonical field; the first full `npm test` demonstrated that mismatch with 8 exact capability-cache equality failures.

The integration therefore preserves main's protocol side end-to-end instead of weakening types:

- `MobileCapabilities` now includes `changeVisionModel` in its documented 1:1 projection;
- the runtime capability boundary requires, validates, and copies all 12 canonical booleans; and
- typed production fallbacks and fixtures provide the field with behavior matching their model-capability fixture (`true` for all-capability fixtures, `false` for denied/fail-closed fixtures).

The focused capability suites then passed 131/131, and the complete mobile suite passed 2058/2058.

### Main pairing migration interaction

Main commit `5fbccbccb` (`hub: move mobile pairing read to AppWire`) moves the hub web settings pairing read to the typed `mobile/pairing/get` AppWire method and adds its Go/AppWire implementation and tests. It does not modify mobile app `RootShell.tsx`, `production-services.ts`, or their profile-scoped native transport. The direct shared boundary is `AppwireClient`; the conflict resolution above preserves main's pending-request method tracking while retaining the mobile handshake callbacks consumed by production services. No pairing behavior was reverted or duplicated.

## Integration resolution paths

Shared client conflict:

- `cmd/evener-hub/frontend/src/protocol/client.ts`

Canonical roster, bridge, Task 4, RootShell, and service paths:

- `mobile/src/state/roster.ts`
- `mobile/src/state/roster.test.ts`
- `mobile/src/services/appwireRealBridge.test.ts`
- `mobile/src/screens/RootShell.test.tsx`
- `mobile/src/screens/production-services.test.ts`
- `mobile/src/test/live-concepts-real-bridge.test.tsx`
- `mobile/src-tauri/examples/appwire_stdio_harness.rs`

Required `changeVisionModel` protocol-projection/fallback/fixture paths discovered by merged typecheck:

- `mobile/src/conversation/model.ts`
- `mobile/src/services/conversation.ts`
- `mobile/src/services/conversation.test.ts`
- `mobile/src/conversation/project.test.ts`
- `mobile/src/components/activity/ActivitySheet.test.tsx`
- `mobile/src/components/composer/AskComposer.test.tsx`
- `mobile/src/components/composer/Composer.test.tsx`
- `mobile/src/dev/conversationFixtures.ts`
- `mobile/src/live-concepts/LiveConceptHost.test.tsx`
- `mobile/src/live-concepts/dispatch-live-intent.test.ts`
- `mobile/src/live-concepts/project-activity.test.ts`
- `mobile/src/live-concepts/project-conversation.test.ts`
- `mobile/src/screens/ConversationScreen.test.tsx`
- `mobile/src/screens/NewSessionScreen.test.tsx`
- `mobile/src/screens/VoiceScreen.test.tsx`
- `mobile/src/services/activity.test.ts`
- `mobile/src/services/newSession.test.ts`
- `mobile/src/services/roster.test.ts`
- `mobile/src/state/activity.test.ts`
- `mobile/src/state/conversation.test.ts`

This report:

- `.superpowers/sdd/2026-08-26-live-mobile-concepts-3-integration/main-integration-report.md`

## Gates and exact evidence

All final required gates exited 0.

| Gate | Result |
| --- | --- |
| Roster TDD red run: `npx vitest run src/state/roster.test.ts --maxWorkers=1` | Expected red: 4 failed, 24 passed; the four missing canonical owners were identified above. |
| Roster TDD green run | 1 file, 28/28 tests passed. |
| Touched/full mobile Biome: `npx biome check --write src` | 207 files checked, no fixes applied, exit 0. Ten existing warnings, all `lint/style/noNonNullAssertion` in untouched `src/state/attachments.test.ts` (lines 21, 22, 23, 24, 32 twice, 38, 43, 72, 80). |
| Focused roster/RootShell/production-services/AppWire real bridge/live vertical suite | 5 files, 78/78 tests passed (`roster` 28, `RootShell` 36, production services 9, real bridge 1, live vertical 4). |
| Focused capability projection/service suite | 2 files, 131/131 tests passed (`conversation/project` 50, `services/conversation` 81). |
| `npm test` | iOS config pretest 11/11; Vitest 76 files, 2058/2058 tests passed. Pretest rebuilt the Rust harness example. |
| `npm run check` | Biome 207 files with the same 10 warnings and zero errors; TypeScript no-emit typecheck passed. |
| `npm run boundary` | Both `check-boundary.mjs` and `check-live-concepts-boundary.mjs` passed, exit 0 (quiet success). |
| `npm run build` | Typecheck passed; Vite transformed 113 modules and built successfully. |
| Shared protocol: `npx vitest run src/protocol --maxWorkers=4` | 9 files, 272/272 tests passed, including 30 `client.test.ts` tests. |
| Shared frontend: `npm run typecheck` | Passed, exit 0. |
| Rust: `cargo fmt --manifest-path src-tauri/Cargo.toml -- --check` | Passed, exit 0. |
| Rust example: `cargo build --manifest-path src-tauri/Cargo.toml --example appwire_stdio_harness` | Passed, exit 0. |
| Rust full tests: `cargo test --manifest-path src-tauri/Cargo.toml` | 179/179 tests passed across test binaries (131 + 19 + 2 + 1 + 23 + 3); doc tests 0. |
| Diff hygiene | `git diff --check` and `git diff --cached --check` passed; obsolete-event audit found zero mobile occurrences. |

One intermediate full `npm test` run failed 8 capability-cache tests after adding the required generated field to fixtures but before the runtime extractor copied it. That was a real integration failure, not ignored: the 12-field runtime projection fix above made the focused 131-test suite and the subsequent full 2058-test suite green.

## Warnings and concerns

- The 10 Biome warnings are confined to pre-existing, untouched `mobile/src/state/attachments.test.ts` non-null assertions; Biome treats them as warnings and the canonical `npm run check` exits 0. No new warning appears in an integration path.
- No unresolved conflict or unmerged index entry remains.
- The worktree intentionally remains an uncommitted merge. The repository contains the large expected staged `origin/main` merge; only the paths enumerated above are integration-resolution additions to that index.
- No functional concern remains from roster notification coverage, shared-client handshake coexistence, pairing migration, or Rust harness wire shape based on the gates above.
