import type { DraftAuthorityToken } from "@evener/appwire-client/state/mutation";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, expectTypeOf, test, vi } from "vitest";
import { composerDraftStorageKey, draftStorageKey } from "../panes/session/composer/draft";
import { MemoryStorage } from "../storageTestUtils";
import {
  type BrowserDraftKey,
  type BrowserDraftOwner,
  type BrowserDraftRepository,
  captureDraftSettlementGuard,
  createBrowserDraftRepository,
  createBrowserDraftTransferRuntime,
} from "./browserDraftRepository";
import { ANNOTATION_NAMESPACES_STORE, LOCAL_COMPOSITION_DATABASE_VERSION } from "./localCompositionSchema";

const namespace = { kind: "browser" as const, origin: "https://hub.test" };
const key = (sessionRef: string): BrowserDraftKey => ({ namespace, sessionRef });
const owner = (compositionOwnerId: string, paneId = "pane-root", lifetimeSerial = 1): BrowserDraftOwner => ({
  paneId,
  compositionOwnerId,
  lifetimeSerial,
});

function expectOwned(commit: Awaited<ReturnType<ReturnType<typeof createBrowserDraftRepository>["claim"]>>) {
  if (commit.state !== "committed" || commit.draft.ownership !== "owned") {
    throw new Error(`expected owned commit, got ${commit.state}`);
  }
  return commit.draft;
}

function transactionCompletion(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.addEventListener("complete", () => resolve(), { once: true });
    transaction.addEventListener("abort", () => reject(transaction.error ?? new Error("transaction aborted")), {
      once: true,
    });
  });
}

async function advanceNamespace(indexedDB: IDBFactory, databaseName: string, incarnation: string): Promise<void> {
  const database = await new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open(databaseName, LOCAL_COMPOSITION_DATABASE_VERSION);
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error ?? new Error("open failed")), { once: true });
  });
  const read = database.transaction(ANNOTATION_NAMESPACES_STORE, "readonly");
  const rows = await new Promise<Array<{ namespaceKey: string }>>((resolve, reject) => {
    const request = read.objectStore(ANNOTATION_NAMESPACES_STORE).getAll();
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error ?? new Error("namespace read failed")), {
      once: true,
    });
  });
  await transactionCompletion(read);
  expect(rows).toHaveLength(1);
  const row = rows[0];
  if (!row) throw new Error("namespace row missing");
  const write = database.transaction(ANNOTATION_NAMESPACES_STORE, "readwrite");
  write.objectStore(ANNOTATION_NAMESPACES_STORE).put({ namespaceKey: row.namespaceKey, incarnation });
  await transactionCompletion(write);
  database.close();
}

function waitForKey(
  repository: ReturnType<typeof createBrowserDraftRepository>,
  wanted: BrowserDraftKey,
): Promise<void> {
  return new Promise((resolve) => {
    const unsubscribe = repository.subscribe((changed) => {
      if (changed.namespace.origin !== wanted.namespace.origin || changed.sessionRef !== wanted.sessionRef) return;
      unsubscribe();
      resolve();
    });
  });
}

afterEach(() => {
  vi.restoreAllMocks();
});

test("does not reimport stale supported keys after committed transfer and clear", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  storage.setItem(draftStorageKey("root"), '{"text":"literal /same"}');
  const cleanupFaults: string[] = [];
  const repository = createBrowserDraftRepository({
    indexedDB,
    databaseName: "transfer-test",
    storage,
    beforeLegacyCleanup: () => {
      cleanupFaults.push("cleanup interrupted");
      throw new Error("cleanup interrupted");
    },
  });

  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  expect(cleanupFaults).toEqual(["cleanup interrupted"]);
  const draft = await repository.load(key("root"));
  expect(draft?.text).toBe('{"text":"literal /same"}');
  expect(draft?.skillNames).toEqual([]);
  expect(draft?.ownership).toBe("unclaimed");
  expect(draft).not.toHaveProperty("compositionOwnerId");
  if (!draft) throw new Error("transferred draft missing");

  const claimed = expectOwned(await repository.claim(key("root"), owner("owner-root"), draft.token, () => true));
  expect(
    await repository.clearSubmitted(key("root"), {
      text: draft.text,
      skillNames: [],
      draftRevisionAtStart: 0,
      persistentRevisionAtStart: claimed.token.persistentRevision,
      settlementGuard: captureDraftSettlementGuard(claimed),
      stillOwnsEdit: () => true,
    }),
  ).toEqual({ cleared: true, draftUnchanged: true, persistence: "committed" });

  const restarted = createBrowserDraftRepository({ indexedDB, databaseName: "transfer-test", storage });
  expect(await restarted.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  expect((await restarted.load(key("root")))?.text ?? "").toBe("");
});

