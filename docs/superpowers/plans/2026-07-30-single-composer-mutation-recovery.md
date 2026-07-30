# Single-Composer Mutation Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the separate Recovery Drafts editor and make definitively rejected mutations editable and resendable through the session's one normal Composer while preserving durable ordering, attachments, and cross-tab safety.

**Architecture:** Keep the existing IndexedDB recovery store as an internal durable owner, but project rejected records into Composer and QueueStrip instead of a second textarea. Add focused atomic recovery-record operations, pure record-to-composer conversion helpers, one attachment hydration seam, queue-row presentation for records that are not active in Composer, and a route-aware conditional resend that rebuilds fresh Composer mutation parameters.

**Tech Stack:** React 19, TypeScript 6, Zustand, IndexedDB/fake-indexeddb, Vitest, Testing Library, Biome, Vite.

## Global Constraints

- A session has exactly one editable message textarea: Composer's `aria-label="Message"` field.
- `mutationOutcome: "notAccepted"` is safe to edit and resend; `blockedUnknown` is not.
- Existing unsent Composer text is never overwritten or automatically submitted.
- Recovered records retain text and attachment blobs across reload, crash, and tab handoff.
- Resending a recovery record remains one conditional IndexedDB transaction with one winning tab.
- Resend uses current Composer routing and current turn/queue CAS values; it must not replay a stale rejected payload unchanged.
- Later intent sequences cannot advance ahead of `blockedUnknown`.
- Do not change the IndexedDB database version or add a compatibility migration.
- Do not modify or stage any pre-existing dirty non-frontend files. Re-check
  `git status --short` before every commit because another process is actively
  changing and advancing this shared branch.

---

### Task 1: Add atomic recovery-record edit, discard, and rerouted-resend operations

**Files:**
- Modify: `cmd/serf-hub/frontend/src/stores/mutationOutbox.ts`
- Modify: `cmd/serf-hub/frontend/src/stores/mutationOutboxIndexedDB.ts`
- Test: `cmd/serf-hub/frontend/src/stores/mutationOutbox.test.ts`

**Interfaces:**
- Consumes: existing `MutationRecoveryRecord`, `MutationIntent`, `MutationAttachment`, and IndexedDB recovery store.
- Produces:

```ts
updateRecoveryInput(
  clientMutationId: string,
  input: InputItem[],
  attachments?: MutationAttachment[],
): Promise<MutationRecoveryRecord | undefined>

discardRecovery(clientMutationId: string): Promise<boolean>

resendRecovery(
  clientMutationId: string,
  intent: MutationIntent,
): Promise<MutationOutboxRecord | undefined>
```

- `resendRecovery` consumes the recovery record conditionally, but takes the replacement method, payload, optimistic display, target, thread, and blobs from `intent`. It preserves the recovery record's ordering relationship by allocating the next sequence in the same transaction and re-mints attachment presentation IDs.

- [ ] **Step 1: Write failing storage tests**

Add behavior tests with literal inputs:

```ts
test("recovery input edits replace text and attachments in one transaction", async () => {
  const store = new MutationOutboxIndexedDB({
    indexedDB,
    databaseName,
    createMutationId: idSequence(),
  });
  const oldAttachment = {
    presentationId: "old-presentation",
    name: "old.png",
    mediaType: "image/png",
    blob: new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }),
  };
  const newAttachment = {
    presentationId: "new-presentation",
    name: "new.png",
    mediaType: "image/png",
    blob: new Blob([new Uint8Array([9, 8, 7])], { type: "image/png" }),
  };
  const original = await store.enqueueIntent({
    ...intent("old text"),
    attachments: [oldAttachment],
  });
  await store.transferToRecovery(original.clientMutationId, "rejected");

  const updated = await store.updateRecoveryInput(
    original.clientMutationId,
    [
      { type: "text", text: "edited text" },
      { type: "image", mediaType: "image/png", data: "CQgH", name: "new.png" },
    ],
    [newAttachment],
  );

  expect(updated?.payload.input).toEqual([
    { type: "text", text: "edited text" },
    { type: "image", mediaType: "image/png", data: "CQgH", name: "new.png" },
  ]);
  expect(updated?.attachments.map((attachment) => attachment.name)).toEqual(["new.png"]);
});

test("discardRecovery removes only the selected durable draft", async () => {
  const store = new MutationOutboxIndexedDB({
    indexedDB,
    databaseName,
    createMutationId: idSequence(),
  });
  const first = await store.enqueueIntent(intent("first"));
  const second = await store.enqueueIntent(intent("second"));
  await store.transferToRecovery(first.clientMutationId, "rejected");
  await store.transferToRecovery(second.clientMutationId, "rejected");

  expect(await store.discardRecovery(first.clientMutationId)).toBe(true);
  expect(await store.getRecovery(first.clientMutationId)).toBeUndefined();
  expect(await store.getRecovery(second.clientMutationId)).toBeDefined();
});

test("recovery resend uses fresh Composer routing while retaining one winner", async () => {
  const createMutationId = idSequence();
  const origin = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId });
  const recovered = await origin.enqueueIntent(intent("stale"));
  await origin.transferToRecovery(recovered.clientMutationId, "rejected");
  const tabA = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId });
  const tabB = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId });
  const freshIntent: MutationIntent = {
    ...intent("edited"),
    method: "turn/queue",
    payload: {
      ref: TARGET,
      expectedTurnId: "turn-current",
      input: [{ type: "text", text: "edited" }],
    },
    optimisticDisplay: {
      method: "turn/queue",
      input: [{ type: "text", text: "edited" }],
    },
  };

  const winners = (
    await Promise.all([
      tabA.resendRecovery(recovered.clientMutationId, freshIntent),
      tabB.resendRecovery(recovered.clientMutationId, freshIntent),
    ])
  ).filter(Boolean);

  expect(winners).toHaveLength(1);
  expect(winners[0]).toMatchObject({
    method: "turn/queue",
    payload: { expectedTurnId: "turn-current" },
  });
});
```

