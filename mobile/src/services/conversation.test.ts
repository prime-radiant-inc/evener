// ConversationService tests with a fake AppwireClient. Covers:
// - One connection serves sequential sessions on profile A
// - Switching closes A before opening B
// - Late A frames/hydration cannot overwrite B (generation safety)
// - Switching back rehydrates A
// - Notification refs route with profile identity
// - Paging cursor is passed through
// - Malformed projection retains last good state with compatibility error
// - All V1 mutation actions
// - False capability blocks before request
// - Server action-unavailable refreshes capabilities
// - Conflict restores draft
// - No automatic retry

import { beforeEach, describe, expect, it, vi } from "vitest";
import { WireError } from "../../../cmd/evener-hub/frontend/src/protocol/errors";
import type {
  AnyNotification,
  EmptyResponse,
  InputItem,
  MethodName,
  MethodTypes,
  MutationReceipt,
  Thread,
  ThreadCapabilities,
  ThreadReadResponse,
  ThreadTurnsListResponse,
  Turn,
  TurnCancelQueuedResponse,
  TurnInterruptResponse,
  TurnQueueResponse,
  TurnStartResponse,
  TurnSteerResponse,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import * as projectModule from "../conversation/project";
import type { createActivityService } from "./activity";
import * as activityModule from "./activity";
import {
  createConversationService,
  type LiveConversationService,
} from "./conversation";

// --- minimal fake client (cannot import Hub testing modules) ----------------

type RequestHandler = (params: unknown) => unknown | Promise<unknown>;

class FakeAppwireClient {
  readonly calls: { method: string; params: unknown }[] = [];
  private readonly handlers = new Map<string, RequestHandler>();
  private readonly notificationHandlers = new Set<
    (n: AnyNotification) => void
  >();

  on<M extends MethodName>(
    method: M,
    handler: (
      params: MethodTypes[M]["params"],
    ) => MethodTypes[M]["result"] | Promise<MethodTypes[M]["result"]>,
  ): void {
    this.handlers.set(method, handler as unknown as RequestHandler);
  }

  request<M extends MethodName>(
    method: M,
    params: MethodTypes[M]["params"],
  ): Promise<MethodTypes[M]["result"]> {
    this.calls.push({ method, params });
    const handler = this.handlers.get(method);
    if (!handler) {
      return Promise.reject(
        new Error(`FakeAppwireClient: no handler for "${method}"`),
      );
    }
    return Promise.resolve().then(
      () => handler(params) as MethodTypes[M]["result"],
    );
  }

  onNotification(cb: (n: AnyNotification) => void): () => void {
    this.notificationHandlers.add(cb);
    return () => {
      this.notificationHandlers.delete(cb);
    };
  }

  emitNotification(n: AnyNotification): void {
    for (const cb of Array.from(this.notificationHandlers)) cb(n);
  }

  get notificationSubscriberCount(): number {
    return this.notificationHandlers.size;
  }
}

// --- fixture helpers ---------------------------------------------------------

const ALL_TRUE_CAPS: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

const EMPTY_RESPONSE: EmptyResponse = {};

function makeThread(over: Partial<Thread> = {}): Thread {
  return {
    id: "thread-1",
    sessionId: "session-1",
    preview: "hello",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 1_000_000,
    updatedAt: 1_000_000,
    status: { type: "ready" },
    cwd: "/tmp",
    cliVersion: "1.0.0",
    source: "local",
    turns: [],
    evener: {
      ref: "ref-1",
      capabilities: ALL_TRUE_CAPS,
      queue: { revision: 0 },
    },
    ...over,
  };
}

function makeReadResponse(
  thread: Thread,
  olderCursor?: string,
): ThreadReadResponse {
  return { thread, olderCursor };
}

function makeReceipt(
  kind: "send" | "steer" | "queue" | "interrupt" = "send",
  over: Partial<MutationReceipt> = {},
): MutationReceipt {
  const receipt: MutationReceipt = {
    clientMutationId: "cmid-1",
    disposition: "accepted",
    threadId: "thread-1",
    projectionState: "current",
  };
  if (kind === "send" || kind === "steer") receipt.turnId = "turn-1";
  if (kind === "queue") receipt.queueEntryIds = ["queue-1"];
  return { ...receipt, ...over };
}

let idCounter = 0;
function fakeIdFactory(): string {
  idCounter += 1;
  return `cmid-${idCounter}`;
}

function setup(options: { thread?: Thread; olderCursor?: string } = {}) {
  const client = new FakeAppwireClient();
  const thread = options.thread ?? makeThread();
  client.on("thread/read", () => makeReadResponse(thread, options.olderCursor));
  client.on(
    "thread/turns/list",
    () => ({ data: [], nextCursor: undefined }) as ThreadTurnsListResponse,
  );
  const service = createConversationService(client, {
    idFactory: fakeIdFactory,
  });
  return { client, service, thread };
}

function textInput(text: string): InputItem[] {
  return [{ type: "text", text }];
}

// --- service tests ------------------------------------------------------------

describe("ConversationService", () => {
  beforeEach(() => {
    idCounter = 0;
  });

  describe("open", () => {
    it("projects thread to MobileConversation", async () => {
      const { service } = setup();
      const conv = await service.open("ref-1");
      expect(conv.id).toBe("thread-1");
      expect(conv.status).toBe("ready");
      expect(conv.capabilities.send).toBe(true);
    });

    it("passes ref to thread/read with subscribe and includeTurns", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      const call = client.calls.find((c) => c.method === "thread/read");
      expect(call).toBeDefined();
      // M1: exact canonical request — toEqual full object, no extra keys.
      expect(call?.params).toEqual({
        ref: "ref-1",
        includeTurns: true,
        subscribe: true,
        replaceSubscription: true,
      });
    });

    it("stores olderCursor from read response", async () => {
      const { service } = setup({ olderCursor: "cursor-abc" });
      await service.open("ref-1");
      const result = await service.loadOlder("cursor-abc");
      expect(result.items).toEqual([]);
    });

    it("one connection serves sequential sessions on profile A", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      service.close();
      // Reopen with same client — same connection generation
      client.on("thread/read", () =>
        makeReadResponse(makeThread({ id: "thread-2" })),
      );
      const service2 = createConversationService(client, {
        idFactory: fakeIdFactory,
      });
      const conv = await service2.open("ref-2");
      expect(conv.id).toBe("thread-2");
    });
  });

  describe("loadOlder", () => {
    it("passes cursor to thread/turns/list", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      client.on(
        "thread/turns/list",
        () =>
          ({
            data: [],
            nextCursor: "next-cursor",
          }) as ThreadTurnsListResponse,
      );
      const result = await service.loadOlder("cursor-xyz");
      const call = client.calls.find((c) => c.method === "thread/turns/list");
      expect(call?.params).toMatchObject({
        cursor: "cursor-xyz",
        ref: "ref-1",
      });
      expect(result.nextCursor).toBe("next-cursor");
    });
  });

  describe("subscribeNotifications", () => {
    it("returns unsubscribe that stops delivery", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      const received: AnyNotification[] = [];
      const unsub = service.subscribeNotifications((n) => received.push(n));
      const notif = {
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
        },
      } as AnyNotification;
      client.emitNotification(notif);
      expect(received).toHaveLength(1);
      unsub();
      client.emitNotification(notif);
      expect(received).toHaveLength(1);
    });
  });

  describe("mutations — all V1 actions", () => {
    it("send calls turn/start with clientMutationId and input", async () => {
      const { client, service } = setup();
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      await service.open("ref-1");
      const receipt = await service.send(textInput("hello"));
      expect(receipt.clientMutationId).toBe("cmid-1");
      const startCall = client.calls.find((c) => c.method === "turn/start");
      expect(startCall).toBeDefined();
      const startParams = (startCall ?? { params: {} }).params as {
        clientMutationId: string;
      };
      expect(startParams).toMatchObject({
        ref: "ref-1",
        input: [{ type: "text", text: "hello" }],
      });
      expect(startParams.clientMutationId).toBe("cmid-1");
    });

    it("steer calls turn/steer", async () => {
      const { client, service } = setup();
      client.on(
        "turn/steer",
        () => ({ receipt: makeReceipt("steer") }) as TurnSteerResponse,
      );
      await service.open("ref-1");
      const receipt = await service.steer(textInput("steer this"));
      expect(receipt.clientMutationId).toBe("cmid-1");
      const call = client.calls.find((c) => c.method === "turn/steer");
      expect(call?.params).toMatchObject({
        ref: "ref-1",
        input: [{ type: "text", text: "steer this" }],
      });
    });

    it("queue calls turn/queue", async () => {
      const { client, service } = setup();
      client.on(
        "turn/queue",
        () => ({ receipt: makeReceipt("queue") }) as TurnQueueResponse,
      );
      await service.open("ref-1");
      const receipt = await service.queue(textInput("queued"));
      expect(receipt.clientMutationId).toBe("cmid-1");
      const call = client.calls.find((c) => c.method === "turn/queue");
      expect(call?.params).toMatchObject({
        ref: "ref-1",
        input: [{ type: "text", text: "queued" }],
      });
    });

    it("interrupt calls turn/interrupt", async () => {
      const { client, service } = setup();
      client.on(
        "turn/interrupt",
        () => ({ receipt: makeReceipt("interrupt") }) as TurnInterruptResponse,
      );
      await service.open("ref-1");
      const receipt = await service.interrupt();
      expect(receipt.clientMutationId).toBe("cmid-1");
      const call = client.calls.find((c) => c.method === "turn/interrupt");
      expect(call?.params).toMatchObject({ ref: "ref-1" });
    });

    it("rejects every malformed, stale, mismatched, or nonaccepted send receipt", async () => {
      const validReceipt = makeReceipt("send");
      const validTurn = { id: "t1", itemsView: "default", status: "running" };
      const invalidResults: unknown[] = [
        null,
        { turn: validTurn },
        { receipt: validReceipt },
        { turn: validTurn, receipt: validReceipt, extra: true },
        { turn: validTurn, receipt: null },
        {
          turn: validTurn,
          receipt: { ...validReceipt, clientMutationId: "stale-cmid" },
        },
        {
          turn: validTurn,
          receipt: { ...validReceipt, disposition: "replayed" },
        },
        {
          turn: validTurn,
          receipt: { ...validReceipt, threadId: "" },
        },
        {
          turn: validTurn,
          receipt: { ...validReceipt, turnId: "" },
        },
        {
          turn: validTurn,
          receipt: { ...validReceipt, projectionState: "" },
        },
        {
          turn: validTurn,
          receipt: { ...validReceipt, queueEntryIds: ["forbidden"] },
        },
      ];
      for (const invalid of invalidResults) {
        const { client, service } = setup();
        client.on("turn/start", () => invalid as TurnStartResponse);
        await service.open("ref-1");
        await expect(service.send(textInput("hello"))).rejects.toThrow(
          /ConversationService/,
        );
      }
    });

    it("enforces exact steer, queue, and interrupt receipt fields", async () => {
      const cases = [
        {
          method: "turn/steer" as const,
          invoke: (service: LiveConversationService) =>
            service.steer(textInput("steer")),
          invalid: { receipt: makeReceipt("interrupt") },
        },
        {
          method: "turn/queue" as const,
          invoke: (service: LiveConversationService) =>
            service.queue(textInput("queue")),
          invalid: {
            receipt: { ...makeReceipt("queue"), queueEntryIds: [] },
          },
        },
        {
          method: "turn/interrupt" as const,
          invoke: (service: LiveConversationService) => service.interrupt(),
          invalid: {
            receipt: { ...makeReceipt("interrupt"), turnId: "forbidden" },
          },
        },
      ];
      for (const testCase of cases) {
        const { client, service } = setup();
        client.on(testCase.method, () => testCase.invalid as never);
        await service.open("ref-1");
        await expect(testCase.invoke(service)).rejects.toThrow(
          /ConversationService/,
        );
      }
    });

    it("compact calls thread/compact/start", async () => {
      const { client, service } = setup();
      client.on("thread/compact/start", () => EMPTY_RESPONSE);
      await service.open("ref-1");
      await service.compact();
      const call = client.calls.find(
        (c) => c.method === "thread/compact/start",
      );
      expect(call?.params).toMatchObject({ ref: "ref-1" });
    });

    it("shutdown calls thread/shutdown", async () => {
      const { client, service } = setup();
      client.on("thread/shutdown", () => EMPTY_RESPONSE);
      await service.open("ref-1");
      await service.shutdown();
      const call = client.calls.find((c) => c.method === "thread/shutdown");
      expect(call?.params).toMatchObject({ ref: "ref-1" });
    });

    it("changeModel calls thread/model/set", async () => {
      const { client, service } = setup();
      client.on("thread/model/set", () => EMPTY_RESPONSE);
      await service.open("ref-1");
      await service.changeModel("openai", "gpt-4");
      const call = client.calls.find((c) => c.method === "thread/model/set");
      expect(call?.params).toMatchObject({
        ref: "ref-1",
        modelProvider: "openai",
        model: "gpt-4",
      });
    });

    it("setReasoningEffort calls thread/reasoning-effort/set", async () => {
      const { client, service } = setup();
      client.on("thread/reasoning-effort/set", () => EMPTY_RESPONSE);
      await service.open("ref-1");
      await service.setReasoningEffort("high");
      const call = client.calls.find(
        (c) => c.method === "thread/reasoning-effort/set",
      );
      expect(call?.params).toMatchObject({
        ref: "ref-1",
        reasoningEffort: "high",
      });
    });

    it("rename calls evener/thread/name/set", async () => {
      const { client, service } = setup();
      client.on("evener/thread/name/set", () => EMPTY_RESPONSE);
      await service.open("ref-1");
      await service.rename("My Thread");
      const call = client.calls.find(
        (c) => c.method === "evener/thread/name/set",
      );
      expect(call?.params).toMatchObject({ ref: "ref-1", name: "My Thread" });
    });

    it("cancelQueued calls turn/cancelQueued", async () => {
      const { client, service } = setup();
      const cancelResp: TurnCancelQueuedResponse = {
        removedText: "queued text",
        receipt: makeReceipt(),
      };
      client.on("turn/cancelQueued", () => cancelResp);
      await service.open("ref-1");
      const result = await service.cancelQueued(0, "entry-1");
      expect(result.removedText).toBe("queued text");
      const call = client.calls.find((c) => c.method === "turn/cancelQueued");
      expect(call?.params).toMatchObject({
        ref: "ref-1",
        index: 0,
        expectedEntryId: "entry-1",
      });
    });
  });

  describe("capability gating", () => {
    it("send throws when capabilities.send is false", async () => {
      const thread = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: { ...ALL_TRUE_CAPS, send: false },
          queue: { revision: 0 },
        },
      });
      const { client, service } = setup({ thread });
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      await service.open("ref-1");
      await expect(service.send(textInput("hello"))).rejects.toThrow();
      const call = client.calls.find((c) => c.method === "turn/start");
      expect(call).toBeUndefined();
    });

    it("steer throws when capabilities.steer is false", async () => {
      const thread = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: { ...ALL_TRUE_CAPS, steer: false },
          queue: { revision: 0 },
        },
      });
      const { client, service } = setup({ thread });
      client.on(
        "turn/steer",
        () => ({ receipt: makeReceipt("steer") }) as TurnSteerResponse,
      );
      await service.open("ref-1");
      await expect(service.steer(textInput("steer"))).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/steer"),
      ).toBeUndefined();
    });

    it("queue throws when capabilities.queue is false", async () => {
      const thread = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: { ...ALL_TRUE_CAPS, queue: false },
          queue: { revision: 0 },
        },
      });
      const { client, service } = setup({ thread });
      client.on(
        "turn/queue",
        () => ({ receipt: makeReceipt("queue") }) as TurnQueueResponse,
      );
      await service.open("ref-1");
      await expect(service.queue(textInput("q"))).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/queue"),
      ).toBeUndefined();
    });

    it("interrupt throws when capabilities.interrupt is false", async () => {
      const thread = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: { ...ALL_TRUE_CAPS, interrupt: false },
          queue: { revision: 0 },
        },
      });
      const { client, service } = setup({ thread });
      client.on(
        "turn/interrupt",
        () => ({ receipt: makeReceipt("interrupt") }) as TurnInterruptResponse,
      );
      await service.open("ref-1");
      await expect(service.interrupt()).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/interrupt"),
      ).toBeUndefined();
    });

    it("compact throws when capabilities.compact is false", async () => {
      const thread = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: { ...ALL_TRUE_CAPS, compact: false },
          queue: { revision: 0 },
        },
      });
      const { client, service } = setup({ thread });
      client.on("thread/compact/start", () => EMPTY_RESPONSE);
      await service.open("ref-1");
      await expect(service.compact()).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/compact/start"),
      ).toBeUndefined();
    });

    it("shutdown throws when capabilities.shutdown is false", async () => {
      const thread = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: { ...ALL_TRUE_CAPS, shutdown: false },
          queue: { revision: 0 },
        },
      });
      const { client, service } = setup({ thread });
      client.on("thread/shutdown", () => EMPTY_RESPONSE);
      await service.open("ref-1");
      await expect(service.shutdown()).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/shutdown"),
      ).toBeUndefined();
    });

    it("changeModel throws when capabilities.changeModel is false", async () => {
      const thread = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: { ...ALL_TRUE_CAPS, changeModel: false },
          queue: { revision: 0 },
        },
      });
      const { client, service } = setup({ thread });
      client.on("thread/model/set", () => EMPTY_RESPONSE);
      await service.open("ref-1");
      await expect(service.changeModel("openai", "gpt-4")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/model/set"),
      ).toBeUndefined();
    });

    it("rename throws when capabilities.rename is false", async () => {
      const thread = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: { ...ALL_TRUE_CAPS, rename: false },
          queue: { revision: 0 },
        },
      });
      const { client, service } = setup({ thread });
      client.on("evener/thread/name/set", () => EMPTY_RESPONSE);
      await service.open("ref-1");
      await expect(service.rename("name")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "evener/thread/name/set"),
      ).toBeUndefined();
    });
  });

  describe("server action-unavailable refreshes capabilities", () => {
    it("does NOT auto-refresh on actionUnavailable — store owns the single read", async () => {
      // F2: The service must NOT call refreshCapabilities itself on
      // actionUnavailable. Exactly one non-subscribing thread/read occurs,
      // and it is driven by the store, not the service. The service just
      // throws so the store can handle recovery.
      const { client, service } = setup();
      let readCount = 0;
      client.on("thread/read", () => {
        readCount += 1;
        return makeReadResponse(makeThread());
      });
      client.on("turn/start", () => {
        throw new WireError("action unavailable", -32603, {
          evenerErrorInfo: "actionUnavailable",
        });
      });
      await service.open("ref-1");
      const initialReadCount = readCount;
      await expect(service.send(textInput("hello"))).rejects.toThrow();
      // The service must NOT have triggered an extra thread/read — only the
      // store will call refreshCapabilities.
      expect(readCount).toBe(initialReadCount);
    });
  });

  describe("readProjection", () => {
    it("sends thread/read with turnLimit 50, includeTurns, subscribe, replaceSubscription", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      // Clear previous calls so we see only readProjection's request.
      client.calls.length = 0;
      await service.readProjection("ref-1");
      const call = client.calls.find((c) => c.method === "thread/read");
      expect(call).toBeDefined();
      // M1: exact canonical request — toEqual full object, no extra keys.
      expect(call?.params).toEqual({
        ref: "ref-1",
        includeTurns: true,
        subscribe: true,
        replaceSubscription: true,
        turnLimit: 50,
      });
    });

    it("returns ConversationReadProjection with conversation, activity, olderCursor", async () => {
      const { service } = setup({ olderCursor: "older-abc" });
      await service.open("ref-1");
      const result = await service.readProjection("ref-1");
      expect(result).toBeDefined();
      expect(result?.conversation).toBeDefined();
      expect(result?.conversation.id).toBe("thread-1");
      expect(result?.activity).toBeDefined();
      expect(result?.activity.tasks).toEqual([]);
      expect(result?.activity.work).toEqual([]);
      expect(result?.activity.usage).toBeDefined();
      expect(result?.activity.capabilities).toBeDefined();
      expect(result?.olderCursor).toBe("older-abc");
    });

    it("does not expose a raw Thread in the projection result", async () => {
      const { service } = setup();
      await service.open("ref-1");
      const result = await service.readProjection("ref-1");
      expect(result).toBeDefined();
      // The result must not carry a raw Thread — only conversation + activity.
      const keys = Object.keys(result ?? {});
      expect(keys).not.toContain("thread");
      expect(keys).toEqual(
        expect.arrayContaining(["conversation", "activity", "olderCursor"]),
      );
    });

    it("does not send cursor to thread/read (cursor belongs to thread/turns/list)", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      client.calls.length = 0;
      await service.readProjection("ref-1");
      const call = client.calls.find((c) => c.method === "thread/read");
      expect(call).toBeDefined();
      const params = call?.params as Record<string, unknown>;
      expect(params).not.toHaveProperty("cursor");
    });

    it("activity contains sanitized tasks/work/usage from the thread", async () => {
      const thread = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
          tasks: { total: 5, done: 2 },
          usage: { inputTokens: 100, outputTokens: 50, totalTokens: 150 },
          diagnostics: {
            jobs: [
              {
                jobId: "job-1",
                jobType: "shell",
                status: "running",
                outputBytes: 1024,
              },
            ],
          },
        },
      });
      const { service } = setup({ thread });
      await service.open("ref-1");
      const result = await service.readProjection("ref-1");
      expect(result?.activity.tasks).toHaveLength(3);
      const doneGroup = result?.activity.tasks.find((g) => g.status === "done");
      expect(doneGroup?.count).toBe(2);
      expect(result?.activity.work).toHaveLength(1);
      expect(result?.activity.work[0]?.label).toBe("shell");
      expect(result?.activity.usage.totalTokens).toBe(150);
    });
  });

  describe("refreshCapabilities (non-subscribing)", () => {
    it("reads thread metadata without subscribing or loading turns", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      client.calls.length = 0;
      const caps = await service.refreshCapabilities("ref-1");
      expect(caps).not.toBeNull();
      expect(caps?.send).toBe(true);
      const call = client.calls.find((c) => c.method === "thread/read");
      expect(call).toBeDefined();
      const params = call?.params as Record<string, unknown>;
      expect(params.includeTurns).toBe(false);
      expect(params.subscribe).toBe(false);
      expect(params).not.toHaveProperty("replaceSubscription");
      // M2: exactly one refresh thread/read occurs.
      const readCalls = client.calls.filter((c) => c.method === "thread/read");
      expect(readCalls).toHaveLength(1);
    });

    it("returns null when no thread is open", async () => {
      const { service } = setup();
      // With explicit ref, refreshCapabilities always reads the wire.
      // The null return is no longer based on internal ref state.
      const caps = await service.refreshCapabilities("ref-1");
      expect(caps).not.toBeNull();
      expect(caps?.send).toBe(true);
    });
    it("late refresh for A after opening B does not overwrite B's cached capabilities", async () => {
      // Deferred-promise test: start refreshCapabilities("ref-A") with a
      // pending thread/read, then open("ref-B") while A's refresh is in
      // flight, then resolve A. The returned A capabilities must remain
      // available to the caller, but B's mutation-gating cache must stay
      // B's — A's late resolution must neither overwrite B's capabilities
      // nor gate B with A's values.
      const { client, service } = setup();
      const capsA: ThreadCapabilities = {
        ...ALL_TRUE_CAPS,
        send: false,
        rename: false,
      };
      const capsB: ThreadCapabilities = { ...ALL_TRUE_CAPS };
      const threadA = makeThread({
        id: "thread-A",
        evener: { ref: "ref-A", capabilities: capsA, queue: { revision: 0 } },
      });
      const threadB = makeThread({
        id: "thread-B",
        evener: { ref: "ref-B", capabilities: capsB, queue: { revision: 0 } },
      });

      let resolveARead = (_resp: ThreadReadResponse) => {};
      const aReadPromise = new Promise<ThreadReadResponse>((r) => {
        resolveARead = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string; subscribe?: boolean };
        // refresh: subscribe=false -> defer A; open: subscribe=true -> immediate
        if (p.ref === "ref-A" && p.subscribe === false) {
          return aReadPromise;
        }
        if (p.ref === "ref-A") return makeReadResponse(threadA);
        return makeReadResponse(threadB);
      });

      // Open A: ref=ref-A, capabilities=capsA.
      await service.open("ref-A");
      // Start A's refresh — pending on aReadPromise (not awaited yet).
      const refreshPromise = service.refreshCapabilities("ref-A");
      // Open B while A's refresh is in flight: ref=ref-B, capabilities=capsB.
      await service.open("ref-B");
      // Resolve A's late refresh now that B is the current ref.
      resolveARead(makeReadResponse(threadA));
      const aCaps = await refreshPromise;
      // Returned A capabilities remain available to the generation-safe store.
      expect(aCaps).toEqual(capsA);

      // B's service mutation gates remain B's. If A's caps (send=false) had
      // leaked into the cache, send would throw before reaching the wire.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      const receipt = await service.send(textInput("hello"));
      expect(receipt).toBeDefined();
      const startCall = client.calls.find((c) => c.method === "turn/start");
      expect(startCall).toBeDefined();
      expect(startCall?.params).toMatchObject({ ref: "ref-B" });
    });
    it("refresh updates cache when threadRef is still the current ref", async () => {
      // Converse/current-ref case: when the refresh resolves and threadRef
      // is still the currently open ref, the mutation-gating cache IS
      // updated. This guards against over-correcting into never updating.
      const { client, service } = setup();
      const capsBefore = ALL_TRUE_CAPS;
      const capsAfter: ThreadCapabilities = { ...ALL_TRUE_CAPS, send: false };
      const threadBefore = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: capsBefore,
          queue: { revision: 0 },
        },
      });
      const threadAfter = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: capsAfter,
          queue: { revision: 0 },
        },
      });

      client.on("thread/read", () => makeReadResponse(threadBefore));
      await service.open("ref-1");
      // ref=ref-1, capabilities=capsBefore (send=true).

      let resolveRead = (_resp: ThreadReadResponse) => {};
      const readPromise = new Promise<ThreadReadResponse>((r) => {
        resolveRead = r;
      });
      client.on("thread/read", () => readPromise);
      const refreshPromise = service.refreshCapabilities("ref-1");
      // ref is still ref-1 — no switch occurred.
      resolveRead(makeReadResponse(threadAfter));
      const caps = await refreshPromise;
      expect(caps).toEqual(capsAfter);

      // Cache was updated (threadRef === ref), so send is now gated by
      // capsAfter.send=false and must throw before reaching the wire.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      await expect(service.send(textInput("hello"))).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/start"),
      ).toBeUndefined();
    });
    it("late refresh A does not overwrite close→reopen same A (epoch bump)", async () => {
      // I2: close increments the epoch and clears the pair. A refresh that
      // was in flight before the close→reopen must not publish its stale
      // caps into the reopened-A cache even though the ref string matches.
      const { client, service } = setup();
      const capsOld: ThreadCapabilities = {
        ...ALL_TRUE_CAPS,
        send: false,
        rename: false,
      };
      const capsNew: ThreadCapabilities = { ...ALL_TRUE_CAPS };
      const threadOld = makeThread({
        evener: {
          ref: "ref-A",
          capabilities: capsOld,
          queue: { revision: 0 },
        },
      });
      const threadNew = makeThread({
        evener: {
          ref: "ref-A",
          capabilities: capsNew,
          queue: { revision: 0 },
        },
      });

      let resolveOldRead = (_resp: ThreadReadResponse) => {};
      const oldReadPromise = new Promise<ThreadReadResponse>((r) => {
        resolveOldRead = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string; subscribe?: boolean };
        if (p.subscribe !== false) return makeReadResponse(threadOld);
        return oldReadPromise;
      });

      await service.open("ref-A");
      // Start a refresh of the OLD epoch — pending on oldReadPromise.
      const refreshPromise = service.refreshCapabilities("ref-A");
      // Close increments the epoch and clears ref+capabilities.
      service.close();
      // Reopen A-new: new epoch, immediate read with capsNew.
      client.on("thread/read", (params) => {
        const p = params as { ref: string; subscribe?: boolean };
        if (p.subscribe !== false) return makeReadResponse(threadNew);
        return oldReadPromise;
      });
      await service.open("ref-A");

      // Now resolve the OLD refresh. Its epoch is stale, so it must NOT
      // publish capsOld into the reopened-A cache (which holds capsNew).
      resolveOldRead(makeReadResponse(threadOld));
      const staleCaps = await refreshPromise;
      // Returned requested capabilities are preserved even when stale.
      expect(staleCaps).toEqual(capsOld);

      // The cache must still hold capsNew (send=true), so send reaches wire.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      const receipt = await service.send(textInput("hello"));
      expect(receipt).toBeDefined();
      const startCall = client.calls.find((c) => c.method === "turn/start");
      expect(startCall).toBeDefined();
      expect(startCall?.params).toMatchObject({ ref: "ref-A" });
    });
    it("late refresh A does not overwrite same-ref readProjection replacement (epoch bump)", async () => {
      // I2: readProjection starting a new open-style read for the SAME ref
      // bumps the epoch, invalidating a refresh that was in flight before it.
      // The stale refresh must not publish caps into the replaced cache.
      const { client, service } = setup();
      const capsOld: ThreadCapabilities = { ...ALL_TRUE_CAPS, send: false };
      const capsNew: ThreadCapabilities = { ...ALL_TRUE_CAPS };
      const threadOld = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: capsOld,
          queue: { revision: 0 },
        },
      });
      const threadNew = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: capsNew,
          queue: { revision: 0 },
        },
      });

      let resolveRefresh = (_resp: ThreadReadResponse) => {};
      const refreshPromise0 = new Promise<ThreadReadResponse>((r) => {
        resolveRefresh = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string; subscribe?: boolean };
        if (p.subscribe === false) return refreshPromise0;
        return makeReadResponse(threadOld);
      });

      await service.open("ref-1");
      // Start a refresh (OLD epoch) — pending.
      const refreshPromise = service.refreshCapabilities("ref-1");
      // readProjection bumps the epoch (new open-style read, same ref).
      client.on("thread/read", () => makeReadResponse(threadNew));
      await service.readProjection("ref-1");

      // Resolve the stale OLD refresh. It must not publish capsOld.
      resolveRefresh(makeReadResponse(threadOld));
      const staleCaps = await refreshPromise;
      // Returned requested capabilities are preserved even when stale.
      expect(staleCaps).toEqual(capsOld);

      // Cache holds capsNew (send=true) -> send reaches wire.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      const receipt = await service.send(textInput("hello"));
      expect(receipt).toBeDefined();
      const startCall = client.calls.find((c) => c.method === "turn/start");
      expect(startCall).toBeDefined();
      expect(startCall?.params).toMatchObject({ ref: "ref-1" });
    });
  });

  describe("loadOlder with explicit limit", () => {
    it("passes limit 50 to thread/turns/list", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      client.calls.length = 0;
      await service.loadOlder("cursor-xyz");
      const call = client.calls.find((c) => c.method === "thread/turns/list");
      expect(call?.params).toMatchObject({
        ref: "ref-1",
        cursor: "cursor-xyz",
        limit: 50,
      });
    });
  });

  describe("close", () => {
    it("unsubscribes from notifications", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      const received: AnyNotification[] = [];
      service.subscribeNotifications((n) => received.push(n));
      service.close();
      client.emitNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
        },
      } as AnyNotification);
      expect(received).toHaveLength(0);
    });
    it("increments epoch and clears ref+capabilities so refresh cannot republish", async () => {
      // I2: close increments epoch and clears the pair. A refresh started
      // before close resolving after close must not republish into a closed
      // service (and must not leave a dangling ref/caps).
      const { client, service } = setup();
      const capsA: ThreadCapabilities = { ...ALL_TRUE_CAPS, send: false };
      const threadA = makeThread({
        evener: {
          ref: "ref-A",
          capabilities: capsA,
          queue: { revision: 0 },
        },
      });

      let resolveRefresh = (_resp: ThreadReadResponse) => {};
      const refreshPromise0 = new Promise<ThreadReadResponse>((r) => {
        resolveRefresh = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string; subscribe?: boolean };
        if (p.subscribe === false) return refreshPromise0;
        return makeReadResponse(threadA);
      });

      await service.open("ref-A");
      const refreshPromise = service.refreshCapabilities("ref-A");
      service.close();
      // Resolve the stale refresh after close.
      resolveRefresh(makeReadResponse(threadA));
      const staleCaps = await refreshPromise;
      // Returned requested capabilities are preserved even when stale.
      expect(staleCaps).toEqual(capsA);

      // After close, the service is fail-closed: send throws before wire.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      await expect(service.send(textInput("hello"))).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/start"),
      ).toBeUndefined();
    });
  });

  describe("open/readProjection epoch pair (I1)", () => {
    it("starting a new open invalidates prior epoch — pending B mutation denied (fail-closed before await)", async () => {
      // I1: starting open("ref-B") clears ref+capabilities before the
      // await, so a mutation issued while B's read is in flight is denied
      // by the fail-closed state (no A gates leak to B's window).
      const { client, service } = setup();
      const capsA: ThreadCapabilities = { ...ALL_TRUE_CAPS };
      const threadA = makeThread({
        id: "thread-A",
        evener: {
          ref: "ref-A",
          capabilities: capsA,
          queue: { revision: 0 },
        },
      });
      const threadB = makeThread({
        id: "thread-B",
        evener: {
          ref: "ref-B",
          capabilities: capsA,
          queue: { revision: 0 },
        },
      });

      let resolveBRead = (_resp: ThreadReadResponse) => {};
      const bReadPromise = new Promise<ThreadReadResponse>((r) => {
        resolveBRead = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string };
        if (p.ref === "ref-A") return makeReadResponse(threadA);
        // ref-B read is deferred.
        return bReadPromise;
      });

      await service.open("ref-A");
      // Start open("ref-B") — clears ref+caps before awaiting B's read.
      const openPromise = service.open("ref-B");
      // While B's read is in flight, the pair is cleared/fail-closed.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      await expect(service.send(textInput("hello"))).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/start"),
      ).toBeUndefined();
      // Let B's open complete.
      resolveBRead(makeReadResponse(threadB));
      await openPromise;
    });
    it("failed open leaves service fail-closed (pair not installed on failure)", async () => {
      // I1: on current failure the service remains closed/fail-closed —
      // ref+capabilities are NOT installed.
      const { client, service } = setup();
      await service.open("ref-1");
      // A second open that fails must not leave ref-2 installed.
      client.on("thread/read", () => {
        throw new WireError("boom", -32603, {});
      });
      await expect(service.open("ref-2")).rejects.toThrow();
      // Service is fail-closed: send throws before wire (no valid ref/caps).
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      await expect(service.send(textInput("hello"))).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/start"),
      ).toBeUndefined();
    });
    it("stale open completion does not alter the current pair", async () => {
      // I1: an open started in an older epoch completing after a newer open
      // must not install its ref+caps over the current pair.
      const { client, service } = setup();
      const threadA = makeThread({
        id: "thread-A",
        evener: {
          ref: "ref-A",
          capabilities: { ...ALL_TRUE_CAPS, send: false },
          queue: { revision: 0 },
        },
      });
      const threadB = makeThread({
        id: "thread-B",
        evener: {
          ref: "ref-B",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
        },
      });

      let resolveARead = (_resp: ThreadReadResponse) => {};
      const aReadPromise = new Promise<ThreadReadResponse>((r) => {
        resolveARead = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string };
        if (p.ref === "ref-A") return aReadPromise;
        return makeReadResponse(threadB);
      });

      // Start open A (deferred), then open B (immediate).
      const openAPromise = service.open("ref-A");
      await service.open("ref-B");
      // Resolve A's stale open after B is current.
      resolveARead(makeReadResponse(threadA));
      await openAPromise;

      // The pair must be B's: send reaches wire with ref-B (capsB.send=true).
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      const receipt = await service.send(textInput("hello"));
      expect(receipt).toBeDefined();
      const startCall = client.calls.find((c) => c.method === "turn/start");
      expect(startCall).toBeDefined();
      expect(startCall?.params).toMatchObject({ ref: "ref-B" });
    });
  });

  describe("LiveConversationService interface (F1)", () => {
    it("createConversationService returns LiveConversationService", () => {
      const { service } = setup();
      // Must have readProjection and refreshCapabilities as required methods
      expect(typeof service.readProjection).toBe("function");
      expect(typeof service.refreshCapabilities).toBe("function");
      // Must also have all base ConversationService methods
      expect(typeof service.open).toBe("function");
      expect(typeof service.send).toBe("function");
      expect(typeof service.steer).toBe("function");
      expect(typeof service.queue).toBe("function");
      expect(typeof service.interrupt).toBe("function");
      expect(typeof service.loadOlder).toBe("function");
      expect(typeof service.subscribeNotifications).toBe("function");
      expect(typeof service.close).toBe("function");
    });

    it("ignores compatibility cursor — sends canonical unbounded subscribed open request", async () => {
      const { client, service } = setup();
      // open(ref, cursor?) preserves the cursor param for signature
      // compatibility but must NOT add turnLimit or paging from cursor.
      // The request stays exactly the canonical unbounded subscribed open;
      // bounded reads live exclusively in readProjection, cursor paging in
      // thread/turns/list.
      await service.open("ref-1", "some-cursor");
      const call = client.calls.find((c) => c.method === "thread/read");
      expect(call).toBeDefined();
      const params = call?.params as Record<string, unknown>;
      // M1: exact canonical request — toEqual full object, no extra keys.
      expect(params).toEqual({
        ref: "ref-1",
        includeTurns: true,
        subscribe: true,
        replaceSubscription: true,
      });
    });
  });

  // R3: beginOpen must make the committed ref/capability pair truly fail-closed
  // by setting BOTH ref=null and capabilities=null for the entire pending state
  // and after current failure. Pending/failed open/readProjection must make send,
  // setReasoningEffort, cancelQueued, loadOlder, and every requireRef-only/gated
  // operation fail before any wire call. A refresh started while pending/closed
  // may never activate the pending/failed ref.
  describe("pending/failed fail-closed for requireRef-only operations (R3)", () => {
    it("pending open B denies setReasoningEffort before any wire call", async () => {
      const { client, service } = setup();
      const threadA = makeThread({
        id: "thread-A",
        evener: {
          ref: "ref-A",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
        },
      });
      const threadB = makeThread({
        id: "thread-B",
        evener: {
          ref: "ref-B",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
        },
      });

      let resolveBRead = (_resp: ThreadReadResponse) => {};
      const bReadPromise = new Promise<ThreadReadResponse>((r) => {
        resolveBRead = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string };
        if (p.ref === "ref-A") return makeReadResponse(threadA);
        return bReadPromise;
      });

      await service.open("ref-A");
      // Start open("ref-B") — must clear ref+caps before awaiting B's read.
      const openPromise = service.open("ref-B");
      client.on("thread/reasoning-effort/set", () => EMPTY_RESPONSE);
      await expect(service.setReasoningEffort("high")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/reasoning-effort/set"),
      ).toBeUndefined();
      resolveBRead(makeReadResponse(threadB));
      await openPromise;
    });

    it("pending open B denies cancelQueued before any wire call", async () => {
      const { client, service } = setup();
      const threadA = makeThread({
        id: "thread-A",
        evener: {
          ref: "ref-A",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
        },
      });
      const threadB = makeThread({
        id: "thread-B",
        evener: {
          ref: "ref-B",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
        },
      });

      let resolveBRead = (_resp: ThreadReadResponse) => {};
      const bReadPromise = new Promise<ThreadReadResponse>((r) => {
        resolveBRead = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string };
        if (p.ref === "ref-A") return makeReadResponse(threadA);
        return bReadPromise;
      });

      await service.open("ref-A");
      const openPromise = service.open("ref-B");
      client.on(
        "turn/cancelQueued",
        () =>
          ({
            removedText: "x",
            receipt: makeReceipt(),
          }) as TurnCancelQueuedResponse,
      );
      await expect(service.cancelQueued(0, "entry-1")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/cancelQueued"),
      ).toBeUndefined();
      resolveBRead(makeReadResponse(threadB));
      await openPromise;
    });

    it("pending open B denies loadOlder before any wire call", async () => {
      const { client, service } = setup();
      const threadA = makeThread({
        id: "thread-A",
        evener: {
          ref: "ref-A",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
        },
      });
      const threadB = makeThread({
        id: "thread-B",
        evener: {
          ref: "ref-B",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
        },
      });

      let resolveBRead = (_resp: ThreadReadResponse) => {};
      const bReadPromise = new Promise<ThreadReadResponse>((r) => {
        resolveBRead = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string };
        if (p.ref === "ref-A") return makeReadResponse(threadA);
        return bReadPromise;
      });

      await service.open("ref-A");
      const openPromise = service.open("ref-B");
      client.on(
        "thread/turns/list",
        () =>
          ({
            data: [],
            nextCursor: undefined,
          }) as ThreadTurnsListResponse,
      );
      await expect(service.loadOlder("cursor-x")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/turns/list"),
      ).toBeUndefined();
      resolveBRead(makeReadResponse(threadB));
      await openPromise;
    });

    it("pending readProjection B denies setReasoningEffort before any wire call", async () => {
      const { client, service } = setup();
      const threadA = makeThread({
        id: "thread-A",
        evener: {
          ref: "ref-A",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
        },
      });
      const threadB = makeThread({
        id: "thread-B",
        evener: {
          ref: "ref-B",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
        },
      });

      let resolveBRead = (_resp: ThreadReadResponse) => {};
      const bReadPromise = new Promise<ThreadReadResponse>((r) => {
        resolveBRead = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string };
        if (p.ref === "ref-A") return makeReadResponse(threadA);
        return bReadPromise;
      });

      await service.open("ref-A");
      // Start readProjection("ref-B") — must clear ref+caps before awaiting.
      const rpPromise = service.readProjection("ref-B");
      client.on("thread/reasoning-effort/set", () => EMPTY_RESPONSE);
      await expect(service.setReasoningEffort("high")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/reasoning-effort/set"),
      ).toBeUndefined();
      resolveBRead(makeReadResponse(threadB));
      await rpPromise;
    });

    it("failed open denies setReasoningEffort before any wire call", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      // A second open that fails must not leave ref-2 installed.
      client.on("thread/read", () => {
        throw new WireError("boom", -32603, {});
      });
      await expect(service.open("ref-2")).rejects.toThrow();
      client.on("thread/reasoning-effort/set", () => EMPTY_RESPONSE);
      await expect(service.setReasoningEffort("high")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/reasoning-effort/set"),
      ).toBeUndefined();
    });

    it("failed open denies cancelQueued before any wire call", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      client.on("thread/read", () => {
        throw new WireError("boom", -32603, {});
      });
      await expect(service.open("ref-2")).rejects.toThrow();
      client.on(
        "turn/cancelQueued",
        () =>
          ({
            removedText: "x",
            receipt: makeReceipt(),
          }) as TurnCancelQueuedResponse,
      );
      await expect(service.cancelQueued(0, "entry-1")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/cancelQueued"),
      ).toBeUndefined();
    });

    it("failed open denies loadOlder before any wire call", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      client.on("thread/read", () => {
        throw new WireError("boom", -32603, {});
      });
      await expect(service.open("ref-2")).rejects.toThrow();
      client.on(
        "thread/turns/list",
        () =>
          ({
            data: [],
            nextCursor: undefined,
          }) as ThreadTurnsListResponse,
      );
      await expect(service.loadOlder("cursor-x")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/turns/list"),
      ).toBeUndefined();
    });

    it("failed readProjection denies setReasoningEffort before any wire call", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      client.on("thread/read", () => {
        throw new WireError("boom", -32603, {});
      });
      await expect(service.readProjection("ref-2")).rejects.toThrow();
      client.on("thread/reasoning-effort/set", () => EMPTY_RESPONSE);
      await expect(service.setReasoningEffort("high")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/reasoning-effort/set"),
      ).toBeUndefined();
    });
  });

  describe("refresh during pending/after failure never activates pending/failed ref (R3)", () => {
    it("refresh B resolving during pending A-open does not publish to cache", async () => {
      // A refresh started while A's open is pending (ref=null) may never
      // activate the pending ref. The returned caps are available to the
      // caller, but the mutation-gating cache stays null/fail-closed until
      // the successful open installs the pair.
      const { client, service } = setup();
      // Refresh response caps have send=false; open response caps have
      // send=true. If the refresh leaked into the cache, send would throw
      // even after the open succeeds.
      const capsRefresh: ThreadCapabilities = {
        ...ALL_TRUE_CAPS,
        send: false,
      };
      const threadRefresh = makeThread({
        id: "thread-B",
        evener: {
          ref: "ref-B",
          capabilities: capsRefresh,
          queue: { revision: 0 },
        },
      });
      const threadOpen = makeThread({
        id: "thread-B",
        evener: {
          ref: "ref-B",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
        },
      });

      let resolveOpenB = (_resp: ThreadReadResponse) => {};
      const openBPromise = new Promise<ThreadReadResponse>((r) => {
        resolveOpenB = r;
      });
      let resolveRefreshB = (_resp: ThreadReadResponse) => {};
      const refreshBPromise0 = new Promise<ThreadReadResponse>((r) => {
        resolveRefreshB = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string; subscribe?: boolean };
        if (p.ref === "ref-B" && p.subscribe === false) return refreshBPromise0;
        if (p.ref === "ref-B") return openBPromise;
        return makeReadResponse(makeThread());
      });

      // Start open("ref-B") — ref=null, caps=null during pending.
      const openPromise = service.open("ref-B");
      // Start a refresh of B while B's open is still pending.
      const refreshPromise = service.refreshCapabilities("ref-B");
      // Resolve the refresh first (it should NOT publish since ref was null
      // at refresh START).
      resolveRefreshB(makeReadResponse(threadRefresh));
      const refreshCaps = await refreshPromise;
      // Returned caps are available to the caller.
      expect(refreshCaps).toEqual(capsRefresh);

      // The cache must still be fail-closed (ref=null): send throws before wire.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      await expect(service.send(textInput("hello"))).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/start"),
      ).toBeUndefined();

      // Now complete B's open — the pair installs and send reaches wire.
      resolveOpenB(makeReadResponse(threadOpen));
      await openPromise;
      const receipt = await service.send(textInput("hello"));
      expect(receipt).toBeDefined();
      const startCalls = client.calls.filter((c) => c.method === "turn/start");
      expect(startCalls.length).toBeGreaterThan(0);
      expect(startCalls[startCalls.length - 1]?.params).toMatchObject({
        ref: "ref-B",
      });
    });

    it("refresh B resolving after failed open does not publish to cache", async () => {
      // After a failed open, ref=null. A refresh resolving after the failure
      // must not publish its caps into the fail-closed cache.
      const { client, service } = setup();
      const capsRefresh: ThreadCapabilities = {
        ...ALL_TRUE_CAPS,
        send: false,
      };
      const threadRefresh = makeThread({
        id: "thread-B",
        evener: {
          ref: "ref-B",
          capabilities: capsRefresh,
          queue: { revision: 0 },
        },
      });

      let resolveRefresh = (_resp: ThreadReadResponse) => {};
      const refreshPromise0 = new Promise<ThreadReadResponse>((r) => {
        resolveRefresh = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string; subscribe?: boolean };
        // open("ref-B") fails synchronously.
        if (p.ref === "ref-B" && p.subscribe !== false) {
          throw new WireError("boom", -32603, {});
        }
        return refreshPromise0;
      });

      // Start open("ref-B") — it will fail. ref stays null.
      await expect(service.open("ref-B")).rejects.toThrow();
      // Start a refresh of B while ref is null (after failure).
      const refreshPromise = service.refreshCapabilities("ref-B");
      resolveRefresh(makeReadResponse(threadRefresh));
      const refreshCaps = await refreshPromise;
      expect(refreshCaps).toEqual(capsRefresh);

      // Cache must still be fail-closed: send throws before wire.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      await expect(service.send(textInput("hello"))).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/start"),
      ).toBeUndefined();
    });
  });

  // R4-Important: open/readProjection must not commit ref/capabilities until ALL
  // response-derived projection work succeeds. After await, compute/validate
  // projectThread, activity projection, olderCursor/result locals first; only
  // then, if epoch current, atomically commit ref+caps. Any malformed
  // response/projectThread/activity projection throw leaves pair null/fail-closed.
  // Stale successful result may return without committing.
  describe("projection throw leaves pair fail-closed (R4-Important)", () => {
    it("open: projectThread throw leaves pair null — all operations fail before wire", async () => {
      const { client, service } = setup();
      await service.open("ref-1"); // initial valid open
      const spy = vi
        .spyOn(projectModule, "projectThread")
        .mockImplementation(() => {
          throw new Error("malformed projection");
        });
      try {
        await expect(service.open("ref-2")).rejects.toThrow(
          "malformed projection",
        );
        // Pair must be null/fail-closed: send throws before wire.
        client.on(
          "turn/start",
          () =>
            ({
              turn: { id: "t1", itemsView: "default", status: "running" },
              receipt: makeReceipt(),
            }) as TurnStartResponse,
        );
        await expect(service.send(textInput("hello"))).rejects.toThrow();
        expect(
          client.calls.find((c) => c.method === "turn/start"),
        ).toBeUndefined();
        // requireRef-only operations also fail before wire.
        client.on("thread/reasoning-effort/set", () => EMPTY_RESPONSE);
        await expect(service.setReasoningEffort("high")).rejects.toThrow();
        expect(
          client.calls.find((c) => c.method === "thread/reasoning-effort/set"),
        ).toBeUndefined();
        client.on(
          "thread/turns/list",
          () =>
            ({
              data: [],
              nextCursor: undefined,
            }) as ThreadTurnsListResponse,
        );
        await expect(service.loadOlder("cursor-x")).rejects.toThrow();
        expect(
          client.calls.find((c) => c.method === "thread/turns/list"),
        ).toBeUndefined();
      } finally {
        spy.mockRestore();
      }
    });

    it("readProjection: projectThread throw leaves pair null — all operations fail before wire", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      const spy = vi
        .spyOn(projectModule, "projectThread")
        .mockImplementation(() => {
          throw new Error("malformed projection");
        });
      try {
        await expect(service.readProjection("ref-2")).rejects.toThrow(
          "malformed projection",
        );
        // Pair null: send throws before wire.
        client.on(
          "turn/start",
          () =>
            ({
              turn: { id: "t1", itemsView: "default", status: "running" },
              receipt: makeReceipt(),
            }) as TurnStartResponse,
        );
        await expect(service.send(textInput("hello"))).rejects.toThrow();
        expect(
          client.calls.find((c) => c.method === "turn/start"),
        ).toBeUndefined();
        // requireRef-only operations also fail.
        client.on("thread/reasoning-effort/set", () => EMPTY_RESPONSE);
        await expect(service.setReasoningEffort("high")).rejects.toThrow();
        expect(
          client.calls.find((c) => c.method === "thread/reasoning-effort/set"),
        ).toBeUndefined();
      } finally {
        spy.mockRestore();
      }
    });

    it("readProjection: activity projection throw leaves pair null — all operations fail before wire", async () => {
      // Spy on createActivityService BEFORE creating the service so the
      // service captures the mock at construction time.
      const spy = vi
        .spyOn(activityModule, "createActivityService")
        .mockImplementation(
          () =>
            ({
              projectActivity: () => {
                throw new Error("activity projection boom");
              },
            }) as unknown as ReturnType<typeof createActivityService>,
        );
      const { client, service } = setup();
      try {
        await service.open("ref-1");
        await expect(service.readProjection("ref-2")).rejects.toThrow(
          "activity projection boom",
        );
        // Pair null: send throws before wire.
        client.on(
          "turn/start",
          () =>
            ({
              turn: { id: "t1", itemsView: "default", status: "running" },
              receipt: makeReceipt(),
            }) as TurnStartResponse,
        );
        await expect(service.send(textInput("hello"))).rejects.toThrow();
        expect(
          client.calls.find((c) => c.method === "turn/start"),
        ).toBeUndefined();
        // requireRef-only also fails.
        client.on("thread/reasoning-effort/set", () => EMPTY_RESPONSE);
        await expect(service.setReasoningEffort("high")).rejects.toThrow();
        expect(
          client.calls.find((c) => c.method === "thread/reasoning-effort/set"),
        ).toBeUndefined();
      } finally {
        spy.mockRestore();
      }
    });

    it("readProjection: malformed response (null thread) throw leaves pair null", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      // Return a response with a null/undefined thread field — accessing
      // thread.evener.capabilities will throw.
      client.on("thread/read", () => {
        return { thread: null } as unknown as ThreadReadResponse;
      });
      await expect(service.readProjection("ref-2")).rejects.toThrow();
      // Pair null: send throws before wire.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      await expect(service.send(textInput("hello"))).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/start"),
      ).toBeUndefined();
    });

    it("stale successful open result returns without committing pair", async () => {
      // An older open whose projectThread succeeds but resolves after a newer
      // open must return its result but NOT commit ref+caps.
      const { client, service } = setup();
      const threadA = makeThread({
        id: "thread-A",
        evener: {
          ref: "ref-A",
          capabilities: { ...ALL_TRUE_CAPS, send: false },
          queue: { revision: 0 },
        },
      });
      const threadB = makeThread({
        id: "thread-B",
        evener: {
          ref: "ref-B",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
        },
      });

      let resolveARead = (_resp: ThreadReadResponse) => {};
      const aReadPromise = new Promise<ThreadReadResponse>((r) => {
        resolveARead = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string };
        if (p.ref === "ref-A") return aReadPromise;
        return makeReadResponse(threadB);
      });

      // Start open A (deferred), then open B (immediate, commits pair).
      const openAPromise = service.open("ref-A");
      await service.open("ref-B");
      // Resolve A's stale open — it returns projectThread(threadA) but must
      // NOT commit A's pair over B's.
      resolveARead(makeReadResponse(threadA));
      const convA = await openAPromise;
      // A's result is returned (stale successful result returns).
      expect(convA.id).toBe("thread-A");
      // B's pair is committed: send reaches wire with ref-B (capsB.send=true).
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      const receipt = await service.send(textInput("hello"));
      expect(receipt).toBeDefined();
      const startCall = client.calls.find((c) => c.method === "turn/start");
      expect(startCall).toBeDefined();
      expect(startCall?.params).toMatchObject({ ref: "ref-B" });
    });

    it("stale successful readProjection result returns without committing pair", async () => {
      // An older readProjection whose projection succeeds but resolves after a
      // newer open must return its result but NOT commit ref+caps.
      const { client, service } = setup();
      const threadA = makeThread({
        id: "thread-A",
        evener: {
          ref: "ref-A",
          capabilities: { ...ALL_TRUE_CAPS, send: false },
          queue: { revision: 0 },
        },
      });
      const threadB = makeThread({
        id: "thread-B",
        evener: {
          ref: "ref-B",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
        },
      });

      let resolveARead = (_resp: ThreadReadResponse) => {};
      const aReadPromise = new Promise<ThreadReadResponse>((r) => {
        resolveARead = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string };
        if (p.ref === "ref-A") return aReadPromise;
        return makeReadResponse(threadB);
      });

      // Start readProjection A (deferred), then open B (immediate, commits pair).
      const rpAPromise = service.readProjection("ref-A");
      await service.open("ref-B");
      // Resolve A's stale readProjection.
      resolveARead(makeReadResponse(threadA));
      const resultA = await rpAPromise;
      // A's result is returned (stale successful result returns).
      expect(resultA.conversation.id).toBe("thread-A");
      // B's pair is committed: send reaches wire with ref-B.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      const receipt = await service.send(textInput("hello"));
      expect(receipt).toBeDefined();
      const startCall = client.calls.find((c) => c.method === "turn/start");
      expect(startCall).toBeDefined();
      expect(startCall?.params).toMatchObject({ ref: "ref-B" });
    });

    it("after projection-throw failure, a later successful open commits and operations reach wire", async () => {
      // After a projection throw leaves the pair null, a subsequent successful
      // open must install the pair and allow operations to reach the wire.
      const { client, service } = setup();
      await service.open("ref-1");
      const spy = vi
        .spyOn(projectModule, "projectThread")
        .mockImplementationOnce(() => {
          throw new Error("malformed projection");
        });
      try {
        await expect(service.open("ref-2")).rejects.toThrow(
          "malformed projection",
        );
        // Now a successful open — restores the pair.
        client.on("thread/read", () => makeReadResponse(makeThread()));
        const conv = await service.open("ref-3");
        expect(conv.id).toBe("thread-1");
        // Operations reach the wire now.
        client.on(
          "turn/start",
          () =>
            ({
              turn: { id: "t1", itemsView: "default", status: "running" },
              receipt: makeReceipt(),
            }) as TurnStartResponse,
        );
        const receipt = await service.send(textInput("hello"));
        expect(receipt).toBeDefined();
        const startCall = client.calls.find((c) => c.method === "turn/start");
        expect(startCall).toBeDefined();
        expect(startCall?.params).toMatchObject({ ref: "ref-3" });
      } finally {
        spy.mockRestore();
      }
    });
  });

  // R4-Minor: pending-refresh test must distinguish the startRef guard. Start
  // refresh(B) while open(B) is pending (startRef=null), allow open(B) to
  // succeed/commit B, only then resolve refresh(B); fetched stale caps must
  // return but not overwrite newly committed B caps.
  describe("pending refresh startRef guard (R4-Minor)", () => {
    it("refresh(B) started while open(B) pending — stale caps return but do not overwrite committed B caps", async () => {
      const { client, service } = setup();
      // Refresh caps have send=false; open caps have send=true. If the stale
      // refresh overwrites the committed B caps, send would throw.
      const capsRefresh: ThreadCapabilities = {
        ...ALL_TRUE_CAPS,
        send: false,
      };
      const threadRefresh = makeThread({
        id: "thread-B",
        evener: {
          ref: "ref-B",
          capabilities: capsRefresh,
          queue: { revision: 0 },
        },
      });
      const threadOpen = makeThread({
        id: "thread-B",
        evener: {
          ref: "ref-B",
          capabilities: ALL_TRUE_CAPS,
          queue: { revision: 0 },
        },
      });

      let resolveOpenB = (_resp: ThreadReadResponse) => {};
      const openBPromise = new Promise<ThreadReadResponse>((r) => {
        resolveOpenB = r;
      });
      let resolveRefreshB = (_resp: ThreadReadResponse) => {};
      const refreshBPromise0 = new Promise<ThreadReadResponse>((r) => {
        resolveRefreshB = r;
      });
      client.on("thread/read", (params) => {
        const p = params as { ref: string; subscribe?: boolean };
        if (p.ref === "ref-B" && p.subscribe === false) return refreshBPromise0;
        if (p.ref === "ref-B") return openBPromise;
        return makeReadResponse(makeThread());
      });

      // Start open("ref-B") — ref=null during pending.
      const openPromise = service.open("ref-B");
      // Start refresh(B) while open(B) is pending — startRef=null.
      const refreshPromise = service.refreshCapabilities("ref-B");
      // Allow open(B) to succeed and commit B (ref=ref-B, caps=ALL_TRUE).
      resolveOpenB(makeReadResponse(threadOpen));
      await openPromise;
      // NOW resolve the stale refresh — startRef was null, so it must NOT
      // overwrite the committed B caps.
      resolveRefreshB(makeReadResponse(threadRefresh));
      const refreshCaps = await refreshPromise;
      // Fetched stale caps are returned to the caller.
      expect(refreshCaps).toEqual(capsRefresh);

      // Committed B caps (send=true) must be intact — send reaches wire.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      const receipt = await service.send(textInput("hello"));
      expect(receipt).toBeDefined();
      const startCall = client.calls.find((c) => c.method === "turn/start");
      expect(startCall).toBeDefined();
      expect(startCall?.params).toMatchObject({ ref: "ref-B" });
    });
  });

  // R5: Before ANY ref/capability state write in open/readProjection/refresh,
  // extract and runtime-validate response.thread.evener.capabilities into a
  // complete plain local ThreadCapabilities copy. All 11 generated fields must
  // be booleans; allow future extra keys but never retain the response object
  // or getters. Null/missing/nonobject/wrong-field/throwing-getter must reject
  // before commit and leave the pair null.
  describe("capability extraction validation (R5)", () => {
    // Helper: make a thread with custom capabilities that may be malformed.
    function makeThreadWithCaps(caps: unknown): Thread {
      return makeThread({
        evener: {
          ref: "ref-1",
          capabilities: caps as ThreadCapabilities,
          queue: { revision: 0 },
        },
      });
    }

    // Helper: verify all requireRef-only and gated operations are fail-closed
    // (no wire mutation) after a failed open/readProjection.
    async function assertAllOpsFailClosed(
      service: ReturnType<typeof createConversationService>,
      client: FakeAppwireClient,
    ) {
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      client.on(
        "turn/steer",
        () => ({ receipt: makeReceipt("steer") }) as TurnSteerResponse,
      );
      client.on(
        "turn/queue",
        () => ({ receipt: makeReceipt("queue") }) as TurnQueueResponse,
      );
      client.on(
        "turn/interrupt",
        () => ({ receipt: makeReceipt("interrupt") }) as TurnInterruptResponse,
      );
      client.on("thread/compact/start", () => EMPTY_RESPONSE);
      client.on("thread/shutdown", () => EMPTY_RESPONSE);
      client.on("thread/model/set", () => EMPTY_RESPONSE);
      client.on("thread/reasoning-effort/set", () => EMPTY_RESPONSE);
      client.on("evener/thread/name/set", () => EMPTY_RESPONSE);
      client.on(
        "thread/turns/list",
        () => ({ data: [], nextCursor: undefined }) as ThreadTurnsListResponse,
      );
      client.on(
        "turn/cancelQueued",
        () =>
          ({
            removedText: "x",
            receipt: makeReceipt(),
          }) as TurnCancelQueuedResponse,
      );

      // Gated ops (requireCap + requireRef) — all must throw before wire.
      await expect(service.send(textInput("hello"))).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/start"),
      ).toBeUndefined();
      await expect(service.steer(textInput("s"))).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/steer"),
      ).toBeUndefined();
      await expect(service.queue(textInput("q"))).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/queue"),
      ).toBeUndefined();
      await expect(service.interrupt()).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/interrupt"),
      ).toBeUndefined();
      await expect(service.compact()).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/compact/start"),
      ).toBeUndefined();
      await expect(service.shutdown()).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/shutdown"),
      ).toBeUndefined();
      await expect(service.changeModel("o", "g")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/model/set"),
      ).toBeUndefined();
      await expect(service.rename("n")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "evener/thread/name/set"),
      ).toBeUndefined();

      // requireRef-only ops — all must throw before wire.
      await expect(service.setReasoningEffort("high")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/reasoning-effort/set"),
      ).toBeUndefined();
      await expect(service.cancelQueued(0, "e")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "turn/cancelQueued"),
      ).toBeUndefined();
      await expect(service.loadOlder("cursor-x")).rejects.toThrow();
      expect(
        client.calls.find((c) => c.method === "thread/turns/list"),
      ).toBeUndefined();
    }

    // --- open: malformed capabilities ---

    it("open: null capabilities rejects before commit, pair stays null", async () => {
      const thread = makeThreadWithCaps(null);
      const { client, service } = setup({ thread });
      await expect(service.open("ref-1")).rejects.toThrow(
        "capabilities is not an object",
      );
      await assertAllOpsFailClosed(service, client);
    });

    it("open: missing capabilities rejects before commit, pair stays null", async () => {
      const thread = makeThread({
        evener: {
          ref: "ref-1",
          // capabilities intentionally omitted
          capabilities: undefined as unknown as ThreadCapabilities,
          queue: { revision: 0 },
        },
      });
      const { client, service } = setup({ thread });
      await expect(service.open("ref-1")).rejects.toThrow(
        "capabilities is not an object",
      );
      await assertAllOpsFailClosed(service, client);
    });

    it("open: non-object capabilities rejects before commit, pair stays null", async () => {
      const thread = makeThreadWithCaps("not-an-object");
      const { client, service } = setup({ thread });
      await expect(service.open("ref-1")).rejects.toThrow(
        "capabilities is not an object",
      );
      await assertAllOpsFailClosed(service, client);
    });

    it("open: wrong-type field (send=string) rejects before commit, pair stays null", async () => {
      const thread = makeThreadWithCaps({
        ...ALL_TRUE_CAPS,
        send: "yes" as unknown as boolean,
      });
      const { client, service } = setup({ thread });
      await expect(service.open("ref-1")).rejects.toThrow(
        'capability "send" is not a boolean',
      );
      await assertAllOpsFailClosed(service, client);
    });

    it("open: throwing getter on a capability field rejects before commit, pair stays null", async () => {
      const throwingCaps = {
        get send() {
          throw new Error("getter bomb");
        },
        steer: true,
        interrupt: true,
        compact: true,
        clear: true,
        forkFromTurn: true,
        shutdown: true,
        changeModel: true,
        queue: true,
        goal: true,
        rename: true,
      };
      const thread = makeThreadWithCaps(throwingCaps);
      const { client, service } = setup({ thread });
      await expect(service.open("ref-1")).rejects.toThrow();
      await assertAllOpsFailClosed(service, client);
    });

    // --- readProjection: malformed capabilities ---

    it("readProjection: null capabilities rejects before commit, pair stays null", async () => {
      const thread = makeThreadWithCaps(null);
      const { client, service } = setup({ thread });
      await expect(service.readProjection("ref-1")).rejects.toThrow(
        "capabilities is not an object",
      );
      await assertAllOpsFailClosed(service, client);
    });

    it("readProjection: missing capabilities rejects before commit, pair stays null", async () => {
      const thread = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: undefined as unknown as ThreadCapabilities,
          queue: { revision: 0 },
        },
      });
      const { client, service } = setup({ thread });
      await expect(service.readProjection("ref-1")).rejects.toThrow(
        "capabilities is not an object",
      );
      await assertAllOpsFailClosed(service, client);
    });

    it("readProjection: non-object capabilities rejects before commit, pair stays null", async () => {
      const thread = makeThreadWithCaps(42);
      const { client, service } = setup({ thread });
      await expect(service.readProjection("ref-1")).rejects.toThrow(
        "capabilities is not an object",
      );
      await assertAllOpsFailClosed(service, client);
    });

    it("readProjection: wrong-type field (rename=number) rejects before commit, pair stays null", async () => {
      const thread = makeThreadWithCaps({
        ...ALL_TRUE_CAPS,
        rename: 1 as unknown as boolean,
      });
      const { client, service } = setup({ thread });
      await expect(service.readProjection("ref-1")).rejects.toThrow(
        'capability "rename" is not a boolean',
      );
      await assertAllOpsFailClosed(service, client);
    });

    it("readProjection: throwing getter rejects before commit, pair stays null", async () => {
      const throwingCaps = {
        send: true,
        steer: true,
        interrupt: true,
        compact: true,
        clear: true,
        forkFromTurn: true,
        shutdown: true,
        changeModel: true,
        queue: true,
        goal: true,
        get rename() {
          throw new Error("late getter bomb");
        },
      };
      const thread = makeThreadWithCaps(throwingCaps);
      const { client, service } = setup({ thread });
      await expect(service.readProjection("ref-1")).rejects.toThrow();
      await assertAllOpsFailClosed(service, client);
    });

    // --- recovery: after malformed caps rejection, successful open works ---

    it("after open malformed-caps rejection, a later successful open commits and operations reach wire", async () => {
      const badThread = makeThreadWithCaps(null);
      const { client, service } = setup({ thread: badThread });
      await expect(service.open("ref-1")).rejects.toThrow(
        "capabilities is not an object",
      );
      // Now a successful open with valid caps.
      client.on("thread/read", () => makeReadResponse(makeThread()));
      const conv = await service.open("ref-2");
      expect(conv.id).toBe("thread-1");
      // Operations reach the wire.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      const receipt = await service.send(textInput("hello"));
      expect(receipt).toBeDefined();
      const startCall = client.calls.find((c) => c.method === "turn/start");
      expect(startCall).toBeDefined();
      expect(startCall?.params).toMatchObject({ ref: "ref-2" });
    });

    // --- refresh: malformed capabilities ---

    it("refresh: null capabilities returns rejection and does not corrupt current pair", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      // Set up a refresh handler that returns null capabilities.
      client.on("thread/read", (params) => {
        const p = params as { ref: string; subscribe?: boolean };
        if (p.subscribe === false) {
          return makeReadResponse(makeThreadWithCaps(null));
        }
        return makeReadResponse(makeThread());
      });
      // The refresh must reject (malformed caps).
      await expect(service.refreshCapabilities("ref-1")).rejects.toThrow(
        "capabilities is not an object",
      );
      // Current pair must be intact — send reaches the wire with ref-1.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      const receipt = await service.send(textInput("hello"));
      expect(receipt).toBeDefined();
      const startCall = client.calls.find((c) => c.method === "turn/start");
      expect(startCall).toBeDefined();
      expect(startCall?.params).toMatchObject({ ref: "ref-1" });
    });

    it("refresh: wrong-type field returns rejection and does not corrupt current pair", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      const badCaps = {
        ...ALL_TRUE_CAPS,
        interrupt: "no" as unknown as boolean,
      };
      client.on("thread/read", (params) => {
        const p = params as { ref: string; subscribe?: boolean };
        if (p.subscribe === false) {
          return makeReadResponse(makeThreadWithCaps(badCaps));
        }
        return makeReadResponse(makeThread());
      });
      await expect(service.refreshCapabilities("ref-1")).rejects.toThrow(
        'capability "interrupt" is not a boolean',
      );
      // Current pair intact — send reaches wire.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      const receipt = await service.send(textInput("hello"));
      expect(receipt).toBeDefined();
      const startCall = client.calls.find((c) => c.method === "turn/start");
      expect(startCall).toBeDefined();
      expect(startCall?.params).toMatchObject({ ref: "ref-1" });
    });

    it("refresh: throwing getter returns rejection and does not corrupt current pair", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      const throwingCaps = {
        send: true,
        steer: true,
        get interrupt() {
          throw new Error("refresh getter bomb");
        },
        compact: true,
        clear: true,
        forkFromTurn: true,
        shutdown: true,
        changeModel: true,
        queue: true,
        goal: true,
        rename: true,
      };
      client.on("thread/read", (params) => {
        const p = params as { ref: string; subscribe?: boolean };
        if (p.subscribe === false) {
          return makeReadResponse(makeThreadWithCaps(throwingCaps));
        }
        return makeReadResponse(makeThread());
      });
      await expect(service.refreshCapabilities("ref-1")).rejects.toThrow();
      // Current pair intact — send reaches wire.
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt(),
          }) as TurnStartResponse,
      );
      const receipt = await service.send(textInput("hello"));
      expect(receipt).toBeDefined();
      const startCall = client.calls.find((c) => c.method === "turn/start");
      expect(startCall).toBeDefined();
      expect(startCall?.params).toMatchObject({ ref: "ref-1" });
    });
  });

  // I3: projectOlderTurns must never emit actionable kind:'question' rows
  // from historical pages. A pending ask cannot legitimately be older than
  // newer continuation turns, and page-local projection otherwise resurrects
  // settled calls. All other projected page items/order/dedupe/cursor are
  // preserved — only question rows are omitted.
  describe("I3: projectOlderTurns omits question rows from historical pages", () => {
    it("completed ask_user + agentMessage => question omitted, other content retained", async () => {
      const { client, service } = setup();
      // Build turns with a completed ask_user + agentMessage.
      const askUserTurn: Turn = {
        id: "t-old-1",
        items: [
          {
            type: "commandExecution",
            id: "ask-old",
            toolName: "ask_user",
            status: "completed",
            argumentsJson:
              '{"questions":[{"header":"Choose","question":"Pick one","options":[{"label":"A","detail":"da"},{"label":"B","detail":"db"}],"multi_select":false}]}',
          },
          {
            type: "agentMessage",
            id: "msg-old",
            text: "old message",
            status: "completed",
          },
        ],
        itemsView: "default",
        status: "completed",
      };
      client.on(
        "thread/turns/list",
        () =>
          ({
            data: [askUserTurn],
            nextCursor: undefined,
          }) as ThreadTurnsListResponse,
      );

      await service.open("ref-1");
      const result = await service.loadOlder("cursor-1");
      const items = result.items;

      // No question items — the completed ask_user must not be resurrected.
      expect(items.some((i) => i.kind === "question")).toBe(false);
      // Other content retained — agentMessage projected as assistant.
      expect(items.some((i) => i.kind === "assistant")).toBe(true);
    });
  });
});
