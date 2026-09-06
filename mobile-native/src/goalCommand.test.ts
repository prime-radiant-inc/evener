import { DatabaseSync } from "node:sqlite";
import { expect, it } from "vitest";
import type {
  ModelListResponse,
  Thread,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
  type ConversationClientLike,
  createConversationService,
} from "../../mobile/src/services/conversation";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { submitComposerCommand } from "./composerCommand";
import { DraftDocument } from "./draftDocument";
import { type DraftDatabase, DraftRepository } from "./draftRepository";
import { goalObjective, submitGoalCommand } from "./goalCommand";

const commandContext = { isCurrent: () => true, reasoning: () => null };

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
    models: async (): Promise<ModelListResponse> => ({
      data: [
        {
          provider: "scripted",
          model: "alternate",
          displayName: "Alternate Model",
        },
      ],
    }),
    lifecycle: async (_method: string, _params: unknown) => ({}),
    read: async () => ({ thread: structuredClone(thread) }),
    set: async (objective: string) => {
      objectives.push(objective);
      return { started: false };
    },
  };
  const service = createConversationService({
    request: async (method, params) => {
      if (method === "thread/read") return io.read();
      if (method === "model/list") return io.models();
      if (
        method === "thread/model/set" ||
        method === "thread/reasoning-effort/set"
      )
        return io.lifecycle(method, params);
      if (method === "goal/set")
        return io.set((params as { objective: string }).objective);
      if (method === "thread/compact/start" || method === "thread/shutdown")
        return io.lifecycle(method, params);
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

it.each([
  ["/compact", "thread/compact/start"],
  ["/shutdown", "thread/shutdown"],
])(
  "checkpoints %s and preserves a newer draft through acknowledgement loss",
  async (command, method) => {
    const db = new DatabaseSync(":memory:");
    const repository = new DraftRepository({
      execSync: (sql) => db.exec(sql),
      runSync: (sql, ...params) => db.prepare(sql).run(...params),
      getFirstSync: <T>(sql: string, ...params: string[]) =>
        (db.prepare(sql).get(...params) as T | undefined) ?? null,
    });
    const destination = { hubId: "hub", sessionRef: "local:test" };
    const document = new DraftDocument(() => repository, destination);
    const { io, service } = boundary();
    try {
      await service.open(destination.sessionRef);
      let received = 0;
      io.lifecycle = async (actual, params) => {
        received++;
        expect(actual).toBe(method);
        expect(params).toEqual({ ref: destination.sessionRef });
        expect(repository.read(destination).unconfirmed).toBe(command);
        return {};
      };
      document.edit(command);
      expect(
        await submitComposerCommand(document, service, commandContext),
      ).toBe(command.slice(1));
      expect(received).toBe(1);
      expect(repository.read(destination)).toMatchObject({
        draft: "",
        unconfirmed: null,
      });
      document.edit(command);
      io.lifecycle = async () => {
        document.edit("newer draft");
        throw new Error("Lost acknowledgement");
      };
      await expect(
        submitComposerCommand(document, service, commandContext),
      ).rejects.toThrow();
      expect(repository.read(destination)).toMatchObject({
        draft: "newer draft",
        unconfirmed: command,
      });
      document.edit(command);
      expect(
        await submitComposerCommand(document, service, commandContext),
      ).toBeNull();
      expect(repository.read(destination).unconfirmed).toBe(command);
    } finally {
      service.close();
      db.close();
    }
  },
);

it("preserves newer live work state across an older hydration and isolates session notifications", async () => {
  const { thread, io, service } = boundary();
  thread.evener.goal = {
    objective: "initial",
    status: "active",
    iterations: 1,
  };
  const store = createConversationStore();
  thread.evener.tasks = { total: 2, done: 0 };
  const activity = createActivityStore().getState();
  try {
    await store.getState().openProjected(service, activity, "local:test");
    expect(store.getState().conversation?.goal?.objective).toBe("initial");
    expect(store.getState().conversation?.tasks).toEqual({ total: 2, done: 0 });
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
    store.getState().applyNotification({
      method: "evener/task/updated",
      params: {
        threadId: "thread",
        ref: "local:test",
        total: 3,
        done: 1,
        current: { id: 2, description: "task-sentinel" },
      },
    });
    finishRead();
    await refresh;
    expect(store.getState().conversation?.goal).toEqual(updated);
    expect(store.getState().conversation?.tasks).toMatchObject({
      total: 3,
      done: 1,
      current: { id: 2 },
    });
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

function commandDraft() {
  const db = new DatabaseSync(":memory:");
  const repository = new DraftRepository({
    execSync: (sql) => db.exec(sql),
    runSync: (sql, ...params) => db.prepare(sql).run(...params),
    getFirstSync: <T>(sql: string, ...params: string[]) =>
      (db.prepare(sql).get(...params) as T | undefined) ?? null,
  });
  const destination = { hubId: "hub", sessionRef: "local:test" };
  return { db, document: new DraftDocument(() => repository, destination) };
}

it.each([
  [
    "/model SCRIPTED/ALTERNATE",
    "thread/model/set",
    { modelProvider: "scripted", model: "alternate" },
  ],
  [
    "/model alternate model",
    "thread/model/set",
    { modelProvider: "scripted", model: "alternate" },
  ],
  [
    "/reasoning-effort HIGH",
    "thread/reasoning-effort/set",
    { reasoningEffort: "high" },
  ],
  [
    "/reasoning-effort (default)",
    "thread/reasoning-effort/set",
    { reasoningEffort: "" },
  ],
  ["/reasoning-effort", "thread/reasoning-effort/set", { reasoningEffort: "" }],
  [
    "/reasoning-effort none (off)",
    "thread/reasoning-effort/set",
    { reasoningEffort: "none" },
  ],
])(
  "resolves the web enum argument for %s before checkpointing",
  async (text, method, expected) => {
    const { db, document } = commandDraft();
    const { io, service } = boundary();
    let received = 0;
    try {
      await service.open("local:test");
      document.edit(text);
      io.lifecycle = async (actual, params) => {
        received++;
        expect(actual).toBe(method);
        expect(params).toEqual({ ref: "local:test", ...expected });
        expect(document.getSnapshot().record.unconfirmed).toBe(text);
        return {};
      };
      await submitComposerCommand(document, service, {
        ...commandContext,
        reasoning: () => ({
          supportsReasoning: true,
          reasoningEffortLevels: ["none", "high"],
        }),
      });
      expect(received).toBe(1);
      expect(document.getSnapshot().record).toMatchObject({
        draft: "",
        unconfirmed: null,
      });
    } finally {
      service.close();
      db.close();
    }
  },
);

it.each([
  "/model missing",
  "/model",
  "/reasoning-effort high",
  "/reasoning-effort",
])("keeps invalid enum input editable: %s", async (text) => {
  const { db, document } = commandDraft();
  const { service } = boundary();
  try {
    await service.open("local:test");
    document.edit(text);
    await expect(
      submitComposerCommand(document, service, {
        ...commandContext,
        reasoning: () => ({
          supportsReasoning: true,
          reasoningEffortLevels: [],
        }),
      }),
    ).rejects.toThrow();
    expect(document.getSnapshot().record).toMatchObject({
      draft: text,
      unconfirmed: null,
    });
  } finally {
    service.close();
    db.close();
  }
});

it.each(["draft", "binding"])(
  "discards a model lookup when its %s changes",
  async (change) => {
    const { db, document } = commandDraft();
    const { io, service } = boundary();
    let current = true;
    try {
      await service.open("local:test");
      document.edit("/model scripted/alternate");
      io.models = async () => {
        if (change === "draft") document.edit("newer draft");
        else current = false;
        return { data: [{ provider: "scripted", model: "alternate" }] };
      };
      io.lifecycle = async () => {
        throw new Error("Stale mutation reached transport");
      };
      expect(
        await submitComposerCommand(document, service, {
          ...commandContext,
          isCurrent: () => current,
        }),
      ).toBeNull();
      expect(document.getSnapshot().record).toMatchObject({
        draft: change === "draft" ? "newer draft" : "/model scripted/alternate",
        unconfirmed: null,
      });
    } finally {
      service.close();
      db.close();
    }
  },
);