The break each test catches is respectively: stale recovery payload survives an edit, blanking one draft deletes another, or resend replays stale method/CAS data or creates two outbox records.

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- src/stores/mutationOutbox.test.ts
```

Expected: tests fail because `updateRecoveryInput` and `discardRecovery` do not exist and `resendRecovery` still accepts only `{targetRef, threadId}`.

- [ ] **Step 3: Implement the minimal IndexedDB operations**

Change the operation discriminator and methods:

```ts
type MutationOutboxOperation =
  | "enqueueIntent"
  | "settleReceipt"
  | "transferToRecovery"
  | "updateRecoveryInput"
  | "discardRecovery"
  | "resendRecovery";
```

`updateRecoveryInput` must read the current record inside one `readwrite` transaction and update only that extant record:

```ts
const next: MutationRecoveryRecord = {
  ...record,
  payload: { ...record.payload, input },
  optimisticDisplay:
    record.optimisticDisplay && typeof record.optimisticDisplay === "object"
      ? { ...record.optimisticDisplay, input }
      : { method: record.method, input },
  attachments: attachments ?? record.attachments,
};
```

`discardRecovery` deletes only when the requested key exists. `resendRecovery` builds the new outbox record from `intent`, the newly allocated sequence/identity, and re-minted presentation IDs, then deletes the recovery record in the same transaction.

- [ ] **Step 4: Run storage tests and verify GREEN**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- src/stores/mutationOutbox.test.ts
```

Expected: the storage suite passes with no warnings.

- [ ] **Step 5: Commit Task 1**

```bash
git status --short
git add cmd/serf-hub/frontend/src/stores/mutationOutbox.ts \
  cmd/serf-hub/frontend/src/stores/mutationOutboxIndexedDB.ts \
  cmd/serf-hub/frontend/src/stores/mutationOutbox.test.ts
git commit -m "webui: add atomic composer recovery operations"
```

---

### Task 2: Convert durable records into normal Composer text and attachment state

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/composer/recovery/recoveryDraft.ts`
- Create: `cmd/serf-hub/frontend/src/panes/session/composer/recovery/recoveryDraft.test.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/attachments/useAttachments.ts`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/attachments/useAttachments.test.ts`

**Interfaces:**
- Consumes: `MutationRecoveryRecord`, the text/image `InputItem` payload, and `PendingAttachment`.
- Produces:

```ts
export interface RecoveredComposerDraft {
  text: string;
  attachments: PendingAttachment[];
}

export function recoveryComposerDraft(record: MutationRecoveryRecord): RecoveredComposerDraft

export function mergeRecoveryComposerDraft(
  currentText: string,
  currentAttachments: PendingAttachment[],
  recovered: RecoveredComposerDraft,
): RecoveredComposerDraft
```

and:

```ts
replaceWithSettled(items: PendingAttachment[]): void
```

on `UseAttachmentsResult`.

- [ ] **Step 1: Write failing pure conversion and merge tests**

Cover record projection and marker collision without sharing implementation helpers in expected values:

```ts
test("projects durable image input into settled Composer attachment state", () => {
  expect(recoveryComposerDraft(recoveryRecord({
    input: [
      { type: "text", text: "look [image 3]" },
      { type: "image", mediaType: "image/png", data: "AQID", name: "proof.png" },
    ],
  }))).toEqual({
    text: "look [image 3]",
    attachments: [{
      marker: 3,
      name: "proof.png",
      mediaType: "image/png",
      data: "AQID",
      pending: false,
    }],
  });
});

test("merging queue-like recovery keeps current text first and renumbers recovered markers", () => {
  const merged = mergeRecoveryComposerDraft(
    "current [image 1]",
    [settledAttachment(1, "current.png", "AAAA")],
    {
      text: "failed [image 1]",
      attachments: [settledAttachment(1, "failed.png", "BBBB")],
    },
  );

  expect(merged.text).toBe("current [image 1]\n\nfailed [image 2]");
  expect(merged.attachments.map((attachment) => attachment.marker)).toEqual([1, 2]);
});
```

