// Owner-free browser draft repository: tests for the approved final contract.
//
// Checkpoint 2 fix round 1 (tests only, 2026-10-07). Production is still the
// original baseline repository, so these tests exercise the FINAL desired
// behavior and are expected to fail as missing-API/contract failures until a
// later bounded implementation phase supplies `seedDraft`, the owner-free
// `commitEdit` signature and the `browserDraftKey` helper. No compatibility
// adapter is added: the file is structured so the module collects and each
// case fails on its own API or assertion rather than one module-level import.
import type { DraftBackingIdentity, DraftSessionToken, SubmittedDraft } from "@evener/appwire-client/state/mutation";
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
  getBrowserDraftRepository,
  resetBrowserDraftRepositoryForTests,
  type StoredComposerDraft,
} from "./browserDraftRepository";
import {
  ANNOTATION_NAMESPACES_STORE,
  COMPOSER_DRAFTS_STORE,
  DRAFT_CONFLICTS_STORE,
  DRAFT_TRANSFERS_STORE,
  LOCAL_COMPOSITION_DATABASE_VERSION,
} from "./localCompositionSchema";

const namespace = { kind: "browser" as const, origin: "https://hub.test" };
const key = (sessionRef: string): BrowserDraftKey => ({ namespace, sessionRef });

// Owner-free result shape: an explicit edit is committed or conflicts on its
// durable session token; there is no owner identity in the result.
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

async function openRawDatabase(indexedDB: IDBFactory, databaseName: string): Promise<IDBDatabase> {
  return new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open(databaseName, LOCAL_COMPOSITION_DATABASE_VERSION);
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error ?? new Error("open failed")), { once: true });
  });
}

// Read a store's rows through the public IndexedDB API, not repository code, so
// rollback assertions observe the durable state directly. The raw connection is
// always closed, even on a failed request.
async function readStoreRows(indexedDB: IDBFactory, databaseName: string, store: string): Promise<unknown[]> {
  const database = await openRawDatabase(indexedDB, databaseName);
  try {
    const read = database.transaction(store, "readonly");
    const rows = await new Promise<unknown[]>((resolve, reject) => {
      const request = read.objectStore(store).getAll();
      request.addEventListener("success", () => resolve(request.result), { once: true });
      request.addEventListener("error", () => reject(request.error ?? new Error("read failed")), { once: true });
    });
    await transactionCompletion(read);
    return rows;
  } finally {
    database.close();
  }
}

// Advance the durable namespace incarnation through public APIs, closing the raw
// connection on every path.
async function advanceNamespace(indexedDB: IDBFactory, databaseName: string, incarnation: string): Promise<void> {
  const database = await openRawDatabase(indexedDB, databaseName);
  try {
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
  } finally {
    database.close();
  }
}

// Every adapter, raw database and runtime opened by a test is torn down before
// the next test. Cleanups run in three phases: runtimes are disposed first (so
// no later lifecycle trigger starts a scan), then the real transfer promises a
// test already observed are drained, then repositories and the visibility
// descriptor are closed/restored. Every phase still runs after an earlier
// cleanup throws, and the failures are aggregated and rethrown so a broken
// cleanup cannot pass as a successful hook.
type CleanupPhase = "dispose" | "drain" | "close";
const cleanups: Record<CleanupPhase, Array<() => void | Promise<void>>> = {
  dispose: [],
  drain: [],
  close: [],
};
function closeAfterTest(fn: () => void | Promise<void>, phase: CleanupPhase = "close"): void {
  cleanups[phase].push(fn);
}
afterEach(async () => {
  const failures: unknown[] = [];
  const runPhase = async (entries: Array<() => void | Promise<void>>): Promise<void> => {
    for (const cleanup of entries.splice(0).reverse()) {
      try {
        await cleanup();
      } catch (error) {
        failures.push(error);
      }
    }
  };
  await runPhase(cleanups.dispose);
  await runPhase(cleanups.drain);
  await runPhase(cleanups.close);
  vi.restoreAllMocks();
  if (failures.length > 0) throw new AggregateError(failures, "browser draft test cleanup failed");
});

// Override document.visibilityState and guarantee the descriptor is restored
// last, after runtimes are disposed and in-flight transfers are drained.
function overrideVisibility(value: string): void {
  const original = Object.getOwnPropertyDescriptor(document, "visibilityState");
  Object.defineProperty(document, "visibilityState", { value, configurable: true });
  closeAfterTest(() => {
    if (original) Object.defineProperty(document, "visibilityState", original);
    else delete (document as unknown as Record<string, unknown>).visibilityState;
  });
}

async function flushMicrotasks(): Promise<void> {
  for (let turn = 0; turn < 3; turn += 1) await new Promise<void>((resolve) => queueMicrotask(resolve));
}

// Test-local transparent delegation to the real repository's public transfer
// method. It records each real returned promise without mocking an outcome, so
// the runtime tests can observe actual settlement (including a pending result
// on an injected failure) rather than a beforeCommit entry.
function observeTransfers(real: BrowserDraftRepository): {
  repository: BrowserDraftRepository;
  settle: (index: number) => Promise<{ state: "committed" | "pending" }>;
  attempts: () => number;
  drain: () => Promise<void>;
} {
  const transfers: Array<Promise<{ state: "committed" | "pending" }>> = [];
  let waiters: Array<() => void> = [];
  const repository = new Proxy(real, {
    get(target, prop) {
      if (prop === "transferSupportedDrafts") {
        return (targetNamespace: BrowserDraftKey["namespace"]) => {
          const promise = target.transferSupportedDrafts(targetNamespace);
          transfers.push(promise);
          const pending = waiters;
          waiters = [];
          for (const wake of pending) wake();
          return promise;
        };
      }
      const value = Reflect.get(target, prop, target) as unknown;
      return typeof value === "function" ? (value as (...args: unknown[]) => unknown).bind(target) : value;
    },
  }) as BrowserDraftRepository;
  const settle = async (index: number) => {
    while (transfers.length <= index) await new Promise<void>((resolve) => waiters.push(resolve));
    const promise = transfers[index];
    if (!promise) throw new Error(`transfer ${index} missing`);
    const result = await promise;
    await flushMicrotasks();
    return result;
  };
  // Finish every already-observed real transfer promise without starting a scan.
  const drain = async () => {
    await Promise.allSettled(transfers.slice());
  };
  return { repository, settle, attempts: () => transfers.length, drain };
}

test("browserDraftKey builds the browser namespace key from a session ref and an explicit or default origin", () => {
  expect(browserDraftKey("root", "https://hub.test")).toEqual({
    namespace: { kind: "browser", origin: "https://hub.test" },
    sessionRef: "root",
  });
  expect(browserDraftKey("default")).toEqual({
    namespace: { kind: "browser", origin: location.origin },
    sessionRef: "default",
  });
});

