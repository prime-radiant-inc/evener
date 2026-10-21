// @vitest-environment node

import { IDBFactory } from "fake-indexeddb";
import { beforeEach, describe, expect, test } from "vitest";
import { setMutationClientIdentityForTests } from "./mutationClientIdentity";
import { MutationAdmissionLostError, type MutationIntent } from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";

// The final admission runs inside the enqueue transaction, so these tests use
// the real adapter and read the rows back through fresh connections. No
// in-memory projection participates.

const TARGET = "local:thread-1";

function intent(text: string, method: "turn/start" | "turn/queue" = "turn/start"): MutationIntent {
  return {
    targetRef: TARGET,
    threadId: "thread-1",
    method,
    payload: { ref: TARGET, input: [{ type: "text", text }] },
    attachments: [],
    optimisticDisplay: { method, input: [{ type: "text", text }] },
    composerText: text,
  };
}

function idSequence(prefix = "admission") {
  let next = 0;
  return () => `${prefix}-${++next}`;
}

describe("MutationOutboxIndexedDB enqueue admission", () => {
  let indexedDB: IDBFactory;
  let databaseName: string;

  beforeEach(() => {
    indexedDB = new IDBFactory();
    databaseName = `mutation-outbox-admission-${crypto.randomUUID()}`;
    setMutationClientIdentityForTests(undefined);
  });

  function store(prefix = "admission") {
    return new MutationOutboxIndexedDB({ indexedDB, databaseName, createMutationId: idSequence(prefix) });
  }

  test("rejects a second turn input behind an unresolved own row and adds nothing", async () => {
    const storage = store();
    await storage.enqueueIntent(intent("ordinary follow-up"));
    await expect(
      storage.enqueueIntent(intent("continue"), undefined, { rejectIfPendingTurnInput: true }),
    ).rejects.toBeInstanceOf(MutationAdmissionLostError);
    const rows = await storage.listOutbox(TARGET);
    expect(rows.map((row) => row.composerText)).toEqual(["ordinary follow-up"]);
  });

  test("a canceled own turn input does not reject", async () => {
    const storage = store();
    const canceled = await storage.enqueueIntent(intent("stopped before it sent"));
    await storage.cancelUnattempted(TARGET);
    expect(canceled.state).toBe("submitting");
    await expect(
      storage.enqueueIntent(intent("continue"), undefined, { rejectIfPendingTurnInput: true }),
    ).resolves.toMatchObject({ composerText: "continue" });
    expect(await storage.listOutbox(TARGET)).toHaveLength(2);
  });

  test("another client's unresolved turn input does not reject", async () => {
    setMutationClientIdentityForTests("tab-a");
    const first = store("first");
    await first.enqueueIntent(intent("another tab's follow-up"));

    setMutationClientIdentityForTests("tab-b");
    const second = store("second");
    await expect(
      second.enqueueIntent(intent("continue"), undefined, { rejectIfPendingTurnInput: true }),
    ).resolves.toMatchObject({ composerText: "continue" });
    expect(await second.listOutbox(TARGET)).toHaveLength(2);
  });

  test("a rejecting validate refuses the row and rolls back its sequence allocation", async () => {
    const storage = store();
    await expect(
      storage.enqueueIntent(intent("continue"), undefined, {
        validate: () => {
          throw new MutationAdmissionLostError();
        },
      }),
    ).rejects.toBeInstanceOf(MutationAdmissionLostError);
    expect(await storage.listOutbox(TARGET)).toEqual([]);
    // The refused transaction did not keep its allocated sequence: the first
    // committed row starts at 1.
    const committed = await storage.enqueueIntent(intent("after the refusal"));
    expect(committed.intentSequence).toBe(1);
  });
});