The break is losing attachment bytes/markers on reload or producing two attachments with the same marker after Edit.

- [ ] **Step 2: Run pure helper tests and verify RED**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- src/panes/session/composer/recovery/recoveryDraft.test.ts
```

Expected: transform/import failure because `recoveryDraft.ts` does not exist.

- [ ] **Step 3: Implement record projection and merge**

Read text and image items from `record.payload.input`. Match images to marker numbers in text in order. When merging, reserve every marker already used by current text or current attachments, then rewrite each recovered marker to the next free number before joining text with one blank line.

Do not derive test expectations with these helpers. Do not compare content to reconcile mutation identity.

- [ ] **Step 4: Write failing attachment replacement tests**

Add:

```ts
test("replaceWithSettled hydrates recovery attachments without re-encoding", () => {
  const { result } = renderHook(() => useAttachments(editor));

  act(() => {
    result.current.replaceWithSettled([
      { marker: 4, name: "proof.png", mediaType: "image/png", data: "AQID", pending: false },
    ]);
  });

  expect(result.current.items).toEqual([
    { marker: 4, name: "proof.png", mediaType: "image/png", data: "AQID", pending: false },
  ]);
  expect(result.current.toInputAttachments()).toEqual([
    { name: "proof.png", mediaType: "image/png", data: "AQID" },
  ]);
});

test("a new attachment after recovery hydration uses the next marker", async () => {
  const editor = makeFakeEditor("[image 4]", "[image 4]".length);
  const { result } = renderHook(() => useAttachments(editor));
  act(() => {
    result.current.replaceWithSettled([
      { marker: 4, name: "old.png", mediaType: "image/png", data: "AQID", pending: false },
    ]);
    result.current.ingestFiles([makeFile("new.png")], () => {});
  });
  await flush();

  expect(editor.getText()).toBe("[image 4][image 5]");
  expect(result.current.items.map((item) => item.marker)).toEqual([4, 5]);
});
```

- [ ] **Step 5: Run attachment tests and verify RED**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- src/panes/session/composer/attachments/useAttachments.test.ts
```

Expected: type/runtime failure because `replaceWithSettled` is absent.

- [ ] **Step 6: Implement `replaceWithSettled`**

Copy the supplied settled items into hook state and set `nextMarkerRef.current`
to their maximum marker. Do not re-encode them or invent dimensions. Existing
remove, submit, and `toInputAttachments` behavior must operate on the hydrated
items unchanged.

- [ ] **Step 7: Run both focused suites and verify GREEN**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- \
  src/panes/session/composer/recovery/recoveryDraft.test.ts \
  src/panes/session/composer/attachments/useAttachments.test.ts
```

Expected: both suites pass.

- [ ] **Step 8: Commit Task 2**

```bash
git status --short
git add cmd/serf-hub/frontend/src/panes/session/composer/recovery/recoveryDraft.ts \
  cmd/serf-hub/frontend/src/panes/session/composer/recovery/recoveryDraft.test.ts \
  cmd/serf-hub/frontend/src/panes/session/composer/attachments/useAttachments.ts \
  cmd/serf-hub/frontend/src/panes/session/composer/attachments/useAttachments.test.ts
git commit -m "webui: project recovery records into composer drafts"
```

---

### Task 3: Expose route-aware recovery actions through the threads projection

**Files:**
- Modify: `cmd/serf-hub/frontend/src/stores/threads.ts`
- Test: `cmd/serf-hub/frontend/src/stores/threads.test.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.test.ts`

**Interfaces:**
- Consumes: Task 1 storage operations and existing `buildInput`,
  `durableAttachments`, `notifyMutationPersistence`, and
  `handleDiscoveredMutations`.
- Produces:

```ts
export type ComposerMutationRoute = "send" | "queue" | "steer" | "drain";

export async function updateRecoveryMutation(
  clientMutationId: string,
  targetRef: string,
  text: string,
  attachments: InputAttachment[],
): Promise<boolean>

export async function discardRecoveryMutation(
  clientMutationId: string,
  targetRef: string,
): Promise<boolean>

export async function resendRecoveryMutation(
  clientMutationId: string,
  targetRef: string,
  route: ComposerMutationRoute,
  text: string,
  attachments: InputAttachment[],
): Promise<MutationOutboxRecord | undefined>
```

- `resendRecoveryMutation` builds a fresh mutation intent from the current
  thread model: `turn/start` for send, `turn/queue` for queue,
  `turn/steer` for steer, and `turn/drainAsSteer` for drain, with current
  `expectedTurnId` and `expectedQueueRevision` where required.

- [ ] **Step 1: Write failing threads-store tests**

Add tests using the real fake IndexedDB runtime:

```ts
test("recovery resend rebuilds queue CAS values from the current thread", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: idSequence() });
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "stale" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "stale" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", {
    serf: {
      ...testThread("ref_a").serf,
      activeTurnId: "turn-current",
      queue: { revision: 7 },
    },
  }));
  await threadsStore.getState().ensureThread("ref_a");

  expect(
    await resendRecoveryMutation(original.clientMutationId, "ref_a", "queue", "edited", []),
  ).toBeDefined();
  expect((await storage.listOutbox("ref_a"))[0]).toMatchObject({
    method: "turn/queue",
    payload: {
      expectedTurnId: "turn-current",
      input: [{ type: "text", text: "edited" }],
    },
  });
});