test("an interrupted post-commit cleanup retains source bytes and markers, and replayed bytes cannot resurrect the cleared draft", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  const legacyV1 = draftStorageKey("root");
  const legacyV2 = composerDraftStorageKey("root");
  const sourceBytes = '{"text":"literal /same"}';
  storage.setItem(legacyV1, sourceBytes);
  let cleanupInterruptions = 0;
  const repository = createBrowserDraftRepository({
    indexedDB,
    databaseName: "transfer-test",
    storage,
    beforeLegacyCleanup: () => {
      // One-shot: the first cleanup is interrupted, later scans clean normally.
      if (cleanupInterruptions > 0) return;
      cleanupInterruptions += 1;
      throw new Error("cleanup interrupted");
    },
  });
  closeAfterTest(() => repository.close());

  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  // The single interruption kept the source bytes and recorded exact inspected
  // markers for both supported keys, including the absent v2 value as null.
  expect(cleanupInterruptions).toBe(1);
  expect(storage.getItem(legacyV1)).toBe(sourceBytes);
  const markers = (await readStoreRows(indexedDB, "transfer-test", DRAFT_TRANSFERS_STORE)) as Array<{
    legacyKey: string;
    inspectedBytes: string | null;
  }>;
  expect(markers).toHaveLength(2);
  expect(markers).toEqual(
    expect.arrayContaining([
      expect.objectContaining({ legacyKey: legacyV1, inspectedBytes: sourceBytes }),
      expect.objectContaining({ legacyKey: legacyV2, inspectedBytes: null }),
    ]),
  );

  // Close the original adapter before reopening.
  repository.close();
  const restarted = createBrowserDraftRepository({ indexedDB, databaseName: "transfer-test", storage });
  closeAfterTest(() => restarted.close());
  const imported = await restarted.load(key("root"));
  if (!imported) throw new Error("transferred draft missing");
  // The complete imported public row: exact value shape plus the token fields.
  expect(imported).toEqual({
    text: sourceBytes,
    skillNames: [],
    key: key("root"),
    token: { namespaceIncarnation: expect.any(String), sessionRevision: expect.any(Number) },
  });

  const submitted = committed(
    await restarted.commitEdit(key("root"), imported.token, { text: sourceBytes, skillNames: [] }, () => true),
  );
  expect(
    await restarted.clearSubmitted(key("root"), {
      draftRevisionAtStart: 0,
      text: sourceBytes,
      skillNames: [],
      settlementGuard: captureDraftSettlementGuard(submitted, { kind: "draft" }, 0),
      stillOwnsEdit: () => true,
    }),
  ).toEqual({ cleared: true, draftUnchanged: true, persistence: "committed" });

  // The durable row is already empty immediately after clear, BEFORE the
  // retained supported bytes are replayed.
  const clearedRow = await restarted.load(key("root"));
  if (!clearedRow) throw new Error("cleared durable row missing");
  expect(clearedRow).toEqual({
    text: "",
    skillNames: [],
    key: key("root"),
    token: {
      namespaceIncarnation: submitted.token.namespaceIncarnation,
      sessionRevision: submitted.token.sessionRevision + 1,
    },
  });

  // The retained identical supported bytes replay after the clear and cannot
  // resurrect the row; the exhausted one-shot hook now allows healthy cleanup.
  expect(await restarted.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  const durable = await restarted.load(key("root"));
  if (!durable) throw new Error("durable row missing");
  expect(durable.text).toBe("");
  expect(durable.token.sessionRevision).toBe(submitted.token.sessionRevision + 1);
  expect(durable.token.namespaceIncarnation).toBe(submitted.token.namespaceIncarnation);
  expect(storage.getItem(legacyV1)).toBeNull();
});

test("transfer preserves decoder semantics and nonempty mention metadata for structured, invalid, JSON-looking and slash drafts", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  const mentions = [
    { kind: "skill" as const, name: "same", offset: 10 },
    { kind: "command" as const, name: "command", offset: 20 },
  ];
  storage.setItem(
    composerDraftStorageKey("structured"),
    JSON.stringify({
      text: "structured /same /command",
      skillNames: [" same ", "same"],
      commandNames: ["command"],
      mentions,
    }),
  );
  storage.setItem(composerDraftStorageKey("fallback"), '{"text":4,"skillNames":[]}');
  storage.setItem(draftStorageKey("fallback"), "literal fallback /same");
  storage.setItem(draftStorageKey("json"), '{"text":"literal","skillNames":["not-selected"]} /slash');
  storage.setItem(composerDraftStorageKey("unsupported"), "not json at all");
  storage.setItem(
    composerDraftStorageKey("empty-mentions"),
    JSON.stringify({ text: "empty mentions /same", skillNames: ["same"], mentions: [] }),
  );
  const repository = createBrowserDraftRepository({ indexedDB, databaseName: "decoder-transfer", storage });
  closeAfterTest(() => repository.close());

  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  // Nonempty skill and command mentions survive the actual structured transfer.
  expect(await repository.load(key("structured"))).toMatchObject({
    text: "structured /same /command",
    skillNames: ["same"],
    commandNames: ["command"],
    mentions,
  });
  // A separate structured migration keeps an explicit empty mentions property
  // with its exact empty array (not inferred absence).
  const emptyMentions = await repository.load(key("empty-mentions"));
  if (!emptyMentions) throw new Error("empty-mentions import missing");
  expect(emptyMentions).toHaveProperty("mentions");
  expect(emptyMentions.mentions).toEqual([]);
  // The literal slash in THAT loaded explicit-empty row stays literal prose: an
  // explicit empty mentions array suppresses the atom even though skillNames
  // lists "same" and the text spells /same.
  expect(parseSkillDocument(emptyMentions).toJSON()).toEqual({
    type: "doc",
    content: [{ type: "text", text: "empty mentions /same" }],
  });
  expect(await repository.load(key("fallback"))).toMatchObject({ text: "literal fallback /same", skillNames: [] });
  expect(await repository.load(key("json"))).toMatchObject({
    text: '{"text":"literal","skillNames":["not-selected"]} /slash',
    skillNames: [],
  });
  const jsonRow = await repository.load(key("json"));
  if (!jsonRow) throw new Error("json import missing");
  // A slash inside literal v1 JSON bytes stays literal prose, not a skill atom.
  expect(parseSkillDocument(jsonRow).toJSON()).toEqual({
    type: "doc",
    content: [{ type: "text", text: '{"text":"literal","skillNames":["not-selected"]} /slash' }],
  });
  // Invalid v2 bytes with no v1 fallback decode to empty, not to a guess.
  expect(await repository.load(key("unsupported"))).toMatchObject({ text: "", skillNames: [] });
  expect(storage.length).toBe(0);
});

test("two adapters racing one edit let the durable winner advance by one and the loser rebase", async () => {
  const factory = new IDBFactory();
  const databaseName = "cas-race";
  const a = createBrowserDraftRepository({ indexedDB: factory, databaseName });
  closeAfterTest(() => a.close());
  const b = createBrowserDraftRepository({ indexedDB: factory, databaseName });
  closeAfterTest(() => b.close());

  const base = committed(await a.seedDraft(key("race"), { text: "/same", skillNames: ["same"], mentions: [] }));
  const [aResult, bResult] = await Promise.all([
    a.commitEdit(key("race"), base.token, { text: "first", skillNames: [] }, () => true),
    b.commitEdit(key("race"), base.token, { text: "second", skillNames: [] }, () => true),
  ]);
  if (!aResult || !bResult) throw new Error("missing race result");
  expect([aResult.state, bResult.state].filter((state) => state === "committed")).toHaveLength(1);
  expect([aResult.state, bResult.state].filter((state) => state === "conflict")).toHaveLength(1);

  // Retain adapter/result correspondence and never assume which transaction wins.
  const competitors = [
    { adapter: a, result: aResult },
    { adapter: b, result: bResult },
  ];
  const winnerEntry = competitors.find((entry) => entry.result.state === "committed");
  const loserEntry = competitors.find((entry) => entry.result.state === "conflict");
  if (!winnerEntry || !loserEntry) throw new Error("race must produce one winner and one loser");
  const winner = committed(winnerEntry.result);

  // The adapter whose own raced result conflicted loads the complete durable winner.
  const durable = await loserEntry.adapter.load(key("race"));
  if (!durable) throw new Error("missing durable winner");
  expect(durable).toEqual(winner);
  expect(durable.token.sessionRevision).toBe(base.token.sessionRevision + 1);
  // The conflict carries the same complete durable row.
  if (loserEntry.result.state !== "conflict") throw new Error("expected conflict");
  expect(loserEntry.result.current).toEqual(durable);

  // The adapter whose own raced result conflicted loads the durable winner and
  // rebases a later explicit write against that loaded token.
  const rebased = committed(
    await loserEntry.adapter.commitEdit(
      key("race"),
      durable.token,
      {
        text: "third",
        skillNames: ["skill"],
        commandNames: ["cmd"],
        mentions: [{ kind: "skill", name: "skill", offset: 0 }],
      },
      () => true,
    ),
  );
  expect(rebased.token.sessionRevision).toBe(durable.token.sessionRevision + 1);
  expect(rebased).toMatchObject({
    text: "third",
    skillNames: ["skill"],
    commandNames: ["cmd"],
    mentions: [{ kind: "skill", name: "skill", offset: 0 }],
  });
  // The durable reload's complete public row equals the returned committed row.
  const durableLater = await loserEntry.adapter.load(key("race"));
  if (!durableLater) throw new Error("missing durable rebased row");
  expect(durableLater).toEqual(rebased);
});

