== panel a1a48703-9d38-4b13-b728-6cbd996665d6 head c8828ce2d outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=20938 verdict=0 chars=906
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=20939 verdict=0 chars=646
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=20940 verdict=0 chars=2236

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium  
  **Location**: `mobile-native/src/nativeMutationRuntime.ts:98-103`  
  **Problem**: The default mutation identity is per-process, but the SQLite outbox survives process restarts. Pending records from a previous launch therefore receive a different identity and can be treated as another client's mutations.  
  **Fix**: Persist the native client identity across launches, or omit `originClientId` for native records.

---

- **Severity**: Low  
  **Location**: `mobile-native/src/nativeMutationRuntime.test.ts:343-346`  
  **Problem**: The durable-enqueue test uses a real 100 ms timeout, making it flaky under a loaded CI runner.  
  **Fix**: Use an injected deterministic barrier or fake timers instead of wall-clock timing.

## Summary

Adds a native SQLite-backed mutation runtime with hub-scoped dispatch, readiness handling, retries, and lifecycle tests.

######## member 1 (codex default)
## Review Findings
- **Severity**: Low
- **Location**: `mobile-native/src/nativeMutationRuntime.test.ts:4`
- **Problem**: Unused `MutationOutboxSQLite` value import; only the database type is used.
- **Fix**: Drop the value import and keep `type MutationOutboxDatabase`.
---
- **Severity**: Low
- **Location**: `mobile-native/src/nativeMutationRuntime.test.ts:250`
- **Problem**: Stray leading space before `expect`, inconsistent with surrounding indentation.
- **Fix**: Remove the extra space to match file formatting.
## Summary
Adds a standalone native mutation runtime with hub-scoped durable outbox dispatch plus supporting types and tests.


######## member 2 (pi default)
## Summary

The series adds a standalone native mutation runtime for the mobile app: shared request/kind types in `mobile/src/state/conversationMutation.ts`, a `NativeMutationRuntime` that wires a durable `MutationOutboxSQLite` to the package `MutationOutbox`/`MutationDispatcher` with hub-scoped target keys, and an extensive vitest suite. Two of the four commits are corrective: `34bb003` keeps outbox discovery wired to the dispatcher via `onDiscover`, and `03004af` fences dispatch by making `#getClient` return `undefined` once `#started` is false. The aggregate result matches that intent: per-hub target scoping, replacement-safe registration cleanup, durable-enqueue-before-dispatch, retry with a stable `clientMutationId`, and stop/restart fencing are all implemented and covered.

No high or medium issues found. Two low-severity items follow.

---

**Severity: low** — `mobile-native/src/nativeMutationRuntime.test.ts:345`

`test("submit resolves at the durable enqueue boundary")` decides the boundary with a 100 ms wall-clock `Promise.race` (`new Promise<"waiting">((resolve) => setTimeout(..., 100))`). If the process is stalled under CI load for more than 100 ms, the timer wins even though `submit` behaved correctly, producing a false failure — contrary to the project rule that default tests must be deterministic. The enqueue path is synchronous underneath, so the assertion doesn't need a clock at all.

Suggested fix: drive the boundary deterministically — e.g. gate the `turn/start` handler on a separately-held promise and assert `submission` is already settled once the scripted request is in flight (flush with `await Promise.resolve()` a bounded number of times), rather than racing a real timer.

---

**Severity: low** — `mobile-native/src/nativeMutationRuntime.test.ts:4`

`MutationOutboxSQLite` is imported but never referenced anywhere in the file (only `MutationOutboxDatabase` is used, in `openDatabase()`), so the import is dead. It has no functional impact today because mobile-native is not in the enforced Biome scope, but it is noise that a future `noUnusedImports`/lint pass would flag.

Suggested fix: change the import to `import { type MutationOutboxDatabase } from "./mutationOutboxStorage";`.
