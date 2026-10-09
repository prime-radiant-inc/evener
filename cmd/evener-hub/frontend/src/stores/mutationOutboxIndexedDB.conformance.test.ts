// @vitest-environment node

// The IndexedDB adapter's half of the package's MutationOutboxStorage
// port-conformance suite: the web factory hosts the shared contracts over
// fake-indexeddb, the same contracts the native expo-sqlite adapter runs
// (mobile-native/src/mutationOutboxStorage.conformance.test.ts). One set of
// contracts, two hosts.
import { AppwireClient, type InputItem } from "@evener/appwire-client";
import type { DraftSessionToken } from "@evener/appwire-client/state/mutation";
import { describeMutationOutboxStorage } from "@evener/appwire-client/testing/mutationOutboxStorageConformance";
import { IDBFactory, IDBObjectStore } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { composerDraftStorageKey, draftStorageKey } from "../panes/session/composer/draft";
import type { SkillEditorValue } from "../panes/session/composer/skillDocument";
import { MemoryStorage } from "../storageTestUtils";
import {
  type BrowserDraftKey,
  type BrowserDraftRepository,
  createBrowserDraftRepository,
  type StoredComposerDraft,
} from "./browserDraftRepository";
import { DRAFT_TRANSFERS_STORE, MUTATION_RECOVERY_STORE } from "./localCompositionSchema";
import { setMutationClientIdentityForTests } from "./mutationClientIdentity";
import type { MutationIntent } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

// Node has no `location`; the outbox derives a recovery write's browser session
// key from the ambient origin, so every case in this file installs the real
// expected browser origin and restores the prior descriptor afterward.
const ORIGIN = "https://hub.test";
let originalLocationDescriptor: PropertyDescriptor | undefined;
beforeEach(() => {
  originalLocationDescriptor = Object.getOwnPropertyDescriptor(globalThis, "location");
  Object.defineProperty(globalThis, "location", { value: { origin: ORIGIN }, configurable: true });
});
afterEach(() => {
  if (originalLocationDescriptor) Object.defineProperty(globalThis, "location", originalLocationDescriptor);
  else Reflect.deleteProperty(globalThis, "location");
});

describeMutationOutboxStorage({
  name: "IndexedDB",
  createStorage(options = {}) {
    // The web reads its identity from a module singleton rather than a
    // constructor option; reset it to the requested id (or to undefined) so
    // each conformance test starts from the same place.
    setMutationClientIdentityForTests(options.getOwnClientId?.());
    return new MutationOutboxIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `mutation-outbox-conformance-${crypto.randomUUID()}`,
      createMutationId: options.createMutationId,
      now: options.now,
    });
  },
});

// ---------------------------------------------------------------------------
// Approved Task2 Step2: recovery writers share the session revision.
//
// A recovery write and the session's draft row are meant to commit in the same
// transaction over `localCompositionSchema`'s one database. These cases host
// the real outbox adapter and the real owner-free browser draft repository
// over ONE fake-indexeddb factory and read the durable session revision back
// through the repository's public API. Node has no `location`, so each case
// installs a scoped browser-origin fixture and restores it, with every
// connection, afterward.
//
// The optional recovery-write guard and ordinary-consumption objects are the
// approved optional final arguments (extract 88-96): `token` fences the durable
// session row, while `stillAuthorized`/`stillOwnsValue` are the live checks the
// transaction evaluates at its last abortable boundary.
// ---------------------------------------------------------------------------

const REF = "root";

const RICH_ORDINARY: SkillEditorValue = {
  text: "ordinary draft",
  skillNames: ["skill-one"],
  commandNames: ["command-one"],
  mentions: [
    { kind: "skill", name: "skill-one", offset: 0 },
    { kind: "command", name: "command-one", offset: 8 },
  ],
};

const WITHOUT_MENTIONS: SkillEditorValue = {
  text: RICH_ORDINARY.text,
  skillNames: [...RICH_ORDINARY.skillNames],
  commandNames: [...(RICH_ORDINARY.commandNames ?? [])],
};

const WITH_EMPTY_MENTIONS: SkillEditorValue = { ...RICH_ORDINARY, mentions: [] };

const EDITED_INPUT: InputItem[] = [{ type: "text", text: "edited" }];

const RECOVERY_SNAPSHOT = {
  version: 1,
  recoveryKind: "rejected",
  recoveryReason: "refused",
  composerText: "recovery",
  payload: { input: [{ type: "text", text: "recovery" }] },
};

function browserKey(sessionRef: string = REF): BrowserDraftKey {
  return { namespace: { kind: "browser", origin: ORIGIN }, sessionRef };
}

function turnIntent(text: string, targetRef: string = REF): MutationIntent {
  return {
    targetRef,
    threadId: "thread-1",
    method: "turn/queue",
    payload: { ref: targetRef, input: [{ type: "text", text }] },
    attachments: [],
    optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text }] },
    composerText: text,
  };
}

