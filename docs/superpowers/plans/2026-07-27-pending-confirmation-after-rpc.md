# Pending Confirmation After RPC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent the WebUI's optimistic confirmation timer from racing an unresolved mutation RPC while preserving exact late errors, release cleanup, and honest post-acceptance feedback.

**Architecture:** `submitWithPendingTracking` remains the single lifecycle owner. It renders optimistic state immediately, lets `perform()` exclusively own the unresolved phase, and arms the existing ten-second echo timer only after success. A typed timeout distinguishes post-success view staleness at the two UI callers, while thread-model release silently retires tracking that can no longer reconcile.

**Tech Stack:** TypeScript, React, Zustand vanilla stores, Vitest fake timers, Testing Library, existing `FakeClient`.

## Global Constraints

- Follow `docs/testing.md`: default tests are deterministic and use the fake AppWire boundary; no sleeps, network access, provider credentials, or live model behavior.
- Follow strict TDD: add each behavioral regression first, run it against the old implementation, and record the expected RED failure before production edits.
- `PENDING_TIMEOUT_MS` remains exactly `10_000`.
- An unresolved `perform()` never enters the pending-confirmation timeout phase, regardless of the AppWire request deadline.
- An echo before RPC rejection must preserve and report the original rejection exactly once.
- Model release silently retires every pending entry, timer, failure callback, settled marker, and first-frame record for that ref.
- A post-success missing echo is a warning with exact copy `<Action> was accepted, but this view didn't update. Reload before retrying.`
- Existing ordinary rejection wording and queued-drain-partial wording remain unchanged.
- Do not add a targeted `thread/read`, change AppWire deadlines, change hub resume behavior, or alter reconciliation content matching.
- Preserve unrelated work and commits on `webui-workspace-shell`; stage only files named by this plan.

## File Structure

- Modify `cmd/serf-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts`: own the RPC/confirmation phase transition, typed timeout error, and model-release retirement.
- Modify `cmd/serf-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.test.ts`: deterministic lifecycle RED/GREEN coverage.
- Modify `cmd/serf-hub/frontend/src/panes/session/composer/Composer.tsx`: translate only the typed post-success timeout into warning feedback.
- Modify `cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx`: exercise the real Composer -> threads store -> fake AppWire path and warning rendering.
- Modify `cmd/serf-hub/frontend/src/panes/session/composer/queue/QueueStrip.tsx`: translate the typed timeout for drain without disturbing partial failures.
- Modify `cmd/serf-hub/frontend/src/panes/session/composer/queue/QueueStrip.test.tsx`: pin drain warning and existing failure contracts.

---

### Task 1: Separate RPC settlement from echo confirmation

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.test.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/queue/QueueStrip.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/queue/QueueStrip.test.tsx`

**Interfaces:**
- Produces: `PendingConfirmationTimeoutError extends Error`, exported from `pendingTurnsStore.ts`.
- Produces: `isPendingConfirmationTimeoutError(error: unknown): error is PendingConfirmationTimeoutError`, exported from `pendingTurnsStore.ts`.
- Preserves: `submitWithPendingTracking(opts, perform): Promise<void>`.
- Preserves: `PENDING_TIMEOUT_MS = 10_000`.
- Consumes: `threadsStore` model presence as the reconciliation-authority boundary.

- [ ] **Step 1: Add store regressions for the unresolved and post-success phases**

Add focused fake-timer tests that express the phase boundary through observable state:

```ts
test("an unresolved perform remains optimistic beyond the confirmation window and later reports its exact rejection", async () => {
  vi.useFakeTimers();
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  let rejectPerform: ((error: unknown) => void) | undefined;
  const onFailure = vi.fn();
  const pending = submitWithPendingTracking(
    { ref: "ref_a", method: "queue", text: "hello", onFailure },
    () =>
      new Promise<void>((_resolve, reject) => {
        rejectPerform = reject;
      }),
  );
  const { result } = renderHook(() => usePendingTurnEntries("ref_a", "queue"));

  await act(async () => {
    await vi.advanceTimersByTimeAsync(PENDING_TIMEOUT_MS);
  });
  expect(result.current).toHaveLength(1);
  expect(onFailure).not.toHaveBeenCalled();

  const failure = new Error("resume failed exactly");
  await act(async () => {
    rejectPerform?.(failure);
    await expect(pending).rejects.toBe(failure);
  });
  expect(result.current).toHaveLength(0);
  expect(onFailure).toHaveBeenCalledTimes(1);
  expect(onFailure).toHaveBeenCalledWith(failure);
});