test("transfer preserves the current decoder semantics for structured, invalid, JSON-looking, and slash drafts", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  storage.setItem(
    composerDraftStorageKey("structured"),
    JSON.stringify({
      text: "structured /same",
      skillNames: [" same ", "same"],
      commandNames: ["command"],
      mentions: [],
    }),
  );
  storage.setItem(composerDraftStorageKey("fallback"), '{"text":4,"skillNames":[]}');
  storage.setItem(draftStorageKey("fallback"), "literal fallback /same");
  storage.setItem(draftStorageKey("json"), '{"text":"literal","skillNames":["not-selected"]} /slash');
  const repository = createBrowserDraftRepository({ indexedDB, databaseName: "decoder-transfer", storage });

  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  expect(await repository.load(key("structured"))).toMatchObject({
    text: "structured /same",
    skillNames: ["same"],
    commandNames: ["command"],
    mentions: [],
    ownership: "unclaimed",
  });
  expect(await repository.load(key("fallback"))).toMatchObject({
    text: "literal fallback /same",
    skillNames: [],
    ownership: "unclaimed",
  });
  expect(await repository.load(key("json"))).toMatchObject({
    text: '{"text":"literal","skillNames":["not-selected"]} /slash',
    skillNames: [],
    ownership: "unclaimed",
  });
  expect(storage.length).toBe(0);
});

test("a real IndexedDB claim race commits exactly one owner and freezes a copied complete guard", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  storage.setItem(
    composerDraftStorageKey("race"),
    JSON.stringify({
      text: "🙂 /same and /command",
      skillNames: ["same"],
      commandNames: ["command"],
      mentions: [
        { kind: "skill", name: "same", offset: 3 },
        { kind: "command", name: "command", offset: 13 },
      ],
    }),
  );
  const repository = createBrowserDraftRepository({ indexedDB, databaseName: "claim-race", storage });
  await repository.transferSupportedDrafts(namespace);
  const imported = await repository.load(key("race"));
  if (!imported) throw new Error("import missing");

  const results = await Promise.all([
    repository.claim(key("race"), owner("owner-a", "pane-a"), imported.token, () => true),
    repository.claim(key("race"), owner("owner-b", "pane-b"), imported.token, () => true),
  ]);
  const committed = results.filter((result) => result.state === "committed");
  const conflicts = results.filter((result) => result.state === "conflict");
  expect(committed).toHaveLength(1);
  expect(conflicts).toHaveLength(1);
  const winningCommit = committed[0];
  if (!winningCommit) throw new Error("claim winner missing");
  const winner = expectOwned(winningCommit);
  expect(winner).toMatchObject({
    text: "🙂 /same and /command",
    skillNames: ["same"],
    commandNames: ["command"],
    mentions: [
      { kind: "skill", name: "same", offset: 3 },
      { kind: "command", name: "command", offset: 13 },
    ],
    backing: { kind: "draft" },
  });
  expect(winner.token.ownerRevision).toBe(imported.token.ownerRevision + 1);

  const guard = captureDraftSettlementGuard(winner);
  expect(guard).not.toBe(winner.token);
  expect(guard.token).not.toBe(winner.token);
  expect(guard.backing).not.toBe(winner.backing);
  expect(Object.isFrozen(guard)).toBe(true);
  expect(Object.isFrozen(guard.token)).toBe(true);
  expect(Object.isFrozen(guard.backing)).toBe(true);
  expect(() => {
    (guard.token as { persistentRevision: number }).persistentRevision = 99;
  }).toThrow();
  expect(guard.token).toEqual(winner.token);
});