test("a later explicit same-session edit from a different pane owner commits and wins", async () => {
  const factory = new IDBFactory();
  const databaseName = "same-session-later-edit";
  const paneA = createBrowserDraftRepository({ indexedDB: factory, databaseName });
  closeAfterTest(() => paneA.close());
  const paneB = createBrowserDraftRepository({ indexedDB: factory, databaseName });
  closeAfterTest(() => paneB.close());

  // Pane A seeds and commits one explicit edit for the session.
  const seeded = committed(
    await paneA.seedDraft(key("same-session"), { text: "/same", skillNames: ["same"], mentions: [] }),
  );
  committed(
    await paneA.commitEdit(key("same-session"), seeded.token, { text: "pane a edit", skillNames: [] }, () => true),
  );

  // Pane B loads the refreshed durable token, then makes its own later explicit
  // edit. No claim, adopt or takeover precedes it, and the local authority
  // callback is true. The write is independent of any durable owner identity.
  const refreshed = await paneB.load(key("same-session"));
  if (!refreshed) throw new Error("missing refreshed durable row");
  const later = committed(
    await paneB.commitEdit(
      key("same-session"),
      refreshed.token,
      { text: "pane b later edit", skillNames: [] },
      () => true,
    ),
  );
  expect(later.text).toBe("pane b later edit");
  expect((await paneA.load(key("same-session")))?.text).toBe("pane b later edit");
});

test("commitEdit advances the session revision for the first edit, identical values and metadata-only changes", async () => {
  const factory = new IDBFactory();
  const databaseName = "revision-advance";
  const a = createBrowserDraftRepository({ indexedDB: factory, databaseName });
  closeAfterTest(() => a.close());
  const b = createBrowserDraftRepository({ indexedDB: factory, databaseName });
  closeAfterTest(() => b.close());

  const base = committed(await a.seedDraft(key("revision"), { text: "/same", skillNames: ["same"], mentions: [] }));
  const edited = committed(
    await a.commitEdit(key("revision"), base.token, { text: "last", skillNames: [] }, () => true),
  );
  expect(edited.token.sessionRevision).toBe(base.token.sessionRevision + 1);
  // An identical explicit value still advances the durable revision.
  const identical = committed(
    await b.commitEdit(key("revision"), edited.token, { text: "last", skillNames: [] }, () => true),
  );
  expect(identical.token.sessionRevision).toBe(edited.token.sessionRevision + 1);
  // A metadata-only change advances it again and persists the metadata.
  const metadata = committed(
    await a.commitEdit(
      key("revision"),
      identical.token,
      { text: "last", skillNames: [], commandNames: ["plan"] },
      () => true,
    ),
  );
  expect(metadata.token.sessionRevision).toBe(identical.token.sessionRevision + 1);
  expect(await b.load(key("revision"))).toMatchObject({ text: "last", commandNames: ["plan"] });
});

test("commitEdit treats omitted and explicit empty mentions as distinct editor values", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "mention-presence",
  });
  closeAfterTest(() => repository.close());
  const omittedValue = { text: "/same", skillNames: ["same"] };
  const seeded = committed(await repository.seedDraft(key("mentions"), omittedValue));

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
      { text: "/same", skillNames: ["same"], mentions: [] },
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
});

test("commitEdit preserves complete nonempty skill and command mention metadata", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "mention-metadata",
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
  expect(edited).toEqual({
    ...value,
    key: key("mention-metadata"),
    token: edited.token,
  });
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

test("seedDraft creates only an absent row, an explicit empty revision wins over migration, and transfer never archives", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  const repository = createBrowserDraftRepository({ indexedDB, databaseName: "seed-once", storage });
  closeAfterTest(() => repository.close());
  const first = committed(await repository.seedDraft(key("seed"), { text: "first", skillNames: [] }));
  expect(await repository.seedDraft(key("seed"), { text: "second", skillNames: [] })).toMatchObject({
    state: "conflict",
    current: { text: "first" },
  });

  // An explicit empty edit is a durable revision: later legacy bytes for the
  // same session never overwrite it and never create a losing-draft archive.
  const legacy = composerDraftStorageKey("seed");
  storage.setItem(legacy, JSON.stringify({ text: "imported", skillNames: [] }));
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  committed(await repository.commitEdit(key("seed"), first.token, { text: "", skillNames: [] }, () => true));
  storage.setItem(legacy, JSON.stringify({ text: "later legacy", skillNames: [] }));
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  const durable = await repository.load(key("seed"));
  if (!durable) throw new Error("durable row missing");
  expect(durable.text).toBe("");
  expect(await readStoreRows(indexedDB, "seed-once", DRAFT_CONFLICTS_STORE)).toEqual([]);
});

test("captureDraftSettlementGuard copies the caller's token and backing and freezes only the copy", () => {
  const token: DraftSessionToken = { namespaceIncarnation: "incarnation", sessionRevision: 4 };
  const backing: DraftBackingIdentity = { kind: "recovery", recoveryId: "r1" };
  const row: StoredComposerDraft = { text: "x", skillNames: [], key: key("guard"), token };
  const guard = captureDraftSettlementGuard(row, backing, 7);

  expect(guard.token).toEqual(token);
  expect(guard.token).not.toBe(token);
  expect(guard.backing).not.toBe(backing);
  expect(guard.backing).toEqual({ kind: "recovery", recoveryId: "r1" });
  expect(guard.localGeneration).toBe(7);
  expect(Object.isFrozen(guard)).toBe(true);
  expect(Object.isFrozen(guard.token)).toBe(true);
  expect(Object.isFrozen(guard.backing)).toBe(true);
  // Mutating the caller-owned originals cannot change the captured guard.
  (token as { sessionRevision: number }).sessionRevision = 99;
  (backing as { recoveryId: string }).recoveryId = "changed";
  expect(guard.token.sessionRevision).toBe(4);
  expect(guard.backing).toEqual({ kind: "recovery", recoveryId: "r1" });
  expect(() => {
    (guard.token as { sessionRevision: number }).sessionRevision = 5;
  }).toThrow();
});