test("a successful perform receives a fresh full confirmation window", async () => {
  vi.useFakeTimers();
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  let resolvePerform: (() => void) | undefined;
  const onFailure = vi.fn();
  const pending = submitWithPendingTracking(
    { ref: "ref_a", method: "queue", text: "hello", onFailure },
    () =>
      new Promise<void>((resolve) => {
        resolvePerform = resolve;
      }),
  );

  await vi.advanceTimersByTimeAsync(PENDING_TIMEOUT_MS);
  resolvePerform?.();
  await pending;
  await vi.advanceTimersByTimeAsync(PENDING_TIMEOUT_MS - 1);
  expect(onFailure).not.toHaveBeenCalled();

  await vi.advanceTimersByTimeAsync(1);
  expect(onFailure).toHaveBeenCalledTimes(1);
  expect(onFailure.mock.calls[0]?.[0]).toBeInstanceOf(PendingConfirmationTimeoutError);
});
```

Retain and, where necessary, update the existing send and queue echo-then-rejection tests so they still assert the original error object exactly once. Replace the obsolete test that expects an unresolved send to synthesize a timeout after its user echo with:

```ts
test("an echo followed by successful send settlement leaves first-frame state frame-owned", async () => {
  vi.useFakeTimers();
  // Register deferred send, emit matching user echo, resolve perform.
  // Advance beyond PENDING_TIMEOUT_MS: no failure and awaiting remains true.
  // Emit an agent item/started frame: awaiting becomes false.
});
```

- [ ] **Step 2: Add model-release regressions**

Add tests that fail if released entries retain any observable lifecycle:

```ts
test("model release retires an unresolved pending entry without failure or resurrection", async () => {
  vi.useFakeTimers();
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a");
  let rejectPerform: ((error: unknown) => void) | undefined;
  const onFailure = vi.fn();
  const pending = submitWithPendingTracking(
    { ref: "ref_a", method: "send", text: "hello", onFailure },
    () =>
      new Promise<void>((_resolve, reject) => {
        rejectPerform = reject;
      }),
  );
  const pendingHook = renderHook(() => usePendingTurnEntries("ref_a", "send"));
  const awaitingHook = renderHook(() => useAwaitingFirstFrameSend("ref_a"));

  act(() => threadsStore.getState().releaseThread("ref_a"));
  expect(pendingHook.result.current).toHaveLength(0);
  expect(awaitingHook.result.current).toBe(false);

  await vi.advanceTimersByTimeAsync(PENDING_TIMEOUT_MS * 2);
  const failure = new Error("late rejection after release");
  await act(async () => {
    rejectPerform?.(failure);
    await expect(pending).rejects.toBe(failure);
  });
  expect(onFailure).not.toHaveBeenCalled();
});
```

Add a release -> remount test using an already-existing same-text historical item. Assert the retired entry stays absent and no callback fires after hydration, timer advancement, and late settlement.

- [ ] **Step 3: Run the focused store tests and capture RED**

Run from `cmd/serf-hub/frontend`:

```bash
npx vitest run src/panes/session/composer/queue/pendingTurnsStore.test.ts
```

Expected RED:

- the unresolved perform receives the old synthesized timeout;
- the fresh post-success window expires too early;
- release leaves at least the optimistic entry alive;
- the obsolete unresolved-send timeout expectation conflicts with the new contract.

If a new test passes against the old implementation, strengthen its observable assertion before continuing.

- [ ] **Step 4: Implement the minimal store phase transition and typed error**

Add the exported type guard:

```ts
export class PendingConfirmationTimeoutError extends Error {
  constructor() {
    super("The server accepted this message, but the view didn't update.");
    this.name = "PendingConfirmationTimeoutError";
  }
}

export function isPendingConfirmationTimeoutError(error: unknown): error is PendingConfirmationTimeoutError {
  return error instanceof PendingConfirmationTimeoutError;
}
```

Construct it only from `timeoutPendingTurn`.

In `submitWithPendingTracking`, keep registration and optimistic state before `perform()`, but remove the initial `setTimeout`. After `await perform()` succeeds:

```ts
settledPerformIds.add(id);
if (pendingTurnsStore.getState().entries.has(id)) {
  timeoutHandles.set(
    id,
    setTimeout(() => timeoutPendingTurn(id), PENDING_TIMEOUT_MS),
  );
} else {
  settledPerformIds.delete(id);
  failureCallbacks.delete(id);
}
```

Preserve the rejection path through `failPendingTurn(id, err)` and rethrow the original value.

Add a small store-owned retirement helper that, in one state update, removes every entry and `awaitingFirstFrame` record for refs absent from the current threads map, while calling `clearBookkeeping(id)` for each retired id. Invoke it from `reconcileAll` after model reconciliation and before forgetting released `lastSeenModels` refs. It must not call `onFailure`.

- [ ] **Step 5: Run store tests GREEN and mutation-check the three load-bearing branches**

Run:

```bash
npx vitest run src/panes/session/composer/queue/pendingTurnsStore.test.ts
```

Expected: all tests pass.

Then temporarily perform each mutation separately, run the named covering test, observe RED, and restore the production code:

1. Arm the timer before `perform()` again -> unresolved-phase test fails.
2. Delete `failureCallbacks` during echo reconciliation -> echo-then-rejection tests fail.
3. Skip release retirement -> release tests fail.

Do not commit any mutation.

- [ ] **Step 6: Commit the store lifecycle**

Stage only the store and its test:

```bash
git add cmd/serf-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts \
  cmd/serf-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.test.ts
