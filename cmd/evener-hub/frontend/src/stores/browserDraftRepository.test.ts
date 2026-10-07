import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, expectTypeOf, test, vi } from "vitest";
import { composerDraftStorageKey, draftStorageKey } from "../panes/session/composer/draft";
import { parseSkillDocument } from "../panes/session/composer/skillDocument";
import { MemoryStorage } from "../storageTestUtils";
import {
  type BrowserDraftKey,
  type BrowserDraftRepository,
  browserDraftKey,
  captureDraftSettlementGuard,
  createBrowserDraftRepository,
  createBrowserDraftTransferRuntime,
  type DraftCommit,
  type StoredComposerDraft,
} from "./browserDraftRepository";
import { ANNOTATION_NAMESPACES_STORE, LOCAL_COMPOSITION_DATABASE_VERSION } from "./localCompositionSchema";

const namespace = { kind: "browser" as const, origin: "https://hub.test" };
const key = (sessionRef: string): BrowserDraftKey => browserDraftKey(sessionRef, "https://hub.test");

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

// Keep the runtime test's post-dispose key literal independent of the production key helper it is checking.
function draftStorageKeyForTest(ref: string): string {
  return `evener.composer.draft.v1.${ref}`;
}

const repositoryCleanups: (() => void)[] = [];
function closeAfterTest(close: () => void): void {
  repositoryCleanups.push(close);
}
afterEach(() => {
  for (const close of repositoryCleanups.splice(0).reverse()) close();
  vi.restoreAllMocks();
});

function committed(result: DraftCommit): StoredComposerDraft {
  if (result.state !== "committed") throw new Error(`expected committed, got ${result.state}`);
  return result.draft;
}

test("orders overlapping CAS writes then a later explicit rebase, including identical metadata", async () => {
  const factory = new IDBFactory(),
    databaseName = "revision";
  const a = createBrowserDraftRepository({ indexedDB: factory, databaseName });
  closeAfterTest(() => a.close());
  const b = createBrowserDraftRepository({ indexedDB: factory, databaseName });
  closeAfterTest(() => b.close());
  const k = browserDraftKey("root", "https://hub.test");
  const base = committed(await a.seedDraft(k, { text: "/same", skillNames: ["same"], mentions: [] }));
  const results = await Promise.all([
    a.commitEdit(k, base.token, { text: "first", skillNames: [] }, () => true),
    b.commitEdit(k, base.token, { text: "second", skillNames: [] }, () => true),
  ]);
  expect(results.filter((r) => r.state === "committed")).toHaveLength(1);
  expect(results.filter((r) => r.state === "conflict")).toHaveLength(1);
  const current = await a.load(k);
  if (!current) throw new Error("missing row");
  const next = committed(
    await b.commitEdit(k, current.token, { text: "last", skillNames: [], mentions: [] }, () => true),
  );
  const identical = committed(await a.commitEdit(k, next.token, next, () => true));
  const metadata = committed(await b.commitEdit(k, identical.token, { ...next, commandNames: ["plan"] }, () => true));
  expect(metadata.token.sessionRevision).toBe(next.token.sessionRevision + 2);
  expect(await a.load(k)).toMatchObject({ text: "last", commandNames: ["plan"], mentions: [] });
  const guard = captureDraftSettlementGuard(metadata, { kind: "draft" }, 7);
  metadata.token = { namespaceIncarnation: "changed", sessionRevision: 99 };
  expect(guard.token.sessionRevision).toBe(next.token.sessionRevision + 2);
  expect(Object.isFrozen(guard.token)).toBe(true);
});

test("never imports over an explicit empty revision, or reimports changed legacy bytes", async () => {
  const factory = new IDBFactory(),
    k = browserDraftKey("root", "https://hub.test");
  const storage = new MemoryStorage();
  const repo = createBrowserDraftRepository({ indexedDB: factory, databaseName: "transfer", storage });
  closeAfterTest(() => repo.close());
  const legacy = composerDraftStorageKey("root");
  storage.setItem(legacy, JSON.stringify({ text: "imported", skillNames: [], mentions: [] }));
  expect(await repo.transferSupportedDrafts(k.namespace)).toEqual({ state: "committed" });
  const row = await repo.load(k);
  if (!row) throw new Error("missing import");
  committed(await repo.commitEdit(k, row.token, { text: "", skillNames: [] }, () => true));
  storage.setItem(legacy, JSON.stringify({ text: "later legacy", skillNames: [] }));
  await repo.transferSupportedDrafts(k.namespace);
  expect((await repo.load(k))?.text).toBe("");
});

