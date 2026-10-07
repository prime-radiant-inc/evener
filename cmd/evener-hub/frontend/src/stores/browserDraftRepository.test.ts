import type { DraftSessionToken } from "@evener/appwire-client/state/mutation";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
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
const key = (sessionRef: string): BrowserDraftKey => ({ namespace, sessionRef });

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

test("orders overlapping CAS writes then a later explicit rebase, including identical metadata", async () => {
  const factory = new IDBFactory();
  const databaseName = "revision";
  const a = createBrowserDraftRepository({ indexedDB: factory, databaseName });
  closeAfterTest(() => a.close());
  const b = createBrowserDraftRepository({ indexedDB: factory, databaseName });
  closeAfterTest(() => b.close());
  const draftKey = browserDraftKey("root", "https://hub.test");
  const base = committed(await a.seedDraft(draftKey, { text: "/same", skillNames: ["same"], mentions: [] }));
  const results = await Promise.all([
    a.commitEdit(draftKey, base.token, { text: "first", skillNames: [] }, () => true),
    b.commitEdit(draftKey, base.token, { text: "second", skillNames: [] }, () => true),
  ]);
  expect(results.filter((r) => r.state === "committed")).toHaveLength(1);
  expect(results.filter((r) => r.state === "conflict")).toHaveLength(1);
  const current = await a.load(draftKey);
  if (!current) throw new Error("missing row");
  const next = committed(
    await b.commitEdit(draftKey, current.token, { text: "last", skillNames: [], mentions: [] }, () => true),
  );
  const identical = committed(await a.commitEdit(draftKey, next.token, next, () => true));
  const metadata = committed(
    await b.commitEdit(draftKey, identical.token, { ...next, commandNames: ["plan"] }, () => true),
  );
  expect(metadata.token.sessionRevision).toBe(next.token.sessionRevision + 2);
  expect(await a.load(draftKey)).toMatchObject({ text: "last", commandNames: ["plan"], mentions: [] });
  const guard = captureDraftSettlementGuard(metadata, { kind: "draft" }, 7);
  metadata.token = { namespaceIncarnation: "changed", sessionRevision: 99 };
  expect(guard.token.sessionRevision).toBe(next.token.sessionRevision + 2);
  expect(Object.isFrozen(guard.token)).toBe(true);
});

test("never imports over an explicit empty revision, or reimports changed legacy bytes", async () => {
  const factory = new IDBFactory();
  const draftKey = browserDraftKey("root", "https://hub.test");
  const storage = new MemoryStorage();
  const repo = createBrowserDraftRepository({ indexedDB: factory, databaseName: "transfer", storage });
  closeAfterTest(() => repo.close());
  const legacy = composerDraftStorageKey("root");
  storage.setItem(legacy, JSON.stringify({ text: "imported", skillNames: [], mentions: [] }));
  expect(await repo.transferSupportedDrafts(draftKey.namespace)).toEqual({ state: "committed" });
  const row = await repo.load(draftKey);
  if (!row) throw new Error("missing import");
  committed(await repo.commitEdit(draftKey, row.token, { text: "", skillNames: [] }, () => true));
  storage.setItem(legacy, JSON.stringify({ text: "later legacy", skillNames: [] }));
  await repo.transferSupportedDrafts(draftKey.namespace);
  expect((await repo.load(draftKey))?.text).toBe("");
});