git commit -m "fix(webui): separate RPC wait from echo confirmation"
```

The commit body must document the RED failure, post-success timer ownership, late-rejection preservation, and silent release cleanup.

- [ ] **Step 7: Add caller-level RED tests**

In `Composer.test.tsx`, add a fake-timer integration test that uses the real `Composer`, `threadsStore.send`, and `FakeClient`:

```ts
test("a cold send still awaiting turn/start after ten seconds remains optimistic and reports only its later exact rejection", async () => {
  vi.useFakeTimers();
  let rejectStart: ((error: unknown) => void) | undefined;
  const fake = await mountComposer("ref_a");
  fake.on(
    "turn/start",
    () =>
      new Promise((_resolve, reject) => {
        rejectStart = reject;
      }),
  );
  // Enter text and submit using the existing user-event/fake-timer idiom.
  // Advance PENDING_TIMEOUT_MS: pending text/skeleton remains; no toast.
  // Reject with Error("resume rendezvous failed"): exactly that failure toast appears.
});
```

Add a second Composer test that lets `turn/start` succeed without an echo, advances a full `PENDING_TIMEOUT_MS`, and asserts a warning toast with exact text:

```text
Send was accepted, but this view didn't update. Reload before retrying.
```

In `QueueStrip.test.tsx`, add an equivalent successful-drain-without-echo timer test asserting warning kind and exact text:

```text
Drain was accepted, but this view didn't update. Reload before retrying.
```

Retain existing tests for ordinary drain rejection and `queuedDrainPartial`; they must continue to assert their existing error messages.

- [ ] **Step 8: Run caller tests and capture RED**

Run from `cmd/serf-hub/frontend`:

```bash
npx vitest run src/panes/session/composer/Composer.test.tsx \
  src/panes/session/composer/queue/QueueStrip.test.tsx
```

Expected RED:

- the unresolved Composer request emits the old ten-second failure;
- the post-success timeout is still rendered as an error/action failure;
- drain uses `Drain failed` instead of accepted-but-stale warning copy.

- [ ] **Step 9: Implement phase-specific caller feedback**

Import `isPendingConfirmationTimeoutError` in `Composer.tsx` and `QueueStrip.tsx`.

In Composer's `onFailure`, branch before ordinary and partial failure formatting:

```ts
if (isPendingConfirmationTimeoutError(err)) {
  const label = kind === "send" ? "Send" : kind === "queue" ? "Queue" : kind === "steer" ? "Steer" : "Drain";
  toasts.push("warning", `${label} was accepted, but this view didn't update. Reload before retrying.`);
} else if (kind === "drain" && isQueuedDrainPartial(err)) {
  // existing partial branch unchanged
} else {
  // existing ordinary rejection branch unchanged
}
```

In QueueStrip's drain callback:

```ts
if (isPendingConfirmationTimeoutError(err)) {
  toasts.push("warning", "Drain was accepted, but this view didn't update. Reload before retrying.");
} else if (isQueuedDrainPartial(err)) {
  toasts.push("error", `Queued, but drain failed: ${errorText(err)}`);
} else {
  toasts.push("error", sessionActionError("Drain failed", err));
}
```

- [ ] **Step 10: Run caller tests GREEN and mutation-check warning classification**

Run:

```bash
npx vitest run src/panes/session/composer/Composer.test.tsx \
  src/panes/session/composer/queue/QueueStrip.test.tsx
```

Expected: all tests pass.

Temporarily route `PendingConfirmationTimeoutError` through the ordinary error branch. Confirm the Composer and QueueStrip warning tests fail, then restore the code.

- [ ] **Step 11: Commit caller feedback**

Stage only the four caller files:

```bash
git add cmd/serf-hub/frontend/src/panes/session/composer/Composer.tsx \
  cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx \
  cmd/serf-hub/frontend/src/panes/session/composer/queue/QueueStrip.tsx \
  cmd/serf-hub/frontend/src/panes/session/composer/queue/QueueStrip.test.tsx
git commit -m "fix(webui): distinguish accepted stale confirmations"
```

The commit body must explain that warning copy prevents an unsafe duplicate retry while preserving real rejection and partial-drain errors.

- [ ] **Step 12: Run focused and frontend verification**

Run from `cmd/serf-hub/frontend`:

```bash
npx vitest run src/panes/session/composer/queue/pendingTurnsStore.test.ts \
  src/panes/session/composer/Composer.test.tsx \
  src/panes/session/composer/queue/QueueStrip.test.tsx
npm run typecheck
npm run lint
```

Then run from the repository root:

```bash
make test-web
git diff --check HEAD~2..HEAD
git status --short --branch
```

Expected:

- focused suites pass;
- the full WebUI suite, typecheck, and Biome lint pass;
- diff check is clean;
- only unrelated user-owned work, if any, remains unstaged.

- [ ] **Step 13: Self-review and update kata evidence**

Review `git diff <task-base>..HEAD` against every state transition in the design. Confirm no targeted read, AppWire timeout change, hub change, or content-matching change entered the diff.

Append a kata `y35t` comment with:

- both implementation commits;
- the exact RED failures observed;
- focused and full verification counts;
- any verification limitation.

Do not close `y35t`; the controller closes it only after independent review and verification.