function noteIntent(note: string, targetRef: string = REF): MutationIntent {
  return {
    targetRef,
    threadId: "thread-1",
    method: "notes/human/set",
    payload: { ref: targetRef, expectedInstanceId: "thread-1", note },
    attachments: [],
    optimisticDisplay: null,
    composerText: note,
  };
}

test.each([
  { method: "turn", format: "v1" },
  { method: "turn", format: "v2" },
  { method: "note", format: "v1" },
  { method: "note", format: "v2" },
])(
  "inactive $method recovery metadata imports $format before writing its first ordinary row",
  async ({ method, format }) => {
    const indexedDB = new IDBFactory();
    const databaseName = `inactive-migration-${method}-${format}-${crypto.randomUUID()}`;
    const storage = new MemoryStorage();
    const previousStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
    Object.defineProperty(globalThis, "localStorage", { value: storage, configurable: true });
    const v1 = "legacy /same literal";
    const v2 =
      '{"text":"🙂 /same /same","skillNames":["same"],"commandNames":["same"],"mentions":[{"kind":"skill","name":"same","offset":3},{"kind":"command","name":"same","offset":9}]}';
    const expected: SkillEditorValue =
      format === "v1"
        ? { text: v1, skillNames: [] }
        : {
            text: "🙂 /same /same",
            skillNames: ["same"],
            commandNames: ["same"],
            mentions: [
              { kind: "skill", name: "same", offset: 3 },
              { kind: "command", name: "same", offset: 9 },
            ],
          };
    storage.setItem(draftStorageKey(REF), v1);
    if (format === "v2") storage.setItem(composerDraftStorageKey(REF), v2);
    storage.setItem("unrelated-legacy-value", "keep");
    const repository = createBrowserDraftRepository({ indexedDB, databaseName, storage });
    const independent = createBrowserDraftRepository({ indexedDB, databaseName, storage });
    const outbox = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: () => "inactive-mutation",
      now: () => 100,
    });
    const rpc = vi.spyOn(AppwireClient.prototype, "request");
    try {
      // No composer and no seed exists. These are the actual public operations
      // used by background turn rejection and Notes persistence recovery.
      expect(await independent.load(browserKey())).toBeNull();
      const intent = method === "turn" ? turnIntent("rejected input") : noteIntent("unsaved note");
      const sent = await outbox.enqueueIntent(intent);
      const recovered = await outbox.transferToRecovery(sent.clientMutationId, "rejected", "refused");
      const expectedRecovery = { ...sent, recoveryKind: "rejected", recoveryReason: "refused" };
      expect(recovered).toEqual(expectedRecovery);
      expect(await outbox.getRecovery(sent.clientMutationId)).toEqual(expectedRecovery);
      expect(await outbox.getOutbox(sent.clientMutationId)).toBeUndefined();
      expect(await outbox.listOutbox(REF)).toEqual([]);
      const beforeOpening = await independent.load(browserKey());
      // Continue past this assertion to witness what a later healthy transfer
      // does to the only legacy bytes after the metadata-only first-row write.
      expect.soft(beforeOpening).toMatchObject({ ...expected, key: browserKey(), token: { sessionRevision: 2 } });
      expect(beforeOpening?.token.namespaceIncarnation).not.toBe("");
      expect(await repository.transferSupportedDrafts(browserKey().namespace)).toEqual({ state: "committed" });
      const afterTransfer = await independent.load(browserKey());
      expect.soft(afterTransfer).toMatchObject(expected);
      expect.soft(afterTransfer?.mentions).toEqual(expected.mentions);
      expect(afterTransfer?.token).toEqual(beforeOpening?.token);
      expect(afterTransfer?.key).toEqual(browserKey());
      expect(storage.getItem(draftStorageKey(REF))).toBeNull();
      expect(storage.getItem(composerDraftStorageKey(REF))).toBeNull();
      expect(storage.getItem("unrelated-legacy-value")).toBe("keep");
      expect(await outbox.getRecovery(sent.clientMutationId)).toEqual(expectedRecovery);
      expect(rpc).not.toHaveBeenCalled();
    } finally {
      rpc.mockRestore();
      outbox.close();
      independent.close();
      repository.close();
      if (previousStorage) Object.defineProperty(globalThis, "localStorage", previousStorage);
      else Reflect.deleteProperty(globalThis, "localStorage");
    }
  },
);