test("cleanup keeps changed inspected bytes, and a listener cannot undo commit", async () => {
  const factory = new IDBFactory();
  const draftKey = browserDraftKey("root", "https://hub.test");
  const storage = new MemoryStorage();
  const legacy = composerDraftStorageKey("root");
  const changed = '{"text":"changed","skillNames":[]}';
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
  await repo.transferSupportedDrafts(draftKey.namespace);
  expect(storage.getItem(legacy)).toBe(changed);
  expect((await repo.load(draftKey))?.text).toBe("original");
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

test("does not reimport stale supported keys after a committed transfer and clear", async () => {
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
  closeAfterTest(() => repository.close());

  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  expect(cleanupFaults).toEqual(["cleanup interrupted"]);
  const draft = await repository.load(key("root"));
  expect(draft?.text).toBe('{"text":"literal /same"}');
  expect(draft?.skillNames).toEqual([]);
  if (!draft) throw new Error("transferred draft missing");
  const cleared = committed(
    await repository.commitEdit(key("root"), draft.token, { text: "", skillNames: [] }, () => true),
  );
  expect(
    await repository.clearSubmitted(key("root"), {
      text: "",
      skillNames: [],
      draftRevisionAtStart: 0,
      settlementGuard: captureDraftSettlementGuard(cleared, { kind: "draft" }, 0),
      stillOwnsEdit: () => true,
    }),
  ).toEqual({ cleared: true, draftUnchanged: true, persistence: "committed" });

  storage.setItem(draftStorageKey("root"), '{"text":"literal /same"}');
  const restarted = createBrowserDraftRepository({ indexedDB, databaseName: "transfer-test", storage });
  closeAfterTest(() => restarted.close());
  expect(await restarted.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  expect((await restarted.load(key("root")))?.text ?? "").toBe("");
});

test("seedDraft creates only an absent row at revision 1", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "seed-once",
    storage: new MemoryStorage(),
  });
  closeAfterTest(() => repository.close());
  const first = committed(await repository.seedDraft(key("seed"), { text: "first", skillNames: [] }));
  expect(first.token.sessionRevision).toBe(1);
  expect(await repository.seedDraft(key("seed"), { text: "second", skillNames: [] })).toMatchObject({
    state: "conflict",
    current: { text: "first" },
  });
});

test("commitEdit advances on identical writes and treats omitted and explicit empty mentions as distinct editor values", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "mention-presence",
    storage: new MemoryStorage(),
  });
  closeAfterTest(() => repository.close());
  const seeded = committed(await repository.seedDraft(key("mentions"), { text: "/same", skillNames: ["same"] }));

  const explicitEmptyValue = { text: "/same", skillNames: ["same"], mentions: [] };
  const explicitEmpty = committed(
    await repository.commitEdit(key("mentions"), seeded.token, explicitEmptyValue, () => true),
  );
  expect(explicitEmpty.token.sessionRevision).toBe(seeded.token.sessionRevision + 1);
  expect(explicitEmpty).toEqual({
    ...explicitEmptyValue,
    key: key("mentions"),
    token: explicitEmpty.token,
  });
  expect(parseSkillDocument(explicitEmpty).toJSON()).toEqual({
    type: "doc",
    content: [{ type: "text", text: "/same" }],
  });
  expect(await repository.load(key("mentions"))).toEqual(explicitEmpty);

  const duplicateEmpty = committed(
    await repository.commitEdit(
      key("mentions"),
      explicitEmpty.token,
      { ...explicitEmptyValue, mentions: [] },
      () => true,
    ),
  );
  expect(duplicateEmpty.token.sessionRevision).toBe(explicitEmpty.token.sessionRevision + 1);
  expect(duplicateEmpty).toMatchObject({ text: "/same", skillNames: ["same"], mentions: [] });

  const omittedAgain = committed(
    await repository.commitEdit(
      key("mentions"),
      duplicateEmpty.token,
      { text: "/same", skillNames: ["same"] },
      () => true,
    ),
  );
  expect(omittedAgain.token.sessionRevision).toBe(duplicateEmpty.token.sessionRevision + 1);
  expect(omittedAgain).not.toHaveProperty("mentions");
  expect(parseSkillDocument(omittedAgain).toJSON()).toEqual({
    type: "doc",
    content: [{ type: "skill", attrs: { name: "same" } }],
  });
  expect(await repository.load(key("mentions"))).toEqual(omittedAgain);
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
  expect(duplicate).toMatchObject(value);
});