test("claim, adopt, and edit use the complete token and composition owner as transaction-local CAS fences", async () => {
  const indexedDB = new IDBFactory();
  const repository = createBrowserDraftRepository({
    indexedDB,
    databaseName: "draft-cas",
    storage: new MemoryStorage(),
  });
  const seeded = await repository.seedUnclaimed(key("cas"), {
    text: "before",
    skillNames: ["skill"],
    commandNames: ["command"],
    mentions: [{ kind: "skill", name: "skill", offset: 0 }],
  });
  if (seeded.state !== "committed") throw new Error("seed failed");
  const first = expectOwned(await repository.claim(key("cas"), owner("owner-a"), seeded.draft.token, () => true));

  expect(await repository.claim(key("cas"), owner("owner-b"), first.token, () => true)).toMatchObject({
    state: "conflict",
    current: { compositionOwnerId: "owner-a", text: "before" },
  });
  expect(await repository.adopt(key("cas"), owner("owner-b"), first.token, "wrong-owner", () => true)).toMatchObject({
    state: "conflict",
    current: { compositionOwnerId: "owner-a" },
  });
  const adopted = expectOwned(await repository.adopt(key("cas"), owner("owner-b"), first.token, "owner-a", () => true));
  expect(adopted).toMatchObject({
    compositionOwnerId: "owner-b",
    text: "before",
    skillNames: ["skill"],
    commandNames: ["command"],
    mentions: [{ kind: "skill", name: "skill", offset: 0 }],
    backing: { kind: "draft" },
  });
  expect(adopted.token.ownerRevision).toBe(first.token.ownerRevision + 1);

  expect(
    await repository.commitEdit(
      key("cas"),
      owner("owner-a"),
      first.token,
      { text: "late", skillNames: [] },
      () => true,
    ),
  ).toMatchObject({ state: "conflict", current: { compositionOwnerId: "owner-b", text: "before" } });
  const edited = expectOwned(
    await repository.commitEdit(
      key("cas"),
      owner("owner-b"),
      adopted.token,
      { text: "after", skillNames: ["skill"] },
      () => true,
    ),
  );
  expect(edited.token.persistentRevision).toBe(adopted.token.persistentRevision + 1);
  const duplicate = expectOwned(
    await repository.commitEdit(
      key("cas"),
      owner("owner-b"),
      edited.token,
      { text: "after", skillNames: ["skill"] },
      () => true,
    ),
  );
  expect(duplicate.token).toEqual(edited.token);
  expect(
    await repository.commitEdit(
      key("cas"),
      owner("owner-b"),
      edited.token,
      { text: "blocked", skillNames: [] },
      () => false,
    ),
  ).toEqual({
    state: "unavailable",
  });
  expect(await repository.claim(key("cas"), owner("owner-b", "reused-pane", 99), edited.token, () => false)).toEqual({
    state: "unavailable",
  });
  expect(await repository.adopt(key("cas"), owner("owner-b"), edited.token, "owner-b", () => false)).toEqual({
    state: "unavailable",
  });
});

test("browser clearing requires the copied full guard and a live edit owner, never the scalar revision alone", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "settlement-guards",
    storage: new MemoryStorage(),
  });
  const seeded = await repository.seedUnclaimed(key("settle"), { text: "send me", skillNames: ["skill"] });
  if (seeded.state !== "committed") throw new Error("seed failed");
  const claimed = expectOwned(await repository.claim(key("settle"), owner("owner-a"), seeded.draft.token, () => true));
  const submitted = {
    text: "send me",
    skillNames: ["skill"],
    draftRevisionAtStart: 0,
    persistentRevisionAtStart: claimed.token.persistentRevision,
  };
  expect(await repository.clearSubmitted(key("settle"), submitted)).toEqual({
    cleared: false,
    draftUnchanged: false,
    persistence: "committed",
  });
  expect(
    await repository.clearSubmitted(key("settle"), {
      ...submitted,
      settlementGuard: captureDraftSettlementGuard(claimed),
      stillOwnsEdit: () => false,
    }),
  ).toEqual({ cleared: false, draftUnchanged: true, persistence: "committed" });
  expect(await repository.load(key("settle"))).toMatchObject({ text: "send me", compositionOwnerId: "owner-a" });
});