test("settlement clears only the exact live draft and refuses stale, mismatched and incomplete submissions", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "settlement-guards",
  });
  closeAfterTest(() => repository.close());
  const value = {
    text: "send me",
    skillNames: ["skill"],
    commandNames: ["cmd"],
    mentions: [{ kind: "skill" as const, name: "skill", offset: 0 }],
  };
  const live = committed(await repository.seedDraft(key("settle"), value));
  const preserved = async () => {
    expect(await repository.load(key("settle"))).toEqual(live);
  };

  // The generation captured at submission entry, and the live source generation
  // the callback compares it against. No numeric equality between stored fields
  // is assumed; the live check connects the two.
  const capturedGeneration = 5;
  let currentGeneration = capturedGeneration;
  const stillOwnsCurrentGeneration = () => currentGeneration === capturedGeneration;
  const submitted: SubmittedDraft = { draftRevisionAtStart: capturedGeneration, ...value };
  const liveGuard = () => captureDraftSettlementGuard(live, { kind: "draft" }, capturedGeneration);

  // Missing guard.
  expect(await repository.clearSubmitted(key("settle"), { ...submitted })).toEqual({
    cleared: false,
    draftUnchanged: false,
    persistence: "committed",
  });
  await preserved();
  // A present guard with no live-edit callback.
  expect(await repository.clearSubmitted(key("settle"), { ...submitted, settlementGuard: liveGuard() })).toEqual({
    cleared: false,
    draftUnchanged: true,
    persistence: "committed",
  });
  await preserved();
  // A newer local edit makes the live callback false while token, value and
  // backing all still match.
  currentGeneration += 1;
  expect(
    await repository.clearSubmitted(key("settle"), {
      ...submitted,
      settlementGuard: liveGuard(),
      stillOwnsEdit: stillOwnsCurrentGeneration,
    }),
  ).toEqual({ cleared: false, draftUnchanged: true, persistence: "committed" });
  await preserved();
  currentGeneration = capturedGeneration;
  // A recovery backing is never the ordinary submitted generation.
  expect(
    await repository.clearSubmitted(key("settle"), {
      ...submitted,
      settlementGuard: captureDraftSettlementGuard(live, { kind: "recovery", recoveryId: "r" }, capturedGeneration),
      stillOwnsEdit: stillOwnsCurrentGeneration,
    }),
  ).toEqual({ cleared: false, draftUnchanged: false, persistence: "committed" });
  await preserved();
  // A stale durable token refuses.
  expect(
    await repository.clearSubmitted(key("settle"), {
      ...submitted,
      settlementGuard: captureDraftSettlementGuard(
        { ...live, token: { ...live.token, sessionRevision: live.token.sessionRevision + 1 } },
        { kind: "draft" },
        capturedGeneration,
      ),
      stillOwnsEdit: stillOwnsCurrentGeneration,
    }),
  ).toEqual({ cleared: false, draftUnchanged: false, persistence: "committed" });
  await preserved();

  // Exact submitted-value mismatches: no clear, committed persistence, and the
  // complete durable row preserved after EVERY refusal. draftUnchanged is not
  // pinned here. Mention KIND varies alone, keeping name and offset.
  const mismatches: SubmittedDraft[] = [
    { ...submitted, text: "other", settlementGuard: liveGuard(), stillOwnsEdit: stillOwnsCurrentGeneration },
    { ...submitted, skillNames: ["other"], settlementGuard: liveGuard(), stillOwnsEdit: stillOwnsCurrentGeneration },
    { ...submitted, commandNames: [], settlementGuard: liveGuard(), stillOwnsEdit: stillOwnsCurrentGeneration },
    {
      ...submitted,
      mentions: [{ kind: "command", name: "skill", offset: 0 }],
      settlementGuard: liveGuard(),
      stillOwnsEdit: stillOwnsCurrentGeneration,
    },
    {
      ...submitted,
      mentions: [{ kind: "skill", name: "other", offset: 0 }],
      settlementGuard: liveGuard(),
      stillOwnsEdit: stillOwnsCurrentGeneration,
    },
    {
      ...submitted,
      mentions: [{ kind: "skill", name: "skill", offset: 1 }],
      settlementGuard: liveGuard(),
      stillOwnsEdit: stillOwnsCurrentGeneration,
    },
    // A nonempty durable submission refused against an explicit empty submitted
    // array is its own boundary, distinct from absent-versus-empty above.
    { ...submitted, mentions: [], settlementGuard: liveGuard(), stillOwnsEdit: stillOwnsCurrentGeneration },
  ];
  for (const mismatch of mismatches) {
    expect(await repository.clearSubmitted(key("settle"), mismatch)).toMatchObject({
      cleared: false,
      persistence: "committed",
    });
    await preserved();
  }

  // The exact live draft with the unchanged live generation clears and advances
  // the revision by one, preserving the incarnation.
  expect(
    await repository.clearSubmitted(key("settle"), {
      ...submitted,
      settlementGuard: liveGuard(),
      stillOwnsEdit: stillOwnsCurrentGeneration,
    }),
  ).toEqual({ cleared: true, draftUnchanged: true, persistence: "committed" });
  const cleared = await repository.load(key("settle"));
  if (!cleared) throw new Error("cleared row missing");
  expect(cleared.text).toBe("");
  expect(cleared.token.sessionRevision).toBe(live.token.sessionRevision + 1);
  expect(cleared.token.namespaceIncarnation).toBe(live.token.namespaceIncarnation);
});

test("settlement treats omitted and explicit empty submitted mentions as distinct from the durable row", async () => {
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "settlement-mention-presence",
  });
  closeAfterTest(() => repository.close());

  // Durable row with OMITTED mentions: an explicit empty submitted array differs.
  const omittedRow = committed(await repository.seedDraft(key("omitted"), { text: "x", skillNames: [] }));
  expect(await repository.load(key("omitted"))).not.toHaveProperty("mentions");
  expect(
    await repository.clearSubmitted(key("omitted"), {
      draftRevisionAtStart: 0,
      text: "x",
      skillNames: [],
      mentions: [],
      settlementGuard: captureDraftSettlementGuard(omittedRow, { kind: "draft" }, 0),
      stillOwnsEdit: () => true,
    }),
  ).toMatchObject({ cleared: false, persistence: "committed" });
  expect(await repository.load(key("omitted"))).toEqual(omittedRow);
  // An omitted submitted value (no mentions property) matches and clears.
  expect(
    await repository.clearSubmitted(key("omitted"), {
      draftRevisionAtStart: 0,
      text: "x",
      skillNames: [],
      settlementGuard: captureDraftSettlementGuard(omittedRow, { kind: "draft" }, 0),
      stillOwnsEdit: () => true,
    }),
  ).toEqual({ cleared: true, draftUnchanged: true, persistence: "committed" });

  // Durable row with EXPLICIT EMPTY mentions: an omitted submitted value differs.
  const emptyRow = committed(await repository.seedDraft(key("empty"), { text: "x", skillNames: [], mentions: [] }));
  expect(emptyRow).toHaveProperty("mentions");
  expect(
    await repository.clearSubmitted(key("empty"), {
      draftRevisionAtStart: 0,
      text: "x",
      skillNames: [],
      settlementGuard: captureDraftSettlementGuard(emptyRow, { kind: "draft" }, 0),
      stillOwnsEdit: () => true,
    }),
  ).toMatchObject({ cleared: false, persistence: "committed" });
  expect(await repository.load(key("empty"))).toEqual(emptyRow);
  // An explicit empty submitted array matches and clears.
  expect(
    await repository.clearSubmitted(key("empty"), {
      draftRevisionAtStart: 0,
      text: "x",
      skillNames: [],
      mentions: [],
      settlementGuard: captureDraftSettlementGuard(emptyRow, { kind: "draft" }, 0),
      stillOwnsEdit: () => true,
    }),
  ).toEqual({ cleared: true, draftUnchanged: true, persistence: "committed" });
});