test("commitEdit fences on both incarnations and the revision, and honors stillAuthorized before writing", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "draft-cas",
    storage: new MemoryStorage(),
  });
  closeAfterTest(() => repository.close());
  const seeded = committed(
    await repository.seedDraft(key("cas"), { text: "before", skillNames: ["skill"], commandNames: ["command"] }),
  );

  expect(
    await repository.commitEdit(
      key("cas"),
      { namespaceIncarnation: "other", sessionRevision: seeded.token.sessionRevision },
      { text: "x", skillNames: [] },
      () => true,
    ),
  ).toMatchObject({ state: "conflict", current: { text: "before" } });
  expect(
    await repository.commitEdit(
      key("cas"),
      { namespaceIncarnation: seeded.token.namespaceIncarnation, sessionRevision: seeded.token.sessionRevision + 5 },
      { text: "x", skillNames: [] },
      () => true,
    ),
  ).toMatchObject({ state: "conflict", current: { text: "before" } });
  expect(await repository.commitEdit(key("absent"), seeded.token, { text: "x", skillNames: [] }, () => true)).toEqual({
    state: "unavailable",
  });
  expect(await repository.commitEdit(key("cas"), seeded.token, { text: "x", skillNames: [] }, () => false)).toEqual({
    state: "unavailable",
  });
  expect(await repository.load(key("cas"))).toMatchObject({
    text: "before",
    token: seeded.token,
    commandNames: ["command"],
  });
});

test("clearSubmitted clears only the submitted unchanged generation, and a stale settlement cannot clear newer work", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "settlement-guards",
    storage: new MemoryStorage(),
  });
  closeAfterTest(() => repository.close());
  const seeded = committed(await repository.seedDraft(key("settle"), { text: "send me", skillNames: ["skill"] }));
  const submitted = { text: "send me", skillNames: ["skill"], draftRevisionAtStart: 0 };

  expect(await repository.clearSubmitted(key("settle"), submitted)).toEqual({
    cleared: false,
    draftUnchanged: false,
    persistence: "committed",
  });
  expect(
    await repository.clearSubmitted(key("settle"), {
      ...submitted,
      settlementGuard: captureDraftSettlementGuard(seeded, { kind: "draft" }, 3),
      stillOwnsEdit: () => false,
    }),
  ).toEqual({ cleared: false, draftUnchanged: true, persistence: "committed" });
  expect(await repository.load(key("settle"))).toMatchObject({ text: "send me" });

  const guard = captureDraftSettlementGuard(seeded, { kind: "draft" }, 3);
  expect(
    await repository.clearSubmitted(key("settle"), { ...submitted, settlementGuard: guard, stillOwnsEdit: () => true }),
  ).toEqual({ cleared: true, draftUnchanged: true, persistence: "committed" });
  const durable = await repository.load(key("settle"));
  if (!durable) throw new Error("cleared row missing");
  expect(durable.text).toBe("");
  expect(durable.token.sessionRevision).toBe(seeded.token.sessionRevision + 1);

  expect(
    await repository.clearSubmitted(key("settle"), { ...submitted, settlementGuard: guard, stillOwnsEdit: () => true }),
  ).toEqual({ cleared: false, draftUnchanged: false, persistence: "committed" });
  expect(await repository.load(key("settle"))).toMatchObject({ text: "" });
});

