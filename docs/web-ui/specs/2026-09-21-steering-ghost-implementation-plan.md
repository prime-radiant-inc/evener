# Steering ghost at the live edge — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render a held steering message (steer/drain/promote) in the transcript as the provisional user message it will become, with a delivery caption, until it is delivered or departs.

**Architecture:** One new trailing virtual row (`live-edge`) under TranscriptBody renders a new `HeldSteerStack` of `UserMessageView` ghosts below the AskDock. The pending-entries package gains a `promote` method mapping, a never-pruned id→`createdAt` carrier (widening `submittedHere` from a set to a map), and a known-`createdAt`-first shared sort. `PendingChips` keeps `send` only. A new `HeldSteerAnnouncements` live region outside the virtual list announces arrivals, delivery, and non-delivery departures exactly once.

**Tech Stack:** React 19 + TypeScript, vitest + @testing-library/react (jsdom, fake-indexeddb, `FakeClient` from `@evener/appwire-client/testing/fakeClient`), CSS modules with design tokens. No new dependencies.

**Spec:** `docs/web-ui/specs/2026-09-20-steering-ghost-live-edge.md` — the plan argues from the spec; executors read both. Mockups: `docs/web-ui/mockups/2026-09-20-steering-almost-delivered/` (idea A is the chosen direction).

**Resolved decisions carried in:** Jesse approved the spec and the §5 race is **accept-and-document** (the spec's default; daemon-side recovery is a non-goal). Do not add daemon or wire changes.

## Global Constraints

- Gates: `make test-web` (typecheck + unit + Biome), `make lint`, `make vet`; `make test-web-browser` on Chrome-capable hosts for geometry. All default tests deterministic, no live provider (see `docs/developing-evener/testing.md`).
- Biome: run from `cmd/evener-hub/frontend` only (`npx biome check --write <touched src files>`); never from the repo root. Avoid `noNonNullAssertion` and array-index-key violations.
- Never run `npm ci` through a symlinked `node_modules`; `make test-web`'s preflight owns install freshness.
- Tokens only, no new colors: the provisional register uses existing `--accent`, `--accent-bg`.
- Caption copy is exact: `Joining this turn`, `Delivers with the next turn`, `Delivers when this step finishes`, separated from the count by ` · ` (space, U+00B7, space). Accepted arms read `held 42s`; submitting arms read the bare count. The count is omitted entirely when `createdAt` is unknown — never `NaN`, never a false `0s`.
- The count formats through the package's exported `formatElapsed` (`appwire-client/typescript/displayFormat.ts`, re-exported from the package root).
- Caption arms key off the thread STATUS TYPE via the package's `isTurnActive`, never `model.activeTurnId` (issue #1330: the id goes false between inline turns while the run continues).
- New UI copy this spec defines: the `[queued messages]` fallback label (owned by HeldSteerStack; `queueEntryPreviewText` itself must keep returning `""` for contentless input — it is a matching key).
- `opensExchange` is always false for ghosts; delivered messages render exactly today's output (absent `provisional` prop must be a no-op regression pin).
- No cross-fade/morph machinery, no wire timestamp on `PendingMutation`, no intentSequence tie-break (same-ms corner accepted per spec §4).

## File Structure

| File | Responsibility |
|---|---|
| `appwire-client/typescript/state/mutation/pendingEntries.ts` | `PendingMethod` gains `promote`; shared `pendingEntryPreview` helper; `reconcilePendingEntries` sort becomes known-`createdAt`-first and reads the id→`createdAt` map into authoritative entries; `submittedHere` param widens set→map |
| `appwire-client/typescript/state/mutation/pendingTurns.ts` | `submittedHere` state widens to the never-pruned id→`createdAt` map, written at `recordSubmittedHere` |
| `cmd/evener-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts` | singleton reset re-initializes the map; exports `pendingTurnEntries` (already present) |
| `cmd/evener-hub/frontend/src/panes/session/composer/queue/queueDisplay.ts` | re-export shim gains `pendingEntryPreview` |
| `cmd/evener-hub/frontend/src/panes/session/pending/PendingChips.tsx` | send-only |
| `cmd/evener-hub/frontend/src/stores/threads.ts` | `promoteQueuedAsSteer` signature widened for the display input |
| `cmd/evener-hub/frontend/src/panes/session/composer/queue/QueueStrip.tsx` | promote passes the row's display text + skills |
| `cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.tsx` | `UserMessageView` gains `provisional?: string` |
| `cmd/evener-hub/frontend/src/panes/session/transcript/messages/usermessageitem.module.css` | `.provisional` register (dashed accent edge, 0.78 opacity, dashed railmark) |
| `cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerStack.tsx` (+ `.module.css`) | **new**: the ghost stack; owns `[queued messages]`, the caption builder, the entries filter, the arrival-epoch hook |
| `cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerAnnouncements.tsx` | **new**: announce-once live region (AskDockAnnouncements pattern; NOT extracted — the two key on different transitions, extraction is a separate refactor) |
| `cmd/evener-hub/frontend/src/panes/session/Session.tsx` | `live-edge` trailingRow composition, derived `renderedRowCount`, `heldEpoch` signal |
| `cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts` | new `heldEpoch` option + arrival-edge effect |
| matching `*.test.ts(x)` files | per task below |

Run commands: frontend and package tests both run under the frontend vitest (its config includes `appwire-client/typescript`'s own test files). From `cmd/evener-hub/frontend`: `npx vitest run src/...` for frontend files, `npx vitest run ../../../appwire-client/typescript/state/mutation/pendingEntries.test.ts` for package files.

---

### Task 1: PendingChips keeps `send` only; the entry-body composition becomes one shared helper

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/pending/PendingChips.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/queue/queueDisplay.ts`
- Create code in: `appwire-client/typescript/state/mutation/pendingEntries.ts` (export only)
- Test: `cmd/evener-hub/frontend/src/panes/session/pending/PendingChips.test.tsx`

**Interfaces:**
- Produces: `pendingEntryPreview(entry: { text: string; imageCount: number; skillNames: readonly string[] }): string` exported from `appwire-client/typescript/state/mutation/pendingEntries.ts`, re-exported through `queueDisplay.ts`. Task 6 (HeldSteerStack) consumes it.

- [ ] **Step 1: Write the failing tests** — in `PendingChips.test.tsx`, replace the test at the `chips the send/steer/drain methods` test with:

```tsx
test("chips send only: steer/drain are the ghost stack's surface, never a chip", async () => {
  await seedPending("send", "a send");
  await seedPending("steer", "a steer");
  await seedPending("drain", "a drain");
  await seedPending("queue", "a queued one");
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText("a send")).toBeTruthy();
  expect(screen.queryByText("a steer")).toBeNull();
  expect(screen.queryByText("a drain")).toBeNull();
  expect(screen.queryByText("a queued one")).toBeNull();
});
```

In the same file, update the `labels each chip with its method` test (its last lines) to assert the send label only:

```tsx
test("labels a send chip as Sending", async () => {
  await seedPending("send", "nudge");
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.getByText(/sending/i)).toBeTruthy();
});
```

Add a package-level pin test in `appwire-client/typescript/state/mutation/pendingEntries.test.ts` (top, after the imports — it pins the matching key Task 6 relies on):

```ts
import { queueEntryPreviewText } from "./pendingEntries";