test("clear keeps a durable revision fence against stale edits and stale settlements after recreation", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "clear-revision-fence";
  const original = createBrowserDraftRepository({ indexedDB, databaseName });
  closeAfterTest(() => original.close());

  const seeded = committed(await original.seedDraft(key("clear"), { text: "before", skillNames: [] }));
  const submitted = committed(
    await original.commitEdit(key("clear"), seeded.token, { text: "same bytes", skillNames: [] }, () => true),
  );
  const guard = captureDraftSettlementGuard(submitted, { kind: "draft" }, 0);
  const cleared = await original.clearSubmitted(key("clear"), {
    draftRevisionAtStart: 0,
    text: "same bytes",
    skillNames: [],
    settlementGuard: guard,
    stillOwnsEdit: () => true,
  });
  expect(cleared).toEqual({ cleared: true, draftUnchanged: true, persistence: "committed" });

  const restarted = createBrowserDraftRepository({ indexedDB, databaseName });
  closeAfterTest(() => restarted.close());
  const durable = await restarted.load(key("clear"));
  if (!durable) throw new Error("durable row missing");
  expect(durable.text).toBe("");
  // Clear advances the revision by one and preserves the incarnation.
  expect(durable.token.sessionRevision).toBe(submitted.token.sessionRevision + 1);
  expect(durable.token.namespaceIncarnation).toBe(submitted.token.namespaceIncarnation);

  // Recreate identical bytes into newer work, then attempt the stale settlement.
  const recreated = committed(
    await restarted.commitEdit(key("clear"), durable.token, { text: "same bytes", skillNames: [] }, () => true),
  );
  expect(
    await restarted.clearSubmitted(key("clear"), {
      draftRevisionAtStart: 0,
      text: "same bytes",
      skillNames: [],
      settlementGuard: guard,
      stillOwnsEdit: () => true,
    }),
  ).toEqual({ cleared: false, draftUnchanged: false, persistence: "committed" });

  // The pre-recreation token is stale: a stale edit conflicts, the current token commits.
  expect(
    await restarted.commitEdit(key("clear"), submitted.token, { text: "stale", skillNames: [] }, () => true),
  ).toMatchObject({ state: "conflict" });
  const fresh = committed(
    await restarted.commitEdit(key("clear"), recreated.token, { text: "fresh", skillNames: [] }, () => true),
  );
  expect(fresh.text).toBe("fresh");
  expect(fresh.token.sessionRevision).toBe(recreated.token.sessionRevision + 1);
  expect((await restarted.load(key("clear")))?.text).toBe("fresh");
});

test("a namespace advance fences both the row and the expected token, and lazy load preserves state", async () => {
  const indexedDB = new IDBFactory();
  const databaseName = "namespace-fence";
  const repository = createBrowserDraftRepository({ indexedDB, databaseName });
  closeAfterTest(() => repository.close());
  const value = {
    text: "preserve",
    skillNames: ["skill"],
    commandNames: ["cmd"],
    mentions: [{ kind: "skill" as const, name: "skill", offset: 0 }],
  };
  const seeded = committed(await repository.seedDraft(key("fenced"), value));
  expect(seeded).toEqual({ ...value, key: key("fenced"), token: seeded.token });

  // Advance the durable namespace FIRST, then test BOTH fences against the
  // still-OLD stored row before any lazy load.
  await advanceNamespace(indexedDB, databaseName, "fixture-incarnation-2");

  // A token matching the NEW namespace against the still-old row isolates the
  // row-incarnation fence.
  expect(
    await repository.commitEdit(
      key("fenced"),
      { namespaceIncarnation: "fixture-incarnation-2", sessionRevision: seeded.token.sessionRevision },
      { text: "row fence", skillNames: [] },
      () => true,
    ),
  ).toMatchObject({ state: "conflict", current: { text: "preserve" } });
  // The OLD expected token also conflicts before any load. The row is still old
  // here too, so this refusal alone does NOT isolate the expected-token fence;
  // the independent expected-token-only case is checked after the lazy load.
  expect(
    await repository.commitEdit(key("fenced"), seeded.token, { text: "expected fence", skillNames: [] }, () => true),
  ).toMatchObject({ state: "conflict", current: { text: "preserve" } });

  // Neither refusal wrote or erased the durable value or metadata (raw read,
  // no lazy refresh).
  const rawRows = (await readStoreRows(indexedDB, databaseName, COMPOSER_DRAFTS_STORE)) as Array<{
    text: string;
    skillNames: string[];
    commandNames?: string[];
    mentions?: unknown[];
    token: { namespaceIncarnation: string; sessionRevision: number };
  }>;
  expect(rawRows).toHaveLength(1);
  expect(rawRows[0]).toMatchObject({ ...value, token: seeded.token });

  // Lazy load preserves the full value and session revision, adopting the new incarnation.
  const refreshed = await repository.load(key("fenced"));
  if (!refreshed) throw new Error("refreshed row missing");
  expect(refreshed).toEqual({
    ...value,
    key: key("fenced"),
    token: { namespaceIncarnation: "fixture-incarnation-2", sessionRevision: seeded.token.sessionRevision },
  });

  // With the durable row now current, an OLD-incarnation expected token against
  // that current row, same session revision and live authority, is refused by
  // the expected-token fence ALONE. A repository that dropped only this check
  // while keeping the row check would commit, so this independently isolates it.
  expect(
    await repository.commitEdit(
      key("fenced"),
      seeded.token,
      { text: "expected-only fence", skillNames: [] },
      () => true,
    ),
  ).toMatchObject({ state: "conflict", current: { text: "preserve" } });
  // The refusal preserved the complete durable row (raw read, no lazy refresh).
  const rawAfterExpectedFence = (await readStoreRows(indexedDB, databaseName, COMPOSER_DRAFTS_STORE)) as Array<{
    text: string;
    skillNames: string[];
    commandNames?: string[];
    mentions?: unknown[];
    token: { namespaceIncarnation: string; sessionRevision: number };
  }>;
  expect(rawAfterExpectedFence).toHaveLength(1);
  expect(rawAfterExpectedFence[0]).toMatchObject({ ...value, token: refreshed.token });
  expect(await repository.load(key("fenced"))).toEqual(refreshed);

  // A captured old settlement is stale after the refresh.
  expect(
    await repository.clearSubmitted(key("fenced"), {
      draftRevisionAtStart: 0,
      text: "preserve",
      skillNames: ["skill"],
      commandNames: ["cmd"],
      mentions: [{ kind: "skill", name: "skill", offset: 0 }],
      settlementGuard: captureDraftSettlementGuard(seeded, { kind: "draft" }, 0),
      stillOwnsEdit: () => true,
    }),
  ).toEqual({ cleared: false, draftUnchanged: false, persistence: "committed" });

  // The refreshed token remains useful.
  const next = committed(
    await repository.commitEdit(key("fenced"), refreshed.token, { text: "after refresh", skillNames: [] }, () => true),
  );
  expect(next.token.sessionRevision).toBe(seeded.token.sessionRevision + 1);
});

test("commit faults are unavailable, and a current-token edit requires a live local authority", async () => {
  const indexedDB = new IDBFactory();
  let failOperation = "";
  const repository = createBrowserDraftRepository({
    indexedDB,
    databaseName: "fault-boundaries",
    beforeCommit: (operation) => {
      if (operation === failOperation) throw new Error(`injected ${operation}`);
    },
  });
  closeAfterTest(() => repository.close());
  const seeded = committed(await repository.seedDraft(key("fault"), { text: "before", skillNames: [] }));
  const notifications: BrowserDraftKey[] = [];
  const unsubscribe = repository.subscribe((changed) => notifications.push(changed));

  failOperation = "commitEdit";
  expect(
    await repository.commitEdit(key("fault"), seeded.token, { text: "after", skillNames: [] }, () => true),
  ).toEqual({ state: "unavailable" });
  expect(await repository.load(key("fault"))).toMatchObject({ text: "before", token: seeded.token });

  failOperation = "";
  // A matching current token with a false authority is unavailable, writes
  // nothing and notifies nothing.
  expect(
    await repository.commitEdit(key("fault"), seeded.token, { text: "blocked", skillNames: [] }, () => false),
  ).toEqual({ state: "unavailable" });
  expect(await repository.load(key("fault"))).toMatchObject({ text: "before", token: seeded.token });
  expect(notifications).toEqual([]);

  // The same token with true authority commits a real write.
  const authorized = committed(
    await repository.commitEdit(key("fault"), seeded.token, { text: "after", skillNames: [] }, () => true),
  );
  expect(authorized.text).toBe("after");
  expect(notifications).toEqual([key("fault")]);

  failOperation = "clearSubmitted";
  expect(
    await repository.clearSubmitted(key("fault"), {
      draftRevisionAtStart: 0,
      text: "after",
      skillNames: [],
      settlementGuard: captureDraftSettlementGuard(authorized, { kind: "draft" }, 0),
      stillOwnsEdit: () => true,
    }),
  ).toEqual({ cleared: false, draftUnchanged: true, persistence: "pending" });
  expect(await repository.load(key("fault"))).toMatchObject({ text: "after" });
  unsubscribe();
});