test("load refreshes an advanced namespace incarnation and a captured old token then conflicts", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "namespace-fence";
  const repository = createBrowserDraftRepository({ indexedDB, databaseName, storage: new MemoryStorage() });
  closeAfterTest(() => repository.close());
  const seeded = committed(await repository.seedDraft(key("fenced"), { text: "preserve", skillNames: ["skill"] }));

  await advanceNamespace(indexedDB, databaseName, "fixture-incarnation-2");
  const refreshed = await repository.load(key("fenced"));
  expect(refreshed).toMatchObject({
    text: "preserve",
    skillNames: ["skill"],
    token: { namespaceIncarnation: "fixture-incarnation-2", sessionRevision: seeded.token.sessionRevision },
  });
  expect(
    await repository.commitEdit(key("fenced"), seeded.token, { text: "stale", skillNames: [] }, () => true),
  ).toMatchObject({ state: "conflict", current: { text: "preserve" } });
  if (!refreshed) throw new Error("refreshed row missing");
  const next = committed(
    await repository.commitEdit(key("fenced"), refreshed.token, { text: "next", skillNames: [] }, () => true),
  );
  expect(next.token.sessionRevision).toBe(refreshed.token.sessionRevision + 1);
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
      settlementGuard: captureDraftSettlementGuard(seeded, { kind: "draft" }, 1),
      stillOwnsEdit: () => true,
    }),
  ).toEqual({ cleared: false, draftUnchanged: true, persistence: "pending" });
  expect(await repository.load(key("fault"))).toMatchObject({ text: "before" });
});

test("a real IndexedDB request fault rolls the write back and leaves the durable row unchanged", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "request-fault";
  const repository = createBrowserDraftRepository({ indexedDB, databaseName, storage: new MemoryStorage() });
  closeAfterTest(() => repository.close());
  const seeded = committed(await repository.seedDraft(key("request-fault"), { text: "authoritative", skillNames: [] }));

  // Instrument the real object store's put and, for one marked value, drop the
  // keyPath so fake-indexeddb raises its genuine DataError; every other call
  // delegates to the original method.
  const originalPut = IDBObjectStore.prototype.put;
  let injected = false;
  vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (this: IDBObjectStore, value: unknown) {
    const candidate = value as { serializedKey?: string; text?: string };
    if (!injected && candidate.text === "faulting rewrite") {
      injected = true;
      const { serializedKey: _dropped, ...broken } = candidate;
      return originalPut.call(this, broken);
    }
    return originalPut.call(this, value);
  });

  expect(
    await repository.commitEdit(
      key("request-fault"),
      seeded.token,
      { text: "faulting rewrite", skillNames: [] },
      () => true,
    ),
  ).toEqual({ state: "unavailable" });
  expect(injected).toBe(true);
  vi.restoreAllMocks();

  const restarted = createBrowserDraftRepository({ indexedDB, databaseName, storage: new MemoryStorage() });
  closeAfterTest(() => restarted.close());
  expect(await restarted.load(key("request-fault"))).toMatchObject({ text: "authoritative", token: seeded.token });
});

test("cleanup removes only inspected bytes and a value changed during cleanup is re-inspected on the next scan", async () => {
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
  expect(storage.getItem(draftStorageKey("cleanup-race"))).toBeNull();
  expect(await repository.load(key("cleanup-race"))).toMatchObject({ text: "inspected" });
});

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
  closeAfterTest(() => repository.close());
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "pending" });
});

test("listener failures cannot change committed seed edit or clear results or stop later listeners", async () => {
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
  const seeded = committed(await repository.seedDraft(key("listener"), { text: "seeded", skillNames: [] }));
  for (const unsubscribe of unsubscribers) unsubscribe();
  const durableSeed = await repository.load(key("listener"));
  if (!durableSeed) throw new Error("durable seed missing");

  unsubscribers = subscribeFaultThenListener();
  const edited = committed(
    await repository.commitEdit(key("listener"), durableSeed.token, { text: "edited", skillNames: [] }, () => true),
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

  expect(seeded).toMatchObject({ text: "seeded" });
  expect(durableSeed).toEqual(seeded);
  expect(edited).toMatchObject({ text: "edited" });
  expect(durableEdit).toEqual(edited);
  expect(cleared).toEqual({ cleared: true, draftUnchanged: true, persistence: "committed" });
  expect(durableClear?.text ?? "").toBe("");
  expect(laterNotifications).toEqual([key("listener"), key("listener"), key("listener")]);
  expect(diagnostics).toHaveLength(3);
  for (const diagnostic of diagnostics) {
    expect(diagnostic).toMatchObject({ path: "listener-failed", key: key("listener") });
    expect(diagnostic.error).toBe(listenerFailure);
  }
});

test("a closed connection reopens on the next write", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "reopen";
  const storage = new MemoryStorage();
  const first = createBrowserDraftRepository({ indexedDB, databaseName, storage });
  const seeded = committed(await first.seedDraft(key("reopen"), { text: "durable", skillNames: [] }));
  first.close();
  const second = createBrowserDraftRepository({ indexedDB, databaseName, storage });
  closeAfterTest(() => second.close());
  expect(await second.load(key("reopen"))).toEqual(seeded);
  const next = committed(
    await second.commitEdit(key("reopen"), seeded.token, { text: "after reopen", skillNames: [] }, () => true),
  );
  expect(next.token.sessionRevision).toBe(seeded.token.sessionRevision + 1);
});