test("cleanup keeps changed inspected bytes, and a listener cannot undo commit", async () => {
  const factory = new IDBFactory(),
    k = browserDraftKey("root", "https://hub.test");
  const storage = new MemoryStorage();
  const legacy = composerDraftStorageKey("root"),
    changed = '{"text":"changed","skillNames":[]}';
  storage.setItem(legacy, '{"text":"original","skillNames":[]}');
  const repo = createBrowserDraftRepository({
    indexedDB: factory,
    databaseName: "cleanup",
    storage,
    beforeLegacyCleanup: () => storage.setItem(legacy, changed),
    onListenerDiagnostic: () => {},
  });
  closeAfterTest(() => repo.close());
  repo.subscribe(() => {
    throw new Error("observer");
  });
  await repo.transferSupportedDrafts(k.namespace);
  expect(storage.getItem(legacy)).toBe(changed);
  expect((await repo.load(k))?.text).toBe("original");
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
  closeAfterTest(() => repository.close());

  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  expect(await repository.load(key("structured"))).toMatchObject({
    text: "structured /same",
    skillNames: ["same"],
    commandNames: ["command"],
    mentions: [],
  });
  expect(await repository.load(key("fallback"))).toMatchObject({ text: "literal fallback /same", skillNames: [] });
  expect(await repository.load(key("json"))).toMatchObject({
    text: '{"text":"literal","skillNames":["not-selected"]} /slash',
    skillNames: [],
  });
  expect(storage.length).toBe(0);
});

test("a failed transfer rolls back its markers and row, and a later trigger commits", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  storage.setItem(draftStorageKey("rollback"), "initial");
  let failTransfer = true;
  const repository = createBrowserDraftRepository({
    indexedDB,
    databaseName: "transfer-rollback",
    storage,
    beforeCommit: (operation) => {
      if (operation === "transferSupportedDrafts" && failTransfer) throw new Error("injected transfer fault");
    },
  });
  closeAfterTest(() => repository.close());

  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "pending" });
  expect(await repository.load(key("rollback"))).toBeNull();
  expect(storage.getItem(draftStorageKey("rollback"))).toBe("initial");

  failTransfer = false;
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  expect(await repository.load(key("rollback"))).toMatchObject({ text: "initial" });
});

test("transfer read faults stay pending and emit no unvalidated console error", async () => {
  class FailingStorage extends MemoryStorage {
    override getItem(_key: string): string | null {
      throw new Error("read unavailable");
    }
  }
  const storage = new FailingStorage();
  storage.setItem(draftStorageKey("fault"), "unreadable");
  const repository = createBrowserDraftRepository({ indexedDB: new IDBFactory(), databaseName: "read-fault", storage });
  closeAfterTest(() => repository.close());
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "pending" });
});

test("seedDraft creates only an absent row", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "seed-once",
    storage: new MemoryStorage(),
  });
  closeAfterTest(() => repository.close());
  const first = await repository.seedDraft(key("seed"), { text: "first", skillNames: [] });
  if (first.state !== "committed") throw new Error("seed failed");
  expect(await repository.seedDraft(key("seed"), { text: "second", skillNames: [] })).toMatchObject({
    state: "conflict",
    current: { text: "first" },
  });
});