test("discardRecoveryMutation removes the record and notifies its target projection", async () => {
  const storage = new MutationOutboxIndexedDB({ createMutationId: () => "mutation-a" });
  setMutationStorageForTests(storage);
  const original = await storage.enqueueIntent({
    targetRef: "ref_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "discard" }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text: "discard" }] },
  });
  await storage.transferToRecovery(original.clientMutationId, "rejected");

  expect(await discardRecoveryMutation(original.clientMutationId, "ref_a")).toBe(true);
  expect(await readMutationPersistence("ref_a")).toMatchObject({ recovery: [] });
});
```

- [ ] **Step 2: Run threads tests and verify RED**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- src/stores/threads.test.ts
```

Expected: new exported functions and route type are absent.

- [ ] **Step 3: Implement route-aware intent construction**

Extract one private helper used by both ordinary Composer actions and recovery
resend so method/payload construction cannot drift:

```ts
function composerMutationIntent(
  ref: string,
  route: ComposerMutationRoute,
  text: string,
  attachments: InputAttachment[],
): MutationIntent {
  const input = buildInput(text, attachments);
  const model = threadsStore.getState().threads.get(ref);
  const base = {
    targetRef: ref,
    threadId: model?.threadId,
    attachments: durableAttachments(attachments),
  };
  if (route === "send") {
    return {
      ...base,
      method: "turn/start",
      payload: { ref, input },
      optimisticDisplay: { method: "turn/start", input },
    };
  }
  const expectedTurnId = model?.activeTurnId ?? "";
  if (route === "queue" || route === "steer") {
    const method = route === "queue" ? "turn/queue" : "turn/steer";
    return {
      ...base,
      method,
      payload: { ref, expectedTurnId, input },
      optimisticDisplay: { method, input },
    };
  }
  const expectedQueueRevision = model?.queue?.revision ?? 0;
  return {
    ...base,
    method: "turn/drainAsSteer",
    payload: { ref, expectedTurnId, expectedQueueRevision, input },
    optimisticDisplay: { method: "turn/drainAsSteer", input },
  };
}
```

`resendRecovery` must overwrite `payload.clientMutationId` with the newly
allocated identity regardless of the replacement intent's payload.

Keep `interrupt`, queue-row promote/cancel, and non-Composer mutations on their
existing paths.

- [ ] **Step 4: Write failing projection-wrapper tests**

Add one test with three independent recovery records so each public wrapper
has a durable target:

```ts
test("recovery action wrappers refresh the durable projection", async () => {
  const storage = new MutationOutboxIndexedDB({
    createMutationId: (() => {
      let id = 0;
      return () => `mutation-${++id}`;
    })(),
  });
  setMutationStorageForTests(storage);
  const records = await Promise.all(
    ["edit", "discard", "resend"].map((text) =>
      storage.enqueueIntent({
        targetRef: "ref_a",
        method: "turn/start",
        payload: { ref: "ref_a", input: [{ type: "text", text }] },
        attachments: [],
        optimisticDisplay: { method: "turn/start", input: [{ type: "text", text }] },
      }),
    ),
  );
  await Promise.all(records.map((record) => storage.transferToRecovery(record.clientMutationId, "rejected")));
  await connect();
  await refreshPendingTurnsProjection("ref_a");

  expect(await updateRecoveryPendingTurn(records[0]!.clientMutationId, "ref_a", "edited", [])).toBe(true);
  expect(await discardRecoveryPendingTurn(records[1]!.clientMutationId, "ref_a")).toBe(true);
  expect(await resendRecoveryPendingTurn(records[2]!.clientMutationId, "ref_a", "send", "resent", [])).toBe(true);

  expect((await storage.getRecovery(records[0]!.clientMutationId))?.payload.input).toEqual([
    { type: "text", text: "edited" },
  ]);
  expect(await storage.getRecovery(records[1]!.clientMutationId)).toBeUndefined();
  expect(await storage.getRecovery(records[2]!.clientMutationId)).toBeUndefined();
});
```

Each wrapper must refresh `ref_a` after the operation so mounted consumers
observe the durable result.