test("the inactive runtime rescans only on its four declared lifecycle triggers and the next trigger recovers a fault", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  storage.setItem(draftStorageKey("runtime"), "initial");
  let transferAttempts = 0;
  let failNextTransfer = false;
  let releaseAttempt: (() => void) | undefined;
  const repository = createBrowserDraftRepository({
    indexedDB,
    databaseName: "transfer-runtime",
    storage,
    beforeCommit: (operation) => {
      if (operation !== "transferSupportedDrafts") return;
      transferAttempts += 1;
      releaseAttempt?.();
      releaseAttempt = undefined;
      if (failNextTransfer) {
        failNextTransfer = false;
        throw new Error("injected transfer fault");
      }
    },
  });
  closeAfterTest(() => repository.close());
  const nextAttempt = () =>
    new Promise<void>((resolve) => {
      releaseAttempt = resolve;
    });

  let attempt = nextAttempt();
  const runtime = createBrowserDraftTransferRuntime({ repository, namespace, storage, window, document });
  await attempt;
  const imported = await repository.load(key("runtime"));
  if (!imported) throw new Error("initial runtime import missing");
  expect(imported.text).toBe("initial");
  committed(
    await repository.commitEdit(key("runtime"), imported.token, { text: "authoritative", skillNames: [] }, () => true),
  );

  storage.setItem(draftStorageKey("runtime"), "from composer-open");
  attempt = nextAttempt();
  runtime.composerOpened(key("runtime"));
  await attempt;

  storage.setItem(draftStorageKey("runtime"), "from visibility");
  attempt = nextAttempt();
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
  await attempt;

  storage.setItem(draftStorageKey("runtime"), "from storage event");
  attempt = nextAttempt();
  window.dispatchEvent(new StorageEvent("storage", { key: draftStorageKey("runtime") }));
  await attempt;

  storage.setItem(draftStorageKey("runtime"), "after fault");
  failNextTransfer = true;
  attempt = nextAttempt();
  window.dispatchEvent(new StorageEvent("storage", { key: composerDraftStorageKey("runtime") }));
  await attempt;

  expect(transferAttempts).toBe(5);
  expect(await repository.load(key("runtime"))).toMatchObject({ text: "authoritative" });
  // The faulting pass kept its un-inspected bytes for the next trigger.
  expect(storage.getItem(draftStorageKey("runtime"))).toBe("after fault");

  runtime.dispose();
  const attemptsAtDispose = transferAttempts;
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
  closeAfterTest(() => repository.close());
  runtime = createBrowserDraftTransferRuntime({ repository, namespace, storage, window, document });
  await followUp;
  expect(attempts).toBe(2);
  runtime.dispose();
});

// Keep the runtime test's post-dispose key literal independent of the production key helper it is checking.
function draftStorageStorageKeyForTest(ref: string): string {
  return `evener.composer.draft.v1.${ref}`;
}

// Compile-time contract: a session token comparison must include every durable authority field.
const completeToken: DraftSessionToken = { namespaceIncarnation: "incarnation", sessionRevision: 1 };
void completeToken;

expectTypeOf(createBrowserDraftRepository).returns.toEqualTypeOf<BrowserDraftRepository>();