test("queueEntryPreviewText still returns the empty string for contentless input", () => {
  expect(queueEntryPreviewText("", 0)).toBe("");
  expect(queueEntryPreviewText("   ", 0)).toBe("");
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/pending/PendingChips.test.tsx`
Expected: FAIL — `a steer` and `a drain` still chip.

- [ ] **Step 3: Implement** — in `appwire-client/typescript/state/mutation/pendingEntries.ts`, add after `skillMarkers`:

```ts
// The one entry-body composition every in-flight surface renders - the
// chips for sends, the held-steer ghost stack for steer/drain/promote
// (steering-ghost spec §2): the matching text-plus-markers preview
// PendingChips used to compose inline, extracted so the ghost is not a
// second copy of the chip's composition. Contentless input still composes
// to "" - the matching-key contract queueEntryPreviewText carries above.
export function pendingEntryPreview(entry: {
  text: string;
  imageCount: number;
  skillNames: readonly string[];
}): string {
  return [queueEntryPreviewText(entry.text, entry.imageCount), skillMarkers(entry.skillNames)]
    .filter((part) => part !== "")
    .join(" ");
}
```

In `queueDisplay.ts`, add `pendingEntryPreview` to the existing value re-export list.

In `PendingChips.tsx`: update the header comment (the strip keeps `send` only; steer/drain/promote ghosts render in the transcript's trailing row per the steering-ghost spec, replacing the old ruling this header recorded). Change the method types and filter:

```tsx
type OptimisticMethod = "send";
type OptimisticEntry = PendingTurnEntry & { method: OptimisticMethod };

function isOptimistic(entry: PendingTurnEntry): entry is OptimisticEntry {
  // blockedUnknown and canceled rows are QueueStrip's durable rows, never
  // in-flight chips: a canceled row would otherwise read as still Sending
  // here while QueueStrip simultaneously reports it as canceled.
  return entry.method === "send" && entry.state !== "blockedUnknown" && entry.state !== "canceled";
}

const METHOD_LABEL: Record<OptimisticMethod, string> = {
  send: "Sending",
};
```

and the chip body becomes `<span className={CLASS.text}>{pendingEntryPreview(entry)}</span>` (import it from `../composer/queue/queueDisplay`; drop the now-unused inline composition and the `queueEntryPreviewText`/`skillMarkers` imports if nothing else in the file uses them).

- [ ] **Step 4: Run tests to verify pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/pending/PendingChips.test.tsx ../../../appwire-client/typescript/state/mutation/pendingEntries.test.ts`
Expected: PASS (all).

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/session/pending/PendingChips.tsx cmd/evener-hub/frontend/src/panes/session/pending/PendingChips.test.tsx cmd/evener-hub/frontend/src/panes/session/composer/queue/queueDisplay.ts appwire-client/typescript/state/mutation/pendingEntries.ts appwire-client/typescript/state/mutation/pendingEntries.test.ts
git commit -m "feat(web-ui): PendingChips keeps send only; extract the shared pending entry preview"
```

---

### Task 2: `turn/promoteQueuedAsSteer` maps to its own `promote` pending method

**Files:**
- Modify: `appwire-client/typescript/state/mutation/pendingEntries.ts`
- Test: `appwire-client/typescript/state/mutation/pendingEntries.test.ts`, `cmd/evener-hub/frontend/src/panes/session/pending/PendingChips.test.tsx`

**Interfaces:**
- Produces: `PendingMethod` = `"send" | "steer" | "queue" | "drain" | "promote"`. Tasks 4, 6, 7 rely on promote entries flowing through `usePendingTurnEntries`.

- [ ] **Step 1: Write the failing tests** — in `pendingEntries.test.ts`:

```ts
test("promote maps to its own PendingMethod, not folded into steer", () => {
  expect(
    reconcilePendingEntries(
      "ref_a",
      [outbox("mutation_1", "turn/promoteQueuedAsSteer", "hello")],
      model(),
      NOTHING_SUBMITTED_HERE,
      UNATTRIBUTED_ONLY,
    ),
  ).toEqual([expect.objectContaining({ id: "mutation_1", method: "promote", text: "hello" })]);
});

test("a promote's display input previews in the entry", () => {
  const record: MutationOutboxRecord = {
    ...outbox("mutation_1", "turn/promoteQueuedAsSteer", ""),
    optimisticDisplay: { method: "turn/promoteQueuedAsSteer", input: [{ type: "text", text: "promoted body" }] },
  };
  expect(
    reconcilePendingEntries("ref_a", [record], model(), NOTHING_SUBMITTED_HERE, UNATTRIBUTED_ONLY),
  ).toEqual([expect.objectContaining({ id: "mutation_1", method: "promote", text: "promoted body" })]);
});
```

In `PendingChips.test.tsx`, extend `seedPending`'s `wireMethod` map with `promote: "turn/promoteQueuedAsSteer"` (the literal map keyed by `PendingMethod` must stay exhaustive once the union grows) and add:

```tsx
test("a pending promote never chips (the ghost stack owns it)", async () => {
  await seedPending("promote", "a promote");
  const { container } = render(<PendingChips sessionRef="ref_a" />);
  expect(screen.queryByText("a promote")).toBeNull();
  expect(container.innerHTML).toBe(""); // the strip renders null with no send entries
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/mutation/pendingEntries.test.ts src/panes/session/pending/PendingChips.test.tsx`
Expected: FAIL — promote entries are dropped (`method: undefined`).

- [ ] **Step 3: Implement** — in `pendingEntries.ts`:

```ts
export type PendingMethod = "send" | "steer" | "queue" | "drain" | "promote";
```

```ts
function pendingMethod(method: string): PendingMethod | undefined {
  if (method === "turn/start") return "send";
  if (method === "turn/steer") return "steer";
  if (method === "turn/queue") return "queue";
  if (method === "turn/drainAsSteer") return "drain";
  if (method === "turn/promoteQueuedAsSteer") return "promote";
  return undefined;
}
```

Add a sentence to the doc comment above `pendingMethod`'s call sites if one names the method family: promote is its own method so labels and tests stay honest (spec §3), never folded into `steer`.

Then sweep for exhaustive maps the widened union breaks: run `rg -n "Record<PendingMethod|Exclude<PendingMethod" cmd/evener-hub/frontend/src appwire-client/typescript` and fix every hit so `promote` is either mapped or excluded deliberately (expected hits: `PendingChips.tsx` — already `send`-only from Task 1; `PendingChips.test.tsx` `seedPending` — done in Step 1). The gate is `npx tsc --noEmit` inside `make test-web`; any miss shows up there.

- [ ] **Step 4: Run tests to verify pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/mutation/pendingEntries.test.ts src/panes/session/pending/PendingChips.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add appwire-client/typescript/state/mutation/pendingEntries.ts appwire-client/typescript/state/mutation/pendingEntries.test.ts cmd/evener-hub/frontend/src/panes/session/pending/PendingChips.test.tsx
git commit -m "feat(web-ui): map turn/promoteQueuedAsSteer to its own promote pending method"
```

---

### Task 3: The id→`createdAt` carrier and the known-first shared sort

**Files:**
- Modify: `appwire-client/typescript/state/mutation/pendingTurns.ts`
- Modify: `appwire-client/typescript/state/mutation/pendingEntries.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts` (reset only)
- Test: `appwire-client/typescript/state/mutation/pendingTurns.test.ts`, `appwire-client/typescript/state/mutation/pendingEntries.test.ts`

**Interfaces:**
- Produces: `PendingTurnsState.submittedHere: ReadonlyMap<string, number>` (id → `createdAt`, never pruned); `reconcilePendingEntries(ref, outbox, model, submittedHere: ReadonlyMap<string, number>, isOwnMutationRecord)` — the 4th parameter changes type from `ReadonlySet<string>`; authoritative entries read `createdAt` from the map; the returned array sorts known-`createdAt` first ascending, unknown last.

- [ ] **Step 1: Write the failing tests.**

In `pendingTurns.test.ts`, inside the existing `describe("recordSubmittedHere", ...)` block (after the no-op test), add (the file's local `outboxRecord`/`optimisticRecord` builders take partial overrides; extend them to accept `createdAt` if they do not already — `records.ts` already requires `createdAt: number` on both record types):

```ts
test("writes the id -> createdAt map entry from the record's createdAt", () => {
  const store = createPendingTurnsStore({
    threads: fakeThreadsPort(),
    draft: fakeDraftPort(),
    identity: fakeIdentity("client-x"),
  });
  const own = outboxRecord({ clientMutationId: "own-1", originClientId: "client-x", createdAt: 1234 });
  store.recordSubmittedHere({ outbox: [own], optimistic: [] });
  expect(store.getState().submittedHere.get("own-1")).toBe(1234);
});

test("never prunes: the map outlives the settle that deletes the durable record", () => {
  const store = createPendingTurnsStore({
    threads: fakeThreadsPort(),
    draft: fakeDraftPort(),
    identity: fakeIdentity("client-x"),
  });
  const own = outboxRecord({ clientMutationId: "own-1", originClientId: "client-x", createdAt: 1234 });
  store.recordSubmittedHere({ outbox: [own], optimistic: [] });
  // The settle deletes the durable record out of the projection...
  store.setState({ outbox: new Map(), optimistic: new Map() });
  // ...and the carrier still holds the createdAt a reload-after-settle cannot
  // re-discover (steering-ghost spec §4).
  expect(store.getState().submittedHere.get("own-1")).toBe(1234);
});
```

In `pendingEntries.test.ts`: change `NOTHING_SUBMITTED_HERE` to `const NOTHING_SUBMITTED_HERE: ReadonlyMap<string, number> = new Map();` and replace every `new Set(["mutation_1"])` argument (three sites: the "stays its own once authoritative" test, the "after its durable record is settled away" test, and the "never submitted is not its own" test) with `new Map([["mutation_1", 1]])`. Then add:

```ts
test("sorts known-createdAt entries first, ascending, with unknown-createdAt after them", () => {
  const late = { ...outbox("mutation_30", "turn/steer", "late"), createdAt: 30 };
  const early = { ...outbox("mutation_10", "turn/steer", "early"), createdAt: 10 };
  const mid = { ...outbox("mutation_20", "turn/steer", "mid"), createdAt: 20 };
  const unknown: PendingMutation = {
    clientMutationId: "mutation_remote",
    method: "turn/steer",
    input: [{ type: "text", text: "remote client's steer" }],
    executionState: "accepted",
    projectionState: "pending",
  };
  const entries = reconcilePendingEntries(
    "ref_a",
    [late, early, mid],
    model({ pendingMutations: [unknown] }),
    NOTHING_SUBMITTED_HERE,
    UNATTRIBUTED_ONLY,
  );
  expect(entries.map((entry) => entry.id)).toEqual([
    "mutation_10",
    "mutation_20",
    "mutation_30",
    "mutation_remote",
  ]);
});

test("the map carrier hands an authoritative entry its createdAt after the settle removed the record", () => {
  const pending: PendingMutation = {
    clientMutationId: "mutation_1",
    method: "turn/steer",
    input: [{ type: "text", text: "hello" }],
    executionState: "accepted",
    projectionState: "pending",
  };
  expect(
    reconcilePendingEntries("ref_a", [], model({ pendingMutations: [pending] }), new Map([["mutation_1", 42]]), UNATTRIBUTED_ONLY),
  ).toEqual([expect.objectContaining({ id: "mutation_1", createdAt: 42, fromThisClient: true })]);
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/mutation/pendingEntries.test.ts ../../../appwire-client/typescript/state/mutation/pendingTurns.test.ts`
Expected: FAIL — `submittedHere.get` is not a function (still a Set); type errors on the map arguments; unknown-`createdAt` still sorts first.

- [ ] **Step 3: Implement.**

In `pendingTurns.ts`: widen the `submittedHere` field of `PendingTurnsState` and its doc comment (ids only → id → `createdAt`, written at the same `recordSubmittedHere` site; never pruned; read for steer-family entries — the pre-settle reload re-discovers `createdAt` through the same durable-read scan); initialize `submittedHere: new Map()`; rewrite the body of `store.recordSubmittedHere`:

```ts
store.recordSubmittedHere = (snapshot) => {
  const known = store.getState().submittedHere;
  // The outbox is shared per origin: another client's records read back
  // out of it are visible here but are not this store's submissions, and
  // claiming them would reroute this store's own routing behind their
  // sends. The carried value is the record's createdAt - the one timestamp
  // the post-settle projection cannot re-derive (the authoritative entry
  // carries none).
  const discovered = [...snapshot.outbox, ...snapshot.optimistic]
    .filter((record) => deps.identity.isOwnMutationRecord(record))
    .map((record) => [record.clientMutationId, record.createdAt] as const)
    .filter(([id]) => !known.has(id));
  if (discovered.length === 0) return;
  store.setState((state) => ({ submittedHere: new Map([...state.submittedHere, ...discovered]) }));
};
```

In `pendingEntries.ts`: change the `submittedHere` parameter of `reconcilePendingEntries` to `ReadonlyMap<string, number>` and update its doc comment (the map is the `fromThisClient` carrier widened to also carry `createdAt` across the settle). `fromThisClient` reads stay `submittedHere.has(mutation.clientMutationId)` (Map.has is the same call). Pass the map into `authoritativeEntry` — and update its call site in the pendingMutations loop to `authoritativeEntry(ref, mutation, fromThisClient, submittedHere)`:

```ts
function authoritativeEntry(
  ref: string,
  mutation: PendingMutation,
  fromThisClient: boolean,
  submittedHere: ReadonlyMap<string, number>,
): PendingTurnEntry | undefined {
  const method = pendingMethod(mutation.method);
  if (!method) return undefined;
  return {
    id: mutation.clientMutationId,
    ref,
    method,
    ...inputPreview(mutation.input),
    // The wire's PendingMutation carries no timestamp: this client's own
    // createdAt survives the settle only through the submittedHere map
    // (page-session scope, spec §4); another client's entry stays unknown.
    createdAt: submittedHere.get(mutation.clientMutationId),
    state: mutation.executionState === "claimed" ? "claimed" : "accepted",
    source: "authoritative",
    fromThisClient,
  };
}
```

and the sort at the end of `reconcilePendingEntries` becomes the §4 rule (one home; queue rows and chips inherit it — extend the surrounding comment to say so):

```ts
  // Known-createdAt first, ascending; entries without one after them (the
  // daemon sorts pendingMutations lexicographically by id, so their
  // relative order is stable within a snapshot only). The stable sort keeps
  // array order for equal createdAt - a same-millisecond double-submit
  // keeps submission order in-session, a corner the spec accepts. This is
  // the ONE home of the rule: queue rows, chips, and the held-steer ghost
  // stack all render this order.
  return [...entries.values()].sort((left, right) => {
    if (left.createdAt === undefined && right.createdAt === undefined) return 0;
    if (left.createdAt === undefined) return 1;
    if (right.createdAt === undefined) return -1;
    return left.createdAt - right.createdAt;
  });
```

In `cmd/evener-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts`: `resetPendingTurnsStoreForTests` sets `submittedHere: new Map()` (the type flows from the package; `usePendingTurnEntries`'s identity-comparison cache needs no change — Map identity behaves like Set identity there).

- [ ] **Step 4: Run tests to verify pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/mutation/pendingEntries.test.ts ../../../appwire-client/typescript/state/mutation/pendingTurns.test.ts src/panes/session/pending/PendingChips.test.tsx src/panes/session/composer/queue/QueueStrip.test.tsx`
Expected: PASS (QueueStrip and PendingChips ride along to prove the inherited-ordering change breaks no durable-row behavior).

- [ ] **Step 5: Commit**

```bash
git add appwire-client/typescript/state/mutation/pendingTurns.ts appwire-client/typescript/state/mutation/pendingEntries.ts appwire-client/typescript/state/mutation/pendingTurns.test.ts appwire-client/typescript/state/mutation/pendingEntries.test.ts cmd/evener-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts
git commit -m "feat(web-ui): carry pending createdAt in a never-pruned map; known-first shared ordering"
```

---

### Task 4: Promote passes the row's display text into the optimistic record

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/threads.ts` (interface line ~187 + action at ~3385)
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/queue/QueueStrip.tsx` (`handlePromote` + the row's onClick)
- Test: `cmd/evener-hub/frontend/src/panes/session/composer/queue/QueueStrip.test.tsx`

**Interfaces:**
- Produces: `export interface PromoteDisplayInput { text: string; skillNames?: readonly string[] }` in `stores/threads.ts`; `promoteQueuedAsSteer(ref: string, index: number, expectedEntryId: string, display: PromoteDisplayInput): Promise<void>`. Task 6's ghost body consumes the resulting `optimisticDisplay.input` through `inputPreview`.

- [ ] **Step 1: Write the failing test** — in `QueueStrip.test.tsx`, inside `describe("promote", ...)`, after the first test:

```tsx
test("promote passes the row's text (or the daemon preview placeholder) into the optimistic display", async () => {
  const fake = connectFakeClient();
  await hydrate(fake, "ref_a", {
    evener: {
      ref: "ref_a",
      capabilities: CAPABILITIES,
      queue: {
        revision: 0,
        depth: 2,
        ids: ["q1", "q2"],
        texts: ["hello", ""],
        preview: ["hello", "[image]"],
        skillNames: [["pkg:probe"], []],
      },
    },
  });
  fake.on("turn/promoteQueuedAsSteer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));
  renderStrip(defaultProps());

  const rows = await screen.findAllByRole("listitem");
  await act(async () => {
    fireEvent.click(within(rows[0]!).getByRole("button", { name: /steer now/i }));
  });
  await act(async () => {
    fireEvent.click(within(rows[1]!).getByRole("button", { name: /steer now/i }));
  });
  await refreshPendingTurnsProjection("ref_a");
  // The display input is observable where it lands: the optimistic promote
  // record's preview - the ghost's body (spec §3.2), not a wire param.
  const entries = pendingTurnEntries("ref_a", "promote");
  expect(entries).toEqual([
    expect.objectContaining({ text: "hello", skillNames: ["pkg:probe"] }),
    expect.objectContaining({ text: "[image]" }),
  ]);
});
```

Add `pendingTurnEntries` to the existing import from `./pendingTurnsStore` in this test file. If `queue.skillNames` is not a field the fake's wire shape accepts, drop it from the fixture and assert skillNames only in a threads-store-level test below.

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/composer/queue/QueueStrip.test.tsx -t "promote passes"`
Expected: FAIL — typecheck error (`promoteQueuedAsSteer` takes 3 args) and/or the entries assert fails (display payload is the wire params today, so the preview composes from an empty text).

- [ ] **Step 3: Implement.**

In `stores/threads.ts`, beside the `ThreadsStore` interface's `promoteQueuedAsSteer` line:

```ts
// The ghost's display input (steering-ghost spec §3): the row's full text
// - or the daemon's own preview placeholder when the row is image-only -
// plus its canonical skill selections, so the optimistic promote record
// previews the message it will become instead of the action's wire
// parameters.
export interface PromoteDisplayInput {
  text: string;
  skillNames?: readonly string[];
}
```

Interface line: `promoteQueuedAsSteer(ref: string, index: number, expectedEntryId: string, display: PromoteDisplayInput): Promise<void>;`

Action body:

```ts
async promoteQueuedAsSteer(ref, index, expectedEntryId, display) {
  // The entry id is the precondition that matters: it names the message being
  // promoted, so a queue that shifted underneath is caught without needing a
  // turn id that would only add a second way to fail.
  await enqueueMutation(
    ref,
    "turn/promoteQueuedAsSteer",
    { ref, index, expectedInstanceId: expectedInstanceID(ref), expectedEntryId },
    {
      method: "turn/promoteQueuedAsSteer",
      input: [
        ...(display.text !== "" ? [{ type: "text", text: display.text }] : []),
        ...(display.skillNames ?? []).map((name) => ({ type: "skill", name })),
      ],
    },
  );
},
```

In `QueueStrip.tsx`:

```ts
async function handlePromote(
  index: number,
  entryId: string,
  displayText: string,
  skillNames?: readonly string[],
): Promise<void> {
  // ...existing refusal/busy code unchanged...
  await threadsStore.getState().promoteQueuedAsSteer(sessionRef, index, entryId, {
    text: displayText,
    skillNames,
  });
  // ...existing catch/finally unchanged...
}
```

and in the row's Steer-now `onClick` (the row scope already holds `fullText`, `preview`, `entrySkillNames`):

```tsx
onClick={() => {
  if (entryId !== undefined) {
    // The ghost's display text: the row's full text, or the daemon's own
    // preview placeholder when the row is image-only (its whole content).
    const rowText = fullText ?? "";
    const displayText = rowText.trim() !== "" ? rowText : (preview?.[index] ?? "");
    void handlePromote(index, entryId, displayText, entrySkillNames);
  }
}}
```

Then sweep every remaining caller: `rg -n "promoteQueuedAsSteer\(" cmd/evener-hub/frontend/src` — expected hits are the interface, the action, and `QueueStrip.handlePromote` (done). Update any test or caller beyond those by adding a `display` argument consistent with the above (`{ text: "<row text>" }`).

- [ ] **Step 4: Run tests to verify pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/composer/queue/QueueStrip.test.tsx`
Expected: PASS (whole file, so the existing promote tests prove the wire params are unchanged).

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/stores/threads.ts cmd/evener-hub/frontend/src/panes/session/composer/queue/QueueStrip.tsx cmd/evener-hub/frontend/src/panes/session/composer/queue/QueueStrip.test.tsx
git commit -m "feat(web-ui): promote passes the row's display text into the optimistic record"
```

---

### Task 5: `UserMessageView` gains the `provisional` register

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/usermessageitem.module.css`
- Test: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.test.tsx`

**Interfaces:**
- Produces: `UserMessageView` prop `provisional?: string`. Presence does both provisional jobs at once: the string renders in the timestamp slot instead of the item time, and the `provisional` class applies (dashed `--accent` bubble edge, `opacity: 0.78`, dashed left railmark). Absence renders exactly today's output. Task 6 passes the caption through this prop.

- [ ] **Step 1: Write the failing tests** — in `UserMessageItem.test.tsx`, following that file's existing render style for `UserMessageView`:

```tsx
test("a provisional caption renders in the meta slot instead of the item time", () => {
  render(
    <UserMessageView
      item={{ id: "item_1", turnId: "turn_1", text: "held body", startedAt: "2026-09-21T16:00:00.000Z" }}
      opensExchange={false}
      provisional="Delivers when this step finishes · held 42s"
    />,
  );
  expect(screen.getByText("Delivers when this step finishes · held 42s")).toBeTruthy();
  expect(screen.queryByText(/16:00/)).toBeNull();
});

test("the provisional register marks the row for styling and tests", () => {
  render(<UserMessageView item={{ id: "item_1", turnId: "turn_1", text: "held body" }} provisional="Joining this turn · 0s" />);
  expect(document.querySelector('[data-testid="user-message-item"][data-provisional="true"]')).not.toBeNull();
});

test("absent provisional renders exactly today's output (regression pin)", () => {
  render(
    <UserMessageView item={{ id: "item_1", turnId: "turn_1", text: "delivered body", startedAt: "2026-09-21T16:00:00.000Z" }} />,
  );
  expect(document.querySelector('[data-testid="user-message-item"][data-provisional]')).toBeNull();
  expect(screen.getByTestId("user-bubble").textContent).toContain("delivered body");
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/messages/UserMessageItem.test.tsx`
Expected: FAIL — `provisional` prop does not exist.

- [ ] **Step 3: Implement.**

In `UserMessageItem.tsx`: add `provisional` to the props type with a doc comment (`/** Held-steer ghost caption (steering-ghost spec §2): presence applies the provisional register and renders in the meta slot; absence renders exactly the delivered form. */`), extend `CLASS` with `provisional: requireClass(styles.provisional, "usermessageitem.module.css", "provisional")` and `railmark: requireClass(styles.railmark, "usermessageitem.module.css", "railmark")`, and:

```tsx
  return (
    <div
      className={provisional === undefined ? CLASS.message : `${CLASS.message} ${CLASS.provisional}`}
      data-testid="user-message-item"
      data-opens-exchange={opensExchange ? "true" : undefined}
      data-provisional={provisional !== undefined ? "true" : undefined}
    >
      ...
        <div className={CLASS.header}>
          <span className={CLASS.name}>{name}</span>
          {provisional !== undefined ? (
            <span className={CLASS.time}>{provisional}</span>
          ) : (
            Number.isFinite(time) && (
              <span className={CLASS.time}>
                <MessageTimestamp value={time} />
              </span>
            )
          )}
          {actions !== undefined && <div className={CLASS.actions}>{actions}</div>}
        </div>
        <div className={CLASS.body} data-testid="user-bubble">
          <div className={CLASS.text}>{entityText ? <EntityText text={item.text} /> : item.text}</div>
          {provisional !== undefined && <span className={CLASS.railmark} aria-hidden="true" />}
          <ImageGallery images={item.images} />
        </div>
```

In `usermessageitem.module.css`, append:

```css
/* The provisional register a held steering ghost renders in
 * (docs/web-ui/specs/2026-09-20-steering-ghost-live-edge.md §2, mockup A):
 * the message it WILL become, marked not-yet-delivered. Dashed --accent
 * bubble edge, the whole row at reduced opacity, and a dashed left
 * railmark beside the bubble. Tokens only - no new colors. */
.provisional {
  opacity: 0.78;
}

.provisional .body {
  border: 1px dashed var(--accent);
  position: relative;
}

.provisional .railmark {
  position: absolute;
  left: -12px;
  top: 4px;
  bottom: 4px;
  width: 2px;
  background: repeating-linear-gradient(180deg, var(--accent) 0 4px, transparent 4px 8px);
  opacity: 0.55;
  border-radius: 1px;
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/messages/UserMessageItem.test.tsx`
Expected: PASS (whole file — the existing tests are the no-change regression pin).

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/usermessageitem.module.css cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.test.tsx
git commit -m "feat(web-ui): UserMessageView gains the provisional register"
```

---

### Task 6: The `HeldSteerStack` component

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerStack.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/heldsteerstack.module.css`
- Test: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerStack.test.tsx`

**Interfaces:**
- Consumes: `pendingEntryPreview` (Task 1), `PendingMethod "promote"` (Task 2), the known-first ordering (Task 3), `provisional` (Task 5), `formatElapsed` and `isTurnActive` from `@evener/appwire-client`, `usePendingTurnEntries` from `../../composer/queue/pendingTurnsStore`, `SessionNowContext` from `../../liveness`.
- Produces: `HeldSteerStack({ ref }: { ref: string }): JSX.Element | null`; `heldSteerEntries(entries: readonly PendingTurnEntry[]): PendingTurnEntry[]` (the one steer-family filter Session shares); `heldCaption(entry: PendingTurnEntry, turnActive: boolean, now: number): string`; `useHeldSteerEpoch(ref: string, entries: readonly PendingTurnEntry[]): number`. Tasks 7 and 8 consume these.

- [ ] **Step 1: Write the failing tests.** Create `HeldSteerStack.test.tsx`. Copy the harness from `QueueStrip.test.tsx` lines 1-113 as the base (imports trimmed to what this file uses): `CAPABILITIES`, `testThread`, `readResponse`, `connectFakeClient`, `hydrate`, plus `resetThreadsStoreForTests`/`setMutationStorageForTests`/`resetPendingTurnsStoreForTests`, `MutationOutboxIndexedDB`, `refreshPendingTurnsProjection`, `flushPendingTurnsProjectionForTests`, and a `seedHeld` helper modeled on PendingChips.test.tsx's `seedPending` (accepting `method`, `text`, `skillNames`, `attachments`; wire method map including `promote: "turn/promoteQueuedAsSteer"`), rendered inside `<SessionNowContext.Provider value={NOW}>` with a fixed `NOW`.

Pure caption tests first (no harness needed):

```tsx
import { heldCaption, heldSteerEntries } from "./HeldSteerStack";
import type { PendingTurnEntry } from "../../composer/queue/pendingReconcile";

const ENTRY = (state: PendingTurnEntry["state"], createdAt?: number): PendingTurnEntry => ({
  id: "m1",
  ref: "ref_a",
  method: "steer",
  text: "hi",
  imageCount: 0,
  skillNames: [],
  state,
  source: "optimistic",
  fromThisClient: true,
  ...(createdAt !== undefined ? { createdAt } : {}),
});

test("accepted with a turn running reads Delivers when this step finishes · held Ns", () => {
  expect(heldCaption(ENTRY("accepted", 1_000), true, 43_000)).toBe("Delivers when this step finishes · held 42s");
});

test("accepted with no turn reads Delivers with the next turn · held Ns", () => {
  expect(heldCaption(ENTRY("accepted", 1_000), false, 43_000)).toBe("Delivers with the next turn · held 42s");
});

test("submitting with a turn running reads Joining this turn · bare count", () => {
  expect(heldCaption(ENTRY("submitting", 1_000), true, 1_000)).toBe("Joining this turn · 0s");
});

test("submitting with no turn reads Delivers with the next turn · bare count", () => {
  expect(heldCaption(ENTRY("submitting", 1_000), false, 1_000)).toBe("Delivers with the next turn · 0s");
});

test("an unknown createdAt omits the count entirely - never NaN, never a false 0s", () => {
  expect(heldCaption(ENTRY("accepted", undefined), true, 43_000)).toBe("Delivers when this step finishes");
});

test("the count formats through formatElapsed (1m05s at 65s)", () => {
  expect(heldCaption(ENTRY("accepted", 1_000), true, 66_000)).toBe("Delivers when this step finishes · held 1m05s");
});

test("heldSteerEntries keeps steer/drain/promote and excludes send/queue/blockedUnknown/canceled", () => {
  const keep = ["steer", "drain", "promote"] as const;
  const entries = [
    ...keep.map((method) => ({ ...ENTRY("submitting"), method, id: method })),
    { ...ENTRY("submitting"), method: "send" as const, id: "s" },
    { ...ENTRY("submitting"), method: "queue" as const, id: "q" },
    { ...ENTRY("blockedUnknown"), id: "b" },
    { ...ENTRY("canceled"), id: "c" },
  ];
  expect(heldSteerEntries(entries).map((entry) => entry.id)).toEqual(["steer", "drain", "promote"]);
});
```

Component tests (harness + seeds; the default `testThread` status is `active`, so a seeded submitting steer reads the `Joining this turn` arm):

```tsx
test("renders one ghost per held entry with its body and caption", async () => {
  connectFakeClient();
  await hydrate(fake, "ref_a"); // status active; no queue
  await seedHeld("steer", "focus on the parser");
  render(
    <SessionNowContext.Provider value={NOW}>
      <HeldSteerStack ref="ref_a" />
    </SessionNowContext.Provider>,
  );
  expect(screen.getByTestId("held-steer-stack")).toBeTruthy();
  expect(screen.getByText("focus on the parser")).toBeTruthy();
  expect(screen.getByText(/joining this turn · 0s/i)).toBeTruthy();
});

test("a skill-only entry renders the skill marker, not an empty bubble", async () => {
  ... await seedHeld("steer", "", { skillNames: ["pkg:probe"] });
  ... expect(screen.getByText("[skill: pkg:probe]")).toBeTruthy();
});

test("an image-bearing entry shows the daemon's own placeholder plus count", async () => {
  ... await seedHeld("steer", "look", { attachments: [one image attachment] });
  ... expect(screen.getByText(/look \[image\]/)).toBeTruthy();
});

test("a blank composed drain body shows the stack-owned [queued messages] fallback", async () => {
  ... await seedHeld("drain", "");
  ... expect(screen.getByText("[queued messages]")).toBeTruthy();
});

test("the drain ghost's text refines to the authoritative combined input at the next hydrate", async () => {
  const id = await seedHeld("drain", "composer text");
  // Re-hydrate with the daemon's pendingMutations carrying the combined input.
  // Verify the wire field's actual name and location (evener.pendingMutations
  // or its sibling) against types.gen.ts's Thread shape before writing the
  // fixture - the contract under test is the ENTRY text refining, not the
  // fixture's spelling.
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      evener: {
        ref: "ref_a",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        pendingMutations: [
          {
            clientMutationId: id,
            method: "turn/drainAsSteer",
            input: [{ type: "text", text: "composer text plus the drained rows" }],
            executionState: "accepted",
            projectionState: "pending",
          },
        ],
      },
    }),
  );
  await threadsStore.getState().refreshThread("ref_a");
  ... render ...
  expect(screen.getByText("composer text plus the drained rows")).toBeTruthy();
});

test("another client's authoritative entry renders after this client's own", async () => {
  ... await seedHeld("steer", "mine"); // own, has createdAt
  // re-hydrate adding a foreign pendingMutations steer (no createdAt -> unknown bucket)
  ... render; const rows = screen.getAllByTestId("user-message-item");
  expect(within(rows[0]).getByText("mine")).toBeTruthy();
  expect(within(rows[1]).getByText("theirs")).toBeTruthy();
});

test("the ghost disappears once the transcript reflects its id", async () => {
  const id = await seedHeld("steer", "reflected steer");
  fake.on("thread/read", () =>
    readResponse("ref_a", {
      turns: [
        {
          id: "turn_1",
          status: "inProgress",
          itemsView: "full",
          items: [{ id: "item_1", turnId: "turn_1", type: "userMessage", text: "reflected steer", clientMutationId: id }],
        },
      ],
    }),
  );
  await threadsStore.getState().refreshThread("ref_a");
  ... render (after refreshPendingTurnsProjection) ...
  expect(screen.queryByTestId("held-steer-stack")).toBeNull();
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/messages/HeldSteerStack.test.tsx`
Expected: FAIL — module `./HeldSteerStack` does not exist.

- [ ] **Step 3: Implement.** Create `HeldSteerStack.tsx`:

```tsx
// HeldSteerStack: every held steering message rendered as the provisional
// user message it will become (steering-ghost spec §2). Reads the shared
// pendingTurnsStore (usePendingTurnEntries) and keeps ONLY the steer
// family - steer/drain/promote - except the terminal states QueueStrip
// owns rows for (blockedUnknown, canceled - the same filter the chips used
// to apply). Scope: every client's held steering renders, not only this
// client's own. Order arrives from reconcilePendingEntries' shared sort
// (§4) - this component never re-sorts.

import { formatElapsed, isTurnActive } from "@evener/appwire-client";
import type { JSX } from "react";
import { useContext, useEffect, useRef, useState } from "react";
import type { PendingTurnEntry } from "../../composer/queue/pendingReconcile";
import { pendingEntryPreview } from "../../composer/queue/queueDisplay";
import { usePendingTurnEntries } from "../../composer/queue/pendingTurnsStore";
import { useThreadsStore } from "../../../../stores/threads";
import { SessionNowContext } from "../../liveness";
import { UserMessageView } from "./UserMessageItem";
import styles from "./heldsteerstack.module.css";

// The one steer-family filter every consumer shares (Session's trailing-row
// predicate, the ghost stack, the announcements region).
export function heldSteerEntries(entries: readonly PendingTurnEntry[]): PendingTurnEntry[] {
  return entries.filter(
    (entry) =>
      (entry.method === "steer" || entry.method === "drain" || entry.method === "promote") &&
      entry.state !== "blockedUnknown" &&
      entry.state !== "canceled",
  );
}

// The caption state table (spec §4). Both arms read the thread STATUS TYPE
// (isTurnActive), never model.activeTurnId - the projector closes one turn
// row before opening the next, so the id goes false between inline turns
// while the run continues (#1330). The count is elapsed time from createdAt
// through the package's formatElapsed, and is omitted entirely when
// createdAt is unknown - never NaN, never a false 0s.
export function heldCaption(entry: PendingTurnEntry, turnActive: boolean, now: number): string {
  const label =
    entry.state === "accepted"
      ? turnActive
        ? "Delivers when this step finishes"
        : "Delivers with the next turn"
      : turnActive
        ? "Joining this turn"
        : "Delivers with the next turn";
  if (entry.createdAt === undefined) return label;
  const elapsed = formatElapsed(now - entry.createdAt);
  return entry.state === "accepted" ? `${label} · held ${elapsed}` : `${label} · ${elapsed}`;
}

// The held set's arrival counter, mirroring askDockStore's activationEpoch
// semantics: bumps when a new id joins the stack, never on removal (a
// departure is the announcements region's job, not new content). A pane
// reused across refs baselines the fresh ref's already-held entries without
// a bump - the reader opens scrolled to the bottom, so nothing is unseen.
export function useHeldSteerEpoch(ref: string, entries: readonly PendingTurnEntry[]): number {
  const [epoch, setEpoch] = useState(0);
  const seenRef = useRef<{ ref: string; ids: ReadonlySet<string> } | null>(null);
  useEffect(() => {
    const seen = seenRef.current;
    if (seen?.ref !== ref) {
      seenRef.current = { ref, ids: new Set(entries.map((entry) => entry.id)) };
      return;
    }
    const added = entries.some((entry) => !seen.ids.has(entry.id));
    if (!added) return;
    seenRef.current = {
      ref,
      ids: new Set([...seen.ids, ...entries.map((entry) => entry.id)]),
    };
    setEpoch((count) => count + 1);
  }, [ref, entries]);
  return epoch;
}

export function HeldSteerStack({ ref: sessionRef }: { ref: string }): JSX.Element | null {
  const allEntries = usePendingTurnEntries(sessionRef);
  const entries = heldSteerEntries(allEntries);
  const statusType = useThreadsStore((s) => s.threads.get(sessionRef)?.status.type);
  // The caption reads the same clock the liveness line does: Session's
  // 3s SessionNowContext tick, so no per-second machinery re-renders the
  // virtualized row and the caption is never itself a ticking live region.
  const now = useContext(SessionNowContext);
  if (entries.length === 0) return null;
  const turnActive = isTurnActive(statusType ?? "");
  return (
    <ul className={styles.stack} data-testid="held-steer-stack">
      {entries.map((entry) => (
        <li key={entry.id}>
          <UserMessageView
            item={{ id: entry.id, turnId: "", text: pendingEntryPreview(entry) || "[queued messages]" }}
            opensExchange={false}
            provisional={heldCaption(entry, turnActive, now)}
          />
        </li>
      ))}
    </ul>
  );
}
```

Notes: the `[queued messages]` fallback is stack-owned because `queueEntryPreviewText` must keep returning `""` for contentless input (matching key); a blank composed body is an empty-composer drain (a steer cannot submit empty), and widening `inputPreview` to carry images is out of scope — an image-bearing ghost shows its text or placeholder plus the count (`[image]`/`[N images]` via `imagePlaceholder`), and the image itself first appears on delivery.

`heldsteerstack.module.css`:

```css
/* Deliberately bare: each row is the same UserMessageView real messages
 * render, so the transcript's own rhythm styles the stack - only the list
 * reset lives here. The provisional register itself (dashed edge, opacity,
 * railmark) belongs to usermessageitem.module.css, applied by the
 * `provisional` prop. */
.stack {
  list-style: none;
  margin: 0;
  padding: 0;
}
```

The entries read is the plain two-line form (`usePendingTurnEntries`, then the filter) — never a hook call wrapped in `useMemo`'s callback (rules of hooks), and the store hook already caches its snapshot, so the per-render filter is cheap.

- [ ] **Step 4: Run tests to verify pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/messages/HeldSteerStack.test.tsx src/panes/session/transcript/messages/UserMessageItem.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerStack.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/heldsteerstack.module.css cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerStack.test.tsx
git commit -m "feat(web-ui): HeldSteerStack renders held steering ghosts"
```

---

### Task 7: Session composes the `live-edge` trailing row and the `heldEpoch` pill signal

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/Session.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts`
- Test: `cmd/evener-hub/frontend/src/panes/session/Session.test.tsx`

**Interfaces:**
- Consumes: `HeldSteerStack`, `heldSteerEntries`, `useHeldSteerEpoch` (Task 6); `usePendingTurnEntries`.
- Produces: trailing row id `"live-edge"` (AskDock keeps its row position and semantics inside it); `useTranscriptScroll` option `heldEpoch?: number`.

- [ ] **Step 1: Write the failing tests.** In `Session.test.tsx` (use the file's existing `connectFakeClient`/`readResponse`/render-with-`ClientProvider` harness, and extend the local `seedPendingSend` (line ~681) with a `seedPendingSteer(ref)` twin whose wire method is `turn/steer`):

```tsx
test("a held steer renders as the live-edge trailing row, under the AskDock when both exist", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  const { ... } = renderPane("ref_a"); // the file's established Session render helper
  await seedPendingSteer("ref_a");
  await waitFor(() => {
    const row = document.querySelector('[data-row-id="live-edge"]');
    expect(row).not.toBeNull();
    expect(row!.querySelector("[data-testid='held-steer-stack']")).not.toBeNull();
  });
  // With the ask dock pending too, both live in the ONE trailing row, dock first.
  fake.emitNotification(askPendingStatusChanged("ref_a"));
  await waitFor(() => {
    const stack = document.querySelector("[data-testid='held-steer-stack']");
    expect(stack).not.toBeNull();
    const dock = document.querySelector("[data-ask-response-dock]");
    expect(dock).not.toBeNull();
    // DOM order: the dock precedes the stack inside the same row.
    expect(dock!.compareDocumentPosition(stack!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});

test("no trailing row renders when neither an ask nor held steering exists", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  renderPane("ref_a");
  await waitFor(() => expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy());
  // Scoped to the trailing row's id: ordinary turn rows carry their own
  // data-row-id values and must keep rendering.
  expect(document.querySelector('[data-row-id="live-edge"]')).toBeNull();
});

test("a held steer without an ask still counts in renderedRowCount (the one-row-short regression)", async () => {
  // Mirror the ask-dock count test (~line 2185): spy useTranscriptScroll,
  // seed a steer only, assert the captured renderedRowCount grows by exactly
  // one when the ghost appears.
  const realUseTranscriptScroll = useTranscriptScrollModule.useTranscriptScroll;
  const captured: Array<{ renderedRowCount?: number; heldEpoch?: number }> = [];
  const spy = vi
    .spyOn(useTranscriptScrollModule, "useTranscriptScroll")
    .mockImplementation((options: Parameters<typeof realUseTranscriptScroll>[0]) => {
      captured.push({ renderedRowCount: options.renderedRowCount, heldEpoch: options.heldEpoch });
      return realUseTranscriptScroll(options);
    });
  try {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    renderPane("ref_a");
    await waitFor(() => {
      expect(captured.length).toBeGreaterThan(0);
      expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy();
    });
    const beforeSeed = captured.at(-1)?.renderedRowCount;
    await seedPendingSteer("ref_a");
    await waitFor(() => expect(screen.getByTestId("held-steer-stack")).toBeTruthy());
    const afterSeed = captured.at(-1)?.renderedRowCount;
    expect(afterSeed).toBe((beforeSeed ?? 0) + 1);
  } finally {
    spy.mockRestore();
  }
});

test("heldEpoch bumps on arrival only - never on removal", async () => {
  // Same spy shape as above, capturing options.heldEpoch instead.
  const realUseTranscriptScroll = useTranscriptScrollModule.useTranscriptScroll;
  const epochs: number[] = [];
  const spy = vi
    .spyOn(useTranscriptScrollModule, "useTranscriptScroll")
    .mockImplementation((options: Parameters<typeof realUseTranscriptScroll>[0]) => {
      epochs.push(options.heldEpoch ?? 0);
      return realUseTranscriptScroll(options);
    });
  try {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    renderPane("ref_a");
    await seedPendingSteer("ref_a");
    await waitFor(() => expect(screen.getByTestId("held-steer-stack")).toBeTruthy());
    expect(Math.max(...epochs)).toBe(1); // arrival bumped it exactly once
    // A departure: Stop-cancel the ref's unattempted rows through the same
    // real write every Stop path makes (PendingChips.test.tsx's shape).
    const storage = new MutationOutboxIndexedDB();
    await storage.cancelUnattempted("ref_a");
    storage.close();
    await refreshPendingTurnsProjection("ref_a");
    await flushPendingTurnsProjectionForTests();
    await waitFor(() => expect(screen.queryByTestId("held-steer-stack")).toBeNull());
    expect(Math.max(...epochs)).toBe(1); // removal never bumps the epoch
  } finally {
    spy.mockRestore();
  }
});

test("held steering renders only on a live surface: a notLoaded session shows no ghost", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { status: { type: "notLoaded" } }));
  renderPane("ref_a");
  await seedPendingSteer("ref_a");
  await flushPendingTurnsProjectionForTests();
  expect(document.querySelector("[data-row-id='live-edge']")).toBeNull();
});
```

Adapt helper names to the file's real ones (`rg -n "renderPane\|function render" Session.test.tsx`); the assertions above are the contract.

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/Session.test.tsx -t "live-edge"`
Expected: FAIL — no `live-edge` row; `heldEpoch` option missing.

- [ ] **Step 3: Implement.**

In `Session.tsx`, extend the `useBlockedMutationEntries` import line to also pull `usePendingTurnEntries` from `./composer/queue/pendingTurnsStore`, and import from `./transcript/messages/HeldSteerStack`: `HeldSteerStack`, `heldSteerEntries`, `useHeldSteerEpoch`. After the `askEpoch` line (line ~297), add:

```tsx
  // Held steering (steer/drain/promote ghosts - HeldSteerStack) renders as
  // the transcript's second trailing-row tenant, below the AskDock when both
  // exist (steering-ghost spec §1). Read ahead of the !model early return,
  // per the rules of hooks, same as askPending/askEpoch above.
  const pendingEntries = usePendingTurnEntries(ref);
  const heldSteers = useMemo(() => heldSteerEntries(pendingEntries), [pendingEntries]);
  const heldEpoch = useHeldSteerEpoch(ref, heldSteers);
  // One predicate decides the row and the count (spec §1): renderedRowCount
  // derives from the trailingRow handed to the list - the same form
  // TranscriptBody itself uses - so the count and the row cannot drift and
  // jump-to-bottom/append-follow cannot land one row short. Live-gated: no
  // ghost on a notLoaded surface (spec §6 accepts that window).
  const heldVisible = model !== undefined && model.status.type !== "notLoaded" && heldSteers.length > 0;
  const trailingRow =
    askPending || heldVisible
      ? {
          id: "live-edge",
          content: (
            <>
              {askPending && <AskDock ref={ref} />}
              {heldVisible && <HeldSteerStack ref={ref} />}
            </>
          ),
        }
      : undefined;
```

Change the `useTranscriptScroll` call: `renderedRowCount: renderRows.length + (trailingRow !== undefined ? 1 : 0)` (replacing `+ (askPending ? 1 : 0)`; keep the surrounding comment, extending it to name both tenants and the derived form), and add:

```tsx
    // ...and a held steer APPEARING is new content the same way an ask
    // activation is: it changes no turn/item shape, so the pill's edge
    // detector never sees it without this signal. Arrival is the only edge
    // (useHeldSteerEpoch never bumps on removal - departures are announced
    // by HeldSteerAnnouncements, not counted as new content).
    heldEpoch,
```

Change the `TranscriptBody` call: `trailingRow={trailingRow}` (replacing the inline ask-dock ternary; keep that comment block, adding that the stack renders below the dock when both exist).

In `useTranscriptScroll.ts`: add to the options interface after `askDockActivationEpoch`:

```ts
  /**
   * The held-steering ghost stack's arrival counter (Session's
   * useHeldSteerEpoch). Same role as askDockActivationEpoch: a held steer
   * APPEARING changes no turn/item shape, so this carries the arrival edge
   * for the pill. Removals never bump it (announced, not counted), so a
   * departure leaves the pill alone.
   */
  heldEpoch?: number;
```

Destructure `heldEpoch = 0` beside `askDockActivationEpoch = 0` (line ~771), and mirror the ask-dock epoch effect (declared right after it, ~line 1634):

```ts
  // The held-steer stack's arrival edge (see the option's doc comment):
  // keyed on the epoch, never on stack presence, and a pane opened with a
  // hold already in flight never fires it (initial mount scrolls to the
  // end; the first observation baselines without a bump).
  const prevHeldEpochRef = useRef(heldEpoch);
  useLayoutEffect(() => {
    const previous = prevHeldEpochRef.current;
    prevHeldEpochRef.current = heldEpoch;
    if (heldEpoch === previous || heldEpoch === 0) return;
    if (!initializedRef.current || wasAtBottomRef.current) return;
    setPillCount((count) => count + 1);
  }, [heldEpoch]);
```

Sweep for stale row-id assertions: `rg -n 'data-row-id="ask-dock"|row-id="ask-dock"' cmd/evener-hub/frontend/src` — update any test hit from `"ask-dock"` to `"live-edge"` (the row id changes for BOTH tenants; the dock's own `data-ask-response-dock` testid is unchanged).

- [ ] **Step 4: Run tests to verify pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/Session.test.tsx src/panes/session/transcript/flow`
Expected: PASS (the whole Session file: the ask-dock tests must stay green through the row-id change).

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/session/Session.tsx cmd/evener-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts cmd/evener-hub/frontend/src/panes/session/Session.test.tsx
git commit -m "feat(web-ui): Session composes the live-edge trailing row and heldEpoch pill signal"
```

---

### Task 8: `HeldSteerAnnouncements` — the announce-once live region

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerAnnouncements.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/Session.tsx` (mount beside AskDockAnnouncements)
- Test: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerAnnouncements.test.tsx`, plus a11y assertions in `cmd/evener-hub/frontend/src/panes/session/Session.test.tsx`

**Interfaces:**
- Consumes: `heldSteerEntries` (Task 6), `usePendingTurnEntries`, `useRecoveryEntries`/`useBlockedMutationEntries`/`useCanceledMutationEntries` (pendingTurnsStore), the thread model for reflection.
- Produces: `HeldSteerAnnouncements({ ref }: { ref: string }): JSX.Element`, live region `data-testid="held-steer-announcements"`.

- [ ] **Step 1: Write the failing tests.** `HeldSteerAnnouncements.test.tsx`, same harness as Task 6 (seeds through real storage + refresh + flush; hydrate for status/reflection):

```tsx
test("a held steer appearing is announced once", async () => {
  ... render <HeldSteerAnnouncements ref="ref_a" /> ...
  await seedHeld("steer", "hello");
  await waitFor(() => expect(screen.getByTestId("held-steer-announcements").textContent).toBe("Steering message held."));
  // Not re-announced on a re-render (no new transition).
  const before = screen.getByTestId("held-steer-announcements").textContent;
  await act(async () => {});
  expect(screen.getByTestId("held-steer-announcements").textContent).toBe(before);
});

test("delivery is announced once when the transcript reflects the id", async () => {
  const id = await seedHeld("steer", "hello");
  ... render ...
  await reHydrateWithTurnItem({ clientMutationId: id, text: "hello" });
  await waitFor(() => expect(...textContent).toBe("Steering message delivered."));
});

test.each([
  ["rejected", "Steering message was rejected. It's kept with the queue.", "transferToRecovery"],
  ["canceled by Stop", "Steering message was canceled by Stop. It's kept with the queue.", "cancelUnattempted"],
  ["delivery-uncertain", "Steering message delivery is uncertain. It's kept with the queue.", "markUnknown"],
])("a %s departure is announced once", async (_label, expected, seedKind) => {
  const id = await seedHeld("steer", "hello");
  ... render ...
  // seedKind names the STORAGE WRITE, not a literal recovery-kind string:
  // use the same real writes QueueStrip.test.tsx's seedRecovery /
  // seedCanceled / seedBlockedUnknown perform. For the rejected arm, look up
  // the actual MutationRecoveryKind value that models an acceptance rejection
  // in stores/mutationOutbox (the type's own doc comments) and pass it to
  // storage.transferToRecovery(id, kind, reason) - never an invented string.
  await refreshPendingTurnsProjection("ref_a");
  await waitFor(() => expect(...textContent).toBe(expected));
});

test("a failed-delivery vanish (record gone, nothing holds the id) is announced once", async () => {
  await seedHeld("steer", "hello");
  ... render ...
  // The post-settle vanish shape: the durable record is gone and no other
  // projection holds the id. A fresh empty IndexedDB snapshot reproduces it.
  globalThis.indexedDB = new IDBFactory();
  await refreshPendingTurnsProjection("ref_a");
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(...textContent).toBe("Steering message failed to deliver."));
});

test("the region stays silent on the timer's cadence", async () => {
  await seedHeld("steer", "hello");
  ... render inside <SessionNowContext.Provider value={NOW_A}> ...
  rerender inside <SessionNowContext.Provider value={NOW_B}> ...
  expect(...textContent).toBe("Steering message held."); // unchanged: the component never reads the clock
});
```

In `Session.test.tsx`, extend the region-placement assertions of the existing ask-dock test (~line 2174) or add a sibling:

```tsx
test("the held-steer announcements region lives outside the virtual list", async () => {
  ... seed a steer; assert screen.getByTestId("held-steer-announcements") exists and
  screen.getByTestId("transcript-virtual-list").contains(it) === false;
  and the ghost stack itself IS inside the list ...
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/messages/HeldSteerAnnouncements.test.tsx`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement.** Create `HeldSteerAnnouncements.tsx`:

```tsx
// HeldSteerAnnouncements is the held-steer surface's ONE aria-live region,
// mounted OUTSIDE the virtual list (Session.tsx, beside
// AskDockAnnouncements - that component's pattern; NOT extracted, because
// the two key on different transitions: ask counts an answered/total string
// keyed on an epoch, this keys on id-set appearance/disappearance). The
// ghost rows are virtualized, so an in-row region would re-announce on
// every scroll-away/scroll-back remount. Announces exactly once each:
// appearance, delivery (the delivered item replaces the ghost in place -
// the announcement is the only audible trace of the swap), and each
// non-delivery departure (spec §5). Never announces on the held-timer's
// cadence: this component does not read the clock at all.
import type { JSX } from "react";
import { useEffect, useRef, useState } from "react";
import { useThreadsStore } from "../../../../stores/threads";
import type { PendingTurnEntry } from "../../composer/queue/pendingReconcile";
import {
  useBlockedMutationEntries,
  useCanceledMutationEntries,
  usePendingTurnEntries,
  useRecoveryEntries,
} from "../../composer/queue/pendingTurnsStore";
import { heldSteerEntries } from "./HeldSteerStack";

// Mirrors pendingEntries.ts's reflectedMutationIds, which is not exported:
// the transcript's own record of which client mutation ids landed.
function reflectedIds(model: { queue?: { clientMutationIds?: readonly string[] }; turns?: ReadonlyArray<{ items: ReadonlyArray<{ clientMutationId?: string }> }> } | undefined): Set<string> {
  const ids = new Set<string>(model?.queue?.clientMutationIds ?? []);
  for (const turn of model?.turns ?? []) {
    for (const item of turn.items) {
      if (item.clientMutationId) ids.add(item.clientMutationId);
    }
  }
  return ids;
}

export function HeldSteerAnnouncements({ ref: sessionRef }: { ref: string }): JSX.Element {
  const held = heldSteerEntries(usePendingTurnEntries(sessionRef));
  const recovery = useRecoveryEntries(sessionRef);
  const blocked = useBlockedMutationEntries(sessionRef);
  const canceled = useCanceledMutationEntries(sessionRef);
  const model = useThreadsStore((s) => s.threads.get(sessionRef));
  const [announcement, setAnnouncement] = useState({ text: "", key: 0 });
  const prevRef = useRef<{ ref: string; ids: ReadonlySet<string> } | null>(null);

  useEffect(() => {
    const prev = prevRef.current;
    const ids = new Set(held.map((entry: PendingTurnEntry) => entry.id));
    prevRef.current = { ref: sessionRef, ids };
    // A pane reused across refs baselines silently, and the first
    // observation never announces (the reader who just opened the pane
    // scrolled to the bottom and sees the ghost).
    if (prev?.ref !== sessionRef) return;
    const announce = (text: string) => setAnnouncement((a) => ({ text, key: a.key + 1 }));
    const appeared = held.some((entry: PendingTurnEntry) => !prev.ids.has(entry.id));
    if (appeared) {
      announce("Steering message held.");
      return;
    }
    const disappeared = [...prev.ids].filter((id) => !ids.has(id));
    if (disappeared.length === 0) return;
    const reflected = reflectedIds(model);
    const why = (id: string): string => {
      if (reflected.has(id)) return "Steering message delivered.";
      if (recovery.some((record) => record.clientMutationId === id))
        return "Steering message was rejected. It's kept with the queue.";
      if (canceled.some((record) => record.clientMutationId === id))
        return "Steering message was canceled by Stop. It's kept with the queue.";
      if (blocked.some((record) => record.clientMutationId === id))
        return "Steering message delivery is uncertain. It's kept with the queue.";
      // Post-settle failed delivery: nothing holds the id anywhere. No
      // QueueStrip row exists for this departure (spec §5) - the
      // announcement is the message's only trace; the failed turn's error
      // surface is the explanation.
      return "Steering message failed to deliver.";
    };
    // One announcement per transition batch, classified by the first
    // departed id - a batch departure is one audible event, not a burst.
    announce(why(disappeared[0] ?? ""));
  }, [sessionRef, held, recovery, blocked, canceled, model]);

  return (
    <div role="status" aria-live="polite" data-testid="held-steer-announcements">
      <span key={announcement.key}>{announcement.text}</span>
    </div>
  );
}
```

Mount in `Session.tsx` right after `<AskDockAnnouncements ref={ref} />`:

```tsx
      {/* The held-steer ghosts' ONE live region, same rule as the ask
          dock's: outside the virtual list, announcing only real
          appearance/delivery/departure transitions. */}
      <HeldSteerAnnouncements ref={ref} />
```

(import it from `./transcript/messages/HeldSteerAnnouncements`).

- [ ] **Step 4: Run tests to verify pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/messages/HeldSteerAnnouncements.test.tsx src/panes/session/Session.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerAnnouncements.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/HeldSteerAnnouncements.test.tsx cmd/evener-hub/frontend/src/panes/session/Session.tsx cmd/evener-hub/frontend/src/panes/session/Session.test.tsx
git commit -m "feat(web-ui): announce held-steer arrivals and departures once"
```

---

### Task 9: Full gates and handback

**Files:** none new — verification only, plus fixes for anything the gates surface.

- [ ] **Step 1: Biome on every touched frontend file**

Run: `cd cmd/evener-hub/frontend && npx biome check --write src/panes/session/Session.tsx src/panes/session/pending/PendingChips.tsx src/panes/session/pending/PendingChips.test.tsx src/panes/session/composer/queue/QueueStrip.tsx src/panes/session/composer/queue/QueueStrip.test.tsx src/panes/session/composer/queue/pendingTurnsStore.ts src/panes/session/composer/queue/queueDisplay.ts src/panes/session/transcript/messages/HeldSteerStack.tsx src/panes/session/transcript/messages/HeldSteerStack.test.tsx src/panes/session/transcript/messages/heldsteerstack.module.css src/panes/session/transcript/messages/HeldSteerAnnouncements.tsx src/panes/session/transcript/messages/HeldSteerAnnouncements.test.tsx src/panes/session/transcript/messages/UserMessageItem.tsx src/panes/session/transcript/messages/UserMessageItem.test.tsx src/panes/session/transcript/messages/usermessageitem.module.css src/panes/session/transcript/flow/useTranscriptScroll.ts src/panes/session/Session.test.tsx`
Expected: clean exit 0.

- [ ] **Step 2: The canonical gate**

Run: `make test-web`
Expected: PASS — typecheck (both trees), unit tests, Biome gate.

- [ ] **Step 3: Lint + vet**

Run: `make lint && make vet`
Expected: PASS (golangci-lint across modules, naming, generated freshness, secret scan — none should trip; this change touches no Go).

- [ ] **Step 4: Browser geometry gate (Chrome-capable host)**

Run: `make test-web-browser`
Expected: PASS.

- [ ] **Step 5: Workspace state check**

Run: `git status --short`
Expected: clean (or only files this plan owns). If fixes came out of the gates, commit them as `fix(web-ui): <what the gate caught>`.

---

## Spec coverage map (self-review)

| Spec section | Where |
|---|---|
| §1 mount point, `live-edge` row, derived `renderedRowCount`, `heldEpoch` | Task 7 |
| §2 HeldSteerStack, `provisional` prop, scope (all clients), shared body helper, `[queued messages]`, image limitation, announce-once | Tasks 1, 5, 6, 8 |
| §3 promote mapping + display input | Tasks 2, 4 (steer/drain already exist) |
| §4 caption state table, formatElapsed timer, status-type arms, `createdAt` carrier, known-first sort | Tasks 3, 6 (caption unit tests pin all four arms) |
| §5 departures + announcements + the documented race (no repair) | Task 8; race stays documented in the spec, nothing to implement |
| §6 chips send-only, notLoaded window | Task 1 (chips), Task 7 (`heldVisible` gate + test) |
| Testing section, file by file | Tasks 1-8 as listed; the reload-mid-hold pins land as Task 3's map/sort tests (post-settle degrade, pre-settle re-discovery) + Task 6's unknown-`createdAt` caption omission |
| Files (expected) | matches the plan's File Structure table; `HeldSteerAnnouncements.tsx` path per spec, extraction deliberately not done (different transition keys — noted in the component header) |

Out of scope, unchanged: wire/protocol, daemon, TUI/mobile parity, cross-fade animation, QueueStrip's durable rows (recovery/blocked/canceled homes), the §5 pre-settle stuck record (pre-existing defect, daemon-side fix is a non-goal).