test("cleanup re-inspects changed bytes, records the new marker, removes matching source bytes and never archives", async () => {
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
  // Changed bytes are kept, never deleted, and the durable row is not overwritten.
  expect(storage.getItem(draftStorageKey("cleanup-race"))).toBe("new work");
  const firstRow = await repository.load(key("cleanup-race"));
  if (!firstRow) throw new Error("first row missing");

  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  const durable = await repository.load(key("cleanup-race"));
  if (!durable) throw new Error("durable row missing");
  expect(durable).toMatchObject({ text: "inspected", token: firstRow.token });
  // The second scan recorded the changed bytes and removed the matching source bytes.
  const markers = (await readStoreRows(indexedDB, "cleanup-race", DRAFT_TRANSFERS_STORE)) as Array<{
    legacyKey: string;
    inspectedBytes: string | null;
  }>;
  expect(markers.some((marker) => marker.inspectedBytes === "new work")).toBe(true);
  expect(storage.getItem(draftStorageKey("cleanup-race"))).toBeNull();
  // No losing-draft archive rows are written for the skipped durable row.
  expect(await readStoreRows(indexedDB, "cleanup-race", DRAFT_CONFLICTS_STORE)).toEqual([]);
});

test("listener and diagnostic failures cannot change seed, edit or clear results or stop later listeners", async () => {
  type ListenerDiagnostic = { path: "listener-failed"; key: BrowserDraftKey; error: unknown };
  const listenerFailure = new Error("injected listener failure");
  const diagnostics: ListenerDiagnostic[] = [];
  const laterNotifications: BrowserDraftKey[] = [];
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "listener-isolation",
    onListenerDiagnostic: (diagnostic: ListenerDiagnostic) => {
      diagnostics.push(diagnostic);
      throw new Error("diagnostic observer failure");
    },
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
  for (const unsubscribe of unsubscribers) unsubscribe();
  const durableEdit = await repository.load(key("listener"));
  if (!durableEdit) throw new Error("durable edit missing");

  unsubscribers = subscribeFaultThenListener();
  const cleared = await repository.clearSubmitted(key("listener"), {
    draftRevisionAtStart: 0,
    text: "edited",
    skillNames: [],
    settlementGuard: captureDraftSettlementGuard(durableEdit, { kind: "draft" }, 0),
    stillOwnsEdit: () => true,
  });
  for (const unsubscribe of unsubscribers) unsubscribe();
  const durableClear = await repository.load(key("listener"));

  expect(seeded).toMatchObject({ state: "committed", draft: { text: "seeded" } });
  if (seeded.state === "committed") expect(durableSeed).toEqual(seeded.draft);
  expect(edited).toMatchObject({ state: "committed", draft: { text: "edited" } });
  if (edited.state === "committed") expect(durableEdit).toEqual(edited.draft);
  expect(cleared).toEqual({ cleared: true, draftUnchanged: true, persistence: "committed" });
  expect(durableClear?.text).toBe("");
  expect(laterNotifications).toEqual([key("listener"), key("listener"), key("listener")]);
  expect(diagnostics).toHaveLength(3);
  for (const diagnostic of diagnostics) {
    expect(diagnostic).toMatchObject({ path: "listener-failed", key: key("listener") });
    expect(diagnostic.error).toBe(listenerFailure);
  }
});

test("a competing upgrade succeeds only after versionchange retires the open connection", async () => {
  const indexedDB = new IDBFactory();
  const paths: string[] = [];
  const repository = createBrowserDraftRepository({
    indexedDB,
    databaseName: "open-diagnostic",
    onOpenDiagnostic: (diagnostic) => paths.push(diagnostic.path),
  });
  closeAfterTest(() => repository.close());
  await repository.load(key("diagnostic"));

  // The competing upgrade cannot succeed until the repository's connection is
  // actually retired, so awaiting its real success proves retirement.
  await new Promise<void>((resolve, reject) => {
    const upgrade = indexedDB.open("open-diagnostic", LOCAL_COMPOSITION_DATABASE_VERSION + 1);
    upgrade.addEventListener(
      "success",
      () => {
        upgrade.result.close();
        resolve();
      },
      { once: true },
    );
    upgrade.addEventListener("error", () => reject(upgrade.error ?? new Error("upgrade failed")), { once: true });
  });
  expect(paths).toContain("versionchange-retire");
});

test("close forgets the connection, a reused adapter reopens and a fresh adapter sees the same row", async () => {
  let opens = 0;
  const counting = new Proxy(new IDBFactory(), {
    get(target, prop) {
      if (prop === "open") {
        return (name: string, version?: number) => {
          opens += 1;
          return target.open(name, version);
        };
      }
      const value = Reflect.get(target, prop, target) as unknown;
      return typeof value === "function" ? (value as (...args: unknown[]) => unknown).bind(target) : value;
    },
  }) as IDBFactory;
  const databaseName = "close-reopen";

  const adapter = createBrowserDraftRepository({ indexedDB: counting, databaseName });
  closeAfterTest(() => adapter.close());
  const seeded = committed(await adapter.seedDraft(key("reopen"), { text: "durable", skillNames: [] }));
  const opensAfterSeed = opens;
  expect(opensAfterSeed).toBeGreaterThan(0);

  adapter.close();
  const reused = await adapter.load(key("reopen"));
  expect(opens).toBe(opensAfterSeed + 1);
  expect(reused).toEqual(seeded);

  const fresh = createBrowserDraftRepository({ indexedDB: counting, databaseName });
  closeAfterTest(() => fresh.close());
  const loaded = await fresh.load(key("reopen"));
  expect(loaded).toEqual(seeded);
});

test("the inactive runtime rescans on its declared lifecycle triggers, retains failed input and recovers", async () => {
  const indexedDB = new IDBFactory();
  const storage = new MemoryStorage();
  storage.setItem(draftStorageKey("runtime"), "initial");
  let injectFailure = false;
  const repository = createBrowserDraftRepository({
    indexedDB,
    databaseName: "transfer-runtime",
    storage,
    beforeCommit: (operation) => {
      if (operation !== "transferSupportedDrafts") return;
      if (injectFailure) {
        injectFailure = false;
        throw new Error("injected transfer fault");
      }
    },
  });
  closeAfterTest(() => repository.close());
  const observer = observeTransfers(repository);
  closeAfterTest(() => observer.drain(), "drain");
  const runtime = createBrowserDraftTransferRuntime({
    repository: observer.repository,
    namespace,
    storage,
    window,
    document,
  });
  closeAfterTest(() => runtime.dispose(), "dispose");

  // Initial scan.
  expect(await observer.settle(0)).toEqual({ state: "committed" });
  const imported = await repository.load(key("runtime"));
  if (!imported) throw new Error("initial runtime import missing");
  const authoritative = committed(
    await repository.commitEdit(key("runtime"), imported.token, { text: "authoritative", skillNames: [] }, () => true),
  );

  // Composer-open trigger with fresh supported input.
  storage.setItem(draftStorageKey("runtime"), "from composer-open");
  runtime.composerOpened(key("runtime"));
  expect(await observer.settle(1)).toEqual({ state: "committed" });

  // Visibility trigger.
  storage.setItem(draftStorageKey("runtime"), "from visibility");
  overrideVisibility("visible");
  document.dispatchEvent(new Event("visibilitychange"));
  expect(await observer.settle(2)).toEqual({ state: "committed" });

  // Storage trigger.
  storage.setItem(draftStorageKey("runtime"), "from storage event");
  window.dispatchEvent(new StorageEvent("storage", { key: draftStorageKey("runtime") }));
  expect(await observer.settle(3)).toEqual({ state: "committed" });

  // Injected failure: the attempt settles pending and its bytes are retained.
  storage.setItem(draftStorageKey("runtime"), "after fault");
  injectFailure = true;
  window.dispatchEvent(new StorageEvent("storage", { key: composerDraftStorageKey("runtime") }));
  expect(await observer.settle(4)).toEqual({ state: "pending" });
  expect(storage.getItem(draftStorageKey("runtime"))).toBe("after fault");

  // The next declared trigger commits the retained inspection outcome.
  runtime.composerOpened(key("runtime"));
  expect(await observer.settle(5)).toEqual({ state: "committed" });
  expect(storage.getItem(draftStorageKey("runtime"))).toBeNull();

  expect(observer.attempts()).toBe(6);
  expect(await repository.load(key("runtime"))).toMatchObject({ text: "authoritative", token: authoritative.token });

  // A disposed runtime ignores later triggers.
  runtime.dispose();
  const attemptsAtDispose = observer.attempts();
  storage.setItem(draftStorageKey("runtime"), "after dispose");
  window.dispatchEvent(new StorageEvent("storage", { key: draftStorageStorageKeyForTest("runtime") }));
  await flushMicrotasks();
  expect(observer.attempts()).toBe(attemptsAtDispose);
});

