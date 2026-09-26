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

import { readFileSync } from "node:fs";
import path from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WireError } from "@evener/appwire-client";
import type {
  AnyNotification,
  EmptyResponse,
  InputItem,
  MethodName,
  MethodTypes,
  MutationReceipt,
  Thread,
  ThreadCapabilities,
  ThreadItem,
  ThreadReadResponse,
  ThreadTurnsListResponse,
  Turn,
  TurnCancelQueuedResponse,
  TurnDrainAsSteerResponse,
  TurnInterruptResponse,
  TurnQueueResponse,
  TurnStartResponse,
  TurnSteerResponse,
} from "@evener/appwire-client";
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
  forceStopCalls: string[] = [];
  resumeThreadCalls: string[] = [];
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

  forceStop(ref: string): Promise<void> {
    this.forceStopCalls.push(ref);
    return Promise.resolve();
  }

  resumeThread(ref: string): Promise<{ thread: Thread }> {
    this.resumeThreadCalls.push(ref);
    return Promise.resolve({ thread: makeThread() });
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
  changeVisionModel: true,
  sharedNotes: true,
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
    disposition: "applied",
    threadId: "thread-1",
    projectionState: kind === "interrupt" ? "reflected" : "pending",
  };
  if (kind === "send" || kind === "steer" || kind === "interrupt")
    receipt.turnId = "turn-1";
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

describe("goal actions", () => {
  it.each([null, undefined, {}, [], { started: "true" }])(
    "rejects a malformed goal acknowledgment without replay: %j",
    async (response) => {
      const { client, service } = setup();
      client.on("goal/set", () => response as never);
      await service.open("local:test");
      await expect(service.setGoal("objective-sentinel")).rejects.toThrow();
      expect(
        client.calls.filter((call) => call.method === "goal/set"),
      ).toHaveLength(1);
      service.close();
    },
  );
  it.each([true, false])(
    "accepts a boolean goal-start acknowledgment: %s",
    async (started) => {
      const { client, service } = setup();
      client.on("goal/set", () => ({ started }));
      await service.open("local:test");
      await expect(
        service.setGoal("objective-sentinel"),
      ).resolves.toBeUndefined();
      service.close();
    },
  );
  it("sets and clears the current session goal through the wire", async () => {
    const { client, service } = setup();
    client.on("goal/set", () => ({ started: false }));
    await service.open("local:test");
    await service.setGoal("objective-sentinel");
    await service.setGoal("");
    expect(client.calls.filter((call) => call.method === "goal/set")).toEqual([
      {
        method: "goal/set",
        params: { ref: "local:test", objective: "objective-sentinel" },
      },
      { method: "goal/set", params: { ref: "local:test", objective: "" } },
    ]);
  });
});

describe("canonical Go mutation receipt schema guard", () => {
  it("binds the decoder literals and optional fields to appwire/types.go", () => {
    const source = readFileSync(
      path.resolve(process.cwd(), "../appwire/types.go"),
      "utf8",
    );
    expect(source).toContain(
      'MutationDispositionApplied  MutationDisposition = "applied"',
    );
    expect(source).toContain(
      'MutationDispositionReplayed MutationDisposition = "replayed"',
    );
    expect(source).not.toMatch(
      /MutationDisposition\w*\s+MutationDisposition = "accepted"/,
    );
    for (const field of [
      'ClientMutationID string                  `json:"clientMutationId"`',
      'Disposition      MutationDisposition     `json:"disposition"`',
      'ThreadID         string                  `json:"threadId"`',
      'TurnID           string                  `json:"turnId,omitempty"`',
      'QueueEntryIDs    []string                `json:"queueEntryIds,omitempty"`',
      'ProjectionState  MutationProjectionState `json:"projectionState"`',
    ]) {
      expect(source).toContain(field);
    }
  });
});

function textInput(text: string): InputItem[] {
  return [{ type: "text", text }];
}

// --- service tests ------------------------------------------------------------