test("inactive metadata refuses an aborted migration and healthy retry preserves the full legacy draft", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = `inactive-migration-retry-${crypto.randomUUID()}`;
  const storage = new MemoryStorage();
  const previousStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", { value: storage, configurable: true });
  const v1 = "older legacy fallback";
  const v2 =
    '{"text":"🙂 /same /same","skillNames":["same"],"commandNames":["same"],"mentions":[{"kind":"skill","name":"same","offset":3},{"kind":"command","name":"same","offset":9}]}';
  const expected: SkillEditorValue = {
    text: "🙂 /same /same",
    skillNames: ["same"],
    commandNames: ["same"],
    mentions: [
      { kind: "skill", name: "same", offset: 3 },
      { kind: "command", name: "same", offset: 9 },
    ],
  };
  storage.setItem(draftStorageKey(REF), v1);
  storage.setItem(composerDraftStorageKey(REF), v2);
  const independent = createBrowserDraftRepository({ indexedDB, databaseName, storage });
  const outbox = new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: () => "retry-mutation" });
  const rpc = vi.spyOn(AppwireClient.prototype, "request");
  const put = IDBObjectStore.prototype.put;
  let armed = true;
  let injected = 0;
  let aborted = 0;
  const fault = vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (this: IDBObjectStore, ...args) {
    if (armed && this.name === DRAFT_TRANSFERS_STORE) {
      injected += 1;
      this.transaction.addEventListener(
        "abort",
        () => {
          aborted += 1;
        },
        { once: true },
      );
      throw new Error("transient legacy transfer commit failure");
    }
    return put.apply(this, args);
  });
  try {
    expect(await independent.load(browserKey())).toBeNull();
    const sent = await outbox.enqueueIntent(turnIntent("rejected input"));
    const [failed] = await Promise.allSettled([
      outbox.transferToRecovery(sent.clientMutationId, "rejected", "refused"),
    ]);
    expect.soft(failed?.status).toBe("rejected");
    expect.soft(injected).toBe(1);
    expect.soft(aborted).toBe(1);
    expect.soft(await independent.load(browserKey())).toBeNull();
    expect.soft(await outbox.getOutbox(sent.clientMutationId)).toEqual(sent);
    expect.soft(await outbox.getRecovery(sent.clientMutationId)).toBeUndefined();
    expect.soft(storage.getItem(draftStorageKey(REF))).toBe(v1);
    expect.soft(storage.getItem(composerDraftStorageKey(REF))).toBe(v2);
    armed = false;
    const recovered = await outbox.transferToRecovery(sent.clientMutationId, "rejected", "refused");
    const expectedRecovery = { ...sent, recoveryKind: "rejected", recoveryReason: "refused" };
    expect(recovered).toEqual(expectedRecovery);
    expect(await outbox.getRecovery(sent.clientMutationId)).toEqual(expectedRecovery);
    expect(await outbox.listOutbox(REF)).toEqual([]);
    expect(await independent.load(browserKey())).toMatchObject({ ...expected, token: { sessionRevision: 2 } });
    expect(storage.getItem(draftStorageKey(REF))).toBeNull();
    expect(storage.getItem(composerDraftStorageKey(REF))).toBeNull();
    expect(rpc).not.toHaveBeenCalled();
  } finally {
    fault.mockRestore();
    rpc.mockRestore();
    outbox.close();
    independent.close();
    if (previousStorage) Object.defineProperty(globalThis, "localStorage", previousStorage);
    else Reflect.deleteProperty(globalThis, "localStorage");
  }
});

