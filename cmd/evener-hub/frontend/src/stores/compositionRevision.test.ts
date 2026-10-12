import { IDBFactory } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import type { BrowserDraftKey } from "./browserDraftRepository";
import {
  ensureSessionRow,
  nextSessionRow,
  putSessionRow,
  readNamespaceRow,
  readSessionRow,
  type SessionRow,
  sameSessionToken,
  serializeNamespace,
  serializeSessionKey,
} from "./compositionRevision";
import { requestResult, transactionCompletion } from "./idbConnection";
import {
  ANNOTATION_NAMESPACES_STORE,
  COMPOSER_DRAFTS_STORE,
  LOCAL_COMPOSITION_DATABASE_VERSION,
  registerLocalCompositionSchema,
} from "./localCompositionSchema";

const namespace = { kind: "browser" as const, origin: "https://hub.test" };
const key = (sessionRef: string): BrowserDraftKey => ({ namespace, sessionRef });

function openDatabase(indexedDB: IDBFactory, databaseName: string): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(databaseName, LOCAL_COMPOSITION_DATABASE_VERSION);
    request.addEventListener("upgradeneeded", () => registerLocalCompositionSchema(request.result));
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error ?? new Error("open failed")), { once: true });
  });
}

function sessionRow(sessionRef: string, overrides: Partial<SessionRow> = {}): SessionRow {
  return {
    serializedKey: serializeSessionKey(key(sessionRef)),
    key: key(sessionRef),
    text: "/same",
    skillNames: ["same"],
    token: { namespaceIncarnation: "inc-1", sessionRevision: 4 },
    ...overrides,
  };
}

afterEach(() => {
  vi.restoreAllMocks();
});

test("serializes namespaces and sessions as the approved JSON tuples", () => {
  expect(serializeNamespace(namespace)).toBe('["browser","https://hub.test"]');
  expect(serializeSessionKey(key("root"))).toBe('["browser","https://hub.test","root"]');
});

test("nextSessionRow is pure, always advances revision by one, and replaces the ordinary value only when supplied", () => {
  const row = sessionRow("next", { mentions: [{ kind: "skill", name: "same", offset: 0 }] });
  const snapshot = JSON.parse(JSON.stringify(row));

  const preserved = nextSessionRow(row);
  expect(preserved).not.toBe(row);
  expect(preserved.text).toBe("/same");
  expect(preserved.skillNames).toEqual(["same"]);
  expect(preserved.mentions).toEqual([{ kind: "skill", name: "same", offset: 0 }]);
  expect(preserved.token).toEqual({ namespaceIncarnation: "inc-1", sessionRevision: 5 });
  expect(row).toEqual(snapshot); // source row untouched

  const replaced = nextSessionRow(row, { text: "next", skillNames: ["other"], commandNames: ["plan"] });
  expect(replaced.text).toBe("next");
  expect(replaced.skillNames).toEqual(["other"]);
  expect(replaced.commandNames).toEqual(["plan"]);
  expect(replaced).not.toHaveProperty("mentions"); // an absent ordinary field stays absent
  expect(replaced.token.sessionRevision).toBe(5);

  const explicitEmpty = nextSessionRow(replaced, { text: "next", skillNames: ["other"], mentions: [] });
  expect(explicitEmpty.mentions).toEqual([]);
  expect(explicitEmpty.commandNames).toBeUndefined();
});

test("sameSessionToken compares both incarnation and revision", () => {
  expect(
    sameSessionToken(
      { namespaceIncarnation: "a", sessionRevision: 1 },
      { namespaceIncarnation: "a", sessionRevision: 1 },
    ),
  ).toBe(true);
  expect(
    sameSessionToken(
      { namespaceIncarnation: "a", sessionRevision: 1 },
      { namespaceIncarnation: "b", sessionRevision: 1 },
    ),
  ).toBe(false);
  expect(
    sameSessionToken(
      { namespaceIncarnation: "a", sessionRevision: 1 },
      { namespaceIncarnation: "a", sessionRevision: 2 },
    ),
  ).toBe(false);
});