describe("ConversationService", () => {
  it("uses the dedicated force-stop client operation for recovery", async () => {
    const client = new FakeAppwireClient();
    client.on("thread/read", async () => ({ thread: makeThread() }));
    const service = createConversationService(client);
    await service.open("local:session-1");

    await service.forceStop();

    expect(client.forceStopCalls).toEqual(["local:session-1"]);
    expect(client.calls.map(({ method }) => method)).toEqual(["thread/read"]);
  });

  it("uses the dedicated resume client operation for recovery", async () => {
    const client = new FakeAppwireClient();
    client.on("thread/read", async () => ({ thread: makeThread() }));
    const service = createConversationService(client);
    await service.open("local:session-1");

    await service.resume();

    expect(client.resumeThreadCalls).toEqual(["local:session-1"]);
    expect(client.calls.map(({ method }) => method)).toEqual(["thread/read"]);
  });

  beforeEach(() => {
    idCounter = 0;
  });

  describe("open", () => {
    it("projects thread to MobileConversation", async () => {
      const { service } = setup();
      const conv = await service.open("ref-1");
      expect(conv.threadId).toBe("thread-1");
      expect(conv.status).toEqual({ type: "ready" });
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
      expect(result.turnsPage).toEqual({ data: [], nextCursor: undefined });
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
      expect(conv.threadId).toBe("thread-2");
    });
  });

  describe("loadOlder", () => {
    // D18 B3 round 3: the older page's own turns (carrying usage) must reach
    // the caller alongside items, so the store can keep conversation.turns in
    // sync with the store's own paging cursor.
    it("returns the older page's wire turns verbatim", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      const page: ThreadTurnsListResponse = {
        data: [
          {
            id: "turn-older",
            itemsView: "fragment",
            status: "completed",
            usage: { inputTokens: 60, outputTokens: 40 },
            items: [],
          },
        ],
        nextCursor: undefined,
      };
      client.on("thread/turns/list", () => page);
      const result = await service.loadOlder("cursor-1");
      expect(result.turnsPage).toEqual(page);
    });

    it("uses the caller-owned cursor after an ordinary completed projection", async () => {
      const { client, service } = setup({ olderCursor: "head-cursor-57" });
      await service.open("ref-1");
      client.on(
        "thread/turns/list",
        () =>
          ({
            data: [],
            nextCursor: "caller-cursor-43",
          }) as ThreadTurnsListResponse,
      );
      await service.readProjection("ref-1");
      await service.loadOlder("caller-cursor-43");
      const listCall = client.calls.find(
        (call) => call.method === "thread/turns/list",
      );
      expect(listCall?.params).toMatchObject({
        ref: "ref-1",
        cursor: "caller-cursor-43",
      });
    });

    it("waits for a same-ref projection read before paging", async () => {
      const { client, service, thread } = setup({
        olderCursor: "fresh-cursor",
      });
      await service.open("ref-1");
      let releaseRead!: (value: ThreadReadResponse) => void;
      const pendingRead = new Promise<ThreadReadResponse>((resolve) => {
        releaseRead = resolve;
      });
      client.on("thread/read", () => {
        return pendingRead;
      });
      client.on(
        "thread/turns/list",
        () => ({ data: [], nextCursor: undefined }) as ThreadTurnsListResponse,
      );

      const refresh = service.readProjection("ref-1");
      const page = service.loadOlder("cursor-from-before-refresh");
      await Promise.resolve();
      expect(
        client.calls.filter((call) => call.method === "thread/turns/list"),
      ).toHaveLength(0);
      releaseRead(makeReadResponse(thread, "fresh-cursor"));
      await refresh;
      await expect(page).resolves.toMatchObject({ turnsPage: { data: [] } });
      const listCall = client.calls.find(
        (call) => call.method === "thread/turns/list",
      );
      expect(listCall?.params).toMatchObject({
        ref: "ref-1",
        cursor: "cursor-from-before-refresh",
      });
    });

    it("paging waits for the host's read fence, not the raw RPC response", async () => {
      // The native host fences the projected publish (onReadComplete) around
      // the same raw read. Paging must wait for the whole publish: a page
      // seated once the raw response landed but before the fence settled would
      // be seated against a projection the publish had not installed.
      const client = new FakeAppwireClient();
      const thread = makeThread();
      client.on("thread/read", () => makeReadResponse(thread, "fresh-cursor"));
      client.on(
        "thread/turns/list",
        () => ({ data: [], nextCursor: undefined }) as ThreadTurnsListResponse,
      );
      let releaseFence!: () => void;
      const fence = new Promise<void>((resolve) => {
        releaseFence = resolve;
      });
      let fences = 0;
      const service = createConversationService(client, {
        onReadComplete: () => {
          fences += 1;
          // open()'s read is unfenced; the refresh's read is held at the fence.
          return fences === 1 ? undefined : fence;
        },
      });
      await service.open("ref-1");

      const refresh = service.readProjection("ref-1");
      const page = service.loadOlder("cursor-from-before-refresh");
      // Let the raw read resolve; the fence is still held.
      for (let i = 0; i < 5; i += 1) await Promise.resolve();
      expect(
        client.calls.filter((call) => call.method === "thread/turns/list"),
      ).toHaveLength(0);

      releaseFence();
      await refresh;
      await expect(page).resolves.toMatchObject({ turnsPage: { data: [] } });
      expect(
        client.calls.find((call) => call.method === "thread/turns/list")?.params,
      ).toMatchObject({ ref: "ref-1", cursor: "cursor-from-before-refresh" });
    });

    it("a rejected host read fence does not fail the projected read", async () => {
      const client = new FakeAppwireClient();
      const thread = makeThread();
      client.on("thread/read", () => makeReadResponse(thread, "cursor"));
      client.on(
        "thread/turns/list",
        () => ({ data: [], nextCursor: undefined }) as ThreadTurnsListResponse,
      );
      const service = createConversationService(client, {
        onReadComplete: () => Promise.reject(new Error("mutation storage down")),
      });
      await service.open("ref-1");
      // The authoritative read succeeded; the fence failure leaves the durable
      // dispatch gate blocked but must not fail the projection.
      await expect(service.readProjection("ref-1")).resolves.toMatchObject({
        olderCursor: "cursor",
      });
    });

    it("invokes the host read fence only after the projection commits", async () => {
      const client = new FakeAppwireClient();
      // A malformed capabilities payload makes projection validation throw
      // after the raw response, so the fence must never run.
      const broken = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: null,
          queue: { revision: 0 },
        } as unknown as Thread["evener"],
      });
      client.on("thread/read", () => makeReadResponse(makeThread(), "cursor"));
      client.on(
        "thread/turns/list",
        () => ({ data: [], nextCursor: undefined }) as ThreadTurnsListResponse,
      );
      let fences = 0;
      const service = createConversationService(client, {
        onReadComplete: () => {
          fences += 1;
        },
      });
      await service.open("ref-1");
      fences = 0;
      client.on("thread/read", () => makeReadResponse(broken, "cursor"));
      await expect(service.readProjection("ref-1")).rejects.toThrow();
      expect(fences).toBe(0);
    });

    it("does not page after the pending read is closed", async () => {
      const { client, service, thread } = setup({ olderCursor: "cursor-a" });
      await service.open("ref-1");
      let releaseRead!: (value: ThreadReadResponse) => void;
      const pendingRead = new Promise<ThreadReadResponse>((resolve) => {
        releaseRead = resolve;
      });
      client.on("thread/read", () => pendingRead);
      client.on(
        "thread/turns/list",
        () => ({ data: [] }) as ThreadTurnsListResponse,
      );
      const refresh = service.readProjection("ref-1");
      const page = service.loadOlder("cursor-a");
      service.close();
      releaseRead(makeReadResponse(thread, "cursor-a"));
      await refresh;
      await expect(page).rejects.toThrow("thread changed while paging");
      expect(
        client.calls.filter((call) => call.method === "thread/turns/list"),
      ).toHaveLength(0);
    });

    it("waits for the latest overlapping same-ref projection", async () => {
      const { client, service, thread } = setup({ olderCursor: "cursor-a" });
      await service.open("ref-1");
      let releaseFirst!: (value: ThreadReadResponse) => void;
      let releaseLatest!: (value: ThreadReadResponse) => void;
      const first = new Promise<ThreadReadResponse>((resolve) => {
        releaseFirst = resolve;
      });
      const latest = new Promise<ThreadReadResponse>((resolve) => {
        releaseLatest = resolve;
      });
      let reads = 0;
      client.on("thread/read", () => (++reads === 1 ? first : latest));
      client.on(
        "thread/turns/list",
        () => ({ data: [] }) as ThreadTurnsListResponse,
      );
      const refresh1 = service.readProjection("ref-1");
      const page = service.loadOlder("caller-cursor");
      const refresh2 = service.readProjection("ref-1");
      releaseFirst(makeReadResponse(thread, "stale-cursor"));
      await refresh1;
      await Promise.resolve();
      expect(
        client.calls.filter((call) => call.method === "thread/turns/list"),
      ).toHaveLength(0);
      releaseLatest(makeReadResponse(thread, "latest-cursor"));
      await refresh2;
      await expect(page).resolves.toMatchObject({ turnsPage: { data: [] } });
      expect(
        client.calls.find((call) => call.method === "thread/turns/list")
          ?.params,
      ).toMatchObject({ cursor: "caller-cursor", ref: "ref-1" });
    });

    it("does not page when a pending read changes ref", async () => {
      const { client, service, thread } = setup({ olderCursor: "cursor-a" });
      await service.open("ref-1");
      let release!: (value: ThreadReadResponse) => void;
      const pending = new Promise<ThreadReadResponse>((resolve) => {
        release = resolve;
      });
      client.on("thread/read", (params) =>
        params.ref === "ref-1"
          ? pending
          : makeReadResponse(
              {
                ...thread,
                id: "thread-b",
                evener: {
                  ...thread.evener,
                  ref: "ref-2",
                  instanceId: "instance-b",
                },
              },
              "cursor-b",
            ),
      );
      client.on(
        "thread/turns/list",
        () => ({ data: [] }) as ThreadTurnsListResponse,
      );
      const refresh = service.readProjection("ref-1");
      const page = service.loadOlder("cursor-a");
      const replacement = service.readProjection("ref-2");
      release(makeReadResponse(thread, "cursor-a"));
      await refresh;
      await replacement;
      await expect(page).rejects.toThrow("thread changed while paging");
      expect(
        client.calls.filter((call) => call.method === "thread/turns/list"),
      ).toHaveLength(0);
    });

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

    // D23d: the service hands the page's wire turns to the store verbatim —
    // the store's own merge folds a call and its result (the package's
    // across-turn merge, covered store-side) — so a page that carries a tool
    // call and its result as two items sharing a callId (in separate wire
    // turns, as apptranscript mints them) reaches the store exactly as the
    // wire served it.
    it("returns a page's call and result wire turns verbatim, for the store's merge to fold", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      const page: ThreadTurnsListResponse = {
        data: [
          {
            id: "turn-call",
            itemsView: "fragment",
            status: "completed",
            items: [
              {
                id: "item_tool_1",
                type: "commandExecution",
                toolName: "shell",
                callId: "call-1",
                argumentsJson: '{"command":"make"}',
                status: "inProgress",
                transcriptKey: "k:call",
                position: { entry: 1, item: 0 },
              },
            ] as ThreadItem[],
          },
          {
            id: "turn-result",
            itemsView: "fragment",
            status: "completed",
            items: [
              {
                id: "item_tool_result_1",
                type: "commandExecution",
                toolName: "shell",
                callId: "call-1",
                output: "ok",
                exitCode: 0,
                status: "completed",
                transcriptKey: "k:result",
                position: { entry: 2, item: 0 },
              },
            ] as ThreadItem[],
          },
        ],
      };
      client.on("thread/turns/list", () => page);

      const result = await service.loadOlder("opaque-cursor");
      expect(result.turnsPage).toEqual(page);
    });

    // A replayed input image's sha-route resolution moved store-side with
    // D23d (the service hands the wire page over verbatim; the store's merge
    // hydrates the page under the real thread id) — see the store suite's
    // "routes an older page's sha-only image through the real thread id".

    it("preserves fragment completeness metadata for the page boundary", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      client.on(
        "thread/turns/list",
        () =>
          ({
            data: [
              {
                id: "turn-old",
                itemsView: "fragment",
                status: "completed",
                hasEarlierItems: true,
                hasLaterItems: true,
                items: [],
              },
            ],
            nextCursor: "next",
          }) as ThreadTurnsListResponse,
      );

      await expect(service.loadOlder("opaque-cursor")).resolves.toMatchObject({
        hasEarlierItems: true,
        hasLaterItems: true,
      });
    });

    it("honors each supplied cursor after a page has no continuation", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      let calls = 0;
      client.on("thread/turns/list", (params) => {
        calls += 1;
        expect(params).toMatchObject({
          cursor: calls === 1 ? "first" : "second",
        });
        return { data: [], nextCursor: undefined } as ThreadTurnsListResponse;
      });

      await service.loadOlder("first");
      await service.loadOlder("second");

      expect(calls).toBe(2);
    });

    it("refreshes the projection after a stale cursor and retries with its new boundary", async () => {
      const { client, service } = setup({ olderCursor: "fresh-cursor" });
      await service.open("ref-1");
      let listCalls = 0;
      client.on("thread/turns/list", (params) => {
        listCalls += 1;
        if (listCalls === 1) {
          throw new WireError("stale transcript cursor", -32020, {
            evenerErrorInfo: "transcriptItemCursorStale",
          });
        }
        expect(params).toMatchObject({
          cursor: "caller-cursor-is-ignored",
          itemLimit: 40,
        });
        return { data: [], nextCursor: undefined } as ThreadTurnsListResponse;
      });

      await expect(service.loadOlder("stale-cursor")).rejects.toThrow(
        "stale transcript cursor",
      );
      const result = await service.loadOlder("caller-cursor-is-ignored");
      expect(result.turnsPage).toEqual({ data: [], nextCursor: undefined });
      expect(listCalls).toBe(2);
      const reads = client.calls.filter(
        (call) => call.method === "thread/read",
      );
      expect(reads).toHaveLength(1);
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

    it("does not let an older unsubscribe stop a newer subscription", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      const first: AnyNotification[] = [];
      const second: AnyNotification[] = [];
      const unsubFirst = service.subscribeNotifications((n) => first.push(n));
      service.subscribeNotifications((n) => second.push(n));

      // Replacing a subscription must leave the replacement active even when
      // the caller later disposes the older subscription handle.
      unsubFirst();
      client.emitNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
        },
      } as AnyNotification);

      expect(first).toHaveLength(0);
      expect(second).toHaveLength(1);
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
        expectedInstanceId: "thread-1",
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
        expectedInstanceId: "thread-1",
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
        expectedInstanceId: "thread-1",
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
      expect(call?.params).toMatchObject({
        ref: "ref-1",
        expectedInstanceId: "thread-1",
      });
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
          receipt: { ...validReceipt, disposition: "accepted" },
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
            receipt: {
              ...makeReceipt("interrupt"),
              queueEntryIds: ["forbidden"],
            },
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

    it("treats a correlated canonical replay as a successful idempotent result", async () => {
      const { client, service } = setup();
      client.on(
        "turn/start",
        () =>
          ({
            turn: { id: "t1", itemsView: "default", status: "running" },
            receipt: makeReceipt("send", { disposition: "replayed" }),
          }) as TurnStartResponse,
      );
      await service.open("ref-1");
      await expect(service.send(textInput("retry"))).resolves.toMatchObject({
        clientMutationId: "cmid-1",
        disposition: "replayed",
        turnId: "turn-1",
        projectionState: "pending",
      });
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

    it("forkFromTurn sends a placeholder item key naming the entry ordinal", async () => {
      const { client, service } = setup();
      client.on("thread/fork", () => ({
        thread: makeThread({ evener: { ...makeThread().evener, ref: "ref-2" } }),
      }));
      await service.open("ref-1");
      await service.forkFromTurn(5);
      const call = client.calls.find((c) => c.method === "thread/fork");
      expect(call?.params).toMatchObject({
        ref: "ref-1",
        sourceItemKey: "apptranscript-item-v2:mobile-fork-entry:4:0",
        deferInput: true,
      });
      expect((call?.params as { sourceTurnId?: string }).sourceTurnId).toBeUndefined();
    });

    it("forkAside sends aside without a source item key", async () => {
      const { client, service } = setup();
      client.on("thread/fork", () => ({
        thread: makeThread({ evener: { ...makeThread().evener, ref: "ref-2" } }),
      }));
      await service.open("ref-1");
      await service.forkAside();
      const call = client.calls.find((c) => c.method === "thread/fork");
      expect(call?.params).toMatchObject({ ref: "ref-1", aside: true });
      expect((call?.params as { sourceItemKey?: string }).sourceItemKey).toBeUndefined();
      expect((call?.params as { sourceTurnId?: string }).sourceTurnId).toBeUndefined();
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

    it.each(["", "off", "provider/model"])(
      "sets the reviewed vision choice %j once for the bound session",
      async (visionModel) => {
        const { client, service } = setup();
        client.on("thread/vision-model/set", () => EMPTY_RESPONSE);
        await service.open("ref-1");
        await service.setVisionModel(visionModel);
        expect(
          client.calls.filter(
            (call) => call.method === "thread/vision-model/set",
          ),
        ).toEqual([
          {
            method: "thread/vision-model/set",
            params: { ref: "ref-1", visionModel },
          },
        ]);
      },
    );

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
        receipt: makeReceipt("queue", {
          instanceId: "thread-1",
          projectionState: "removed",
          queueEntryIds: ["entry-1"],
        }),
      };
      client.on("turn/cancelQueued", () => cancelResp);
      await service.open("ref-1");
      const result = await service.cancelQueued(0, "entry-1", "thread-1");
      expect(result.removedText).toBe("queued text");
      const call = client.calls.find((c) => c.method === "turn/cancelQueued");
      expect(call?.params).toMatchObject({
        ref: "ref-1",
        index: 0,
        expectedEntryId: "entry-1",
        expectedInstanceId: "thread-1",
      });
    });
  });

  describe("session instance identity", () => {
    it("pins mutations to the read instance until the conversation is rehydrated", async () => {
      const { client, service, thread } = setup();
      thread.evener.instanceId = "instance-a";
      client.on("turn/start", (params) => {
        expect(params.expectedInstanceId).toBe(thread.evener.instanceId);
        return {
          turn: { id: "turn-1" } as Turn,
          receipt: makeReceipt("send", {
            clientMutationId: params.clientMutationId,
            instanceId: params.expectedInstanceId,
          }),
        };
      });
      await service.open("ref-1");
      expect((await service.send(textInput("first"))).instanceId).toBe(
        "instance-a",
      );
      const replacement = makeThread({
        evener: { ...thread.evener, instanceId: "instance-b" },
      });
      client.on("thread/read", () => makeReadResponse(replacement));
      await service.readProjection("ref-1");
      thread.evener.instanceId = "instance-b";
      expect((await service.send(textInput("replacement"))).instanceId).toBe(
        "instance-b",
      );
    });

    it("rejects a receipt for a different session instance", async () => {
      const { client, service } = setup();
      client.on("turn/start", () => ({
        turn: { id: "turn-1" } as Turn,
        receipt: makeReceipt("send", { instanceId: "other-instance" }),
      }));
      await service.open("ref-1");
      await expect(service.send(textInput("hello"))).rejects.toThrow(
        "instance mismatch",
      );
    });
  });

  describe("pushed capability transitions", () => {

    it("preserves a same-thread capability push received during rehydration", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      service.subscribeNotifications(() => {});
      let resolve!: (response: ThreadReadResponse) => void;
      client.on(
        "thread/read",
        () =>
          new Promise<ThreadReadResponse>((done) => {
            resolve = done;
          }),
      );
      const hydration = service.readProjection("ref-1");
      await Promise.resolve();
      client.emitNotification({
        method: "thread/status/changed",
        params: {
          ref: "ref-1",
          threadId: "thread-1",
          status: { type: "active" },
          capabilities: { ...ALL_TRUE_CAPS, send: false },
        },
      });
      resolve(makeReadResponse(makeThread()));
      const hydrated = await hydration;
      expect(hydrated.conversation.capabilities.send).toBe(false);
      expect(hydrated.activity.capabilities.send).toBe(false);
      await expect(
        service.send([{ type: "text", text: "not idle" }]),
      ).rejects.toThrow();
      expect(
        client.calls.filter((c) => c.method === "turn/start"),
      ).toHaveLength(0);
    });

    it("uses the active capability set for mutations and rejects other thread updates", async () => {
      const { client, service } = setup({
        thread: makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS, steer: false },
            queue: { revision: 0 },
          },
        }),
      });
      await service.open("ref-1");
      service.subscribeNotifications(() => {});
      client.on(
        "turn/steer",
        () => ({ receipt: makeReceipt("steer") }) as TurnSteerResponse,
      );
      const emit = (ref: string, threadId: string, caps: ThreadCapabilities) =>
        client.emitNotification({
          method: "thread/status/changed",
          params: {
            ref,
            threadId,
            status: { type: "active" },
            capabilities: caps,
          },
        });
      emit("other-ref", "thread-1", ALL_TRUE_CAPS);
      emit("ref-1", "other-thread", ALL_TRUE_CAPS);
      await expect(
        service.steer([{ type: "text", text: "direction" }]),
      ).rejects.toThrow();
      expect(
        client.calls.filter((c) => c.method === "turn/steer"),
      ).toHaveLength(0);
      emit("ref-1", "thread-1", { ...ALL_TRUE_CAPS, send: false });
      await service.steer([{ type: "text", text: "direction" }]);
      expect(
        client.calls.filter((c) => c.method === "turn/steer"),
      ).toHaveLength(1);
      await expect(
        service.send([{ type: "text", text: "wrong mode" }]),
      ).rejects.toThrow();
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

    it.each(["", "off", "provider/model"])(
      "setVisionModel rejects without dispatch when changeVisionModel is false (%j)",
      async (visionModel) => {
        const thread = makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS, changeVisionModel: false },
            queue: { revision: 0 },
          },
        });
        const { client, service } = setup({ thread });
        client.on("thread/vision-model/set", () => EMPTY_RESPONSE);
        await service.open("ref-1");
        await expect(service.setVisionModel(visionModel)).rejects.toThrow();
        expect(
          client.calls.find((c) => c.method === "thread/vision-model/set"),
        ).toBeUndefined();
      },
    );

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

  describe("actionUnavailable is re-thrown", () => {
    it("does NOT auto-refresh on actionUnavailable — store owns the single read", async () => {
      // F2: The service must NOT read capabilities itself on
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
      // store requests its coalesced reread of the snapshot.
      expect(readCount).toBe(initialReadCount);
    });
  });

  describe("readProjection", () => {
    it("sends itemLimit 40 for the bounded item-mode read", async () => {
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
        itemsView: "fragment",
        itemLimit: 40,
      });
    });

    it("returns ConversationReadProjection with conversation, activity, olderCursor", async () => {
      const { service } = setup({ olderCursor: "older-abc" });
      await service.open("ref-1");
      const result = await service.readProjection("ref-1");
      expect(result).toBeDefined();
      expect(result?.conversation).toBeDefined();
      expect(result?.conversation.threadId).toBe("thread-1");
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


  describe("loadOlder with explicit limit", () => {
    it("passes opaque cursor and itemLimit to thread/turns/list", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      client.calls.length = 0;
      await service.loadOlder("cursor-xyz");
      const call = client.calls.find((c) => c.method === "thread/turns/list");
      expect(call?.params).toMatchObject({
        ref: "ref-1",
        cursor: "cursor-xyz",
        itemsView: "fragment",
        itemLimit: 40,
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
    it("increments epoch and clears ref+capabilities so a later mutation fails closed", async () => {
      // I2: close increments epoch and clears the pair, leaving no dangling
      // ref/caps for a mutation to send under.
      const { client, service } = setup();
      const threadA = makeThread({
        evener: {
          ref: "ref-A",
          capabilities: { ...ALL_TRUE_CAPS },
          queue: { revision: 0 },
        },
      });
      client.on("thread/read", () => makeReadResponse(threadA));
      await service.open("ref-A");
      service.close();

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
      // Must have readProjection as a required method
      expect(typeof service.readProjection).toBe("function");
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
      await expect(
        service.cancelQueued(0, "entry-1", "thread-1"),
      ).rejects.toThrow();
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
      await expect(
        service.cancelQueued(0, "entry-1", "thread-1"),
      ).rejects.toThrow();
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


  // R4-Important: open/readProjection must not commit ref/capabilities until ALL
  // response-derived projection work succeeds. After await, compute/validate
  // projectConversation, activity projection, olderCursor/result locals first; only
  // then, if epoch current, atomically commit ref+caps. Any malformed
  // response/projectConversation/activity projection throw leaves pair null/fail-closed.
  // Stale successful result may return without committing.
  describe("projection throw leaves pair fail-closed (R4-Important)", () => {
    it("open: projectConversation throw leaves pair null — all operations fail before wire", async () => {
      const { client, service } = setup();
      await service.open("ref-1"); // initial valid open
      const spy = vi
        .spyOn(projectModule, "projectConversation")
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

    it("readProjection: projectConversation throw leaves pair null — all operations fail before wire", async () => {
      const { client, service } = setup();
      await service.open("ref-1");
      const spy = vi
        .spyOn(projectModule, "projectConversation")
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
      // An older open whose projectConversation succeeds but resolves after a newer
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
      // Resolve A's stale open — it returns projectConversation(threadA) but must
      // NOT commit A's pair over B's.
      resolveARead(makeReadResponse(threadA));
      const convA = await openAPromise;
      // A's result is returned (stale successful result returns).
      expect(convA.threadId).toBe("thread-A");
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
      expect(resultA.conversation.threadId).toBe("thread-A");
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
        .spyOn(projectModule, "projectConversation")
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
        expect(conv.threadId).toBe("thread-1");
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

  // R5: Before ANY ref/capability state write in open/readProjection/refresh,
  // extract and runtime-validate response.thread.evener.capabilities into a
  // complete plain local ThreadCapabilities copy. All 12 generated fields must
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
      await expect(service.cancelQueued(0, "e", "thread-1")).rejects.toThrow();
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

    it("open: a peer that predates sharedNotes keeps the session usable", async () => {
      // The field is newer than the protocol version this client speaks, so a
      // hub built before it omits it. Treating it as required rejected the whole
      // payload and failed every capability-gated action for the session.
      const { sharedNotes: _omitted, ...legacyCaps } = ALL_TRUE_CAPS;
      const thread = makeThreadWithCaps(legacyCaps);
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
      // A capability-gated action still reaches the wire.
      await service.send(textInput("hello"));
      expect(client.calls.filter((c) => c.method === "turn/start")).toHaveLength(1);
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
        changeVisionModel: true,
        sharedNotes: false,
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
        changeVisionModel: true,
        sharedNotes: false,
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
      expect(conv.threadId).toBe("thread-1");
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



  });

  // I3: the service-side historical-page question filter died with
  // projectOlderTurns (D23d) — the ask gate lives in the projection the
  // store re-projects through (liveAskQuestions), covered by the store
  // suite's "a page ask_user item renders as its tool row..." fixture.
});

describe("queue action receipts", () => {
  const cases = [
    {
      method: "turn/cancelQueued" as const,
      call: (service: ReturnType<typeof createConversationService>) =>
        service.cancelQueued(1, "entry-b", "thread-1"),
    },
    {
      method: "turn/promoteQueuedAsSteer" as const,
      call: (service: ReturnType<typeof createConversationService>) =>
        service.promoteQueuedAsSteer(1, "entry-b", "thread-1"),
    },
    {
      method: "turn/drainAsSteer" as const,
      call: (service: ReturnType<typeof createConversationService>) =>
        service.drainAsSteer(7, "thread-1"),
    },
  ];
  function response(method: string) {
    const cancel = method === "turn/cancelQueued";
    return {
      ...(cancel ? { removedText: "sentinel", removedImages: 2 } : {}),
      receipt: {
        clientMutationId: "cmid-1",
        threadId: "thread-1",
        instanceId: "thread-1",
        disposition: "applied",
        projectionState: cancel ? "removed" : "pending",
        queueEntryIds: ["entry-b"],
        ...(cancel ? {} : { turnId: "turn-1" }),
      },
    };
  }
  it.each(cases)(
    "validates $method before treating the action as acknowledged",
    async ({ method, call }) => {
      for (const change of [
        () => null,
        () => ({}),
        (valid: ReturnType<typeof response>) => ({
          ...valid,
          receipt: { ...valid.receipt, clientMutationId: "other" },
        }),
        (valid: ReturnType<typeof response>) => ({
          ...valid,
          receipt: { ...valid.receipt, threadId: "other" },
        }),
        (valid: ReturnType<typeof response>) => ({
          ...valid,
          receipt: { ...valid.receipt, instanceId: undefined },
        }),
        (valid: ReturnType<typeof response>) => ({
          ...valid,
          receipt: { ...valid.receipt, instanceId: "other" },
        }),
        (valid: ReturnType<typeof response>) => ({
          ...valid,
          receipt: { ...valid.receipt, disposition: "unknown" },
        }),
        (valid: ReturnType<typeof response>) => ({
          ...valid,
          receipt: { ...valid.receipt, projectionState: "reflected" },
        }),
        (valid: ReturnType<typeof response>) => ({
          ...valid,
          receipt: { ...valid.receipt, queueEntryIds: [] },
        }),
        (valid: ReturnType<typeof response>) => ({
          ...valid,
          receipt: { ...valid.receipt, queueEntryIds: ["entry-b", "entry-b"] },
        }),
        (valid: ReturnType<typeof response>) => ({
          ...valid,
          receipt: {
            ...valid.receipt,
            turnId: method === "turn/cancelQueued" ? "unexpected" : "",
          },
        }),
      ]) {
        idCounter = 0;
        const { client, service } = setup();
        client.on(method, () => change(response(method)) as never);
        await service.open("ref-1");
        await expect(call(service)).rejects.toThrow();
        expect(
          client.calls.filter((entry) => entry.method === method),
        ).toHaveLength(1);
        service.close();
      }
    },
  );
  it.each(cases)(
    "returns the validated $method response",
    async ({ method, call }) => {
      idCounter = 0;
      const { client, service } = setup();
      const value = response(method);
      client.on(method, () => value as never);
      await service.open("ref-1");
      await expect(call(service)).resolves.toEqual(value);
      expect(
        client.calls.filter((entry) => entry.method === method),
      ).toHaveLength(1);
      service.close();
    },
  );
  it.each(cases.slice(0, 2))(
    "rejects the wrong selected entry for $method",
    async ({ method, call }) => {
      idCounter = 0;
      const { client, service } = setup();
      const value = response(method);
      value.receipt.queueEntryIds = ["other"];
      client.on(method, () => value as never);
      await service.open("ref-1");
      await expect(call(service)).rejects.toThrow();
      service.close();
    },
  );
  it("rejects malformed cancellation echoes", async () => {
    for (const change of [
      { removedText: null },
      { removedImages: -1 },
      { removedImages: 0.5 },
    ]) {
      idCounter = 0;
      const { client, service } = setup();
      client.on(
        "turn/cancelQueued",
        () => ({ ...response("turn/cancelQueued"), ...change }) as never,
      );
      await service.open("ref-1");
      await expect(
        service.cancelQueued(1, "entry-b", "thread-1"),
      ).rejects.toThrow();
      service.close();
    }
  });
});

describe("observed queue guards", () => {
  beforeEach(() => {
    idCounter = 0;
  });
  // A hub-served past session carries no evener.instanceId; the conversation
  // still shows the fence the service mutates under (the thread id).
  it("retains the instance identity used by the displayed queue", async () => {
    const { service } = setup();
    expect((await service.open("ref-1")).instanceId).toBe("thread-1");
  });
  it("rejects cancellation from an obsolete instance before dispatch", async () => {
    const { service, client } = setup();
    await service.open("ref-1");
    await expect(
      service.cancelQueued(0, "entry-1", "old-instance"),
    ).rejects.toThrow();
    expect(
      client.calls.filter((c) => c.method === "turn/cancelQueued"),
    ).toHaveLength(0);
  });
  // The hub advertises steer as harness support (not "a turn is running"), so
  // a steering harness carries steer:true at idle too, and a held queue can be
  // promoted or drained from idle; the status half of that rule is the
  // caller's (the SDK's sessionControls). A harness that advertises no steer
  // cannot take a drain at all, whatever else it advertises.
  it("refuses to run a held queue when the harness advertises no steer, even with send", async () => {
    const thread = makeThread();
    thread.evener.capabilities = { ...ALL_TRUE_CAPS, steer: false, send: true };
    const { service, client } = setup({ thread });
    await service.open("ref-1");
    await expect(
      service.promoteQueuedAsSteer(0, "held-entry", "thread-1"),
    ).rejects.toThrow(/unavailable for this session/);
    await expect(service.drainAsSteer(3, "thread-1")).rejects.toThrow(
      /unavailable for this session/,
    );
    expect(
      client.calls.filter(
        (c) =>
          c.method === "turn/promoteQueuedAsSteer" ||
          c.method === "turn/drainAsSteer",
      ),
    ).toHaveLength(0);
  });
  it("can run a held queue from idle on a harness that advertises steer", async () => {
    const thread = makeThread();
    thread.evener.capabilities = { ...ALL_TRUE_CAPS, steer: true, send: true };
    const { service, client } = setup({ thread });
    client.on("turn/promoteQueuedAsSteer", ({ clientMutationId }) => ({
      receipt: makeReceipt("steer", {
        clientMutationId,
        instanceId: "thread-1",
        queueEntryIds: ["held-entry"],
      }),
    }));
    client.on("turn/drainAsSteer", ({ clientMutationId }) => ({
      receipt: makeReceipt("steer", {
        clientMutationId,
        instanceId: "thread-1",
        queueEntryIds: ["held-entry"],
      }),
    }));
    await service.open("ref-1");
    await expect(
      service.promoteQueuedAsSteer(0, "held-entry", "thread-1"),
    ).resolves.toHaveProperty("receipt");
    await expect(service.drainAsSteer(3, "thread-1")).resolves.toHaveProperty(
      "receipt",
    );
    expect(
      client.calls.filter((c) => c.method.startsWith("turn/")),
    ).toHaveLength(2);
  });
  it("carries the observed entry and revision without retrying a conflict", async () => {
    const { service, client } = setup();
    const conflict = new WireError("queue changed", -32013, {
      evenerErrorInfo: "conflict",
    });
    client.on("turn/promoteQueuedAsSteer", () => {
      throw conflict;
    });
    client.on("turn/drainAsSteer", () => {
      throw conflict;
    });
    await service.open("ref-1");
    await expect(
      service.promoteQueuedAsSteer(2, "entry-c", "thread-1"),
    ).rejects.toBe(conflict);
    await expect(service.drainAsSteer(17, "thread-1")).rejects.toBe(conflict);
    expect(client.calls.filter((c) => c.method.startsWith("turn/"))).toEqual([
      {
        method: "turn/promoteQueuedAsSteer",
        params: {
          ref: "ref-1",
          index: 2,
          expectedEntryId: "entry-c",
          expectedInstanceId: "thread-1",
          clientMutationId: "cmid-1",
        },
      },
      {
        method: "turn/drainAsSteer",
        params: {
          ref: "ref-1",
          expectedQueueRevision: 17,
          expectedInstanceId: "thread-1",
          clientMutationId: "cmid-2",
        },
      },
    ]);
  });
  // A drain's receipt names the queue intents it consumed
  // (consumedClientMutationIds, issue #1704). Wire-shaped: the daemon omits
  // the key entirely when nothing was consumed (never an empty array), so the
  // decoded receipt must mirror that -- present only when the daemon named it.
  it("keeps a drain's consumedClientMutationIds on the decoded receipt when the daemon named some", async () => {
    const { client, service } = setup();
    client.on(
      "turn/drainAsSteer",
      () =>
        ({
          receipt: makeReceipt("steer", {
            consumedClientMutationIds: ["queued-1", "queued-2"],
          }),
        }) as TurnDrainAsSteerResponse,
    );
    await service.open("ref-1");
    const receipt = await service.steer(textInput("steer this"), 4);
    expect(receipt.consumedClientMutationIds).toEqual([
      "queued-1",
      "queued-2",
    ]);
  });
  it("omits consumedClientMutationIds from the decoded receipt when the daemon consumed nothing", async () => {
    const { client, service } = setup();
    client.on(
      "turn/drainAsSteer",
      () => ({ receipt: makeReceipt("steer") }) as TurnDrainAsSteerResponse,
    );
    await service.open("ref-1");
    const receipt = await service.steer(textInput("steer this"), 4);
    expect(receipt).not.toHaveProperty("consumedClientMutationIds");
  });
  it("rejects a drain receipt with an empty consumedClientMutationIds array", async () => {
    const { client, service } = setup();
    client.on(
      "turn/drainAsSteer",
      () =>
        ({
          receipt: makeReceipt("steer", {
            consumedClientMutationIds: [],
          }),
        }) as TurnDrainAsSteerResponse,
    );
    await service.open("ref-1");
    await expect(service.steer(textInput("steer this"), 4)).rejects.toThrow(
      /ConversationService/,
    );
  });
  // #1759: a receipt may carry additive keys a shipped build has never seen;
  // the decoder ignores them rather than rejecting the whole receipt.
  it("ignores an additive receipt key the decoder does not know", async () => {
    const { client, service } = setup();
    client.on(
      "turn/drainAsSteer",
      () =>
        ({
          receipt: makeReceipt("steer", {
            futureAdditiveField: "ignored",
          } as Partial<MutationReceipt>),
        }) as TurnDrainAsSteerResponse,
    );
    await service.open("ref-1");
    const receipt = await service.steer(textInput("steer this"), 4);
    expect(receipt).toMatchObject({
      clientMutationId: "cmid-1",
      disposition: "applied",
      threadId: "thread-1",
      projectionState: "pending",
      turnId: "turn-1",
    });
    expect(receipt).not.toHaveProperty("futureAdditiveField");
  });
  it("ignores an additive receipt key on a mutation other than drain", async () => {
    const { client, service } = setup();
    client.on(
      "turn/queue",
      () =>
        ({
          receipt: makeReceipt("queue", {
            futureAdditiveField: "ignored",
          } as Partial<MutationReceipt>),
        }) as TurnQueueResponse,
    );
    await service.open("ref-1");
    await expect(service.queue(textInput("queued"))).resolves.toMatchObject({
      clientMutationId: "cmid-1",
      queueEntryIds: ["queue-1"],
    });
  });
  it("still rejects a receipt missing a key the decoder requires", async () => {
    const { client, service } = setup();
    const { threadId: _omitted, ...withoutThreadId } = makeReceipt("steer");
    client.on(
      "turn/drainAsSteer",
      () => ({ receipt: withoutThreadId }) as unknown as TurnDrainAsSteerResponse,
    );
    await service.open("ref-1");
    await expect(service.steer(textInput("steer this"), 4)).rejects.toThrow(
      /ConversationService/,
    );
  });
  it("still rejects a known receipt key on a mutation kind that does not expect it", async () => {
    const { client, service } = setup();
    client.on(
      "turn/queue",
      () =>
        ({
          receipt: makeReceipt("queue", {
            turnId: "misplaced",
          } as Partial<MutationReceipt>),
        }) as TurnQueueResponse,
    );
    await service.open("ref-1");
    await expect(service.queue(textInput("queued"))).rejects.toThrow(
      /ConversationService/,
    );
  });
});

describe("bound conversation model catalog", () => {
  it.each(["open", "readProjection"] as const)(
    "scopes %s catalog reads to the bound harness and cwd",
    async (openMethod) => {
      const client = new FakeAppwireClient();
      client.on("thread/read", () =>
        makeReadResponse(
          makeThread({ source: "codex-local", cwd: "/projects/a b" }),
        ),
      );
      client.on("model/list", (params) => {
        expect(params).toEqual({
          harness: "codex-local",
          cwd: "/projects/a b",
        });
        return { data: [{ provider: "codex-local", model: "test-model" }] };
      });
      const service = createConversationService(client);
      await service[openMethod]("codex-local:session");
      expect((await service.models()).data[0]?.model).toBe("test-model");
    },
  );
  it("rejects a catalog from a previous binding and blocks reads after close", async () => {
    const client = new FakeAppwireClient();
    client.on("thread/read", () => makeReadResponse(makeThread()));
    let finish!: (value: { data: [] }) => void;
    let started!: () => void;
    const requestStarted = new Promise<void>((resolve) => {
      started = resolve;
    });
    client.on("model/list", () => {
      started();
      return new Promise((resolve) => {
        finish = resolve;
      });
    });
    const service = createConversationService(client);
    await service.open("local:a");
    const oldCatalog = service.models();
    await requestStarted;
    await service.open("local:b");
    finish({ data: [] });
    await expect(oldCatalog).rejects.toThrow();
    service.close();
    const calls = client.calls.length;
    await expect(service.models()).rejects.toThrow();
    expect(client.calls).toHaveLength(calls);
  });
});