test("the inactive runtime coalesces triggers during an attempt to one completed follow-up without a retry loop", async () => {
  const storage = new MemoryStorage();
  storage.setItem(draftStorageKey("coalesce"), "initial");
  let firstAttempt = true;
  let runtime: ReturnType<typeof createBrowserDraftTransferRuntime> | undefined;
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "runtime-coalesce",
    storage,
    beforeCommit: (operation) => {
      if (operation !== "transferSupportedDrafts") return;
      if (firstAttempt) {
        firstAttempt = false;
        storage.setItem(draftStorageKey("coalesce"), "changed during attempt");
        runtime?.composerOpened(key("coalesce"));
        runtime?.composerOpened(key("coalesce"));
        window.dispatchEvent(new StorageEvent("storage", { key: draftStorageKey("coalesce") }));
      }
    },
  });
  closeAfterTest(() => repository.close());
  const observer = observeTransfers(repository);
  closeAfterTest(() => observer.drain(), "drain");
  runtime = createBrowserDraftTransferRuntime({
    repository: observer.repository,
    namespace,
    storage,
    window,
    document,
  });
  closeAfterTest(() => runtime?.dispose(), "dispose");

  expect(await observer.settle(0)).toEqual({ state: "committed" });
  expect(await observer.settle(1)).toEqual({ state: "committed" });
  await flushMicrotasks();
  // No autonomous retry follows the completed follow-up.
  expect(observer.attempts()).toBe(2);
  runtime.dispose();
});

test("async teardown disposes the runtime then drains an in-flight transfer without a new scan", async () => {
  const storage = new MemoryStorage();
  storage.setItem(draftStorageKey("drain"), "initial");
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "runtime-drain",
    storage,
  });
  closeAfterTest(() => repository.close());
  const observer = observeTransfers(repository);
  closeAfterTest(() => observer.drain(), "drain");
  const runtime = createBrowserDraftTransferRuntime({
    repository: observer.repository,
    namespace,
    storage,
    window,
    document,
  });
  closeAfterTest(() => runtime.dispose(), "dispose");

  // Start awaiting the initial scan, then dispose before it settles. The drain
  // finishes the already-observed transfer promise; disposal starts no new scan.
  const inFlight = observer.settle(0);
  runtime.dispose();
  await observer.drain();
  expect(await inFlight).toEqual({ state: "committed" });
  const attemptsAtDispose = observer.attempts();
  storage.setItem(draftStorageKey("drain"), "after dispose");
  window.dispatchEvent(new StorageEvent("storage", { key: draftStorageKey("drain") }));
  await flushMicrotasks();
  expect(observer.attempts()).toBe(attemptsAtDispose);
});

// Keep the runtime test's post-dispose key literal independent of the production key helper it is checking.
function draftStorageStorageKeyForTest(ref: string): string {
  return `evener.composer.draft.v1.${ref}`;
}

test("transfer read faults stay pending and emit no console error", async () => {
  class FailingStorage extends MemoryStorage {
    override getItem(_key: string): string | null {
      throw new Error("read unavailable");
    }
  }
  const storage = new FailingStorage();
  storage.setItem(draftStorageKey("fault"), "unreadable");
  const errorSpy = vi.spyOn(console, "error").mockImplementation(() => undefined);
  const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => undefined);
  const repository = createBrowserDraftRepository({
    indexedDB: new IDBFactory(),
    databaseName: "read-fault",
    storage,
  });
  closeAfterTest(() => repository.close());
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "pending" });
  expect(errorSpy).not.toHaveBeenCalled();
  expect(warnSpy).not.toHaveBeenCalled();
});