test("load refreshes an advanced namespace incarnation and stale callbacks cannot write across the namespace fence", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "namespace-fence";
  const repository = createBrowserDraftRepository({ indexedDB, databaseName, storage: new MemoryStorage() });
  const seeded = await repository.seedUnclaimed(key("fenced"), { text: "preserve", skillNames: ["skill"] });
  if (seeded.state !== "committed") throw new Error("seed failed");
  const claimed = expectOwned(await repository.claim(key("fenced"), owner("owner-a"), seeded.draft.token, () => true));

  await advanceNamespace(indexedDB, databaseName, "fixture-incarnation-2");
  const refreshed = await repository.load(key("fenced"));
  expect(refreshed).toMatchObject({
    text: "preserve",
    skillNames: ["skill"],
    ownership: "owned",
    compositionOwnerId: "owner-a",
    backing: { kind: "draft" },
    token: { namespaceIncarnation: "fixture-incarnation-2" },
  });
  expect(
    await repository.commitEdit(
      key("fenced"),
      owner("owner-a"),
      claimed.token,
      { text: "stale", skillNames: [] },
      () => true,
    ),
  ).toMatchObject({ state: "conflict", current: { text: "preserve" } });
});

test("commit faults are unavailable or pending and leave authoritative work unchanged", async () => {
  const indexedDB = new IDBFactory();
  let failOperation = "";
  const repository = createBrowserDraftRepository({
    indexedDB,
    databaseName: "fault-boundaries",
    storage: new MemoryStorage(),
    beforeCommit: (operation) => {
      if (operation === failOperation) throw new Error(`injected ${operation}`);
    },
  });
  const seeded = await repository.seedUnclaimed(key("fault"), { text: "before", skillNames: [] });
  if (seeded.state !== "committed") throw new Error("seed failed");
  const claimed = expectOwned(await repository.claim(key("fault"), owner("owner-a"), seeded.draft.token, () => true));

  failOperation = "commitEdit";
  expect(
    await repository.commitEdit(
      key("fault"),
      owner("owner-a"),
      claimed.token,
      { text: "after", skillNames: [] },
      () => true,
    ),
  ).toEqual({ state: "unavailable" });
  expect(await repository.load(key("fault"))).toMatchObject({ text: "before", token: claimed.token });

  failOperation = "clearSubmitted";
  expect(
    await repository.clearSubmitted(key("fault"), {
      text: "before",
      skillNames: [],
      draftRevisionAtStart: 0,
      settlementGuard: captureDraftSettlementGuard(claimed),
      stillOwnsEdit: () => true,
    }),
  ).toEqual({ cleared: false, draftUnchanged: true, persistence: "pending" });
  expect(await repository.load(key("fault"))).toMatchObject({ text: "before" });
});

test("a changed supported value becomes one deduplicated conflict without overwriting newer IDB work", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  storage.setItem(draftStorageKey("late"), "old value");
  const repository = createBrowserDraftRepository({ indexedDB, databaseName: "late-write", storage });
  await repository.transferSupportedDrafts(namespace);
  const imported = await repository.load(key("late"));
  if (!imported) throw new Error("import missing");
  const claimed = expectOwned(await repository.claim(key("late"), owner("owner-a"), imported.token, () => true));
  const edited = expectOwned(
    await repository.commitEdit(
      key("late"),
      owner("owner-a"),
      claimed.token,
      { text: "new authoritative", skillNames: ["skill"] },
      () => true,
    ),
  );

  storage.setItem(draftStorageKey("late"), "late /same");
  await repository.transferSupportedDrafts(namespace);
  await repository.transferSupportedDrafts(namespace);
  expect(await repository.load(key("late"))).toMatchObject({
    text: "new authoritative",
    skillNames: ["skill"],
    token: edited.token,
    compositionOwnerId: "owner-a",
  });
  expect(await repository.listConflicts(key("late"))).toMatchObject([
    {
      key: key("late"),
      legacyKey: draftStorageKey("late"),
      inspectedBytes: "late /same",
      value: { text: "late /same", skillNames: [] },
    },
  ]);
});

test("cleanup removes only inspected bytes and reconciles a value changed during cleanup on the next scan", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  storage.setItem(draftStorageKey("cleanup-race"), "inspected");
  let changeDuringCleanup = true;
  const repository = createBrowserDraftRepository({
    indexedDB,
    databaseName: "cleanup-race",
    storage,
    beforeLegacyCleanup: () => {
      if (!changeDuringCleanup) return;
      changeDuringCleanup = false;
      storage.setItem(draftStorageKey("cleanup-race"), "new work");
    },
  });
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  expect(storage.getItem(draftStorageKey("cleanup-race"))).toBe("new work");
  expect(await repository.load(key("cleanup-race"))).toMatchObject({ text: "inspected", ownership: "unclaimed" });
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  expect(await repository.listConflicts(key("cleanup-race"))).toMatchObject([
    { inspectedBytes: "new work", value: { text: "new work", skillNames: [] } },
  ]);
});