test("readSessionRow and readNamespaceRow read only, returning undefined for absent rows", async () => {
  const indexedDB = new IDBFactory();
  const database = await openDatabase(indexedDB, "helpers-read-absent");
  const read = database.transaction([COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE], "readonly");
  expect(await readSessionRow(read, key("root"))).toBeUndefined();
  expect(await readNamespaceRow(read, namespace)).toBeUndefined();
  await transactionCompletion(read);
  database.close();
});

test("ensureSessionRow initializes an absent row at revision 1, durably, and returns an existing row unchanged", async () => {
  const indexedDB = new IDBFactory();
  const database = await openDatabase(indexedDB, "helpers-ensure");
  const write = database.transaction([COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE], "readwrite");
  const created = await ensureSessionRow(write, key("root"));
  await transactionCompletion(write);
  expect(created.text).toBe("");
  expect(created.skillNames).toEqual([]);
  expect(created.key).toEqual(key("root"));
  expect(created.token.sessionRevision).toBe(1);
  expect(created.token.namespaceIncarnation).not.toBe("");
  database.close();

  const reopened = await openDatabase(indexedDB, "helpers-ensure");
  const readTx = reopened.transaction([COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE], "readwrite");
  const durable = await readSessionRow(readTx, key("root"));
  const namespaceRow = await readNamespaceRow(readTx, namespace);
  expect(durable).toEqual(created);
  expect(namespaceRow?.incarnation).toBe(created.token.namespaceIncarnation);

  const again = await ensureSessionRow(readTx, key("root"));
  await transactionCompletion(readTx);
  expect(again).toEqual(created); // existing row returned untouched, no second write
  reopened.close();
});

test("putSessionRow issues only a durable put: no read on the store, and the row survives completion", async () => {
  const indexedDB = new IDBFactory();
  const database = await openDatabase(indexedDB, "helpers-put");
  const seedTx = database.transaction([COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE], "readwrite");
  const seeded = await ensureSessionRow(seedTx, key("root"));
  await transactionCompletion(seedTx);

  const write = database.transaction(COMPOSER_DRAFTS_STORE, "readwrite");
  const store = write.objectStore(COMPOSER_DRAFTS_STORE);
  const getSpy = vi.spyOn(store, "get");
  const next = nextSessionRow(seeded, { text: "written", skillNames: [] });
  await requestResult(putSessionRow(write, next));
  expect(getSpy).not.toHaveBeenCalled();
  await transactionCompletion(write);
  database.close();

  const reopened = await openDatabase(indexedDB, "helpers-put");
  const read = reopened.transaction(COMPOSER_DRAFTS_STORE, "readonly");
  expect(await readSessionRow(read, key("root"))).toEqual(next);
  await transactionCompletion(read);
  reopened.close();
});

test("putSessionRow's write rolls back when its transaction aborts", async () => {
  const indexedDB = new IDBFactory();
  const database = await openDatabase(indexedDB, "helpers-put-abort");
  const seedTx = database.transaction([COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE], "readwrite");
  const seeded = await ensureSessionRow(seedTx, key("root"));
  await transactionCompletion(seedTx);

  const write = database.transaction(COMPOSER_DRAFTS_STORE, "readwrite");
  await requestResult(putSessionRow(write, nextSessionRow(seeded, { text: "aborted", skillNames: [] })));
  write.abort();
  await transactionCompletion(write).catch(() => undefined);
  database.close();

  const reopened = await openDatabase(indexedDB, "helpers-put-abort");
  const read = reopened.transaction(COMPOSER_DRAFTS_STORE, "readonly");
  expect(await readSessionRow(read, key("root"))).toEqual(seeded);
  await transactionCompletion(read);
  reopened.close();
});
