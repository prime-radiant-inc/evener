# Absorb A1 — protocol-consumer round — report

Status: **DONE**
Worktree: `webui-absorb-a1`, branch `absorb-a1`, base `388129621`.
Commit range: `388129621..16019799d` (5 commits, all `webui absorb-a1:`).

## Test summary

Full frontend suite green at the tip: **186 test files, 2870 tests passed** (base was 186/2857; +13 tests, no new files — all added to existing test files). Every commit was AND-chain gated by `npx tsc --noEmit` → `npx vitest run` (captured to a log, exit-code gated, never piped) → `npm run lint` → `npm run build` → `git restore dist/PLACEHOLDER`. Go drift test `TestGeneratedFileCurrent` passes (types.gen.ts current). Working tree clean.

## Items

### 1. Regenerate `types.gen.ts` (commit `864cb323b`)

Generator located (not guessed): `internal/appwirets`, invoked by `make generate` (per the file header) → `go generate ./appwire/...` → the `//go:generate` directive in `appwire/doc.go`. Ran the types generator surgically (`go run primeradiant.com/serf/internal/appwirets -out cmd/serf-hub/frontend/src/protocol/types.gen.ts`) so only types.gen.ts changed — `docs/appwire-protocol.md` (the other `go generate` output, out of my manifest) was left untouched; it was already current from the main absorb.

Diff was exactly the stale wire-honesty pieces: new `SandboxEscalationResolved` interface `{threadId?, ref?, escalationId}`, `ThreadItem.exitCode?: number`, and `serf/sandbox/escalation/resolved` added to the `NotificationName` union + `NotificationTypes` map. (`argumentsJson` was already present on `ThreadItem`.) Verified via the Go drift test (`ok`).

### 2. `serf/sandbox/escalation/resolved` reducer case (commit `7ad65a17e`)

One case in `applyNotification`, reusing the existing `resolvePendingEscalation` helper (grepped — it is the by-id clear the local resolve action already uses):
`return { ...resolvePendingEscalation(model, n.params.escalationId), lastFrameAt: now };`
Reusing the helper (not reimplementing) and stamping liveness like every other targeted case.

Also rewrote `resolvePendingEscalation`'s doc comment, which falsely claimed "the wire has no resolved broadcast" — now states the broadcast exists and both callers (store action + this case) reuse the helper.

RED-first: 3 reducer unit tests (mirror the `requested` tests) + 1 store-level test via `FakeClient.emitNotification` with the real wire shape (`{threadId, ref, escalationId}`), asserting both `threads` and `watchedThreads` clear. Confirmed RED (card not cleared / lastFrameAt not stamped without the case) then GREEN. The two positive tests jointly pin both behaviors (clear + liveness-stamp-on-unknown-id) — mutation net.

### 3. HIGH gap (a) — errored/denied ask not answerable (commit `7cf8eb4a7`)

`deriveAskQuestions.ts`'s `isAckedAskUserItem` gained `&& item.error === undefined`. Chose `=== undefined` (not truthiness) to match the model's documented "error PRESENCE" doctrine and the `mcp.tsx` presence convention; the wire never emits `error: ""` (Go omitempty). Verified this is the single source of the answerable set — `askDockStore`/`reconcileBatches` consume `liveAskQuestions`' output, so no sibling-owned (forbidden) file needed touching.

RED-first: an errored ask fixture yielded an answerable card (`[{key:"call_1:0"...}]`) before the fix; `[]` after. The pre-fix RED is itself the mutation net (test goes red precisely when the error check is absent).

### 4. HIGH gap (b) — escalation resolve Conflict-terminal (commit `a1da837ae`)

`resolveEscalation` in the store dropped the `mapConflict` wrapper every sibling mutating action carries. Wrapped the request in the established `try/catch → throw mapConflict(err)` pattern; the local card-clear runs only on a resolve that landed. Verified against the daemon: `server/appwire_runtime.go`'s `handleAppSandboxEscalationResolve` deliberately returns `appwire.Conflict(err.Error())` for a stale/double/raced resolve ("Surface it as a conflict so the client can drop the card rather than retry") — the client was failing to honor that. Replaced the stale "No mapConflict here" comment; broadened `mapConflict`'s doc (it now also covers escalation resolve, not only turn-CAS).

RED-first: a FakeClient conflict rejection (`WireError(-32013, {serfErrorInfo:"conflict"})`) reached the caller as a raw `WireError` before the fix; `ConflictError` after. Added the negative guard (same-code, different-serfErrorInfo → not mapped), mirroring the `send`-action conflict pair.

### 5. shellTool consumes typed `exitCode` + comment reconciliation (commit `16019799d`)

`ItemModel` had no `exitCode` field, so: added `exitCode?: number` to the model (model.ts) and mapped `exitCode: item.exitCode` in `wireItemToModel` (reducer.ts). shellTool now derives exit via `shellExitCode(item) = item.exitCode ?? parseShellExitCode(item.output ?? "")` — typed field primary, output-footer text heuristic as the old-daemon fallback. Both `summary` and `autoExpand` use it.

Reconciled the three catalogued stale comments to state present truth (no history narration): `shellTool.tsx` header (dropped the false "error is dropped / text is the only signal" claim — error and exitCode are now mapped, text is the fallback), `helpers.ts:5-6` (error is mapped; only `ThreadItem.raw`/tool_state is still dropped — verified the reducer maps neither `raw`), and `ToolCallItem.tsx:3` (removed the dead `isAskUserItem` citation — confirmed the symbol exists nowhere else — and the T1/T3 wave narration).

RED-first: 2 reducer mapping tests (live + snapshot paths, mirror the F1 error tests) + 4 shellTool tests (typed value used directly, typed wins over a conflicting footer, for summary and autoExpand). Snapshot update for the 4 fixture `toMatchSnapshot` tests was verified to be exactly 8 `exitCode: undefined` insertions and nothing else. Mutation-verified the subtle `??`-vs-`||`: swapping to `||` was caught by the two "typed 0 wins over footer" tests (a `0 || fallback` bug), then reverted.

## Concerns / follow-ups (non-blocking)

- **`ItemModel.error` is mapped but no tool descriptor renders it.** A denied/errored shell call (error set, no exitCode) still renders collapsed with the error text unsurfaced. Out of scope here (item 5 was exitCode consumption), but a real parity gap worth a follow-up — error-presence rendering for tool calls.
- **Go follow-ups (as the brief noted, out of scope):** the projector stamps `Status:"completed"` even on errored/denied items, which is why item 3 keys off error presence rather than status.
- `docs/appwire-protocol.md` was intentionally not regenerated (already current from the main absorb; not in my manifest). If a future full `make generate` ever shows doc drift, it is unrelated to this stream.