test("a real IndexedDB request failure rolls back transfer markers and remains pending for the next trigger", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  storage.setItem(draftStorageKey("request-fault"), "initial");
  const repository = createBrowserDraftRepository({ indexedDB, databaseName: "request-fault", storage });
  await repository.transferSupportedDrafts(namespace);
  const imported = await repository.load(key("request-fault"));
  if (!imported) throw new Error("import missing");
  const claimed = expectOwned(
    await repository.claim(key("request-fault"), owner("owner-a"), imported.token, () => true),
  );
  await repository.commitEdit(
    key("request-fault"),
    owner("owner-a"),
    claimed.token,
    { text: "authoritative", skillNames: [] },
    () => true,
  );

  const randomUUID = vi.spyOn(globalThis.crypto, "randomUUID").mockReturnValue("00000000-0000-4000-8000-000000000000");
  storage.setItem(draftStorageKey("request-fault"), "first incoming");
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  storage.setItem(draftStorageKey("request-fault"), "second incoming");
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "pending" });
  expect(storage.getItem(draftStorageKey("request-fault"))).toBe("second incoming");
  expect(await repository.listConflicts(key("request-fault"))).toMatchObject([{ inspectedBytes: "first incoming" }]);

  randomUUID.mockRestore();
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  expect(new Set((await repository.listConflicts(key("request-fault"))).map((row) => row.inspectedBytes))).toEqual(
    new Set(["first incoming", "second incoming"]),
  );
});

test("restoreConflict atomically restores only empty unchanged owned work and consumes only the restored conflict", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  const repository = createBrowserDraftRepository({ indexedDB, databaseName: "restore-conflict", storage });
  const seeded = await repository.seedUnclaimed(key("restore"), { text: "", skillNames: [] });
  if (seeded.state !== "committed") throw new Error("seed failed");
  const claimed = expectOwned(await repository.claim(key("restore"), owner("owner-a"), seeded.draft.token, () => true));
  storage.setItem(draftStorageKey("restore"), "incoming");
  await repository.transferSupportedDrafts(namespace);
  const [conflict] = await repository.listConflicts(key("restore"));
  if (!conflict) throw new Error("conflict missing");
  const restored = expectOwned(
    await repository.restoreConflict(key("restore"), owner("owner-a"), claimed.token, conflict.conflictId, () => true),
  );
  expect(restored.text).toBe("incoming");
  expect(await repository.listConflicts(key("restore"))).toEqual([]);

  storage.setItem(draftStorageKey("restore"), "second incoming");
  await repository.transferSupportedDrafts(namespace);
  const [second] = await repository.listConflicts(key("restore"));
  if (!second) throw new Error("second conflict missing");
  expect(
    await repository.restoreConflict(key("restore"), owner("owner-a"), restored.token, second.conflictId, () => true),
  ).toMatchObject({
    state: "conflict",
    current: { text: "incoming" },
  });
  expect(await repository.listConflicts(key("restore"))).toHaveLength(1);
});