// Dependency fault injection over the REAL injected IDBFactory, through public
// IndexedDB APIs only. Every normal production request is forwarded unchanged.
// State, the first-row entry snapshot, the observed successes and the single
// injection are tracked PER ACTUAL TRANSACTION across repeated objectStore
// retrievals. Once a real earlier draft write AND a real earlier transfer-marker
// write have genuinely succeeded IN THAT TRANSACTION, the second of those
// success callbacks enqueues one extra real duplicate-key add of the captured
// first draft row. The store's own primary key raises a genuine asynchronous
// ConstraintError, so the transaction aborts for real. No request, event,
// rejection or transaction is synthesized and no explicit abort is used. The
// injector does not depend on add vs put, a particular store wrapper, a
// particular draft/marker write order, or any later production write: a
// producer that enqueues all its writes before any success callback is handled
// the same as one that awaits each request before issuing the next.
interface TransactionWriteState {
  draftStore?: IDBObjectStore;
  firstRow?: unknown;
  captured: boolean;
  draftSucceeded: boolean;
  markerSucceeded: boolean;
  injected: boolean;
}
function faultAfterEarlierDraftAndMarker(real: IDBFactory): {
  factory: IDBFactory;
  draftSucceeded: () => boolean;
  markerSucceeded: () => boolean;
  requestErrorName: () => string | undefined;
  events: () => string[];
} {
  let sawDraftSuccess = false;
  let sawMarkerSuccess = false;
  let injectedError: DOMException | undefined;
  const events: string[] = [];
  const databases = new WeakMap<object, IDBDatabase>();
  const transactionStates = new WeakMap<object, TransactionWriteState>();
  // A plain snapshot at write entry, so a producer that mutates its own object
  // after issuing the real request cannot change what is re-added.
  const snapshotRow = (value: unknown): unknown => JSON.parse(JSON.stringify(value)) as unknown;

  const stateFor = (transaction: object): TransactionWriteState => {
    let state = transactionStates.get(transaction);
    if (!state) {
      state = { captured: false, draftSucceeded: false, markerSucceeded: false, injected: false };
      transactionStates.set(transaction, state);
    }
    return state;
  };
  // Enqueue the real duplicate-key add only once both earlier successes have
  // genuinely happened IN THIS transaction, so no later production write is
  // required. This is a PURE extra request the code under test is not awaiting;
  // its own error listener observes the ConstraintError. The production
  // transaction-completion promise is created before the body is awaited and its
  // handler is attached only after the awaited request rejects, so this seam can
  // expose a genuine production unhandled-rejection window (recorded as a RED for
  // the next source phase, not hidden here).
  const injectOnceBothSucceeded = (state: TransactionWriteState): void => {
    if (!state.draftSucceeded || !state.markerSucceeded || state.injected) return;
    if (!state.captured || !state.draftStore) return;
    state.injected = true;
    const duplicate = state.draftStore.add(snapshotRow(state.firstRow) as never);
    duplicate.addEventListener("success", () => {
      events.push("unexpected-injected-success");
    });
    duplicate.addEventListener("error", () => {
      injectedError = duplicate.error ?? undefined;
      events.push("error");
    });
  };
  const wrapWriteStore = (transaction: object, store: IDBObjectStore, kind: "draft" | "marker"): IDBObjectStore =>
    new Proxy(store, {
      get(target, prop) {
        if (prop === "add" || prop === "put") {
          return (value: unknown, explicitKey?: IDBValidKey) => {
            const state = stateFor(transaction);
            if (kind === "draft") {
              if (!state.captured) {
                state.firstRow = snapshotRow(value);
                state.captured = true;
              }
              state.draftStore = target;
            }
            const request =
              prop === "add"
                ? target.add(value as never, explicitKey)
                : target.put(value as never, explicitKey as never);
            request.addEventListener("success", () => {
              if (kind === "draft") {
                state.draftSucceeded = true;
                sawDraftSuccess = true;
                events.push("draft");
              } else {
                state.markerSucceeded = true;
                sawMarkerSuccess = true;
                events.push("marker");
              }
              injectOnceBothSucceeded(state);
            });
            return request;
          };
        }
        const value = Reflect.get(target, prop, target) as unknown;
        return typeof value === "function" ? (value as (...args: unknown[]) => unknown).bind(target) : value;
      },
    }) as IDBObjectStore;

  const wrapTransaction = (transaction: IDBTransaction): IDBTransaction =>
    new Proxy(transaction, {
      get(target, prop) {
        if (prop === "objectStore") {
          return (name: string) => {
            const store = target.objectStore(name);
            if (name === COMPOSER_DRAFTS_STORE) return wrapWriteStore(target, store, "draft");
            if (name === DRAFT_TRANSFERS_STORE) return wrapWriteStore(target, store, "marker");
            return store;
          };
        }
        const value = Reflect.get(target, prop, target) as unknown;
        return typeof value === "function" ? (value as (...args: unknown[]) => unknown).bind(target) : value;
      },
    }) as IDBTransaction;

  const wrapDatabase = (database: IDBDatabase): IDBDatabase => {
    const existing = databases.get(database);
    if (existing) return existing;
    const proxy = new Proxy(database, {
      get(target, prop) {
        if (prop === "transaction") {
          return (storeNames: string | string[], mode?: IDBTransactionMode) =>
            wrapTransaction(target.transaction(storeNames, mode));
        }
        const value = Reflect.get(target, prop, target) as unknown;
        return typeof value === "function" ? (value as (...args: unknown[]) => unknown).bind(target) : value;
      },
    }) as IDBDatabase;
    databases.set(database, proxy);
    return proxy;
  };

  const factory = new Proxy(real, {
    get(target, prop) {
      if (prop === "open") {
        return (name: string, version?: number) => {
          const request = target.open(name, version);
          return new Proxy(request, {
            get(requestTarget, requestProp) {
              if (requestProp === "result") return wrapDatabase(requestTarget.result);
              const value = Reflect.get(requestTarget, requestProp, requestTarget) as unknown;
              return typeof value === "function"
                ? (value as (...args: unknown[]) => unknown).bind(requestTarget)
                : value;
            },
          }) as IDBOpenDBRequest;
        };
      }
      const value = Reflect.get(target, prop, target) as unknown;
      return typeof value === "function" ? (value as (...args: unknown[]) => unknown).bind(target) : value;
    },
  }) as IDBFactory;

  return {
    factory,
    draftSucceeded: () => sawDraftSuccess,
    markerSucceeded: () => sawMarkerSuccess,
    requestErrorName: () => injectedError?.name,
    events: () => [...events],
  };
}

test("a real duplicate-key request failure rolls back namespace, drafts and markers, and a reopened retry commits", async () => {
  const real = new IDBFactory();
  const storage = new MemoryStorage();
  storage.setItem(draftStorageKey("first"), "first legacy");
  storage.setItem(draftStorageKey("second"), "second legacy");
  const fault = faultAfterEarlierDraftAndMarker(real);
  const repository = createBrowserDraftRepository({
    indexedDB: fault.factory,
    databaseName: "request-fault",
    storage,
  });
  closeAfterTest(() => repository.close());

  // A real earlier draft write and a real earlier marker write both succeeded in
  // the same transaction; only then does the second success callback enqueue the
  // extra real duplicate-key add, which fails with a real asynchronous
  // ConstraintError, not a method throw or an explicit abort.
  expect(await repository.transferSupportedDrafts(namespace)).toEqual({ state: "pending" });
  expect(fault.draftSucceeded()).toBe(true);
  expect(fault.markerSucceeded()).toBe(true);
  const events = fault.events();
  const errorIndex = events.indexOf("error");
  expect(events.filter((event) => event === "error")).toHaveLength(1);
  expect(events.slice(0, errorIndex)).toContain("draft");
  expect(events.slice(0, errorIndex)).toContain("marker");
  expect(fault.requestErrorName()).toBe("ConstraintError");
  expect(fault.requestErrorName()).not.toBe("AbortError");

  // The aborted transaction left no namespace, draft or marker rows.
  expect(await readStoreRows(real, "request-fault", ANNOTATION_NAMESPACES_STORE)).toEqual([]);
  expect(await readStoreRows(real, "request-fault", COMPOSER_DRAFTS_STORE)).toEqual([]);
  expect(await readStoreRows(real, "request-fault", DRAFT_TRANSFERS_STORE)).toEqual([]);
  // The supported source bytes are untouched.
  expect(storage.getItem(draftStorageKey("first"))).toBe("first legacy");
  expect(storage.getItem(draftStorageKey("second"))).toBe("second legacy");

  // Close the failed adapter, then retry with a fresh unwrapped factory/adapter.
  repository.close();
  const retry = createBrowserDraftRepository({ indexedDB: real, databaseName: "request-fault", storage });
  closeAfterTest(() => retry.close());
  expect(await retry.transferSupportedDrafts(namespace)).toEqual({ state: "committed" });
  // Complete fresh durable values/metadata, not a partial text/skills match.
  expect(await retry.load(key("first"))).toEqual({
    text: "first legacy",
    skillNames: [],
    key: key("first"),
    token: { namespaceIncarnation: expect.any(String), sessionRevision: expect.any(Number) },
  });
  expect(await retry.load(key("second"))).toEqual({
    text: "second legacy",
    skillNames: [],
    key: key("second"),
    token: { namespaceIncarnation: expect.any(String), sessionRevision: expect.any(Number) },
  });
});

// Compile-time contract: the durable session token carries exactly incarnation
// and revision, and the factory returns the owner-free repository interface.
const completeToken: DraftSessionToken = { namespaceIncarnation: "incarnation", sessionRevision: 2 };
void completeToken;

void (0 as unknown as DraftBackingIdentity);
expectTypeOf(createBrowserDraftRepository).returns.toEqualTypeOf<BrowserDraftRepository>();

// The reset seam must hand out a repository for the NEW storage universe: a
// stale captured factory would otherwise leak one test's durable row into the
// next test that replaces globalThis.indexedDB.
test("the test singleton reset drops the previous storage universe", async () => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  vi.stubGlobal("localStorage", new MemoryStorage());
  const first = getBrowserDraftRepository();
  const resetKey = browserDraftKey("reset-seam", "https://hub.test");
  await first.seedDraft(resetKey, { text: "first universe", skillNames: [] });
  // Replace the universe and reset deliberately.
  vi.stubGlobal("indexedDB", new IDBFactory());
  resetBrowserDraftRepositoryForTests();
  const second = getBrowserDraftRepository();
  expect(second).not.toBe(first);
  expect(await second.load(resetKey)).toBeNull();
  resetBrowserDraftRepositoryForTests();
  vi.unstubAllGlobals();
});
