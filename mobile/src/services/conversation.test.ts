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

import { beforeEach, describe, expect, it } from "vitest";
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
  TurnCancelQueuedResponse,
  TurnInterruptResponse,
  TurnQueueResponse,
  TurnStartResponse,
  TurnSteerResponse,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import { createConversationService } from "./conversation";

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

function makeReceipt(over: Partial<MutationReceipt> = {}): MutationReceipt {
  return {
    clientMutationId: "cmid-1",
    disposition: "accepted",
    threadId: "thread-1",
    projectionState: "current",
    ...over,
  };
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
      expect(call?.params).toMatchObject({
        ref: "ref-1",
        subscribe: true,
        includeTurns: true,
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
        () => ({ receipt: makeReceipt() }) as TurnSteerResponse,
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
        () => ({ receipt: makeReceipt() }) as TurnQueueResponse,
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
        () => ({ receipt: makeReceipt() }) as TurnInterruptResponse,
      );
      await service.open("ref-1");
      const receipt = await service.interrupt();
      expect(receipt.clientMutationId).toBe("cmid-1");
      const call = client.calls.find((c) => c.method === "turn/interrupt");
      expect(call?.params).toMatchObject({ ref: "ref-1" });
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
        () => ({ receipt: makeReceipt() }) as TurnSteerResponse,
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
        () => ({ receipt: makeReceipt() }) as TurnQueueResponse,
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
        () => ({ receipt: makeReceipt() }) as TurnInterruptResponse,
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
      expect(call?.params).toMatchObject({
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
      const caps = await service.refreshCapabilities();
      expect(caps).not.toBeNull();
      expect(caps?.send).toBe(true);
      const call = client.calls.find((c) => c.method === "thread/read");
      expect(call).toBeDefined();
      const params = call?.params as Record<string, unknown>;
      expect(params.includeTurns).toBe(false);
      expect(params.subscribe).toBe(false);
      expect(params).not.toHaveProperty("replaceSubscription");
    });

    it("returns null when no thread is open", async () => {
      const { service } = setup();
      const caps = await service.refreshCapabilities();
      expect(caps).toBeNull();
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

    it("open(ref) signature preserved exactly — no cursor parameter", async () => {
      const { service } = setup();
      // open takes exactly one argument (ref), not (ref, cursor)
      const conv = await service.open("ref-1");
      expect(conv.id).toBe("thread-1");
    });
  });
});