// A transparent request-failure seam. Only the adapter given this factory sees
// it, and every normal production request is forwarded to the real IndexedDB
// with unchanged arguments and return value. Once armed, the first `put` on the
// recovery store additionally issues one genuine duplicate-key `add`, whose
// request error aborts the real transaction; the extra request is never
// substituted for a production request. The request error and the real abort
// are both observed, injection is disarmed after it fires, and `restore()`
// clears it unconditionally.
function injectRecoveryPutFailure(real: IDBFactory): {
  indexedDB: IDBFactory;
  requestError: Promise<unknown>;
  aborted: Promise<boolean>;
  restore: () => void;
} {
  let armed = true;
  let observeRequestError: (error: unknown) => void = () => undefined;
  let observeAbort: (aborted: boolean) => void = () => undefined;
  const requestError = new Promise<unknown>((resolve) => {
    observeRequestError = resolve;
  });
  const aborted = new Promise<boolean>((resolve) => {
    observeAbort = resolve;
  });
  const databaseWrappers = new WeakMap<IDBDatabase, IDBDatabase>();
  const transactionWrappers = new WeakMap<IDBTransaction, IDBTransaction>();
  const storeWrappers = new WeakMap<IDBObjectStore, IDBObjectStore>();

  function wrapStore(store: IDBObjectStore): IDBObjectStore {
    const cached = storeWrappers.get(store);
    if (cached) return cached;
    const wrapper = new Proxy(store, {
      get(target, property) {
        if (property === "put") {
          return (value: unknown, key?: IDBValidKey) => {
            const forwarded = key === undefined ? target.put(value) : target.put(value, key);
            if (armed && target.name === MUTATION_RECOVERY_STORE) {
              armed = false;
              const injected = target.add(value);
              injected.addEventListener("error", () => observeRequestError(injected.error), { once: true });
              injected.addEventListener(
                "success",
                () => observeRequestError(new Error("injected request unexpectedly succeeded")),
                { once: true },
              );
            }
            return forwarded;
          };
        }
        const value = Reflect.get(target, property, target);
        return typeof value === "function" ? value.bind(target) : value;
      },
    }) as IDBObjectStore;
    storeWrappers.set(store, wrapper);
    return wrapper;
  }

  function wrapTransaction(transaction: IDBTransaction): IDBTransaction {
    const cached = transactionWrappers.get(transaction);
    if (cached) return cached;
    transaction.addEventListener("abort", () => observeAbort(true), { once: true });
    const wrapper = new Proxy(transaction, {
      get(target, property) {
        if (property === "objectStore") {
          return (name: string) => wrapStore(target.objectStore(name));
        }
        const value = Reflect.get(target, property, target);
        return typeof value === "function" ? value.bind(target) : value;
      },
    }) as IDBTransaction;
    transactionWrappers.set(transaction, wrapper);
    return wrapper;
  }

  function wrapDatabase(database: IDBDatabase): IDBDatabase {
    const cached = databaseWrappers.get(database);
    if (cached) return cached;
    const wrapper = new Proxy(database, {
      get(target, property) {
        if (property === "transaction") {
          return (names: string | string[], mode?: IDBTransactionMode) =>
            wrapTransaction(target.transaction(names, mode));
        }
        const value = Reflect.get(target, property, target);
        return typeof value === "function" ? value.bind(target) : value;
      },
    }) as IDBDatabase;
    databaseWrappers.set(database, wrapper);
    return wrapper;
  }

  const indexedDB = new Proxy(real, {
    get(target, property) {
      if (property === "open") {
        return (name: string, version?: number) => {
          const request = target.open(name, version);
          return new Proxy(request, {
            get(requestTarget, requestProperty) {
              if (requestProperty === "result") return wrapDatabase(requestTarget.result);
              const value = Reflect.get(requestTarget, requestProperty, requestTarget);
              return typeof value === "function" ? value.bind(requestTarget) : value;
            },
          }) as IDBOpenDBRequest;
        };
      }
      const value = Reflect.get(target, property, target);
      return typeof value === "function" ? value.bind(target) : value;
    },
  }) as IDBFactory;

  return {
    indexedDB,
    requestError,
    aborted,
    restore: () => {
      armed = false;
    },
  };
}