- [ ] **Step 5: Run projection tests and verify RED**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- src/panes/session/composer/queue/pendingTurnsStore.test.ts
```

Expected: the new wrapper signatures/functions do not exist.

- [ ] **Step 6: Implement the wrappers and remove stale text-only API shape**

Replace `updateRecoveryText(record, text)` with the identity/ref/text/attachment
wrapper. Keep promise sequencing in Composer, not in the global store. Preserve
`useRecoveryEntries`, `useBlockedMutationEntries`, and refresh generation
guards.

- [ ] **Step 7: Run both suites and verify GREEN**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- \
  src/stores/threads.test.ts \
  src/panes/session/composer/queue/pendingTurnsStore.test.ts
```

Expected: both suites pass.

- [ ] **Step 8: Commit Task 3**

```bash
git status --short
git add cmd/serf-hub/frontend/src/stores/threads.ts \
  cmd/serf-hub/frontend/src/stores/threads.test.ts \
  cmd/serf-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts \
  cmd/serf-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.test.ts
git commit -m "webui: route recovered drafts through current composer state"
```

---

### Task 4: Present non-active recovery and blocked records in QueueStrip

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/queue/QueueStrip.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/queue/QueueStrip.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/pending/PendingChips.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/pending/PendingChips.test.tsx`

**Interfaces:**
- Consumes: `useRecoveryEntries`, `useBlockedMutationEntries`,
  `retryBlockedPendingTurn`, `discardRecoveryPendingTurn`, and Task 2 preview
  conversion.
- Produces two new QueueStrip props:

```ts
activeRecoveryId?: string
onEditRecovery(record: MutationRecoveryRecord): void
```

- Rejected records other than `activeRecoveryId` render in intent order with
  their normal message preview and `Edit message`.
- `blockedUnknown` renders `Delivery uncertain` and Retry, never Edit, Send,
  Steer, or Remove.
- `orphaned` renders `Destination deleted` and Copy, never Send or Retry.

- [ ] **Step 1: Write failing QueueStrip behavior tests**

Seed real IndexedDB records and refresh the projection:

```ts
test("a rejected record renders as an ordinary editable queued row", async () => {
  await seedRecovery("rejected", "not sent");
  renderStrip(defaultProps({ onEditRecovery }));

  const row = within(await screen.findByText("not sent").closest("li")!);
  await user.click(row.getByRole("button", { name: "Edit message" }));
  expect(onEditRecovery).toHaveBeenCalledWith(expect.objectContaining({ recoveryKind: "rejected" }));
  expect(screen.queryByText("Recovery drafts")).toBeNull();
});

test("blocked unknown has Retry but no sendable action", async () => {
  await seedBlockedUnknown("uncertain");
  renderStrip(defaultProps());

  const row = within(await screen.findByText("Delivery uncertain").closest("li")!);
  expect(row.getByRole("button", { name: "Retry" })).toBeTruthy();
  expect(row.queryByRole("button", { name: /edit|send|steer|remove/i })).toBeNull();
});
```

Also prove `activeRecoveryId` removes that record from QueueStrip, orphaned
records expose Copy only, and later records retain intent order:

```ts
test("active recovery is omitted while later and orphaned records retain order", async () => {
  const first = await seedRecovery("rejected", "active");
  await seedRecovery("rejected", "later");
  await seedRecovery("orphaned", "copy me");
  renderStrip(defaultProps({ activeRecoveryId: first.clientMutationId }));

  const rows = await screen.findAllByRole("listitem");
  expect(rows.map((row) => row.textContent)).toEqual([
    expect.stringContaining("later"),
    expect.stringContaining("copy me"),
  ]);
  expect(within(rows[1]!).getByRole("button", { name: "Copy" })).toBeTruthy();
  expect(within(rows[1]!).queryByRole("button", { name: /edit|send|retry/i })).toBeNull();
});
```

- [ ] **Step 2: Run QueueStrip tests and verify RED**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- src/panes/session/composer/queue/QueueStrip.test.tsx
```

Expected: recovery/blocked records are absent because only RecoveryTray reads
those hooks.

- [ ] **Step 3: Implement QueueStrip rows**

Reuse the existing row, row text, and row actions classes. The strip is visible
when daemon rows, optimistic queue rows, rejected records, blocked records, or
orphaned records exist. Count browser-only rows in the heading. Render `Steer
queue now` only when there is actual daemon/optimistic queued work; recovery
and blocked rows alone must not enable draining.

Retry calls `retryBlockedPendingTurn`. Copy uses the existing
`copyToClipboard` helper and the record's full text. Do not reproduce Export or
a second textarea.

- [ ] **Step 4: Write and run the PendingChips duplicate-presentation RED test**

Add:

```ts
test("blocked unknown is owned by QueueStrip rather than PendingChips", async () => {
  await seedBlockedUnknown("uncertain");
  render(<PendingChips sessionRef="ref_a" />);
  expect(screen.queryByText("uncertain")).toBeNull();
});
```

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- src/panes/session/pending/PendingChips.test.tsx
```

Expected: RED because PendingChips currently renders every non-queue method,
including `blockedUnknown`.

- [ ] **Step 5: Exclude `blockedUnknown` from PendingChips and verify GREEN**

Narrow the existing predicate:

```ts
function isOptimistic(entry: PendingTurnEntry): entry is OptimisticEntry {
  return entry.method !== "queue" && entry.state !== "blockedUnknown";
}
```

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- \
  src/panes/session/composer/queue/QueueStrip.test.tsx \
  src/panes/session/pending/PendingChips.test.tsx
```