test("commitEdit treats omitted and explicit empty mentions as distinct editor values and advances identical edits", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "mention-presence",
    storage: new MemoryStorage(),
  });
  closeAfterTest(() => repository.close());
  const omittedValue = { text: "/same", skillNames: ["same"] };
  const seeded = committed(await repository.seedDraft(key("mentions"), omittedValue));

  const explicitEmptyValue = { text: "/same", skillNames: ["same"], mentions: [] };
  const explicitEmpty = committed(
    await repository.commitEdit(key("mentions"), seeded.token, explicitEmptyValue, () => true),
  );
  expect(explicitEmpty.token.sessionRevision).toBe(seeded.token.sessionRevision + 1);
  expect(explicitEmpty).toEqual({ ...explicitEmptyValue, key: key("mentions"), token: explicitEmpty.token });
  expect(parseSkillDocument(explicitEmpty).toJSON()).toEqual({
    type: "doc",
    content: [{ type: "text", text: "/same" }],
  });
  expect(await repository.load(key("mentions"))).toEqual(explicitEmpty);

  // An explicit identical edit is still an explicit edit: the revision advances.
  const duplicateEmpty = committed(
    await repository.commitEdit(
      key("mentions"),
      explicitEmpty.token,
      { ...explicitEmptyValue, mentions: [] },
      () => true,
    ),
  );
  expect(duplicateEmpty.token.sessionRevision).toBe(explicitEmpty.token.sessionRevision + 1);

  const omittedAgain = committed(
    await repository.commitEdit(key("mentions"), duplicateEmpty.token, omittedValue, () => true),
  );
  expect(omittedAgain.token.sessionRevision).toBe(duplicateEmpty.token.sessionRevision + 1);
  expect(omittedAgain).not.toHaveProperty("mentions");
  expect(parseSkillDocument(omittedAgain).toJSON()).toEqual({
    type: "doc",
    content: [{ type: "skill", attrs: { name: "same" } }],
  });
  expect(await repository.load(key("mentions"))).toEqual(omittedAgain);

  const duplicateOmitted = committed(
    await repository.commitEdit(key("mentions"), omittedAgain.token, { ...omittedValue }, () => true),
  );
  expect(duplicateOmitted.token.sessionRevision).toBe(omittedAgain.token.sessionRevision + 1);
});

test("commitEdit preserves complete nonempty skill and command mention metadata", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "mention-metadata",
    storage: new MemoryStorage(),
  });
  closeAfterTest(() => repository.close());
  const seeded = committed(await repository.seedDraft(key("mention-metadata"), { text: "", skillNames: [] }));
  const value = {
    text: "🙂 /same /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "command" as const, name: "same", offset: 3 },
      { kind: "skill" as const, name: "same", offset: 9 },
    ],
  };

  const edited = committed(await repository.commitEdit(key("mention-metadata"), seeded.token, value, () => true));
  expect(edited).toEqual({ ...value, key: key("mention-metadata"), token: edited.token });
  expect(await repository.load(key("mention-metadata"))).toEqual(edited);
  const duplicate = committed(
    await repository.commitEdit(
      key("mention-metadata"),
      edited.token,
      { ...value, mentions: value.mentions.map((mention) => ({ ...mention })) },
      () => true,
    ),
  );
  expect(duplicate.token.sessionRevision).toBe(edited.token.sessionRevision + 1);
});

test("load re-tokens an advanced namespace incarnation, keeps the revision, and cannot authorize a captured old cleanup", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "namespace-fence";
  const repository = createBrowserDraftRepository({ indexedDB, databaseName, storage: new MemoryStorage() });
  closeAfterTest(() => repository.close());
  const seeded = committed(await repository.seedDraft(key("fenced"), { text: "preserve", skillNames: ["skill"] }));
  const guard = captureDraftSettlementGuard(seeded, { kind: "draft" }, 0);

  await advanceNamespace(indexedDB, databaseName, "fixture-incarnation-2");
  const refreshed = await repository.load(key("fenced"));
  expect(refreshed).toMatchObject({
    text: "preserve",
    skillNames: ["skill"],
    token: { namespaceIncarnation: "fixture-incarnation-2", sessionRevision: seeded.token.sessionRevision },
  });

  expect(
    await repository.commitEdit(key("fenced"), seeded.token, { text: "stale", skillNames: [] }, () => true),
  ).toMatchObject({
    state: "conflict",
    current: { text: "preserve" },
  });
  expect(
    await repository.clearSubmitted(key("fenced"), {
      text: "preserve",
      skillNames: ["skill"],
      draftRevisionAtStart: 0,
      settlementGuard: guard,
      stillOwnsEdit: () => true,
    }),
  ).toEqual({ cleared: false, draftUnchanged: false, persistence: "committed" });
  expect((await repository.load(key("fenced")))?.text).toBe("preserve");
});