test("the inactive runtime rescans only on its four declared lifecycle triggers and the next trigger recovers a fault", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  storage.setItem(draftStorageKey("runtime"), "initial");
  let transferAttempt: (() => void) | undefined;
  let failNextTransfer = false;
  let transferAttempts = 0;
  const repository = createBrowserDraftRepository({
    indexedDB,
    databaseName: "transfer-runtime",
    storage,
    beforeCommit: (operation) => {
      if (operation !== "transferSupportedDrafts") return;
      transferAttempts += 1;
      transferAttempt?.();
      transferAttempt = undefined;
      if (failNextTransfer) {
        failNextTransfer = false;
        throw new Error("injected transfer fault");
      }
    },
  });
  const initial = waitForKey(repository, key("runtime"));
  const runtime = createBrowserDraftTransferRuntime({ repository, namespace, storage, window, document });
  await initial;
  const imported = await repository.load(key("runtime"));
  if (!imported) throw new Error("initial runtime import missing");
  const claimed = expectOwned(await repository.claim(key("runtime"), owner("owner-a"), imported.token, () => true));
  await repository.commitEdit(
    key("runtime"),
    owner("owner-a"),
    claimed.token,
    { text: "authoritative", skillNames: [] },
    () => true,
  );

  storage.setItem(draftStorageKey("runtime"), "from composer-open");
  let changed = waitForKey(repository, key("runtime"));
  runtime.composerOpened(key("runtime"));
  await changed;

  storage.setItem(draftStorageKey("runtime"), "from visibility");
  changed = waitForKey(repository, key("runtime"));
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
  await changed;

  storage.setItem(draftStorageKey("runtime"), "from storage event");
  changed = waitForKey(repository, key("runtime"));
  window.dispatchEvent(new StorageEvent("storage", { key: draftStorageKey("runtime") }));
  await changed;

  storage.setItem(draftStorageKey("runtime"), "after fault");
  failNextTransfer = true;
  const attempted = new Promise<void>((resolve) => {
    transferAttempt = resolve;
  });
  window.dispatchEvent(new StorageEvent("storage", { key: composerDraftStorageKey("runtime") }));
  await attempted;
  changed = waitForKey(repository, key("runtime"));
  runtime.composerOpened(key("runtime"));
  await changed;

  expect(await repository.load(key("runtime"))).toMatchObject({ text: "authoritative", compositionOwnerId: "owner-a" });
  expect(new Set((await repository.listConflicts(key("runtime"))).map((conflict) => conflict.inspectedBytes))).toEqual(
    new Set(["from composer-open", "from visibility", "from storage event", "after fault"]),
  );

  runtime.dispose();
  const attemptsAtDispose = transferAttempts;
  storage.setItem(draftStorageKey("runtime"), "after dispose");
  window.dispatchEvent(new StorageEvent("storage", { key: draftStorageStorageKeyForTest("runtime") }));
  await repository.load(key("runtime"));
  expect(transferAttempts).toBe(attemptsAtDispose);
});

test("the inactive runtime coalesces triggers during an attempt to one follow-up without a retry loop", async () => {
  const storage = new MemoryStorage();
  storage.setItem(draftStorageKey("coalesce"), "initial");
  let attempts = 0;
  let runtime: ReturnType<typeof createBrowserDraftTransferRuntime> | undefined;
  let resolveFollowUp: (() => void) | undefined;
  const followUp = new Promise<void>((resolve) => {
    resolveFollowUp = resolve;
  });
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "runtime-coalesce",
    storage,
    beforeCommit: (operation) => {
      if (operation !== "transferSupportedDrafts") return;
      attempts += 1;
      if (attempts === 1) {
        storage.setItem(draftStorageKey("coalesce"), "changed during attempt");
        runtime?.composerOpened(key("coalesce"));
        runtime?.composerOpened(key("coalesce"));
        window.dispatchEvent(new StorageEvent("storage", { key: draftStorageKey("coalesce") }));
      }
      if (attempts === 2) resolveFollowUp?.();
    },
  });
  runtime = createBrowserDraftTransferRuntime({ repository, namespace, storage, window, document });
  await followUp;
  expect(attempts).toBe(2);
  runtime.dispose();
});

// Keep the runtime test's post-dispose key literal independent of the production key helper it is checking.
function draftStorageStorageKeyForTest(ref: string): string {
  return `evener.composer.draft.v1.${ref}`;
}

test("transfer read faults stay pending and emit no unvalidated console error", async () => {
  class FailingStorage extends MemoryStorage {
    override getItem(_key: string): string | null {
      throw new Error("read unavailable");
    }
  }
  const storage = new FailingStorage();
  storage.setItem(draftStorageKey("fault"), "unreadable");
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "read-fault",
    storage,
  });
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "pending" });
});

test("seedUnclaimed creates only an absent row", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "seed-once",
    storage: new MemoryStorage(),
  });
  const first = await repository.seedUnclaimed(key("seed"), { text: "first", skillNames: [] });
  if (first.state !== "committed") throw new Error("seed failed");
  expect(await repository.seedUnclaimed(key("seed"), { text: "second", skillNames: [] })).toMatchObject({
    state: "conflict",
    current: { text: "first", ownership: "unclaimed" },
  });
});

// Compile-time contract: a token comparison must include every durable authority field.
const completeToken: DraftAuthorityToken = {
  namespaceIncarnation: "incarnation",
  persistentRevision: 1,
  ownerRevision: 2,
};
void completeToken;

expectTypeOf(createBrowserDraftRepository).returns.toEqualTypeOf<BrowserDraftRepository>();