Expected: both suites pass.

- [ ] **Step 6: Commit Task 4**

```bash
git status --short
git add cmd/serf-hub/frontend/src/panes/session/composer/queue/QueueStrip.tsx \
  cmd/serf-hub/frontend/src/panes/session/composer/queue/QueueStrip.test.tsx \
  cmd/serf-hub/frontend/src/panes/session/pending/PendingChips.tsx \
  cmd/serf-hub/frontend/src/panes/session/pending/PendingChips.test.tsx
git commit -m "webui: fold mutation recovery into queued messages"
```

---

### Task 5: Make Composer the sole editor for rejected mutations

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.integration.test.tsx`

**Interfaces:**
- Consumes: Tasks 2-4 helpers/hooks/actions.
- Produces Composer-local state:

```ts
const [activeRecoveryId, setActiveRecoveryId] = useState<string | null>(null);
const recoveryWrites = useRef(Promise.resolve());
```

and one activation function:

```ts
function activateRecovery(record: MutationRecoveryRecord): void
```

- The active recovery ID, not content equality, determines whether edits and
  send operate on a durable recovery record.

Add these local test helpers before the new Composer tests:

```ts
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function notAcceptedError(clientMutationId = "ignored-until-requested"): WireError {
  return new WireError("validation failed", -32602, {
    clientMutationId,
    mutationOutcome: "notAccepted",
    retryDisposition: "none",
  });
}

async function seedRejectedRecovery(
  storage: MutationOutboxIndexedDB,
  ref: string,
  text: string,
): Promise<MutationRecoveryRecord> {
  const outbox = await storage.enqueueIntent({
    targetRef: ref,
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref, input: [{ type: "text", text }] },
    attachments: [],
    optimisticDisplay: { method: "turn/start", input: [{ type: "text", text }] },
  });
  const recovered = await storage.transferToRecovery(outbox.clientMutationId, "rejected");
  if (!recovered) throw new Error("failed to seed recovery");
  return recovered;
}

async function seedRejectedRecoveryWithAttachment(
  storage: MutationOutboxIndexedDB,
  ref: string,
): Promise<MutationRecoveryRecord> {
  const input = [
    { type: "text" as const, text: "edit me [image 1]" },
    { type: "image" as const, mediaType: "image/png", data: "AQID", name: "proof.png" },
  ];
  const outbox = await storage.enqueueIntent({
    targetRef: ref,
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref, input },
    attachments: [{
      presentationId: "presentation-1",
      name: "proof.png",
      mediaType: "image/png",
      blob: new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }),
    }],
    optimisticDisplay: { method: "turn/start", input },
  });
  const recovered = await storage.transferToRecovery(outbox.clientMutationId, "rejected");
  if (!recovered) throw new Error("failed to seed attachment recovery");
  return recovered;
}

async function mountComposerWithHandle(ref: string, overrides: Partial<Thread> = {}) {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse(ref, overrides));
  await threadsStore.getState().ensureThread(ref);
  const view = render(
    <>
      <Toast />
      <Composer ref={ref} />
    </>,
  );
  return { fake, ...view };
}
```

When the rejection must echo the request identity, construct it inside the
FakeClient handler with `notAcceptedError(String(params.clientMutationId))`.

- [ ] **Step 1: Replace the existing recovery-tray expectation with the sole-editor RED test**

Change the current test named `an explicit rejection appears in the recovery
tray...` to:

```ts
test("an explicit rejection returns to the sole Composer textarea", async () => {
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  fake.on("turn/start", (params) => {
    throw notAcceptedError(String(params.clientMutationId));
  });

  await user.type(textarea(), "rejected draft");
  await user.click(submitButton());

  await waitFor(() => expect(textarea().value).toBe("rejected draft"));
  expect(screen.getAllByRole("textbox")).toEqual([textarea()]);
  expect(screen.queryByText("Recovery drafts")).toBeNull();
  expect(screen.queryByRole("textbox", { name: "Recovered message" })).toBeNull();
});
```

The break is the current second textarea: Composer clears and RecoveryTray
owns the text.

- [ ] **Step 2: Run the sole-editor test and verify RED**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- src/panes/session/composer/Composer.test.tsx \
  -t "an explicit rejection returns to the sole Composer textarea"
```

Expected: FAIL because the Message textarea is empty and the recovery textarea
exists.

- [ ] **Step 3: Implement automatic activation and durable editing**

Read `useRecoveryEntries(ref)`. When Composer has no active recovery and no
local text/attachments, activate the oldest rejected record:

```ts
const draft = recoveryComposerDraft(record);
setActiveRecoveryId(record.clientMutationId);
updateText(draft.text);
attachments.replaceWithSettled(draft.attachments);
clearDraft(ref);
```

While a recovery is active, enqueue every text/attachment edit onto
`recoveryWrites.current` and call `updateRecoveryPendingTurn` with the active
identity. Ordinary drafts continue using `writeDraft`. Removing the last
content calls `discardRecoveryPendingTurn`, clears `activeRecoveryId`, and
leaves an empty normal Composer.

- [ ] **Step 4: Add occupied-composer and queue-edit RED tests**

Add:

```ts
test("an occupied Composer is not overwritten by a later rejection", async () => {
  const rejection = deferred<never>();
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  let clientMutationId = "";
  fake.on("turn/start", (params) => {
    clientMutationId = String(params.clientMutationId);
    return rejection.promise;
  });

  await user.type(textarea(), "rejected draft");
  await user.click(submitButton());
  await waitFor(() => expect(textarea().value).toBe(""));
  await user.type(textarea(), "current work");
  rejection.reject(notAcceptedError(clientMutationId));

  await waitFor(() => expect(screen.getByText("rejected draft")).toBeTruthy());
  expect(textarea().value).toBe("current work");
});

test("editing a rejected queue row merges it through the normal Composer", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const user = userEvent.setup();
  await mountComposer("ref_a", { status: { type: "idle" } });
  await user.type(textarea(), "current work");
  await seedRejectedRecovery(storage, "ref_a", "rejected draft");
  await refreshPendingTurnsProjection("ref_a");

  const row = screen.getByText("rejected draft").closest("li");
  if (!row) throw new Error("missing rejected queue row");
  await user.click(within(row).getByRole("button", { name: "Edit message" }));

  await waitFor(() => expect(textarea().value).toBe("current work\n\nrejected draft"));
  expect(screen.getAllByRole("textbox")).toEqual([textarea()]);
  expect(screen.queryByText("rejected draft", { selector: "li *" })).toBeNull();
});
```

Use a controlled deferred FakeClient rejection rather than timers.

- [ ] **Step 5: Run the new tests and verify RED**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- src/panes/session/composer/Composer.test.tsx \
  -t "occupied Composer|editing a rejected queue row"
```

Expected: the current tray behavior fails both contracts.

- [ ] **Step 6: Implement explicit queue-row activation**

Pass `activeRecoveryId` and `onEditRecovery={activateRecovery}` to QueueStrip.
For explicit Edit, call `mergeRecoveryComposerDraft` with the live
`textRef.current` and attachment state, persist the merged recovery input
before making it active, clear the independent local draft key, then replace
Composer text/attachments. If attachments are still encoding, refuse Edit with
the same processing toast used by submit.

- [ ] **Step 7: Add route-aware resend and one-winner RED tests**

Add:

```ts
test("sending recovered text uses current Composer routing and clears once", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  await seedRejectedRecovery(storage, "ref_a", "retry me");
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", {
    status: { type: "active" },
    serf: {
      ref: "ref_a",
      capabilities: FULL_CAPABILITIES,
      activeTurnId: "turn-current",
      queue: { revision: 4 },
    },
  });
  fake.on("turn/queue", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));

  await waitFor(() => expect(textarea().value).toBe("retry me"));
  await user.click(submitButton());

  await waitFor(() => expect(fake.calls.some((call) => call.method === "turn/queue")).toBe(true));
  expect(fake.calls.find((call) => call.method === "turn/queue")?.params).toMatchObject({
    expectedTurnId: "turn-current",
    input: [{ type: "text", text: "retry me" }],
  });
});

test("a losing cross-tab recovered send does not issue a second request", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  const original = await seedRejectedRecovery(storage, "ref_a", "one winner");
  const user = userEvent.setup();
  const fake = await mountComposer("ref_a", { status: { type: "idle" } });
  await waitFor(() => expect(textarea().value).toBe("one winner"));
  const otherTab = new MutationOutboxIndexedDB();
  await otherTab.resendRecovery(original.clientMutationId, {
    targetRef: "ref_a",
    threadId: "thread_a",
    method: "turn/start",
    payload: { ref: "ref_a", input: [{ type: "text", text: "one winner" }] },
    attachments: [],
    optimisticDisplay: {
      method: "turn/start",
      input: [{ type: "text", text: "one winner" }],
    },
  });

  await user.click(submitButton());

  await waitFor(() => expect(textarea().value).toBe(""));
  expect(fake.calls.filter((call) => call.method === "turn/start")).toHaveLength(0);
});
```

- [ ] **Step 8: Run resend tests and verify RED**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- src/panes/session/composer/Composer.test.tsx \
  -t "sending recovered text|losing cross-tab"
```

Expected: current Composer has no recovered-send path.

- [ ] **Step 9: Implement recovered submit**

