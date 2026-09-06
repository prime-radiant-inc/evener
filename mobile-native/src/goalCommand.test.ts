import { DatabaseSync } from "node:sqlite";
import { expect, it } from "vitest";
import type { Thread } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
  type ConversationClientLike,
  createConversationService,
} from "../../mobile/src/services/conversation";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { DraftDocument } from "./draftDocument";
import { type DraftDatabase, DraftRepository } from "./draftRepository";
import { goalObjective, submitGoalCommand } from "./goalCommand";

function boundary() {
  const thread: Thread = {
    id: "thread",
    sessionId: "session",
    preview: "",
    ephemeral: false,
    modelProvider: "scripted",
    createdAt: 1,
    updatedAt: 1,
    status: { type: "idle" },
    cwd: "/test",
    cliVersion: "test",
    source: "local",
    turns: [],
    evener: {
      ref: "local:test",
      instanceId: "instance",
      queue: { revision: 0 },
      capabilities: {
        send: true,
        steer: false,
        interrupt: false,
        compact: true,
        clear: false,
        forkFromTurn: false,
        shutdown: true,
        changeModel: true,
        changeVisionModel: false,
        queue: false,
        goal: true,
        rename: true,
      },
    },
  };
  const objectives: string[] = [];
  const io = {
    read: async () => ({ thread: structuredClone(thread) }),
    set: async (objective: string) => {
      objectives.push(objective);
      return { started: false };
    },
  };
  const service = createConversationService({
    request: async (method, params) => {
      if (method === "thread/read") return io.read();
      if (method === "goal/set")
        return io.set((params as { objective: string }).objective);
      throw new Error(`Unexpected ${method}`);
    },
    onNotification: () => () => {},
  } as ConversationClientLike);
  return { thread, objectives, io, service };
}

it.each([
  ["/goal", ""],
  ["/goal  objective\nnext line ", "objective\nnext line"],
  ["/goals objective", null],
  [" /goal objective", null],
  ["/goal\nobjective", null],
  ["/plugin:goal objective", null],
])("parses the web goal command grammar: %s", (input, expected) => {
  expect(goalObjective(input)).toBe(expected);
  expect(goalObjective(input, 1)).toBeNull();
});

it("preserves a newer live goal across an older hydration and isolates session notifications", async () => {
  const { thread, io, service } = boundary();
  thread.evener.goal = {
    objective: "initial",
    status: "active",
    iterations: 1,
  };
  const store = createConversationStore();
  const activity = createActivityStore().getState();
  try {
    await store.getState().openProjected(service, activity, "local:test");
    expect(store.getState().conversation?.goal?.objective).toBe("initial");
    let finishRead!: () => void;
    let entered!: () => void;
    const enteredRead = new Promise<void>((resolve) => {
      entered = resolve;
    });
    io.read = () =>
      new Promise((resolve) => {
        finishRead = () => resolve({ thread: structuredClone(thread) });
        entered();
      });
    const refresh = store.getState().rehydrate(service, activity);
    await enteredRead;
    const updated = { objective: "remote", status: "achieved", iterations: 4 };
    store.getState().applyNotification({
      method: "evener/goal/updated",
      params: { threadId: "other", ref: "other:test", goal: updated },
    });
    expect(store.getState().conversation?.goal?.objective).toBe("initial");
    store.getState().applyNotification({
      method: "evener/goal/updated",
      params: { threadId: "thread", ref: "local:test", goal: updated },
    });
    finishRead();
    await refresh;
    expect(store.getState().conversation?.goal).toEqual(updated);
    store.getState().applyNotification({
      method: "evener/goal/updated",
      params: { threadId: "thread", ref: "local:test", goal: null },
    });
    expect(store.getState().conversation?.goal).toBeNull();
  } finally {
    store.getState().close();
    service.close();
  }
});

it("checkpoints goal changes, retains uncertain delivery, and clears without consuming an ordinary draft", async () => {
  const db = new DatabaseSync(":memory:");
  const adapter: DraftDatabase = {
    execSync: (sql) => db.exec(sql),
    runSync: (sql, ...params) => db.prepare(sql).run(...params),
    getFirstSync: <T>(sql: string, ...params: string[]) =>
      (db.prepare(sql).get(...params) as T | undefined) ?? null,
  };
  const repository = new DraftRepository(adapter);
  const destination = { hubId: "hub", sessionRef: "local:test" };
  const { io, objectives, service, thread } = boundary();
  const document = new DraftDocument(() => repository, destination);
  try {
    await service.open("local:test");
    document.edit("/goal objective");
    await submitGoalCommand(document, service);
    expect(objectives).toEqual(["objective"]);
    expect(repository.read(destination).unconfirmed).toBeNull();
    document.edit("ordinary draft");
    await submitGoalCommand(document, service, true);
    expect(objectives).toEqual(["objective", ""]);
    expect(repository.read(destination).draft).toBe("ordinary draft");
    thread.evener.capabilities.goal = false;
    await service.open("local:test");
    await expect(service.setGoal("blocked")).rejects.toThrow();
    expect(objectives).toHaveLength(2);
    thread.evener.capabilities.goal = true;
    await service.open("local:test");
    io.set = async () => {
      expect(repository.read(destination).unconfirmed).toBe("/goal uncertain");
      document.edit("newer draft");
      throw new Error("Lost acknowledgement");
    };
    document.edit("/goal uncertain");
    await expect(submitGoalCommand(document, service)).rejects.toThrow();
    const reopened = new DraftDocument(() => repository, destination);
    expect(reopened.getSnapshot().record).toMatchObject({
      draft: "newer draft",
      unconfirmed: "/goal uncertain",
    });
    await submitGoalCommand(reopened, service, true);
    expect(reopened.getSnapshot().record.unconfirmed).toBe("/goal uncertain");
  } finally {
    service.close();
    db.close();
  }
});