test("browser clearing requires the copied full guard, a live edit owner and an exact value including mentions", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "settlement-guards",
    storage: new MemoryStorage(),
  });
  closeAfterTest(() => repository.close());
  const seeded = committed(
    await repository.seedDraft(key("settle"), { text: "send me", skillNames: ["skill"], mentions: [] }),
  );
  const base = { text: "send me", skillNames: ["skill"], mentions: [], draftRevisionAtStart: 0 };

  expect(await repository.clearSubmitted(key("settle"), base)).toEqual({
    cleared: false,
    draftUnchanged: false,
    persistence: "committed",
  });
  const guard = captureDraftSettlementGuard(seeded, { kind: "draft" }, 5);
  expect(
    await repository.clearSubmitted(key("settle"), { ...base, settlementGuard: guard, stillOwnsEdit: () => false }),
  ).toEqual({
    cleared: false,
    draftUnchanged: true,
    persistence: "committed",
  });
  // An omitted mention list is a different value than the row's explicit empty list.
  expect(
    await repository.clearSubmitted(key("settle"), {
      ...base,
      mentions: undefined,
      settlementGuard: guard,
      stillOwnsEdit: () => true,
    }),
  ).toEqual({ cleared: false, draftUnchanged: true, persistence: "committed" });
  expect(await repository.load(key("settle"))).toMatchObject({ text: "send me" });

  expect(
    await repository.clearSubmitted(key("settle"), { ...base, settlementGuard: guard, stillOwnsEdit: () => true }),
  ).toEqual({
    cleared: true,
    draftUnchanged: true,
    persistence: "committed",
  });
  expect((await repository.load(key("settle")))?.text).toBe("");
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
  closeAfterTest(() => repository.close());
  const seeded = committed(await repository.seedDraft(key("fault"), { text: "before", skillNames: [] }));

  failOperation = "commitEdit";
  expect(
    await repository.commitEdit(key("fault"), seeded.token, { text: "after", skillNames: [] }, () => true),
  ).toEqual({
    state: "unavailable",
  });
  expect(await repository.load(key("fault"))).toMatchObject({ text: "before", token: seeded.token });

  failOperation = "clearSubmitted";
  expect(
    await repository.clearSubmitted(key("fault"), {
      text: "before",
      skillNames: [],
      draftRevisionAtStart: 0,
      settlementGuard: captureDraftSettlementGuard(seeded, { kind: "draft" }, 0),
      stillOwnsEdit: () => true,
    }),
  ).toEqual({ cleared: false, draftUnchanged: true, persistence: "pending" });
  expect(await repository.load(key("fault"))).toMatchObject({ text: "before" });
});

test("listener failures cannot change committed seed, edit or clear results or stop later listeners", async () => {
  type ListenerDiagnostic = { path: "listener-failed"; key: BrowserDraftKey; error: unknown };
  const listenerFailure = new Error("injected listener failure");
  const diagnostics: ListenerDiagnostic[] = [];
  const laterNotifications: BrowserDraftKey[] = [];
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "listener-isolation",
    storage: new MemoryStorage(),
    onListenerDiagnostic: (diagnostic: ListenerDiagnostic) => diagnostics.push(diagnostic),
  });
  closeAfterTest(() => repository.close());
  const subscribeFaultThenListener = () => [
    repository.subscribe(() => {
      throw listenerFailure;
    }),
    repository.subscribe((changed) => laterNotifications.push(changed)),
  ];

  let unsubscribers = subscribeFaultThenListener();
  const seeded = await repository.seedDraft(key("listener"), { text: "seeded", skillNames: [] });
  for (const unsubscribe of unsubscribers) unsubscribe();
  const durableSeed = await repository.load(key("listener"));
  if (!durableSeed) throw new Error("durable seed missing");

  unsubscribers = subscribeFaultThenListener();
  const edited = await repository.commitEdit(
    key("listener"),
    durableSeed.token,
    { text: "edited", skillNames: [] },
    () => true,
  );
  const durableEdit = await repository.load(key("listener"));
  if (!durableEdit) throw new Error("durable edit missing");
  const cleared = await repository.clearSubmitted(key("listener"), {
    text: "edited",
    skillNames: [],
    draftRevisionAtStart: 0,
    settlementGuard: captureDraftSettlementGuard(durableEdit, { kind: "draft" }, 0),
    stillOwnsEdit: () => true,
  });
  for (const unsubscribe of unsubscribers) unsubscribe();
  const durableClear = await repository.load(key("listener"));

  expect.soft(seeded).toMatchObject({ state: "committed", draft: { text: "seeded" } });
  expect.soft(edited).toMatchObject({ state: "committed", draft: { text: "edited" } });
  expect.soft(cleared).toEqual({ cleared: true, draftUnchanged: true, persistence: "committed" });
  expect.soft(durableClear?.text ?? "").toBe("");
  expect.soft(laterNotifications).toEqual([key("listener"), key("listener"), key("listener")]);
  expect.soft(diagnostics).toHaveLength(3);
  for (const diagnostic of diagnostics) {
    expect.soft(diagnostic).toMatchObject({ path: "listener-failed", key: key("listener") });
    expect.soft(diagnostic.error).toBe(listenerFailure);
  }
});