In `submitAction`, snapshot the active recovery ID. For an active record, the
`perform` callback calls `resendRecoveryPendingTurn` with the current route,
text, and attachments; otherwise it uses the existing ordinary store action.
Await queued recovery edits before resend. On the winning local commit, clear
only the unchanged submitted snapshot and submitted markers, then clear the
active recovery ID. On a losing tab, refresh and clear the unchanged recovered
snapshot without sending.

- [ ] **Step 10: Add remount, attachment removal, and blank-discard tests**

Prove with real fake IndexedDB:

```ts
test("recovered edits and attachment removal survive Composer remount", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  await seedRejectedRecoveryWithAttachment(storage, "ref_a");
  const user = userEvent.setup();
  const first = await mountComposerWithHandle("ref_a");
  await user.clear(textarea());
  await user.type(textarea(), "edited");
  await user.click(screen.getByRole("button", { name: "Remove proof.png" }));
  first.unmount();

  await mountComposer("ref_a");
  await waitFor(() => expect(textarea().value).toBe("edited"));
  expect(screen.queryByText("proof.png")).toBeNull();
});

test("blanking an attachment-free recovered draft discards it durably", async () => {
  const storage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(storage);
  await seedRejectedRecovery(storage, "ref_a", "discard me");
  const user = userEvent.setup();
  const first = await mountComposerWithHandle("ref_a");
  await waitFor(() => expect(textarea().value).toBe("discard me"));
  await user.clear(textarea());
  first.unmount();

  await mountComposer("ref_a");
  expect(textarea().value).toBe("");
  expect(screen.queryByText("discard me")).toBeNull();
});
```

- [ ] **Step 11: Run focused Composer suites and verify GREEN**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- \
  src/panes/session/composer/Composer.test.tsx \
  src/panes/session/composer/Composer.integration.test.tsx
```

Expected: both suites pass with no `Recovery drafts` UI.

- [ ] **Step 12: Commit Task 5**

```bash
git status --short
git add cmd/serf-hub/frontend/src/panes/session/composer/Composer.tsx \
  cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx \
  cmd/serf-hub/frontend/src/panes/session/composer/Composer.integration.test.tsx
git commit -m "webui: restore rejected mutations to the composer"
```

---

### Task 6: Delete the second editor and run the full frontend gates

**Files:**
- Delete: `cmd/serf-hub/frontend/src/panes/session/composer/recovery/RecoveryTray.tsx`
- Delete: `cmd/serf-hub/frontend/src/panes/session/composer/recovery/RecoveryTray.test.tsx`
- Delete: `cmd/serf-hub/frontend/src/panes/session/composer/recovery/recoverytray.module.css`
- Modify only if references remain: recovery imports in the frontend source tree.

**Interfaces:**
- Consumes: completed single-Composer and QueueStrip behavior.
- Produces: no `RecoveryTray`, `Recovered message`, or `Send recovered draft`
  source or rendered UI.

- [ ] **Step 1: Delete the legacy tray files**

Use `apply_patch` file deletion. Keep `recovery/recoveryDraft.ts` and its tests;
that module is the internal durable-record projection, not a user-facing tray.

- [ ] **Step 2: Verify no legacy UI references remain**

Run:

```bash
rg -n "RecoveryTray|Recovery drafts|Recovered message|Send recovered draft" \
  cmd/serf-hub/frontend/src --glob '!*.test.ts' --glob '!*.test.tsx'
```

Expected: no matches.

- [ ] **Step 3: Run focused mutation/composer tests**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test -- \
  src/stores/mutationOutbox.test.ts \
  src/stores/mutationDispatcher.test.ts \
  src/stores/threads.test.ts \
  src/panes/session/composer \
  src/panes/session/pending
```

Expected: all selected files pass.

- [ ] **Step 4: Run full frontend verification**

Run:

```bash
cd cmd/serf-hub/frontend
npm run test
npm run typecheck
npm run lint
npm run build
```

Expected: every command exits zero with no test failures, type errors, lint
errors, or build errors.

- [ ] **Step 5: Run repository hygiene checks**

Run:

```bash
git diff --check
git status --short
```

Expected: no whitespace errors. The two pre-existing unrelated dirty
`agent/*_fuzz_test.go` files remain untouched; only intended frontend changes
are staged for the final implementation commit.

- [ ] **Step 6: Commit Task 6**

```bash
git add cmd/serf-hub/frontend/src/panes/session/composer/recovery/RecoveryTray.tsx \
  cmd/serf-hub/frontend/src/panes/session/composer/recovery/RecoveryTray.test.tsx \
  cmd/serf-hub/frontend/src/panes/session/composer/recovery/recoverytray.module.css
git commit -m "webui: remove the recovery drafts tray"
```

- [ ] **Step 7: Inspect the complete implementation**

Run:

```bash
git log --oneline 91aebeeba..HEAD
git diff --stat 91aebeeba..HEAD
git status --short
```

Confirm every acceptance criterion in
`docs/superpowers/specs/2026-07-30-single-composer-mutation-recovery-design.md`
has a passing behavioral test and that unrelated user changes were not staged
or committed.
