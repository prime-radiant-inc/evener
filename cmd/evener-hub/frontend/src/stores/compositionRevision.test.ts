import { IDBFactory } from "fake-indexeddb";
import { expect, test } from "vitest";
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
import {
  ANNOTATION_NAMESPACES_STORE,
  COMPOSER_DRAFTS_STORE,
  LOCAL_COMPOSITION_DATABASE_VERSION,
  registerLocalCompositionSchema,
} from "./localCompositionSchema";

const namespace = { kind: "browser" as const, origin: "https://hub.test" };
const key = (sessionRef: string) => ({ namespace, sessionRef });

function openStore(indexedDB: IDBFactory, databaseName: string): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(databaseName, LOCAL_COMPOSITION_DATABASE_VERSION);
    request.addEventListener("upgradeneeded", () => registerLocalCompositionSchema(request.result));
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error ?? new Error("open failed")), { once: true });
  });
}

function transactionCompletion(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.addEventListener("complete", () => resolve(), { once: true });
    transaction.addEventListener("abort", () => reject(transaction.error ?? new Error("transaction aborted")), {
      once: true,
    });
  });
}

function writeTransaction(database: IDBDatabase): IDBTransaction {
  return database.transaction([COMPOSER_DRAFTS_STORE, ANNOTATION_NAMESPACES_STORE], "readwrite");
}

function allRows(database: IDBDatabase, store: string): Promise<unknown[]> {
  const transaction = database.transaction(store, "readonly");
  return new Promise((resolve, reject) => {
    const request = transaction.objectStore(store).getAll();
    request.addEventListener("success", () => resolve(request.result as unknown[]), { once: true });
    request.addEventListener("error", () => reject(request.error ?? new Error("read failed")), { once: true });
  });
}

test("serializes the namespace and session keys as the durable JSON tuples", () => {
  expect(serializeNamespace(namespace)).toBe(JSON.stringify(["browser", "https://hub.test"]));
  expect(serializeSessionKey(key("root"))).toBe(JSON.stringify(["browser", "https://hub.test", "root"]));
});

test("sameSessionToken compares both the incarnation and the revision", () => {
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

test("read helpers only read: an absent row stays absent and mints no namespace", async () => {
  const indexedDB = new IDBFactory();
  const database = await openStore(indexedDB, "helper-read");
  const transaction = writeTransaction(database);
  expect(await readSessionRow(transaction, key("root"))).toBeUndefined();
  expect(await readNamespaceRow(transaction, namespace)).toBeUndefined();
  await transactionCompletion(transaction);
  expect(await allRows(database, COMPOSER_DRAFTS_STORE)).toEqual([]);
  expect(await allRows(database, ANNOTATION_NAMESPACES_STORE)).toEqual([]);
  database.close();
});

test("ensureSessionRow initializes absent metadata at revision 1 and returns an existing row unchanged", async () => {
  const indexedDB = new IDBFactory();
  const database = await openStore(indexedDB, "helper-ensure");
  const created = writeTransaction(database);
  const row = await ensureSessionRow(created, key("root"));
  await transactionCompletion(created);
  expect(row).toMatchObject({
    serializedKey: serializeSessionKey(key("root")),
    key: key("root"),
    text: "",
    skillNames: [],
    token: { sessionRevision: 1 },
  });
  expect(row.token.namespaceIncarnation).not.toBe("");

  const again = writeTransaction(database);
  const existing = await ensureSessionRow(again, key("root"));
  await transactionCompletion(again);
  expect(existing).toEqual(row);
  database.close();
});

test("nextSessionRow is pure, preserves an omitted value and always advances by exactly one", () => {
  const row: SessionRow = {
    serializedKey: '["browser","https://hub.test","root"]',
    key: key("root"),
    text: "old",
    skillNames: ["a"],
    commandNames: ["c"],
    mentions: [{ kind: "skill", name: "a", offset: 0 }],
    token: { namespaceIncarnation: "inc", sessionRevision: 4 },
  };
  const preserved = nextSessionRow(row);
  expect(preserved).toEqual({ ...row, token: { namespaceIncarnation: "inc", sessionRevision: 5 } });
  expect(preserved).not.toBe(row);
  expect(preserved.skillNames).not.toBe(row.skillNames);
  expect(preserved.commandNames).not.toBe(row.commandNames);
  expect(preserved.mentions).not.toBe(row.mentions);
  expect(preserved.mentions?.[0]).not.toBe(row.mentions?.[0]);
  // The caller's row is never mutated.
  expect(row.token.sessionRevision).toBe(4);
});

test("nextSessionRow replaces the ordinary value only when supplied, keeping explicit absent-versus-empty metadata", () => {
  const row: SessionRow = {
    serializedKey: "k",
    key: key("root"),
    text: "old",
    skillNames: ["a"],
    commandNames: ["c"],
    mentions: [{ kind: "command", name: "c", offset: 1 }],
    token: { namespaceIncarnation: "inc", sessionRevision: 1 },
  };
  const replaced = nextSessionRow(row, { text: "new", skillNames: [], commandNames: ["plan"], mentions: [] });
  expect(replaced).toMatchObject({
    text: "new",
    skillNames: [],
    commandNames: ["plan"],
    mentions: [],
    token: { namespaceIncarnation: "inc", sessionRevision: 2 },
  });
  const omitted = nextSessionRow(row, { text: "new", skillNames: [] });
  expect(omitted).not.toHaveProperty("commandNames");
  expect(omitted).not.toHaveProperty("mentions");
});

test("putSessionRow issues only a put and never reads", () => {
  const puts: Array<{ store: string; row: unknown }> = [];
  const transaction = {
    objectStore: (store: string) => ({
      put: (row: unknown) => {
        puts.push({ store, row });
        return {} as IDBRequest<IDBValidKey>;
      },
      get: () => {
        throw new Error("putSessionRow must not read");
      },
    }),
  } as unknown as IDBTransaction;
  const row: SessionRow = {
    serializedKey: "k",
    key: key("root"),
    text: "t",
    skillNames: ["a"],
    token: { namespaceIncarnation: "inc", sessionRevision: 3 },
  };
  putSessionRow(transaction, row);
  expect(puts).toEqual([{ store: COMPOSER_DRAFTS_STORE, row }]);
});