test("cleanup removes only inspected bytes and never reimports over a durable row", async () => {
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
  closeAfterTest(() => repository.close());
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  expect(storage.getItem(draftStorageKey("cleanup-race"))).toBe("new work");
  expect(await repository.load(key("cleanup-race"))).toMatchObject({ text: "inspected" });
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  expect(await repository.load(key("cleanup-race"))).toMatchObject({ text: "inspected" });
});

test("a reopened repository reads the durable cleared row and never reimports legacy bytes", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "reopen";
  const storage = new MemoryStorage();
  const legacy = composerDraftStorageKey("root");
  storage.setItem(legacy, JSON.stringify({ text: "imported", skillNames: [] }));
  const first = createBrowserDraftRepository({ indexedDB, databaseName, storage });
  await first.transferSupportedDrafts(namespace);
  const row = await first.load(key("root"));
  if (!row) throw new Error("import missing");
  await first.commitEdit(key("root"), row.token, { text: "", skillNames: [] }, () => true);
  first.close();

  const reopened = createBrowserDraftRepository({ indexedDB, databaseName, storage });
  closeAfterTest(() => reopened.close());
  expect(await reopened.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  expect((await reopened.load(key("root")))?.text).toBe("");
});

test("the inactive runtime rescans on its declared lifecycle triggers, recovers a fault, and stops at dispose", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  storage.setItem(draftStorageKey("runtime"), "initial");
  let attempts = 0;
  let failNextTransfer = false;
  const waiters: Array<{ count: number; resolve: () => void }> = [];
  const awaitAttempts = (count: number): Promise<void> => {
    if (attempts >= count) return Promise.resolve();
    return new Promise((resolve) => waiters.push({ count, resolve }));
  };
  const repository = createBrowserDraftRepository({
    indexedDB,
    databaseName: "transfer-runtime",
    storage,
    beforeCommit: (operation) => {
      if (operation !== "transferSupportedDrafts") return;
      attempts += 1;
      for (const waiter of waiters.splice(0)) {
        if (attempts >= waiter.count) waiter.resolve();
        else waiters.push(waiter);
      }
      if (failNextTransfer) {
        failNextTransfer = false;
        throw new Error("injected transfer fault");
      }
    },
  });
  closeAfterTest(() => repository.close());
  const runtime = createBrowserDraftTransferRuntime({ repository, namespace, storage, window, document });

  await awaitAttempts(1);
  storage.setItem(draftStorageKey("runtime"), "from composer-open");
  runtime.composerOpened(key("runtime"));
  await awaitAttempts(2);

  storage.setItem(draftStorageKey("runtime"), "from visibility");
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
  await awaitAttempts(3);

  storage.setItem(draftStorageKey("runtime"), "from storage event");
  window.dispatchEvent(new StorageEvent("storage", { key: draftStorageKey("runtime") }));
  await awaitAttempts(4);

  storage.setItem(draftStorageKey("runtime"), "after fault");
  failNextTransfer = true;
  window.dispatchEvent(new StorageEvent("storage", { key: composerDraftStorageKey("runtime") }));
  await awaitAttempts(5);
  runtime.composerOpened(key("runtime"));
  await awaitAttempts(6);

  expect(await repository.load(key("runtime"))).toMatchObject({ text: "initial" });

  runtime.dispose();
  const attemptsAtDispose = attempts;
  storage.setItem(draftStorageKey("runtime"), "after dispose");
  window.dispatchEvent(new StorageEvent("storage", { key: draftStorageKeyForTest("runtime") }));
  await repository.load(key("runtime"));
  expect(attempts).toBe(attemptsAtDispose);
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
  closeAfterTest(() => repository.close());
  runtime = createBrowserDraftTransferRuntime({ repository, namespace, storage, window, document });
  await followUp;
  expect(attempts).toBe(2);
  runtime.dispose();
});

expectTypeOf(createBrowserDraftRepository).returns.toEqualTypeOf<BrowserDraftRepository>();