describe("MutationOutboxIndexedDB recovery revision participation", () => {
  let indexedDB: IDBFactory;
  let databaseName: string;
  let mutationSequence = 0;
  const cleanups: Array<() => void> = [];

  const nextMutationId = (): string => `recovery-mutation-${++mutationSequence}`;

  beforeEach(() => {
    indexedDB = new IDBFactory();
    databaseName = `mutation-outbox-recovery-${crypto.randomUUID()}`;
    mutationSequence = 0;
    setMutationClientIdentityForTests(undefined);
  });

  afterEach(() => {
    const failures: unknown[] = [];
    for (const cleanup of cleanups.splice(0).reverse()) {
      try {
        cleanup();
      } catch (error) {
        failures.push(error);
      }
    }
    if (failures.length > 0) throw new AggregateError(failures, "recovery test cleanup failed");
  });

  function openStores() {
    const repository = createBrowserDraftRepository({ indexedDB, databaseName, storage: new MemoryStorage() });
    const outbox = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: nextMutationId,
      now: () => 100,
    });
    cleanups.push(() => {
      outbox.close();
      repository.close();
    });
    return { repository, outbox };
  }

  async function requireRow(repository: BrowserDraftRepository, key: BrowserDraftKey): Promise<StoredComposerDraft> {
    const row = await repository.load(key);
    if (!row) throw new Error("expected a durable draft row");
    return row;
  }

  function recoveryGuard(token: DraftSessionToken, stillAuthorized: () => boolean) {
    return { key: browserKey(), token, stillAuthorized };
  }

  async function arrangeTransferred(value: SkillEditorValue = RICH_ORDINARY) {
    const { repository, outbox } = openStores();
    await repository.seedDraft(browserKey(), value);
    const sent = await outbox.enqueueIntent(turnIntent("recovery"));
    await outbox.transferToRecovery(sent.clientMutationId, "rejected", "refused");
    return { repository, outbox, id: sent.clientMutationId };
  }

  // A refusal must leave the revision and both complete records untouched.
  async function expectRefusedWithStatePreserved(
    repository: BrowserDraftRepository,
    outbox: MutationOutboxIndexedDB,
    id: string,
    baseline: DraftSessionToken,
  ) {
    const row = await requireRow(repository, browserKey());
    expect(row).toMatchObject(RICH_ORDINARY);
    expect(row.token).toEqual(baseline);
    expect(await outbox.getRecovery(id)).toMatchObject(RECOVERY_SNAPSHOT);
    expect(await outbox.listOutbox(REF)).toEqual([]);
  }

  test("transferToRecovery advances the session revision exactly once and preserves the ordinary draft", async () => {
    const { repository, outbox } = openStores();
    await repository.seedDraft(browserKey(), RICH_ORDINARY);
    expect((await requireRow(repository, browserKey())).token.sessionRevision).toBe(1);

    const sent = await outbox.enqueueIntent(turnIntent("recovery"));
    await outbox.transferToRecovery(sent.clientMutationId, "rejected", "refused");

    const row = await requireRow(repository, browserKey());
    expect(row).toMatchObject(RICH_ORDINARY);
    expect(row.token.sessionRevision).toBe(2);
    expect(await outbox.getRecovery(sent.clientMutationId)).toMatchObject(RECOVERY_SNAPSHOT);
    expect(await outbox.getOutbox(sent.clientMutationId)).toBeUndefined();
  });

  test("transferToRecovery on an absent ordinary draft initializes a metadata row at revision one", async () => {
    const { repository, outbox } = openStores();
    const sent = await outbox.enqueueIntent(turnIntent("recovery"));
    await outbox.transferToRecovery(sent.clientMutationId, "rejected");

    const row = await repository.load(browserKey());
    expect(row).toMatchObject({ text: "", skillNames: [] });
    expect(row?.token.sessionRevision).toBe(1);
  });

  test("updateRecoveryInput advances the session revision exactly once and preserves the ordinary draft", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    const baseline = (await requireRow(repository, browserKey())).token;

    await outbox.updateRecoveryInput(id, EDITED_INPUT, [], "edited", []);

    const row = await requireRow(repository, browserKey());
    expect(row).toMatchObject(RICH_ORDINARY);
    expect(row.token.sessionRevision).toBe(baseline.sessionRevision + 1);
    expect(await outbox.getRecovery(id)).toMatchObject({ composerText: "edited", version: 1 });
  });

  test("discardRecovery advances the session revision exactly once and preserves the ordinary draft", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    const baseline = (await requireRow(repository, browserKey())).token;

    expect(await outbox.discardRecovery(id)).toBe(true);

    const row = await requireRow(repository, browserKey());
    expect(row).toMatchObject(RICH_ORDINARY);
    expect(row.token.sessionRevision).toBe(baseline.sessionRevision + 1);
    expect(await outbox.getRecovery(id)).toBeUndefined();
  });

  test("resendRecovery advances the session revision exactly once and preserves the ordinary draft", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    const baseline = (await requireRow(repository, browserKey())).token;

    const resent = await outbox.resendRecovery(id, turnIntent("resent"));

    expect(resent).toMatchObject({ method: "turn/queue", state: "submitting" });
    const row = await requireRow(repository, browserKey());
    expect(row).toMatchObject(RICH_ORDINARY);
    expect(row.token.sessionRevision).toBe(baseline.sessionRevision + 1);
    expect(await outbox.getRecovery(id)).toBeUndefined();
  });

  test("settleReceipt advances the session revision exactly once when it removes the recovery row", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    const baseline = (await requireRow(repository, browserKey())).token;

    expect(await outbox.settleReceipt(id, "applied")).toBe(true);

    const row = await requireRow(repository, browserKey());
    expect(row).toMatchObject(RICH_ORDINARY);
    expect(row.token.sessionRevision).toBe(baseline.sessionRevision + 1);
    expect(await outbox.getRecovery(id)).toBeUndefined();
  });

  test("settleApplied advances the session revision exactly once when it removes the recovery row", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    const baseline = (await requireRow(repository, browserKey())).token;

    expect(await outbox.settleApplied(id)).toBe(true);

    const row = await requireRow(repository, browserKey());
    expect(row).toMatchObject(RICH_ORDINARY);
    expect(row.token.sessionRevision).toBe(baseline.sessionRevision + 1);
    expect(await outbox.getRecovery(id)).toBeUndefined();
  });

  test("absent recovery records neither resurrect nor advance the revision", async () => {
    const { repository, outbox } = openStores();
    await repository.seedDraft(browserKey(), RICH_ORDINARY);
    const before = (await requireRow(repository, browserKey())).token.sessionRevision;

    expect(await outbox.updateRecoveryInput("missing", EDITED_INPUT, [], "edited", [])).toBeUndefined();
    expect(await outbox.discardRecovery("missing")).toBe(false);
    expect(await outbox.resendRecovery("missing", turnIntent("x"))).toBeUndefined();
    expect(await outbox.getRecovery("missing")).toBeUndefined();
    expect(await outbox.getOutbox("missing")).toBeUndefined();
    expect((await requireRow(repository, browserKey())).token.sessionRevision).toBe(before);
  });

  test("repeated receipt and applied settlement after deletion leave the revision unchanged", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    await outbox.settleReceipt(id, "applied");
    const after = (await requireRow(repository, browserKey())).token.sessionRevision;

    expect(await outbox.settleReceipt(id, "applied")).toBe(false);
    expect(await outbox.settleApplied(id)).toBe(false);
    expect((await requireRow(repository, browserKey())).token.sessionRevision).toBe(after);
  });

  async function arrangeStale() {
    const ctx = await arrangeTransferred();
    const captured = (await requireRow(ctx.repository, browserKey())).token;
    await ctx.repository.commitEdit(browserKey(), captured, RICH_ORDINARY, () => true);
    const current = (await requireRow(ctx.repository, browserKey())).token;
    expect(current.sessionRevision).toBeGreaterThan(captured.sessionRevision);
    return { ...ctx, stale: captured, current };
  }

  test("a captured stale token refuses a recovery update and preserves newer ordinary work", async () => {
    const { repository, outbox, id, stale, current } = await arrangeStale();

    const result = await outbox.updateRecoveryInput(
      id,
      EDITED_INPUT,
      [],
      "edited",
      [],
      recoveryGuard(stale, () => true),
    );

    expect(result).toBeUndefined();
    await expectRefusedWithStatePreserved(repository, outbox, id, current);
  });

  test("a captured stale token refuses a recovery discard and preserves newer ordinary work", async () => {
    const { repository, outbox, id, stale, current } = await arrangeStale();

    expect(
      await outbox.discardRecovery(
        id,
        undefined,
        recoveryGuard(stale, () => true),
      ),
    ).toBe(false);

    await expectRefusedWithStatePreserved(repository, outbox, id, current);
  });

  test("a captured stale token refuses a recovery resend and creates no outbox record", async () => {
    const { repository, outbox, id, stale, current } = await arrangeStale();

    expect(
      await outbox.resendRecovery(
        id,
        turnIntent("resent"),
        recoveryGuard(stale, () => true),
      ),
    ).toBeUndefined();

    await expectRefusedWithStatePreserved(repository, outbox, id, current);
  });

  test("a false live authorization refuses a recovery update even with a current token", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    const baseline = (await requireRow(repository, browserKey())).token;

    const result = await outbox.updateRecoveryInput(
      id,
      EDITED_INPUT,
      [],
      "edited",
      [],
      recoveryGuard(baseline, () => false),
    );

    expect(result).toBeUndefined();
    await expectRefusedWithStatePreserved(repository, outbox, id, baseline);
  });

  test("a false live authorization refuses a recovery discard even with a current token", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    const baseline = (await requireRow(repository, browserKey())).token;

    expect(
      await outbox.discardRecovery(
        id,
        undefined,
        recoveryGuard(baseline, () => false),
      ),
    ).toBe(false);

    await expectRefusedWithStatePreserved(repository, outbox, id, baseline);
  });

  test("a false live authorization refuses a recovery resend even with a current token", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    const baseline = (await requireRow(repository, browserKey())).token;

    expect(
      await outbox.resendRecovery(
        id,
        turnIntent("resent"),
        recoveryGuard(baseline, () => false),
      ),
    ).toBeUndefined();

    await expectRefusedWithStatePreserved(repository, outbox, id, baseline);
  });

  test("a recovery guard with a wrong namespace incarnation is refused at the same numeric revision", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    const baseline = (await requireRow(repository, browserKey())).token;
    const wrongIncarnation: DraftSessionToken = {
      namespaceIncarnation: `${baseline.namespaceIncarnation}-wrong`,
      sessionRevision: baseline.sessionRevision,
    };

    const result = await outbox.updateRecoveryInput(
      id,
      EDITED_INPUT,
      [],
      "edited",
      [],
      recoveryGuard(wrongIncarnation, () => true),
    );

    expect(result).toBeUndefined();
    await expectRefusedWithStatePreserved(repository, outbox, id, baseline);
  });

  test("a guarded update consumes matching ordinary work in the same transaction", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    const token = (await requireRow(repository, browserKey())).token;
    const consume = { token, value: RICH_ORDINARY, stillOwnsValue: () => true };

    const result = await outbox.updateRecoveryInput(
      id,
      EDITED_INPUT,
      [],
      "edited",
      [],
      recoveryGuard(token, () => true),
      consume,
    );

    expect(result).toMatchObject({ composerText: "edited" });
    const row = await requireRow(repository, browserKey());
    expect(row).toMatchObject({ text: "", skillNames: [] });
    expect(row.token.sessionRevision).toBe(token.sessionRevision + 1);
  });

  test("a stale consumeOrdinary token leaves matching ordinary work intact while the recovery edit commits", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    const current = (await requireRow(repository, browserKey())).token;
    const stale: DraftSessionToken = {
      namespaceIncarnation: current.namespaceIncarnation,
      sessionRevision: current.sessionRevision - 1,
    };
    const consume = { token: stale, value: RICH_ORDINARY, stillOwnsValue: () => true };

    const result = await outbox.updateRecoveryInput(
      id,
      EDITED_INPUT,
      [],
      "edited",
      [],
      recoveryGuard(current, () => true),
      consume,
    );

    expect(result).toMatchObject({ composerText: "edited" });
    const row = await requireRow(repository, browserKey());
    expect(row).toMatchObject(RICH_ORDINARY);
    expect(row.token.sessionRevision).toBe(current.sessionRevision + 1);
  });

  test("a guarded update with a mismatched ordinary value retains that work and commits the recovery edit", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    const token = (await requireRow(repository, browserKey())).token;
    const consume = { token, value: { ...RICH_ORDINARY, text: "different" }, stillOwnsValue: () => true };

    await outbox.updateRecoveryInput(
      id,
      EDITED_INPUT,
      [],
      "edited",
      [],
      recoveryGuard(token, () => true),
      consume,
    );

    expect(await outbox.getRecovery(id)).toMatchObject({ composerText: "edited" });
    const row = await requireRow(repository, browserKey());
    expect(row).toMatchObject(RICH_ORDINARY);
    expect(row.token.sessionRevision).toBe(token.sessionRevision + 1);
  });

  test("a guarded update whose ordinary claim is lost retains that work and commits the recovery edit", async () => {
    const { repository, outbox, id } = await arrangeTransferred();
    const token = (await requireRow(repository, browserKey())).token;
    const consume = { token, value: RICH_ORDINARY, stillOwnsValue: () => false };

    await outbox.updateRecoveryInput(
      id,
      EDITED_INPUT,
      [],
      "edited",
      [],
      recoveryGuard(token, () => true),
      consume,
    );

    expect(await outbox.getRecovery(id)).toMatchObject({ composerText: "edited" });
    const row = await requireRow(repository, browserKey());
    expect(row).toMatchObject(RICH_ORDINARY);
    expect(row.token.sessionRevision).toBe(token.sessionRevision + 1);
  });

  test("ordinary consumption distinguishes absent mentions from an explicit empty array", async () => {
    const { repository, outbox, id } = await arrangeTransferred(WITHOUT_MENTIONS);
    const token = (await requireRow(repository, browserKey())).token;
    expect((await requireRow(repository, browserKey())).mentions).toBeUndefined();
    const consume = { token, value: WITH_EMPTY_MENTIONS, stillOwnsValue: () => true };

    await outbox.updateRecoveryInput(
      id,
      EDITED_INPUT,
      [],
      "edited",
      [],
      recoveryGuard(token, () => true),
      consume,
    );

    expect(await outbox.getRecovery(id)).toMatchObject({ composerText: "edited" });
    const row = await requireRow(repository, browserKey());
    expect(row).toMatchObject(WITHOUT_MENTIONS);
    expect(row.mentions).toBeUndefined();
    expect(row.token.sessionRevision).toBe(token.sessionRevision + 1);
  });

  test("ordinary consumption distinguishes an explicit empty mentions array from absent", async () => {
    const { repository, outbox, id } = await arrangeTransferred(WITH_EMPTY_MENTIONS);
    const token = (await requireRow(repository, browserKey())).token;
    expect((await requireRow(repository, browserKey())).mentions).toEqual([]);
    const consume = { token, value: WITHOUT_MENTIONS, stillOwnsValue: () => true };

    await outbox.updateRecoveryInput(
      id,
      EDITED_INPUT,
      [],
      "edited",
      [],
      recoveryGuard(token, () => true),
      consume,
    );

    expect(await outbox.getRecovery(id)).toMatchObject({ composerText: "edited" });
    const row = await requireRow(repository, browserKey());
    expect(row).toMatchObject(WITH_EMPTY_MENTIONS);
    expect(row.mentions).toEqual([]);
    expect(row.token.sessionRevision).toBe(token.sessionRevision + 1);
  });

  async function arrangeSuperseded() {
    const { repository, outbox } = openStores();
    await repository.seedDraft(browserKey(), RICH_ORDINARY);
    const first = await outbox.enqueueIntent(noteIntent("note one"));
    await outbox.transferToRecovery(first.clientMutationId, "rejected");
    const second = await outbox.enqueueIntent(noteIntent("note two"));
    await outbox.transferToRecovery(second.clientMutationId, "rejected");
    const newer = await outbox.enqueueIntent(noteIntent("note three"));
    await outbox.transferToRecovery(newer.clientMutationId, "rejected");
    const other = await outbox.enqueueIntent(noteIntent("other ref note", "other"));
    await outbox.transferToRecovery(other.clientMutationId, "rejected");
    return {
      repository,
      outbox,
      first: first.clientMutationId,
      second: second.clientMutationId,
      newer: newer.clientMutationId,
      other: other.clientMutationId,
    };
  }

  async function expectSupersededRemoval(
    settle: (ctx: Awaited<ReturnType<typeof arrangeSuperseded>>) => Promise<boolean>,
  ) {
    const ctx = await arrangeSuperseded();
    const { repository, outbox } = ctx;
    const before = (await requireRow(repository, browserKey())).token.sessionRevision;
    const notified: string[] = [];
    outbox.setSupersededDiscardListener((targetRef) => notified.push(targetRef));
    try {
      expect(await settle(ctx)).toBe(true);
      // A read queues behind the settle's own background supersede discard, so
      // it doubles as the drain even when a later assertion fails.
      await outbox.listOutbox(REF);
      expect(await outbox.getRecovery(ctx.second)).toBeUndefined();
      expect(await outbox.getRecovery(ctx.first)).toBeUndefined();
      expect(await outbox.getRecovery(ctx.newer)).toBeDefined();
      expect(await outbox.getRecovery(ctx.other)).toBeDefined();
      const row = await requireRow(repository, browserKey());
      expect(row).toMatchObject(RICH_ORDINARY);
      expect(row.token.sessionRevision).toBe(before + 1);
      expect(notified).toEqual([REF]);
    } finally {
      try {
        // Drain the still-owned background write, reporting a failure rather
        // than swallowing it.
        await outbox.listOutbox(REF);
      } finally {
        outbox.setSupersededDiscardListener(undefined);
      }
    }
  }

  test("settling a later note removes the superseded recovery row and advances the revision once", async () => {
    await expectSupersededRemoval((ctx) => ctx.outbox.settleReceipt(ctx.second, "applied"));
  });

  test("settleApplied removes the superseded recovery row and advances the revision once", async () => {
    await expectSupersededRemoval((ctx) => ctx.outbox.settleApplied(ctx.second));
  });

  async function expectRollbackAndRetry(
    crashing: MutationOutboxIndexedDB,
    run: (id: string, pre: StoredComposerDraft) => Promise<void>,
  ) {
    const { repository, outbox, id } = await arrangeTransferred();
    const pre = await requireRow(repository, browserKey());
    const preRecovery = await outbox.getRecovery(id);
    cleanups.push(() => crashing.close());
    await run(id, pre);

    // Both complete records and the exact revision are unchanged after the abort.
    const abortedRow = await requireRow(repository, browserKey());
    expect(abortedRow).toMatchObject(RICH_ORDINARY);
    expect(abortedRow).toEqual(pre);
    const abortedRecovery = await outbox.getRecovery(id);
    expect(abortedRecovery).toMatchObject(RECOVERY_SNAPSHOT);
    expect(abortedRecovery).toEqual(preRecovery);

    // Close every original adapter before the fresh reopen verification.
    crashing.close();
    outbox.close();
    repository.close();
    const reopened = openStores();
    const reopenedRow = await requireRow(reopened.repository, browserKey());
    expect(reopenedRow).toMatchObject(RICH_ORDINARY);
    expect(reopenedRow).toEqual(pre);
    const reopenedRecovery = await reopened.outbox.getRecovery(id);
    expect(reopenedRecovery).toMatchObject(RECOVERY_SNAPSHOT);
    expect(reopenedRecovery).toEqual(preRecovery);

    const consume = { token: pre.token, value: RICH_ORDINARY, stillOwnsValue: () => true };
    const result = await reopened.outbox.updateRecoveryInput(
      id,
      EDITED_INPUT,
      [],
      "edited",
      [],
      recoveryGuard(pre.token, () => true),
      consume,
    );
    expect(result).toMatchObject({ composerText: "edited" });
    const finalRow = await requireRow(reopened.repository, browserKey());
    expect(finalRow).toMatchObject({ text: "", skillNames: [] });
    expect(finalRow.token.sessionRevision).toBe(pre.token.sessionRevision + 1);
  }

  test("a commit failure rolls recovery and composition back together, and a healthy retry applies both", async () => {
    const crashing = new MutationOutboxIndexedDB({
      indexedDB,
      databaseName,
      createMutationId: nextMutationId,
      beforeCommit(operation) {
        if (operation === "updateRecoveryInput") throw new Error("commit refused");
      },
    });
    await expectRollbackAndRetry(crashing, async (id, pre) => {
      const consume = { token: pre.token, value: RICH_ORDINARY, stillOwnsValue: () => true };
      await expect(
        crashing.updateRecoveryInput(
          id,
          EDITED_INPUT,
          [],
          "edited",
          [],
          recoveryGuard(pre.token, () => true),
          consume,
        ),
      ).rejects.toThrow("commit refused");
    });
  });

  test("a real request error rolls recovery and composition back together, and a healthy retry applies both", async () => {
    const injected = injectRecoveryPutFailure(indexedDB);
    cleanups.push(() => injected.restore());
    const crashing = new MutationOutboxIndexedDB({
      indexedDB: injected.indexedDB,
      databaseName,
      createMutationId: nextMutationId,
    });
    await expectRollbackAndRetry(crashing, async (id, pre) => {
      const consume = { token: pre.token, value: RICH_ORDINARY, stillOwnsValue: () => true };
      await expect(
        crashing.updateRecoveryInput(
          id,
          EDITED_INPUT,
          [],
          "edited",
          [],
          recoveryGuard(pre.token, () => true),
          consume,
        ),
      ).rejects.toBeDefined();
      injected.restore();
      // The injected duplicate-key request really errored (a ConstraintError, not
      // the unexpected-success branch) and really aborted the transaction: not a
      // synchronous guard and not a substituted request.
      const requestError = await injected.requestError;
      expect((requestError as { name?: string }).name).toBe("ConstraintError");
      expect(await injected.aborted).toBe(true);
    });
  });
});
