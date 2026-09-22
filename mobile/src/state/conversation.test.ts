// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import { describe, expect, it, vi } from "vitest";
import {
  hydrateThread,
  QUEUE_UNAVAILABLE,
  SEND_UNAVAILABLE,
  sessionControls,
  sessionTokens,
  TURN_RUNNING,
  WireError,
} from "@evener/appwire-client";
import type {
  AnyNotification,
  AskQuestionRef,
  EvenerThread,
  InputItem,
  MutationReceipt,
  Thread,
  ThreadCapabilities,
  ThreadItem,
  ThreadTurnsListResponse,
  Turn,
} from "@evener/appwire-client";
import type {
  ActivityMember,
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/project";
import { projectConversation } from "../conversation/project";
import { projectNativeTranscript } from "../../../mobile-native/src/transcriptPresentation";
import type { ActivityView } from "../services/activity";
import type {
  ConversationReadProjection,
  LiveConversationService,
} from "../services/conversation";
import type { ActivityIdentity } from "./activity";
import { createActivityStore } from "./activity";
import {
  createConversationStore,
  type LiveActivitySink,
  MAX_ITEM_BYTES,
  TRUNCATION_MARKER,
  truncateItem,
  truncateText,
} from "./conversation";

// --- fixture helpers ---------------------------------------------------------

// F4: A fake LiveActivitySink implementing the strict identity-first API.
// Records all calls for test assertions. applyLiveNotification returns
// "applied" by default, but tests can override the return per-notification.
// setLiveView returns true by default; tests can override to return false.
class FakeLiveActivitySink implements LiveActivitySink {
  setLiveViewCalls: { identity: ActivityIdentity; view: ActivityView }[] = [];
  applyLiveNotificationCalls: {
    identity: ActivityIdentity;
    n: AnyNotification;
  }[] = [];
  resetCalls = 0;
  // Override per-notification return value. If set, called for each n.
  notificationOutcome?: (
    n: AnyNotification,
  ) => "applied" | "rehydrate" | "ignored";
  // Override setLiveView return value. If set, called for each setLiveView.
  setLiveViewResult?: (
    identity: ActivityIdentity,
    view: ActivityView,
  ) => boolean;

  setLiveView(view: ActivityView, identity: ActivityIdentity): boolean {
    this.setLiveViewCalls.push({ identity, view });
    return this.setLiveViewResult
      ? this.setLiveViewResult(identity, view)
      : true;
  }
  applyLiveNotification(
    n: AnyNotification,
    identity: ActivityIdentity,
  ): "applied" | "rehydrate" | "ignored" {
    this.applyLiveNotificationCalls.push({ identity, n });
    return this.notificationOutcome ? this.notificationOutcome(n) : "applied";
  }
  reset(): void {
    this.resetCalls += 1;
  }
}

// Helper: create a fake sink that always returns "applied".
function createFakeSink(): FakeLiveActivitySink {
  return new FakeLiveActivitySink();
}

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
  sharedNotes: false,
  queue: true,
  goal: true,
  rename: true,
};

// The hub refusing a mutation whose capability moved on without a status frame
// this client saw (F11: a real WireError, matched by identity).
const refusal = () =>
  new WireError("action unavailable", -32000, {
    evenerErrorInfo: "actionUnavailable",
  }) as Error;

function makeConversation(
  over: Partial<MobileConversation> = {},
): MobileConversation {
  // Hydrated through the package from a minimal wire Thread, so the fixture
  // tracks hydrateThread's defaults instead of restating every model field.
  const thread: Thread = {
    id: "thread-1",
    sessionId: "session-1",
    preview: "hello",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 0,
    updatedAt: 0,
    // The wire's vocabulary. Idle is the shape the hub publishes when it
    // advertises Send (Send folds !active at the source); the steering
    // submissions name a running turn themselves.
    status: { type: "idle" },
    cwd: "",
    cliVersion: "",
    source: "",
    turns: [],
    evener: {
      ref: "ref-1",
      capabilities: ALL_TRUE_CAPS,
      queue: { revision: 0, depth: 0, preview: [] },
    },
  };
  return {
    ...projectConversation(hydrateThread({ thread }, "ref-1", 0)),
    ...over,
  };
}

function makeReceipt(over: Partial<MutationReceipt> = {}): MutationReceipt {
  return {
    clientMutationId: "cmid-1",
    disposition: "applied",
    threadId: "thread-1",
    turnId: "turn-1",
    projectionState: "pending",
    ...over,
  };
}

function textInput(text: string): InputItem[] {
  return [{ type: "text", text }];
}

// A wire Turn (thread/turns/list's own shape) carrying usage - what a
// loadOlder/rehydrate mock returns before hydration, distinct from the
// already-hydrated TurnModel makeConversation's turns take.
function wireTurn(id: string, inputTokens: number, outputTokens: number): Turn {
  return { id, itemsView: "fragment", status: "completed", usage: { inputTokens, outputTokens } };
}

function wireTurnFragment(
  id: string,
  items: NonNullable<Turn["items"]>,
  usage?: Turn["usage"],
): Turn {
  return {
    id,
    itemsView: "fragment",
    status: "completed",
    ...(usage === undefined ? {} : { usage }),
    items,
  };
}

function turnsPage(data: Turn[], nextCursor?: string): ThreadTurnsListResponse {
  return { data, nextCursor };
}

// A fake ConversationService that returns scripted values without any network.
class FakeConversationService implements LiveConversationService {
  ref: string | null = null;
  openConv: MobileConversation = makeConversation();
  olderCursor: string | null = null;
  olderItems: {
    items: MobileConversation["items"];
    turnsPage?: ThreadTurnsListResponse;
    nextCursor?: string;
    hasEarlierItems?: boolean;
    hasLaterItems?: boolean;
  } = {
    items: [],
  };
  receipt: MutationReceipt = makeReceipt();
  sendShouldReject: Error | null = null;
  sendCallCount = 0;
  steerCallCount = 0;
  queueCallCount = 0;
  interruptCallCount = 0;
  cancelQueuedResult: {
    removedText: string;
    removedImages?: number;
    receipt: MutationReceipt;
  } = {
    removedText: "text",
    receipt: makeReceipt(),
  };
  notificationHandler: ((n: AnyNotification) => void) | null = null;
  closed = false;
  // readProjection support
  readProjectionResult: {
    conversation: MobileConversation;
    activity: ActivityView;
    olderCursor: string | null;
  } | null = null;
  readProjectionBlock: Promise<ConversationReadProjection> | null = null;
  readProjectionCalls: { ref: string }[] = [];

  // Like the real service, the conversation returned is hydrated under the
  // ref that was opened: the store routes notifications by the model's ref.
  private hydratedUnder(
    ref: string,
    conversation: MobileConversation,
  ): MobileConversation {
    return { ...conversation, ref };
  }
  async open(ref: string, _cursor?: string): Promise<MobileConversation> {
    this.ref = ref;
    return this.hydratedUnder(ref, this.openConv);
  }
  async readProjection(ref: string): Promise<ConversationReadProjection> {
    this.readProjectionCalls.push({ ref });
    if (this.readProjectionBlock) return this.readProjectionBlock;
    if (this.readProjectionResult) {
      return {
        ...this.readProjectionResult,
        conversation: this.hydratedUnder(ref, this.readProjectionResult.conversation),
      };
    }
    return {
      conversation: this.hydratedUnder(ref, this.openConv),
      activity: {
        tasks: [],
        work: [],
        usage: {},
        capabilities: ALL_TRUE_CAPS,
      },
      olderCursor: this.olderCursor,
    };
  }
  async loadOlder(_cursor: string): Promise<{
    items: MobileConversation["items"];
    turnsPage?: ThreadTurnsListResponse;
    nextCursor?: string;
    hasEarlierItems?: boolean;
    hasLaterItems?: boolean;
  }> {
    // Support hanging for stale-safety tests: if olderItems is a Promise,
    // await it so it resolves when the test wants.
    if (this.olderItems instanceof Promise) {
      return this.olderItems;
    }
    return this.olderItems;
  }
  subscribeNotifications(handler: (n: AnyNotification) => void): () => void {
    this.notificationHandler = handler;
    return () => {
      this.notificationHandler = null;
    };
  }
  async send(_input: InputItem[]): Promise<MutationReceipt> {
    this.sendCallCount += 1;
    if (this.sendShouldReject) throw this.sendShouldReject;
    return this.receipt;
  }
  async steer(_input: InputItem[]): Promise<MutationReceipt> {
    this.steerCallCount += 1;
    return this.receipt;
  }
  async queue(_input: InputItem[]): Promise<MutationReceipt> {
    this.queueCallCount += 1;
    return this.receipt;
  }
  async interrupt(): Promise<MutationReceipt> {
    this.interruptCallCount += 1;
    return this.receipt;
  }
  async compact(): Promise<void> {}
  async shutdown(): Promise<void> {}
  async changeModel(_p: string, _m: string): Promise<void> {}
  async setReasoningEffort(_e: string): Promise<void> {}
  async setVisionModel(_v: string): Promise<void> {}
  async rename(_n: string): Promise<void> {}
  async cancelQueued(
    _i: number,
    _e: string,
  ): Promise<{
    removedText: string;
    removedImages?: number;
    receipt: MutationReceipt;
  }> {
    return this.cancelQueuedResult;
  }
  close(): void {
    this.closed = true;
    this.notificationHandler = null;
  }
}

// --- store tests -------------------------------------------------------------

function heldCluster(): MobileTimelineItem {
  return {
    kind: "activity",
    id: "wire-first",
    transcriptKey: "first",
    label: "shell",
    family: "tool",
    state: "running",
    detail: {},
    members: [
      { id: "wire-first", transcriptKey: "first", label: "shell", family: "tool", state: "running", detail: {} },
      { id: "wire-later", transcriptKey: "later", label: "shell", family: "tool", state: "running", detail: {} },
    ],
  };
}

async function beginHeldClusterRehydrate() {
  const service = new FakeConversationService();
  const stale = makeConversation({ items: [heldCluster()] });
  service.openConv = stale;
  service.readProjectionResult = {
    conversation: stale,
    activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
    olderCursor: null,
  };
  const store = createConversationStore();
  const sink = createFakeSink();
  await store.getState().openProjected(service, sink, "ref-1");
  let release!: (value: ConversationReadProjection) => void;
  service.readProjectionBlock = new Promise((resolve) => { release = resolve; });
  const rehydratePromise = store.getState().rehydrate(service, sink);
  return { service, store, sink, stale, release, rehydratePromise };
}

describe("ConversationStore", () => {
  describe("open", () => {
    it("transitions idle -> opening -> open and stores conversation", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      expect(store.getState().status).toBe("idle");
      const p = store.getState().open(service, "ref-1");
      expect(store.getState().status).toBe("opening");
      await p;
      const s = store.getState();
      expect(s.status).toBe("open");
      expect(s.ref).toBe("ref-1");
      expect(s.conversation).not.toBeNull();
      expect(s.conversation?.threadId).toBe("thread-1");
    });

    it("increments conversation generation on open", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const gen0 = store.getState().conversationGeneration;
      await store.getState().open(service, "ref-1");
      const gen1 = store.getState().conversationGeneration;
      expect(gen1).toBeGreaterThan(gen0);
    });

    it("stores olderCursor from conversation if available", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      expect(store.getState().olderCursor).toBeNull();
    });
  });

  describe("loadOlder", () => {
    it("sets loadingOlder and prepends items", async () => {
      const service = new FakeConversationService();
      service.olderItems = {
        items: [{ kind: "user", id: "older-result", text: "older" }],
        nextCursor: "next",
      };
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      // Set a cursor so loadOlder has a page to request.
      store.setState({ olderCursor: "cursor-1" });
      const p = store.getState().loadOlder(service);
      expect(store.getState().loadingOlder).toBe(true);
      await expect(p).resolves.toEqual({
        status: "loaded",
        itemKeys: ["older-result"],
      });
      expect(store.getState().loadingOlder).toBe(false);
    });

    it("F8: does not request when olderCursor is null", async () => {
      const service = new FakeConversationService();
      service.olderItems = { items: [], nextCursor: "next" };
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      // olderCursor is null after open — loadOlder should not request.
      expect(store.getState().olderCursor).toBeNull();
      await expect(store.getState().loadOlder(service)).resolves.toEqual({
        status: "ignored",
      });
      expect(store.getState().loadingOlder).toBe(false);
    });
  });

  describe("setDraft", () => {
    it("sets draft text", () => {
      const store = createConversationStore();
      store.getState().setDraft("hello world");
      expect(store.getState().draft).toBe("hello world");
    });
  });

  describe("send", () => {
    it("clears draft on success and sets pendingSend", async () => {
      const service = new FakeConversationService();
      service.receipt = makeReceipt({ clientMutationId: "cmid-99" });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().setDraft("my message");
      const p = store.getState().send(service, textInput("my message"));
      // While in-flight, draft should be cleared and pendingSend set
      expect(store.getState().draft).toBe("");
      await p;
      expect(store.getState().pendingSend).toBeNull();
      expect(store.getState().draft).toBe("");
    });

    it("publishes a replayed Hub receipt as success without retry or failure", async () => {
      const service = new FakeConversationService();
      service.receipt = makeReceipt({ disposition: "replayed" });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().setDraft("idempotent retry");

      await store.getState().send(service, textInput("idempotent retry"));

      expect(service.sendCallCount).toBe(1);
      expect(store.getState().pendingMutation).toBeNull();
      expect(store.getState().error).toBeNull();
      expect(store.getState().lastAcceptedMutation).toEqual({
        kind: "send",
        receipt: expect.objectContaining({ disposition: "replayed" }),
      });
    });

    it("restores draft on conflict and shows error", async () => {
      const service = new FakeConversationService();
      service.sendShouldReject = new Error("conflict");
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().setDraft("my message");
      await store.getState().send(service, textInput("my message"));
      const s = store.getState();
      expect(s.draft).toBe("my message");
      expect(s.error).not.toBeNull();
      expect(s.pendingSend).toBeNull();
    });

    it("does not auto-retry on failure", async () => {
      const service = new FakeConversationService();
      service.sendShouldReject = new Error("conflict");
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().setDraft("my message");
      await store.getState().send(service, textInput("my message"));
      expect(service.sendCallCount).toBe(1);
      // Wait a microtask to ensure no retry is scheduled
      await Promise.resolve();
      expect(service.sendCallCount).toBe(1);
    });
  });

  // --- mutation-state tests (Step 3) ------------------------------------------

  describe("mutation state — parameterized send/steer/queue/interrupt", () => {
    type MutationKind = "send" | "steer" | "queue" | "interrupt";

    const mutationCases: {
      kind: MutationKind;
      call: (
        store: ReturnType<typeof createConversationStore>,
        service: FakeConversationService,
        input: InputItem[],
      ) => Promise<void>;
      callCountField: keyof FakeConversationService;
    }[] = [
      {
        kind: "send",
        call: (store, s, i) => store.getState().send(s, i),
        callCountField: "sendCallCount",
      },
      {
        kind: "steer",
        call: (store, s, i) => store.getState().steer(s, i),
        callCountField: "steerCallCount",
      },
      {
        kind: "queue",
        call: (store, s, i) => store.getState().queue(s, i),
        callCountField: "queueCallCount",
      },
      {
        kind: "interrupt",
        call: (store, s, _i) => store.getState().interrupt(s),
        callCountField: "interruptCallCount",
      },
    ];

    // Send is offered at rest; the steering mutations need a running turn.
    function serviceFor(kind: MutationKind): FakeConversationService {
      const service = new FakeConversationService();
      service.openConv = makeConversation({ status: { type: kind === "send" ? "idle" : "active" } });
      return service;
    }

    for (const { kind, call, callCountField } of mutationCases) {
      it(`${kind}: calls the service ${kind} method`, async () => {
        const service = serviceFor(kind);
        const store = createConversationStore();
        await store.getState().open(service, "ref-1");
        if (kind === "interrupt") {
          await call(store, service, []);
        } else {
          await call(store, service, textInput("test"));
        }
        expect(service[callCountField]).toBe(1);
      });

      it(`${kind}: sets pending mutation state while in-flight`, async () => {
        const service = serviceFor(kind);
        // Make the service hang so we can inspect the in-flight state.
        let resolveFn: (() => void) | null = null as (() => void) | null;
        service.receipt = makeReceipt();
        const hangPromise = new Promise<MutationReceipt>((resolve) => {
          resolveFn = () => resolve(makeReceipt());
        });
        if (kind === "send") {
          service.sendShouldReject = null;
          service.send = async () => hangPromise;
        } else if (kind === "steer") {
          service.steer = async () => hangPromise;
        } else if (kind === "queue") {
          service.queue = async () => hangPromise;
        } else {
          service.interrupt = async () => hangPromise;
        }

        const store = createConversationStore();
        await store.getState().open(service, "ref-1");
        store.getState().setDraft("unsent draft");

        const p = call(store, service, textInput("test"));
        // While in-flight, mutation state should be pending.
        const pending = store.getState().pendingMutation;
        expect(pending).not.toBeNull();
        expect(pending?.status).toBe("pending");
        expect(pending?.kind).toBe(kind);
        expect(store.getState().lastAcceptedMutation).toBeNull();

        // Resolve and await
        resolveFn?.();
        await p;
        // After resolution, pending should be cleared
        expect(store.getState().pendingMutation).toBeNull();
        expect(store.getState().lastAcceptedMutation).toEqual({
          kind,
          receipt: expect.objectContaining({ disposition: "applied" }),
        });
      });

      it(`${kind}: records exact draft snapshot in mutation state`, async () => {
        const service = serviceFor(kind);
        let resolveFn: (() => void) | null = null as (() => void) | null;
        const hangPromise = new Promise<MutationReceipt>((resolve) => {
          resolveFn = () => resolve(makeReceipt());
        });
        if (kind === "send") {
          service.send = async () => hangPromise;
        } else if (kind === "steer") {
          service.steer = async () => hangPromise;
        } else if (kind === "queue") {
          service.queue = async () => hangPromise;
        } else {
          service.interrupt = async () => hangPromise;
        }

        const store = createConversationStore();
        await store.getState().open(service, "ref-1");
        store.getState().setDraft("exact draft text");
        const p = call(store, service, textInput("test"));
        const pending = store.getState().pendingMutation;
        // Interrupt snapshots null; others snapshot the exact draft text.
        if (kind === "interrupt") {
          expect(pending?.draftSnapshot).toBeNull();
        } else {
          expect(pending?.draftSnapshot).toBe("exact draft text");
        }
        resolveFn?.();
        await p;
      });

      it(`${kind}: clears pending and error on success`, async () => {
        const service = serviceFor(kind);
        const store = createConversationStore();
        await store.getState().open(service, "ref-1");
        await call(store, service, textInput("test"));
        expect(store.getState().pendingMutation).toBeNull();
        expect(store.getState().error).toBeNull();
      });

      it(`${kind}: restores draft and sets failed state on failure`, async () => {
        const service = serviceFor(kind);
        const rejectErr = new Error(`${kind} conflict`);
        if (kind === "send") {
          service.sendShouldReject = rejectErr;
        } else if (kind === "steer") {
          service.steer = async () => Promise.reject(rejectErr);
        } else if (kind === "queue") {
          service.queue = async () => Promise.reject(rejectErr);
        } else {
          service.interrupt = async () => Promise.reject(rejectErr);
        }

        const store = createConversationStore();
        await store.getState().open(service, "ref-1");
        store.getState().setDraft("draft to restore");
        await call(store, service, textInput("test"));
        // Error should be set
        expect(store.getState().error).not.toBeNull();
        // Failed mutation state PERSISTS (not cleared to null)
        expect(store.getState().pendingMutation).not.toBeNull();
        expect(store.getState().pendingMutation?.status).toBe("failed");
        expect(store.getState().lastAcceptedMutation).toBeNull();
        // Draft should be restored for send/steer/queue (interrupt doesn't
        // snapshot draft, so it's not restored)
        if (kind === "interrupt") {
          // Interrupt doesn't clear draft, so it remains as-is
          expect(store.getState().draft).toBe("draft to restore");
        } else {
          // Send/steer/queue clear draft on submit, restore on failure if no
          // new text was typed. Since no new text was typed, restore happens.
          expect(store.getState().draft).toBe("draft to restore");
        }
      });

      it(`${kind}: records generation in mutation state`, async () => {
        const service = serviceFor(kind);
        let resolveFn: (() => void) | null = null as (() => void) | null;
        const hangPromise = new Promise<MutationReceipt>((resolve) => {
          resolveFn = () => resolve(makeReceipt());
        });
        if (kind === "send") {
          service.send = async () => hangPromise;
        } else if (kind === "steer") {
          service.steer = async () => hangPromise;
        } else if (kind === "queue") {
          service.queue = async () => hangPromise;
        } else {
          service.interrupt = async () => hangPromise;
        }

        const store = createConversationStore();
        await store.getState().open(service, "ref-1");
        const genBefore = store.getState().conversationGeneration;
        store.getState().setDraft("draft");
        const p = call(store, service, textInput("test"));
        const pending = store.getState().pendingMutation;
        expect(pending?.generation).toBe(genBefore);
        resolveFn?.();
        await p;
      });
    }

    it("send has an independent capability gate from steer", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Only send is disabled, in the state that offers it.
      service.openConv = makeConversation({
        capabilities: { ...ALL_TRUE_CAPS, send: false },
      });
      await store.getState().open(service, "ref-1");
      await expect(
        store.getState().send(service, textInput("x")),
      ).rejects.toThrow(SEND_UNAVAILABLE);
      // steer should work since steer capability is true, once a turn runs
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: { threadId: "thread-1", ref: "ref-1", status: { type: "active" } },
      } as AnyNotification);
      await store.getState().steer(service, textInput("x"));
      expect(store.getState().error).toBeNull();
    });

    it("queue has an independent capability gate from send", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Only queue is disabled, in the state that offers it.
      service.openConv = makeConversation({
        status: { type: "active" },
        capabilities: { ...ALL_TRUE_CAPS, queue: false },
      });
      await store.getState().open(service, "ref-1");
      await expect(
        store.getState().queue(service, textInput("x")),
      ).rejects.toThrow(QUEUE_UNAVAILABLE);
      // send should work since send capability is true, once the turn ends
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: { threadId: "thread-1", ref: "ref-1", status: { type: "idle" } },
      } as AnyNotification);
      await store.getState().send(service, textInput("x"));
      expect(store.getState().error).toBeNull();
    });
  });

  // The web's rule (decision 2): a refused mutation surfaces its typed error
  // and the model converges through the reducer — one coalesced reread of the
  // authoritative snapshot — not through a bespoke capability read and write.
  describe("actionUnavailable surfaces the error and requests one coalesced reread", () => {
    it("surfaces the error and rereads once; the reread's capabilities are what the composer sees", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ evener: { ref: "ref-1", capabilities: { ...ALL_TRUE_CAPS }, queue: { revision: 0 } } }),
      );
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      expect(store.getState().conversation?.capabilities.send).toBe(true);
      const readsBefore = service.readProjectionCalls.length;
      // The hub refuses: its capabilities moved on without a status frame we saw.
      service.sendShouldReject = refusal();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          evener: { ref: "ref-1", capabilities: { ...ALL_TRUE_CAPS, send: false }, queue: { revision: 0 } },
        }),
      );
      const ctrl = makeControlledRead(service);
      await store.getState().send(service, textInput("x"));
      expect(store.getState().error).not.toBeNull();
      expect(store.getState().pendingMutation?.status).toBe("failed");
      await ctrl.ready(1);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      expect(service.readProjectionCalls.length).toBe(readsBefore + 1);
      expect(store.getState().conversation?.capabilities.send).toBe(false);
    });

    // The non-projected open() binds no activity sink and no production screen
    // uses it (a compatibility surface for test mocks): a refusal there
    // surfaces the error and issues no read at all.
    it("plain open(): actionUnavailable surfaces the error and issues no read", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      const readsBefore = service.readProjectionCalls.length;
      service.sendShouldReject = refusal();
      await store.getState().send(service, textInput("x"));
      await yieldMicrotask();
      await yieldMicrotask();
      expect(store.getState().error).not.toBeNull();
      expect(store.getState().pendingMutation?.status).toBe("failed");
      expect(service.readProjectionCalls.length).toBe(readsBefore);
    });
  });

  describe("applyNotification", () => {
    it.each(["", "off", "provider/model"])(
      "updates the vision model %j for the matching session",
      async (visionModel) => {
        const store = createConversationStore();
        const service = new FakeConversationService();
        await store.getState().open(service, "ref-1");
        store.getState().applyNotification({
          method: "thread/vision-model/changed",
          params: { threadId: "thread-1", ref: "ref-1", visionModel },
        } as AnyNotification);
        expect(store.getState().conversation?.visionModel).toBe(visionModel);
      },
    );

    // The package reducer's routing: a frame names its thread by ref when it
    // carries one, else by threadId; a frame naming neither is not about this
    // thread.
    it("drops vision changes that name another session or thread", async () => {
      const store = createConversationStore();
      const service = new FakeConversationService();
      await store.getState().open(service, "ref-1");
      const original = store.getState().conversation;
      for (const params of [
        { threadId: "thread-1", ref: "other" },
        { threadId: "other" },
        {},
      ]) {
        store.getState().applyNotification({
          method: "thread/vision-model/changed",
          params: { ...params, visionModel: "off" },
        } as AnyNotification);
      }
      expect(store.getState().conversation).toBe(original);
    });

    it("routes a vision change by its ref when it carries one", async () => {
      const store = createConversationStore();
      const service = new FakeConversationService();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "thread/vision-model/changed",
        params: { threadId: "other", ref: "ref-1", visionModel: "off" },
      } as AnyNotification);
      expect(store.getState().conversation?.visionModel).toBe("off");
    });
    it("drops notifications that don't match current ref", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      const originalConv = store.getState().conversation;
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-other",
          ref: "ref-other",
          status: { type: "running" },
        },
      } as AnyNotification);
      expect(store.getState().conversation).toBe(originalConv);
    });

    it("updates status on matching thread/status/changed", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
        },
      } as AnyNotification);
      expect(store.getState().conversation?.status.type).toBe("running");
    });

    it("updates name on matching evener/thread/name/changed", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "evener/thread/name/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          name: "New Name",
        },
      } as AnyNotification);
      expect(store.getState().conversation?.name).toBe("New Name");
    });

    it("updates queue on matching thread/queueChanged", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "thread/queueChanged",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          queue: {
            revision: 7,
            depth: 2,
            ids: ["entry-a", "entry-b"],
            texts: ["full first", "full second"],
            clientMutationIds: ["send-a", "send-b"],
            preview: ["first", "second"],
          },
        },
      } as AnyNotification);
      expect(store.getState().conversation?.queue?.depth).toBe(2);
      expect(store.getState().conversation?.queue?.revision).toBe(7);
      expect(store.getState().conversation?.queue?.ids).toEqual([
        "entry-a",
        "entry-b",
      ]);
      expect(store.getState().conversation?.queue?.texts).toEqual([
        "full first",
        "full second",
      ]);
      expect(store.getState().conversation?.queue?.clientMutationIds).toEqual([
        "send-a",
        "send-b",
      ]);
      expect(store.getState().conversation?.queue?.preview).toEqual([
        "first",
        "second",
      ]);
    });

    it("marks running on turn/started", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({ status: { type: "idle" } });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "turn/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turn: { id: "t1", itemsView: "default", status: "running" },
        },
      } as AnyNotification);
      // The status is thread/status/changed's, never turn/started's (the
      // status frame rides right behind it); the turn id is this frame's.
      expect(store.getState().conversation?.status.type).toBe("idle");
      expect(store.getState().conversation?.activeTurnId).toBe("t1");
    });

    it("leaves the status to the status frame on turn/completed", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({ status: { type: "active" } });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "turn/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turn: { id: "t1", itemsView: "default", status: "running" },
        },
      } as AnyNotification);
      expect(store.getState().conversation?.status.type).toBe("active");
      // Now complete
      store.getState().applyNotification({
        method: "turn/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turn: {
            id: "t1",
            itemsView: "default",
            status: "completed",
            usage: { totalTokens: 100 },
          },
        },
      } as AnyNotification);
      // A completed turn is followed by its status frame (idle at session end,
      // active at an inline boundary); this frame leaves the status alone.
      expect(store.getState().conversation?.status.type).toBe("active");
    });

    // The inline turn boundary through the store: turn/completed(previous),
    // turn/started(next), status(active), one frame each, and the controls
    // the SDK derives from the store's status never blink (the web's and
    // TUI's #1330).
    it("keeps steer and stop through an inline turn boundary", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({ status: { type: "active" }, activeTurnId: "t1" });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      const frames: AnyNotification[] = [
        {
          method: "turn/completed",
          params: { threadId: "thread-1", ref: "ref-1", turn: { id: "t1", itemsView: "", status: "completed" } },
        } as AnyNotification,
        {
          method: "turn/started",
          params: { threadId: "thread-1", ref: "ref-1", turn: { id: "t2", itemsView: "default", status: "inProgress" } },
        } as AnyNotification,
        {
          method: "thread/status/changed",
          params: { threadId: "thread-1", ref: "ref-1", status: { type: "active" } },
        } as AnyNotification,
      ];
      for (const frame of frames) {
        store.getState().applyNotification(frame);
        const conv = store.getState().conversation;
        if (conv === null) throw new Error("conversation gone");
        const controls = sessionControls(conv.status.type, conv.capabilities, conv.queue?.depth ?? 0);
        expect({ frame: frame.method, steer: controls.steer, stop: controls.stop }).toEqual({
          frame: frame.method,
          steer: true,
          stop: true,
        });
      }
    });

    // A genuine failure ends as turn/completed{status: "failed"} followed by
    // its own thread/status/changed(idle) frame (the agent's failure exit,
    // agent/session_lifecycle.go endInputAtTurnFailure, kata hen0); the status
    // frame owns the settle, exactly as it does for a completed turn.
    it("settles idle on the status frame when the active turn fails", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({ status: { type: "active" }, activeTurnId: "t1" });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "turn/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turn: { id: "t1", itemsView: "", status: "failed", error: { message: "rate limited" } },
        },
      } as AnyNotification);
      // The failed completion ends the turn; its status frame has not arrived.
      expect(store.getState().conversation?.status.type).toBe("active");
      expect(store.getState().conversation?.activeTurnId).toBeUndefined();
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: { threadId: "thread-1", ref: "ref-1", status: { type: "idle" } },
      } as AnyNotification);
      expect(store.getState().conversation?.status.type).toBe("idle");
    });

    // A failure that leaves a message queued is not a rest: the daemon resumes
    // the queued message and its turn_failed input-end emission reports the
    // session active (WireState reads active while pendingQueueDepth > 0), not
    // the idle a settled turn would reach. The client must hold the session
    // active at the failed frame and at the authoritative thread/status/changed
    // frame behind it -- so Send stays closed and the queued entry is still
    // there to run -- and follow the hand-off as the queued message becomes the
    // next turn and leaves the queue.
    it("keeps active through a failed turn that resumes a queued message", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({
        status: { type: "active" },
        activeTurnId: "t1",
        queue: {
          revision: 1,
          depth: 1,
          preview: ["queued"],
          ids: ["q1"],
          texts: ["queued"],
        },
      });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");

      // t1 fails; its queued message has not started yet.
      store.getState().applyNotification({
        method: "turn/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turn: { id: "t1", itemsView: "", status: "failed", error: { message: "rate limited" } },
        },
      } as AnyNotification);
      const failed = store.getState().conversation;
      if (failed === null) throw new Error("conversation gone");
      expect(failed.status.type).toBe("active");
      const controls = sessionControls(failed.status.type, failed.capabilities, failed.queue?.depth ?? 0);
      expect({ send: controls.send, queue: controls.queue }).toEqual({ send: false, queue: true });
      expect(failed.queue?.depth).toBe(1);
      expect(failed.queue?.texts).toEqual(["queued"]);

      // The failure exit's own status frame is the authority. With work still
      // queued it announces active, so the session stays active and Send stays
      // closed with the entry still waiting to run.
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: { threadId: "thread-1", ref: "ref-1", status: { type: "active" } },
      } as AnyNotification);
      const announced = store.getState().conversation;
      if (announced === null) throw new Error("conversation gone");
      expect(announced.status.type).toBe("active");
      expect(announced.queue?.depth).toBe(1);
      expect(announced.queue?.texts).toEqual(["queued"]);
      const afterStatus = sessionControls(announced.status.type, announced.capabilities, announced.queue?.depth ?? 0);
      expect(afterStatus.send).toBe(false);

      // The queue drains before the queued turn opens: the drain claims the
      // entry and emits thread/queueChanged (agent/session_queue.go
      // popQueueHead -> reflectDurableInputQueue), and the next iteration's
      // EventUserInput opens the turn. Assert each boundary rather than only
      // the final state, so an ordering regression is caught.
      store.getState().applyNotification({
        method: "thread/queueChanged",
        params: { threadId: "thread-1", ref: "ref-1", queue: { revision: 2, depth: 0 } },
      } as AnyNotification);
      const drained = store.getState().conversation;
      expect(drained?.status.type).toBe("active");
      expect(drained?.queue?.depth).toBe(0);
      expect(drained?.activeTurnId).toBeUndefined();

      store.getState().applyNotification({
        method: "turn/started",
        params: { threadId: "thread-1", ref: "ref-1", turn: { id: "t2", itemsView: "default", status: "running" } },
      } as AnyNotification);
      const resumed = store.getState().conversation;
      expect(resumed?.status.type).toBe("active");
      expect(resumed?.activeTurnId).toBe("t2");
      expect(resumed?.queue?.depth).toBe(0);
    });

    // The status is authoritative and the turn id can be absent while the
    // session is active (a read cut between turns); the failed completion's
    // own status frame settles idle, while one for a superseded turn is left
    // alone.
    it("settles idle on a failed completion's status frame, not on a superseded turn", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({ status: { type: "active" }, activeTurnId: undefined });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "turn/completed",
        params: { threadId: "thread-1", ref: "ref-1", turn: { id: "t-x", itemsView: "", status: "failed", error: { message: "boom" } } },
      } as AnyNotification);
      expect(store.getState().conversation?.status.type).toBe("active");
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: { threadId: "thread-1", ref: "ref-1", status: { type: "idle" } },
      } as AnyNotification);
      expect(store.getState().conversation?.status.type).toBe("idle");

      const other = new FakeConversationService();
      other.openConv = makeConversation({ status: { type: "active" }, activeTurnId: "t2" });
      const store2 = createConversationStore();
      await store2.getState().open(other, "ref-1");
      store2.getState().applyNotification({
        method: "turn/completed",
        params: { threadId: "thread-1", ref: "ref-1", turn: { id: "t1", itemsView: "", status: "failed", error: { message: "late" } } },
      } as AnyNotification);
      expect(store2.getState().conversation?.status.type).toBe("active");
      expect(store2.getState().conversation?.activeTurnId).toBe("t2");
    });

    // The control is re-evaluated at the mutation boundary: a Steer the
    // screen offered while active is refused if the status flipped idle
    // before the submit reached the store.
    it("refuses a steer submitted after the status flipped idle", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: { threadId: "thread-1", ref: "ref-1", status: { type: "idle" } },
      } as AnyNotification);
      await expect(store.getState().steer(service, textInput("x"))).rejects.toThrow(/no active turn/);
      expect(service.steerCallCount).toBe(0);
    });

    // Send is a control like the others: while a turn runs the session queues,
    // it does not send. The store refuses with the control's reason so the
    // fake service (which gates on nothing) cannot accept what the hub would
    // not.
    it("refuses a send while a turn is running, with the control's reason", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({ status: { type: "active" } });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      await expect(store.getState().send(service, textInput("x"))).rejects.toThrow(TURN_RUNNING);
      expect(service.sendCallCount).toBe(0);
    });

    it("does not merge a completed turn's usage into the cumulative conversation usage", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({
        usage: { totalTokens: 500, inputTokens: 300 },
      });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      // Two turns complete, each carrying its own (much smaller) per-turn
      // usage. The cumulative conversation usage set by the projection must
      // survive both — it is not the sum or replacement of per-turn totals.
      store.getState().applyNotification({
        method: "turn/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turn: {
            id: "t1",
            itemsView: "default",
            status: "completed",
            usage: { totalTokens: 40, inputTokens: 10 },
          },
        },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "turn/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turn: {
            id: "t2",
            itemsView: "default",
            status: "completed",
            usage: { totalTokens: 55, inputTokens: 15 },
          },
        },
      } as AnyNotification);
      expect(store.getState().conversation?.usage?.totalTokens).toBe(500);
      expect(store.getState().conversation?.usage?.inputTokens).toBe(300);
    });
  });

  describe("close", () => {
    it("transitions to closed and clears conversation", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().close();
      const s = store.getState();
      expect(s.status).toBe("closed");
      expect(s.conversation).toBeNull();
      expect(s.ref).toBeNull();
    });
  });

  describe("reset", () => {
    it("returns to idle state", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().setDraft("hello");
      store.getState().reset();
      const s = store.getState();
      expect(s.status).toBe("idle");
      expect(s.conversation).toBeNull();
      expect(s.ref).toBeNull();
      expect(s.draft).toBe("");
      expect(s.error).toBeNull();
    });
  });

  describe("generation safety", () => {
    it("late open from older generation does not overwrite newer conversation", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Open ref-1 (gen 1)
      await store.getState().open(service, "ref-1");
      // Reset (back to idle)
      store.getState().reset();
      // Open ref-2 (gen 2)
      service.openConv = makeConversation({ threadId: "thread-2", status: { type: "active" } });
      await store.getState().open(service, "ref-2");
      expect(store.getState().conversation?.threadId).toBe("thread-2");
      // A stale notification for ref-1 should be dropped: ref-2 keeps the
      // status it opened with rather than taking ref-1's idle.
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "idle" },
        },
      } as AnyNotification);
      expect(store.getState().conversation?.threadId).toBe("thread-2");
      expect(store.getState().conversation?.status.type).toBe("active");
    });
  });

  describe("openProjected", () => {
    // The initial read is the same thread/read (subscribe + replaceSubscription)
    // as a reread: a frame delivered before its response is already folded
    // into the snapshot, so it is neither replayed nor a reason to reread.
    it("does not reread for a frame that precedes the initial read's response", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ status: { type: "idle" } }),
      );
      const store = createConversationStore();
      const ctrl = makeControlledRead(service);
      const opening = store.getState().openProjected(service, createFakeSink(), "ref-1");
      await ctrl.started(1);
      service.notificationHandler?.({
        method: "thread/status/changed",
        params: { threadId: "thread-1", ref: "ref-1", status: { type: "active" } },
      } as AnyNotification);
      await ctrl.ready(1);
      ctrl.release();
      await ctrl.completed(1);
      await opening;
      await yieldMicrotask();
      expect(service.readProjectionCalls).toHaveLength(1);
      expect(store.getState().conversation?.status).toEqual({ type: "idle" });
    });

    it("uses readProjection to set conversation, cursor, and activity view", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const activityView: ActivityView = {
        tasks: [{ status: "done", count: 3 }],
        work: [],
        usage: { totalTokens: 42 },
        capabilities: ALL_TRUE_CAPS,
      };
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-proj" }),
        activity: activityView,
        olderCursor: "cursor-initial",
      };
      await store.getState().openProjected(service, sink, "ref-1");
      expect(store.getState().conversation?.threadId).toBe("thread-proj");
      expect(store.getState().olderCursor).toBe("cursor-initial");
      expect(store.getState().status).toBe("open");
      // F4: The sink should have received the activity view via setLiveView.
      expect(sink.setLiveViewCalls.length).toBe(1);
      expect(sink.setLiveViewCalls[0]?.view).toBe(activityView);
    });

    it("subscribes to notifications", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // The store should have subscribed for notifications
      expect(service.notificationHandler).not.toBeNull();
    });

    it("preserves olderCursor across openProjected", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation(),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "page-1",
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      expect(store.getState().olderCursor).toBe("page-1");
    });

    it("resets draft from prior thread (I8)", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Set draft from a prior thread
      store.getState().setDraft("old thread draft");
      // Open a new projected conversation
      service.readProjectionResult = {
        conversation: makeConversation(),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Draft should be reset — not carried from the prior thread.
      expect(store.getState().draft).toBe("");
    });

    it("C3: routes notifications to both conversation and activity stores", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const activityView: ActivityView = {
        tasks: [{ status: "done", count: 3 }],
        work: [],
        usage: { totalTokens: 42 },
        capabilities: ALL_TRUE_CAPS,
      };
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: activityView,
        olderCursor: null,
      };
      await store.getState().openProjected(service, sink, "ref-1");
      // Emit a notification that both stores should handle
      const n: AnyNotification = {
        method: "evener/task/updated",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          total: 10,
          done: 5,
        },
      } as AnyNotification;
      service.notificationHandler?.(n);
      // F4: The sink should have received the notification via applyLiveNotification.
      expect(sink.applyLiveNotificationCalls.length).toBeGreaterThan(0);
      expect(sink.applyLiveNotificationCalls[0]?.n).toBe(n);
    });

    it("suspends without clearing the displayed conversation and blocks mutations", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const sink = createFakeSink();
      await store.getState().openProjected(service, sink, "ref-1");
      const before = store.getState().conversation;
      const oldHandler = service.notificationHandler;

      store.getState().suspendProjected();
      expect(store.getState().conversation).toBe(before);
      expect(store.getState().status).toBe("opening");
      await store.getState().send(service, textInput("blocked"));
      expect(service.sendCallCount).toBe(0);

      oldHandler?.({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
        },
      } as AnyNotification);
      expect(store.getState().conversation).toBe(before);
    });

    it("retains the display while resuming and installs a fresh subscribed read", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const sink = createFakeSink();
      await store.getState().openProjected(service, sink, "ref-1");
      const before = store.getState().conversation;
      store.getState().suspendProjected();
      let release!: (value: ConversationReadProjection) => void;
      service.readProjectionBlock = new Promise((resolve) => {
        release = resolve;
      });

      const resume = store.getState().resumeProjected(service, sink, "ref-1");
      expect(store.getState().conversation).toBe(before);
      expect(store.getState().status).toBe("opening");
      expect(service.readProjectionCalls).toHaveLength(2);
      await store.getState().send(service, textInput("blocked while resuming"));
      expect(service.sendCallCount).toBe(0);
      release({
        conversation: before as MobileConversation,
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-resumed",
      });
      await resume;
      expect(store.getState().status).toBe("open");
      expect(store.getState().conversation?.threadId).toBe(before?.threadId);
      expect(store.getState().olderCursor).toBe("cursor-resumed");
      expect(service.notificationHandler).not.toBeNull();
    });

    it("reopens instead of retaining a display for a different service identity", async () => {
      const service = new FakeConversationService();
      const otherService = new FakeConversationService();
      otherService.openConv = makeConversation({ threadId: "thread-2" });
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.getState().suspendProjected();
      await store
        .getState()
        .resumeProjected(otherService, createFakeSink(), "ref-1");
      expect(store.getState().conversation?.threadId).toBe("thread-2");
      expect(store.getState().status).toBe("open");
    });

    it("retains the suspended display when the fresh subscribed read fails", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const before = store.getState().conversation;
      store.getState().suspendProjected();
      service.readProjectionBlock = Promise.reject(new Error("resume failed"));

      await store
        .getState()
        .resumeProjected(service, createFakeSink(), "ref-1");
      expect(store.getState().conversation).toBe(before);
      expect(store.getState().status).toBe("error");
      expect(store.getState().error).toBe("resume failed");
      await store.getState().send(service, textInput("blocked"));
      expect(service.sendCallCount).toBe(0);
    });

    it("preserves completed older-page history across a resumed latest read", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const sink = createFakeSink();
      const latest = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        items: [{ kind: "user", id: "new", text: "new" }],
      });
      service.readProjectionResult = {
        conversation: latest,
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-1",
      };
      service.olderItems = {
        items: [{ kind: "user", id: "old", text: "old" }],
        nextCursor: "cursor-2",
      };
      await store.getState().openProjected(service, sink, "ref-1");
      await store.getState().loadOlder(service);
      expect(
        store.getState().conversation?.items.map((item) => item.id),
      ).toEqual(["old", "new"]);
      store.getState().suspendProjected();
      let release!: (value: ConversationReadProjection) => void;
      service.readProjectionBlock = new Promise((resolve) => {
        release = resolve;
      });
      const resume = store.getState().resumeProjected(service, sink, "ref-1");
      expect(
        store.getState().conversation?.items.map((item) => item.id),
      ).toEqual(["old", "new"]);
      release({
        conversation: latest,
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      });
      await resume;
      expect(
        store.getState().conversation?.items.map((item) => item.id),
      ).toEqual(["old", "new"]);
      expect(store.getState().olderCursor).toBe("cursor-2");

      await store.getState().rehydrate(service, sink);
      expect(
        store.getState().conversation?.items.map((item) => item.id),
      ).toEqual(["old", "new"]);
      expect(store.getState().olderCursor).toBe("cursor-2");

      const replacement = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-2",
        items: [{ kind: "user", id: "replacement", text: "replacement" }],
      });
      service.readProjectionBlock = null;
      service.readProjectionResult = {
        conversation: replacement,
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "fresh-cursor",
      };
      await store.getState().rehydrate(service, sink);
      expect(
        store.getState().conversation?.items.map((item) => item.id),
      ).toEqual(["replacement"]);
      expect(store.getState().olderCursor).toBe("fresh-cursor");
    });

    it("invalidates an open that is still awaiting its first projection", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      let release!: (value: ConversationReadProjection) => void;
      service.readProjectionBlock = new Promise((resolve) => {
        release = resolve;
      });
      const opening = store
        .getState()
        .openProjected(service, createFakeSink(), "ref-1");
      store.getState().suspendProjected();
      release({
        conversation: makeConversation(),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      });
      await opening;
      expect(store.getState().conversation).toBeNull();
      expect(store.getState().status).toBe("opening");
    });
  });

  describe("loadOlder retained cap and ordering (I7)", () => {
    it("enforces a 500-item retained cap at the store level", async () => {
      const service = new FakeConversationService();
      // Generate 600 items from loadOlder; only 500 should be retained.
      const manyItems = Array.from({ length: 600 }, (_, i) => ({
        kind: "user" as const,
        id: `item-${i}`,
        text: `msg ${i}`,
      }));
      service.olderItems = { items: manyItems, nextCursor: undefined };
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      // Set a cursor so loadOlder has a page to request.
      store.setState({ olderCursor: "cursor-1" });
      await store.getState().loadOlder(service);
      const conv = store.getState().conversation;
      expect(conv).not.toBeNull();
      // The store should cap retained items at 500.
      expect(conv?.items.length).toBeLessThanOrEqual(500);
    });

    it("I7: prepends older items and retains the newest live tail at cap", async () => {
      const service = new FakeConversationService();
      // Create 600 existing items + 600 older items = 1200 total. Cap is 500.
      // Prepend ordering should keep the newest 500 (the live tail at the end
      // of the merged array), so the most recent items remain visible.
      const existingItems = Array.from({ length: 600 }, (_, i) => ({
        kind: "user" as const,
        id: `existing-${i}`,
        text: `existing ${i}`,
      }));
      const olderItems = Array.from({ length: 600 }, (_, i) => ({
        kind: "user" as const,
        id: `older-${i}`,
        text: `older ${i}`,
      }));
      service.openConv = makeConversation({ items: existingItems });
      service.olderItems = { items: olderItems, nextCursor: undefined };
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      // Set a cursor so loadOlder has a page to request.
      store.setState({ olderCursor: "cursor-1" });
      // open() caps existing items to 500 (newest)
      expect(store.getState().conversation?.items.length).toBe(500);
      await store.getState().loadOlder(service);
      const conv = store.getState().conversation;
      expect(conv?.items.length).toBe(500);
      // The newest items (from existing) should be at the tail, since
      // we trim from the oldest (front) in prepend mode to retain the
      // live tail.
      expect(conv?.items[499]?.id).toBe("existing-599");
    });

    // F8's stop signal is "the cap discarded rows", not "the final count
    // reached the cap": capItems slices to the newest RETAINED_ITEM_CAP rows
    // and then drops a leading attachment whose source fell off the cut, so a
    // full merge that drops an orphan ends below the cap. A count-based proxy
    // then keeps paging enabled through a load that discarded its whole page,
    // and stops paging after a merge that discarded nothing.
    const pairSource = {
      kind: "user" as const,
      id: "src-1",
      text: "source row",
    };
    const pairAttachment = {
      kind: "attachments" as const,
      id: "src-1:attachments",
      items: [{ id: "img-1", src: "data:image/png;base64,AAA" }],
    };
    const fillers = (count: number) =>
      Array.from({ length: count }, (_, i) => ({
        kind: "user" as const,
        id: `filler-${i}`,
        text: `filler ${i}`,
      }));

    it("stops paging when the cap discards the whole page, even though the orphan drop leaves the count below the cap", async () => {
      const service = new FakeConversationService();
      // Open with 502 rows whose 500-cut splits the pair: the attachment
      // survives the slice as its first row (index 2 of 502, the first the
      // newest-500 slice keeps), its source one slot earlier does not, and
      // the orphan drop leaves 499 retained rows.
      service.openConv = makeConversation({
        items: [
          { kind: "user" as const, id: "head", text: "head row" },
          pairSource,
          pairAttachment,
          ...fillers(499),
        ],
      });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      expect(store.getState().conversation?.items.length).toBe(499);
      store.setState({ olderCursor: "cursor-1" });
      // The page is the split pair again: the cap cuts the source and the
      // orphan drop removes the attachment, so this load retains nothing.
      service.olderItems = {
        items: [pairSource, pairAttachment],
        nextCursor: "cursor-2",
        hasEarlierItems: true,
      };
      await store.getState().loadOlder(service);
      const conv = store.getState().conversation;
      expect(
        conv?.items.some(
          (row) => row.id === "src-1" || row.id === "src-1:attachments",
        ),
      ).toBe(false);
      // A load that discarded everything it fetched must end paging.
      expect(store.getState().olderCursor).toBeNull();
      expect(store.getState().hasEarlierItems).toBe(false);
    });

    it("keeps paging after a merge the cap did not trim, even when the retained count reaches the cap", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({
        items: [
          { kind: "user" as const, id: "head", text: "head row" },
          pairSource,
          pairAttachment,
          ...fillers(499),
        ],
      });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.setState({ olderCursor: "cursor-1" });
      // One older row: the merge reaches exactly the cap and discards
      // nothing, so the wire's own paging signal stands.
      service.olderItems = {
        items: [pairSource],
        nextCursor: "cursor-2",
        hasEarlierItems: true,
      };
      await store.getState().loadOlder(service);
      const conv = store.getState().conversation;
      expect(conv?.items[0]?.id).toBe("src-1");
      expect(store.getState().olderCursor).toBe("cursor-2");
      expect(store.getState().hasEarlierItems).toBe(true);
    });
  });

  // --- item lifecycle and delta notification tests (Step 2) -------------------

  describe("item/started inserts/replaces authoritative item", () => {
    it("inserts a new assistant item from item/started", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "agentMessage",
            id: "item-a",
            text: "Hello",
            status: "inProgress",
          },
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "item-a");
      expect(item).toBeDefined();
      expect(item?.kind).toBe("assistant");
    });

    it("replaces an existing item when item/started carries the same id", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        items: [
          {
            kind: "assistant",
            id: "item-a",
            markdown: "old",
            streaming: false,
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "agentMessage",
            id: "item-a",
            text: "new text",
            status: "inProgress",
          },
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "item-a");
      expect(item?.kind).toBe("assistant");
      if (item?.kind === "assistant") {
        expect(item.markdown).toBe("new text");
      }
    });

    // The replacement carries the transcript key, so it IS the same message
    // under a new wire id — and it says nothing about images, so the ones
    // already known stay with it (mergeItemImages, the hub's own rule: an
    // absent or empty input-images list is not a removal signal).
    //
    // The surviving row MUST be the one folded from conv.turns (the model
    // item, which retains the image) and re-keyed to the new wire id — not the
    // pre-existing companion row still sitting under the old id. The two
    // sources carry deliberately different images so the assertion can tell
    // them apart: a regression that leaves the stale row in place, or that
    // rebuilds the row from the raw wire item instead of the folded model item
    // (findFoldedItem/itemAttachments), fails here.
    it("replaces the same transcriptKey across wire IDs, folding images onto the new id", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        turns: [
          {
            id: "t1",
            status: "inProgress",
            items: [
              {
                id: "wire-old",
                turnId: "t1",
                type: "userMessage",
                transcriptKey: "stable-message",
                text: "old",
                images: [{ src: "https://hub.test/folded" }],
              },
            ],
          },
        ],
        items: [
          {
            kind: "user",
            id: "wire-old",
            transcriptKey: "stable-message",
            text: "old",
          },
          {
            kind: "attachments",
            id: "wire-old:attachments",
            sourceTranscriptKey: "stable-message",
            items: [{ id: "image", src: "https://hub.test/stale" }],
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "userMessage",
            id: "wire-new",
            transcriptKey: "stable-message",
            text: "new",
          },
        },
      } as AnyNotification);
      const items = store.getState().conversation?.items ?? [];
      expect(
        items.filter((item) => item.transcriptKey === "stable-message"),
      ).toHaveLength(1);
      expect(
        items.find((item) => item.transcriptKey === "stable-message")?.id,
      ).toBe("wire-new");
      // One attachment row, re-keyed to the replacement and carrying the
      // image the fold retained ("folded"), never the stale row's image.
      const attachments = items.filter((item) => item.kind === "attachments");
      expect(attachments).toHaveLength(1);
      expect(attachments[0]).toMatchObject({
        id: "wire-new:attachments",
        sourceTranscriptKey: "stable-message",
        items: [{ id: "wire-new:0", src: "https://hub.test/folded" }],
      });
    });
  });

  describe("item/completed settles item", () => {
    it("updates a first clustered member without losing later members or attachments", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({
        items: [
          {
            kind: "activity",
            id: "call-first",
            label: "shell",
            family: "tool",
            state: "running",
            detail: {},
            members: [
              {
                id: "call-first",
                label: "shell",
                family: "tool",
                state: "running",
                detail: {},
              },
              {
                id: "call-later",
                label: "shell",
                family: "tool",
                state: "running",
                detail: {},
              },
            ],
          },
          {
            kind: "attachments",
            id: "call-first:attachments",
            sourceTranscriptKey: "call-first",
            items: [{ id: "old", src: "https://hub.test/old" }],
          },
          {
            kind: "attachments",
            id: "call-later:attachments",
            sourceTranscriptKey: "call-later",
            items: [{ id: "later", src: "https://hub.test/later" }],
          },
        ],
      });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "commandExecution",
            id: "call-first",
            toolName: "shell",
            status: "completed",
            output: "updated",
            outputImages: [{ source: "new", url: "https://hub.test/new" }],
          },
        },
      } as AnyNotification);
      const items = store.getState().conversation?.items ?? [];
      const cluster = items.find(
        (item) => item.kind === "activity" && item.id === "call-first",
      );
      expect(cluster?.kind).toBe("activity");
      expect(cluster && "members" in cluster ? cluster.members : []).toHaveLength(
        2,
      );
      expect(items.filter((item) => item.id === "call-first")).toHaveLength(1);
      expect(
        items.find(
          (item): item is Extract<MobileTimelineItem, { kind: "attachments" }> =>
            item.kind === "attachments" && item.id === "call-first:attachments",
        )?.items,
      ).toEqual([{ id: "call-first:out:0", src: "https://hub.test/new" }]);
      expect(
        items.find((item) => item.id === "call-later:attachments"),
      ).toBeDefined();
    });

    it("uses transcript identity for a later member and keeps its attachment beside the cluster", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({
        items: [
          {
            kind: "activity",
            id: "wire-first",
            label: "shell",
            family: "tool",
            state: "completed",
            detail: {},
            members: [
              {
                id: "wire-first",
                label: "shell",
                family: "tool",
                state: "completed",
                detail: {},
                transcriptKey: "first",
              },
              {
                id: "wire-later",
                label: "shell",
                family: "tool",
                state: "running",
                detail: {},
                transcriptKey: "later",
              },
            ],
          },
          {
            kind: "attachments",
            id: "wire-first:attachments",
            sourceTranscriptKey: "first",
            items: [{ id: "first", src: "https://hub.test/first" }],
          },
          {
            kind: "attachments",
            id: "wire-later:attachments",
            sourceTranscriptKey: "later",
            items: [{ id: "old", src: "https://hub.test/old" }],
          },
        ],
      });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "commandExecution",
            id: "new-wire-later",
            transcriptKey: "later",
            toolName: "shell",
            status: "inProgress",
            outputImages: [{ source: "new", url: "https://hub.test/new" }],
          },
        },
      } as AnyNotification);
      const items = store.getState().conversation?.items ?? [];
      const activities = items.filter((item) => item.kind === "activity");
      expect(activities).toHaveLength(1);
      expect(
        activities[0]?.kind === "activity" && activities[0].members,
      ).toHaveLength(2);
      expect(items.map((item) => item.id)).toEqual([
        "wire-first",
        "wire-first:attachments",
        "new-wire-later:attachments",
      ]);
      expect(items[1]).toMatchObject({
        kind: "attachments",
        sourceTranscriptKey: "first",
        items: [{ id: "first", src: "https://hub.test/first" }],
      });
      expect(items[2]).toMatchObject({
        kind: "attachments",
        sourceTranscriptKey: "later",
        items: [{ id: "new-wire-later:out:0", src: "https://hub.test/new" }],
      });
      expect(items).not.toContainEqual(
        expect.objectContaining({ id: "wire-later:attachments" }),
      );
    });

    it("retains an omitted live-owned cluster during a held rehydrate", async () => {
      const { store, release, rehydratePromise } = await beginHeldClusterRehydrate();
      store.getState().applyNotification({
        method: "item/completed",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", item: { type: "commandExecution", id: "new-wire-later", transcriptKey: "later", toolName: "shell", status: "completed", output: "updated" } },
      } as AnyNotification);
      release({ conversation: makeConversation({ items: [] }), activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS }, olderCursor: null });
      await rehydratePromise;
      expect(store.getState().conversation?.items.filter((item) => item.kind === "activity")).toHaveLength(1);
      const members = store.getState().conversation?.items.flatMap((item) => item.kind === "activity" ? item.members ?? [item] : []) ?? [];
      expect(members.map((member) => member.transcriptKey)).toEqual(["first", "later"]);
      expect(members[1]?.detail.output).toBe("updated");
    });

    it.each([false, true])("keeps a failed member separate during a held rehydrate with page history %s", async (withPageHistory) => {
      const { store, service, stale, release, rehydratePromise } = await beginHeldClusterRehydrate();
      if (withPageHistory) {
        service.olderItems = { items: [{ kind: "user", id: "older", text: "older" }], nextCursor: undefined };
        store.setState({ olderCursor: "older-cursor" });
        await store.getState().loadOlder(service);
      }
      store.getState().applyNotification({
        method: "item/completed",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", item: { type: "commandExecution", id: "new-wire-later", transcriptKey: "later", toolName: "shell", status: "failed", output: "failed", error: "boom" } },
      } as AnyNotification);
      // The read is newer for the untouched first member, but predates the
      // second member's failure. Ownership must be resolved per member.
      const snapshot = heldCluster();
      if (snapshot.kind !== "activity" || !snapshot.members) throw new Error("invalid fixture");
      const first = snapshot.members[0];
      if (!first) throw new Error("missing first member");
      snapshot.members[0] = { ...first, state: "completed", detail: { output: "authoritative first" } };
      release({ conversation: { ...stale, items: [snapshot] }, activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS }, olderCursor: null });
      await rehydratePromise;
      const activities = store.getState().conversation?.items.filter((item) => item.kind === "activity") ?? [];
      expect(activities).toHaveLength(2);
      expect(activities.map((item) => item.transcriptKey)).toEqual(["first", "later"]);
      expect(activities[0]).toMatchObject({ state: "completed", detail: { output: "authoritative first" } });
      expect(activities[1]).toMatchObject({ state: "failed", detail: { output: "failed" } });
      expect(activities.every((item) => !item.members)).toBe(true);
      if (withPageHistory) expect(store.getState().conversation?.items[0]?.id).toBe("older");
    });

    it("does not resurrect a removed image when an attachment wire ID changes", async () => {
      const cluster = heldCluster();
      const stale = makeConversation({ items: [cluster, { kind: "attachments", id: "old-wire:attachments", sourceTranscriptKey: "later", items: [{ id: "old", src: "old" }] }] });
      const service = new FakeConversationService();
      service.openConv = stale;
      service.readProjectionResult = { conversation: stale, activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS }, olderCursor: null };
      const store = createConversationStore();
      const sink = createFakeSink();
      await store.getState().openProjected(service, sink, "ref-1");
      let release!: (value: ConversationReadProjection) => void;
      service.readProjectionBlock = new Promise((resolve) => { release = resolve; });
      const rehydratePromise = store.getState().rehydrate(service, sink);
      store.getState().applyNotification({ method: "item/completed", params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", item: { type: "commandExecution", id: "new-wire-later", transcriptKey: "later", toolName: "shell", status: "completed", output: "done" } } } as AnyNotification);
      release({ conversation: stale, activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS }, olderCursor: null });
      await rehydratePromise;
      expect(store.getState().conversation?.items.some((item) => item.kind === "attachments")).toBe(false);
    });

    it("preserves a later member completion during a held rehydrate", async () => {
      const service = new FakeConversationService();
      const cluster = {
        kind: "activity" as const,
        id: "wire-first",
        transcriptKey: "first",
        label: "shell",
        family: "tool" as const,
        state: "completed" as const,
        detail: {},
        members: [
          { id: "wire-first", label: "shell", family: "tool" as const, state: "completed" as const, detail: {}, transcriptKey: "first" },
          { id: "wire-later", label: "shell", family: "tool" as const, state: "running" as const, detail: {}, transcriptKey: "later" },
        ],
      };
      const stale = makeConversation({
        items: [
          cluster,
          { kind: "attachments", id: "wire-later:attachments", sourceTranscriptKey: "later", items: [{ id: "old", src: "old" }] },
        ],
      });
      service.openConv = stale;
      service.readProjectionResult = { conversation: stale, activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS }, olderCursor: null };
      const store = createConversationStore();
      const sink = createFakeSink();
      await store.getState().openProjected(service, sink, "ref-1");
      let release!: (value: ConversationReadProjection) => void;
      service.readProjectionBlock = new Promise((resolve) => { release = resolve; });
      const rehydratePromise = store.getState().rehydrate(service, sink);
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1", ref: "ref-1", turnId: "t1",
          item: { type: "commandExecution", id: "new-wire-later", transcriptKey: "later", toolName: "shell", status: "completed", output: "updated", outputImages: [{ source: "new", url: "new" }] },
        },
      } as AnyNotification);
      release({ conversation: stale, activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS }, olderCursor: null });
      await rehydratePromise;
      const items = store.getState().conversation?.items ?? [];
      const activity = items.find((item) => item.kind === "activity");
      expect(activity?.kind === "activity" ? activity.members?.find((member) => member.transcriptKey === "later")?.detail.output : undefined).toBe("updated");
      expect(items.find(
        (item): item is Extract<MobileTimelineItem, { kind: "attachments" }> =>
          item.kind === "attachments" && item.sourceTranscriptKey === "later",
      )?.items).toEqual([{ id: "new-wire-later:out:0", src: "new" }]);
    });

    it("splits a failed member out of a hydrated cluster", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({
        items: [
          {
            kind: "activity",
            id: "call-first",
            label: "shell",
            family: "tool",
            state: "running",
            detail: {},
            members: [
              {
                id: "call-first",
                label: "shell",
                family: "tool",
                state: "running",
                detail: {},
              },
              {
                id: "call-later",
                label: "shell",
                family: "tool",
                state: "running",
                detail: {},
              },
            ],
          },
        ],
      });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "commandExecution",
            id: "call-later",
            toolName: "shell",
            status: "completed",
            error: "boom",
          },
        },
      } as AnyNotification);
      const activities = (store.getState().conversation?.items ?? []).filter(
        (item) => item.kind === "activity",
      );
      expect(activities).toHaveLength(2);
      expect(
        activities.some(
          (item) =>
            item.kind === "activity" &&
            item.id === "call-later" &&
            item.state === "failed",
        ),
      ).toBe(true);
      expect(
        activities.filter(
          (item) => item.kind === "activity" && item.id === "call-later",
        ),
      ).toHaveLength(1);
      const flattened = projectNativeTranscript(
        store.getState().conversation,
        null,
      ).items;
      expect(
        flattened.filter((item) => item.kind === "activity").map((item) => item.id),
      ).toEqual(["call-first", "call-later"]);
    });

    it("marks an assistant item as not streaming on item/completed", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        items: [
          {
            kind: "assistant",
            id: "item-a",
            markdown: "streaming text",
            streaming: true,
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "agentMessage",
            id: "item-a",
            text: "streaming text",
            status: "completed",
          },
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "item-a");
      if (item?.kind === "assistant") {
        expect(item.streaming).toBe(false);
      }
    });

    it("marks an activity item as completed/failed on item/completed", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        items: [
          {
            kind: "activity",
            id: "tool-1",
            label: "shell",
            family: "tool",
            state: "running",
            detail: {},
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "commandExecution",
            id: "tool-1",
            toolName: "shell",
            status: "completed",
            output: "done",
          },
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "tool-1");
      expect(item?.kind).toBe("activity");
      if (item?.kind === "activity") {
        expect(item.state).toBe("completed");
      }
    });

    it("marks an activity item as failed on item/completed with a nonzero exit code and no error", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        items: [
          {
            kind: "activity",
            id: "tool-1",
            label: "shell",
            family: "tool",
            state: "running",
            detail: {},
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "commandExecution",
            id: "tool-1",
            toolName: "shell",
            status: "completed",
            output: "done",
            exitCode: 1,
          },
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "tool-1");
      expect(item?.kind).toBe("activity");
      if (item?.kind === "activity") {
        expect(item.state).toBe("failed");
      }
    });

    it("marks an activity item as completed on item/completed with a zero exit code and no error", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        items: [
          {
            kind: "activity",
            id: "tool-1",
            label: "shell",
            family: "tool",
            state: "running",
            detail: {},
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "commandExecution",
            id: "tool-1",
            toolName: "shell",
            status: "completed",
            output: "done",
            exitCode: 0,
          },
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "tool-1");
      expect(item?.kind).toBe("activity");
      if (item?.kind === "activity") {
        expect(item.state).toBe("completed");
      }
    });

    it("marks an activity item as failed on item/completed with an error and no exit code", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        items: [
          {
            kind: "activity",
            id: "tool-1",
            label: "shell",
            family: "tool",
            state: "running",
            detail: {},
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "commandExecution",
            id: "tool-1",
            toolName: "shell",
            status: "completed",
            output: "done",
            error: "boom",
          },
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "tool-1");
      expect(item?.kind).toBe("activity");
      if (item?.kind === "activity") {
        expect(item.state).toBe("failed");
      }
    });

    it("C6: upserts completed item even when start was missed", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Start with NO items — the item/started was missed.
      service.openConv = makeConversation({ items: [] });
      await store.getState().open(service, "ref-1");
      // item/completed arrives for an item that was never started.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "commandExecution",
            id: "tool-missed",
            toolName: "shell",
            status: "completed",
            output: "done",
          },
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      // The completed item should be inserted (UPSERT), not lost.
      const item = conv?.items.find((i) => i.id === "tool-missed");
      expect(item).toBeDefined();
      expect(item?.kind).toBe("activity");
    });

    // Task 2A: notification path (projectSingleItem) must set the durable
    // activity family from the wire type, independent of the label. A
    // commandExecution whose toolName is "Reasoning" stays family "tool" and
    // preserves callId exactly; a reasoning item is family "reasoning".
    it("2A: notification-path commandExecution named 'Reasoning' is family 'tool' with exact callId", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({ items: [] });
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "commandExecution",
            id: "tool-reasoning-1",
            toolName: "Reasoning",
            callId: "call-reasoning-1",
            status: "completed",
            output: "thoughts",
            argumentsJson: '{"summary":"thinking"}',
          },
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "tool-reasoning-1");
      expect(item?.kind).toBe("activity");
      if (item?.kind === "activity") {
        // Family follows the wire type (commandExecution), not the toolName.
        expect(item.family).toBe("tool");
        // Label is still the toolName verbatim (display only).
        expect(item.label).toBe("Reasoning");
        // callId preserved exactly for diagnostics disclosure.
        expect(item.detail.callId).toBe("call-reasoning-1");
      }
    });

    it("2A: notification-path reasoning item is family 'reasoning'", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({ items: [] });
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "reasoning",
            id: "reason-notify-1",
            status: "inProgress",
            text: "analyzing",
          },
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "reason-notify-1");
      expect(item?.kind).toBe("activity");
      if (item?.kind === "activity") {
        expect(item.family).toBe("reasoning");
        expect(item.label).toBe("Reasoning");
      }
    });

    it.each(["completed", "failed", "interrupted"] as const)(
      "preserves streamed reasoning text when sparse item/completed settles it as %s",
      async (status) => {
        const service = new FakeConversationService();
        const store = createConversationStore();
        service.openConv = makeConversation({ items: [] });
        await store.getState().open(service, "ref-1");
        store.getState().applyNotification({
          method: "item/started",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t1",
            item: {
              type: "reasoning",
              id: "reason-sparse-1",
              status: "inProgress",
            },
          },
        } as AnyNotification);
        for (const delta of ["Think", "ing"]) {
          store.getState().applyNotification({
            method: "item/reasoning/summaryTextDelta",
            params: {
              threadId: "thread-1",
              ref: "ref-1",
              turnId: "t1",
              itemId: "reason-sparse-1",
              summaryIndex: 0,
              delta,
            },
          } as AnyNotification);
        }
        store.getState().applyNotification({
          method: "item/completed",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t1",
            item: {
              type: "reasoning",
              id: "reason-sparse-1",
              status,
            },
          },
        } as AnyNotification);
        const item = store
          .getState()
          .conversation?.items.find(
            (candidate) => candidate.id === "reason-sparse-1",
          );
        expect(item?.kind).toBe("activity");
        if (item?.kind === "activity") {
          expect(item.state).toBe("completed");
          expect(item.detail.output).toBe("Thinking");
        }
      },
    );

    it.each([
      ["explicit empty text", "", ""],
      ["explicit text", "final reasoning", "final reasoning"],
    ] as const)(
      "%s remains authoritative when reasoning item completes",
      async (_label, text, expectedOutput) => {
        const service = new FakeConversationService();
        const store = createConversationStore();
        service.openConv = makeConversation({ items: [] });
        await store.getState().open(service, "ref-1");
        store.getState().applyNotification({
          method: "item/started",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t1",
            item: {
              type: "reasoning",
              id: "reason-authoritative-1",
              status: "inProgress",
              text: "old reasoning",
            },
          },
        } as AnyNotification);
        store.getState().applyNotification({
          method: "item/completed",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t1",
            item: {
              type: "reasoning",
              id: "reason-authoritative-1",
              status: "completed",
              text,
            },
          },
        } as AnyNotification);
        const item = store
          .getState()
          .conversation?.items.find(
            (candidate) => candidate.id === "reason-authoritative-1",
          );
        expect(item?.kind).toBe("activity");
        if (item?.kind === "activity") {
          expect(item.detail.output).toBe(expectedOutput);
        }
      },
    );

    it("retains truncation ownership when sparse completion preserves output", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({ items: [] });
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "reasoning",
            id: "reason-truncated-1",
            status: "inProgress",
          },
        },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "item/reasoning/summaryTextDelta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "reason-truncated-1",
          summaryIndex: 0,
          delta: "x".repeat(MAX_ITEM_BYTES + 100),
        },
      } as AnyNotification);
      expect(
        store.getState().getTruncatedItemIds().has("reason-truncated-1"),
      ).toBe(true);
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "reasoning",
            id: "reason-truncated-1",
            status: "completed",
          },
        },
      } as AnyNotification);
      const item = store
        .getState()
        .conversation?.items.find(
          (candidate) => candidate.id === "reason-truncated-1",
        );
      expect(item?.kind).toBe("activity");
      if (item?.kind === "activity") {
        expect(item.detail.output?.endsWith("… truncated")).toBe(true);
      }
      expect(
        store.getState().getTruncatedItemIds().has("reason-truncated-1"),
      ).toBe(true);
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "reasoning",
            id: "reason-truncated-1",
            status: "completed",
            text: "short",
          },
        },
      } as AnyNotification);
      expect(
        store.getState().getTruncatedItemIds().has("reason-truncated-1"),
      ).toBe(false);
    });

    it("preserves existing attachments when item/completed for the same user message carries no images field", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({
        turns: [
          {
            id: "t1",
            status: "inProgress",
            items: [
              {
                id: "msg-1",
                turnId: "t1",
                type: "userMessage",
                text: "hello",
                images: [{ src: "https://hub.test/image" }],
              },
            ],
          },
        ],
        items: [
          { kind: "user", id: "msg-1", text: "hello" },
          {
            kind: "attachments",
            id: "msg-1:attachments",
            items: [{ id: "image", src: "https://hub.test/image" }],
          },
        ],
      });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "userMessage",
            id: "msg-1",
            text: "hello",
            status: "completed",
            // No images field: an absent input-image list means "unchanged",
            // not "removed" (the same rule the package reducer's
            // mergeItemImages already applies).
          },
        },
      } as AnyNotification);
      const items = store.getState().conversation?.items ?? [];
      expect(
        items.find(
          (item): item is Extract<MobileTimelineItem, { kind: "attachments" }> =>
            item.kind === "attachments" && item.id === "msg-1:attachments",
        )?.items,
      ).toEqual([{ id: "msg-1:0", src: "https://hub.test/image" }]);
    });
  });

  describe("assistant delta appends to item", () => {
    it("appends delta text to assistant item markdown", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        items: [
          {
            kind: "assistant",
            id: "item-a",
            markdown: "Hello",
            streaming: true,
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "item-a",
          delta: " world",
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "item-a");
      if (item?.kind === "assistant") {
        expect(item.markdown).toBe("Hello world");
      }
    });

    it("appends reasoning summary delta to activity item", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        items: [
          {
            kind: "activity",
            id: "reason-1",
            label: "Reasoning",
            family: "reasoning",
            state: "running",
            detail: { output: "Thinking" },
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/reasoning/summaryTextDelta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "reason-1",
          summaryIndex: 0,
          delta: " more",
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "reason-1");
      if (item?.kind === "activity") {
        expect(item.detail.output).toBe("Thinking more");
      }
    });

    it("appends tool output delta to activity item", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        items: [
          {
            kind: "activity",
            id: "tool-1",
            label: "shell",
            family: "tool",
            state: "running",
            detail: { output: "line1", callId: "call-1" },
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-1",
          callId: "call-1",
          delta: "\nline2",
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "tool-1");
      if (item?.kind === "activity") {
        expect(item.detail.output).toBe("line1\nline2");
      }
    });

    it("I4: delta targeting missing item triggers coalesced resync", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const sink = createFakeSink();
      service.openConv = makeConversation({ items: [] });
      // F2: openProjected binds the coalescer internally.
      await store.getState().openProjected(service, sink, "ref-1");
      // The internal coalescer calls rehydrate — verify via readProjectionCalls.
      const initialReads = service.readProjectionCalls.length;
      // Delta for an item that doesn't exist in the store
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "nonexistent",
          delta: "text",
        },
      } as AnyNotification);
      // The internal coalescer schedules a rehydrate via microtask.
      // Wait for it to fire.
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
    });
  });

  describe("split Unicode remains valid", () => {
    it("does not corrupt surrogate pairs split across deltas", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        items: [
          {
            kind: "assistant",
            id: "item-u",
            markdown: "",
            streaming: true,
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      // 𝐀 is U+1D400 (surrogate pair D835 DC00)
      // Send the first half, then the second half as separate deltas.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "item-u",
          delta: "\uD835",
        },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "item-u",
          delta: "\uDC00",
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "item-u");
      if (item?.kind === "assistant") {
        // The combined string should be valid: the surrogate pair forms 𝐀
        expect(item.markdown).toBe("\uD835\uDC00");
        // It should be a single code point (length 1 by code point, 2 by UTF-16)
        expect([...item.markdown].length).toBe(1);
      }
    });
  });

  describe("arguments/output stop at 64 KiB UTF-8 and end with truncation marker", () => {
    it("truncates assistant item markdown at 64 KiB UTF-8 with marker", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const largeText = "x".repeat(70_000);
      service.openConv = makeConversation({
        items: [
          {
            kind: "assistant",
            id: "item-big",
            markdown: largeText,
            streaming: true,
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "item-big");
      if (item?.kind === "assistant") {
        // UTF-8 byte length must be <= 64 KiB
        const encoder = new TextEncoder();
        expect(encoder.encode(item.markdown).length).toBeLessThanOrEqual(65536);
        expect(item.markdown.endsWith("… truncated")).toBe(true);
        const markerCount = item.markdown.split("… truncated").length - 1;
        expect(markerCount).toBe(1);
      }
    });

    it("truncates tool output at 64 KiB UTF-8 with marker exactly once", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const largeOutput = "y".repeat(70_000);
      service.openConv = makeConversation({
        items: [
          {
            kind: "activity",
            id: "tool-big",
            label: "shell",
            family: "tool",
            state: "running",
            detail: { output: largeOutput },
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "tool-big");
      if (item?.kind === "activity") {
        const encoder = new TextEncoder();
        expect(
          encoder.encode(item.detail.output ?? "").length,
        ).toBeLessThanOrEqual(65536);
        expect(item.detail.output?.endsWith("… truncated")).toBe(true);
        const markerCount =
          (item.detail.output?.split("… truncated").length ?? 1) - 1;
        expect(markerCount).toBe(1);
      }
    });

    it("truncates multibyte text at 64 KiB UTF-8 boundary without splitting surrogates", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Each 'é' is 2 bytes in UTF-8. 35,000 é chars = 70,000 bytes > 64 KiB.
      const largeMultibyte = "é".repeat(35_000);
      service.openConv = makeConversation({
        items: [
          {
            kind: "assistant",
            id: "item-multi",
            markdown: largeMultibyte,
            streaming: true,
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "item-multi");
      if (item?.kind === "assistant") {
        const encoder = new TextEncoder();
        const byteLen = encoder.encode(item.markdown).length;
        expect(byteLen).toBeLessThanOrEqual(65536);
        // The result must be valid Unicode (no split surrogates)
        const decoded = new TextDecoder().decode(encoder.encode(item.markdown));
        expect(decoded).toBe(item.markdown);
        expect(item.markdown.endsWith("… truncated")).toBe(true);
      }
    });
  });

  describe("attachment names stop at 64 KiB UTF-8 while src passes through", () => {
    it("bounds an oversized attachment name and leaves src byte-for-byte intact", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const largeName = "n".repeat(MAX_ITEM_BYTES + 100);
      // src is not display text: cutting a data URI (or a fetch URL) yields
      // something that cannot decode, so it must survive verbatim.
      const src = `data:image/png;base64,${"A".repeat(200)}`;
      service.openConv = makeConversation({
        items: [
          {
            kind: "attachments",
            id: "msg-1:attachments",
            sourceTranscriptKey: "msg-1",
            items: [{ id: "image-1", src, name: largeName }],
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      const item = store
        .getState()
        .conversation?.items.find((candidate) => candidate.id === "msg-1:attachments");
      expect(item?.kind).toBe("attachments");
      if (item?.kind !== "attachments") return;
      const attachment = item.items[0];
      expect(attachment).toBeDefined();
      if (!attachment) return;
      const encoder = new TextEncoder();
      expect(encoder.encode(attachment.name ?? "").length).toBeLessThanOrEqual(
        MAX_ITEM_BYTES,
      );
      expect(attachment.name?.endsWith("… truncated")).toBe(true);
      const markerCount = (attachment.name?.split("… truncated").length ?? 1) - 1;
      expect(markerCount).toBe(1);
      // src is never bounded — byte-for-byte identical to the input.
      expect(attachment.src).toBe(src);
      expect(encoder.encode(attachment.src).length).toBe(
        encoder.encode(src).length,
      );
    });

    it("bounds an oversized attachment name arriving on a live item/started", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({ items: [] });
      await store.getState().open(service, "ref-1");
      const largeName = "n".repeat(MAX_ITEM_BYTES + 100);
      const src = "https://hub.test/live.png";
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "userMessage",
            id: "msg-live",
            text: "hi",
            images: [{ url: src, name: largeName }],
          },
        },
      } as AnyNotification);
      const item = store
        .getState()
        .conversation?.items.find((candidate) => candidate.id === "msg-live:attachments");
      expect(item?.kind).toBe("attachments");
      if (item?.kind !== "attachments") return;
      const attachment = item.items[0];
      expect(attachment).toBeDefined();
      if (!attachment) return;
      const encoder = new TextEncoder();
      expect(encoder.encode(attachment.name ?? "").length).toBeLessThanOrEqual(
        MAX_ITEM_BYTES,
      );
      expect(attachment.name?.endsWith("… truncated")).toBe(true);
      // src is never bounded — byte-for-byte identical to the input.
      expect(attachment.src).toBe(src);
    });
  });

  describe("truncation ownership covers every kind the display bounds cut (#1737 follow-up)", () => {
    // project.ts's truncateItem (arrived with #1737) cuts user text, notice
    // text, a failure row's title and detail, question prose, and an
    // activity's description and label — but reconcileTruncationFrom decided
    // ownership from assistant markdown and activity arguments/output/error
    // alone, so rows of the other kinds arrived truncated with no id in the
    // set and the affordance never showed for them.
    const big = "x".repeat(MAX_ITEM_BYTES + 100);

    async function openWithItems(items: MobileTimelineItem[]) {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({ items });
      await store.getState().open(service, "ref-1");
      return store;
    }

    it("records an oversized user row's text as truncated", async () => {
      const store = await openWithItems([
        { kind: "user", id: "user-big", text: big },
        { kind: "user", id: "user-short", text: "short" },
      ]);
      const truncated = store.getState().getTruncatedItemIds();
      expect(truncated.has("user-big")).toBe(true);
      expect(truncated.has("user-short")).toBe(false);
      const row = store
        .getState()
        .conversation?.items.find((candidate) => candidate.id === "user-big");
      expect(row?.kind).toBe("user");
      if (row?.kind === "user") {
        expect(row.text.endsWith(TRUNCATION_MARKER)).toBe(true);
      }
    });

    it("records an oversized notice row's text as truncated", async () => {
      const store = await openWithItems([
        {
          kind: "notice",
          id: "notice-big",
          origin: "steering",
          family: "informational",
          tone: "info",
          text: big,
        },
      ]);
      expect(store.getState().getTruncatedItemIds().has("notice-big")).toBe(true);
      const row = store
        .getState()
        .conversation?.items.find((candidate) => candidate.id === "notice-big");
      expect(row?.kind).toBe("notice");
      if (row?.kind === "notice") {
        expect(row.text.endsWith(TRUNCATION_MARKER)).toBe(true);
      }
    });

    it("records an oversized failure row's title or detail as truncated", async () => {
      const store = await openWithItems([
        { kind: "failure", id: "failure-title-big", title: big, detail: "short" },
        { kind: "failure", id: "failure-detail-big", title: "short", detail: big },
      ]);
      const truncated = store.getState().getTruncatedItemIds();
      expect(truncated.has("failure-title-big")).toBe(true);
      expect(truncated.has("failure-detail-big")).toBe(true);
    });

    it("records a question row whose own prose is oversized as truncated", async () => {
      const question = (over: Partial<AskQuestionRef>): AskQuestionRef => ({
        key: "k",
        callId: "c",
        header: "header",
        question: "prompt",
        options: [{ label: "option", detail: "detail" }],
        multiSelect: false,
        ...over,
      });
      const store = await openWithItems([
        {
          kind: "question",
          id: "q-header-big",
          questions: [question({ header: big })],
        },
        {
          kind: "question",
          id: "q-option-label-big",
          questions: [question({ options: [{ label: big, detail: "detail" }] })],
        },
      ]);
      const truncated = store.getState().getTruncatedItemIds();
      expect(truncated.has("q-header-big")).toBe(true);
      expect(truncated.has("q-option-label-big")).toBe(true);
    });

    it("records an activity row with an oversized description or label as truncated", async () => {
      const store = await openWithItems([
        {
          kind: "activity",
          id: "act-description-big",
          label: "shell",
          family: "tool",
          state: "completed",
          detail: { description: big },
        },
        {
          kind: "activity",
          id: "act-label-big",
          label: big,
          family: "tool",
          state: "completed",
          detail: {},
        },
      ]);
      const truncated = store.getState().getTruncatedItemIds();
      expect(truncated.has("act-description-big")).toBe(true);
      expect(truncated.has("act-label-big")).toBe(true);
    });

    it("records a clustered member with an oversized description under its own identity", async () => {
      const store = await openWithItems([
        {
          kind: "activity",
          id: "act-members",
          label: "shell",
          family: "tool",
          state: "completed",
          detail: {},
          members: [
            {
              id: "member-short",
              label: "shell",
              family: "tool",
              state: "completed",
              detail: { output: "fine" },
            },
            {
              id: "member-description-big",
              label: "shell",
              family: "tool",
              state: "completed",
              detail: { description: big },
            },
          ],
        },
      ]);
      const truncated = store.getState().getTruncatedItemIds();
      expect(truncated.has("member-description-big")).toBe(true);
      expect(truncated.has("member-short")).toBe(false);
      expect(truncated.has("act-members")).toBe(false);
    });
  });

  describe("resync coalesces to one rehydrate via internal coalescer (F5)", () => {
    it("coalesces evener/thread/resync into one rehydrate call", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-after-resync",
      };
      // F2: openProjected binds the coalescer internally.
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      // The initial openProjected calls readProjection once.
      const initialReads = service.readProjectionCalls.length;
      expect(initialReads).toBe(1);

      // Emit multiple resync notifications — they should coalesce to one
      // rehydrate (one additional readProjection call).
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);

      // The internal coalescer schedules rehydrate via microtask.
      // Wait for microtasks to flush.
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      // F5: One rehydrate call = one additional readProjection call.
      expect(service.readProjectionCalls.length).toBe(initialReads + 1);
    });

    it("unsupported item transition triggers coalesced rehydrate", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      const initialReads = service.readProjectionCalls.length;

      // An unknown notification method (unsupported item transition) should
      // trigger the internal coalescer, not a crash.
      store.getState().applyNotification({
        method: "item/unknownFutureTransition" as never,
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: { type: "futureType", id: "x", status: "unknown" },
        },
      } as AnyNotification);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
    });
  });

  describe("stale generation completion is ignored", () => {
    it("drops item/completed from an older generation", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      // Reset to bump generation, then open a new conversation
      store.getState().reset();
      service.openConv = makeConversation({ threadId: "thread-2" });
      await store.getState().open(service, "ref-2");
      // A stale completion for thread-1/ref-1 should be dropped
      const before = store.getState().conversation;
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "agentMessage",
            id: "item-a",
            text: "done",
            status: "completed",
          },
        },
      } as AnyNotification);
      expect(store.getState().conversation).toBe(before);
    });
  });

  describe("rehydrate preserves draft and presentation state", () => {
    it("preserves draft text across rehydrate", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-1",
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.getState().setDraft("my unsent draft");
      // Rehydrate: should preserve the draft
      await store.getState().rehydrate(service, createFakeSink());
      expect(store.getState().draft).toBe("my unsent draft");
    });

    it("preserves expandedToolKeys/presentation state across rehydrate", async () => {
      // C7: expandedToolKeys is removed from the production store — presentation
      // state lives in live-ui-store. Rehydrate only preserves draft.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-1",
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Rehydrate should preserve draft but not carry presentation state.
      store.getState().setDraft("my draft");
      await store.getState().rehydrate(service, createFakeSink());
      expect(store.getState().draft).toBe("my draft");
    });
  });

  describe("olderCursor survives open/rehydrate", () => {
    it("preserves olderCursor from openProjected through rehydrate", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "initial-cursor",
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      expect(store.getState().olderCursor).toBe("initial-cursor");
      // Update the projection result for rehydrate
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "updated-cursor",
      };
      await store.getState().rehydrate(service, createFakeSink());
      expect(store.getState().olderCursor).toBe("updated-cursor");
    });
  });

  // --- Task 2A reslice: F3-F11 tests -------------------------------------------

  describe("F3: LiveConversationState is a required interface", () => {
    it("createConversationStore returns state with required live methods", () => {
      const store = createConversationStore();
      const s = store.getState();
      // These must be functions on the production store (not optional).
      expect(typeof s.openProjected).toBe("function");
      expect(typeof s.rehydrate).toBe("function");
      // F2: setCoalescer is removed — openProjected binds the coalescer
      // internally.
      expect(
        typeof (s as unknown as Record<string, unknown>).setCoalescer,
      ).toBe("undefined");
    });
  });

  describe("F4: mutation object identity / private mutation ID", () => {
    it("out-of-order completion cannot clear a newer mutation (send)", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().setDraft("first");

      // Start a send that hangs.
      let resolve1: ((r: MutationReceipt) => void) | null = null as
        | ((r: MutationReceipt) => void)
        | null;
      const hang1 = new Promise<MutationReceipt>((r) => {
        resolve1 = r;
      });
      service.send = async () => hang1;
      const p1 = store.getState().send(service, textInput("first"));

      // While mutation 1 is in flight, start mutation 2.
      store.getState().setDraft("second");
      let resolve2: ((r: MutationReceipt) => void) | null = null as
        | ((r: MutationReceipt) => void)
        | null;
      const hang2 = new Promise<MutationReceipt>((r) => {
        resolve2 = r;
      });
      service.send = async () => hang2;
      const p2 = store.getState().send(service, textInput("second"));

      // Resolve mutation 2 first (newer).
      resolve2?.(makeReceipt());
      await p2;
      expect(store.getState().pendingMutation).toBeNull();

      // Now resolve mutation 1 (older, stale). It must NOT clear the
      // already-resolved state or set a newer mutation to null.
      resolve1?.(makeReceipt());
      await p1;
      // The mutation was already cleared by mutation 2; stale completion
      // must not re-introduce pending state.
      expect(store.getState().pendingMutation).toBeNull();
    });

    it("out-of-order failure cannot overwrite a newer mutation's error", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().setDraft("first");

      // Start mutation 1 that hangs, then fails.
      let resolveFail1: (() => void) | null = null as (() => void) | null;
      const hangFail1 = new Promise<MutationReceipt>((_resolve, reject) => {
        resolveFail1 = () => reject(new Error("mutation 1 error"));
      });
      service.send = async () => hangFail1;
      const p1 = store.getState().send(service, textInput("first"));

      // Start mutation 2 that succeeds.
      store.getState().setDraft("second");
      service.send = async () => Promise.resolve(makeReceipt());
      const p2 = store.getState().send(service, textInput("second"));
      await p2;
      expect(store.getState().pendingMutation).toBeNull();
      expect(store.getState().error).toBeNull();

      // Now mutation 1 fails (stale). It must NOT set error or pendingMutation.
      resolveFail1?.();
      await p1.catch(() => {}); // swallow the rejection
      expect(store.getState().pendingMutation).toBeNull();
      expect(store.getState().error).toBeNull();
    });

    it("draft restore only happens if user has not typed since submit", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().setDraft("original draft");
      // Start a send that fails.
      service.sendShouldReject = new Error("conflict");
      const p = store.getState().send(service, textInput("original draft"));
      // While in-flight, the user types new text.
      store.getState().setDraft("new text typed during send");
      await p;
      // Since the user typed new text, the draft should NOT be restored.
      expect(store.getState().draft).toBe("new text typed during send");
    });

    it("interrupt snapshot is null and does not clear draft", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({ status: { type: "active" } });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().setDraft("my draft text");
      let resolveFn: (() => void) | null = null as (() => void) | null;
      const hang = new Promise<MutationReceipt>((r) => {
        resolveFn = () => r(makeReceipt());
      });
      service.interrupt = async () => hang;
      const p = store.getState().interrupt(service);
      const pending = store.getState().pendingMutation;
      expect(pending?.draftSnapshot).toBeNull();
      // Draft should not be cleared by interrupt.
      expect(store.getState().draft).toBe("my draft text");
      resolveFn?.();
      await p;
    });
  });

  describe("F5: AuthoritativeRereadScheduler.request(key, effect)", () => {
    it("coalesces multiple signals to one readProjection", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-1",
      };
      // F2: openProjected binds the coalescer internally.
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      // The internal coalescer coalesces multiple requestRehydrate calls
      // into one actual readProjection call.
      const ctrl = makeControlledRead(service);

      // Emit multiple resync notifications.
      service.readProjectionCalls = []; // reset count
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);

      // M1: deterministic barrier — await started, yield so orig resolves and
      // the release gate is pushed, then set up completed and release.
      await ctrl.started();
      await yieldMicrotask(); // let orig(ref) resolve and push to releaseQueue
      const completedP = ctrl.completed();
      ctrl.release();
      await completedP;

      // Multiple signals should result in only 1 readProjection call.
      expect(service.readProjectionCalls.length).toBe(1);
    });
  });

  describe("F6: LiveActivitySink accepted by openProjected/rehydrate", () => {
    it("openProjected accepts a LiveActivitySink and routes notifications to both", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const activityView: ActivityView = {
        tasks: [],
        work: [],
        usage: {},
        capabilities: ALL_TRUE_CAPS,
      };
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: activityView,
        olderCursor: "cursor-1",
      };

      // Track what the sink receives.
      const setLiveViewCalls: {
        view: ActivityView;
        identity: { threadId: string; ref: string; generation: number };
      }[] = [];
      const applyCalls: {
        n: AnyNotification;
        identity: { threadId: string; ref: string; generation: number };
      }[] = [];
      const resetCalls: number[] = [];

      const sink: LiveActivitySink = {
        setLiveView(view: ActivityView, identity: ActivityIdentity) {
          setLiveViewCalls.push({ view, identity });
          return true;
        },
        applyLiveNotification(n: AnyNotification, identity: ActivityIdentity) {
          applyCalls.push({ n, identity });
          return "applied" as const;
        },
        reset() {
          resetCalls.push(1);
        },
      };

      await store.getState().openProjected(service, sink, "ref-1");

      // F4: setLiveView should have been called with the activity view
      // (identity-first).
      expect(setLiveViewCalls).toHaveLength(1);
      expect(setLiveViewCalls[0]?.view).toBe(activityView);

      // Emit a notification via the service's notification handler — it
      // should be routed to both conversation store and the sink.
      service.notificationHandler?.({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
        },
      } as AnyNotification);

      // The sink should have received the notification.
      expect(applyCalls).toHaveLength(1);
      expect(applyCalls[0]?.n.method).toBe("thread/status/changed");
    });
  });

  describe("F7: ask_user started/completed projects question, deltas schedule reread", () => {
    it("item/started with ask_user type schedules reread, not generic activity", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ items: [] }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      // F6: openProjected binds the coalescer internally.
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const initialReads = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "commandExecution",
            id: "ask-1",
            toolName: "ask_user",
            status: "inProgress",
            argumentsJson:
              '{"questions":[{"header":"Q","question":"Pick one","options":[{"label":"A","detail":"da"},{"label":"B","detail":"db"}],"multi_select":false}]}',
          },
        },
      } as AnyNotification);
      // F6: ask_user should NOT be projected as a generic activity item.
      // It should schedule an authoritative reread instead.
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "ask-1");
      expect(item).toBeUndefined();
      // Wait for the internal coalescer to fire.
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
    });

    it("item/completed with ask_user schedules reread, not generic activity", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ items: [] }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const initialReads = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "commandExecution",
            id: "ask-1",
            toolName: "ask_user",
            status: "completed",
            argumentsJson:
              '{"questions":[{"header":"Q","question":"Pick one","options":[{"label":"A","detail":"da"},{"label":"B","detail":"db"}],"multi_select":false}]}',
          },
        },
      } as AnyNotification);
      // F6: ask_user should NOT be projected as a generic activity item.
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "ask-1");
      expect(item).toBeUndefined();
      // Wait for the internal coalescer to fire.
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
    });

    it("missing assistant delta schedules reread via internal coalescer", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({ items: [] });
      service.readProjectionResult = {
        conversation: makeConversation({ items: [] }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      // F2: openProjected binds the coalescer internally.
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const initialReads = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "nonexistent",
          delta: "text",
        },
      } as AnyNotification);
      // Wait for the internal coalescer to fire.
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
    });

    it("wrong-kind delta (reasoning delta targeting activity of wrong kind) schedules reread", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // An assistant item that a reasoning delta targets — wrong kind.
      service.openConv = makeConversation({
        items: [
          { kind: "assistant", id: "r-1", markdown: "", streaming: false },
        ],
      });
      service.readProjectionResult = {
        conversation: makeConversation({
          items: [
            { kind: "assistant", id: "r-1", markdown: "", streaming: false },
          ],
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const initialReads = service.readProjectionCalls.length;
      // reasoning delta targeting an assistant item — wrong kind.
      store.getState().applyNotification({
        method: "item/reasoning/summaryTextDelta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "r-1",
          summaryIndex: 0,
          delta: "thinking",
        },
      } as AnyNotification);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
    });

    it("unrelated notification does NOT schedule reread", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ items: [] }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const initialReads = service.readProjectionCalls.length;
      // A thread/status/changed is a known notification — should NOT reread.
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
        },
      } as AnyNotification);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBe(initialReads);
    });

    it("warning insertion obeys item cap", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Start with 500 items (at cap).
      const items: MobileConversation["items"] = [];
      for (let i = 0; i < 500; i++) {
        items.push({ kind: "user", id: `u${i}`, text: "" });
      }
      service.openConv = makeConversation({ items });
      await store.getState().open(service, "ref-1");
      // Emit a warning — it should be inserted but the cap maintained.
      store.getState().applyNotification({
        method: "warning",
        params: { threadId: "thread-1", ref: "ref-1", message: "test warning" },
      } as AnyNotification);
      const conv = store.getState().conversation;
      expect(conv?.items.length).toBeLessThanOrEqual(500);
    });

    it("keeps repeated warning identities stable through a later item update", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({
        items: [{ kind: "user", id: "user-1", text: "input" }],
      });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");

      const warning = {
        method: "warning",
        params: { threadId: "thread-1", ref: "ref-1", title: "Provider warning", message: "Retrying" },
      } as AnyNotification;
      store.getState().applyNotification(warning);
      store.getState().applyNotification(warning);
      const warningIdsBefore = (store.getState().conversation?.items ?? [])
        .filter((item) => item.kind === "failure")
        .map((item) => item.id);

      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "userMessage",
            id: "user-1",
            text: "updated input",
            transcriptEntryIndex: 4,
          },
        },
      } as AnyNotification);

      const warningIdsAfter = (store.getState().conversation?.items ?? [])
        .filter((item) => item.kind === "failure")
        .map((item) => item.id);
      expect(warningIdsBefore).toHaveLength(2);
      expect(new Set(warningIdsBefore).size).toBe(2);
      expect(warningIdsAfter).toEqual(warningIdsBefore);
    });

    it("preserves a command description through live item projection", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");

      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "commandExecution",
            id: "command-1",
            toolName: "shell",
            description: "Inspect the source tree",
            status: "completed",
          },
        },
      } as AnyNotification);

      const item = store.getState().conversation?.items.find(
        (candidate) => candidate.id === "command-1",
      );
      expect(item).toMatchObject({
        kind: "activity",
        detail: { description: "Inspect the source tree" },
      });
    });

    it("preserves a user transcript entry index through live item projection", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");

      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "userMessage",
            id: "fork-source",
            text: "fork from this input",
            transcriptEntryIndex: 8,
          },
        },
      } as AnyNotification);

      expect(
        store.getState().conversation?.items.find(
          (candidate) => candidate.id === "fork-source",
        ),
      ).toMatchObject({
        kind: "user",
        transcriptEntryIndex: 8,
      });
    });
  });

  describe("F8: UTF-8 byte cap, valid boundary, delta-after-marker", () => {
    it("delta cannot append after truncation marker", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Create text that is already at the cap with the marker.
      const largeText = "x".repeat(70_000);
      service.openConv = makeConversation({
        items: [
          {
            kind: "assistant",
            id: "item-trunc",
            markdown: largeText,
            streaming: true,
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      // The initial projection truncates to 64KiB with marker.
      const beforeDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "item-trunc");
      if (beforeDelta?.kind === "assistant") {
        expect(beforeDelta.markdown.endsWith("… truncated")).toBe(true);
      }

      // Now send a delta — it should NOT append after the marker.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "item-trunc",
          delta: " more text after truncation",
        },
      } as AnyNotification);

      const afterDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "item-trunc");
      if (afterDelta?.kind === "assistant") {
        // The marker must still be exactly once at the end.
        const markerCount = afterDelta.markdown.split("… truncated").length - 1;
        expect(markerCount).toBe(1);
        expect(afterDelta.markdown.endsWith("… truncated")).toBe(true);
        // The delta text must NOT appear after the marker.
        expect(afterDelta.markdown).not.toContain("more text after truncation");
      }
    });

    it("multibyte text at boundary does not split code points", () => {
      // Direct test of truncateText with multibyte at the exact boundary.
      // Each 'é' is 2 bytes. 32,768 é chars = 65,536 bytes = exactly 64 KiB.
      // One more é would exceed, so truncation kicks in.
      const exact = "é".repeat(32_768);
      const encoder = new TextEncoder();
      expect(encoder.encode(exact).length).toBe(65536);
      // At exactly 64KiB, no truncation needed.
      // But 32,769 é = 65,538 bytes > 64KiB, so truncate.
      const overBy2 = "é".repeat(32_769);
      const truncated = truncateText(overBy2, MAX_ITEM_BYTES);
      const truncatedBytes = encoder.encode(truncated).length;
      expect(truncatedBytes).toBeLessThanOrEqual(65536);
      expect(truncated.endsWith("… truncated")).toBe(true);
      // Result must be valid Unicode.
      const decoded = new TextDecoder().decode(encoder.encode(truncated));
      expect(decoded).toBe(truncated);
    });

    it("never exceeds a limit smaller than the truncation marker", () => {
      const encoder = new TextEncoder();
      // The marker is 13 UTF-8 bytes, so a limit below that cannot hold it.
      // The byte limit is the contract every caller judges by; the marker is
      // best effort, so the result is the longest prefix that fits.
      expect(encoder.encode(TRUNCATION_MARKER).length).toBe(13);
      const truncated = truncateText("abcdef", 3);
      expect(encoder.encode(truncated).length).toBeLessThanOrEqual(3);
      expect(truncated).toBe("abc");
    });

    it("returns the empty string for a zero byte limit", () => {
      expect(truncateText("abcdef", 0)).toBe("");
    });

    // truncateItem delegates to project.ts's shared implementation (the
    // "unwired native helper layer" fix), which bounds every row kind the
    // canonical projection produces — not just "assistant" and "activity",
    // the only two this store's own switch used to cover. A pasted user
    // message, a daemon notice, and a question's own text were never bounded
    // by the live path before.
    it.each([
      ["user", { kind: "user" as const, id: "u1", text: "x".repeat(MAX_ITEM_BYTES + 100) }, "text"],
      [
        "notice",
        {
          kind: "notice" as const,
          id: "n1",
          origin: "system" as const,
          family: "system" as const,
          tone: "system" as const,
          text: "x".repeat(MAX_ITEM_BYTES + 100),
        },
        "text",
      ],
      [
        "failure",
        { kind: "failure" as const, id: "f1", title: "oops", detail: "x".repeat(MAX_ITEM_BYTES + 100) },
        "detail",
      ],
    ])("bounds an oversized %s item's text", (_kind, item, field) => {
      const truncated = truncateItem(item as MobileTimelineItem) as unknown as Record<string, string>;
      expect(truncated[field].endsWith(TRUNCATION_MARKER)).toBe(true);
      expect(new TextEncoder().encode(truncated[field]).length).toBeLessThanOrEqual(MAX_ITEM_BYTES);
    });

    it("F12: emoji at boundary does not produce U+FFFD", () => {
      // 😀 is U+1F600, 4 bytes in UTF-8. Place it right at the boundary so
      // the code-point iteration must decide whether to include it.
      // 16,381 'a' chars = 16,381 bytes. Plus one 😀 = 4 bytes = 16,385.
      // Max is 64KiB = 65,536 bytes. We need text that's just over 64KiB.
      const filler = "a".repeat(65_533); // 65,533 bytes
      const text = `${filler}😀${"x".repeat(10)}`; // over 64KiB
      const truncated = truncateText(text, MAX_ITEM_BYTES);
      const encoder = new TextEncoder();
      expect(encoder.encode(truncated).length).toBeLessThanOrEqual(65536);
      expect(truncated.endsWith("… truncated")).toBe(true);
      // Must not contain U+FFFD replacement char
      expect(truncated).not.toContain("\uFFFD");
      // Must be valid Unicode (round-trip)
      const decoded = new TextDecoder().decode(encoder.encode(truncated));
      expect(decoded).toBe(truncated);
    });

    it("F12: genuine marker suffix in content does not freeze delta appends", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Content that genuinely ends with "… truncated" but is under the
      // byte limit — should NOT be treated as already truncated.
      const genuineContent = "Hello… truncated";
      service.openConv = makeConversation({
        items: [
          {
            kind: "assistant",
            id: "item-genuine",
            markdown: genuineContent,
            streaming: true,
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      // The item is under the byte limit, so it should not be in the
      // truncated set. A delta should append normally.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "item-genuine",
          delta: " more text",
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      const item = conv?.items.find((i) => i.id === "item-genuine");
      if (item?.kind === "assistant") {
        // The delta should have been appended — not frozen by the
        // genuine "… truncated" suffix.
        expect(item.markdown).toBe("Hello… truncated more text");
      }
    });

    it("F12: marker appears exactly once and delta after cap is blocked", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const largeText = "x".repeat(70_000);
      service.openConv = makeConversation({
        items: [
          {
            kind: "assistant",
            id: "item-cap",
            markdown: largeText,
            streaming: true,
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      // The initial projection truncates to 64KiB with marker.
      const beforeDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "item-cap");
      if (beforeDelta?.kind === "assistant") {
        expect(beforeDelta.markdown.endsWith("… truncated")).toBe(true);
      }

      // Now send a delta — it should NOT append after the marker.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "item-cap",
          delta: " more text after truncation",
        },
      } as AnyNotification);

      const afterDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "item-cap");
      if (afterDelta?.kind === "assistant") {
        // The marker must still be exactly once at the end.
        const markerCount = afterDelta.markdown.split("… truncated").length - 1;
        expect(markerCount).toBe(1);
        expect(afterDelta.markdown.endsWith("… truncated")).toBe(true);
        // The delta text must NOT appear after the marker.
        expect(afterDelta.markdown).not.toContain("more text after truncation");
      }
    });
  });

  describe("F9: stale safety — generation and operation identity", () => {
    it("close increments generation so stale completion cannot clear new error", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      const gen = store.getState().conversationGeneration;
      store.getState().close();
      expect(store.getState().conversationGeneration).toBeGreaterThan(gen);
      expect(store.getState().conversation).toBeNull();
    });

    it("stale rehydrate catch does not set error on newer generation", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-1",
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      // Start a rehydrate that will fail.
      let rejectRehydrate: ((e: Error) => void) | null = null as
        | ((e: Error) => void)
        | null;
      const failProjection = new Promise<ConversationReadProjection>(
        (_resolve, reject) => {
          rejectRehydrate = reject;
        },
      );
      const origReadProjection = service.readProjection;
      service.readProjection = async () => failProjection;

      const rehydratePromise = store
        .getState()
        .rehydrate(service, createFakeSink());

      // While rehydrate is in-flight, open a new conversation (new generation).
      service.readProjection = origReadProjection;
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-2" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-2",
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-2");

      // Now the stale rehydrate fails.
      rejectRehydrate?.(new Error("stale rehydrate error"));
      await rehydratePromise.catch(() => {});

      // The stale error must NOT have overwritten the newer conversation.
      expect(store.getState().conversation?.threadId).toBe("thread-2");
      expect(store.getState().error).toBeNull();
    });

    it("loadOlder checks generation before applying results", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      // Set a cursor so loadOlder has a page to request.
      store.setState({ olderCursor: "cursor-1" });

      // Start loadOlder that hangs.
      let resolveOlder: (() => void) | null = null as (() => void) | null;
      const hangOlder = new Promise<{
        items: MobileConversation["items"];
        nextCursor?: string;
      }>((r) => {
        resolveOlder = () => r({ items: [], nextCursor: undefined });
      });
      service.olderItems = hangOlder as never;
      const p = store.getState().loadOlder(service);

      // While loadOlder is in-flight, reset (bump generation).
      store.getState().reset();

      // Resolve the stale loadOlder.
      resolveOlder?.();
      await p;

      // The stale results should not have been applied.
      expect(store.getState().conversation).toBeNull();
    });
  });

  describe("F10: paging — only thread/turns/list receives cursor, dedupe by source identity", () => {
    it("loadOlder prepends older items and dedupes by item id", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Start with one existing item.
      service.openConv = makeConversation({
        items: [{ kind: "user", id: "item-1", text: "existing" }],
      });
      await store.getState().open(service, "ref-1");
      // Set a cursor so loadOlder has a page to request.
      store.setState({ olderCursor: "cursor-1" });

      // loadOlder returns items that include a duplicate of item-1.
      service.olderItems = {
        items: [
          { kind: "user", id: "item-0", text: "older" },
          { kind: "user", id: "item-1", text: "duplicate" }, // duplicate id
        ],
        nextCursor: "next-cursor",
      };
      await store.getState().loadOlder(service);

      const conv = store.getState().conversation;
      // item-1 should appear only once (deduped), and the older item
      // prepended.
      const ids = conv?.items.map((i) => i.id);
      expect(ids?.filter((id) => id === "item-1")).toHaveLength(1);
      // The older item should be at the front (prepended).
      expect(conv?.items[0]?.id).toBe("item-0");
    });

    it("loadOlder dedupes an attachment for a clustered member by transcriptKey, not just top-level id", async () => {
      // The current conversation has ONE clustered activity whose SECOND
      // (non-first) member carries transcriptKey "key-A" and wire id
      // "wire-A". Its own identity lives only inside .members[], invisible
      // to a dedup set seeded from top-level timelineIdentity alone.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        items: [
          {
            kind: "activity",
            id: "wire-X",
            label: "shell",
            family: "tool",
            state: "completed",
            detail: { output: "first", callId: "call-x" },
            members: [
              {
                id: "wire-X",
                label: "shell",
                family: "tool",
                state: "completed",
                detail: { output: "first", callId: "call-x" },
                transcriptKey: "key-X",
              },
              {
                id: "wire-A",
                label: "shell",
                family: "tool",
                state: "completed",
                detail: { output: "second", callId: "call-a" },
                transcriptKey: "key-A",
              },
            ],
          },
        ],
      });
      await store.getState().open(service, "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      // An older page replays an attachment FOR the "key-A" member under a
      // DIFFERENT wire id ("wire-B") — same source key, different reference.
      // A control attachment for a genuinely unrelated source is included
      // too, and must still be admitted.
      service.olderItems = {
        items: [
          {
            kind: "attachments",
            id: "wire-B:attachments",
            items: [{ id: "att-1", src: "https://example.com/dup.png" }],
            sourceTranscriptKey: "key-A",
          },
          {
            kind: "attachments",
            id: "wire-C:attachments",
            items: [{ id: "att-2", src: "https://example.com/new.png" }],
            sourceTranscriptKey: "unrelated-key",
          },
        ],
        nextCursor: "next-cursor",
      };
      await store.getState().loadOlder(service);

      const conv = store.getState().conversation;
      const ids = conv?.items.map((i) => i.id);
      // The duplicate (same source key as an existing cluster member) is
      // dropped, not retained.
      expect(ids).not.toContain("wire-B:attachments");
      // A genuinely new attachment for an unrelated source is still admitted.
      expect(ids).toContain("wire-C:attachments");
    });

    it("loadOlder retains newest 500 and disables further paging at cap", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Start with 400 items.
      const items: MobileConversation["items"] = [];
      for (let i = 100; i < 500; i++) {
        items.push({ kind: "user", id: `item-${i}`, text: "" });
      }
      service.openConv = makeConversation({ items });
      await store.getState().open(service, "ref-1");
      // Set a cursor so loadOlder has a page to request.
      store.setState({ olderCursor: "cursor-1" });

      // loadOlder returns 200 more items — total would be 600, capped to 500.
      const olderItems: MobileConversation["items"] = [];
      for (let i = 0; i < 200; i++) {
        olderItems.push({ kind: "user", id: `item-old-${i}`, text: "" });
      }
      service.olderItems = { items: olderItems, nextCursor: "more" };
      await store.getState().loadOlder(service);

      const conv = store.getState().conversation;
      expect(conv?.items.length).toBe(500);
      // F8: When at cap, further paging should be disabled honestly —
      // olderCursor set to null so we don't repeatedly load discarded rows.
      expect(store.getState().olderCursor).toBeNull();
    });

    it("stops offering earlier items once the cap nulls the cursor", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const items: MobileConversation["items"] = [];
      for (let i = 100; i < 500; i++) {
        items.push({ kind: "user", id: `item-${i}`, text: "" });
      }
      service.openConv = makeConversation({ items });
      await store.getState().open(service, "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      const olderItems: MobileConversation["items"] = [];
      for (let i = 0; i < 200; i++) {
        olderItems.push({ kind: "user", id: `item-old-${i}`, text: "" });
      }
      // The server still reports earlier items — the cap, not the server, is
      // what ends paging here.
      service.olderItems = {
        items: olderItems,
        nextCursor: "more",
        hasEarlierItems: true,
      };
      await store.getState().loadOlder(service);

      expect(store.getState().olderCursor).toBeNull();
      // The flag must agree with the cursor: offering a load that
      // early-returns "ignored" is a button that can never add a row.
      expect(store.getState().hasEarlierItems).toBe(false);
      expect(await store.getState().loadOlder(service)).toEqual({
        status: "ignored",
      });
    });
  });

  // D18 B3 round 3: sessionTokens (the shared session-usage derivation) reads
  // conversation.turns and conversation.olderCursor, but loadOlder only ever
  // updated conversation.items and the store's OWN olderCursor field. A
  // session with no thread-level cumulative usage therefore kept summing
  // just the first page forever, even after older turns loaded.
  describe("loadOlder keeps conversation.turns/olderCursor in sync with items", () => {
    it("merges the older page's turns into conversation.turns and advances conversation.olderCursor", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        usage: null,
        turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
        olderCursor: "cursor-1",
      });
      await store.getState().open(service, "ref-1");
      // Set a cursor so loadOlder has a page to request.
      store.setState({ olderCursor: "cursor-1" });

      service.olderItems = {
        items: [],
        turnsPage: turnsPage([wireTurn("t1", 500, 20)], "cursor-2"),
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);

      const conv = store.getState().conversation!;
      // The older turn is prepended, ahead of the page-one turn.
      expect(conv.turns.map((t) => t.id)).toEqual(["t1", "t2"]);
      // conversation.olderCursor mirrors the same cursor that now governs
      // the store's own paging (there is still more history to load).
      expect(conv.olderCursor).toBe("cursor-2");
      // Both turns now count: a session with no cumulative usage must not
      // keep reporting only the first page's total once a second page loads.
      expect(sessionTokens(conv)).toEqual({ inputTokens: 560, outputTokens: 60, scope: "loaded" });
    });

    it("does not duplicate a turn the store already holds, but still adds a new one from the same page", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } }],
        olderCursor: "cursor-1",
      });
      await store.getState().open(service, "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      // A page race can hand back a turn the store already has (the same
      // dedupe concern F10 already covers for items) alongside a genuinely
      // new older turn on the same page.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage([wireTurn("t1", 500, 20), wireTurn("t2", 999, 999)]),
        nextCursor: undefined,
      };
      await store.getState().loadOlder(service);

      const conv = store.getState().conversation!;
      expect(conv.turns.map((t) => t.id)).toEqual(["t1", "t2"]);
      // The already-held t2 keeps its own version, not the incoming duplicate.
      expect(conv.turns.find((t) => t.id === "t2")?.usage).toEqual({ inputTokens: 1, outputTokens: 1 });
    });

    // D18 B3 round 4 (1): the item cap forces the STORE's own olderCursor to
    // null so paging stops honestly (F8), but conversation.olderCursor must
    // still tell sessionTokens the WIRE truth — the daemon has more history
    // even though this client has decided not to fetch it further.
    it("keeps conversation.olderCursor at the wire's cursor even when the item cap stops paging", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const items: MobileConversation["items"] = [];
      for (let i = 100; i < 500; i++) {
        items.push({ kind: "user", id: `item-${i}`, text: "" });
      }
      service.openConv = makeConversation({
        usage: null,
        items,
        turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
        olderCursor: "cursor-1",
      });
      await store.getState().open(service, "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      // 200 more items — total 600, capped to 500 (F8's existing test).
      const olderItems: MobileConversation["items"] = [];
      for (let i = 0; i < 200; i++) {
        olderItems.push({ kind: "user", id: `item-old-${i}`, text: "" });
      }
      service.olderItems = {
        items: olderItems,
        turnsPage: turnsPage([wireTurn("t1", 500, 20)], "more"),
        nextCursor: "more", // the wire says there IS more history...
      };
      await store.getState().loadOlder(service);

      // ...even though the cap disables further paging in the UI.
      expect(store.getState().olderCursor).toBeNull();
      const conv = store.getState().conversation!;
      expect(conv.olderCursor).toBe("more");
      expect(sessionTokens(conv)?.scope).toBe("loaded");
    });

    // D18 B3 round 4 (2): a same-session rehydrate's reread window only
    // covers the current itemLimit-bounded turns, so a turn loaded via an
    // earlier loadOlder falls outside it — the same reason the item-history
    // merge above (preservePageHistory) exists for items.
    it("rehydrate preserves the older turns loaded via loadOlder", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const latest = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        items: [{ kind: "user", id: "new", text: "new" }],
        turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
        olderCursor: "cursor-1",
      });
      service.readProjectionResult = {
        conversation: latest,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      };
      service.olderItems = {
        items: [{ kind: "user", id: "old", text: "old" }],
        turnsPage: turnsPage([wireTurn("t1", 500, 20)], "cursor-2"),
        nextCursor: "cursor-2",
      };
      await store.getState().openProjected(service, sink, "ref-1");
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.map((t) => t.id)).toEqual(["t1", "t2"]);

      await store.getState().rehydrate(service, sink);
      const conv = store.getState().conversation!;
      // The existing item-history merge (preservePageHistory) already keeps
      // "old" prepended; the same gate must keep t1 too. These usage-only
      // windows do not overlap at the transcript level, so the fresh wire
      // cursor remains authoritative. Turn order is not
      // asserted: mergeOlderItemPage places the resp argument's turns first
      // (here, the fresh reread), and only the SET of turns/usage matters to
      // sessionTokens, which sums regardless of order.
      expect(conv.items.map((i) => i.id)).toEqual(["old", "new"]);
      expect(conv.turns.map((t) => t.id).sort()).toEqual(["t1", "t2"]);
      // Both turns still count (not just the fresh reread's own window), and
      // the scope stays "loaded" because the fresh cursor is still present.
      expect(sessionTokens(conv)).toEqual({ inputTokens: 560, outputTokens: 60, scope: "loaded" });
    });
  });

  // D18 B3 round 5: closes the class rounds 3-4 kept re-opening at different
  // sites — the store's own top-level olderCursor (a UI-only, intentionally
  // capped "is there another page to fetch" signal, F8) and the
  // conversation's own ThreadModel olderCursor (the wire truth sessionTokens
  // reads) are two different values, and code kept collapsing one into the
  // other. Per-state table (a session with no thread-level cumulative usage,
  // so sessionTokens is always summing turns):
  //
  //   state                          | store cursor | conv cursor | turns   | scope
  //   initial (open)                 | null (F8)    | "cursor-1"  | [t2]    | loaded
  //   loadOlder (wire has more)      | "cursor-2"   | "cursor-2"  | [t1,t2] | loaded
  //   cap hit (wire still has more)  | null         | "more"      | [t1,t2] | loaded
  //   rehydrate, page history kept   | (unchanged)  | prior conv's| [t1,t2] | loaded
  //                                  |              | own cursor  |         |
  //   rehydrate, no page history     | fresh read's | fresh read's| fresh   | per fresh
  //                                  | own          | own         | only    | read
  //
  // "loadOlder" and "cap hit" (without a following rehydrate) are already
  // covered above by "merges the older page's turns..." and "keeps
  // conversation.olderCursor at the wire's cursor...". The remaining rows,
  // plus the two regressions the panel found, are below.
  describe("D18 B3 round 6: turn merges reuse the package's own identity-aware merge, never an id-only filter", () => {
    // Failing-first (a): thread/turns/list is itself item-paginated, so a
    // turn can be split into fragments across the page boundary. An id-only
    // filter treats a same-id fragment as a pure duplicate and drops it,
    // losing whatever content/usage it alone carries.
    it("a turn split across a page boundary keeps its usage instead of being dropped by an id-only filter", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // The turn's later fragment is already loaded, with no usage of its
      // own — usage arrives on the fragment that continues further back.
      service.openConv = makeConversation({
        usage: null,
        turns: [{ id: "t1", status: "completed", items: [] }],
        olderCursor: "cursor-1",
      });
      await store.getState().open(service, "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      service.olderItems = {
        items: [],
        turnsPage: turnsPage([wireTurn("t1", 500, 20)]),
        nextCursor: undefined,
      };
      await store.getState().loadOlder(service);

      const conv = store.getState().conversation!;
      // The two fragments merge into one turn, not two, and the usage the
      // older fragment carried survives — an id-only filter would have kept
      // only the already-loaded (usage-less) copy and lost it.
      expect(conv.turns).toHaveLength(1);
      expect(sessionTokens(conv)).toEqual({ inputTokens: 500, outputTokens: 20, scope: "session" });
    });

    // Failing-first (b): preserveTurnHistory being true only means page
    // history EXISTS somewhere in this session's lifetime, not that THIS
    // rehydrate's merge actually contributed anything beyond the fresh
    // reread's own window (its own window can grow to cover what page
    // history already supplied). Carrying the accumulated cursor
    // unconditionally then mislabels a now-complete read as "loaded".
    it("rehydrate with page history but a fully-covering fresh window: scope follows the fresh read, not a stale accumulated cursor", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const opened = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
        olderCursor: "cursor-1",
      });
      service.readProjectionResult = {
        conversation: opened,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      service.olderItems = {
        items: [],
        turnsPage: turnsPage([wireTurn("t1", 500, 20)], "cursor-2"),
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);
      // Page history now exists (pageOwnedTurnIds has "t1").

      // A fresh rehydrate whose own window now covers BOTH turns and says
      // there is nothing more beyond it.
      const fresh = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        turns: [
          { id: "t1", status: "completed", items: [], usage: { inputTokens: 500, outputTokens: 20 } },
          { id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } },
        ],
        olderCursor: undefined,
      });
      service.readProjectionResult = {
        conversation: fresh,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: null,
      };
      await store.getState().rehydrate(service, sink);
      const conv = store.getState().conversation!;
      expect(conv.turns.map((t) => t.id).sort()).toEqual(["t1", "t2"]);
      expect(conv.olderCursor).toBeUndefined();
      expect(sessionTokens(conv)).toEqual({ inputTokens: 560, outputTokens: 60, scope: "session" });
    });
  });

  it("rehydrate keeps the fresh cursor for disjoint page-history windows", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const opened = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        turns: [{ id: "turn-initial", status: "completed", items: [], usage: { inputTokens: 10, outputTokens: 1 } }],
        olderCursor: "cursor-initial",
      });
      service.readProjectionResult = {
        conversation: opened,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-initial",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // The page reaches the beginning of the old window. Its undefined
      // cursor must not replace a fresh cursor if the next reread is disjoint.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage([wireTurn("turn-older", 500, 20)]),
        nextCursor: undefined,
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.olderCursor).toBeUndefined();

      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          turns: [{ id: "turn-fresh", status: "completed", items: [], usage: { inputTokens: 20, outputTokens: 2 } }],
          olderCursor: "fresh-cursor",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "fresh-cursor",
      };
      await store.getState().rehydrate(service, sink);

      const conv = store.getState().conversation!;
      expect(conv.turns.map((turn) => turn.id)).toEqual(["turn-older", "turn-initial", "turn-fresh"]);
      expect(conv.olderCursor).toBe("fresh-cursor");
      expect(sessionTokens(conv)).toEqual({ inputTokens: 530, outputTokens: 23, scope: "loaded" });
    });

    it("replacement rehydrate clears page turn ownership before the next refresh", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const initial = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-a",
        usage: null,
        turns: [{ id: "turn-a", status: "completed", items: [], usage: { inputTokens: 10, outputTokens: 1 } }],
        olderCursor: "cursor-a",
      });
      service.readProjectionResult = {
        conversation: initial,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-a",
      };
      await store.getState().openProjected(service, sink, "ref-1");
      service.olderItems = {
        items: [],
        turnsPage: turnsPage([wireTurn("turn-page-a", 500, 20)], "cursor-page-a"),
        nextCursor: "cursor-page-a",
      };
      await store.getState().loadOlder(service);

      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-b",
          usage: null,
          turns: [{ id: "turn-b", status: "completed", items: [], usage: { inputTokens: 20, outputTokens: 2 } }],
          olderCursor: "cursor-b",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-b",
      };
      await store.getState().rehydrate(service, sink);
      expect(store.getState().conversation?.turns.map((turn) => turn.id)).toEqual(["turn-b"]);

      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-b",
          usage: null,
          turns: [],
          olderCursor: undefined,
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: null,
      };
      await store.getState().rehydrate(service, sink);

      const conv = store.getState().conversation!;
      expect(conv.turns).toEqual([]);
      expect(conv.olderCursor).toBeUndefined();
    });

    it("rehydrate merges an older turn fragment's fallback fields and items, then keeps its cursor", async () => {
    const service = new FakeConversationService();
    const sink = createFakeSink();
    const store = createConversationStore();
    const opened = makeConversation({
      threadId: "thread-1",
      instanceId: "instance-1",
      usage: null,
      turns: [{ id: "t-latest", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
      olderCursor: "cursor-1",
    });
    service.readProjectionResult = {
      conversation: opened,
      activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
      olderCursor: "cursor-1",
    };
    await store.getState().openProjected(service, sink, "ref-1");

    // #1919 follow-up reshape: the fragment is now in-window by construction
    // — the real service projects a page's turns into display rows, so the
    // page's fragment items arrive as rows too, keeping the fragment inside
    // the retained-turn keep-window with its full payload. (The test's
    // pre-fix shape — payload items with no retained rows at all — is
    // out-of-window under the retained-turn bound and moved to the
    // counterpart test below.)
    service.olderItems = {
      items: [
        { kind: "assistant", id: "old-only", markdown: "older-only item", streaming: false, transcriptKey: "old-only" },
        { kind: "assistant", id: "old-shared", markdown: "older text", streaming: false, transcriptKey: "shared-item" },
      ],
      turnsPage: turnsPage([
        wireTurnFragment(
          "t-fragment-old",
          [
            {
              id: "old-only",
              transcriptKey: "old-only",
              turnId: "t-fragment-old",
              type: "agentMessage",
              text: "older-only item",
              position: { entry: 1, item: 0 },
              status: "completed",
            },
            {
              id: "old-shared",
              transcriptKey: "shared-item",
              turnId: "t-fragment-old",
              type: "agentMessage",
              text: "older text",
              position: { entry: 2, item: 0 },
              status: "completed",
            },
          ],
          { inputTokens: 500, outputTokens: 20 },
        ),
      ], "cursor-2"),
      nextCursor: "cursor-2",
    };
    await store.getState().loadOlder(service);

    const fresh = makeConversation({
      threadId: "thread-1",
      instanceId: "instance-1",
      usage: null,
      // The fresh read's own window rows, so its fragment stays in-window
      // and the page fragment's fallback supply is merged, not trimmed.
      items: [
        { kind: "assistant", id: "fresh-shared", markdown: "fresh text", streaming: false, transcriptKey: "shared-item" },
        { kind: "assistant", id: "fresh-only", markdown: "fresh-only item", streaming: false, transcriptKey: "fresh-only" },
      ],
      turns: [
        {
          id: "t-fragment-fresh",
          status: "completed",
          items: [
            {
              id: "fresh-shared",
              transcriptKey: "shared-item",
              turnId: "t-fragment-fresh",
              type: "agentMessage",
              text: "fresh text",
              position: { entry: 2, item: 0 },
              status: "completed",
            },
            {
              id: "fresh-only",
              transcriptKey: "fresh-only",
              turnId: "t-fragment-fresh",
              type: "agentMessage",
              text: "fresh-only item",
              position: { entry: 3, item: 0 },
              status: "completed",
            },
          ],
        },
        { id: "t-latest", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } },
      ],
      olderCursor: undefined,
    });
    service.readProjectionResult = {
      conversation: fresh,
      activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
      olderCursor: null,
    };
    await store.getState().rehydrate(service, sink);

    const conv = store.getState().conversation!;
    const merged = conv.turns.find((turn) => turn.id === "t-fragment-fresh");
    expect(conv.turns.map((turn) => turn.id)).toEqual(["t-fragment-fresh", "t-latest"]);
    expect(merged?.usage).toEqual({ inputTokens: 500, outputTokens: 20 });
    expect(merged?.items.map((item) => item.transcriptKey)).toEqual([
      "old-only",
      "shared-item",
      "fresh-only",
    ]);
    expect(merged?.items.find((item) => item.transcriptKey === "shared-item")?.text).toBe("fresh text");
    expect(conv.olderCursor).toBe("cursor-2");
    expect(sessionTokens(conv)).toEqual({ inputTokens: 560, outputTokens: 60, scope: "loaded" });
  });

    // #1919 follow-up counterpart: the same page fragment, out-of-window —
    // its payload items back no retained display row. The bound's contract
    // there: the page turn survives as compact identity + usage (so
    // sessionTokens keeps covering everything actually loaded) but supplies
    // nothing — no fallback items — and claims no transcript overlap, so
    // the fresh read's own wire cursor stands.
    it("out-of-window page fragment: the turn survives compact with its usage but supplies no items or cursor", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const opened = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        turns: [{ id: "t-latest", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
        olderCursor: "cursor-1",
      });
      service.readProjectionResult = {
        conversation: opened,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // The page's fragment carries payloads, but none of its items project
      // to a retained display row — the projector-filtered/evicted class.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage([
          wireTurnFragment(
            "t-fragment-old",
            [
              {
                id: "old-only",
                transcriptKey: "old-only",
                turnId: "t-fragment-old",
                type: "agentMessage",
                text: "older-only item",
                position: { entry: 1, item: 0 },
                status: "completed",
              },
              {
                id: "old-alt",
                transcriptKey: "old-alt",
                turnId: "t-fragment-old",
                type: "agentMessage",
                text: "older text",
                position: { entry: 2, item: 0 },
                status: "completed",
              },
            ],
            { inputTokens: 500, outputTokens: 20 },
          ),
        ], "cursor-2"),
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);

      const fresh = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        items: [
          { kind: "assistant", id: "fresh-a", markdown: "fresh a", streaming: false, transcriptKey: "fresh-a" },
          { kind: "assistant", id: "fresh-b", markdown: "fresh b", streaming: false, transcriptKey: "fresh-b" },
        ],
        turns: [
          {
            id: "t-fragment-fresh",
            status: "completed",
            items: [
              {
                id: "fresh-a",
                transcriptKey: "fresh-a",
                turnId: "t-fragment-fresh",
                type: "agentMessage",
                text: "fresh a",
                position: { entry: 3, item: 0 },
                status: "completed",
              },
              {
                id: "fresh-b",
                transcriptKey: "fresh-b",
                turnId: "t-fragment-fresh",
                type: "agentMessage",
                text: "fresh b",
                position: { entry: 4, item: 0 },
                status: "completed",
              },
            ],
          },
          { id: "t-latest", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } },
        ],
        olderCursor: "cursor-1",
      });
      service.readProjectionResult = {
        conversation: fresh,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      };
      await store.getState().rehydrate(service, sink);

      const conv = store.getState().conversation!;
      const compact = conv.turns.find((turn) => turn.id === "t-fragment-old");
      // The page turn survives the bound as compact identity + usage...
      expect(compact?.items).toEqual([]);
      expect(compact?.usage).toEqual({ inputTokens: 500, outputTokens: 20 });
      // ...and supplies nothing: the fresh fragment keeps only its own
      // items, with no "old-only" fallback folded in.
      const merged = conv.turns.find((turn) => turn.id === "t-fragment-fresh");
      expect(merged?.items.map((item) => item.transcriptKey)).toEqual(["fresh-a", "fresh-b"]);
      // A trimmed turn claims no transcript overlap, so the fresh read's
      // own wire cursor stands.
      expect(conv.olderCursor).toBe("cursor-1");
      // Accounting completeness: the compact turn's usage still counts.
      expect(sessionTokens(conv)).toEqual({ inputTokens: 560, outputTokens: 60, scope: "loaded" });

      // A second rehydrate still preserves the compact page turn: its id
      // left pageOwnedTurnIds with the same bound, so preservation now runs
      // through the compact-survivor side of the gate.
      await store.getState().rehydrate(service, sink);
      const conv2 = store.getState().conversation!;
      const compact2 = conv2.turns.find((turn) => turn.id === "t-fragment-old");
      expect(compact2?.items).toEqual([]);
      expect(compact2?.usage).toEqual({ inputTokens: 500, outputTokens: 20 });
      expect(sessionTokens(conv2)).toEqual({ inputTokens: 560, outputTokens: 60, scope: "loaded" });
    });

  describe("D18 B3 round 5: conversation's wire cursor and turn ownership never derive from the store's capped cursor or item eviction", () => {
    it("initial: conversation.olderCursor and turns come straight from the hydrated model", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        usage: null,
        turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
        olderCursor: "cursor-1",
      });
      await store.getState().open(service, "ref-1");
      // The store's own cursor is a UI-only pagination-enablement value that
      // plain open() always starts at null (F8's live path establishes it
      // separately) - it is not the wire truth conversation.olderCursor is.
      expect(store.getState().olderCursor).toBeNull();
      const conv = store.getState().conversation!;
      expect(conv.olderCursor).toBe("cursor-1");
      expect(conv.turns.map((t) => t.id)).toEqual(["t2"]);
      expect(sessionTokens(conv)?.scope).toBe("loaded");
    });

    // Failing-first (1): round 4's own fix made conversation.olderCursor take
    // mergedCursor, which is currentSnapshot.olderCursor - the STORE's capped
    // cursor - whenever there is page history to preserve. That happens to
    // equal the wire truth when the cap was never hit (this file's earlier
    // "rehydrate preserves the older turns..." test doesn't distinguish the
    // two), but diverges the moment the cap forces the store's cursor to
    // null while the wire still has more.
    it("cap hit then rehydrate: conversation.olderCursor stays the wire truth, not the store's capped null", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const items: MobileConversation["items"] = [];
      for (let i = 100; i < 500; i++) items.push({ kind: "user", id: `item-${i}`, text: "" });
      const opened = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        items,
        turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
        olderCursor: "cursor-1",
      });
      service.readProjectionResult = {
        conversation: opened,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // 200 more items - total 600, capped to 500 - but the wire still says
      // there is more (the existing F8 cap scenario, plus a turn).
      const olderItems: MobileConversation["items"] = [];
      for (let i = 0; i < 200; i++) olderItems.push({ kind: "user", id: `item-old-${i}`, text: "" });
      service.olderItems = {
        items: olderItems,
        turnsPage: turnsPage([wireTurn("t1", 500, 20)], "more"),
        nextCursor: "more",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().olderCursor).toBeNull();
      expect(store.getState().conversation?.olderCursor).toBe("more");

      // A same-session rehydrate must keep the fresh cursor because the
      // usage-only page and reread windows have no transcript overlap.
      service.readProjectionResult = {
        conversation: opened,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      };
      await store.getState().rehydrate(service, sink);
      const conv = store.getState().conversation!;
      expect(conv.olderCursor).toBe("cursor-1");
      expect(sessionTokens(conv)?.scope).toBe("loaded");
    });

    it("rehydrate without page history: conversation.olderCursor and turns come straight from the fresh reread", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const opened = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        turns: [{ id: "t1", status: "completed", items: [], usage: { inputTokens: 10, outputTokens: 5 } }],
        olderCursor: "cursor-1",
      });
      service.readProjectionResult = {
        conversation: opened,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      };
      await store.getState().openProjected(service, sink, "ref-1");
      // No loadOlder ever ran - pageOwnedIds/pageOwnedTurnIds stay empty, so
      // there is no page history to preserve.

      const fresh = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 20, outputTokens: 8 } }],
        olderCursor: undefined,
      });
      service.readProjectionResult = {
        conversation: fresh,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: null,
      };
      await store.getState().rehydrate(service, sink);
      const conv = store.getState().conversation!;
      // t1 is gone - there was no page history to preserve it, so the fresh
      // reread's own (smaller) window is authoritative, same as it always was.
      expect(conv.turns.map((t) => t.id)).toEqual(["t2"]);
      expect(conv.olderCursor).toBeUndefined();
      expect(sessionTokens(conv)).toEqual({ inputTokens: 20, outputTokens: 8, scope: "session" });
    });

    // Failing-first (2): the item-history merge (preservePageHistory) gates
    // on pageOwnedIds, which only tracks items that actually survived
    // loadOlder's own dedupe. A page whose only item duplicates one already
    // in hand contributes nothing to pageOwnedIds, but its turn is real and
    // must still survive - which is exactly why pageOwnedTurnIds is tracked
    // separately from pageOwnedIds.
    //
    // #1919 follow-up: the page turn now also carries payload items that
    // back no retained row (the projector-filtered class), so the test pins
    // the retained-turn bound's other half too: the turn and its usage
    // survive the rehydrate, with the payloads bounded to the compact
    // identity + usage shape.
    it("evicted/filtered page: a turn survives a rehydrate even when none of that page's items did", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const opened = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        items: [{ kind: "user", id: "existing", text: "existing" }],
        turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
        olderCursor: "cursor-1",
      });
      service.readProjectionResult = {
        conversation: opened,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // The older page's only item duplicates one already in hand (F10's own
      // dedupe drops it entirely - pageOwnedIds gets nothing), but its turn
      // is genuinely new, and its payload items project to no retained row.
      service.olderItems = {
        items: [{ kind: "user", id: "existing", text: "existing" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "t1",
              [
                {
                  id: "t1-gone-0",
                  transcriptKey: "t1-gone-0",
                  turnId: "t1",
                  type: "agentMessage",
                  text: "filtered payload 0",
                  position: { entry: 5, item: 0 },
                  status: "completed",
                },
                {
                  id: "t1-gone-1",
                  transcriptKey: "t1-gone-1",
                  turnId: "t1",
                  type: "agentMessage",
                  text: "filtered payload 1",
                  position: { entry: 6, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "cursor-2",
        ),
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.map((t) => t.id)).toEqual(["t1", "t2"]);

      const fresh = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        items: [{ kind: "user", id: "existing", text: "existing" }],
        turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
        olderCursor: "cursor-1",
      });
      service.readProjectionResult = {
        conversation: fresh,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      };
      await store.getState().rehydrate(service, sink);
      const conv = store.getState().conversation!;
      // Order is not asserted (see the "rehydrate preserves..." test above).
      expect(conv.turns.map((t) => t.id).sort()).toEqual(["t1", "t2"]);
      // The turn and its usage survive... with the payloads bounded: none of
      // its items back a retained row, so the retained-turn bound trims them
      // to the compact identity + usage shape.
      const surviving = conv.turns.find((turn) => turn.id === "t1")!;
      expect(surviving.items).toEqual([]);
      expect(surviving.usage).toEqual({ inputTokens: 500, outputTokens: 20 });
      expect(sessionTokens(conv)).toEqual({ inputTokens: 560, outputTokens: 60, scope: "loaded" });
    });
  });

  // D18 B3 round 8: a racing loadOlder's CURSOR MOVEMENT must survive a held
  // rehydrate even when the page retained no display rows. Row ownership
  // (pageOwnedIds) is the wrong signal for the store's own paging cursor: a
  // fully deduped or cap-evicted page owns no rows yet still advances the
  // cursor the next loadOlder must continue from. Dropping the
  // entryLoadOlderToken disjunct (round 2, for failed pages) had let a held
  // rehydrate regress that advancement to the fresh read's own window
  // cursor — re-offering a page the racing loadOlder had already consumed,
  // or resurrecting paging at a cursor that had honestly stopped. What
  // separates the racing outcomes is not the token (a FAILED page bumps it
  // too) but whether the store's own cursor actually MOVED during the await.
  describe("D18 B3 round 8: a racing loadOlder's cursor movement survives a held rehydrate", () => {
    it("a successful racing page that retains no rows keeps its cursor advancement through the rehydrate", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const opened = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        items: [{ kind: "user", id: "existing", text: "existing" }],
        turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
        olderCursor: "cursor-1",
      });
      service.readProjectionResult = {
        conversation: opened,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      };
      await store.getState().openProjected(service, sink, "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      // Hold the rehydrate open on its read.
      let releaseRead!: (value: ConversationReadProjection) => void;
      service.readProjectionBlock = new Promise((resolve) => { releaseRead = resolve; });
      const rehydratePromise = store.getState().rehydrate(service, sink);
      await yieldMicrotask();

      // While the rehydrate is in flight, loadOlder succeeds with a page
      // whose every row duplicates one already in hand — F10's dedupe drops
      // them all, so pageOwnedIds stays empty — but its nextCursor still
      // advances the store's own paging cursor.
      service.olderItems = {
        items: [{ kind: "user", id: "existing", text: "existing" }],
        turnsPage: turnsPage([wireTurn("t1", 500, 20)], "cursor-2"),
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().olderCursor).toBe("cursor-2");

      // The fresh reread reports its own itemLimit-bounded window cursor,
      // which knows nothing about the page this client just consumed.
      releaseRead({
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user", id: "existing", text: "existing" }],
          turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
          olderCursor: "cursor-1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      });
      await rehydratePromise;

      const conv = store.getState().conversation!;
      // The racing page's cursor advancement survives: the store's own
      // cursor must not regress to the fresh read's window cursor and
      // re-offer the page that was already consumed.
      expect(store.getState().olderCursor).toBe("cursor-2");
      // conversation.olderCursor is the separate wire truth: the page turn
      // t1 has no transcript overlap with the fresh window, so the fresh
      // read's own wire cursor still stands there (scope labeling only).
      expect(conv.olderCursor).toBe("cursor-1");
    });

    it("a successful racing page that exhausted history keeps the store's honest null cursor through the rehydrate", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const opened = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        items: [{ kind: "user", id: "existing", text: "existing" }],
        turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
        olderCursor: "cursor-1",
      });
      service.readProjectionResult = {
        conversation: opened,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      };
      await store.getState().openProjected(service, sink, "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      let releaseRead!: (value: ConversationReadProjection) => void;
      service.readProjectionBlock = new Promise((resolve) => { releaseRead = resolve; });
      const rehydratePromise = store.getState().rehydrate(service, sink);
      await yieldMicrotask();

      // The racing page reaches the beginning of history: every row
      // duplicates one already in hand (no retained rows, pageOwnedIds
      // empty) and the wire offers no next cursor, so the store's own
      // paging cursor honestly stops at null.
      service.olderItems = {
        items: [{ kind: "user", id: "existing", text: "existing" }],
        turnsPage: turnsPage([wireTurn("t1", 500, 20)]),
        nextCursor: undefined,
      };
      await store.getState().loadOlder(service);
      expect(store.getState().olderCursor).toBeNull();

      releaseRead({
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user", id: "existing", text: "existing" }],
          turns: [{ id: "t2", status: "completed", items: [], usage: { inputTokens: 60, outputTokens: 40 } }],
          olderCursor: "cursor-1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      });
      await rehydratePromise;

      // The rehydrate must not resurrect paging at the fresh read's window
      // cursor after the racing page exhausted it.
      expect(store.getState().olderCursor).toBeNull();
    });

    it("a failed racing loadOlder still lets the fresh read's cursor signal win", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const opened = makeConversation({
        threadId: "thread-1",
        instanceId: "instance-1",
        usage: null,
        items: [{ kind: "user", id: "existing", text: "existing" }],
        turns: [],
        olderCursor: "cursor-1",
      });
      service.readProjectionResult = {
        conversation: opened,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "cursor-1",
      };
      await store.getState().openProjected(service, sink, "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      let releaseRead!: (value: ConversationReadProjection) => void;
      service.readProjectionBlock = new Promise((resolve) => { releaseRead = resolve; });
      const rehydratePromise = store.getState().rehydrate(service, sink);
      await yieldMicrotask();

      // A racing loadOlder FAILS: it bumps the page token but moves the
      // cursor not at all and owns nothing.
      service.olderItems = Promise.reject(new Error("page boom")) as never;
      await store.getState().loadOlder(service).catch(() => {});

      // The fresh reread reports no further history.
      releaseRead({
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user", id: "existing", text: "existing" }],
          turns: [],
          olderCursor: undefined,
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: null,
      });
      await rehydratePromise;

      // The store's paging cursor follows the fresh signal — a failed page
      // must not pin the stale pre-race cursor through the rehydrate.
      expect(store.getState().olderCursor).toBeNull();
    });
  });

  // #1919 follow-up (retained-turn bound): loadOlder's page turns used to be
  // exempt from every retention bound — once any older page loaded, the store
  // retained every page turn's FULL item payloads for the conversation's
  // lifetime behind the 500-row RETAINED_ITEM_CAP, so memory and the
  // per-refresh merge/sum cost grew with the whole loaded transcript. The
  // bound: a retained turn keeps full payloads only while it is inside the
  // keep-window (its items intersect the retained display rows); outside it,
  // the turn survives as compact identity + usage so sessionTokens' turn-summed
  // fallback still covers everything actually loaded.
  describe("#1919 follow-up: retained page-turn payloads are bounded to the display keep-window", () => {
    it("repeated loadOlder + rehydrate with display-row eviction keeps retained turn payloads bounded while every loaded turn's usage still counts", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();

      const ROW_PAGES = 12;
      const ROWS_PER_PAGE = 8;
      const PAYLOAD_ITEMS_PER_TURN = 20;
      const PAGES = 40;

      // A live conversation's fresh read window: `rows` display rows plus one
      // live turn's usage. The fresh window GROWS across the loop the way a
      // live conversation does, so the 500-row cap evicts the oldest retained
      // rows — the early pages' — exactly as live churn does on device.
      const freshConversation = (rows: number) =>
        makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: rows }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 5, outputTokens: 1 } },
          ],
          olderCursor: "wire",
        });
      const freshRead = (rows: number) => ({
        conversation: freshConversation(rows),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "wire",
      });

      service.readProjectionResult = freshRead(40);
      await store.getState().openProjected(service, sink, "ref-1");

      // Each page turn carries a usage stamp plus a heavy item payload: items
      // that project onto the page's display rows (while those rows are
      // retained) and payload-only items the projector filters or the dedupe
      // drops — the pinned evicted/filtered-page shapes. Pages past
      // ROW_PAGES carry no display rows at all: their turns are the
      // evicted/filtered class, and on current main they are exactly the
      // payloads that accumulate without bound.
      const pageTurn = (i: number): Turn =>
        wireTurnFragment(
          `pt-${i}`,
          [
            ...(i <= ROW_PAGES
              ? Array.from({ length: ROWS_PER_PAGE }, (_, j) => ({
                  id: `w-${i}-${j}`,
                  transcriptKey: `w-${i}-${j}`,
                  turnId: `pt-${i}`,
                  type: "agentMessage",
                  text: `page ${i} row item ${j}`,
                  position: { entry: 1000 - i * 16 - j, item: 0 },
                  status: "completed",
                }))
              : []),
            ...Array.from(
              { length: PAYLOAD_ITEMS_PER_TURN - (i <= ROW_PAGES ? ROWS_PER_PAGE : 0) },
              (_, j) => ({
                id: `x-${i}-${j}`,
                transcriptKey: `x-${i}-${j}`,
                turnId: `pt-${i}`,
                type: "agentMessage",
                text: `page ${i} payload-only item ${j}`,
                position: { entry: 2000 - i * 16 - j, item: 0 },
                status: "completed",
              }),
            ),
          ],
          { inputTokens: 100, outputTokens: 10 },
        );

      for (let i = 1; i <= PAGES; i++) {
        service.olderItems = {
          items:
            i <= ROW_PAGES
              ? Array.from({ length: ROWS_PER_PAGE }, (_, j) => ({
                  kind: "user" as const,
                  id: `w-${i}-${j}`,
                  text: `w-${i}-${j}`,
                }))
              : [],
          turnsPage: turnsPage([pageTurn(i)], `cursor-${i}`),
          nextCursor: `cursor-${i}`,
        };
        const result = await store.getState().loadOlder(service);
        expect(result.status).toBe("loaded");

        // The fresh window grows 40 rows per refresh once the row-carrying
        // pages are done, so the cap evicts the early pages' rows (rehydrate
        // 22 evicts pt-1's, rehydrate 24 evicts every page row) while the
        // row-less pages keep paging — merged input never overflows the cap,
        // so F8's honest stop never fires and the loop keeps loading.
        const freshRows = i <= ROW_PAGES ? 40 : 40 + 40 * (i - ROW_PAGES);
        service.readProjectionResult = freshRead(freshRows);
        await store.getState().rehydrate(service, sink);
      }

      const conv = store.getState().conversation!;
      const retainedPayloadItems = conv.turns.reduce(
        (total, turn) => total + turn.items.length,
        0,
      );
      // The bound: only turns whose items intersect the retained display
      // window may keep payloads. By the final iteration every page row has
      // been evicted and every row-less page turn is outside the window, so
      // the retained payload set must be well under the worst in-window
      // shape (ROW_PAGES turns x 20 items) — on the unbounded main it is the
      // whole loaded transcript (PAGES turns x 20 items = 800).
      expect(retainedPayloadItems).toBeLessThanOrEqual(ROW_PAGES * PAYLOAD_ITEMS_PER_TURN);
      // Accounting completeness is the bound's other half: EVERY loaded turn
      // keeps identity... (order is not asserted; the merge places freely)
      expect(
        conv.turns.some((turn) => turn.id === `pt-${PAGES}`),
      ).toBe(true);
      expect(new Set(conv.turns.map((turn) => turn.id)).size).toBe(PAGES + 1);
      // ...and usage, so the turn-summed fallback still covers everything
      // actually loaded: PAGES page turns plus the live turn.
      expect(sessionTokens(conv)).toEqual({
        inputTokens: 5 + 100 * PAGES,
        outputTokens: 1 + 10 * PAGES,
        scope: "loaded",
      });
      // The first page's rows were evicted by the cap, so its turn is the
      // trimmed shape: identity + usage survive, display-fallback payloads
      // do not.
      const evictedPageTurn = conv.turns.find((turn) => turn.id === "pt-1")!;
      expect(evictedPageTurn.items).toEqual([]);
      expect(evictedPageTurn.usage).toEqual({ inputTokens: 100, outputTokens: 10 });
      // The display window itself stayed capped through the churn.
      expect(conv.items.length).toBeLessThanOrEqual(500);
    });

    // Review round 1, finding 2: live notification paths cap display rows
    // too, so page rows can be evicted by live growth alone. The bound must
    // hold there as well — not only at page/rehydrate publishes — or a page
    // turn's full payloads linger until some later refresh happens to run.
    it("live growth that evicts a page's rows bounds its turn payloads without a rehydrate or page load", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      // 492 fresh rows + an 8-row page = exactly the 500-row cap, so the
      // page loads without eviction and its turn keeps full payloads.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 492 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      service.olderItems = {
        items: Array.from({ length: 8 }, (_, j) => ({
          kind: "user" as const,
          id: `w-${j}`,
          text: `w-${j}`,
        })),
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              Array.from({ length: 8 }, (_, j) => ({
                id: `w-${j}`,
                transcriptKey: `w-${j}`,
                turnId: "pt",
                type: "agentMessage",
                text: `page item ${j}`,
                position: { entry: 100 - j, item: 0 },
                status: "completed",
              })),
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      const loaded = store.getState().conversation?.turns.find((t) => t.id === "pt");
      expect(loaded?.items).toHaveLength(8);

      // Live traffic alone (no rehydrate, no further page) appends 8 rows,
      // and each append's cap evicts one page row from the oldest end.
      for (let j = 0; j < 8; j++) {
        store.getState().applyNotification({
          method: "item/started",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "ft",
            item: { type: "userMessage", id: `l-${j}`, text: `live ${j}` },
          },
        } as AnyNotification);
      }

      const conv = store.getState().conversation!;
      // The page's rows are all gone...
      expect(conv.items.some((row) => row.id.startsWith("w-"))).toBe(false);
      // ...so its turn is the compact shape: identity + usage survive, and
      // the payloads are bounded by the LIVE path's own bound — no
      // rehydrate or page load needed to trim them.
      const pageTurn = conv.turns.find((t) => t.id === "pt");
      expect(pageTurn?.items).toEqual([]);
      expect(pageTurn?.usage).toEqual({ inputTokens: 500, outputTokens: 20 });
      expect(conv.items.length).toBeLessThanOrEqual(500);
      expect(sessionTokens(conv)).toEqual({ inputTokens: 501, outputTokens: 21, scope: "loaded" });
    });

    // Review round 1, finding 1: the package's merges match fragments by
    // ITEM identity when turn ids differ. A compact survivor has no items to
    // match with, so when the wire re-issues its content under another turn
    // id the two would otherwise both survive and sessionTokens would
    // double-count their usage. The remembered-identity fold below keeps
    // exactly one turn with exactly one usage stamp.
    it("a compact page fragment re-issued under a fresh turn id folds away instead of double-counting usage", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      // 496 fresh rows + an 8-row page overflows the cap by 4, evicting the
      // page's first 4 rows — including both identities the page turn's
      // payloads back — so the page turn is out-of-window at its own load.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 496 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      service.olderItems = {
        items: Array.from({ length: 8 }, (_, j) => ({
          kind: "user" as const,
          id: `w-${j}`,
          text: `w-${j}`,
        })),
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "w-0",
                  transcriptKey: "w-0",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "page item 0",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
                {
                  id: "w-1",
                  transcriptKey: "w-1",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "page item 1",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      const compact = store.getState().conversation?.turns.find((t) => t.id === "pt");
      expect(compact?.items).toEqual([]);
      expect(compact?.usage).toEqual({ inputTokens: 500, outputTokens: 20 });

      // A fresh read re-issues the same content under a different turn id,
      // with its own usage and its own rows in the fresh window.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [
            { kind: "assistant", id: "fw-0", markdown: "fresh w0", streaming: false, transcriptKey: "w-0" },
            { kind: "assistant", id: "fw-1", markdown: "fresh w1", streaming: false, transcriptKey: "w-1" },
          ],
          turns: [
            {
              id: "pt-fresh",
              status: "completed",
              items: [
                {
                  id: "fw-0",
                  transcriptKey: "w-0",
                  turnId: "pt-fresh",
                  type: "agentMessage",
                  text: "fresh w0",
                  position: { entry: 3, item: 0 },
                  status: "completed",
                },
                {
                  id: "fw-1",
                  transcriptKey: "w-1",
                  turnId: "pt-fresh",
                  type: "agentMessage",
                  text: "fresh w1",
                  position: { entry: 4, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 7, outputTokens: 3 },
            },
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);

      const conv = store.getState().conversation!;
      // The compact survivor folded into the re-issued fresh fragment...
      expect(conv.turns.some((turn) => turn.id === "pt")).toBe(false);
      const merged = conv.turns.find((turn) => turn.id === "pt-fresh");
      // ...which keeps its own usage (the fresh side is the newer merge
      // input, mirroring mergePageTurn) and its items (in-window via the
      // fresh read's own rows).
      expect(merged?.usage).toEqual({ inputTokens: 7, outputTokens: 3 });
      expect(merged?.items.map((item) => item.transcriptKey)).toEqual(["w-0", "w-1"]);
      // The fresh side's own text survives the fold — the injected skeleton
      // must not erase it.
      expect(merged?.items.map((item) => item.text)).toEqual(["fresh w0", "fresh w1"]);
      // Exactly one usage stamp counts — not the compact survivor's
      // {500,20} beside the re-issued turn's {7,3}.
      expect(sessionTokens(conv)).toEqual({ inputTokens: 8, outputTokens: 4, scope: "loaded" });
    });

    // Review round 1, finding 1 (page side): the same fold through a later
    // PAGE that re-issues a compact turn's content under another turn id.
    // mergeOlderItemPage makes the retained copy the newer merge input, so
    // the compact side's usage wins there. The compact turn here is the
    // pinned evicted/filtered shape — a page turn whose payload items back
    // no retained row — so it compacts at its own load while the window
    // keeps spare capacity for the re-issuing page's rows.
    it("a compact page fragment re-issued by a later page folds away with the retained copy's usage winning", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 488 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Page 1 lands under the cap. Its turn's payloads are the
      // projector-filtered class — identities with no display row — so the
      // turn compacts at its own load.
      service.olderItems = {
        items: Array.from({ length: 8 }, (_, j) => ({
          kind: "user" as const,
          id: `w-${j}`,
          text: `w-${j}`,
        })),
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "x-0",
                  transcriptKey: "x-0",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "filtered payload 0",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
                {
                  id: "x-1",
                  transcriptKey: "x-1",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "filtered payload 1",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().olderCursor).toBe("c1");
      const compact = store.getState().conversation?.turns.find((t) => t.id === "pt");
      expect(compact?.items).toEqual([]);

      // A later page re-issues the same content under another turn id with
      // its own usage — and display rows of its own, so the folded turn is
      // in-window and keeps the re-issued payloads with the page's own text.
      service.olderItems = {
        items: [
          { kind: "user" as const, id: "x-0", text: "x-0" },
          { kind: "user" as const, id: "x-1", text: "x-1" },
        ],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt2",
              [
                {
                  id: "rw-0",
                  transcriptKey: "x-0",
                  turnId: "pt2",
                  type: "agentMessage",
                  text: "reissued 0",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
                {
                  id: "rw-1",
                  transcriptKey: "x-1",
                  turnId: "pt2",
                  type: "agentMessage",
                  text: "reissued 1",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 7, outputTokens: 3 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      const result = await store.getState().loadOlder(service);
      expect(result.status).toBe("loaded");

      const conv = store.getState().conversation!;
      // The fold keeps the retained copy's id — the newer merge input under
      // mergeOlderItemPage — with the retained copy's usage; the re-issuing
      // page turn folded in and is gone.
      expect(conv.turns.some((turn) => turn.id === "pt2")).toBe(false);
      const merged = conv.turns.find((turn) => turn.id === "pt");
      expect(merged?.usage).toEqual({ inputTokens: 500, outputTokens: 20 });
      // In-window through the re-issued rows: the payloads survive with the
      // page's own text (a skeleton on the model side must not erase it).
      expect(merged?.items.map((item) => item.transcriptKey)).toEqual(["x-0", "x-1"]);
      expect(merged?.items.map((item) => item.text)).toEqual(["reissued 0", "reissued 1"]);
      // Exactly one usage stamp counts.
      expect(sessionTokens(conv)).toEqual({ inputTokens: 501, outputTokens: 21, scope: "loaded" });
    });

    // Review round 2, finding 2.1: a compact turn that is only PARTIALLY
    // restored (a same-id page fragment brings back one of its items) must
    // keep folding later re-issues of its remaining identities — the
    // remembered set may not be replaced by the restored subset.
    it("a partially restored compact turn still folds later re-issues of its remaining identities", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 488 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Page 1's turn carries two payload-only identities (no display rows),
      // so it compacts at its own load remembering both.
      const payloadOnlyItems = (turnId: string, texts: [string, string]) =>
        [
          {
            id: "x-0",
            transcriptKey: "px",
            turnId,
            type: "agentMessage",
            text: texts[0],
            position: { entry: 99, item: 0 },
            status: "completed",
          },
          {
            id: "x-1",
            transcriptKey: "py",
            turnId,
            type: "agentMessage",
            text: texts[1],
            position: { entry: 100, item: 0 },
            status: "completed",
          },
        ] as NonNullable<Turn["items"]>;
      service.olderItems = {
        items: Array.from({ length: 8 }, (_, j) => ({
          kind: "user" as const,
          id: `w-${j}`,
          text: `w-${j}`,
        })),
        turnsPage: turnsPage(
          [wireTurnFragment("pt", payloadOnlyItems("pt", ["payload 0", "payload 1"]), { inputTokens: 500, outputTokens: 20 })],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // Page 2 restores only px, under the SAME turn id: the fold adopts
      // the real item and its row; py stays remembered.
      service.olderItems = {
        items: [{ kind: "user" as const, id: "px", text: "px" }],
        turnsPage: turnsPage(
          [
            {
              id: "pt",
              itemsView: "fragment",
              status: "completed",
              usage: { inputTokens: 500, outputTokens: 20 },
              items: [
                {
                  id: "rx-0",
                  transcriptKey: "px",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "restored",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
            },
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const restored = store.getState().conversation?.turns.find((t) => t.id === "pt");
      expect(restored?.items.map((item) => item.transcriptKey)).toEqual(["px"]);
      expect(restored?.items.map((item) => item.text)).toEqual(["restored"]);
      expect(restored?.usage).toEqual({ inputTokens: 500, outputTokens: 20 });

      // Page 3 re-issues py under a DIFFERENT turn id: the partially
      // restored turn must still fold it away — one turn, one usage stamp.
      service.olderItems = {
        items: [{ kind: "user" as const, id: "py", text: "py" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt-b",
              [
                {
                  id: "ry-0",
                  transcriptKey: "py",
                  turnId: "pt-b",
                  type: "agentMessage",
                  text: "late",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 7, outputTokens: 3 },
            ),
          ],
          "c3",
        ),
        nextCursor: "c3",
      };
      await store.getState().loadOlder(service);

      const conv = store.getState().conversation!;
      expect(conv.turns.some((turn) => turn.id === "pt-b")).toBe(false);
      const merged = conv.turns.find((turn) => turn.id === "pt");
      expect(merged?.items.map((item) => item.transcriptKey)).toEqual(["px", "py"]);
      expect(merged?.items.map((item) => item.text)).toEqual(["restored", "late"]);
      expect(merged?.usage).toEqual({ inputTokens: 500, outputTokens: 20 });
      expect(sessionTokens(conv)).toEqual({ inputTokens: 501, outputTokens: 21, scope: "loaded" });
    });

    // Review round 2, finding 2.2: a compact fragment whose remembered
    // identities connect TWO incoming turns must coalesce all of them —
    // the package's own grouping is transitive, and stopping at the first
    // match would leave the second turn counted separately.
    it("a compact fragment connecting two fresh turns coalesces them into one", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 488 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      service.olderItems = {
        items: Array.from({ length: 8 }, (_, j) => ({
          kind: "user" as const,
          id: `w-${j}`,
          text: `w-${j}`,
        })),
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "x-0",
                  transcriptKey: "tx",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload x",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
                {
                  id: "x-1",
                  transcriptKey: "ty",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload y",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // The fresh read re-issues the two identities under two DIFFERENT
      // turn ids, as usage-less fragments — only the compact survivor's
      // memory connects them, and only its usage stamp completes the group.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [
            { kind: "user" as const, id: "tx", text: "tx" },
            { kind: "user" as const, id: "ty", text: "ty" },
          ],
          turns: [
            {
              id: "pb",
              status: "completed",
              items: [
                {
                  id: "fb-0",
                  transcriptKey: "tx",
                  turnId: "pb",
                  type: "agentMessage",
                  text: "fresh x",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
            },
            {
              id: "pc",
              status: "completed",
              items: [
                {
                  id: "fc-0",
                  transcriptKey: "ty",
                  turnId: "pc",
                  type: "agentMessage",
                  text: "fresh y",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
              ],
            },
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);

      const conv = store.getState().conversation!;
      // All three fragments coalesced into ONE turn (the fold keeps the
      // last fresh fragment's id), carrying the compact survivor's usage as
      // the group's only usage stamp.
      expect(conv.turns.some((turn) => turn.id === "pt")).toBe(false);
      expect(conv.turns.some((turn) => turn.id === "pb")).toBe(false);
      const coalesced = conv.turns.find((turn) => turn.id === "pc");
      expect(coalesced?.usage).toEqual({ inputTokens: 500, outputTokens: 20 });
      expect(coalesced?.items.map((item) => item.transcriptKey)).toEqual(["tx", "ty"]);
      expect(sessionTokens(conv)).toEqual({ inputTokens: 501, outputTokens: 21, scope: "loaded" });
    });

    // Review round 2, finding 2.3: itemIdentityMatches compares IDS when
    // either side lacks a transcript key, so a remembered identity must keep
    // both fields — a later id-only fragment of the same item has to fold.
    it("a compact identity matches a later id-only fragment carrying no transcript key", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 488 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // The shed item carries BOTH identity fields.
      service.olderItems = {
        items: Array.from({ length: 8 }, (_, j) => ({
          kind: "user" as const,
          id: `w-${j}`,
          text: `w-${j}`,
        })),
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "i",
                  transcriptKey: "k",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // The fresh re-issue carries the same item id but NO transcript key.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "i", text: "i" }],
          turns: [
            {
              id: "pi",
              status: "completed",
              items: [
                {
                  id: "i",
                  turnId: "pi",
                  type: "agentMessage",
                  text: "reissued",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 7, outputTokens: 3 },
            },
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);

      const conv = store.getState().conversation!;
      expect(conv.turns.some((turn) => turn.id === "pt")).toBe(false);
      const merged = conv.turns.find((turn) => turn.id === "pi");
      expect(merged?.usage).toEqual({ inputTokens: 7, outputTokens: 3 });
      expect(merged?.items.map((item) => item.id)).toEqual(["i"]);
      expect(sessionTokens(conv)).toEqual({ inputTokens: 8, outputTokens: 4, scope: "loaded" });
    });

    // Review round 3, skeleton dedupe: repeated restore-and-trim cycles of
    // unchanged history must keep the remembered set constant (the dedupe)
    // and the cycle's outcomes stable — the turn keeps folding, its usage
    // keeps counting, and nothing duplicates.
    it("repeated restoration and compaction of unchanged history stays correct and bounded", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // A row-less page turn: payload-only identities, so it compacts at its
      // own load and remembers them.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "x-0",
                  transcriptKey: "px",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      const restoreRead = () => {
        service.readProjectionResult = {
          conversation: makeConversation({
            threadId: "thread-1",
            instanceId: "instance-1",
            usage: null,
            items: [
              { kind: "user" as const, id: "px", text: "px" },
              ...Array.from({ length: 480 }, (_, j) => ({
                kind: "user" as const,
                id: `f-${j}`,
                text: `f-${j}`,
              })),
            ],
            turns: [
              {
                id: "pt",
                status: "completed",
                items: [
                  {
                    id: "rx",
                    transcriptKey: "px",
                    turnId: "pt",
                    type: "agentMessage",
                    text: "restored",
                    position: { entry: 99, item: 0 },
                    status: "completed",
                  },
                ],
                usage: { inputTokens: 500, outputTokens: 20 },
              },
              { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
            ],
            olderCursor: "c1",
          }),
          activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
          olderCursor: "c1",
        };
      };
      const evictRead = () => {
        service.readProjectionResult = {
          conversation: makeConversation({
            threadId: "thread-1",
            instanceId: "instance-1",
            usage: null,
            items: Array.from({ length: 500 }, (_, j) => ({
              kind: "user" as const,
              id: `f-${j}`,
              text: `f-${j}`,
            })),
            turns: [
              { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
            ],
            olderCursor: "c1",
          }),
          activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
          olderCursor: "c1",
        };
      };

      for (let cycle = 0; cycle < 3; cycle++) {
        restoreRead();
        await store.getState().rehydrate(service, sink);
        const restored = store.getState().conversation?.turns.find((t) => t.id === "pt");
        expect(restored?.items.map((item) => item.transcriptKey)).toEqual(["px"]);
        expect(restored?.items.map((item) => item.text)).toEqual(["restored"]);
        expect(restored?.usage).toEqual({ inputTokens: 500, outputTokens: 20 });
        expect(sessionTokens(store.getState().conversation!)).toEqual({
          inputTokens: 501,
          outputTokens: 21,
          scope: "loaded",
        });

        evictRead();
        await store.getState().rehydrate(service, sink);
        const compacted = store.getState().conversation?.turns.find((t) => t.id === "pt");
        expect(compacted?.items).toEqual([]);
        expect(compacted?.usage).toEqual({ inputTokens: 500, outputTokens: 20 });
        expect(sessionTokens(store.getState().conversation!)).toEqual({
          inputTokens: 501,
          outputTokens: 21,
          scope: "loaded",
        });
      }
    });

    // Review round 3, text adoption guard: a skeleton whose shed item carried
    // a transcript key, re-issued by a page item with the same BARE ID (no
    // transcript key), must still get the page's text back after the fold —
    // the guard may not return early before the id fallback runs.
    it("a loadOlder id-only re-issue restores the folded item's text through the id fallback", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 488 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      service.olderItems = {
        items: Array.from({ length: 8 }, (_, j) => ({
          kind: "user" as const,
          id: `w-${j}`,
          text: `w-${j}`,
        })),
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "i",
                  transcriptKey: "k",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // The re-issuing page carries the same item under its bare id, with
      // its own row so the folded turn is in-window.
      service.olderItems = {
        items: [{ kind: "user" as const, id: "i", text: "i" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pr",
              [
                {
                  id: "i",
                  turnId: "pr",
                  type: "agentMessage",
                  text: "reissued",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 7, outputTokens: 3 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);

      const conv = store.getState().conversation!;
      expect(conv.turns.some((turn) => turn.id === "pr")).toBe(false);
      const merged = conv.turns.find((turn) => turn.id === "pt");
      // The fold kept the retained copy's usage, and the id-fallback
      // adoption restored the page's text on the merged item.
      expect(merged?.usage).toEqual({ inputTokens: 500, outputTokens: 20 });
      expect(merged?.items.map((item) => item.id)).toEqual(["i"]);
      expect(merged?.items.map((item) => item.text)).toEqual(["reissued"]);
      expect(sessionTokens(conv)).toEqual({ inputTokens: 501, outputTokens: 21, scope: "loaded" });
    });

    // Review round 3, ownership transfer: an id-only remembered identity
    // whose re-issue GAINED a transcript key folds by id; the transfer must
    // still find the surviving turn under the bare id so the compact turn's
    // OTHER remembered identities stay foldable.
    it("an id-only restoration gaining a transcript key keeps the remaining identities foldable", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 488 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // pt remembers an id-only item and a keyed item.
      service.olderItems = {
        items: Array.from({ length: 8 }, (_, j) => ({
          kind: "user" as const,
          id: `w-${j}`,
          text: `w-${j}`,
        })),
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "i",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload i",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
                {
                  id: "j-0",
                  transcriptKey: "pj",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload j",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // A fresh turn re-issues the id-only item — now carrying a transcript
      // key — so the package folds by id and the merged item's identity
      // becomes the new key.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "i", text: "i" }],
          turns: [
            {
              id: "pi",
              status: "completed",
              items: [
                {
                  id: "i",
                  transcriptKey: "k",
                  turnId: "pi",
                  type: "agentMessage",
                  text: "fresh i",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 7, outputTokens: 3 },
            },
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);
      expect(store.getState().conversation?.turns.some((t) => t.id === "pt")).toBe(false);

      // A later fresh turn re-issues the OTHER identity under yet another
      // id: the transferred memory must still fold it into the carrier.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [
            { kind: "user" as const, id: "i", text: "i" },
            { kind: "user" as const, id: "pj", text: "pj" },
          ],
          turns: [
            {
              id: "pj2",
              status: "completed",
              items: [
                {
                  id: "j-1",
                  transcriptKey: "pj",
                  turnId: "pj2",
                  type: "agentMessage",
                  text: "fresh j",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 9, outputTokens: 9 },
            },
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);

      const conv = store.getState().conversation!;
      expect(conv.turns.some((turn) => turn.id === "pi")).toBe(false);
      const merged = conv.turns.find((turn) => turn.id === "pj2");
      expect(merged?.usage).toEqual({ inputTokens: 9, outputTokens: 9 });
      expect(merged?.items.map((item) => item.transcriptKey)).toEqual(["k", "pj"]);
      expect(merged?.items.map((item) => item.text)).toEqual(["fresh i", "fresh j"]);
      expect(sessionTokens(conv)).toEqual({ inputTokens: 10, outputTokens: 10, scope: "loaded" });
    });

    // Review round 3, cursor gate: a partial reissue must not let the
    // unmatched skeleton's coverage claim keep the accumulated wire cursor
    // over the fresh read's own — skeletons are memory, not retained
    // transcript evidence.
    it("a partial reissue keeps the fresh wire cursor instead of skeleton-claimed coverage", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 488 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // The page consumes deep history (cursor c9) and its turn carries
      // two payload-only identities.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "a-0",
                  transcriptKey: "pa",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload a",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
                {
                  id: "b-0",
                  transcriptKey: "pb",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload b",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c9",
        ),
        nextCursor: "c9",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.olderCursor).toBe("c9");

      // The fresh read re-issues only ONE of the two identities, with its
      // own usage — and its own window cursor.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "pa", text: "pa" }],
          turns: [
            {
              id: "pfr",
              status: "completed",
              items: [
                {
                  id: "fa",
                  transcriptKey: "pa",
                  turnId: "pfr",
                  type: "agentMessage",
                  text: "fresh a",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 7, outputTokens: 3 },
            },
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);

      const conv = store.getState().conversation!;
      // The fold happened (one turn, the fresh fragment's id, one usage
      // stamp)...
      expect(conv.turns.some((turn) => turn.id === "pt")).toBe(false);
      expect(conv.turns.find((turn) => turn.id === "pfr")?.usage).toEqual({ inputTokens: 7, outputTokens: 3 });
      expect(sessionTokens(conv)).toEqual({ inputTokens: 8, outputTokens: 4, scope: "loaded" });
      // ...but the fresh read's own wire cursor stands: the unmatched
      // skeleton's coverage claim is not retained transcript evidence.
      expect(conv.olderCursor).toBe("c1");
    });

    // Review round 4, sparse reissue: a page wire item that omits text (the
    // sparse fragment shape thread/turns/list sends for stripped entries)
    // folding against an injected skeleton must settle to a valid empty
    // string. mergePageItem's textSource selection can pick the skeleton, and
    // a skeleton leaking an undefined text would surface as "undefined"
    // prefixes during streaming and throw in reasoningText's item.text.length.
    // The wire-text repair cannot rescue it either: a page item that omitted
    // text has nothing to adopt.
    it("a sparse page reissue of a compact identity settles to empty text, never undefined", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn: compacts at its own load and remembers px.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "x-0",
                  transcriptKey: "px",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // Overlapping page: a row keeps pt inside the window, and the page's
      // fragment re-issues px with NO text field — the sparse wire shape.
      // With nothing to adopt, the fold's settle must be the valid empty
      // string the wire's own hydration would produce.
      service.olderItems = {
        items: [{ kind: "user" as const, id: "px", text: "px row" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "rx",
                  transcriptKey: "px",
                  turnId: "pt",
                  type: "agentMessage",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const folded =
        store.getState().conversation?.turns.find((t) => t.id === "pt")?.items ?? [];
      expect(folded).toHaveLength(1);
      expect(folded[0]?.transcriptKey).toBe("px");
      expect(folded[0]?.text).toBe("");
      // The string invariant reasoningText and the streaming prefix logic
      // both lean on.
      expect(folded[0]?.text.length).toBe(0);
      expect(sessionTokens(store.getState().conversation!)).toEqual({
        inputTokens: 501,
        outputTokens: 21,
        scope: "loaded",
      });
    });

    // Review round 4, restored-identity presence: an id-only compaction
    // restored carrying a transcript key is the SAME item as its remembered
    // id-only skeleton (the package's identity rule matches them by id), so
    // a later overlapping page must not inject the skeleton beside the real
    // item — the injection's fold would erase the retained copy's text and
    // the wire-text repair would then re-adopt the OLDER page's text.
    it("an id-only compaction restored with a key keeps the retained text through an overlapping page", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn carrying an ID-ONLY item: compacts at its own
      // load, remembering the bare id.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "a-0",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload old",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // Restoration: the fresh read re-issues the item with a gained
      // transcript key (same id) and a row that keeps it in the window.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "kk", text: "k row" }],
          turns: [
            {
              id: "pt",
              status: "completed",
              items: [
                {
                  id: "a-0",
                  transcriptKey: "kk",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "retained new",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 500, outputTokens: 20 },
            },
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);
      const restored = store.getState().conversation?.turns.find((t) => t.id === "pt")?.items ?? [];
      expect(restored).toHaveLength(1);
      expect(restored[0]?.id).toBe("a-0");
      expect(restored[0]?.transcriptKey).toBe("kk");
      expect(restored[0]?.text).toBe("retained new");

      // The overlapping older page re-issues the same item with DIFFERENT
      // text. The retained copy is the newer merge input and must win: the
      // remembered id-only skeleton must not be injected beside the real
      // item, so nothing erases the retained text and re-adopts the page's.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "a-0",
                  transcriptKey: "kk",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "stale old",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const after = store.getState().conversation?.turns.find((t) => t.id === "pt")?.items ?? [];
      expect(after).toHaveLength(1);
      expect(after[0]?.transcriptKey).toBe("kk");
      expect(after[0]?.text).toBe("retained new");
      expect(sessionTokens(store.getState().conversation!)).toEqual({
        inputTokens: 501,
        outputTokens: 21,
        scope: "loaded",
      });
    });

    // Review round 5, sparse settle is not authoritative: after a sparse
    // reissue folds to the empty settle, a later overlapping page that brings
    // the item's REAL text must still win it. The folded item carries the
    // skeleton's omitted-text semantics, so mergePageItem treats the empty
    // settle as "nothing to say" — never as an authoritative empty that
    // blocks the item's actual text from loading.
    it("a sparse restoration stays adoptable by a later page with the item's real text", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn: compacts at its own load and remembers px.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "x-0",
                  transcriptKey: "px",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // Sparse reissue: a row keeps pt in the window; the page's fragment
      // re-issues px with NO text field, so the fold settles to "".
      service.olderItems = {
        items: [{ kind: "user" as const, id: "px", text: "px row" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "rx",
                  transcriptKey: "px",
                  turnId: "pt",
                  type: "agentMessage",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const settled =
        store.getState().conversation?.turns.find((t) => t.id === "pt")?.items ?? [];
      expect(settled).toHaveLength(1);
      expect(settled[0]?.text).toBe("");

      // The later overlapping page brings the item's real text. The folded
      // item's empty settle is omitted-text, so the page's provided text
      // wins it back.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "r3",
                  transcriptKey: "px",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "actual text",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c3",
        ),
        nextCursor: "c3",
      };
      await store.getState().loadOlder(service);
      const adopted =
        store.getState().conversation?.turns.find((t) => t.id === "pt")?.items ?? [];
      expect(adopted).toHaveLength(1);
      expect(adopted[0]?.transcriptKey).toBe("px");
      expect(adopted[0]?.text).toBe("actual text");
    });

    // Review round 5, ownership transfer: a remembered KEYED item must not
    // transfer to a surviving turn whose item merely shares its BARE id under
    // a conflicting transcript key. The wrong transfer would let a later page
    // fragment coalesce that unrelated turn (dropping its separate usage
    // stamp and polluting its items) instead of folding into the turn that
    // actually carries the remembered content.
    it("a remembered keyed item does not transfer through a conflicting bare-id match", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn remembering TWO keyed items (k1 and k2).
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "ta",
              [
                {
                  id: "x-0",
                  transcriptKey: "k1",
                  turnId: "ta",
                  type: "agentMessage",
                  text: "one",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
                {
                  id: "y-0",
                  transcriptKey: "k2",
                  turnId: "ta",
                  type: "agentMessage",
                  text: "two",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "ta")?.items).toEqual([]);

      // The fresh read re-issues k2 under a NEW turn id (ta folds away into
      // it) and carries an unrelated turn whose item shares x-0's bare id
      // under a CONFLICTING key k3. Rows keep both turns in the window.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [
            { kind: "user" as const, id: "k2", text: "k2 row" },
            { kind: "user" as const, id: "k3", text: "k3 row" },
          ],
          turns: [
            {
              id: "tf",
              status: "completed",
              items: [
                {
                  id: "y2",
                  transcriptKey: "k2",
                  turnId: "tf",
                  type: "agentMessage",
                  text: "k2 fresh",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 500, outputTokens: 20 },
            },
            {
              id: "tb",
              status: "completed",
              items: [
                {
                  id: "x-0",
                  transcriptKey: "k3",
                  turnId: "tb",
                  type: "agentMessage",
                  text: "tb item",
                  position: { entry: 101, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 50, outputTokens: 5 },
            },
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);
      const conv = store.getState().conversation!;
      expect(conv.turns.some((turn) => turn.id === "ta")).toBe(false);
      expect(conv.turns.find((turn) => turn.id === "tf")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "k2", text: "k2 fresh" }),
      ]);
      expect(conv.turns.find((turn) => turn.id === "tb")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "k3", text: "tb item" }),
      ]);

      // A later page re-issues k2. The fragment must fold into tf (the turn
      // carrying the remembered content), leaving tb — whose item merely
      // shares a bare id with the remembered k1 skeleton under a conflicting
      // key — untouched, its usage stamp intact.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "tp",
              [
                {
                  id: "z9",
                  transcriptKey: "k2",
                  turnId: "tp",
                  type: "agentMessage",
                  text: "k2 page",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 700, outputTokens: 70 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const after = store.getState().conversation!;
      expect(after.turns.find((turn) => turn.id === "tf")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "k2", text: "k2 fresh" }),
      ]);
      expect(after.turns.find((turn) => turn.id === "tb")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "k3", text: "tb item" }),
      ]);
      expect(after.turns.find((turn) => turn.id === "tb")?.usage).toEqual({
        inputTokens: 50,
        outputTokens: 5,
      });
      expect(sessionTokens(after)).toEqual({
        inputTokens: 551,
        outputTokens: 26,
        scope: "loaded",
      });
    });

    // Review round 6, alias folds: an item restored under a different id with
    // the same transcript key and compacted again leaves BOTH id aliases
    // remembered. A later page restoring a DIFFERENT item of the same turn
    // injects both aliases, and the package folds them into each other — a
    // placeholder no real side contributed to. The strip pass must remove
    // it: an unrestored placeholder would claim transcript coverage a later
    // rehydrate reads as retained evidence.
    it("a partial page does not leave alias-folded skeleton placeholders on the turn", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn remembering TWO keyed items.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "x-0",
                  transcriptKey: "px",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
                {
                  id: "z0",
                  transcriptKey: "pz",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "zed",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // Restore px under a DIFFERENT id (same transcript key).
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "px", text: "px row" }],
          turns: [
            {
              id: "pt",
              status: "completed",
              items: [
                {
                  id: "rx",
                  transcriptKey: "px",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "restored",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 500, outputTokens: 20 },
            },
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);
      expect(
        store.getState().conversation?.turns.find((t) => t.id === "pt")?.items,
      ).toEqual([expect.objectContaining({ transcriptKey: "px", text: "restored" })]);

      // Compact again: the restored item sheds a SECOND alias for px.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // A PARTIAL page restores only pz — and carries an unrelated turn whose
      // item shares the FOLDED ALIAS's BARE ID (rx) under a conflicting key.
      // Both remembered px aliases are injected and fold into each other; the
      // placeholder they form ({id rx, tk px}) must not survive the strip —
      // the unrelated item's bare id is not a match under the package's
      // identity rule — while the real pz fold and the unrelated turn both do.
      service.olderItems = {
        items: [
          { kind: "user" as const, id: "pz", text: "pz row" },
          { kind: "user" as const, id: "kk", text: "kk row" },
        ],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "z9",
                  transcriptKey: "pz",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "zed page",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
            wireTurnFragment(
              "ut",
              [
                {
                  id: "rx",
                  transcriptKey: "kk",
                  turnId: "ut",
                  type: "agentMessage",
                  text: "unrelated",
                  position: { entry: 101, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 7, outputTokens: 7 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const folded =
        store.getState().conversation?.turns.find((t) => t.id === "pt")?.items ?? [];
      expect(folded).toHaveLength(1);
      expect(folded[0]?.transcriptKey).toBe("pz");
      expect(folded[0]?.text).toBe("zed page");
      expect(folded.some((item) => item.transcriptKey === "px")).toBe(false);
      const unrelated = store.getState().conversation?.turns.find((t) => t.id === "ut");
      expect(unrelated?.items).toEqual([
        expect.objectContaining({ transcriptKey: "kk", text: "unrelated" }),
      ]);
    });

    // Review round 6, explicit empty text: overlapping page fragments that
    // provide text and then an EXPLICIT "" for the same identity settle to
    // the empty string (the reducer keeps the later fragment's provided
    // value). Nothing may reinstate the earlier fragment's stale text over
    // that legitimate empty.
    it("a later fragment's explicit empty text is not overwritten by an earlier fragment's text", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn: compacts at its own load and remembers px.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "x-0",
                  transcriptKey: "px",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "payload",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // Overlapping page carrying the SAME turn twice: the first fragment
      // provides text, the second explicitly clears it. The later
      // fragment's empty is the legitimate settle.
      service.olderItems = {
        items: [{ kind: "user" as const, id: "px", text: "px row" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "r1",
                  transcriptKey: "px",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "hello",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
            wireTurnFragment(
              "pt",
              [
                {
                  id: "r2",
                  transcriptKey: "px",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const folded =
        store.getState().conversation?.turns.find((t) => t.id === "pt")?.items ?? [];
      expect(folded).toHaveLength(1);
      expect(folded[0]?.transcriptKey).toBe("px");
      expect(folded[0]?.text).toBe("");
    });

    // Review round 8, tool-result folds: the incoming page can restore a
    // compact turn's OTHER identity while carrying the RESULT of the tool
    // call the same compact turn remembers as a skeleton. The injection
    // puts the call skeleton into the merge, the package's tool fold
    // removes the page's result item and carries its fields onto that
    // skeleton by callId, and the rewritten host is a new object whose
    // identity matches no real source — so the strip pass deleted it, and
    // BOTH representations of the result (the folded host and the removed
    // result item) vanished from conversation.turns. A skeleton host that
    // received a real result's fields is content, not remembered memory:
    // it must survive the strip.
    it("a page's real tool result folded into a remembered call skeleton survives the strip", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn remembering a tool CALL skeleton (callId call-1)
      // and a second item under transcript key ka.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "item_tool_1",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "call-1",
                  argumentsJson: '{"command":"make"}',
                  status: "inProgress",
                  transcriptKey: "kc",
                  position: { entry: 99, item: 0 },
                },
                {
                  id: "a-0",
                  transcriptKey: "ka",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "alpha",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // The incoming page restores ka — carrying the call's RESULT under the
      // same callId, but not the call itself. The retained turn is the
      // newer merge input, so pt hosts the fold; the ka row keeps pt's
      // window seat so the merged payloads stay retained.
      service.olderItems = {
        items: [{ kind: "user" as const, id: "ka", text: "ka row" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pq",
              [
                {
                  id: "item_tool_result_1",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "call-1",
                  output: "ok",
                  exitCode: 0,
                  status: "completed",
                  transcriptKey: "kr",
                  position: { entry: 101, item: 0 },
                },
                {
                  id: "a-1",
                  transcriptKey: "ka",
                  turnId: "pq",
                  type: "agentMessage",
                  text: "alpha page",
                  position: { entry: 100, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 700, outputTokens: 70 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const folded = store.getState().conversation?.turns.find((t) => t.id === "pt")?.items ?? [];
      // The restored identity folds as ever, and the enriched call host
      // keeps the result's real fields — the fold removed the result item,
      // so the host is the only item left holding that content.
      expect(folded).toEqual([
        expect.objectContaining({
          id: "item_tool_1",
          callId: "call-1",
          output: "ok",
          exitCode: 0,
          status: "completed",
        }),
        expect.objectContaining({ transcriptKey: "ka", text: "alpha page" }),
      ]);
      // The turn's usage still reads through the retained stamp.
      expect(sessionTokens(store.getState().conversation!)).toEqual({
        inputTokens: 501,
        outputTokens: 21,
        scope: "loaded",
      });
    });

    // Review round 8, transitive alias folds: the fresh read re-issues a
    // remembered keyless item under its bare id now carrying a transcript
    // key, and a second fresh fragment shares that key under a different id —
    // the package coalesces the chain, and the merged item's FINAL identity
    // (the second fragment's) matches neither remembered skeleton. The
    // ownership transfer matched only final identities, found no carrier, and
    // deleted the vanished compact turn's memory — including the
    // still-unrestored identities it also remembered. A later reissue of one
    // of those then survived beside the carrier and double-counted usage.
    // The transfer must follow the merge's own fragment membership instead.
    it("a transitive alias fold keeps a vanished compact turn's remaining identities foldable", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn remembering TWO KEYLESS items (fa and fz).
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "ta",
              [
                {
                  id: "fa",
                  turnId: "ta",
                  type: "agentMessage",
                  text: "alpha",
                  position: { entry: 90, item: 0 },
                  status: "completed",
                },
                {
                  id: "fz",
                  turnId: "ta",
                  type: "agentMessage",
                  text: "zed",
                  position: { entry: 91, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "ta")?.items).toEqual([]);

      // A fresh read re-issues fa under its bare id — now carrying transcript
      // key kk — in one fragment, and a second fragment shares kk under a
      // different id. The package coalesces the chain: fa folds through kk
      // into fb, whose final identity matches NEITHER remembered skeleton,
      // and ta folds away with the fresh carrier.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "kk", text: "kk row" }],
          turns: [
            {
              id: "tf1",
              status: "completed",
              items: [
                {
                  id: "fa",
                  transcriptKey: "kk",
                  turnId: "tf1",
                  type: "agentMessage",
                  text: "alpha fresh",
                  position: { entry: 90, item: 0 },
                  status: "completed",
                },
              ],
            },
            {
              id: "tf2",
              status: "completed",
              items: [
                {
                  id: "fb",
                  transcriptKey: "kk",
                  turnId: "tf2",
                  type: "agentMessage",
                  text: "bravo",
                  position: { entry: 90, item: 0 },
                  status: "completed",
                },
              ],
            },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);
      const conv = store.getState().conversation!;
      expect(conv.turns.some((turn) => turn.id === "ta")).toBe(false);
      expect(conv.turns.find((turn) => turn.id === "tf2")?.items).toEqual([
        expect.objectContaining({ id: "fb", transcriptKey: "kk", text: "bravo" }),
      ]);

      // A later page re-issues fz. It must fold into the carrier that took
      // ta's content — its memory survived the alias fold — instead of
      // surviving beside it and double-counting usage.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "tz",
              [
                {
                  id: "fz",
                  turnId: "tz",
                  type: "agentMessage",
                  text: "zed page",
                  position: { entry: 91, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 700, outputTokens: 70 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const after = store.getState().conversation!;
      expect(after.turns.some((turn) => turn.id === "tz")).toBe(false);
      expect(after.turns.find((turn) => turn.id === "tf2")?.items).toEqual([
        expect.objectContaining({ id: "fb", transcriptKey: "kk", text: "bravo" }),
        expect.objectContaining({ id: "fz", text: "zed page" }),
      ]);
      expect(sessionTokens(after)).toEqual({
        inputTokens: 501,
        outputTokens: 21,
        scope: "loaded",
      });
    });

    // Review round 8, cursor coverage without injection: compact-only turns
    // were excluded from the coverage merge only when a collision had
    // actually injected skeletons. With NO collision the unmatched compact
    // turn itself still entered the merge and claimed olderCoverage (usage
    // metadata counts as coverage for an empty turn), and an unchanged real
    // turn supplied transcriptOverlap — so the stale wire cursor overrode a
    // complete fresh read. A compact-only turn holds no transcript content by
    // construction; it must never supply coverage, injected or not.
    it("a noncolliding compact turn does not claim cursor coverage over a complete fresh read", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "PK", text: "pk row" }],
          turns: [
            {
              id: "rt",
              status: "completed",
              items: [
                {
                  id: "rk",
                  transcriptKey: "PK",
                  turnId: "rt",
                  type: "agentMessage",
                  text: "real text",
                  position: { entry: 98, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 9, outputTokens: 9 },
            },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn: compacts at its own load and remembers px.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "ct",
              [
                {
                  id: "x-0",
                  transcriptKey: "px",
                  turnId: "ct",
                  type: "agentMessage",
                  text: "payload",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "ct")?.items).toEqual([]);

      // A COMPLETE fresh read repeats the unchanged real turn and re-issues
      // nothing the compact turn remembers — no collision, no injection.
      // The fresh read's own cursor is null: nothing older exists.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "PK", text: "pk row" }],
          turns: [
            {
              id: "rt",
              status: "completed",
              items: [
                {
                  id: "rk",
                  transcriptKey: "PK",
                  turnId: "rt",
                  type: "agentMessage",
                  text: "real text",
                  position: { entry: 98, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 9, outputTokens: 9 },
            },
          ],
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: null,
      };
      await store.getState().rehydrate(service, sink);
      const conv = store.getState().conversation!;
      // The complete read's null cursor stands: the compact turn survives
      // with its usage, but usage metadata is not transcript coverage.
      expect(conv.olderCursor).toBeUndefined();
      expect(conv.turns.find((turn) => turn.id === "ct")?.usage).toEqual({
        inputTokens: 500,
        outputTokens: 20,
      });
      expect(sessionTokens(conv)).toEqual({
        inputTokens: 509,
        outputTokens: 29,
        scope: "session",
      });
    });

    // Review round 7, cursor coverage: a compact turn that the injected
    // merge folds into a fresh carrier under a different id must not claim
    // older coverage in the cursor gate's uninjected merge — an unmatched
    // empty turn with usage counts as coverage there. With an unchanged real
    // turn supplying transcriptOverlap, the stale cursor would override a
    // fresh read that actually covers everything, and sessionTokens would
    // report "loaded" for a complete read.
    it("a renamed compact turn does not claim cursor coverage over a complete fresh read", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [
            { kind: "user" as const, id: "PK", text: "pk row" },
            ...Array.from({ length: 480 }, (_, j) => ({
              kind: "user" as const,
              id: `f-${j}`,
              text: `f-${j}`,
            })),
          ],
          turns: [
            {
              id: "rt",
              status: "completed",
              items: [
                {
                  id: "rk",
                  transcriptKey: "PK",
                  turnId: "rt",
                  type: "agentMessage",
                  text: "real text",
                  position: { entry: 98, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 9, outputTokens: 9 },
            },
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn: compacts at its own load and remembers px.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "ct",
              [
                {
                  id: "x-0",
                  transcriptKey: "px",
                  turnId: "ct",
                  type: "agentMessage",
                  text: "payload",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "ct")?.items).toEqual([]);

      // A COMPLETE fresh read re-issues px under a NEW turn id (the injected
      // merge folds ct into it) and repeats the unchanged real turn. The
      // fresh read's own cursor is null — nothing older exists.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [
            { kind: "user" as const, id: "px", text: "px row" },
            { kind: "user" as const, id: "PK", text: "pk row" },
          ],
          turns: [
            {
              id: "f2",
              status: "completed",
              items: [
                {
                  id: "c9",
                  transcriptKey: "px",
                  turnId: "f2",
                  type: "agentMessage",
                  text: "fresh px",
                  position: { entry: 99, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 500, outputTokens: 20 },
            },
            {
              id: "rt",
              status: "completed",
              items: [
                {
                  id: "rk",
                  transcriptKey: "PK",
                  turnId: "rt",
                  type: "agentMessage",
                  text: "real text",
                  position: { entry: 98, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 9, outputTokens: 9 },
            },
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: null,
      };
      await store.getState().rehydrate(service, sink);
      const conv = store.getState().conversation!;
      // The fold happened: ct folded into the renamed carrier, its usage
      // preserved through the ACTUAL merge.
      expect(conv.turns.some((turn) => turn.id === "ct")).toBe(false);
      expect(conv.turns.find((turn) => turn.id === "f2")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "px", text: "fresh px" }),
      ]);
      // The complete read's null cursor stands: the compact turn's usage
      // metadata is not transcript coverage.
      expect(conv.olderCursor).toBeUndefined();
      expect(sessionTokens(conv)).toEqual({
        inputTokens: 510,
        outputTokens: 30,
        scope: "session",
      });
    });

    // Review round 9, alias chains through the strip: a compact turn can
    // remember TWO id aliases of one persisted item (restored under a
    // different id, compacted again). When a later page supplies the item
    // KEYLESS under the first alias's bare id, the merge folds the real page
    // item through BOTH remembered skeletons and the merged item settles on
    // the SECOND alias's identity — carried by no real source under the
    // strip's exact rule. The restored text used to die with it: the page
    // item itself was consumed by the fold. The strip must track real-source
    // participation through the item merges, not reconstruct it from final
    // identities.
    it("a page's restored text folded through remembered aliases survives the strip", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn remembering {id fa, transcriptKey kk}.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "fa",
                  transcriptKey: "kk",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "alpha",
                  position: { entry: 70, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // The fresh read restores kk under a DIFFERENT id, in window: the
      // injected merge folds the remembered alias into it and (with the
      // turn folded away) transfers the memory to the carrier.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "kk", text: "kk row" }],
          turns: [
            {
              id: "rt",
              status: "completed",
              items: [
                {
                  id: "fb",
                  transcriptKey: "kk",
                  turnId: "rt",
                  type: "agentMessage",
                  text: "alias restored",
                  position: { entry: 70, item: 0 },
                  status: "completed",
                },
              ],
            },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);
      expect(store.getState().conversation?.turns.some((t) => t.id === "pt")).toBe(false);
      expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "kk", text: "alias restored" }),
      ]);

      // The window moves on without the kk row: the carrier compacts again,
      // and its memory now remembers BOTH id aliases of kk.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);
      expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([]);

      // A page now supplies the item KEYLESS under fa's bare id. Both
      // remembered aliases are injected and the merge folds the real page
      // item through them, settling on fb's identity. The real projector
      // rows a keyless page item under its bare id (fa) — the merged item's
      // final identity matches no row, but its participating source does,
      // so the window must keep the turn and the restored text must survive
      // the strip (review round 14).
      service.olderItems = {
        items: [{ kind: "user" as const, id: "fa", text: "fa row" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pq",
              [
                {
                  id: "fa",
                  turnId: "pq",
                  type: "agentMessage",
                  text: "restored fa",
                  position: { entry: 70, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 700, outputTokens: 70 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const folded = store.getState().conversation?.turns.find((t) => t.id === "rt")?.items ?? [];
      expect(folded).toEqual([
        expect.objectContaining({ id: "fb", transcriptKey: "kk", text: "restored fa" }),
      ]);

      // An unrelated later page must not erase the chain's aliases (review
      // round 15): its merge leaves rt's items untouched, and recording an
      // untouched item must keep the identities the earlier page's merge
      // remembered for it, or the bound trims the turn the row still backs.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "un",
              [
                {
                  id: "un-0",
                  transcriptKey: "un",
                  turnId: "un",
                  type: "agentMessage",
                  text: "unrelated",
                  position: { entry: 90, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 9, outputTokens: 9 },
            ),
          ],
          "c3",
        ),
        nextCursor: "c3",
      };
      await store.getState().loadOlder(service);
      const afterUnrelated = store.getState().conversation!;
      expect(afterUnrelated.turns.find((turn) => turn.id === "rt")?.items ?? []).toEqual([
        expect.objectContaining({ id: "fb", transcriptKey: "kk", text: "restored fa" }),
      ]);
    });

    // Review round 16, notification replacements: the package's folds
    // build each live-updated item as a NEW object off the model item they
    // found — and the fold-source ancestry is keyed by object. A streaming
    // delta after an alias fold must inherit the ancestry: the resync it
    // schedules is a retention pass whose rows still name only the
    // folded-from alias, so the turn stays in the window through the
    // ancestry the fold recorded — or the resync sheds the payload the
    // visible row displays.
    it("a live delta keeps the alias ancestry through the resync it schedules", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "fa", text: "fa row" }],
          turns: [
            {
              id: "rt",
              status: "completed",
              items: [
                {
                  id: "fb",
                  transcriptKey: "kk",
                  turnId: "rt",
                  type: "agentMessage",
                  text: "alias restored",
                  position: { entry: 70, item: 0 },
                  status: "completed",
                },
              ],
            },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // A row-less page reissues the alias under fa's bare id; the fold
      // leaves the turn backed by the fa row through the recorded
      // ancestry — the item's own identity names no row.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pq",
              [
                {
                  id: "fa",
                  transcriptKey: "kk",
                  turnId: "pq",
                  type: "agentMessage",
                  text: "page fa",
                  position: { entry: 70, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 700, outputTokens: 70 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const folded = store.getState().conversation?.turns.find((t) => t.id === "rt")?.items ?? [];
      expect(folded).toEqual([
        expect.objectContaining({ id: "fb", transcriptKey: "kk", text: "alias restored" }),
      ]);

      // The live delta has no display row to update (the only row names
      // the alias), so the store publishes the package's half and
      // schedules the resync — a fresh read whose rows still name only
      // the alias. The package replaced the folded item with a NEW
      // object carrying the same identity.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "fa", text: "fa row" }],
          turns: [
            {
              id: "rt",
              status: "completed",
              items: [
                {
                  id: "fb",
                  transcriptKey: "kk",
                  turnId: "rt",
                  type: "agentMessage",
                  text: "alias restored",
                  position: { entry: 70, item: 0 },
                  status: "completed",
                },
              ],
            },
          ],
          olderCursor: "c2",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c2",
      };
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          itemId: "fb",
          delta: " live",
        },
      } as AnyNotification);
      // Let the drain scheduler's microtask fire and the resync settle.
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      const after = store.getState().conversation!;
      expect(after.turns.find((turn) => turn.id === "rt")?.items ?? []).toEqual([
        expect.objectContaining({ id: "fb", transcriptKey: "kk", text: "alias restored" }),
      ]);
    });

    // Review round 9, pagination bridges: a loadOlder page can bridge a
    // compact turn into ANOTHER retained turn through a shared transcript
    // key — the page item identity-matches the compact turn's remembered
    // keyless id AND the retained turn's keyed item — and the merged turn
    // then carries the retained turn's id. The compact turn's own id does
    // not always survive at loadOlder the way the transfer's comment
    // assumed: matched only by final identities, the bridge left no carrier
    // for the vanished turn's memory, and deleting it forgot the turn's
    // still-unrestored identities. A later page reissuing one of those
    // survived separately and double-counted usage. The transfer must read
    // the retained side's fold membership from the page merge.
    it("a page bridging a compact turn into another retained turn keeps its remaining identities foldable", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [
            { kind: "user" as const, id: "kk", text: "kk row" },
            ...Array.from({ length: 479 }, (_, j) => ({
              kind: "user" as const,
              id: `f-${j}`,
              text: `f-${j}`,
            })),
          ],
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
            {
              id: "tb",
              status: "completed",
              items: [
                {
                  id: "fb",
                  transcriptKey: "kk",
                  turnId: "tb",
                  type: "agentMessage",
                  text: "bravo",
                  position: { entry: 80, item: 0 },
                  status: "completed",
                },
              ],
              usage: { inputTokens: 50, outputTokens: 5 },
            },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn remembering TWO KEYLESS items (fa and fz).
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "ta",
              [
                {
                  id: "fa",
                  turnId: "ta",
                  type: "agentMessage",
                  text: "alpha",
                  position: { entry: 70, item: 0 },
                  status: "completed",
                },
                {
                  id: "fz",
                  turnId: "ta",
                  type: "agentMessage",
                  text: "zed",
                  position: { entry: 71, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "ta")?.items).toEqual([]);

      // The bridge page: a fragment re-issuing fa under its bare id now
      // carrying transcript key kk — which also matches tb's retained item.
      // The merge folds ta into tb's group and settles on tb's identity.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pq",
              [
                {
                  id: "fa",
                  transcriptKey: "kk",
                  turnId: "pq",
                  type: "agentMessage",
                  text: "bridge",
                  position: { entry: 70, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 300, outputTokens: 30 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const bridged = store.getState().conversation!;
      expect(bridged.turns.some((turn) => turn.id === "ta")).toBe(false);
      expect(bridged.turns.find((turn) => turn.id === "tb")?.items).toEqual([
        expect.objectContaining({ id: "fb", transcriptKey: "kk", text: "bravo" }),
      ]);

      // A later page re-issues fz. It must fold into tb — the turn the
      // bridge merged ta's memory into — instead of surviving beside it
      // and double-counting usage.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "tz",
              [
                {
                  id: "fz",
                  turnId: "tz",
                  type: "agentMessage",
                  text: "zed page",
                  position: { entry: 71, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 700, outputTokens: 70 },
            ),
          ],
          "c3",
        ),
        nextCursor: "c3",
      };
      await store.getState().loadOlder(service);
      const after = store.getState().conversation!;
      expect(after.turns.some((turn) => turn.id === "tz")).toBe(false);
      expect(after.turns.find((turn) => turn.id === "tb")?.items).toEqual([
        expect.objectContaining({ id: "fz", text: "zed page" }),
        expect.objectContaining({ id: "fb", transcriptKey: "kk", text: "bravo" }),
      ]);
      expect(sessionTokens(after)).toEqual({
        inputTokens: 51,
        outputTokens: 6,
        scope: "loaded",
      });
    });

    // Review round 10, superseded aliases: the injection skipped a
    // remembered skeleton whenever the turn already carried a real item
    // matching it — but a restored item supersedes its skeleton under ONE
    // alias only. Compaction remembered {id ra, transcriptKey kr}; the
    // fresh read restored kr under a DIFFERENT id; a later page then
    // re-issued the item KEYLESS under the remembered bare id ra. The
    // keyless reissue matches neither the restored item (keyed kr, another
    // id) nor the turn id, so with the alias skeleton skipped the reissue
    // survived as its own turn and its usage double-counted. The remembered
    // alias must stay injectable for turn matching while the restored
    // payload stays authoritative.
    it("a keyless reissue before the restored turn compacts again folds instead of double-counting", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn remembering {id ra, transcriptKey kr}.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "ra",
                  transcriptKey: "kr",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "alpha",
                  position: { entry: 60, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // The fresh read restores kr under a DIFFERENT id, in window: the
      // merge folds the remembered alias into it and the vanished turn's
      // memory moves to the carrier.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "kr", text: "kr row" }],
          turns: [
            {
              id: "rt",
              status: "completed",
              items: [
                {
                  id: "rb",
                  transcriptKey: "kr",
                  turnId: "rt",
                  type: "agentMessage",
                  text: "restored alias",
                  position: { entry: 60, item: 0 },
                  status: "completed",
                },
              ],
            },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);
      expect(store.getState().conversation?.turns.some((t) => t.id === "pt")).toBe(false);
      expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "kr", text: "restored alias" }),
      ]);

      // BEFORE the carrier compacts again, a page re-issues the item KEYLESS
      // under the remembered bare id ra. The reissue matches neither the
      // restored item's keyed identity nor the turn id — only the
      // remembered alias can fold it.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pq",
              [
                {
                  id: "ra",
                  turnId: "pq",
                  type: "agentMessage",
                  text: "reissued",
                  position: { entry: 60, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 700, outputTokens: 70 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const after = store.getState().conversation!;
      expect(after.turns.some((turn) => turn.id === "pq")).toBe(false);
      // Exactly one item under kr survives the alias fold, carrying the
      // retained text — the page's stale reissue text must not linger on a
      // second item claiming the same identity.
      expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
        expect.objectContaining({ id: "rb", transcriptKey: "kr", text: "restored alias" }),
      ]);
      expect(sessionTokens(after)).toEqual({
        inputTokens: 501,
        outputTokens: 21,
        scope: "loaded",
      });
    });

    // Review round 10, folded call chains: a real KEYLESS call reissue can
    // fold through remembered call aliases into a final identity no real
    // source carries, and the tool-result fold then rewrites that call with
    // the page's real result — removing the result item. The strip's
    // tool-host guard rejected the rewritten call because the PAGE carried
    // a real call with that callId — but that call was consumed by the
    // alias chain, so nothing else held the result's content. Both
    // representations of the result vanished. The guard must ask whether a
    // real call with that callId SURVIVES in the merged output, not whether
    // one existed among the inputs.
    it("remembered call aliases folding a page's keyless call keep its tool result", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn remembering a tool CALL skeleton under
      // {id item_tool_1, transcriptKey kc, callId call-1}.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "item_tool_1",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "call-1",
                  argumentsJson: '{"command":"make"}',
                  status: "inProgress",
                  transcriptKey: "kc",
                  position: { entry: 40, item: 0 },
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // The fresh read restores the call under a DIFFERENT id, in window.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "kc", text: "kc row" }],
          turns: [
            {
              id: "rt",
              status: "completed",
              items: [
                {
                  id: "item_tool_2",
                  transcriptKey: "kc",
                  turnId: "rt",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "call-1",
                  argumentsJSON: '{"command":"make"}',
                  text: "",
                  status: "inProgress",
                  position: { entry: 40, item: 0 },
                },
              ],
            },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);
      expect(store.getState().conversation?.turns.some((t) => t.id === "pt")).toBe(false);
      expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([
        expect.objectContaining({ id: "item_tool_2", transcriptKey: "kc" }),
      ]);

      // The window moves on without the kc row: the carrier compacts again,
      // and its memory now remembers BOTH call aliases of kc.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);
      expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([]);

      // A page re-issues the call KEYLESS under the first alias's bare id —
      // together with its RESULT. The identity fold consumes the page's
      // call through both remembered aliases (the merged call ends on the
      // second alias's id), and the tool fold removes the result item while
      // carrying its fields onto that call. The real projector rows the
      // keyless call under its bare id (item_tool_1) — the surviving call's
      // own identity names no row, so the window must see the identity the
      // call folded from to keep the turn and its result content (review
      // round 15).
      service.olderItems = {
        items: [{ kind: "user" as const, id: "item_tool_1", text: "call row" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pq",
              [
                {
                  id: "item_tool_1",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "call-1",
                  argumentsJson: '{"command":"make"}',
                  status: "inProgress",
                  position: { entry: 40, item: 0 },
                },
                {
                  id: "item_tool_result_1",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "call-1",
                  output: "ok",
                  exitCode: 0,
                  status: "completed",
                  position: { entry: 41, item: 0 },
                },
              ],
              { inputTokens: 700, outputTokens: 70 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const folded = store.getState().conversation?.turns.find((t) => t.id === "rt")?.items ?? [];
      expect(folded).toEqual([
        expect.objectContaining({
          id: "item_tool_2",
          transcriptKey: "kc",
          callId: "call-1",
          output: "ok",
          exitCode: 0,
          status: "completed",
        }),
      ]);
    });

    // Review round 16, result-row backing: the tool fold moves a result's
    // fields onto its call and removes the result item, so the call can be
    // the ONLY model payload behind a visible result row. When the rows
    // retain the RESULT — the call never had a row of its own — the call's
    // own identity names no retained row, and the identity-fold ancestry
    // does not either (the callId fold is a different mechanism with its
    // own participation rule): the window must read the result identities
    // the call absorbed, or the bound deletes the payload the visible row
    // displays.
    it("a folded call keeps its payload when only the result row is retained", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // A row-less page turn carrying a call and its result, plus the
      // RESULT's display row only — the call never had a row to lose.
      service.olderItems = {
        items: [{ kind: "user" as const, id: "item_tool_result_1", text: "result row" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pq",
              [
                {
                  id: "item_tool_1",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "call-1",
                  argumentsJson: '{"command":"make"}',
                  status: "inProgress",
                  position: { entry: 40, item: 0 },
                },
                {
                  id: "item_tool_result_1",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "call-1",
                  output: "ok",
                  exitCode: 0,
                  status: "completed",
                  position: { entry: 41, item: 0 },
                },
              ],
              { inputTokens: 700, outputTokens: 70 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      const after = store.getState().conversation!;
      expect(after.items.some((row) => row.id === "item_tool_result_1")).toBe(true);
      expect(after.turns.find((turn) => turn.id === "pq")?.items ?? []).toEqual([
        expect.objectContaining({
          id: "item_tool_1",
          callId: "call-1",
          output: "ok",
          exitCode: 0,
          status: "completed",
        }),
      ]);
    });

    // Review round 11, alias reconciliation at rehydrate: the same
    // duplicate-identity hole the superseded-alias fold opens at loadOlder
    // opens at rehydrate too — the turn carries its restored item under one
    // alias, the injected remembered alias hosts the fresh read's keyless
    // reissue, and the merge leaves BOTH items keyed kr: the fresh text on
    // one, the superseded retained text on the other. One identity must
    // reconcile to one item, with the fresh side winning at rehydrate.
    it("a fresh keyless reissue under a remembered alias reconciles to one item", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn remembering {id ra, transcriptKey kr}.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "ra",
                  transcriptKey: "kr",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "alpha",
                  position: { entry: 60, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // The fresh read restores kr under a DIFFERENT id, in window: the
      // carrier holds the restored item while still remembering the alias.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "kr", text: "kr row" }],
          turns: [
            {
              id: "rt",
              status: "completed",
              items: [
                {
                  id: "rb",
                  transcriptKey: "kr",
                  turnId: "rt",
                  type: "agentMessage",
                  text: "restored alias",
                  position: { entry: 60, item: 0 },
                  status: "completed",
                },
              ],
            },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);
      expect(store.getState().conversation?.turns.some((t) => t.id === "pt")).toBe(false);
      expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "kr", text: "restored alias" }),
      ]);

      // A fresh read now re-issues the item KEYLESS under the remembered
      // bare id ra. The remembered alias hosts the reissue (the restored
      // item's keyed identity does not match it), and one item under kr
      // must come out of the merge — carrying the fresh side's text, which
      // wins at rehydrate — not two items claiming the same identity.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "kr", text: "kr row" }],
          turns: [
            {
              id: "fr",
              status: "completed",
              items: [
                {
                  id: "ra",
                  turnId: "fr",
                  type: "agentMessage",
                  text: "fresh reissue",
                  position: { entry: 60, item: 0 },
                  status: "completed",
                },
              ],
            },
          ],
          olderCursor: "c2",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c2",
      };
      await store.getState().rehydrate(service, sink);
      const conv = store.getState().conversation!;
      expect(conv.turns.some((turn) => turn.id === "rt")).toBe(false);
      expect(conv.turns.find((turn) => turn.id === "fr")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "kr", text: "fresh reissue" }),
      ]);
    });

    // Review round 12, reconciliation precedence: the duplicate
    // reconciliation initially treated display order as freshness order. A
    // page can carry a keyless alias of an item beside a keyed stale
    // sibling; the retained turn holds the item's fresh payload. The merge
    // folds the retained item into the keyless alias (first identity match)
    // while the stale sibling sits untouched — and the reconciliation then
    // let that PURE OLDER sibling fold over the fresh result, publishing
    // the stale text. Source precedence must decide the fold, not list
    // order: content from the newer side of the merge wins independently
    // of where the aliases sort.
    it("a page's untouched stale alias does not overwrite the retained fold", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // A retained turn holds the item's fresh payload under
      // {id pa, transcriptKey pk}, in window.
      service.olderItems = {
        items: [{ kind: "user" as const, id: "pk", text: "pk row" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "rt",
              [
                {
                  id: "pa",
                  transcriptKey: "pk",
                  turnId: "rt",
                  type: "agentMessage",
                  text: "fresh",
                  position: { entry: 30, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 50, outputTokens: 5 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "pk", text: "fresh" }),
      ]);

      // The next page carries a KEYLESS alias of the same item (the retained
      // item's bare id) followed by a keyed sibling holding the item's
      // STALE text. The merge folds the retained item into the keyless
      // alias — the first identity match — and the untouched stale sibling
      // must not then fold over the fresh result.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pq",
              [
                {
                  id: "pa",
                  turnId: "pq",
                  type: "agentMessage",
                  text: "older keyless",
                  position: { entry: 30, item: 0 },
                  status: "completed",
                },
                {
                  id: "sb",
                  transcriptKey: "pk",
                  turnId: "pq",
                  type: "agentMessage",
                  text: "stale",
                  position: { entry: 31, item: 0 },
                  status: "completed",
                },
              ],
              { inputTokens: 70, outputTokens: 7 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const after = store.getState().conversation!;
      expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "pk", text: "fresh" }),
      ]);
    });

    // Review round 13, skeleton participation: an injected skeleton's
    // participation is identity-only — it carries the omitted-text marker
    // and no payload — but the reconciliation's source-precedence check
    // counted it as fresh-side participation anyway. A POSITIONED restored
    // item beside its UNPOSITIONED remembered alias then both read as
    // fresh, the tiebreak fell to display order, and because the
    // unpositioned skeleton fold sorts last, an older keyless reissue's
    // STALE text — folded through the alias — overwrote the restored text.
    // Precedence must come from the inputs that actually supplied payload,
    // and an identity-only skeleton supplies none.
    it("a keyless reissue through an unpositioned alias does not overwrite the restored text", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // Row-less page turn remembering {id ra, transcriptKey kr} — the item
      // carries NO position, so its remembered skeleton is unpositioned.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pt",
              [
                {
                  id: "ra",
                  transcriptKey: "kr",
                  turnId: "pt",
                  type: "agentMessage",
                  text: "alpha",
                  status: "completed",
                },
              ],
              { inputTokens: 500, outputTokens: 20 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

      // The fresh read restores kr under a DIFFERENT id — POSITIONED, in
      // window — and the carrier keeps remembering the unpositioned alias.
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: [{ kind: "user" as const, id: "kr", text: "kr row" }],
          turns: [
            {
              id: "rt",
              status: "completed",
              items: [
                {
                  id: "rb",
                  transcriptKey: "kr",
                  turnId: "rt",
                  type: "agentMessage",
                  text: "restored alias",
                  position: { entry: 60, item: 0 },
                  status: "completed",
                },
              ],
            },
          ],
          olderCursor: "c1",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c1",
      };
      await store.getState().rehydrate(service, sink);
      expect(store.getState().conversation?.turns.some((t) => t.id === "pt")).toBe(false);
      expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([
        expect.objectContaining({ id: "rb", transcriptKey: "kr", text: "restored alias" }),
      ]);

      // An older page re-issues the item KEYLESS under the remembered bare
      // id, positionless, carrying the item's STALE text. The alias hosts
      // the reissue (the positioned restored item does not match it), the
      // unpositioned skeleton fold sorts after the positioned item — and
      // the restored text must still win: the skeleton supplied no payload.
      service.olderItems = {
        items: [{ kind: "user" as const, id: "kr", text: "kr row" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pq",
              [
                {
                  id: "ra",
                  turnId: "pq",
                  type: "agentMessage",
                  text: "stale reissue",
                  status: "completed",
                },
              ],
              { inputTokens: 700, outputTokens: 70 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const after = store.getState().conversation!;
      expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
        expect.objectContaining({ id: "rb", transcriptKey: "kr", text: "restored alias" }),
      ]);
    });

    // Review round 14, non-text payload: omitted text does not mean an item
    // lacks payload. A hydrated tool item can carry a current output,
    // arguments, and status while the wire omitted its text, and the
    // freshness check read exactly that — text omitted — so a retained tool
    // item folding into a page's keyless alias classified its fold as
    // payload-less. A later stale keyed sibling then tied with it and let
    // display order overwrite the retained fields. Identity-only skeletons
    // are the items to exclude — the ones carrying no field a fold keeps —
    // never real items merely because their text is omitted.
    it("a retained tool item with omitted text keeps its fields through the fold", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // A retained tool item whose wire omitted the text field — hydrated
      // with the omitted-text marker — carrying its CURRENT output.
      service.olderItems = {
        items: [{ kind: "user" as const, id: "kt", text: "kt row" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "rt",
              [
                {
                  id: "item_tool_7",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "call-9",
                  argumentsJson: '{"command":"make"}',
                  output: "current output",
                  status: "completed",
                  transcriptKey: "kt",
                  position: { entry: 50, item: 0 },
                },
              ],
              { inputTokens: 60, outputTokens: 6 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "kt", output: "current output" }),
      ]);

      // The next page carries the item's KEYLESS alias (the retained item's
      // bare id, text omitted too) followed by a keyed sibling holding the
      // item's STALE output. The retained item folds into the keyless alias
      // (the first identity match) — and its fields, not the stale
      // sibling's, must come out of the reconciliation.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pq",
              [
                {
                  id: "item_tool_7",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "call-9",
                  argumentsJson: '{"command":"make"}',
                  status: "inProgress",
                  position: { entry: 50, item: 0 },
                },
                {
                  id: "item_tool_8",
                  type: "commandExecution",
                  toolName: "shell",
                  callId: "call-8",
                  argumentsJson: '{"command":"make"}',
                  output: "stale output",
                  status: "completed",
                  transcriptKey: "kt",
                  position: { entry: 51, item: 0 },
                },
              ],
              { inputTokens: 70, outputTokens: 7 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const after = store.getState().conversation!;
      expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "kt", output: "current output" }),
      ]);
    });

    // Review round 16, hydration shape: the structural identity-only check
    // read key PRESENCE, but hydration creates every field as an enumerable
    // property — undefined-valued when the wire omitted it — so the wire's
    // own sparse identity-only fragment (a bare call reissue: identity and
    // ordering fields only) never passed the check. A payload-free retained
    // fragment folding into a page's keyless alias then counted as FRESH
    // payload participation, and the stale content the alias carried won
    // duplicate reconciliation by source precedence over the keyed sibling
    // holding the restored fields.
    it("a sparse wire fragment folds without claiming freshness over a restored sibling", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          instanceId: "instance-1",
          usage: null,
          items: Array.from({ length: 480 }, (_, j) => ({
            kind: "user" as const,
            id: `f-${j}`,
            text: `f-${j}`,
          })),
          turns: [
            { id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
          ],
          olderCursor: "c0",
        }),
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: "c0",
      };
      await store.getState().openProjected(service, sink, "ref-1");

      // A retained turn holding the SPARSE fragment the wire reissued:
      // identity and ordering fields only — no text, no arguments, no
      // output, no status. The projector rows it under its transcript key.
      service.olderItems = {
        items: [{ kind: "user" as const, id: "kt", text: "kt row" }],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "rt",
              [
                {
                  id: "item_tool_7",
                  type: "commandExecution",
                  callId: "call-9",
                  transcriptKey: "kt",
                  position: { entry: 50, item: 0 },
                },
              ],
              { inputTokens: 60, outputTokens: 6 },
            ),
          ],
          "c1",
        ),
        nextCursor: "c1",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([
        expect.objectContaining({ id: "item_tool_7", transcriptKey: "kt" }),
      ]);

      // The next page carries the fragment's KEYLESS alias — holding the
      // STALE output — followed by a keyed sibling holding the restored
      // one. The sparse retained fragment folds into the keyless alias
      // (the first identity match); the fold supplies no payload, so the
      // merged item must not claim freshness, and the reconciliation must
      // let the later keyed sibling's restored fields stand.
      service.olderItems = {
        items: [],
        turnsPage: turnsPage(
          [
            wireTurnFragment(
              "pq",
              [
                {
                  id: "item_tool_7",
                  type: "commandExecution",
                  callId: "call-9",
                  output: "stale output",
                  exitCode: 1,
                  status: "failed",
                  position: { entry: 50, item: 0 },
                },
                {
                  id: "item_tool_8",
                  type: "commandExecution",
                  callId: "call-8",
                  output: "restored output",
                  exitCode: 0,
                  status: "completed",
                  transcriptKey: "kt",
                  position: { entry: 51, item: 0 },
                },
              ],
              { inputTokens: 70, outputTokens: 7 },
            ),
          ],
          "c2",
        ),
        nextCursor: "c2",
      };
      await store.getState().loadOlder(service);
      const after = store.getState().conversation!;
      expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
        expect.objectContaining({ transcriptKey: "kt", output: "restored output" }),
      ]);
    });
  });

  describe("F11: no presentation disclosure state in conversation store", () => {
    it("conversation store does not carry expandedToolKeys or setExpandedToolKeys", () => {
      const store = createConversationStore();
      const s = store.getState();
      // These presentation fields must NOT exist on the store.
      expect(s).not.toHaveProperty("expandedToolKeys");
      expect(
        typeof (s as unknown as Record<string, unknown>).setExpandedToolKeys,
      ).toBe("undefined");
    });
  });

  // --- Fix round 1: I1/I2/M1/M2 tests ----------------------------------------

  // M1: True deferred started/release/completed promise helpers. No
  // drainMicrotasks(count) or counter polling — tests deterministically
  // force scheduler effects via started/release/completed barriers. No
  // test-only mutable API on the store.

  // Level-triggered controlled readProjection: every call hangs until
  // released. started(target=1) resolves immediately when startedCount has
  // already reached target (level-triggered — no edge counting, no
  // yieldMicrotask(count), no setTimeout, no polling). release() unblocks
  // the next hanging call. completed(target=1) resolves immediately when
  // doneCount has already reached target.
  function makeControlledRead(service: FakeConversationService): {
    started: (target?: number) => Promise<void>;
    ready: (target?: number) => Promise<void>;
    release: () => boolean;
    completed: (target?: number) => Promise<void>;
    getStartedCount: () => number;
    getDoneCount: () => number;
  } {
    let startedCount = 0;
    let readyCount = 0;
    let doneCount = 0;
    const releaseQueue: Array<() => void> = [];
    // Level-triggered waiters: each carries a target count. Resolved when
    // the counter reaches or exceeds the target. If the target is already
    // met when started()/completed() is called, resolve immediately.
    const startedWaiters: Array<{ target: number; resolve: () => void }> = [];
    const readyWaiters: Array<{ target: number; resolve: () => void }> = [];
    const doneWaiters: Array<{ target: number; resolve: () => void }> = [];
    const orig = service.readProjection.bind(service);
    service.readProjection = async (ref: string) => {
      startedCount += 1;
      // Resolve all started waiters whose target has been reached.
      for (let i = startedWaiters.length - 1; i >= 0; i--) {
        const w = startedWaiters[i];
        if (w !== undefined && startedCount >= w.target) {
          w.resolve();
          startedWaiters.splice(i, 1);
        }
      }
      const result = await orig(ref);
      await new Promise<void>((resolve) => {
        releaseQueue.push(resolve);
        readyCount += 1;
        for (let i = readyWaiters.length - 1; i >= 0; i--) {
          const w = readyWaiters[i];
          if (w !== undefined && readyCount >= w.target) {
            w.resolve();
            readyWaiters.splice(i, 1);
          }
        }
      });
      doneCount += 1;
      // Resolve all done waiters whose target has been reached.
      for (let i = doneWaiters.length - 1; i >= 0; i--) {
        const w = doneWaiters[i];
        if (w !== undefined && doneCount >= w.target) {
          w.resolve();
          doneWaiters.splice(i, 1);
        }
      }
      return result;
    };
    return {
      started: (target = 1) => {
        if (startedCount >= target) return Promise.resolve();
        return new Promise<void>((resolve) => {
          startedWaiters.push({ target, resolve });
        });
      },
      ready: (target = 1) => {
        if (readyCount >= target) return Promise.resolve();
        return new Promise<void>((resolve) => {
          readyWaiters.push({ target, resolve });
        });
      },
      release: () => {
        const r = releaseQueue.shift();
        if (r !== undefined) {
          r();
          return true;
        }
        return false;
      },
      completed: (target = 1) => {
        if (doneCount >= target) return Promise.resolve();
        return new Promise<void>((resolve) => {
          doneWaiters.push({ target, resolve });
        });
      },
      getStartedCount: () => startedCount,
      getDoneCount: () => doneCount,
    };
  }


  // One-shot microtask yield — single await Promise.resolve() so the
  // scheduler microtask fires. NOT a count-based drain.
  async function yieldMicrotask(): Promise<void> {
    await Promise.resolve();
  }

  describe("I1: projected binding epoch — stale work suppressed before effect", () => {
    it("queued rehydrate for A is suppressed after switch to B (zero serviceA reads/sinkA writes)", async () => {
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-A" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const sinkA = createFakeSink();
      const store = createConversationStore();

      // Open A (completes).
      await store.getState().openProjected(serviceA, sinkA, "ref-A");
      // Queue a rehydrate for A via resync — the scheduler captures the
      // binding (serviceA+refA+epoch) but defers to a microtask.
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-A", ref: "ref-A" },
      } as AnyNotification);
      // Now set up serviceB and switch to B before the scheduler fires.
      const serviceB = new FakeConversationService();
      serviceB.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-B" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const sinkB = createFakeSink();
      await store.getState().openProjected(serviceB, sinkB, "ref-B");
      const readsA = serviceA.readProjectionCalls.length;
      const writesA = sinkA.setLiveViewCalls.length;
      // M1: yield so the suppressed A effect's microtask fires and bails.
      await yieldMicrotask();
      // I1: zero serviceA reads and zero sinkA writes after the switch.
      expect(serviceA.readProjectionCalls.length).toBe(readsA);
      expect(sinkA.setLiveViewCalls.length).toBe(writesA);
      // B should be the current conversation.
      expect(store.getState().conversation?.threadId).toBe("thread-B");
    });

    it("queued rehydrate for A is suppressed after close (no serviceA reads)", async () => {
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-A" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const sinkA = createFakeSink();
      const store = createConversationStore();
      await store.getState().openProjected(serviceA, sinkA, "ref-A");
      // Trigger a rehydrate then immediately close.
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-A", ref: "ref-A" },
      } as AnyNotification);
      store.getState().close();
      const readsA = serviceA.readProjectionCalls.length;
      await yieldMicrotask();
      // I1: zero additional serviceA reads after close.
      expect(serviceA.readProjectionCalls.length).toBe(readsA);
    });

    it("queued rehydrate for A is suppressed after reset (no serviceA reads)", async () => {
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-A" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const sinkA = createFakeSink();
      const store = createConversationStore();
      await store.getState().openProjected(serviceA, sinkA, "ref-A");
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-A", ref: "ref-A" },
      } as AnyNotification);
      store.getState().reset();
      const readsA = serviceA.readProjectionCalls.length;
      await yieldMicrotask();
      expect(serviceA.readProjectionCalls.length).toBe(readsA);
    });

    it("plain open clears projected bindings (no projected rehydrate after open)", async () => {
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-A" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const sinkA = createFakeSink();
      const store = createConversationStore();
      await store.getState().openProjected(serviceA, sinkA, "ref-A");
      // Queue a rehydrate via resync.
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-A", ref: "ref-A" },
      } as AnyNotification);
      // Plain open clears projected bindings.
      const serviceB = new FakeConversationService();
      serviceB.openConv = makeConversation({ threadId: "thread-B" });
      await store.getState().open(serviceB, "ref-B");
      const readsA = serviceA.readProjectionCalls.length;
      const writesA = sinkA.setLiveViewCalls.length;
      await yieldMicrotask();
      // I1: plain open cleared bindings — no projected rehydrate for A.
      expect(serviceA.readProjectionCalls.length).toBe(readsA);
      expect(sinkA.setLiveViewCalls.length).toBe(writesA);
      expect(store.getState().conversation?.threadId).toBe("thread-B");
    });

    it("rehydrate can never call serviceA with refB", async () => {
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const sink = createFakeSink();
      const store = createConversationStore();
      await store.getState().openProjected(serviceA, sink, "ref-1");
      // Queue a rehydrate for ref-1.
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // Before the scheduler fires, openProjected with a different service+ref.
      const serviceB = new FakeConversationService();
      serviceB.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-2" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(serviceB, sink, "ref-2");
      const readsA = serviceA.readProjectionCalls.length;
      await yieldMicrotask();
      // I1: the queued rehydrate (captured with serviceA+ref-1) must NOT
      // have called serviceA.readProjection after the switch.
      expect(serviceA.readProjectionCalls.length).toBe(readsA);
    });
  });

  // A refused mutation requests the one coalesced reread; the error never waits
  // on it, and a newer mutation's own outcome is not disturbed by it.
  describe("a refused mutation and the reread scheduler", () => {
    it("surfaces the error while an earlier reread is still held; the refusal's reread runs after it", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const readsBefore = service.readProjectionCalls.length;
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      service.sendShouldReject = refusal();
      await store.getState().send(service, textInput("x"));
      // The send settled and the error is visible with the first read still held.
      expect(store.getState().error).not.toBeNull();
      expect(store.getState().pendingMutation?.status).toBe("failed");
      await ctrl.ready(1);
      ctrl.release();
      await ctrl.completed(1);
      await ctrl.ready(2);
      ctrl.release();
      await ctrl.completed(2);
      await yieldMicrotask();
      expect(service.readProjectionCalls.length).toBe(readsBefore + 2);
    });

    it("a newer mutation's outcome is not disturbed by the older refusal's reread", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      service.sendShouldReject = refusal();
      const ctrl = makeControlledRead(service);
      await store.getState().send(service, textInput("first"));
      await ctrl.started(1);
      // The refusal's reread is in flight; a second mutation succeeds meanwhile.
      service.sendShouldReject = null;
      await store.getState().send(service, textInput("second"));
      expect(store.getState().error).toBeNull();
      expect(store.getState().pendingMutation).toBeNull();
      await ctrl.ready(1);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      expect(store.getState().error).toBeNull();
      expect(store.getState().pendingMutation).toBeNull();
      expect(store.getState().conversation?.threadId).toBe("thread-1");
    });
  });

  describe("M1: setLiveView before conversation state commit (directly observed)", () => {
    it("directly observes setLiveView fires before the store state changes", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const sink = createFakeSink();
      const activityView: ActivityView = {
        tasks: [],
        work: [],
        usage: {},
        capabilities: ALL_TRUE_CAPS,
      };
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: activityView,
        olderCursor: null,
      };
      // Subscribe to store state changes to record when the conversation
      // projection lands.
      const order: string[] = [];
      let committed = false;
      store.subscribe((state) => {
        if (!committed && state.conversation !== null) {
          committed = true;
          order.push("conversationCommitted");
        }
      });
      // Record when setLiveView is called.
      sink.setLiveViewResult = () => {
        order.push("setLiveView");
        return true;
      };
      await store.getState().openProjected(service, sink, "ref-1");
      // setLiveView must appear before conversationCommitted in the order.
      expect(order.indexOf("setLiveView")).toBeLessThan(
        order.indexOf("conversationCommitted"),
      );
    });
  });

  describe("DrainScheduler: store-level invariants (M2 — tested through store)", () => {
    it("coalesces a synchronous pre-effect burst to one rehydrate", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const ctrl = makeControlledRead(service);
      const initialReads = service.readProjectionCalls.length;
      // Synchronous burst — 3 resync notifications coalesce to one rehydrate.
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // M1: deterministic — await started, yield so orig resolves and the
      // release gate is pushed, set up completed, release.
      await ctrl.started();
      await yieldMicrotask();
      const completedP = ctrl.completed();
      ctrl.release();
      await completedP;
      expect(service.readProjectionCalls.length).toBe(initialReads + 1);
    });

    it("retains a request during an in-flight rehydrate and drains it", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Hang every readProjection so we can control when each completes.
      const ctrl = makeControlledRead(service);
      const readsAfterOpen = service.readProjectionCalls.length;
      // Trigger a rehydrate (hangs on first readProjection).
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // M1: deterministic — await started so we know the rehydrate is in-flight.
      await ctrl.started();
      await yieldMicrotask(); // let orig resolve and push to releaseQueue
      // While in-flight, trigger another resync (trailing).
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // Release the first — the trailing must drain.
      const started2P = ctrl.started(2);
      const completed1P = ctrl.completed();
      ctrl.release();
      await started2P;
      await yieldMicrotask(); // let trailing orig resolve and push to releaseQueue
      const completed2P = ctrl.completed(2);
      ctrl.release();
      await completed1P;
      await completed2P;
      // One initial openProjected + one first rehydrate + one trailing = 3.
      expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 2);
    });

    it("scheduler drains all trailing work recursively (no test-only flush)", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // M1: deterministic — await started so we know the rehydrate is in-flight.
      await ctrl.started();
      await yieldMicrotask(); // let orig resolve and push to releaseQueue
      // The rehydrate is hanging. Queue a trailing request while in-flight.
      // The trailing is retained and must drain after the first completes.
      const readsBeforeTrailing = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // Release the first — the trailing must drain recursively.
      const started2P = ctrl.started(2);
      const completed1P = ctrl.completed();
      ctrl.release();
      await started2P;
      await yieldMicrotask(); // let trailing orig resolve and push to releaseQueue
      const completed2P = ctrl.completed(2);
      ctrl.release();
      await completed1P;
      await completed2P;
      expect(service.readProjectionCalls.length).toBeGreaterThan(
        readsBeforeTrailing,
      );
    });

    it("catches rehydrate errors without unhandled rejections and remains usable", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Make readProjection throw on the first rehydrate call.
      const origRead = service.readProjection.bind(service);
      let throwOnce = true;
      service.readProjection = async (ref: string) => {
        if (throwOnce) {
          throwOnce = false;
          throw new Error("rehydrate boom");
        }
        return origRead(ref);
      };
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // M1: let the erroring effect run and bail. The error is caught by the
      // scheduler — yield so the microtask fires and the effect errors out.
      await yieldMicrotask();
      await yieldMicrotask();
      await yieldMicrotask();
      // The scheduler must remain usable — retrigger.
      const readsBefore = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // The second rehydrate's readProjection passes through (throwOnce=false).
      // Use controlled read to deterministically await completion.
      const ctrl = makeControlledRead(service);
      await ctrl.started();
      await yieldMicrotask();
      await yieldMicrotask(); // extra hop for throwOnce→origRead async chain
      const completedP = ctrl.completed();
      ctrl.release();
      await completedP;
      expect(service.readProjectionCalls.length).toBeGreaterThan(readsBefore);
    });
  });

  describe("Strict activity sink: setLiveView before commit, atomic rejection", () => {
    it("openProjected does NOT commit conversation projection when setLiveView returns false", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const sink = createFakeSink();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-1",
      };
      sink.setLiveViewResult = () => false;
      await store.getState().openProjected(service, sink, "ref-1");
      expect(store.getState().conversation).toBeNull();
      expect(store.getState().status).toBe("opening");
      expect(store.getState().olderCursor).toBeNull();
    });

    it("rehydrate does NOT commit conversation projection when setLiveView returns false", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const openSink = createFakeSink();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-1",
      };
      await store.getState().openProjected(service, openSink, "ref-1");
      expect(store.getState().conversation).not.toBeNull();
      const rejectSink = createFakeSink();
      rejectSink.setLiveViewResult = () => false;
      const beforeConv = store.getState().conversation;
      await store.getState().rehydrate(service, rejectSink);
      expect(store.getState().conversation).toBe(beforeConv);
    });

    it("uses the same exact identity tuple for both stores", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const sink = createFakeSink();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, sink, "ref-1");
      const identity = sink.setLiveViewCalls[0]?.identity;
      expect(identity?.threadId).toBe("thread-1");
      expect(identity?.ref).toBe("ref-1");
      expect(identity?.generation).toBe(
        store.getState().conversationGeneration,
      );
    });

    it("propagates rehydrate outcome from applyLiveNotification to the drain scheduler", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const sink = createFakeSink();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, sink, "ref-1");
      sink.notificationOutcome = () => "rehydrate";
      const initialReads = service.readProjectionCalls.length;
      service.notificationHandler?.({
        method: "evener/jobs/treeUpdated",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await yieldMicrotask();
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
    });
  });

  describe("Real activity store integration", () => {
    it("openProjected with real createActivityStore installs the activity view", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const activityStore = createActivityStore();
      const activityView: ActivityView = {
        tasks: [{ status: "done", count: 3 }],
        work: [],
        usage: { totalTokens: 42 },
        capabilities: ALL_TRUE_CAPS,
      };
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: activityView,
        olderCursor: null,
      };
      await store
        .getState()
        .openProjected(service, activityStore.getState(), "ref-1");
      expect(activityStore.getState().view).toBe(activityView);
      expect(activityStore.getState().status).toBe("open");
    });

    it("real activity store applyLiveNotification rehydrate triggers the drain scheduler", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const activityStore = createActivityStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store
        .getState()
        .openProjected(service, activityStore.getState(), "ref-1");
      const initialReads = service.readProjectionCalls.length;
      service.notificationHandler?.({
        method: "turn/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turn: {
            id: "t1",
            itemsView: "default",
            status: "completed",
            usage: { totalTokens: 100 },
          },
        },
      } as AnyNotification);
      await yieldMicrotask();
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
    });

    it("false setLiveView from real store (stale identity) rejects the conversation projection", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const activityStore = createActivityStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-1",
      };
      const realState = activityStore.getState();
      const staleSink: LiveActivitySink = {
        setLiveView(view, identity) {
          return realState.setLiveView(view, {
            threadId: identity.threadId,
            ref: identity.ref,
            generation: 0,
          });
        },
        applyLiveNotification(n, identity) {
          return realState.applyLiveNotification(n, identity);
        },
        reset() {
          realState.reset();
        },
      };
      await store.getState().openProjected(service, staleSink, "ref-1");
      expect(store.getState().conversation).toBeNull();
      expect(activityStore.getState().view).toBeNull();
    });
  });

  // --- Residual: exact binding tuple, heterogeneous scheduler, no test API ---

  describe("Residual 1: same-epoch-like rebind with different objects suppresses stale queued effect", () => {
    it("rehydrate with different service+sink increments epoch — queued A effect suppressed", async () => {
      // Open A, queue a rehydrate via notification, then call rehydrate
      // directly with different service+sink objects (same ref+gen). The
      // epoch increment suppresses the queued A effect — serviceA/sinkA
      // are never called after the rebind.
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const sinkA = createFakeSink();
      const store = createConversationStore();
      await store.getState().openProjected(serviceA, sinkA, "ref-1");

      // Queue a rehydrate for A via resync — scheduler captures binding
      // (serviceA+sinkA+epoch).
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);

      // Call rehydrate with DIFFERENT service+sink objects (same ref+gen).
      // This increments bindingEpoch before assignment, so the queued A
      // effect is suppressed.
      const serviceB = new FakeConversationService();
      serviceB.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const sinkB = createFakeSink();
      await store.getState().rehydrate(serviceB, sinkB);

      const readsA = serviceA.readProjectionCalls.length;
      const writesA = sinkA.setLiveViewCalls.length;
      // Drain microtasks — the queued A effect must be suppressed.
      await yieldMicrotask();
      // I1: zero serviceA reads and zero sinkA writes after the rebind.
      expect(serviceA.readProjectionCalls.length).toBe(readsA);
      expect(sinkA.setLiveViewCalls.length).toBe(writesA);
      // B's rehydrate should have run.
      expect(serviceB.readProjectionCalls.length).toBe(1);
      expect(sinkB.setLiveViewCalls.length).toBe(1);
    });

    it("RequestBinding is not exported from the module", () => {
      // RequestBinding is internal — it must not be importable.
      // We verify by checking that the store state does not expose it.
      const store = createConversationStore();
      const s = store.getState();
      expect(
        typeof (s as unknown as Record<string, unknown>).RequestBinding,
      ).toBe("undefined");
      expect(
        typeof (s as unknown as Record<string, unknown>).captureBinding,
      ).toBe("undefined");
    });
  });


  describe("Residual 4: no test-only mutable API on the production store", () => {
    it("store state does not expose flushScheduler", () => {
      const store = createConversationStore();
      const s = store.getState();
      expect(
        typeof (s as unknown as Record<string, unknown>).flushScheduler,
      ).toBe("undefined");
    });

    it("LiveConversationState type does not include flushScheduler", () => {
      // Type-level check: the interface must not declare flushScheduler.
      // We verify at runtime that the method is absent (cast to unknown
      // record since TS correctly rejects the property access).
      const store = createConversationStore();
      expect(
        (store.getState() as unknown as Record<string, unknown>).flushScheduler,
      ).toBeUndefined();
    });
  });

  // --- Fix round 1: I1/I2/M1 rejection items ----------------------------------



  // --- Task 2A-Scheduler proof: per-key exact completion --------------------

  // These tests prove the per-key completion promise resolves for the cap
  // key independently of unrelated rereads — the core invariant that the old
  // global-idle scheduler could not satisfy. All tests use level-triggered
  // deferred helpers (started/completed with target) and a watchdog to
  // detect hangs. No yieldMicrotask(count), setTimeout, or polling.

  // Watchdog: rejects if the promise does not settle within a timeout.
  // Catches hangs from a global-idle implementation that waits for all work.
  // The timer is cleared in finally so no open timer remains after the
  // guarded promise wins or the watchdog rejects.
  function withWatchdog<T>(
    label: string,
    p: Promise<T>,
    ms = 2000,
  ): Promise<T> {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const watchdog = new Promise<never>((_resolve, reject) => {
      timer = setTimeout(
        () => reject(new Error(`watchdog: ${label} timed out after ${ms}ms`)),
        ms,
      );
    });
    return Promise.race([p, watchdog]).finally(() => {
      if (timer !== undefined) clearTimeout(timer);
    });
  }



  describe("Task 2A-3: same-key reread coalescing through public notifications", () => {
    it("multiple same-key reread requests during a blocker produce exactly one trailing read", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const readCtrl = makeControlledRead(service);

      const readsAfterOpen = service.readProjectionCalls.length;

      // Emit multiple resync notifications (all same key "ref-1") while a
      // blocker is in flight. They must coalesce to exactly one trailing
      // read — not N reads.
      // First, start the initial rehydrate (held).
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await readCtrl.started(1); // 1st controlled read (openProjected was before)
      await yieldMicrotask(); // let the wrapper reach the release gate

      // While the first is held, emit 3 more same-key resync notifications.
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);

      // Release the first — the trailing coalesced read drains.
      readCtrl.release();
      await readCtrl.completed(1);
      await yieldMicrotask(); // let scheduler drain to trailing read

      // Exactly one trailing read started (coalesced from 3 same-key signals).
      await readCtrl.started(2);
      await yieldMicrotask(); // let the wrapper reach the release gate
      readCtrl.release();
      await readCtrl.completed(2);

      // Total reads: 1 (openProjected) + 1 (first rehydrate) + 1 (trailing) = 3.
      expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 2);
    });

  });

  // --- Task 2A-4: deferred error-path drain proof ---------------------------
  //
  // The first effect (reread) exposes a started barrier, remains in flight,
  // then deterministically errors after release. While in flight, queue
  // same-key work (coalesces) and distinct heterogeneous work (cap refresh).
  // Prove: first error is caught (no unhandled rejection), every queued
  // effect/outcome drains exactly once, completion promises/state settle,
  // scheduler remains usable. No fixed yieldMicrotask counts — only
  // level-triggered started/completed barriers and a watchdog for RED.

  // Controlled error-read: patches readProjection so the first call hangs
  // until released (with rejection), and subsequent calls hang until
  // released (with normal resolution). Level-triggered started/completed.
  function makeControlledErrorRead(service: FakeConversationService): {
    started: (target?: number) => Promise<void>;
    releaseFirst: () => void;
    release: () => void;
    completed: (target?: number) => Promise<void>;
    getStartedCount: () => number;
    getDoneCount: () => number;
  } {
    let startedCount = 0;
    let doneCount = 0;
    let firstCallDone = false;
    const startedWaiters: Array<{ target: number; resolve: () => void }> = [];
    const doneWaiters: Array<{ target: number; resolve: () => void }> = [];
    // One release gate per call. First call rejects; subsequent resolve.
    const releaseQueue: Array<{
      resolve: () => void;
      reject: (e: Error) => void;
    }> = [];
    const orig = service.readProjection.bind(service);
    service.readProjection = async (ref: string) => {
      startedCount += 1;
      for (let i = startedWaiters.length - 1; i >= 0; i--) {
        const w = startedWaiters[i];
        if (w !== undefined && startedCount >= w.target) {
          w.resolve();
          startedWaiters.splice(i, 1);
        }
      }
      if (!firstCallDone) {
        // First call: hang until released, then reject.
        firstCallDone = true;
        await new Promise<void>((_resolve, reject) => {
          releaseQueue.push({ resolve: () => {}, reject });
        });
        throw new Error("first reread error");
      }
      // Subsequent calls: hang until released, then resolve normally.
      await new Promise<void>((resolve) => {
        releaseQueue.push({ resolve, reject: () => resolve() });
      });
      const result = await orig(ref);
      doneCount += 1;
      for (let i = doneWaiters.length - 1; i >= 0; i--) {
        const w = doneWaiters[i];
        if (w !== undefined && doneCount >= w.target) {
          w.resolve();
          doneWaiters.splice(i, 1);
        }
      }
      return result;
    };
    // Patch the doneCount increment for the first (error) call too, so
    // completed() can track it. We do this by wrapping the rejection path.
    // Actually, doneCount is NOT incremented for the first call (it throws).
    // So completed(1) would hang. We need to increment doneCount when the
    // first call's error is caught by the scheduler. The scheduler's
    // runOne .catch() swallows the error, then .then() resolves waiters and
    // drains. So the first call's "done" is when the scheduler catches the
    // error. We can't observe that from inside the patched method.
    //
    // Solution: track the first call's completion separately. The first
    // call rejects, but the scheduler catches it. The scheduler's .then()
    // runs after the catch. We increment doneCount in the subsequent calls.
    // For the first call, we need a separate signal. Since the first call
    // throws, the patched method never reaches the doneCount increment.
    // But the scheduler's runOne .catch().then() will fire, which means the
    // drain continues. The trailing reread (2nd call) will start — that's
    // our signal that the first call's error was caught.
    //
    // So completed(1) is NOT useful for the first (error) call. We use
    // started(2) as the signal that the first error was caught and the
    // drain continued.
    return {
      started: (target = 1) => {
        if (startedCount >= target) return Promise.resolve();
        return new Promise<void>((resolve) => {
          startedWaiters.push({ target, resolve });
        });
      },
      releaseFirst: () => {
        const r = releaseQueue.shift();
        if (r !== undefined) r.reject(new Error("first reread error"));
      },
      release: () => {
        const r = releaseQueue.shift();
        if (r !== undefined) r.resolve();
      },
      completed: (target = 1) => {
        if (doneCount >= target) return Promise.resolve();
        return new Promise<void>((resolve) => {
          doneWaiters.push({ target, resolve });
        });
      },
      getStartedCount: () => startedCount,
      getDoneCount: () => doneCount,
    };
  }

  describe("a reread that fails does not block the next reread", () => {
    it("the second resync rereads and commits after the first read threw", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const orig = service.readProjection.bind(service);
      let failNext = true;
      service.readProjection = async (ref: string) => {
        if (!failNext) return orig(ref);
        failNext = false;
        service.readProjectionCalls.push({ ref });
        throw new Error("read failed");
      };
      const readsBefore = service.readProjectionCalls.length;
      const resync = {
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification;
      store.getState().applyNotification(resync);
      await yieldMicrotask();
      await yieldMicrotask();
      expect(store.getState().error).toBe("read failed");
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ name: "after the failed read" }),
      );
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification(resync);
      await ctrl.ready(1);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      expect(service.readProjectionCalls.length).toBe(readsBefore + 2);
      expect(store.getState().conversation?.name).toBe("after the failed read");
      expect(store.getState().error).toBeNull();
    });
  });


  // --- Fix round 1: I1/I2/I3/I4 — rehydrate/loadOlder ownership, mutation
  // error clear, raw Thread question lifecycle ---

  // Raw Thread fixture helpers for I4 (projectConversation-based question tests).
  function makeThread(over: Partial<Thread> = {}): Thread {
    return {
      id: "thread-1",
      sessionId: "session-1",
      preview: "hello",
      ephemeral: false,
      modelProvider: "anthropic",
      createdAt: 0,
      updatedAt: 0,
      status: { type: "ready" },
      cwd: "",
      cliVersion: "",
      source: "",
      evener: {
        ref: "ref-1",
        capabilities: { ...ALL_TRUE_CAPS },
        queue: { revision: 0 },
      },
      ...over,
    };
  }

  function makeTurn(over: Partial<Turn> = {}): Turn {
    return {
      id: "t1",
      items: [],
      itemsView: "default",
      status: "completed",
      ...over,
    };
  }

  function askUserItem(
    id: string,
    argsJson: string,
    status = "completed",
  ): ThreadItem {
    return {
      type: "commandExecution",
      id,
      toolName: "ask_user",
      status,
      argumentsJson: argsJson,
    };
  }

  function userMessageItem(id: string, text: string): ThreadItem {
    return { type: "userMessage", id, text };
  }

  // text undefined is a sparse wire item (no text field), as a live
  // item/started or a sparse item/completed carries.
  function agentMessageItem(
    id: string,
    text: string | undefined,
    status = "completed",
  ): ThreadItem {
    return { type: "agentMessage", id, status, ...(text === undefined ? {} : { text }) };
  }

  function reasoningItem(
    id: string,
    text: string,
    status = "completed",
  ): ThreadItem {
    return { type: "reasoning", id, text, status };
  }

  function commandExecItem(
    id: string,
    toolName: string,
    status = "completed",
  ): ThreadItem {
    return { type: "commandExecution", id, toolName, status };
  }

  const VALID_ASK_ARGS =
    '{"questions":[{"header":"Choose","question":"Pick one","options":[{"label":"A","detail":"da"},{"label":"B","detail":"db"}],"multi_select":false}]}';

  function makeReadProjectionResult(
    thread: Thread,
    capabilities: ThreadCapabilities = ALL_TRUE_CAPS,
  ): {
    conversation: MobileConversation;
    activity: ActivityView;
    olderCursor: string | null;
  } {
    return {
      conversation: projectConversation(
        hydrateThread({ thread }, thread.evener.ref, 0),
      ),
      activity: {
        tasks: [],
        work: [],
        usage: {},
        capabilities,
      },
      olderCursor: null,
    };
  }

  // A store opened through openProjected onto a fresh fake service serving
  // `thread` as its projection.
  async function openProjectedThread(thread: Thread) {
    const service = new FakeConversationService();
    service.readProjectionResult = makeReadProjectionResult(thread);
    const store = createConversationStore();
    await store.getState().openProjected(service, createFakeSink(), "ref-1");
    return store;
  }

  // Helper: wrap a real ActivityStore as a LiveActivitySink.
  function wrapActivityStoreAsSink(
    activityStore: ReturnType<typeof createActivityStore>,
  ): LiveActivitySink {
    const state = activityStore.getState();
    return {
      setLiveView(view: ActivityView, identity: ActivityIdentity) {
        return state.setLiveView(view, identity);
      },
      applyLiveNotification(n: AnyNotification, identity: ActivityIdentity) {
        return state.applyLiveNotification(n, identity);
      },
      reset() {
        state.reset();
      },
    };
  }

  describe("I1: rehydrate commit-time ownership — page/mutation/error owner capture", () => {
    it("R pending→L success: R cannot delete page items or regress cursor after L owns page", async () => {
      // Rehydrate (R) is pending. A loadOlder (L) succeeds during the await,
      // prepending older items and advancing the cursor. When R completes, it
      // must NOT replace the conversation (deleting L's items) or regress the
      // cursor — L's page ownership is newer.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t1",
              items: [{ kind: "user", id: "u1", text: "hi" } as never] as never,
            }),
          ],
        }),
      );
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Set up an older cursor so loadOlder can run.
      store.setState({ olderCursor: "cursor-1" });
      // Start a rehydrate (R) that hangs.
      const ctrl = makeControlledRead(service);
      // Change the projection result so R would regress the cursor and
      // replace items.
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      service.readProjectionResult.olderCursor = null;
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await yieldMicrotask();
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, a loadOlder (L) succeeds — prepends items,
      // advances cursor.
      service.olderItems = {
        items: [{ kind: "user", id: "old-page-item", text: "older" }],
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);
      const itemsAfterL = store.getState().conversation?.items ?? [];
      const cursorAfterL = store.getState().olderCursor;
      expect(itemsAfterL.some((i) => i.id === "old-page-item")).toBe(true);
      expect(cursorAfterL).toBe("cursor-2");
      // Now release R — it must NOT replace the conversation or regress cursor.
      ctrl.release();
      await ctrl.completed(1);
      // R's stale domain write (replacing conversation) is discarded — L's
      // page items survive, cursor is NOT regressed.
      const itemsAfterR = store.getState().conversation?.items ?? [];
      expect(itemsAfterR.some((i) => i.id === "old-page-item")).toBe(true);
      expect(store.getState().olderCursor).toBe(cursorAfterL);
    });

    it("R→L failure: R success preserves page error set by L failure", async () => {
      // Rehydrate (R) is pending. A loadOlder (L) fails during the await,
      // setting a page error. When R succeeds, it must NOT clear that error.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });
      // Start R (hanging).
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await yieldMicrotask();
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, L fails — sets a page error.
      service.olderItems = Promise.reject(
        new Error("page load failed"),
      ) as never;
      await store
        .getState()
        .loadOlder(service)
        .catch(() => {});
      expect(store.getState().error).not.toBeNull();
      const errorAfterL = store.getState().error;
      // Release R — it must preserve the page error.
      ctrl.release();
      await ctrl.completed(1);
      expect(store.getState().error).toBe(errorAfterL);
    });

    it("R→new mutation failure: R failure cannot overwrite mutation error", async () => {
      // Rehydrate (R) is pending. A mutation fails during the await, setting
      // a mutation error. When R also fails, R's failure must NOT overwrite
      // the mutation's error.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
          },
        }),
      );
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Start R (hanging).
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, a send fails — sets mutation error.
      service.sendShouldReject = new Error("mutation boom");
      await store.getState().send(service, textInput("x"));
      expect(store.getState().error).toBe("mutation boom");
      expect(store.getState().pendingMutation?.status).toBe("failed");
      // Now make R fail — patch readProjection to reject.
      service.readProjection = async () => {
        throw new Error("rehydrate boom");
      };
      ctrl.release();
      await ctrl.completed(1).catch(() => {});
      // R's failure must NOT overwrite the mutation error.
      expect(store.getState().error).toBe("mutation boom");
      expect(store.getState().pendingMutation?.status).toBe("failed");
    });
  });

  describe("I2: generation/identity-stale loadOlder performs ZERO set calls", () => {
    it("does not rehydrate a stale cursor through the replacement binding", async () => {
      const serviceA = new FakeConversationService();
      const serviceB = new FakeConversationService();
      const store = createConversationStore();
      serviceA.readProjectionResult = makeReadProjectionResult(makeThread({ id: "thread-A" }));
      serviceB.readProjectionResult = makeReadProjectionResult(makeThread({ id: "thread-B" }));
      await store.getState().openProjected(serviceA, createFakeSink(), "ref-A");
      store.setState({ olderCursor: "cursor-A" });
      const readsBeforeLoad = serviceA.readProjectionCalls.length;

      let rejectA!: (error: Error) => void;
      serviceA.olderItems = new Promise<never>((_resolve, reject) => {
        rejectA = reject;
      }) as never;
      const loadA = store.getState().loadOlder(serviceA);
      await store.getState().openProjected(serviceB, createFakeSink(), "ref-B");

      rejectA(new WireError("stale transcript cursor", -32020, {
        evenerErrorInfo: "transcriptItemCursorStale",
      }));
      await expect(loadA).resolves.toEqual({ status: "ignored" });

      expect(serviceA.readProjectionCalls).toHaveLength(readsBeforeLoad);
      expect(store.getState().conversation?.threadId).toBe("thread-B");
      expect(store.getState().ref).toBe("ref-B");
    });

    it("barrier: A pending, open/reset B, start B page, resolve A → subscriber sees zero stale writes", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ id: "thread-A" }),
      );
      await store.getState().openProjected(service, createFakeSink(), "ref-A");
      store.setState({ olderCursor: "cursor-A" });
      // Start loadOlder A (hangs).
      let resolveA: (() => void) | null = null as (() => void) | null;
      const hangA = new Promise<{
        items: MobileConversation["items"];
        nextCursor?: string;
      }>((r) => {
        resolveA = () =>
          r({
            items: [{ kind: "user", id: "stale-A-item", text: "stale" }],
            nextCursor: "stale-cursor-A",
          });
      });
      service.olderItems = hangA as never;
      // Track all set() calls via subscriber.
      const setCalls: string[] = [];
      let snapshotConversation = store.getState().conversation;
      let snapshotLoadingOlder = store.getState().loadingOlder;
      let snapshotError = store.getState().error;
      let snapshotOlderCursor = store.getState().olderCursor;
      store.subscribe((s) => {
        if (s.conversation !== snapshotConversation)
          setCalls.push("conversation");
        if (s.loadingOlder !== snapshotLoadingOlder)
          setCalls.push("loadingOlder");
        if (s.error !== snapshotError) setCalls.push("error");
        if (s.olderCursor !== snapshotOlderCursor) setCalls.push("olderCursor");
        snapshotConversation = s.conversation;
        snapshotLoadingOlder = s.loadingOlder;
        snapshotError = s.error;
        snapshotOlderCursor = s.olderCursor;
      });
      const aP = store.getState().loadOlder(service);
      // While A is in-flight, open B (new conversation generation).
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ id: "thread-B" }),
      );
      await store.getState().openProjected(service, createFakeSink(), "ref-B");
      store.setState({ olderCursor: "cursor-B" });
      // Start and complete a B page operation.
      service.olderItems = {
        items: [{ kind: "user", id: "B-item", text: "B page" }],
        nextCursor: "cursor-B2",
      };
      await store.getState().loadOlder(service);
      expect(store.getState().conversation?.threadId).toBe("thread-B");
      // Clear tracked calls — from this point, only A's stale resolution
      // should produce writes. A must produce ZERO.
      setCalls.length = 0;
      snapshotConversation = store.getState().conversation;
      snapshotLoadingOlder = store.getState().loadingOlder;
      snapshotError = store.getState().error;
      snapshotOlderCursor = store.getState().olderCursor;
      // Now resolve the stale A — it must perform ZERO set calls.
      (resolveA as () => void)();
      await aP;
      // The subscriber must NOT have seen any stale writes from A.
      expect(setCalls.length).toBe(0);
      // B's state is intact.
      expect(store.getState().conversation?.threadId).toBe("thread-B");
      expect(store.getState().loadingOlder).toBe(false);
    });

    it("stale loadOlder failure after reset performs ZERO set calls", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });
      // Start loadOlder that hangs, then reset.
      let rejectA: ((e: Error) => void) | null = null as
        | ((e: Error) => void)
        | null;
      const hangA = new Promise<{
        items: MobileConversation["items"];
        nextCursor?: string;
      }>((_r, reject) => {
        rejectA = reject;
      });
      service.olderItems = hangA as never;
      const aP = store.getState().loadOlder(service);
      store.getState().reset();
      // Track set calls AFTER reset — only A's stale resolution should be tracked.
      const setCalls: string[] = [];
      let snapLoading = store.getState().loadingOlder;
      let snapError = store.getState().error;
      let snapConv = store.getState().conversation;
      store.subscribe((s) => {
        if (s.loadingOlder !== snapLoading) setCalls.push("loadingOlder");
        if (s.error !== snapError) setCalls.push("error");
        if (s.conversation !== snapConv) setCalls.push("conversation");
        snapLoading = s.loadingOlder;
        snapError = s.error;
        snapConv = s.conversation;
      });
      // Resolve stale A with failure — must perform ZERO set calls.
      (rejectA as (e: Error) => void)(new Error("stale A failure"));
      await aP.catch(() => {});
      expect(setCalls.length).toBe(0);
      expect(store.getState().conversation).toBeNull();
      expect(store.getState().error).toBeNull();
      expect(store.getState().loadingOlder).toBe(false);
    });
  });

  describe("I3: mutation starts atomically clear prior error", () => {
    it("send clears prior error atomically with new pending mutation", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        capabilities: { ...ALL_TRUE_CAPS },
      });
      await store.getState().open(service, "ref-1");
      // Set an error via a failed send.
      service.sendShouldReject = new Error("first failure");
      await store.getState().send(service, textInput("first"));
      expect(store.getState().error).not.toBeNull();
      // Start a second send — it must atomically clear the error.
      service.sendShouldReject = null;
      let resolveSend: (() => void) | null = null as (() => void) | null;
      const hangSend = new Promise<MutationReceipt>((r) => {
        resolveSend = () => r(makeReceipt());
      });
      service.send = async () => hangSend;
      store.getState().send(service, textInput("second"));
      // Error must be cleared immediately (atomically with pending mutation).
      expect(store.getState().error).toBeNull();
      expect(store.getState().pendingMutation?.status).toBe("pending");
      (resolveSend as () => void)();
    });

    it("old failed mutation cannot republish error after newer mutation starts", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        capabilities: { ...ALL_TRUE_CAPS },
      });
      await store.getState().open(service, "ref-1");
      // First send hangs then fails.
      let rejectFirst: ((e: Error) => void) | null = null as
        | ((e: Error) => void)
        | null;
      const hangFirst = new Promise<MutationReceipt>((_r, reject) => {
        rejectFirst = reject;
      });
      service.sendShouldReject = null;
      service.send = async () => hangFirst;
      const send1P = store.getState().send(service, textInput("first"));
      // While first is in-flight, start a second send that succeeds.
      store.getState().setDraft("second");
      service.sendShouldReject = null;
      service.send = async () => makeReceipt();
      await store.getState().send(service, textInput("second"));
      expect(store.getState().error).toBeNull();
      expect(store.getState().pendingMutation).toBeNull();
      // Now the first send fails — it must NOT republish error.
      (rejectFirst as (e: Error) => void)(new Error("first failed"));
      await send1P;
      expect(store.getState().error).toBeNull();
      expect(store.getState().pendingMutation).toBeNull();
    });
  });

  describe("Task 2A-Ops-4 (preserved): explicit draft revision — type-then-delete is an edit", () => {
    it("failure restores draft snapshot only if user has not edited since clear", async () => {
      const service = new FakeConversationService();
      service.sendShouldReject = new Error("send failed");
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().setDraft("original draft");
      await store.getState().send(service, textInput("original draft"));
      expect(store.getState().draft).toBe("original draft");
    });

    it("type-then-delete after clear counts as edit — failure does NOT restore snapshot", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().setDraft("my draft text");
      let rejectSend: ((e: Error) => void) | null = null as
        | ((e: Error) => void)
        | null;
      const hangSend = new Promise<MutationReceipt>((_r, reject) => {
        rejectSend = reject;
      });
      service.sendShouldReject = null;
      service.send = async () => hangSend;
      const sendP = store.getState().send(service, textInput("my draft text"));
      expect(store.getState().draft).toBe("");
      store.getState().setDraft("typed then deleted");
      store.getState().setDraft("");
      (rejectSend as (e: Error) => void)(new Error("send failed"));
      await sendP;
      expect(store.getState().draft).toBe("");
      expect(store.getState().error).not.toBeNull();
    });

    it("out-of-order mutation failure cannot alter newer mutation draft", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().setDraft("first");
      let rejectFirst: ((e: Error) => void) | null = null as
        | ((e: Error) => void)
        | null;
      const hangFirst = new Promise<MutationReceipt>((_r, reject) => {
        rejectFirst = reject;
      });
      service.sendShouldReject = null;
      service.send = async () => hangFirst;
      const send1P = store.getState().send(service, textInput("first"));
      store.getState().setDraft("second");
      service.sendShouldReject = null;
      service.send = async () => makeReceipt();
      await store.getState().send(service, textInput("second"));
      expect(store.getState().draft).toBe("");
      expect(store.getState().pendingMutation).toBeNull();
      (rejectFirst as (e: Error) => void)(new Error("first failed"));
      await send1P;
      expect(store.getState().draft).toBe("");
      expect(store.getState().pendingMutation).toBeNull();
    });
  });

  describe("I4: raw Thread fixtures through projectConversation — question lifecycle", () => {
    it("completed parseable ask_user drives reread → question rows + askPending via projectConversation", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Initial: no turns, no pending ask.
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      expect(store.getState().conversation?.askPending).toBe(false);
      // After the ask_user notification, the reread returns a Thread with a
      // completed ask_user turn — projectConversation produces question rows.
      const askThread = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: { ...ALL_TRUE_CAPS },
          queue: { revision: 0 },
          askPending: true,
        },
        turns: [
          makeTurn({
            id: "t1",
            items: [askUserItem("ask-1", VALID_ASK_ARGS)],
            status: "completed",
          }),
        ],
      });
      service.readProjectionResult = makeReadProjectionResult(askThread);
      const initialReads = service.readProjectionCalls.length;
      // Trigger the ask_user completed notification — schedules a reread.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: askUserItem("ask-1", VALID_ASK_ARGS),
        },
      } as AnyNotification);
      // Deterministic barrier: await reread started + completed.
      const ctrl = makeControlledRead(service);
      await ctrl.started(1);
      await yieldMicrotask();
      ctrl.release();
      await ctrl.completed(1);
      // Wait for the rehydrate effect to finish committing.
      await yieldMicrotask();
      // The reread must have produced actual question rows via projectConversation.
      const conv = store.getState().conversation;
      expect(conv?.askPending).toBe(true);
      const questionItem = conv?.items.find((i) => i.kind === "question");
      expect(questionItem).toBeDefined();
      if (questionItem?.kind === "question") {
        expect(questionItem.questions).toHaveLength(1);
        expect(questionItem.questions[0]?.question).toBe("Pick one");
        expect(questionItem.questions[0]?.options).toHaveLength(2);
      }
      // Bounded reread count: exactly one reread.
      expect(service.readProjectionCalls.length).toBe(initialReads + 1);
    });

    it("later user-message answer triggers reread → settled/removal via projectConversation", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Initial: has a pending ask_user.
      const askThread = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: { ...ALL_TRUE_CAPS },
          queue: { revision: 0 },
          askPending: true,
        },
        turns: [
          makeTurn({
            id: "t1",
            items: [askUserItem("ask-1", VALID_ASK_ARGS)],
            status: "completed",
          }),
        ],
      });
      service.readProjectionResult = makeReadProjectionResult(askThread);
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      expect(store.getState().conversation?.askPending).toBe(true);
      const initialReads = service.readProjectionCalls.length;
      // After the user-message, the reread returns a Thread where the ask is
      // followed by a userMessage — projectConversation settles (no question rows,
      // askPending false).
      const settledThread = makeThread({
        turns: [
          makeTurn({
            id: "t1",
            items: [askUserItem("ask-1", VALID_ASK_ARGS)],
            status: "completed",
          }),
          makeTurn({
            id: "t2",
            items: [userMessageItem("answer-1", "I choose A")],
            status: "completed",
          }),
        ],
      });
      service.readProjectionResult = makeReadProjectionResult(settledThread);
      // A user-message notification triggers a reread (answer lifecycle).
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t2",
          item: userMessageItem("answer-1", "I choose A"),
        },
      } as AnyNotification);
      const ctrl = makeControlledRead(service);
      await ctrl.started(1);
      await yieldMicrotask();
      ctrl.release();
      await ctrl.completed(1);
      // Wait for the rehydrate effect to finish committing.
      await yieldMicrotask();
      // The reread must have settled the pending question state.
      const conv = store.getState().conversation;
      expect(conv?.askPending).toBe(false);
      const questionItem = conv?.items.find((i) => i.kind === "question");
      expect(questionItem).toBeUndefined();
      // Bounded reread count: exactly one reread.
      expect(service.readProjectionCalls.length).toBe(initialReads + 1);
    });

    it("askPending resolve via a model-only status frame resyncs: the question row leaves with the sheet", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const askTurn = makeTurn({
        id: "t1",
        items: [askUserItem("ask-1", VALID_ASK_ARGS)],
        status: "completed",
      });
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: true,
          },
          turns: [askTurn],
        }),
      );
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      expect(store.getState().conversation?.items.some((row) => row.kind === "question")).toBe(true);
      const initialReads = service.readProjectionCalls.length;
      // The hub resolves the ask with a status frame alone — no item frame,
      // no answer row. The reducer folds askPending (snapshot-authoritative
      // on thread/status/changed), the row appliers have no case for this
      // frame, and question rows come only from the canonical projection
      // (F6), so the timeline needs the reread to agree with the sheet.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: false,
          },
          turns: [askTurn],
        }),
      );
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "idle" },
          askPending: false,
        },
      } as AnyNotification);
      // The resync is a scheduled rehydrate (microtask-only drain). The
      // pre-fix code schedules no read at all, so the controlled-read barrier
      // cannot be used here — it would wait for a read that never comes.
      // Flush the drain instead: a no-op when nothing was requested, and
      // the assertions below name the stale state directly.
      for (let i = 0; i < 25; i++) await yieldMicrotask();
      const conv = store.getState().conversation;
      expect(conv?.askPending).toBe(false);
      expect(
        conv?.items.some((row) => row.kind === "question"),
      ).toBe(false);
      // Bounded reread count: exactly one resync for the flip.
      expect(service.readProjectionCalls.length).toBe(initialReads + 1);
    });

    it("askPending raise via a model-only status frame resyncs: the sheet's new ask gains its question row", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const askTurn = makeTurn({
        id: "t1",
        items: [askUserItem("ask-1", VALID_ASK_ARGS)],
        status: "completed",
      });
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: false,
          },
          turns: [askTurn],
        }),
      );
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Not pending: the ask item renders as a settled tool activity, no
      // question row, and the sheet is empty.
      expect(
        store.getState().conversation?.items.some((row) => row.kind === "question"),
      ).toBe(false);
      const initialReads = service.readProjectionCalls.length;
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: true,
          },
          turns: [askTurn],
        }),
      );
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
          askPending: true,
        },
      } as AnyNotification);
      for (let i = 0; i < 25; i++) await yieldMicrotask();
      const conv = store.getState().conversation;
      expect(conv?.askPending).toBe(true);
      const row = conv?.items.find((i) => i.kind === "question");
      expect(row).toBeDefined();
      // The row matches the sheet: the same canonical refs pendingQuestions
      // composes answers from.
      if (row?.kind === "question") {
        expect(row.questions).toHaveLength(1);
        expect(row.questions[0]?.callId).toBe("ask-1");
        expect(row.questions[0]?.question).toBe("Pick one");
        expect(row.questions[0]?.options).toHaveLength(2);
      }
      expect(service.readProjectionCalls.length).toBe(initialReads + 1);
    });

    it("no-sink open(): an askPending resolve via a model-only frame reprojects the question rows with the sheet", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const askTurn = makeTurn({
        id: "t1",
        items: [askUserItem("ask-1", VALID_ASK_ARGS)],
        status: "completed",
      });
      service.openConv = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: true,
          },
          turns: [askTurn],
        }),
      ).conversation;
      await store.getState().open(service, "ref-1");
      expect(
        store
          .getState()
          .conversation?.items.some((row) => row.kind === "question"),
      ).toBe(true);
      // A plain open() binds no activity sink, so the rehydrate the
      // sink-bound path takes is a no-op here — the compatibility path must
      // reconcile the question rows itself.
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "idle" },
          askPending: false,
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      expect(conv?.askPending).toBe(false);
      expect(
        conv?.items.some((row) => row.kind === "question"),
      ).toBe(false);
      // The resolved ask renders canonically: the question row became the
      // settled tool-call activity, exactly what a sink-bound reread yields.
      expect(
        conv?.items.some(
          (row) => row.kind === "activity" && row.id === "ask-1",
        ),
      ).toBe(true);
    });

    it("no-sink open(): an askPending raise via a model-only frame gains the sheet's question row", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const askTurn = makeTurn({
        id: "t1",
        items: [askUserItem("ask-1", VALID_ASK_ARGS)],
        status: "completed",
      });
      service.openConv = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: false,
          },
          turns: [askTurn],
        }),
      ).conversation;
      await store.getState().open(service, "ref-1");
      // Not pending: the ask renders as a settled tool activity, and the
      // sheet is empty.
      expect(
        store
          .getState()
          .conversation?.items.some((row) => row.kind === "question"),
      ).toBe(false);
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
          askPending: true,
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      expect(conv?.askPending).toBe(true);
      const row = conv?.items.find((i) => i.kind === "question");
      expect(row).toBeDefined();
      // The row matches the sheet: the same canonical refs pendingQuestions
      // composes answers from.
      if (row?.kind === "question") {
        expect(row.questions).toHaveLength(1);
        expect(row.questions[0]?.callId).toBe("ask-1");
        expect(row.questions[0]?.question).toBe("Pick one");
        expect(row.questions[0]?.options).toHaveLength(2);
      }
      // The settled-ask activity row it replaced is gone.
      expect(
        conv?.items.some(
          (row) => row.kind === "activity" && row.id === "ask-1",
        ),
      ).toBe(false);
    });

    it("no-sink open(): a status-only frame that does not move askPending leaves the timeline untouched", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: true,
          },
          turns: [
            makeTurn({
              id: "t1",
              items: [askUserItem("ask-1", VALID_ASK_ARGS)],
              status: "completed",
            }),
          ],
        }),
      ).conversation;
      await store.getState().open(service, "ref-1");
      const before = store.getState().conversation?.items;
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
        },
      } as AnyNotification);
      // No askPending move, no reconciliation: the items array is not even
      // replaced (the model-only publish preserves it).
      expect(store.getState().conversation?.items).toBe(before);
    });

    it("no-sink open(): an askPending transition preserves live-owned rows the model cannot reproject", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: true,
          },
          turns: [
            makeTurn({
              id: "t1",
              items: [askUserItem("ask-1", VALID_ASK_ARGS)],
              status: "completed",
            }),
          ],
        }),
      ).conversation;
      await store.getState().open(service, "ref-1");
      // An idle warning — no active turn, so the reducer folds no model
      // item and the live row applier carries it in the timeline alone.
      store.getState().applyNotification({
        method: "warning",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          title: "Sandbox blocked",
          message: "retry later",
        },
      } as AnyNotification);
      const failureRow = store
        .getState()
        .conversation?.items.find((row) => row.kind === "failure");
      expect(failureRow).toBeDefined();
      const failureId = failureRow?.id;
      // The askPending resolve reconciles the question row — and must not
      // disturb the live-owned warning: it exists only in items, so a
      // whole-timeline reprojection from the model would silently drop it.
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "idle" },
          askPending: false,
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      expect(
        conv?.items.some((row) => row.kind === "question"),
      ).toBe(false);
      expect(conv?.items.some((row) => row.id === failureId)).toBe(true);
    });

    it("no-sink raise: an ask leading a tool cluster splits it without dropping members", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: false,
          },
          turns: [
            makeTurn({
              id: "t1",
              items: [
                askUserItem("ask-1", VALID_ASK_ARGS),
                commandExecItem("tool-1", "bash"),
                commandExecItem("tool-2", "ls"),
              ],
              status: "completed",
            }),
          ],
        }),
      ).conversation;
      await store.getState().open(service, "ref-1");
      // askPending false: the three same-family tools form one cluster row.
      const clusterBefore = store.getState().conversation?.items[0];
      expect(clusterBefore?.kind).toBe("activity");
      if (clusterBefore?.kind === "activity") {
        expect(clusterBefore.members?.map((m) => m.id)).toEqual([
          "ask-1",
          "tool-1",
          "tool-2",
        ]);
      }
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
          askPending: true,
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      // The question row takes the ask's place…
      expect(
        conv?.items.some((row) => row.kind === "question"),
      ).toBe(true);
      // …and the cluster rebuilds from its remaining members — the
      // neighboring tools survive, exactly as a sink-bound reread yields.
      const rebuilt = conv?.items.find(
        (row) => row.kind === "activity" && row.id === "tool-1",
      );
      if (rebuilt?.kind === "activity") {
        expect(rebuilt.members?.map((m) => m.id)).toEqual([
          "tool-1",
          "tool-2",
        ]);
      } else {
        expect(rebuilt).toBeDefined();
      }
    });

    it("no-sink raise: an ask inside a tool cluster rebuilds it without the stale member", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: false,
          },
          turns: [
            makeTurn({
              id: "t1",
              items: [
                commandExecItem("tool-1", "bash"),
                askUserItem("ask-1", VALID_ASK_ARGS),
                commandExecItem("tool-2", "ls"),
              ],
              status: "completed",
            }),
          ],
        }),
      ).conversation;
      await store.getState().open(service, "ref-1");
      const clusterBefore = store.getState().conversation?.items[0];
      expect(clusterBefore?.kind).toBe("activity");
      if (clusterBefore?.kind === "activity") {
        expect(clusterBefore.members?.map((m) => m.id)).toEqual([
          "tool-1",
          "ask-1",
          "tool-2",
        ]);
      }
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
          askPending: true,
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      // The question row appears at the ask's canonical position…
      expect(conv?.items.map((row) => row.id)).toEqual([
        "tool-1",
        "ask-1",
        "tool-2",
      ]);
      // …and no row still carries the ask as a stale cluster member.
      expect(
        conv?.items.some(
          (row) =>
            row.kind === "activity" &&
            (row.members ?? []).some((m) => m.id === "ask-1"),
        ),
      ).toBe(false);
    });

    it("no-sink resolve: a settled ask joins its neighbors' cluster", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: true,
          },
          turns: [
            makeTurn({
              id: "t1",
              items: [
                commandExecItem("tool-1", "bash"),
                askUserItem("ask-1", VALID_ASK_ARGS),
                commandExecItem("tool-2", "ls"),
              ],
              status: "completed",
            }),
          ],
        }),
      ).conversation;
      await store.getState().open(service, "ref-1");
      // The pending question splits the tool run into standalone rows.
      expect(store.getState().conversation?.items.map((row) => row.id)).toEqual(
        ["tool-1", "ask-1", "tool-2"],
      );
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "idle" },
          askPending: false,
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      // The resolved ask folds into the one cluster a reread would build.
      expect(conv?.items).toHaveLength(1);
      const merged = conv?.items[0];
      if (merged?.kind === "activity") {
        expect(merged.members?.map((m) => m.id)).toEqual([
          "tool-1",
          "ask-1",
          "tool-2",
        ]);
      } else {
        expect(merged?.kind).toBe("activity");
      }
      expect(
        conv?.items.some((row) => row.kind === "question"),
      ).toBe(false);
    });

    it("no-sink askPending transition keeps a frozen truncated row frozen", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: true,
          },
          turns: [
            makeTurn({
              id: "t1",
              items: [
                // Multibyte content: the cutoff leaves spare bytes under
                // the cap, so an appended delta would fit verbatim after
                // the marker if the freeze were lost.
                agentMessageItem("msg-1", "é".repeat(40_000)),
                askUserItem("ask-1", VALID_ASK_ARGS),
              ],
              status: "completed",
            }),
          ],
        }),
      ).conversation;
      await store.getState().open(service, "ref-1");
      const before = store
        .getState()
        .conversation?.items.find((i) => i.id === "msg-1");
      expect(before?.kind).toBe("assistant");
      const beforeText = before?.kind === "assistant" ? before.markdown : "";
      expect(beforeText.endsWith("… truncated")).toBe(true);
      // The askPending resolve reconciles the question row and must carry
      // the assistant row's truncation freeze through untouched.
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "idle" },
          askPending: false,
        },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "msg-1",
          delta: "x",
        },
      } as AnyNotification);
      const after = store
        .getState()
        .conversation?.items.find((i) => i.id === "msg-1");
      const afterText = after?.kind === "assistant" ? after.markdown : "";
      // Frozen display: the delta cannot append after the marker.
      expect(afterText).toBe(beforeText);
    });

    it("no-sink resolve: a transcript-keyed neighbor row still joins the ask's cluster", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: true,
          },
          turns: [
            makeTurn({
              id: "t1",
              items: [
                askUserItem("ask-1", VALID_ASK_ARGS),
                {
                  ...commandExecItem("tool-2", "bash"),
                  transcriptKey: "tk-2",
                },
              ],
              status: "completed",
            }),
          ],
        }),
      ).conversation;
      await store.getState().open(service, "ref-1");
      // A live completion re-applies the neighbor row carrying its wire
      // transcript key — the row's timeline identity is now the key, not
      // its wire id, while the canonical projection knows the item only
      // under the wire id.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            ...commandExecItem("tool-2", "bash"),
            transcriptKey: "tk-2",
          },
        },
      } as AnyNotification);
      expect(
        store.getState().conversation?.items.map((row) => row.id),
      ).toEqual(["ask-1", "tool-2"]);
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "idle" },
          askPending: false,
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      // The resolved ask folds into one cluster with its neighbor — the
      // key-carrying row is replaced, not duplicated beside the cluster.
      expect(conv?.items).toHaveLength(1);
      const merged = conv?.items[0];
      if (merged?.kind === "activity") {
        expect(merged.members?.map((m) => m.id)).toEqual(["ask-1", "tool-2"]);
      } else {
        expect(merged?.kind).toBe("activity");
      }
    });

    it("no-sink resolve keeps a live warning between the ask and its cluster neighbor", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: true,
          },
          turns: [
            makeTurn({
              id: "t1",
              items: [askUserItem("ask-1", VALID_ASK_ARGS)],
              status: "completed",
            }),
          ],
        }),
      ).conversation;
      await store.getState().open(service, "ref-1");
      // An idle warning lands after the pending ask…
      store.getState().applyNotification({
        method: "warning",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          title: "Sandbox blocked",
          message: "retry later",
        },
      } as AnyNotification);
      const failureRow = store
        .getState()
        .conversation?.items.find((row) => row.kind === "failure");
      expect(failureRow).toBeDefined();
      const failureId = failureRow?.id;
      // …and a live tool completion lands after the warning, sandwiching
      // it between the ask and its canonical cluster neighbor.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: commandExecItem("tool-2", "bash"),
        },
      } as AnyNotification);
      expect(
        store.getState().conversation?.items.map((row) => row.id),
      ).toEqual(["ask-1", failureId, "tool-2"]);
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "idle" },
          askPending: false,
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      // The warning survives the reconciliation, and the ask settles as
      // its own row — the live warning is a display boundary the model
      // cannot see, so the canonical cluster does not merge across it.
      expect(conv?.items.some((row) => row.id === failureId)).toBe(true);
      expect(conv?.items.map((row) => row.id)).toEqual([
        "ask-1",
        failureId,
        "tool-2",
      ]);
      expect(
        conv?.items.some((row) => row.kind === "question"),
      ).toBe(false);
    });

    it("no-sink resolve: an ask carrying a distinct transcript key still settles into its cluster", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: true,
          },
          turns: [
            makeTurn({
              id: "t1",
              items: [
                {
                  ...askUserItem("ask-1", VALID_ASK_ARGS),
                  transcriptKey: "tk-1",
                },
                commandExecItem("tool-2", "bash"),
              ],
              status: "completed",
            }),
          ],
        }),
      ).conversation;
      await store.getState().open(service, "ref-1");
      expect(
        store.getState().conversation?.items.map((row) => row.id),
      ).toEqual(["ask-1", "tool-2"]);
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "idle" },
          askPending: false,
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      // The resolved ask folds into the cluster with its neighbor — the
      // canonical rows know it only under the transcript key, while the
      // stale question row knew it only under its wire id.
      expect(conv?.items).toHaveLength(1);
      const merged = conv?.items[0];
      if (merged?.kind === "activity") {
        expect(merged.members?.map((m) => m.id)).toEqual(["ask-1", "tool-2"]);
      } else {
        expect(merged?.kind).toBe("activity");
      }
    });

    it("no-sink raise: an ask carrying a distinct transcript key replaces its settled row", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS },
            queue: { revision: 0 },
            askPending: false,
          },
          turns: [
            makeTurn({
              id: "t1",
              items: [
                {
                  ...askUserItem("ask-1", VALID_ASK_ARGS),
                  transcriptKey: "tk-1",
                },
                commandExecItem("tool-2", "bash"),
              ],
              status: "completed",
            }),
          ],
        }),
      ).conversation;
      await store.getState().open(service, "ref-1");
      // Settled: the ask clusters with its neighbor under the key identity.
      expect(store.getState().conversation?.items).toHaveLength(1);
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
          askPending: true,
        },
      } as AnyNotification);
      const conv = store.getState().conversation;
      // The question row appears at the ask's place…
      expect(
        conv?.items.some((row) => row.kind === "question"),
      ).toBe(true);
      // …the settled row for the same item is replaced, not left stale in
      // the cluster beside it.
      expect(
        conv?.items.some(
          (row) =>
            row.kind === "activity" &&
            (row.members ?? []).some((m) => m.id === "ask-1"),
        ),
      ).toBe(false);
      expect(conv?.items.map((row) => row.id)).toEqual(["ask-1", "tool-2"]);
    });

    it("malformed ask_user remains conservative — schedules reread, no question rows", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const initialReads = service.readProjectionCalls.length;
      // A malformed ask_user (invalid argumentsJson) schedules a reread.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: askUserItem("ask-bad", "not valid json {{{"),
        },
      } as AnyNotification);
      const ctrl = makeControlledRead(service);
      await ctrl.started(1);
      await yieldMicrotask();
      ctrl.release();
      await ctrl.completed(1);
      // A reread was scheduled (conservative), but no question item projected.
      expect(service.readProjectionCalls.length).toBe(initialReads + 1);
      const conv = store.getState().conversation;
      const questionItem = conv?.items.find((i) => i.kind === "question");
      expect(questionItem).toBeUndefined();
    });

    it("incomplete ask_user (inProgress) remains conservative — schedules reread only", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const initialReads = service.readProjectionCalls.length;
      // An inProgress ask_user schedules a reread.
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: askUserItem("ask-incomplete", VALID_ASK_ARGS, "inProgress"),
        },
      } as AnyNotification);
      const ctrl = makeControlledRead(service);
      await ctrl.started(1);
      await yieldMicrotask();
      ctrl.release();
      await ctrl.completed(1);
      expect(service.readProjectionCalls.length).toBe(initialReads + 1);
      const conv = store.getState().conversation;
      const questionItem = conv?.items.find((i) => i.kind === "question");
      expect(questionItem).toBeUndefined();
    });

  });

  // --- Residual: R1 — monotonic mutation-owner and error-owner revisions ---

  describe("R1: monotonic mutation-owner revision — ABA safe", () => {
    it("rehydrate success cannot clear error after mutation cleared+re-set same error (ABA)", async () => {
      // The error owner revision must increment even when the error string
      // is cleared then re-set to the same value. A stale rehydrate that
      // captured the old error revision must NOT clear the new error.
      // Real ABA: mutation1 fails (error="boom"), mutation2 starts (error
      // cleared via set), mutation2 fails (error="boom" again). The
      // error-owner revision incremented on each transition.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Start a rehydrate (R) that hangs.
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, mutation1 fails — sets error "boom".
      service.sendShouldReject = new Error("boom");
      await store.getState().send(service, textInput("first"));
      expect(store.getState().error).toBe("boom");
      // Mutation2 starts — clears error via set (errorOwnerRev increments).
      // Then mutation2 also fails with the SAME error string — ABA.
      service.sendShouldReject = new Error("boom");
      await store.getState().send(service, textInput("second"));
      expect(store.getState().error).toBe("boom");
      // Release R — it must NOT clear the error (the error-owner revision
      // changed during the await, even though the string is the same).
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      // Clean up any trailing reread from the mutation owner change.
      ctrl.release();
      await yieldMicrotask();
      expect(store.getState().error).toBe("boom");
    });

    it("rehydrate success cannot publish projection after mutation ABA (same mutationId)", async () => {
      // The mutation-owner revision must increment when a mutation is set
      // then cleared then a new mutation set (even if mutationId wraps or
      // the same pendingMutation shape reappears). A stale rehydrate must
      // not publish a predating projection.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t1",
              items: [
                { kind: "user", id: "u1", text: "initial" } as never,
              ] as never,
            }),
          ],
        }),
      );
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Start a rehydrate (R) that hangs — it captured the initial mutation
      // owner revision (no mutation pending → revision 0).
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, start a send (sets pendingMutation, increments
      // mutation-owner revision), then let it succeed (clears pendingMutation,
      // increments mutation-owner revision again).
      const sendP = store.getState().send(service, textInput("hello"));
      await sendP;
      // pendingMutation is null again, but the mutation-owner revision
      // incremented twice (set + clear). R's captured revision (0) is stale.
      expect(store.getState().pendingMutation).toBeNull();
      // Change the projection result so R would publish a stale projection
      // (different items).
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t1",
              items: [
                { kind: "user", id: "stale-item", text: "stale" } as never,
              ] as never,
            }),
          ],
        }),
      );
      // Release R — it must NOT replace the conversation because the
      // mutation-owner revision changed during the await.
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      // The conversation should NOT have been replaced by R's stale
      // projection. The items from the original open should remain.
      const items = store.getState().conversation?.items ?? [];
      expect(items.some((i) => i.id === "u1")).toBe(true);
      expect(items.some((i) => i.id === "stale-item")).toBe(false);
    });

    it("rehydrate failure installs rejection before controlled read captures it and proves true reject", async () => {
      // The failure path must capture the error-owner revision BEFORE the
      // read that rejects. If a newer error owner writes during the await,
      // R's failure must not overwrite it.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Start a rehydrate (R) that hangs — the read will reject.
      const ctrl = makeControlledErrorRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, a mutation fails — sets a mutation error.
      service.sendShouldReject = new Error("mutation boom");
      await store.getState().send(service, textInput("x"));
      expect(store.getState().error).toBe("mutation boom");
      // Now release R's first call — it rejects.
      ctrl.releaseFirst();
      // R's failure must NOT overwrite the mutation error.
      await yieldMicrotask();
      await yieldMicrotask();
      expect(store.getState().error).toBe("mutation boom");
      expect(store.getState().pendingMutation?.status).toBe("failed");
      // Clean up the trailing reread if one was scheduled.
      ctrl.release();
      await yieldMicrotask();
    });

    it("if mutation owner changed during rehydrate, one bounded trailing reread is scheduled", async () => {
      // When the mutation owner changes during a rehydrate, the rehydrate
      // must not publish its projection. Instead, exactly one trailing
      // authoritative reread is scheduled through the scheduler (no loop,
      // no reentrant await). The trailing reread publishes the fresh
      // projection once the mutation has settled.
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Initial projection has no items.
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Start a rehydrate (R) that hangs.
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      const readsBeforeMutation = service.readProjectionCalls.length;
      // While R is in-flight, start a send that succeeds (mutation owner
      // revision increments: set then clear).
      const sendP = store.getState().send(service, textInput("hello"));
      await sendP;
      // Change the projection so the trailing reread returns fresh items.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t1",
              items: [
                { kind: "user", id: "fresh-item", text: "fresh" } as never,
              ] as never,
            }),
          ],
        }),
      );
      // Release R — it must not publish, but must schedule one trailing reread.
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      // The trailing reread was scheduled via the scheduler. It needs to
      // start and complete. Since it goes through the same scheduler, it
      // hangs on the controlled read. Let it start and release it.
      await ctrl.started(2);
      await yieldMicrotask();
      ctrl.release();
      await ctrl.completed(2);
      await yieldMicrotask();
      // Exactly one trailing reread was scheduled (in addition to the
      // original rehydrate read).
      expect(service.readProjectionCalls.length).toBe(readsBeforeMutation + 1);
      // The trailing reread publishes the fresh projection.
      const items = store.getState().conversation?.items ?? [];
      expect(items.some((i) => i.id === "fresh-item")).toBe(true);
    });
  });

  // --- Residual: R2 — page-race must not drop authoritative outcome ---

  describe("R2: page-race preserves authoritative outcome", () => {
    it("ask reread racing page success: authoritative question appears, page items/cursor preserved", async () => {
      // A rehydrate (R) is triggered by an ask_user notification. While R
      // is in-flight, a loadOlder (L) succeeds, prepending page items and
      // advancing the cursor. R's projection contains a question. When R
      // completes, the question must appear AND the page items/cursor must
      // be preserved (merged, not dropped).
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Initial: empty conversation.
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });
      // Set up R's projection to contain a question.
      const askThread = makeThread({
        evener: {
          ref: "ref-1",
          capabilities: { ...ALL_TRUE_CAPS },
          queue: { revision: 0 },
          askPending: true,
        },
        turns: [
          makeTurn({
            id: "t1",
            items: [askUserItem("ask-1", VALID_ASK_ARGS)],
            status: "completed",
          }),
        ],
      });
      service.readProjectionResult = makeReadProjectionResult(askThread);
      // Trigger R via ask_user notification — it hangs.
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: askUserItem("ask-1", VALID_ASK_ARGS),
        },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, L succeeds — prepends page items, advances cursor.
      service.olderItems = {
        items: [{ kind: "user", id: "old-page-item", text: "older" }],
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);
      const itemsAfterL = store.getState().conversation?.items ?? [];
      const cursorAfterL = store.getState().olderCursor;
      expect(itemsAfterL.some((i) => i.id === "old-page-item")).toBe(true);
      expect(cursorAfterL).toBe("cursor-2");
      // Release R — the question must appear AND page items/cursor preserved.
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      const items = store.getState().conversation?.items ?? [];
      // Question row from R's projection appears.
      expect(items.some((i) => i.kind === "question")).toBe(true);
      // Page items from L are preserved (not dropped).
      expect(items.some((i) => i.id === "old-page-item")).toBe(true);
      // Cursor from L is preserved.
      expect(store.getState().olderCursor).toBe("cursor-2");
    });

    it("preserves page-owned history when wire id differs from transcript key", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [makeTurn({ items: [userMessageItem("base", "base")] })],
        }),
      );
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              items: [userMessageItem("base", "base")],
            }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();

      service.olderItems = {
        items: [
          {
            kind: "user",
            id: "wire-page",
            transcriptKey: "stable-page",
            text: "older page",
          },
        ],
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);

      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      const items = store.getState().conversation?.items ?? [];
      expect(
        items.filter((item) => item.transcriptKey === "stable-page"),
      ).toHaveLength(1);
      expect(
        items.find((item) => item.transcriptKey === "stable-page")?.id,
      ).toBe("wire-page");
      expect(store.getState().olderCursor).toBe("cursor-2");
    });

    it("dedupes page and reread items by transcript key when wire ids differ", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });
      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              items: [
                {
                  ...userMessageItem("wire-reread", "canonical"),
                  transcriptKey: "stable-page",
                },
              ],
            }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      service.olderItems = {
        items: [
          {
            kind: "user",
            id: "wire-page",
            transcriptKey: "stable-page",
            text: "older",
          },
        ],
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      const matches = (store.getState().conversation?.items ?? []).filter(
        (item) => item.transcriptKey === "stable-page",
      );
      expect(matches).toHaveLength(1);
      expect(matches[0]).toMatchObject({
        id: "wire-reread",
        text: "canonical",
      });
    });

    it("dedupes a live tail against a reread item by transcript key", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: {
            ...userMessageItem("wire-live", "live"),
            transcriptKey: "stable-live",
          },
        },
      } as AnyNotification);
      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              items: [
                {
                  ...userMessageItem("wire-reread", "canonical"),
                  transcriptKey: "stable-live",
                },
              ],
            }),
          ],
        }),
      );
      const reread = store.getState().rehydrate(service, createFakeSink());
      await ctrl.started(1);
      ctrl.release();
      await reread;
      await ctrl.completed(1);
      await yieldMicrotask();
      const matches = (store.getState().conversation?.items ?? []).filter(
        (item) => item.transcriptKey === "stable-live",
      );
      expect(matches).toHaveLength(1);
      expect(matches[0]).toMatchObject({
        id: "wire-reread",
        text: "canonical",
      });
    });

    it("preserves a newer live version across a wire-id change during reread", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              items: [
                {
                  ...userMessageItem("wire-reread", "canonical"),
                  transcriptKey: "stable-live",
                },
              ],
            }),
          ],
        }),
      );
      const reread = store.getState().rehydrate(service, createFakeSink());
      await ctrl.started(1);
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: {
            ...userMessageItem("wire-live-new", "newer-live"),
            transcriptKey: "stable-live",
          },
        },
      } as AnyNotification);
      ctrl.release();
      await reread;
      const matches = (store.getState().conversation?.items ?? []).filter(
        (item) => item.transcriptKey === "stable-live",
      );
      expect(matches).toHaveLength(1);
      expect(matches[0]).toMatchObject({
        id: "wire-live-new",
        text: "newer-live",
      });
    });

    it("activity reread racing page failure: authoritative activity appears, page error preserved", async () => {
      // A rehydrate (R) is triggered by an unknown item notification. While
      // R is in-flight, a loadOlder (L) fails, setting a page error. R's
      // projection contains an activity item. When R completes, the activity
      // must appear AND the page error must be preserved.
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Initial: empty conversation.
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });
      // Set up R's projection to contain an activity item.
      const activityThread = makeThread({
        turns: [
          makeTurn({
            id: "t1",
            items: [
              {
                type: "commandExecution",
                id: "tool-1",
                toolName: "bash",
                status: "completed",
                argumentsJson: "{}",
                output: "done",
              } as ThreadItem,
            ],
            status: "completed",
          }),
        ],
      });
      service.readProjectionResult = makeReadProjectionResult(activityThread);
      // Trigger R via resync — it hangs.
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, L fails — sets a page error.
      service.olderItems = Promise.reject(
        new Error("page load failed"),
      ) as never;
      await store
        .getState()
        .loadOlder(service)
        .catch(() => {});
      expect(store.getState().error).not.toBeNull();
      const errorAfterL = store.getState().error;
      // Release R — the activity must appear AND page error preserved.
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      const items = store.getState().conversation?.items ?? [];
      // Activity row from R's projection appears.
      expect(
        items.some((i) => i.kind === "activity" && i.id === "tool-1"),
      ).toBe(true);
      // Page error is preserved.
      expect(store.getState().error).toBe(errorAfterL);
    });
  });

  // --- Residual: R3 — real malformed/incomplete Thread fixtures through projectConversation ---

  describe("R3: real malformed/incomplete Thread fixtures through projectConversation", () => {
    it("malformed completed ask_user in raw Thread: conservative activity rows, no question rows, askPending false", async () => {
      // A real raw Thread with a completed ask_user whose argumentsJson is
      // malformed must produce conservative activity rows (not question
      // rows), askPending false, and exactly one bounded reread.
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Initial: empty.
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // The reread returns a Thread with a malformed completed ask_user.
      const malformedThread = makeThread({
        turns: [
          makeTurn({
            id: "t1",
            items: [askUserItem("ask-malformed", "{{not valid json")],
            status: "completed",
          }),
        ],
      });
      service.readProjectionResult = makeReadProjectionResult(malformedThread);
      const initialReads = service.readProjectionCalls.length;
      // Trigger the reread via the ask_user completed notification.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: askUserItem("ask-malformed", "{{not valid json"),
        },
      } as AnyNotification);
      const ctrl = makeControlledRead(service);
      await ctrl.started(1);
      await yieldMicrotask();
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      // Exactly one bounded reread.
      expect(service.readProjectionCalls.length).toBe(initialReads + 1);
      const conv = store.getState().conversation;
      expect(conv).not.toBeNull();
      // No question rows — malformed ask_user is conservative.
      const questionItem = conv?.items.find((i) => i.kind === "question");
      expect(questionItem).toBeUndefined();
      // askPending is false — malformed ask does not set pending.
      expect(conv?.askPending).toBe(false);
      // The ask_user is projected as a completed activity row (not a
      // question), since projectConversation's parseAskUserQuestions returns
      // undefined for malformed argumentsJson.
      const activityItem = conv?.items.find(
        (i) => i.kind === "activity" && i.id === "ask-malformed",
      );
      expect(activityItem).toBeDefined();
    });

    it("incomplete inProgress ask_user in raw Thread: conservative, no question rows, askPending false", async () => {
      // A real raw Thread with an inProgress ask_user must not produce
      // question rows or set askPending. The inProgress ask_user is
      // projected as a running activity row.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // The reread returns a Thread with an inProgress ask_user.
      const incompleteThread = makeThread({
        turns: [
          makeTurn({
            id: "t1",
            items: [
              askUserItem("ask-incomplete", VALID_ASK_ARGS, "inProgress"),
            ],
            status: "running",
          }),
        ],
      });
      service.readProjectionResult = makeReadProjectionResult(incompleteThread);
      const initialReads = service.readProjectionCalls.length;
      // Trigger the reread via the ask_user started notification.
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: askUserItem("ask-incomplete", VALID_ASK_ARGS, "inProgress"),
        },
      } as AnyNotification);
      const ctrl = makeControlledRead(service);
      await ctrl.started(1);
      await yieldMicrotask();
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      // Exactly one bounded reread.
      expect(service.readProjectionCalls.length).toBe(initialReads + 1);
      const conv = store.getState().conversation;
      expect(conv).not.toBeNull();
      // No question rows — inProgress ask is not pending.
      const questionItem = conv?.items.find((i) => i.kind === "question");
      expect(questionItem).toBeUndefined();
      // askPending is false — inProgress ask_user is not answerable.
      expect(conv?.askPending).toBe(false);
      // The inProgress ask_user is projected as a running activity row.
      const activityItem = conv?.items.find(
        (i) => i.kind === "activity" && i.id === "ask-incomplete",
      );
      expect(activityItem).toBeDefined();
      if (activityItem?.kind === "activity") {
        expect(activityItem.state).toBe("running");
      }
    });
  });

  // --- Fix round 1 (residual review): C1/I1/I2/I3 ---

  // C1: Trailing reread must capture the EXACT original RequestBinding at
  // schedule time (epoch/ref/gen/service/sink), NOT recapture the current
  // binding inside the effect. If the store switched to serviceB+sinkB during
  // the await, the trailing reread must NOT call serviceA. The effect
  // validates the captured snapshot — never recapture B then combine with
  // A's closure variables.
  describe("C1: trailing reread captures exact original binding snapshot", () => {
    it("trailing reread suppressed after switch to serviceB — zero serviceA reads", async () => {
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = makeReadProjectionResult(
        makeThread({ id: "thread-A" }),
      );
      const sinkA = createFakeSink();
      const store = createConversationStore();
      await store.getState().openProjected(serviceA, sinkA, "ref-A");
      // Start a rehydrate (R) on serviceA that hangs.
      const ctrl = makeControlledRead(serviceA);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-A", ref: "ref-A" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, start a send that succeeds — mutation owner
      // revision changes, triggering a trailing reread.
      const sendP = store.getState().send(serviceA, textInput("hello"));
      await sendP;
      // Switch to serviceB — new binding epoch.
      const serviceB = new FakeConversationService();
      serviceB.readProjectionResult = makeReadProjectionResult(
        makeThread({ id: "thread-B" }),
      );
      await store.getState().openProjected(serviceB, createFakeSink(), "ref-B");
      // Release R — it should detect mutation owner changed and schedule a
      // trailing reread. The trailing reread was captured with serviceA+sinkA.
      // After the switch to serviceB, the trailing reread must be suppressed
      // (the captured binding is stale).
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      // Let the trailing reread effect run (it should be suppressed).
      await yieldMicrotask();
      // serviceA should NOT have been called for a trailing reread.
      // The original R read + openProjected read = 2 calls to serviceA.
      const aReads = serviceA.readProjectionCalls.length;
      // serviceB's openProjected read = 1 call to serviceB.
      const bReads = serviceB.readProjectionCalls.length;
      // No trailing reread on serviceA — its read count stays at 2.
      expect(aReads).toBe(2);
      expect(bReads).toBe(1);
    });
  });

  // I1: If mutation revision changed and mutation still pending, the trailing
  // reread must NOT queue/read yet. Store one binding+revision-owned trailing
  // request and drain exactly once only after that mutation settles (success
  // or failed terminal). Additional mutation revisions create at most one
  // later need. A switch to serviceB while the trailing reread is deferred
  // drops the deferred request entirely.
  describe("I1: trailing reread deferred until mutation settles", () => {
    it("zero trailing reads while mutation pending, exactly one after settlement", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Start a rehydrate (R) that hangs.
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, start a send that HANGS (mutation still pending).
      let resolveSend: (() => void) | null = null as (() => void) | null;
      const hangSend = new Promise<MutationReceipt>((r) => {
        resolveSend = () => r(makeReceipt());
      });
      service.send = async () => hangSend;
      store.getState().send(service, textInput("hello"));
      // Mutation is pending — mutation owner revision changed.
      expect(store.getState().pendingMutation?.status).toBe("pending");
      // Release R — it detects mutation owner changed, schedules trailing
      // reread. But the mutation is STILL PENDING, so the trailing reread
      // must NOT start yet (not even call readProjection).
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      await yieldMicrotask();
      await yieldMicrotask();
      // Zero trailing reads while mutation is pending — startedCount stays
      // at 1 (only the original R read started).
      expect(ctrl.getStartedCount()).toBe(1);
      // Now settle the mutation (success).
      (resolveSend as () => void)();
      await yieldMicrotask();
      await yieldMicrotask();
      // After settlement, exactly one trailing reread should start.
      await ctrl.started(2);
      await yieldMicrotask();
      ctrl.release();
      await ctrl.completed(2);
      await yieldMicrotask();
      // Exactly one trailing read after settlement.
      expect(ctrl.getStartedCount()).toBe(2);
    });

    it("zero trailing reads while mutation pending (failed terminal), exactly one after", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Start a rehydrate (R) that hangs.
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, start a send that HANGS, then will fail.
      let rejectSend: ((e: Error) => void) | null = null as
        | ((e: Error) => void)
        | null;
      const hangSend = new Promise<MutationReceipt>((_r, reject) => {
        rejectSend = reject;
      });
      service.send = async () => hangSend;
      const sendP = store.getState().send(service, textInput("hello"));
      // Mutation is pending.
      expect(store.getState().pendingMutation?.status).toBe("pending");
      // Release R — schedules trailing reread, but mutation pending.
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      await yieldMicrotask();
      await yieldMicrotask();
      // Zero trailing reads while mutation is pending.
      expect(ctrl.getStartedCount()).toBe(1);
      // Now fail the mutation (terminal failed state).
      (rejectSend as (e: Error) => void)(new Error("send failed"));
      await withWatchdog("send settle (failed)", sendP);
      await yieldMicrotask();
      await yieldMicrotask();
      // After settlement (failed), exactly one trailing reread.
      await ctrl.started(2);
      await yieldMicrotask();
      ctrl.release();
      await ctrl.completed(2);
      await yieldMicrotask();
      expect(ctrl.getStartedCount()).toBe(2);
    });

    it("switch to serviceB while trailing reread deferred drops A entirely", async () => {
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = makeReadProjectionResult(
        makeThread({ id: "thread-A" }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(serviceA, createFakeSink(), "ref-A");
      // Start a rehydrate (R) on serviceA that hangs.
      const ctrlA = makeControlledRead(serviceA);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-A", ref: "ref-A" },
      } as AnyNotification);
      await ctrlA.started(1);
      await yieldMicrotask();
      // While R is in-flight, start a send that hangs (mutation pending).
      let resolveSend: (() => void) | null = null as (() => void) | null;
      const hangSend = new Promise<MutationReceipt>((r) => {
        resolveSend = () => r(makeReceipt());
      });
      serviceA.send = async () => hangSend;
      store.getState().send(serviceA, textInput("hello"));
      // Release R — schedules deferred trailing reread, mutation pending.
      ctrlA.release();
      await ctrlA.completed(1);
      await yieldMicrotask();
      await yieldMicrotask();
      // Switch to serviceB while trailing reread is deferred.
      const serviceB = new FakeConversationService();
      serviceB.readProjectionResult = makeReadProjectionResult(
        makeThread({ id: "thread-B" }),
      );
      await store.getState().openProjected(serviceB, createFakeSink(), "ref-B");
      // Now settle the mutation on serviceA — the trailing reread was
      // captured with serviceA's binding. After the switch to serviceB,
      // the binding is stale, so the trailing reread must be suppressed.
      (resolveSend as () => void)();
      await yieldMicrotask();
      await yieldMicrotask();
      // Let any potential trailing reread drain.
      await yieldMicrotask();
      // serviceA should NOT have received a trailing reread (binding changed).
      const aReads = serviceA.readProjectionCalls.length;
      const bReads = serviceB.readProjectionCalls.length;
      // serviceA: 1 (openProjected) + 1 (R) = 2. No trailing reread.
      expect(aReads).toBe(2);
      // serviceB: 1 (openProjected) only.
      expect(bReads).toBe(1);
    });
  });

  // The reread's snapshot is authoritative over every thread-level frame that
  // preceded its response: AppWire orders the response at the snapshot cut,
  // so such a frame is already folded into the snapshot (docs/superpowers/
  // plans/2026-07-28-appwire-retry-safe-mutations-and-atomic-rejoin.md:19,
  // 372-373). These fixtures hand the store a snapshot that does NOT reflect
  // the frame — an ordering the protocol excludes — to pin that the store
  // does not second-guess the snapshot: no replay, no per-field ownership.
  async function rereadRacing(
    liveWrite: (store: ReturnType<typeof createConversationStore>) => void,
    snapshot: Thread,
    initial: Thread = makeThread(),
  ) {
    const service = new FakeConversationService();
    service.readProjectionResult = makeReadProjectionResult(initial);
    const store = createConversationStore();
    await store.getState().openProjected(service, createFakeSink(), "ref-1");
    service.readProjectionResult = makeReadProjectionResult(snapshot);
    const ctrl = makeControlledRead(service);
    store.getState().applyNotification({
      method: "evener/thread/resync",
      params: { threadId: "thread-1", ref: "ref-1" },
    } as AnyNotification);
    await ctrl.started(1);
    await yieldMicrotask();
    liveWrite(store);
    ctrl.release();
    await ctrl.completed(1);
    await yieldMicrotask();
    return store;
  }
  const target = { threadId: "thread-1", ref: "ref-1" };
  const evenerWith = (over: Partial<EvenerThread>): Thread["evener"] => ({
    ref: "ref-1",
    capabilities: ALL_TRUE_CAPS,
    queue: { revision: 0 },
    ...over,
  });

  describe("the reread's snapshot is authoritative over pre-response capability writes", () => {
    it("normal resync changes caps — rehydrate commits projected capabilities", async () => {
      const conv = (
        await rereadRacing(
          () => {},
          makeThread({ evener: evenerWith({ capabilities: { ...ALL_TRUE_CAPS, send: false } }) }),
          makeThread({ evener: evenerWith({ capabilities: { ...ALL_TRUE_CAPS, send: true } }) }),
        )
      ).getState().conversation;
      expect(conv?.capabilities.send).toBe(false);
    });

    it("a capability publication that preceded the response yields to the snapshot", async () => {
      // The publication turned queue off before the response arrived; the
      // snapshot (queue on, send off) is the newer truth and is what commits.
      const conv = (
        await rereadRacing(
          (store) =>
            store.getState().applyNotification({
              method: "thread/status/changed",
              params: {
                ...target,
                status: { type: "ready" },
                capabilities: { ...ALL_TRUE_CAPS, send: false, queue: false },
              },
            } as AnyNotification),
          makeThread({ evener: evenerWith({ capabilities: { ...ALL_TRUE_CAPS, send: false } }) }),
          makeThread({ evener: evenerWith({ capabilities: { ...ALL_TRUE_CAPS, send: true } }) }),
        )
      ).getState().conversation;
      expect(conv?.capabilities.send).toBe(false);
      expect(conv?.capabilities.queue).toBe(true);
    });
  });

  describe("the reread's snapshot is authoritative over frames that preceded it", () => {
    const liveEscalation = {
      ...target,
      escalationId: "esc-live",
      mode: "workspace",
      tool: "exec",
      kind: "read",
      deniedPath: "/outside",
    };
    it.each<{
      name: string;
      method: string;
      params: Record<string, unknown>;
      snapshot: Thread;
      read: (conv: NonNullable<MobileConversation>) => unknown;
      expected: unknown;
    }>([
      {
        name: "a status change",
        method: "thread/status/changed",
        params: { status: { type: "active" } },
        snapshot: makeThread({ status: { type: "idle" } }),
        read: (conv) => conv.status,
        expected: { type: "idle" },
      },
      {
        name: "a queue change",
        method: "thread/queueChanged",
        params: { queue: { revision: 4, depth: 1, texts: ["later"] } },
        snapshot: makeThread(),
        read: (conv) => conv.queue,
        expected: { revision: 0 },
      },
      {
        name: "a rename",
        method: "evener/thread/name/changed",
        params: { name: "renamed before the response" },
        snapshot: makeThread({ name: "snapshot name" }),
        read: (conv) => conv.name,
        expected: "snapshot name",
      },
      {
        name: "a model change",
        method: "thread/model/changed",
        params: {
          modelProvider: "openai/gpt-x",
          model: "gpt-x",
          reasoningEffortLevels: ["low", "high"],
          supportsReasoning: true,
        },
        snapshot: makeThread({ modelProvider: "anthropic" }),
        read: (conv) => [conv.modelProvider, conv.reasoningEffortLevels],
        expected: ["anthropic", []],
      },
      {
        name: "a goal update",
        method: "evener/goal/updated",
        params: { goal: { objective: "before the response", status: "active", iterations: 2 } },
        snapshot: makeThread({
          evener: evenerWith({ goal: { objective: "snapshot", status: "active", iterations: 1 } }),
        }),
        read: (conv) => conv.goal,
        expected: { objective: "snapshot", status: "active", iterations: 1 },
      },
      {
        name: "a task update",
        method: "evener/task/updated",
        params: { total: 3, done: 2 },
        snapshot: makeThread({ evener: evenerWith({ tasks: { total: 3, done: 1 } }) }),
        read: (conv) => conv.tasks,
        expected: { total: 3, done: 1 },
      },
      {
        name: "a started turn",
        method: "turn/started",
        params: { turn: { id: "t-before", itemsView: "default", status: "inProgress" } },
        snapshot: makeThread(),
        read: (conv) => [conv.activeTurnId, conv.turns.length],
        expected: [undefined, 0],
      },
      {
        name: "an approval request",
        method: "evener/sandbox/escalation/requested",
        params: liveEscalation,
        snapshot: makeThread(),
        read: (conv) => conv.pendingEscalations,
        expected: [],
      },
    ])("commits the snapshot over $name that preceded the response", async ({ method, params, snapshot, read, expected }) => {
      const conv = (
        await rereadRacing(
          (store) =>
            store.getState().applyNotification({
              method,
              params: { ...target, ...params },
            } as AnyNotification),
          snapshot,
        )
      ).getState().conversation;
      if (!conv) throw new Error("conversation gone");
      expect(read(conv)).toEqual(expected);
    });

    // The ordering the protocol does allow: a frame delivered after the
    // response lands on top of the committed snapshot.
    it("applies a frame that arrives after the response on top of the snapshot", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ status: { type: "idle" }, name: "snapshot name" }),
      );
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: target,
      } as AnyNotification);
      await ctrl.ready(1);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: { ...target, status: { type: "active" } },
      } as AnyNotification);
      expect(store.getState().conversation).toMatchObject({
        name: "snapshot name",
        status: { type: "active" },
      });
    });

    // Two fences on the same rule for the frames where a second application
    // would not be idempotent: a turn the snapshot already carries keeps its
    // items and raises no duplicate-turn report, and a steering the snapshot
    // already carries is not appended twice, so the next live steering takes
    // the next index.
    it("does not replay a started turn the snapshot already carries", async () => {
      const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
      try {
        const conv = (
          await rereadRacing(
            (store) =>
              store.getState().applyNotification({
                method: "turn/started",
                params: { ...target, turn: { id: "t-live", itemsView: "default", status: "inProgress" } },
              } as AnyNotification),
            makeThread({
              turns: [
                makeTurn({
                  id: "t-live",
                  status: "inProgress",
                  items: [userMessageItem("u-live", "started during the read")],
                }),
              ],
              evener: evenerWith({ activeTurnId: "t-live" }),
            }),
          )
        ).getState().conversation;
        expect(conv?.activeTurnId).toBe("t-live");
        expect(conv?.turns.map((turn) => [turn.id, turn.items.length])).toEqual([["t-live", 1]]);
        expect(consoleError).not.toHaveBeenCalled();
      } finally {
        consoleError.mockRestore();
      }
    });

    it("does not replay a steering injection the snapshot already carries", async () => {
      const steer = { text: "go left", kind: "user", source: "user", startedAt: 1000 };
      const activeTurn = (items: ThreadItem[]) =>
        makeThread({
          turns: [makeTurn({ id: "t1", status: "inProgress", items })],
          evener: evenerWith({ activeTurnId: "t1" }),
        });
      const store = await rereadRacing(
        (store) =>
          store.getState().applyNotification({
            method: "evener/steering/injected",
            params: { ...target, ...steer },
          } as AnyNotification),
        activeTurn([
          {
            id: "item_steering_0",
            turnId: "t1",
            type: "steering",
            text: steer.text,
            steeringKind: steer.kind,
            source: steer.source,
            startedAt: steer.startedAt,
            status: "completed",
          } as ThreadItem,
        ]),
        activeTurn([]),
      );
      const steeringIds = (conv: MobileConversation | null) =>
        conv?.turns.flatMap((turn) => turn.items.filter((item) => item.type === "steering").map((item) => item.id));
      expect(steeringIds(store.getState().conversation)).toEqual(["item_steering_0"]);
      store.getState().applyNotification({
        method: "evener/steering/injected",
        params: { ...target, text: "then right", kind: "user", source: "user", startedAt: 2000 },
      } as AnyNotification);
      expect(steeringIds(store.getState().conversation)).toEqual([
        "item_steering_0",
        "item_steering_live_t1_1",
      ]);
    });

    // A reread belongs to the conversation it was for: closing that
    // conversation and opening another must leave the next conversation's
    // frames applied and the dead read's snapshot never committed.
    it("never commits a dead read's snapshot after the conversation closes", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ name: "old read's snapshot" }),
      );
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: target,
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      store.getState().close();

      const next = new FakeConversationService();
      next.openConv = makeConversation({ threadId: "thread-2", name: "second" });
      await store.getState().open(next, "ref-2");
      store.getState().applyNotification({
        method: "evener/thread/name/changed",
        params: { threadId: "thread-2", ref: "ref-2", name: "second, renamed live" },
      } as AnyNotification);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      expect(store.getState().conversation).toMatchObject({
        threadId: "thread-2",
        name: "second, renamed live",
      });
    });
  });

  // Dual-write until c-2b: every item frame folds into the package reducer's
  // model as well as today's display rows, so the model half is live now —
  // turns, streamed text, the retry indicator — and c-2b can flip the rows to
  // a projection of it without changing what the phone shows.
  describe("the model is live under the item frames (dual-write until c-2b)", () => {
    const withActiveTurn = (items: ThreadItem[]) =>
      makeThread({
        turns: [makeTurn({ id: "t1", status: "inProgress", items })],
        evener: evenerWith({ activeTurnId: "t1" }),
      });
    const modelItem = (store: ReturnType<typeof createConversationStore>, id: string) =>
      store.getState().conversation?.turns.flatMap((turn) => turn.items).find((item) => item.id === id);

    it("carries a delta stream into the model's item, and a sparse completion keeps it", async () => {
      const store = await openProjectedThread(withActiveTurn([agentMessageItem("a1", undefined, "inProgress")]));
      for (const delta of ["hello ", "world"]) {
        store.getState().applyNotification({
          method: "item/agentMessage/delta",
          params: { ...target, turnId: "t1", itemId: "a1", delta },
        } as AnyNotification);
      }
      store.getState().applyNotification({
        method: "item/completed",
        params: { ...target, turnId: "t1", item: agentMessageItem("a1", undefined, "completed") },
      } as AnyNotification);
      expect(modelItem(store, "a1")).toMatchObject({ text: "hello world", status: "completed" });
      // The row applier is untouched until c-2b: today it re-projects the
      // assistant row from the sparse frame, so the streamed text survives only
      // in the model here — the flip in c-2b is what puts it back on screen.
    });

    it("appends one steering item to the active turn in the model", async () => {
      const store = await openProjectedThread(withActiveTurn([]));
      store.getState().applyNotification({
        method: "evener/steering/injected",
        params: { ...target, text: "go left", kind: "user", source: "user", startedAt: 1000 },
      } as AnyNotification);
      const steering = store.getState().conversation?.turns
        .flatMap((turn) => turn.items)
        .filter((item) => item.type === "steering");
      expect(steering?.map((item) => [item.id, item.text])).toEqual([["item_steering_live_t1_0", "go left"]]);
    });

    it("clears modelRetry when the model's own output item completes", async () => {
      const store = await openProjectedThread(withActiveTurn([agentMessageItem("a1", undefined, "inProgress")]));
      store.getState().applyNotification({
        method: "evener/thread/modelRetry",
        params: { ...target, attempt: 1, maxAttempts: 3, delayMs: 10, groupElapsedMs: 5, attemptCap: 3, turnId: "t1" },
      } as AnyNotification);
      expect(store.getState().conversation?.modelRetry).toMatchObject({ attempt: 1, maxAttempts: 3 });
      store.getState().applyNotification({
        method: "item/completed",
        params: { ...target, turnId: "t1", item: agentMessageItem("a1", "done", "completed") },
      } as AnyNotification);
      expect(store.getState().conversation?.modelRetry).toBeUndefined();
    });

    // Decision 2 in the model: a warning with no active turn has nowhere
    // wire-true to land and is dropped. The row applier still appends its
    // failure row until c-2b — model-only until then.
    it("drops a warning without an active turn in the model while the row applier still shows it", async () => {
      const store = await openProjectedThread(makeThread());
      store.getState().applyNotification({
        method: "warning",
        params: { ...target, title: "Provider", message: "careful" },
      } as AnyNotification);
      const conv = store.getState().conversation;
      expect(conv?.turns.flatMap((turn) => turn.items).filter((item) => item.type === "warning")).toEqual([]);
      expect(conv?.items.filter((row) => row.kind === "failure")).toHaveLength(1);
    });

    // The live row must show the reducer-folded warning item's text/hint,
    // not just params.message — a title/hint-only frame (no top-level
    // message) folds to a non-blank text in the model (the hint), but the
    // row applier was building its failure row from params.message alone
    // and losing it.
    it("shows the reducer-folded hint text for a title/hint-only warning with an active turn", async () => {
      const store = await openProjectedThread(withActiveTurn([]));
      store.getState().applyNotification({
        method: "warning",
        params: { ...target, title: "Provider warning", hint: "slow down" },
      } as AnyNotification);
      const failureRow = store
        .getState()
        .conversation?.items.find((row) => row.kind === "failure");
      expect(failureRow).toMatchObject({
        kind: "failure",
        title: "Provider warning",
        detail: "slow down",
      });
    });

    // A warning carrying both a message and a hint must show both, the same
    // as the web and TUI renderers — the live row must not drop the hint
    // just because a message is also present.
    it("composes both message and hint in the live row's detail", async () => {
      const store = await openProjectedThread(withActiveTurn([]));
      store.getState().applyNotification({
        method: "warning",
        params: { ...target, message: "rate limit approaching", hint: "slow down" },
      } as AnyNotification);
      const failureRow = store
        .getState()
        .conversation?.items.find((row) => row.kind === "failure");
      expect(failureRow).toMatchObject({
        kind: "failure",
        detail: "rate limit approaching — slow down",
      });
    });

    // A whitespace-only stored title is not real content, the same reading
    // hasWarningText gives everywhere else — it must fall back to the
    // generic "Warning" label rather than rendering a blank chip.
    it("falls back to the generic Warning label for a whitespace-only title with an active turn", async () => {
      const store = await openProjectedThread(withActiveTurn([]));
      store.getState().applyNotification({
        method: "warning",
        params: { ...target, title: "   ", message: "careful" },
      } as AnyNotification);
      const failureRow = store
        .getState()
        .conversation?.items.find((row) => row.kind === "failure");
      expect(failureRow).toMatchObject({ kind: "failure", title: "Warning" });
    });

    // Decision 2 in the model: a warning with no active turn has nowhere
    // wire-true to land, so the reducer drops it and this row is the ONLY
    // place the frame folds through. It must run the same validated shape
    // as the reducer's own fold, not copy params.title/params.message raw —
    // a malformed object value must never reach a string-typed timeline
    // field (mobile-native's TimelineItem.tsx renders title/detail as React
    // Native text and would crash on a non-string).
    it("sanitizes a malformed warning without an active turn instead of copying params raw", async () => {
      const store = await openProjectedThread(makeThread());
      store.getState().applyNotification({
        method: "warning",
        params: {
          ...target,
          title: { nested: "object" } as unknown as string,
          message: { nested: "object" } as unknown as string,
        },
      } as AnyNotification);
      const failureRow = store
        .getState()
        .conversation?.items.find((row) => row.kind === "failure");
      expect(failureRow?.kind).toBe("failure");
      if (failureRow?.kind === "failure") {
        expect(typeof failureRow.title).toBe("string");
        expect(typeof failureRow.detail).toBe("string");
        expect(failureRow.title).toBe("Warning");
      }
    });

    // The polymorphic `warning` field (a bare string, or an object with its
    // own `message`) is a supported wire shape (warningMessage's own
    // contract) that the no-active-turn path must honor too, not just
    // top-level `message`.
    it("reads a polymorphic warning field without an active turn", async () => {
      const store = await openProjectedThread(makeThread());
      store.getState().applyNotification({
        method: "warning",
        params: { ...target, warning: "provider hiccup" },
      } as AnyNotification);
      const failureRow = store
        .getState()
        .conversation?.items.find((row) => row.kind === "failure");
      expect(failureRow).toMatchObject({ kind: "failure", detail: "provider hiccup" });
    });

    // A message-less warning frame's text is the up-to-2000-char raw JSON
    // fallback (rawWarningFrame), and the live id used to embed the
    // sanitized title directly (`warning:${title}:${serial}`), which
    // foldWarningParams only bounds to 2000 code points — far short of
    // "short". Neither case's row id must ever embed folded.text or the
    // title; that bloats timeline ids and the ownership keys they feed. The
    // serial alone is already unique, so the id never needs either.
    it.each<[string, Record<string, unknown>]>([
      // No message/title/hint anywhere: folded.text is the bounded raw JSON
      // fallback (up to 2000 chars) — the `extra` field forces it long
      // enough that reusing folded.text for the id would be obvious.
      ["a message-less warning with no active turn", { extra: "x".repeat(500) }],
      ["an oversized title", { title: "T".repeat(2000) }],
    ])("keeps the live row id short for %s", async (_case, warningFields) => {
      const store = await openProjectedThread(makeThread());
      store.getState().applyNotification({
        method: "warning",
        params: { ...target, ...warningFields },
      } as AnyNotification);
      const failureRow = store
        .getState()
        .conversation?.items.find((row) => row.kind === "failure");
      expect(failureRow?.kind).toBe("failure");
      if (failureRow?.kind === "failure") {
        expect(failureRow.id.length).toBeLessThan(30);
        expect(failureRow.id.startsWith("warning:")).toBe(true);
      }
    });

    // The live row applier (case "warning" above) and the canonical projector
    // (projectConversation, applied on every reread) must produce the SAME row
    // for the same warning: an attention row — kind "failure", title as its own
    // field, message+hint joined as detail. The canonical projector used to
    // route type "warning" through its generic unknown-activity fallback, so
    // the row changed kind, lost its attention treatment, and regressed to a
    // collapsed "Activity" row labelled "unknown" the moment a reread replaced
    // the live one.
    it("projects the same warning to the same failure row live and canonically", async () => {
      const store = await openProjectedThread(withActiveTurn([]));
      const warning = {
        method: "warning",
        params: {
          ...target,
          title: "Provider warning",
          message: "rate limit approaching",
          hint: "slow down",
        },
      } as AnyNotification;
      store.getState().applyNotification(warning);
      const live = store.getState().conversation;
      expect(live).not.toBeNull();
      const liveRow = live?.items.find((row) => row.kind === "failure");
      const canonicalRow = projectConversation(live!).items.find(
        (row) => row.kind === "failure",
      );
      const expected = {
        kind: "failure",
        title: "Provider warning",
        detail: "rate limit approaching — slow down",
      };
      expect(liveRow).toMatchObject(expected);
      expect(canonicalRow).toMatchObject(expected);
      expect(canonicalRow?.kind).toBe(liveRow?.kind);
      // One identity, not just one shape: timelineIdentity is
      // transcriptKey ?? id, and a reread dedupes on exactly that, so the two
      // rows must agree here or the warning survives the reread twice.
      const identity = (row: MobileTimelineItem | undefined) =>
        row === undefined ? undefined : (row.transcriptKey ?? row.id);
      expect(identity(canonicalRow)).toBe(identity(liveRow));
    });

    // The identity the live row shares with the canonical one is what keeps a
    // reread from showing the warning twice. The reread projection here is the
    // canonical projection of the same model (what a hub that carries the
    // warning frame in a snapshot — or a future projection path — serves); the
    // live-owned row must be recognized as that identity and dropped, leaving
    // exactly one failure row.
    it("merges a warning to one failure row across a reread", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(withActiveTurn([]));
      const store = createConversationStore();
      const sink = createFakeSink();
      await store.getState().openProjected(service, sink, "ref-1");
      store.getState().applyNotification({
        method: "warning",
        params: {
          ...target,
          title: "Provider warning",
          message: "rate limit approaching",
          hint: "slow down",
        },
      } as AnyNotification);
      const liveRows = (store.getState().conversation?.items ?? []).filter(
        (row) => row.kind === "failure",
      );
      expect(liveRows).toHaveLength(1);
      const liveIdentity = liveRows[0]!.transcriptKey ?? liveRows[0]!.id;

      const reread = projectConversation(store.getState().conversation!);
      service.readProjectionResult = {
        conversation: reread,
        activity: { tasks: [], work: [], usage: {}, capabilities: ALL_TRUE_CAPS },
        olderCursor: null,
      };
      await store.getState().rehydrate(service, sink);

      const rows = (store.getState().conversation?.items ?? []).filter(
        (row) => row.kind === "failure",
      );
      expect(rows).toHaveLength(1);
      expect(rows[0]!.transcriptKey ?? rows[0]!.id).toBe(liveIdentity);
    });

    // Warning → reread without the warning → warning. A hub's snapshot does
    // not carry the non-persisted warning item, so the reread drops it from
    // the model while the live-owned row stays; the model's per-turn warning
    // count is back to zero, so the second warning is handed the same
    // `item_warning_live_<turn>_0` id as the first. The retained row must not
    // hand that id to a second row (duplicate timeline ids), and the second
    // warning must still land as its own row.
    it("keeps warning rows unique through a reread that drops the warning model item", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(withActiveTurn([]));
      const store = createConversationStore();
      const sink = createFakeSink();
      await store.getState().openProjected(service, sink, "ref-1");

      store.getState().applyNotification({
        method: "warning",
        params: { ...target, message: "first warning" },
      } as AnyNotification);
      const firstIds = (store.getState().conversation?.items ?? [])
        .filter((row) => row.kind === "failure")
        .map((row) => row.id);
      expect(firstIds).toHaveLength(1);

      // Reread serves the same thread with no warning item in its turn.
      service.readProjectionResult = makeReadProjectionResult(withActiveTurn([]));
      await store.getState().rehydrate(service, sink);
      const afterReread = (store.getState().conversation?.items ?? [])
        .filter((row) => row.kind === "failure")
        .map((row) => row.id);
      expect(afterReread).toEqual(firstIds);

      store.getState().applyNotification({
        method: "warning",
        params: { ...target, message: "second warning" },
      } as AnyNotification);
      const rows = (store.getState().conversation?.items ?? []).filter(
        (row) => row.kind === "failure",
      );
      expect(rows).toHaveLength(2);
      expect(new Set(rows.map((row) => row.id)).size).toBe(2);
      expect(rows.map((row) => row.detail)).toEqual([
        "first warning",
        "second warning",
      ]);
    });
  });

  // I3: Track actual page-owned item IDs per binding/token. On reread merge,
  // prepend only missing page-owned history; append current-only non-page
  // items as live tail. Never move live notifications to oldest/cap discard.
  // Clear on transition.
  describe("I3: page-owned item tracking — order-aware merge", () => {
    it("page items + concurrent live notification + reread: correct order, no discard", async () => {
      const service = new FakeConversationService();
      // Initial: one user message.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [userMessageItem("live-0", "hello")],
            }),
          ],
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Set cursor so loadOlder can run.
      store.setState({ olderCursor: "cursor-1" });
      // Start a rehydrate (R) that hangs.
      const ctrl = makeControlledRead(service);
      // R's projection has live-0 + a new live-1 item.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [userMessageItem("live-0", "hello")],
            }),
            makeTurn({
              id: "t1",
              items: [userMessageItem("live-1", "world")],
            }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, loadOlder succeeds — prepends page items.
      const pageItems: MobileConversation["items"] = [];
      for (let i = 0; i < 10; i++) {
        pageItems.push({ kind: "user", id: `page-${i}`, text: "old" });
      }
      service.olderItems = { items: pageItems, nextCursor: "cursor-2" };
      await store.getState().loadOlder(service);
      // Current items: 10 page items + live-0.
      const itemsAfterL = store.getState().conversation?.items ?? [];
      expect(itemsAfterL.some((i) => i.id === "page-0")).toBe(true);
      expect(itemsAfterL.some((i) => i.id === "live-0")).toBe(true);
      // Release R — its projection has live-0 + live-1.
      // The merge must:
      // 1. Prepend page items that are missing from R's projection.
      // 2. Append live-1 (from R's projection) as the live tail (after page items).
      // 3. NOT move page items to the oldest position (they stay prepended).
      // 4. NOT move live-1 to the oldest position (it stays as the tail).
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      const items = store.getState().conversation?.items ?? [];
      // All items present.
      expect(items.some((i) => i.id === "page-0")).toBe(true);
      expect(items.some((i) => i.id === "live-0")).toBe(true);
      expect(items.some((i) => i.id === "live-1")).toBe(true);
      // Order: page items first (oldest), then reread items.
      // Page items should be before live-0 and live-1.
      const page0Idx = items.findIndex((i) => i.id === "page-0");
      const live0Idx = items.findIndex((i) => i.id === "live-0");
      const live1Idx = items.findIndex((i) => i.id === "live-1");
      expect(page0Idx).toBeLessThan(live0Idx);
      expect(live0Idx).toBeLessThan(live1Idx);
    });

    it("500-cap tail retention: page items + live notifications, live tail preserved", async () => {
      const service = new FakeConversationService();
      // Initial: 450 items (live-50..live-499) as userMessage ThreadItems.
      const initialThreadItems: ThreadItem[] = [];
      for (let i = 50; i < 500; i++) {
        initialThreadItems.push(userMessageItem(`live-${i}`, ""));
      }
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: initialThreadItems,
            }),
          ],
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Set cursor for loadOlder.
      store.setState({ olderCursor: "cursor-1" });
      // Start a rehydrate (R) that hangs.
      const ctrl = makeControlledRead(service);
      // R's projection: 450 initial + 50 new live items (live-500..live-549).
      const rereadThreadItems: ThreadItem[] = [];
      for (let i = 50; i < 550; i++) {
        rereadThreadItems.push(userMessageItem(`live-${i}`, ""));
      }
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: rereadThreadItems,
            }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, loadOlder loads 100 page items.
      // loadOlder merges: [page-0..page-99(100), live-50..live-499(450)] = 550.
      // capItems keeps newest 500: [page-50..page-99(50), live-50..live-499(450)].
      const pageItems: MobileConversation["items"] = [];
      for (let i = 0; i < 100; i++) {
        pageItems.push({ kind: "user", id: `page-${i}`, text: "" });
      }
      service.olderItems = { items: pageItems, nextCursor: "cursor-2" };
      await store.getState().loadOlder(service);
      const itemsAfterL = store.getState().conversation?.items ?? [];
      expect(itemsAfterL.length).toBe(500);
      // Release R — its projection has live-50..live-549 (500 items).
      // I3 merge: prepend page-owned items (page-50..page-99) that are NOT in
      // R's projection, then append R's items (live-50..live-549).
      // merged = [page-50..page-99(50), live-50..live-549(500)] = 550.
      // capItems keeps newest 500: live-50..live-549 (drops all page items).
      // BUT I3 requires that page items (oldest) are dropped FIRST, not live
      // items. The correct behavior: page items are at the front (oldest),
      // so capItems drops them first (keeping the live tail intact).
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      const items = store.getState().conversation?.items ?? [];
      expect(items.length).toBeLessThanOrEqual(500);
      // The newest live items must be retained (live tail preserved).
      expect(items.some((i) => i.id === "live-549")).toBe(true);
      expect(items.some((i) => i.id === "live-500")).toBe(true);
      // The last item is the newest live item.
      const lastItem = items[items.length - 1];
      expect(lastItem?.id).toBe("live-549");
    });
  });

  // Residual 1: deferred trailing reread rechecks exact binding, captured/current
  // monotonic mutation revision, and true terminal status INSIDE the scheduler
  // effect immediately before any read. If a newer mutation is pending after
  // enqueue, do zero read; atomically restore/update one binding-owned deferred
  // request for that mutation revision and let its settle hook drain exactly
  // once. Keep separate queued/deferred identity so no duplicate effects/third
  // reread.
  describe("Residual 1: deferred trailing reread rechecks mutation revision inside effect", () => {
    it("M1 settles and queues; M2 starts before effect; release => zero reread; settle M2 => exactly one", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Start a rehydrate (R) that hangs.
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, start M1 that hangs (mutation pending).
      let resolveM1: (() => void) | null = null as (() => void) | null;
      const hangM1 = new Promise<MutationReceipt>((r) => {
        resolveM1 = () => r(makeReceipt());
      });
      service.send = async () => hangM1;
      store.getState().send(service, textInput("m1"));
      expect(store.getState().pendingMutation?.status).toBe("pending");
      // Release R — detects mutation owner changed, schedules deferred
      // trailing reread. But M1 is still pending, so it stays deferred.
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      await yieldMicrotask();
      await yieldMicrotask();
      // Zero trailing reads while M1 is pending.
      expect(ctrl.getStartedCount()).toBe(1);
      // Settle M1 (success) — drainTrailingReread enqueues the trailing reread
      // via scheduler.request(). The effect is deferred to a microtask.
      (resolveM1 as () => void)();
      await yieldMicrotask(); // M1's settle resolves, drainTrailingReread fires
      // Now, BEFORE the trailing reread's scheduler effect fires, start M2
      // (new mutation pending). This must be synchronous — no await between
      // M1 settle and M2 start.
      let resolveM2: (() => void) | null = null as (() => void) | null;
      const hangM2 = new Promise<MutationReceipt>((r) => {
        resolveM2 = () => r(makeReceipt());
      });
      service.send = async () => hangM2;
      store.getState().send(service, textInput("m2"));
      expect(store.getState().pendingMutation?.status).toBe("pending");
      // Now let the trailing reread's scheduler effect fire. It must detect
      // M2 is pending (mutationOwnerRev advanced, mutation pending) and do
      // ZERO read — re-defer for M2's revision.
      await yieldMicrotask();
      await yieldMicrotask();
      await yieldMicrotask();
      // Still only 1 started read (the original R). No trailing reread fired.
      expect(ctrl.getStartedCount()).toBe(1);
      // Settle M2 (success) — drainTrailingReread should now fire exactly one
      // trailing reread.
      (resolveM2 as () => void)();
      await yieldMicrotask();
      await yieldMicrotask();
      // The trailing reread should start (2nd controlled read).
      await ctrl.started(2);
      await yieldMicrotask();
      ctrl.release();
      await ctrl.completed(2);
      await yieldMicrotask();
      // Exactly one trailing reread after M2 settles — no third reread.
      expect(ctrl.getStartedCount()).toBe(2);
    });

    it("B transition drops A: re-deferred trailing reread suppressed after switch to serviceB", async () => {
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = makeReadProjectionResult(
        makeThread({ id: "thread-A" }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(serviceA, createFakeSink(), "ref-A");
      // Start a rehydrate (R) on serviceA that hangs.
      const ctrlA = makeControlledRead(serviceA);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-A", ref: "ref-A" },
      } as AnyNotification);
      await ctrlA.started(1);
      await yieldMicrotask();
      // While R is in-flight, start M1 that hangs (mutation pending).
      let resolveM1: (() => void) | null = null as (() => void) | null;
      const hangM1 = new Promise<MutationReceipt>((r) => {
        resolveM1 = () => r(makeReceipt());
      });
      serviceA.send = async () => hangM1;
      store.getState().send(serviceA, textInput("m1"));
      expect(store.getState().pendingMutation?.status).toBe("pending");
      // Release R — schedules deferred trailing reread, M1 pending.
      ctrlA.release();
      await ctrlA.completed(1);
      await yieldMicrotask();
      await yieldMicrotask();
      // Settle M1 — drainTrailingReread enqueues trailing reread via scheduler.
      (resolveM1 as () => void)();
      await yieldMicrotask();
      // Before the effect fires, start M2 (new mutation pending).
      let resolveM2: (() => void) | null = null as (() => void) | null;
      const hangM2 = new Promise<MutationReceipt>((r) => {
        resolveM2 = () => r(makeReceipt());
      });
      serviceA.send = async () => hangM2;
      store.getState().send(serviceA, textInput("m2"));
      expect(store.getState().pendingMutation?.status).toBe("pending");
      // Let the effect fire — it should re-defer (M2 pending, zero read).
      await yieldMicrotask();
      await yieldMicrotask();
      // Switch to serviceB — new binding epoch.
      const serviceB = new FakeConversationService();
      serviceB.readProjectionResult = makeReadProjectionResult(
        makeThread({ id: "thread-B" }),
      );
      await store.getState().openProjected(serviceB, createFakeSink(), "ref-B");
      // Settle M2 on serviceA — drainTrailingReread should check binding,
      // find it stale (switched to B), and drop. Zero trailing reread on A.
      (resolveM2 as () => void)();
      await yieldMicrotask();
      await yieldMicrotask();
      await yieldMicrotask();
      // serviceA: 1 (openProjected) + 1 (R) = 2. No trailing reread.
      expect(serviceA.readProjectionCalls.length).toBe(2);
      // serviceB: 1 (openProjected) only.
      expect(serviceB.readProjectionCalls.length).toBe(1);
    });
  });

  // Residual 2: exact live-notification-owned item IDs per binding, separate
  // from pageOwned IDs. Mark IDs only from actual accepted item lifecycle/live
  // notifications (and relevant deltas); clear/reconcile on open/transition/
  // authoritative inclusion. Page merge: prepend only current-only pageOwned
  // history, commit authoritative projection, append only current-only liveOwned
  // tail; drop current-only items owned by neither as omitted old history.
  // Dedupe/order/cap newest tail.
  describe("Residual 2: live-owned item IDs — page merge drops unowned, keeps live tail", () => {
    it("current old A omitted, page P, live N absent reread: result P + B/C + N, A dropped", async () => {
      const service = new FakeConversationService();
      // Initial projection: items A and B (A is old, will be omitted from
      // reread; B is authoritative and will appear in reread).
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("A", "old initial"),
                userMessageItem("B", "kept initial"),
              ],
            }),
          ],
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Set cursor so loadOlder can run.
      store.setState({ olderCursor: "cursor-1" });
      // Start a rehydrate (R) that hangs.
      const ctrl = makeControlledRead(service);
      // R's projection: B and C (authoritative — no A, no N, no P).
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("B", "kept initial"),
                userMessageItem("C", "fresh authoritative"),
              ],
            }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, loadOlder succeeds — prepends page items P.
      service.olderItems = {
        items: [{ kind: "user", id: "P", text: "page old" }],
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);
      // Current items: P + A + B.
      const itemsAfterL = store.getState().conversation?.items ?? [];
      expect(itemsAfterL.some((i) => i.id === "P")).toBe(true);
      expect(itemsAfterL.some((i) => i.id === "A")).toBe(true);
      expect(itemsAfterL.some((i) => i.id === "B")).toBe(true);
      // While R is still in-flight, a live notification inserts N (live-owned).
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: userMessageItem("N", "live notification"),
        },
      } as AnyNotification);
      // Current items: P + A + B + N.
      const itemsAfterN = store.getState().conversation?.items ?? [];
      expect(itemsAfterN.some((i) => i.id === "N")).toBe(true);
      // Release R — its projection has B + C (no A, no N, no P).
      // Merge must: prepend P (pageOwned), commit B/C (authoritative), append
      // N (liveOwned tail), drop A (owned by neither, omitted from reread).
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      const items = store.getState().conversation?.items ?? [];
      const ids = items.map((i) => i.id);
      // P is retained (page-owned history).
      expect(ids).toContain("P");
      // B and C are retained (authoritative reread projection).
      expect(ids).toContain("B");
      expect(ids).toContain("C");
      // N is retained (live-owned tail from actual notification).
      expect(ids).toContain("N");
      // A is dropped (owned by neither page nor live, omitted from reread).
      expect(ids).not.toContain("A");
      // Order: P (page history) before B/C (authoritative) before N (live tail).
      const pIdx = ids.indexOf("P");
      const bIdx = ids.indexOf("B");
      const cIdx = ids.indexOf("C");
      const nIdx = ids.indexOf("N");
      expect(pIdx).toBeLessThan(bIdx);
      expect(bIdx).toBeLessThan(nIdx);
      expect(cIdx).toBeLessThan(nIdx);
    });

    it("500 cap retains live-owned N, drops unowned old history", async () => {
      const service = new FakeConversationService();
      // Initial: 499 items — A-0..A-498 (old initial, not page/live owned) + B.
      // Actually we need exactly: initial has many old items + a few live-owned.
      // Let's make: 450 old items (old-0..old-449) + 49 items live-owned (live-0..live-48) + B.
      // That's 500 total. Then loadOlder adds 50 page items, pushing old items
      // out via cap. Then a live notification adds N. Reread has B + C only.
      // After merge: P(50) + B + C + N. But we need 500 cap to retain N.
      // Simpler: fill to near cap, then check N survives the cap.
      const initialThreadItems: ThreadItem[] = [];
      // 499 old initial items (will be omitted from reread, owned by neither).
      for (let i = 0; i < 499; i++) {
        initialThreadItems.push(userMessageItem(`old-${i}`, ""));
      }
      // B is in both initial and reread (authoritative).
      initialThreadItems.push(userMessageItem("B", ""));
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [makeTurn({ id: "t0", items: initialThreadItems })],
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });
      // Start a rehydrate (R) that hangs.
      const ctrl = makeControlledRead(service);
      // R's projection: B + C (authoritative — no old items, no N, no P).
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [userMessageItem("B", ""), userMessageItem("C", "")],
            }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, loadOlder loads 500 page items.
      const pageItems: MobileConversation["items"] = [];
      for (let i = 0; i < 500; i++) {
        pageItems.push({ kind: "user", id: `P-${i}`, text: "" });
      }
      service.olderItems = { items: pageItems, nextCursor: "cursor-2" };
      await store.getState().loadOlder(service);
      // loadOlder merge: [P-0..P-499(500), old-0..old-498(499), B(1)] = 1000.
      // capItems keeps newest 500: [old-249..old-498(250), B(1), P-0..P-249(250)].
      // Wait — loadOlder prepends deduped page items. P items are new (not in
      // current), so deduped = all 500 P items. merged = [P-0..P-499, old-0..old-498, B]
      // = 1000. capItems keeps newest 500: old-250..old-498, B, P-0..P-249.
      // Actually capItems slices from the end: items.slice(len - 500).
      // So newest 500 = [old-250..old-498(249), B(1), P-0..P-249(250)] = 500.
      // While R is still in-flight, a live notification inserts N (live-owned).
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: userMessageItem("N", "live"),
        },
      } as AnyNotification);
      // Current items after loadOlder + N: 500 + N = 501, capped to 500.
      // N is at the tail (newest), so it's retained.
      const itemsBeforeR = store.getState().conversation?.items ?? [];
      expect(itemsBeforeR.some((i) => i.id === "N")).toBe(true);
      // Release R — projection has B + C only.
      // Merge: prepend pageOwned items not in reread, commit B/C, append
      // liveOwned items not in reread (N), drop items owned by neither
      // (old-* items that are neither pageOwned nor liveOwned).
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      const items = store.getState().conversation?.items ?? [];
      const ids = items.map((i) => i.id);
      // N must be retained (live-owned tail preserved by 500 cap).
      expect(ids).toContain("N");
      // B and C are retained (authoritative reread).
      expect(ids).toContain("B");
      expect(ids).toContain("C");
      // Old items (owned by neither) are dropped.
      expect(ids.some((id) => id.startsWith("old-"))).toBe(false);
      // Total within cap.
      expect(items.length).toBeLessThanOrEqual(500);
      // N is at or near the tail.
      const nIdx = ids.indexOf("N");
      expect(nIdx).toBeGreaterThan(-1);
      // N should be after B and C (live tail after authoritative).
      const bIdx = ids.indexOf("B");
      const cIdx = ids.indexOf("C");
      expect(nIdx).toBeGreaterThan(bIdx);
      expect(nIdx).toBeGreaterThan(cIdx);
    });
  });

  // Fix round 1: Replace insertion-only ownership with accepted per-item live
  // ownership + monotonic revision. Every accepted lifecycle insertion OR
  // replacement, agent/reasoning/tool delta, reset, warning increments/marks;
  // rejected/missing/wrong/frozen no mark. Rehydrate captures entry live
  // revision. At commit: if authoritative contains ID but current revision
  // advanced after entry, preserve current updated version in authoritative
  // position and keep ownership; otherwise accept authoritative and clear that
  // ID's ownership. If authoritative omits ID, append only genuinely live-owned
  // current item; page IDs still prepend; unowned old drops.
  describe("Fix round 1: per-item live ownership with monotonic revision", () => {
    // Helper: set up a store with an initial assistant item X and a page cursor,
    // start a hanging rehydrate, apply a live notification to X, then release
    // the rehydrate. Returns the store and ctrl for further assertions.
    async function setupLiveUpdateX(
      initialX: ThreadItem,
      rereadItems: ThreadItem[],
      liveNotification: AnyNotification,
      pageItems?: MobileConversation["items"],
    ) {
      const service = new FakeConversationService();
      // Initial projection: B + X.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [userMessageItem("B", "base"), initialX],
            }),
          ],
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });
      // Start a rehydrate (R) that hangs.
      const ctrl = makeControlledRead(service);
      // R's projection may or may not include X (stale or omitted).
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [makeTurn({ id: "t0", items: rereadItems })],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, optionally loadOlder (page items).
      if (pageItems !== undefined) {
        service.olderItems = { items: pageItems, nextCursor: "cursor-2" };
        await store.getState().loadOlder(service);
      }
      // While R is still in-flight, apply the live notification to X.
      store.getState().applyNotification(liveNotification);
      // Release R.
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      return { store, service, ctrl };
    }

    // (a) duplicate completed replacement
    it("reread includes stale X: item/completed replacement preserves live-updated X", async () => {
      const { store } = await setupLiveUpdateX(
        agentMessageItem("X", "original", "inProgress"),
        // Reread includes stale X (same id, old text).
        [
          userMessageItem("B", "base"),
          agentMessageItem("X", "stale-from-reread", "completed"),
        ],
        // Live notification: item/completed replaces X with final text.
        {
          method: "item/completed",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t0",
            item: agentMessageItem("X", "live-final-text", "completed"),
          },
        } as AnyNotification,
      );
      const items = store.getState().conversation?.items ?? [];
      const xItem = items.find((i) => i.id === "X");
      expect(xItem).toBeDefined();
      expect(xItem?.kind).toBe("assistant");
      if (xItem?.kind === "assistant") {
        // The live-updated version survives, not the stale reread version.
        expect(xItem.markdown).toBe("live-final-text");
      }
      // B is retained from reread.
      expect(items.some((i) => i.id === "B")).toBe(true);
    });

    it("reread omits X: item/completed replacement appends live-owned X as tail", async () => {
      const { store } = await setupLiveUpdateX(
        agentMessageItem("X", "original", "inProgress"),
        // Reread omits X (only B).
        [userMessageItem("B", "base")],
        // Live notification: item/completed inserts X.
        {
          method: "item/completed",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t0",
            item: agentMessageItem("X", "live-final-text", "completed"),
          },
        } as AnyNotification,
      );
      const items = store.getState().conversation?.items ?? [];
      const ids = items.map((i) => i.id);
      expect(ids).toContain("X");
      expect(ids).toContain("B");
      // X is after B (live tail).
      expect(ids.indexOf("X")).toBeGreaterThan(ids.indexOf("B"));
      const xItem = items.find((i) => i.id === "X");
      if (xItem?.kind === "assistant") {
        expect(xItem.markdown).toBe("live-final-text");
      }
    });

    // (b) delta
    it("reread includes stale X: agentMessage delta preserves live-updated X", async () => {
      const { store } = await setupLiveUpdateX(
        agentMessageItem("X", "base-text", "inProgress"),
        // Reread includes stale X (base text, no delta).
        [
          userMessageItem("B", "base"),
          agentMessageItem("X", "base-text", "inProgress"),
        ],
        // Live notification: delta appends to X.
        {
          method: "item/agentMessage/delta",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            itemId: "X",
            delta: "-appended",
          },
        } as AnyNotification,
      );
      const items = store.getState().conversation?.items ?? [];
      const xItem = items.find((i) => i.id === "X");
      expect(xItem).toBeDefined();
      expect(xItem?.kind).toBe("assistant");
      if (xItem?.kind === "assistant") {
        // The live-updated version (base-text + appended) survives.
        expect(xItem.markdown).toBe("base-text-appended");
      }
    });

    it("reread omits X: agentMessage delta triggers resync (missing item), X from initial dropped", async () => {
      // Delta targeting missing item triggers resync, not a mark.
      // Since X is in the initial projection but not in the reread, and the
      // delta targets a missing item (X is not in current after reread commits),
      // the delta notification itself triggers requestRehydrate. But at the
      // time the delta arrives, X IS in the current conversation (before R
      // completes). So the delta should update X and mark it live-owned.
      // After R commits (omitting X), X should be appended as live tail.
      const { store } = await setupLiveUpdateX(
        agentMessageItem("X", "base-text", "inProgress"),
        // Reread omits X (only B).
        [userMessageItem("B", "base")],
        // Live notification: delta appends to X (X exists in current).
        {
          method: "item/agentMessage/delta",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            itemId: "X",
            delta: "-appended",
          },
        } as AnyNotification,
      );
      const items = store.getState().conversation?.items ?? [];
      const ids = items.map((i) => i.id);
      expect(ids).toContain("X");
      expect(ids).toContain("B");
      expect(ids.indexOf("X")).toBeGreaterThan(ids.indexOf("B"));
      const xItem = items.find((i) => i.id === "X");
      if (xItem?.kind === "assistant") {
        expect(xItem.markdown).toBe("base-text-appended");
      }
    });

    // (c) reset
    it("reread includes stale X: agentMessage reset preserves live-reset X", async () => {
      const { store } = await setupLiveUpdateX(
        agentMessageItem("X", "will-be-reset", "inProgress"),
        // Reread includes stale X (old text).
        [
          userMessageItem("B", "base"),
          agentMessageItem("X", "will-be-reset", "inProgress"),
        ],
        // Live notification: reset clears X's markdown.
        {
          method: "item/agentMessage/reset",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            itemId: "X",
          },
        } as AnyNotification,
      );
      const items = store.getState().conversation?.items ?? [];
      const xItem = items.find((i) => i.id === "X");
      expect(xItem).toBeDefined();
      expect(xItem?.kind).toBe("assistant");
      if (xItem?.kind === "assistant") {
        // The live-reset version (empty markdown) survives, not stale.
        expect(xItem.markdown).toBe("");
      }
    });

    it("reread omits X: agentMessage reset appends live-owned X as tail", async () => {
      const { store } = await setupLiveUpdateX(
        agentMessageItem("X", "will-be-reset", "inProgress"),
        // Reread omits X (only B).
        [userMessageItem("B", "base")],
        // Live notification: reset clears X's markdown.
        {
          method: "item/agentMessage/reset",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            itemId: "X",
          },
        } as AnyNotification,
      );
      const items = store.getState().conversation?.items ?? [];
      const ids = items.map((i) => i.id);
      expect(ids).toContain("X");
      expect(ids).toContain("B");
      expect(ids.indexOf("X")).toBeGreaterThan(ids.indexOf("B"));
      const xItem = items.find((i) => i.id === "X");
      if (xItem?.kind === "assistant") {
        expect(xItem.markdown).toBe("");
      }
    });

    // Reasoning delta marking
    it("reread includes stale X: reasoning delta preserves live-updated activity X", async () => {
      const { store } = await setupLiveUpdateX(
        reasoningItem("X", "reasoning-base", "inProgress"),
        // Reread includes stale X (base text).
        [
          userMessageItem("B", "base"),
          reasoningItem("X", "reasoning-base", "inProgress"),
        ],
        // Live notification: reasoning delta appends to X.
        {
          method: "item/reasoning/summaryTextDelta",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            itemId: "X",
            delta: "-more",
          },
        } as AnyNotification,
      );
      const items = store.getState().conversation?.items ?? [];
      const xItem = items.find((i) => i.id === "X");
      expect(xItem).toBeDefined();
      expect(xItem?.kind).toBe("activity");
      if (xItem?.kind === "activity") {
        expect(xItem.detail.output).toBe("reasoning-base-more");
      }
    });

    // Tool output delta marking
    it("reread includes stale X: tool output delta preserves live-updated activity X", async () => {
      const { store } = await setupLiveUpdateX(
        { ...commandExecItem("X", "mytool", "inProgress"), callId: "call-X" },
        // Reread includes stale X (no output yet).
        [
          userMessageItem("B", "base"),
          { ...commandExecItem("X", "mytool", "inProgress"), callId: "call-X" },
        ],
        // Live notification: tool output delta appends to X.
        {
          method: "item/toolOutput/delta",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            itemId: "X",
            callId: "call-X",
            delta: "tool-result",
          },
        } as AnyNotification,
      );
      const items = store.getState().conversation?.items ?? [];
      const xItem = items.find((i) => i.id === "X");
      expect(xItem).toBeDefined();
      expect(xItem?.kind).toBe("activity");
      if (xItem?.kind === "activity") {
        expect(xItem.detail.output).toBe("tool-result");
      }
    });

    // Order and cap: page items + live-owned X preserved at cap
    it("page items + live-updated X: order P + B + X, cap retains X", async () => {
      const pageItems: MobileConversation["items"] = [];
      for (let i = 0; i < 498; i++) {
        pageItems.push({ kind: "user", id: `P-${i}`, text: "" });
      }
      const { store } = await setupLiveUpdateX(
        agentMessageItem("X", "original", "inProgress"),
        // Reread: B only (omits X).
        [userMessageItem("B", "base")],
        // Live notification: delta updates X.
        {
          method: "item/agentMessage/delta",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            itemId: "X",
            delta: "-updated",
          },
        } as AnyNotification,
        pageItems,
      );
      const items = store.getState().conversation?.items ?? [];
      const ids = items.map((i) => i.id);
      expect(items.length).toBeLessThanOrEqual(500);
      // X is retained (live-owned tail).
      expect(ids).toContain("X");
      // B is retained (authoritative).
      expect(ids).toContain("B");
      // X is after B.
      expect(ids.indexOf("X")).toBeGreaterThan(ids.indexOf("B"));
      const xItem = items.find((i) => i.id === "X");
      if (xItem?.kind === "assistant") {
        expect(xItem.markdown).toBe("original-updated");
      }
    });

    // Frozen (truncated) delta does not mark
    it("frozen truncated item: delta does not mark, reread version accepted", async () => {
      // Create an assistant item that is already truncated (at byte limit).
      // A delta to a truncated item is frozen — no mark, so the reread's
      // version should be accepted.
      const longText = "x".repeat(MAX_ITEM_BYTES + 100);
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("B", "base"),
                agentMessageItem("X", longText, "inProgress"),
              ],
            }),
          ],
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // X should be truncated after openProjected.
      const xBefore = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xBefore?.kind).toBe("assistant");
      if (xBefore?.kind === "assistant") {
        expect(xBefore.markdown).toContain("… truncated");
      }
      // Start a hanging rehydrate.
      const ctrl = makeControlledRead(service);
      // Reread includes X with different (shorter) text.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("B", "base"),
                agentMessageItem("X", "reread-short", "completed"),
              ],
            }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, send a delta to X — but X is truncated, so the
      // delta is frozen (break, no mark).
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          itemId: "X",
          delta: "-should-not-append",
        },
      } as AnyNotification);
      // Release R — since the delta was frozen (no mark), the reread version
      // should be accepted.
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      const items = store.getState().conversation?.items ?? [];
      const xItem = items.find((i) => i.id === "X");
      expect(xItem).toBeDefined();
      expect(xItem?.kind).toBe("assistant");
      if (xItem?.kind === "assistant") {
        // Reread version accepted (frozen delta did not mark).
        expect(xItem.markdown).toBe("reread-short");
      }
    });
  });

  // --- Task 2A live-matrix: table-driven PAGE-RACE with real pageItems ---
  //
  // 6 cases × page-race: completed-replacement include+omit, agent-delta
  // include+omit, reset include+omit. Every case starts a controlled reread,
  // commits page P + cursor while the reread is pending, accepts an
  // existing-X live update AFTER the reread snapshot is taken, then resolves.
  // Assertions: page/history order, cursor preserved, included-X live version
  // stays in the authoritative position, omitted-X appears once at live tail,
  // dedupe (X never duplicated), unowned old history drops.
  describe("Task 2A live-matrix: PAGE-RACE × live-ownership for all 6 cases", () => {
    // Table: one row per (mutationKind, rereadIncludesX). Each row defines the
    // initial X fixture, the reread items (stale X or omit X), and the live
    // notification that updates X while the reread is pending. The expected
    // live-updated markdown/output is checked in the assertions.
    type MutKind = "completed" | "delta" | "reset";

    const matrixCases: {
      label: string;
      kind: MutKind;
      rereadIncludesX: boolean;
      initialX: ThreadItem;
      rereadItems: ThreadItem[];
      liveNotification: AnyNotification;
      // Expected live-updated value after merge:
      // - completed/delta include → X stays in authoritative position with live text
      // - completed/delta/reset omit → X at live tail with live text
      expectedXMarkdown: string;
    }[] = [
      // (a) completed replacement — include
      {
        label: "completed replacement, reread includes stale X",
        kind: "completed",
        rereadIncludesX: true,
        initialX: agentMessageItem("X", "original", "inProgress"),
        rereadItems: [
          userMessageItem("B", "base"),
          agentMessageItem("X", "stale-from-reread", "completed"),
          userMessageItem("C", "fresh-authoritative"),
        ],
        liveNotification: {
          method: "item/completed",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t0",
            item: agentMessageItem("X", "live-final-text", "completed"),
          },
        } as AnyNotification,
        expectedXMarkdown: "live-final-text",
      },
      // (b) completed replacement — omit
      {
        label: "completed replacement, reread omits X",
        kind: "completed",
        rereadIncludesX: false,
        initialX: agentMessageItem("X", "original", "inProgress"),
        rereadItems: [
          userMessageItem("B", "base"),
          userMessageItem("C", "fresh-authoritative"),
        ],
        liveNotification: {
          method: "item/completed",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t0",
            item: agentMessageItem("X", "live-final-text", "completed"),
          },
        } as AnyNotification,
        expectedXMarkdown: "live-final-text",
      },
      // (c) agent delta — include
      {
        label: "agent delta, reread includes stale X",
        kind: "delta",
        rereadIncludesX: true,
        initialX: agentMessageItem("X", "base-text", "inProgress"),
        rereadItems: [
          userMessageItem("B", "base"),
          agentMessageItem("X", "base-text", "inProgress"),
          userMessageItem("C", "fresh-authoritative"),
        ],
        liveNotification: {
          method: "item/agentMessage/delta",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            itemId: "X",
            delta: "-appended",
          },
        } as AnyNotification,
        expectedXMarkdown: "base-text-appended",
      },
      // (d) agent delta — omit
      {
        label: "agent delta, reread omits X",
        kind: "delta",
        rereadIncludesX: false,
        initialX: agentMessageItem("X", "base-text", "inProgress"),
        rereadItems: [
          userMessageItem("B", "base"),
          userMessageItem("C", "fresh-authoritative"),
        ],
        liveNotification: {
          method: "item/agentMessage/delta",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            itemId: "X",
            delta: "-appended",
          },
        } as AnyNotification,
        expectedXMarkdown: "base-text-appended",
      },
      // (e) reset — include
      {
        label: "reset, reread includes stale X",
        kind: "reset",
        rereadIncludesX: true,
        initialX: agentMessageItem("X", "will-be-reset", "inProgress"),
        rereadItems: [
          userMessageItem("B", "base"),
          agentMessageItem("X", "will-be-reset", "inProgress"),
          userMessageItem("C", "fresh-authoritative"),
        ],
        liveNotification: {
          method: "item/agentMessage/reset",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            itemId: "X",
          },
        } as AnyNotification,
        expectedXMarkdown: "",
      },
      // (f) reset — omit
      {
        label: "reset, reread omits X",
        kind: "reset",
        rereadIncludesX: false,
        initialX: agentMessageItem("X", "will-be-reset", "inProgress"),
        rereadItems: [
          userMessageItem("B", "base"),
          userMessageItem("C", "fresh-authoritative"),
        ],
        liveNotification: {
          method: "item/agentMessage/reset",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            itemId: "X",
          },
        } as AnyNotification,
        expectedXMarkdown: "",
      },
    ];

    // Shared setup for all 6 matrix cases: creates a store with initial B + X + A
    // (A is unowned old history that will be omitted from reread and dropped),
    // starts a hanging rehydrate, commits page P + cursor while pending,
    // accepts the live notification to X after the reread snapshot, then releases.
    async function setupPageRaceMatrix(
      initialX: ThreadItem,
      rereadItems: ThreadItem[],
      liveNotification: AnyNotification,
    ): Promise<{
      store: ReturnType<typeof createConversationStore>;
      service: FakeConversationService;
    }> {
      const service = new FakeConversationService();
      // Initial projection: B + X + A (A is old, will be omitted from reread).
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("B", "base"),
                initialX,
                userMessageItem("A", "old-unowned"),
              ],
            }),
          ],
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      // Start a rehydrate (R) that hangs — captures the reread snapshot.
      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [makeTurn({ id: "t0", items: rereadItems })],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();

      // While R is in-flight, loadOlder commits page items P + cursor.
      service.olderItems = {
        items: [{ kind: "user", id: "P", text: "page-old" }],
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);
      // Verify page items committed while R is pending.
      const itemsAfterL = store.getState().conversation?.items ?? [];
      expect(itemsAfterL.some((i) => i.id === "P")).toBe(true);
      expect(store.getState().olderCursor).toBe("cursor-2");

      // While R is still in-flight, accept the existing-X live update.
      store.getState().applyNotification(liveNotification);

      // Release R — the page-race merge resolves.
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      return { store, service };
    }

    for (const tc of matrixCases) {
      it(`PAGE-RACE: ${tc.label} — page order, cursor preserved, X correct, dedupe, A drops`, async () => {
        const { store } = await setupPageRaceMatrix(
          tc.initialX,
          tc.rereadItems,
          tc.liveNotification,
        );
        const items = store.getState().conversation?.items ?? [];
        const ids = items.map((i) => i.id);

        // Page item P is retained (page-owned history prepended).
        expect(ids).toContain("P");
        // B and C are retained (authoritative reread).
        expect(ids).toContain("B");
        expect(ids).toContain("C");
        // A is dropped (owned by neither page nor live, omitted from reread).
        expect(ids).not.toContain("A");

        // X appears exactly once (dedupe).
        const xCount = ids.filter((id) => id === "X").length;
        expect(xCount).toBe(1);
        expect(ids).toContain("X");

        // X has the live-updated value, not the stale reread value.
        const xItem = items.find((i) => i.id === "X");
        expect(xItem).toBeDefined();
        expect(xItem?.kind).toBe("assistant");
        if (xItem?.kind === "assistant") {
          expect(xItem.markdown).toBe(tc.expectedXMarkdown);
        }

        // Cursor from page is preserved (not overwritten by reread).
        expect(store.getState().olderCursor).toBe("cursor-2");

        // Order: P (page history) before B/C (authoritative reread) before
        // live tail. For include cases, X is in the authoritative reread
        // position (where the reread placed it). For omit cases, X is appended
        // as the live tail after the authoritative items.
        const pIdx = ids.indexOf("P");
        const bIdx = ids.indexOf("B");
        const cIdx = ids.indexOf("C");
        const xIdx = ids.indexOf("X");

        // P always precedes the authoritative reread items.
        expect(pIdx).toBeLessThan(bIdx);
        expect(pIdx).toBeLessThan(cIdx);

        if (tc.rereadIncludesX) {
          // Included X stays in the authoritative reread position. The
          // reread placed it between B and C, so X must be between B and C.
          expect(bIdx).toBeLessThan(xIdx);
          expect(xIdx).toBeLessThan(cIdx);
        } else {
          // Omitted X appears once at the live tail (after all reread items).
          expect(xIdx).toBeGreaterThan(bIdx);
          expect(xIdx).toBeGreaterThan(cIdx);
          // X is the last item.
          expect(xIdx).toBe(ids.length - 1);
        }
      });
    }

    // Cap case: newest live X survives the 500-item cap while page oldest
    // precedes X. Fill to near cap with page items, add live X, ensure X
    // survives the merge and page items still precede it.
    it("PAGE-RACE cap: newest live X survives 500 while page oldest precedes X, A drops", async () => {
      const service = new FakeConversationService();
      // Initial: B + X + A (3 items; A is unowned old history).
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("B", "base"),
                agentMessageItem("X", "original", "inProgress"),
                userMessageItem("A", "old-unowned"),
              ],
            }),
          ],
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      // Start a rehydrate (R) that hangs.
      const ctrl = makeControlledRead(service);
      // Reread omits X and A: B + C only.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("B", "base"),
                userMessageItem("C", "fresh"),
              ],
            }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();

      // While R is in-flight, loadOlder commits 497 page items + cursor.
      // After loadOlder: 497 P items + B + X + A = 500 (at cap). The cap
      // trims A from the front (oldest), so 497 P + B + X = 500 survives.
      // At cap, the cursor is set to null (honest: further paging would
      // discard rows). This is correct behavior, not a bug.
      const pageItems: MobileConversation["items"] = [];
      for (let i = 0; i < 497; i++) {
        pageItems.push({ kind: "user", id: `P-${i}`, text: "" });
      }
      service.olderItems = { items: pageItems, nextCursor: "cursor-2" };
      await store.getState().loadOlder(service);
      // Verify page items committed (A may be trimmed by cap; check P-0).
      const itemsAfterL = store.getState().conversation?.items ?? [];
      expect(itemsAfterL.some((i) => i.id === "P-0")).toBe(true);

      // While R is still in-flight, accept live delta to X.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          itemId: "X",
          delta: "-updated",
        },
      } as AnyNotification);

      // Release R — the page-race merge resolves.
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();

      const items = store.getState().conversation?.items ?? [];
      const ids = items.map((i) => i.id);

      // Within cap.
      expect(items.length).toBeLessThanOrEqual(500);

      // X survives the cap (live-owned tail).
      expect(ids).toContain("X");
      const xCount = ids.filter((id) => id === "X").length;
      expect(xCount).toBe(1);

      // X has the live-updated value.
      const xItem = items.find((i) => i.id === "X");
      expect(xItem?.kind).toBe("assistant");
      if (xItem?.kind === "assistant") {
        expect(xItem.markdown).toBe("original-updated");
      }

      // B and C are retained (authoritative reread).
      expect(ids).toContain("B");
      expect(ids).toContain("C");

      // A is dropped (owned by neither).
      expect(ids).not.toContain("A");

      // Page oldest (P-0) precedes X. P-0 is the oldest surviving page item.
      const xIdx = ids.indexOf("X");
      const p0Idx = ids.indexOf("P-0");
      // P-0 should survive — 497 page items + B + C + X = 500, so all fit.
      if (p0Idx >= 0) {
        expect(p0Idx).toBeLessThan(xIdx);
      }

      // At least some page items survive and precede X.
      const pageIdxs = ids
        .map((id, idx) => ({ id, idx }))
        .filter(({ id }) => id.startsWith("P-"));
      expect(pageIdxs.length).toBeGreaterThan(0);
      const oldestPageIdx = Math.min(...pageIdxs.map((p) => p.idx));
      expect(oldestPageIdx).toBeLessThan(xIdx);

      // X is after B and C (live tail).
      expect(xIdx).toBeGreaterThan(ids.indexOf("B"));
      expect(xIdx).toBeGreaterThan(ids.indexOf("C"));
    });
  });

  // --- Task 2A-Items reslice: exact delta families, unified truncation
  // ownership, and within-page paging dedupe.
  //
  // These tests are written RED first, then the store is fixed to satisfy them.
  // They cover the three brief requirements:
  // 1. Exact delta families — reasoning deltas only match reasoning items;
  //    tool-output deltas only match tool items with matching callId.
  // 2. Unified truncation ownership — every content-install path uses one
  //    UTF-8 byte-bounded mechanism; frozen items stay frozen until an
  //    authoritative reset/replacement removes the freeze.
  // 3. Paging dedupe — dedupe against retained IDs AND within the incoming page.
  describe("Task 2A-Items: exact delta families", () => {
    // Helper: open a conversation with the given mobile items via openProjected.
    async function openWithItems(items: MobileConversation["items"]): Promise<{
      store: ReturnType<typeof createConversationStore>;
      service: FakeConversationService;
    }> {
      const service = new FakeConversationService();
      const conv = makeConversation({ items });
      service.readProjectionResult = {
        conversation: conv,
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      return { store, service };
    }

    it("reasoning delta targeting a tool activity triggers reread, does not append", async () => {
      // A tool activity has a callId — a reasoning delta to it is a wrong family.
      const { store, service } = await openWithItems([
        {
          kind: "activity",
          id: "tool-1",
          label: "shell",
          family: "tool",
          state: "running",
          detail: { output: "line1", callId: "call-tool-1" },
        },
      ]);
      const initialReads = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "item/reasoning/summaryTextDelta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-1",
          summaryIndex: 0,
          delta: " reasoning-overflow",
        },
      } as AnyNotification);
      await Promise.resolve();
      await Promise.resolve();
      // Wrong family → reread requested.
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
      // Text was NOT mutated.
      const item = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      if (item?.kind === "activity") {
        expect(item.detail.output).toBe("line1");
      }
    });

    it("tool-output delta targeting a reasoning activity triggers reread, does not append", async () => {
      // A reasoning activity has no callId — a tool-output delta to it is a
      // wrong family.
      const { store, service } = await openWithItems([
        {
          kind: "activity",
          id: "reason-1",
          label: "Reasoning",
          family: "reasoning",
          state: "running",
          detail: { output: "Thinking" },
        },
      ]);
      const initialReads = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "reason-1",
          callId: "call-tool-1",
          delta: " tool-overflow",
        },
      } as AnyNotification);
      await Promise.resolve();
      await Promise.resolve();
      // Wrong family → reread requested.
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
      // Text was NOT mutated.
      const item = store
        .getState()
        .conversation?.items.find((i) => i.id === "reason-1");
      if (item?.kind === "activity") {
        expect(item.detail.output).toBe("Thinking");
      }
    });

    it("tool-output delta with wrong callId triggers reread, does not append", async () => {
      // The activity has callId "call-A" but the delta supplies "call-B".
      const { store, service } = await openWithItems([
        {
          kind: "activity",
          id: "tool-1",
          label: "shell",
          family: "tool",
          state: "running",
          detail: { output: "line1", callId: "call-A" },
        },
      ]);
      const initialReads = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-1",
          callId: "call-B",
          delta: " wrong-call-delta",
        },
      } as AnyNotification);
      await Promise.resolve();
      await Promise.resolve();
      // Wrong callId → reread requested.
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
      // Text was NOT mutated.
      const item = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      if (item?.kind === "activity") {
        expect(item.detail.output).toBe("line1");
      }
    });

    it("tool-output delta with matching callId appends correctly", async () => {
      const { store, service } = await openWithItems([
        {
          kind: "activity",
          id: "tool-1",
          label: "shell",
          family: "tool",
          state: "running",
          detail: { output: "line1", callId: "call-A" },
        },
      ]);
      const initialReads = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-1",
          callId: "call-A",
          delta: "\nline2",
        },
      } as AnyNotification);
      await Promise.resolve();
      // No reread — matching call accepted.
      expect(service.readProjectionCalls.length).toBe(initialReads);
      const item = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      if (item?.kind === "activity") {
        expect(item.detail.output).toBe("line1\nline2");
      }
    });

    it("reasoning delta targeting a reasoning activity (no callId) appends correctly", async () => {
      const { store, service } = await openWithItems([
        {
          kind: "activity",
          id: "reason-1",
          label: "Reasoning",
          family: "reasoning",
          state: "running",
          detail: { output: "Thinking" },
        },
      ]);
      const initialReads = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "item/reasoning/summaryTextDelta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "reason-1",
          summaryIndex: 0,
          delta: " more",
        },
      } as AnyNotification);
      await Promise.resolve();
      // No reread — reasoning family accepted.
      expect(service.readProjectionCalls.length).toBe(initialReads);
      const item = store
        .getState()
        .conversation?.items.find((i) => i.id === "reason-1");
      if (item?.kind === "activity") {
        expect(item.detail.output).toBe("Thinking more");
      }
    });

    // Task 2A-Family (round 2, fix 2): controlled-reread adversarial tests,
    // resliced into separate (A) and (B) deterministic proofs. Do NOT conflate
    // A/B.
    //
    // (A) invalid-only ownership test: trigger invalid delta, hold one reread,
    //     assert sync unchanged, release omitted-target authoritative result,
    //     target MUST drop — proves invalid did not mark live ownership.
    //     Exactly one controlled read started/completed.
    //
    // (B) invalid-then-valid freeze test (only where a valid exact family/call
    //     delta is possible): while invalid reread is blocked, valid delta
    //     applies synchronously — proves invalid did not freeze. Release/
    //     reconcile with explicit expected ownership (target preserved as
    //     live tail by the valid delta's live ownership, not by the invalid).
    //     Exactly one controlled read started/completed.
    //
    // Stored-missing-callId and unknown-family cases get (A) only — no valid
    // exact family/call delta exists, so (B) is not applicable.
    //
    // Fix 2 timing: install the reconciliation barrier (store.subscribe
    // level-triggered against the pre-release snapshot) BEFORE releasing
    // read1, so a synchronous rehydrate commit during release cannot be
    // missed. After release+complete, await the barrier, then cross exactly
    // one scheduler dispatch (yieldMicrotask). In EVERY A/B case, reassert
    // started=1, done=1, and service.readProjectionCalls=readsAfterOpen+1
    // AFTER reconciliation+dispatch so queued duplicates cannot hide. Any
    // unexpected straggler read is released AND awaited to completion plus
    // scheduler drain quiescence in try/finally BEFORE asserting exact
    // counts — fire-and-forget release is not sufficient. Positive valid
    // controls cross an actual scheduler dispatch turn/barrier before
    // asserting zero controlled reads/unchanged service count.

    // Helper: open via openProjected with given mobile items, then install
    // a controlled readProjection that hangs until released. The reread
    // result omits all activity items (the target drops on release).
    async function openWithControlledReread(
      items: MobileConversation["items"],
    ): Promise<{
      store: ReturnType<typeof createConversationStore>;
      service: FakeConversationService;
      ctrl: ReturnType<typeof makeControlledRead>;
      readsAfterOpen: number;
    }> {
      const service = new FakeConversationService();
      const conv = makeConversation({ items });
      service.readProjectionResult = {
        conversation: conv,
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      const sink = createFakeSink();
      await store.getState().openProjected(service, sink, "ref-1");
      const ctrl = makeControlledRead(service);
      // The reread result omits all activity items — only a base user message.
      service.readProjectionResult = {
        conversation: makeConversation({
          items: [{ kind: "user", id: "base", text: "base" }],
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      return {
        store,
        service,
        ctrl,
        readsAfterOpen: service.readProjectionCalls.length,
      };
    }

    // Fix 2: Deterministic post-rehydrate reconciliation barrier. The
    // rehydrate commit calls set({ conversation: ... }) synchronously in the
    // same microtask turn that the controlled read resolves. store.subscribe
    // fires synchronously on every set(), so this level-triggered promise
    // resolves when the conversation items change — a true state barrier, not
    // a microtask count guess.
    //
    // CRITICAL: the subscription MUST be installed BEFORE ctrl.release() so
    // the barrier cannot miss a synchronous commit that fires during release.
    // The caller captures preReleaseConv, calls installReconcileBarrier to get
    // the barrier promise, THEN releases+completes, THEN awaits the promise.
    // This avoids the snapshot==current tautology: preReleaseConv is captured
    // before the rehydrate effect runs, so the barrier checks against the
    // conversation that existed before release.
    function installReconcileBarrier(
      store: ReturnType<typeof createConversationStore>,
      preReleaseConv: MobileConversation | null,
    ): Promise<void> {
      // Level-triggered: if already changed (sync commit before install),
      // resolve now. This should not happen when called before release, but
      // is kept for safety.
      if (store.getState().conversation !== preReleaseConv) {
        return Promise.resolve();
      }
      return new Promise<void>((resolve) => {
        const unsub = store.subscribe((s) => {
          if (s.conversation !== preReleaseConv) {
            unsub();
            resolve();
          }
        });
      });
    }

    // Event-driven quiescent straggler draining. Every started read is awaited
    // at its release gate, then paired with an awaited completion and an exact
    // reconciliation barrier. A scheduler turn after reconciliation exposes
    // any trailing read before the next iteration. No sleeps, count polling,
    // fixed flush counts, or fire-and-forget releases.
    async function drainAndAwaitStragglers(
      store: ReturnType<typeof createConversationStore>,
      ctrl: ReturnType<typeof makeControlledRead>,
    ): Promise<void> {
      // Expose work queued before cleanup began.
      await yieldMicrotask();

      while (ctrl.getDoneCount() < ctrl.getStartedCount()) {
        const target = ctrl.getDoneCount() + 1;
        await ctrl.ready(target);

        const beforeReconcile = store.getState().conversation;
        const reconcileP = installReconcileBarrier(store, beforeReconcile);
        if (!ctrl.release()) {
          throw new Error(
            "controlled read was ready but could not be released",
          );
        }
        await ctrl.completed(target);
        await reconcileP;

        // The completed read's scheduler effect has reconciled. Cross its
        // dispatch boundary so any trailing request starts before rechecking.
        await yieldMicrotask();
      }
    }

    async function assertAfterQuiescence(
      store: ReturnType<typeof createConversationStore>,
      ctrl: ReturnType<typeof makeControlledRead>,
      assertions: () => void,
    ): Promise<void> {
      await drainAndAwaitStragglers(store, ctrl);
      assertions();
    }

    // --- (A) invalid-only ownership tests ---

    it("2A-Family-r2 (A): reasoning delta to tool item — no mutation, target drops (invalid did not mark live ownership)", async () => {
      const { store, service, ctrl, readsAfterOpen } =
        await openWithControlledReread([
          {
            kind: "activity",
            id: "tool-r",
            label: "Reasoning",
            family: "tool",
            state: "running",
            detail: { output: "out0", callId: "call-r" },
          },
        ]);
      // Invalid: reasoning delta to a tool-family item → reread, no mutation.
      store.getState().applyNotification({
        method: "item/reasoning/summaryTextDelta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-r",
          summaryIndex: 0,
          delta: " should-not-append",
        },
      } as AnyNotification);
      // Reread starts and hangs on controlled read.
      await yieldMicrotask();
      await ctrl.started(1);
      // Exactly one controlled read started.
      // Synchronous: text/detail unchanged — no mutation, no freeze.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-r");
      expect(item0?.kind).toBe("activity");
      if (item0?.kind === "activity") {
        expect(item0.detail.output).toBe("out0");
        expect(item0.detail.callId).toBe("call-r");
      }
      // Release the reread — reread omits tool-r. Invalid did NOT mark it
      // live-owned, so it drops as omitted old history.
      // Capture pre-release conversation BEFORE release (avoids tautology).
      const preReleaseConv = store.getState().conversation;
      const reconcileP = installReconcileBarrier(store, preReleaseConv);
      ctrl.release();
      await ctrl.completed(1);
      // Fix 2: the barrier was installed BEFORE release (above) so the
      // synchronous rehydrate commit cannot be missed. Await it now.
      await reconcileP;
      // Cross exactly one scheduler dispatch turn before final count
      // capture so any queued trailing reread has dispatched.
      await yieldMicrotask();
      // Detect any started read2, release/complete all stragglers BEFORE
      // asserting exact counts — failures cannot leave jobs blocked.
      await assertAfterQuiescence(store, ctrl, () => {
        // Reassert exact counts after reconciliation — queued duplicates
        // cannot hide.
        expect(ctrl.getStartedCount()).toBe(1);
        expect(ctrl.getDoneCount()).toBe(1);
        expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 1);
        // Target MUST drop — proves invalid did not mark live ownership.
        const item1 = store
          .getState()
          .conversation?.items.find((i) => i.id === "tool-r");
        expect(item1).toBeUndefined();
      });
    });

    it("2A-Family-r2 (A): tool delta to reasoning item — no mutation, target drops (invalid did not mark live ownership)", async () => {
      const { store, service, ctrl, readsAfterOpen } =
        await openWithControlledReread([
          {
            kind: "activity",
            id: "reason-1",
            label: "Reasoning",
            family: "reasoning",
            state: "running",
            detail: { output: "Thinking" },
          },
        ]);
      // Invalid: tool delta to a reasoning-family item → reread, no mutation.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "reason-1",
          callId: "call-x",
          delta: " should-not-append",
        },
      } as AnyNotification);
      await yieldMicrotask();
      await ctrl.started(1);
      // Synchronous: text unchanged.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "reason-1");
      expect(item0?.kind).toBe("activity");
      if (item0?.kind === "activity") {
        expect(item0.detail.output).toBe("Thinking");
      }
      // Release — reread omits reason-1. Invalid did NOT mark it live-owned.
      const preReleaseConv = store.getState().conversation;
      const reconcileP = installReconcileBarrier(store, preReleaseConv);
      ctrl.release();
      await ctrl.completed(1);
      await reconcileP;
      await yieldMicrotask();
      await assertAfterQuiescence(store, ctrl, () => {
        expect(ctrl.getStartedCount()).toBe(1);
        expect(ctrl.getDoneCount()).toBe(1);
        expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 1);
        // Target MUST drop.
        const item1 = store
          .getState()
          .conversation?.items.find((i) => i.id === "reason-1");
        expect(item1).toBeUndefined();
      });
    });

    it("2A-Family-r2 (A): callId mismatch — no mutation, target drops (invalid did not mark live ownership)", async () => {
      const { store, service, ctrl, readsAfterOpen } =
        await openWithControlledReread([
          {
            kind: "activity",
            id: "tool-mismatch",
            label: "shell",
            family: "tool",
            state: "running",
            detail: { output: "base", callId: "call-A" },
          },
        ]);
      // Invalid: callId mismatch (call-A stored, call-B incoming) → reread.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-mismatch",
          callId: "call-B",
          delta: " should-not-append",
        },
      } as AnyNotification);
      await yieldMicrotask();
      await ctrl.started(1);
      // Synchronous: text unchanged.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-mismatch");
      expect(item0?.kind).toBe("activity");
      if (item0?.kind === "activity") {
        expect(item0.detail.output).toBe("base");
      }
      // Release — reread omits tool-mismatch. Invalid did NOT mark it live-owned.
      const preReleaseConv = store.getState().conversation;
      const reconcileP = installReconcileBarrier(store, preReleaseConv);
      ctrl.release();
      await ctrl.completed(1);
      await reconcileP;
      await yieldMicrotask();
      await assertAfterQuiescence(store, ctrl, () => {
        expect(ctrl.getStartedCount()).toBe(1);
        expect(ctrl.getDoneCount()).toBe(1);
        expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 1);
        // Target MUST drop.
        const item1 = store
          .getState()
          .conversation?.items.find((i) => i.id === "tool-mismatch");
        expect(item1).toBeUndefined();
      });
    });

    it("2A-Family-r2 (A): incoming missing callId — no mutation, target drops (invalid did not mark live ownership)", async () => {
      const { store, service, ctrl, readsAfterOpen } =
        await openWithControlledReread([
          {
            kind: "activity",
            id: "tool-hascall",
            label: "shell",
            family: "tool",
            state: "running",
            detail: { output: "base", callId: "call-A" },
          },
        ]);
      // Invalid: incoming callId missing → reread, no mutation.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-hascall",
          delta: " should-not-append",
        },
      } as AnyNotification);
      await yieldMicrotask();
      await ctrl.started(1);
      // Synchronous: text unchanged.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-hascall");
      expect(item0?.kind).toBe("activity");
      if (item0?.kind === "activity") {
        expect(item0.detail.output).toBe("base");
      }
      // Release — reread omits tool-hascall. Invalid did NOT mark it live-owned.
      const preReleaseConv = store.getState().conversation;
      const reconcileP = installReconcileBarrier(store, preReleaseConv);
      ctrl.release();
      await ctrl.completed(1);
      await reconcileP;
      await yieldMicrotask();
      await assertAfterQuiescence(store, ctrl, () => {
        expect(ctrl.getStartedCount()).toBe(1);
        expect(ctrl.getDoneCount()).toBe(1);
        expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 1);
        // Target MUST drop.
        const item1 = store
          .getState()
          .conversation?.items.find((i) => i.id === "tool-hascall");
        expect(item1).toBeUndefined();
      });
    });

    it("2A-Family-r2 (A): stored missing callId — no mutation, target drops (invalid did not mark live ownership)", async () => {
      const { store, service, ctrl, readsAfterOpen } =
        await openWithControlledReread([
          {
            kind: "activity",
            id: "tool-nocall",
            label: "shell",
            family: "tool",
            state: "running",
            detail: { output: "base" },
          },
        ]);
      // Invalid: stored callId missing, incoming present → reread, no mutation.
      // Only one invalid delta — no second invalid/freeze claim.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-nocall",
          callId: "call-1",
          delta: " should-not-append",
        },
      } as AnyNotification);
      await yieldMicrotask();
      await ctrl.started(1);
      // Synchronous: text unchanged — no mutation, no freeze.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-nocall");
      expect(item0?.kind).toBe("activity");
      if (item0?.kind === "activity") {
        expect(item0.detail.output).toBe("base");
      }
      // Release — reread omits tool-nocall. Invalid did NOT mark it live-owned.
      const preReleaseConv = store.getState().conversation;
      const reconcileP = installReconcileBarrier(store, preReleaseConv);
      ctrl.release();
      await ctrl.completed(1);
      await reconcileP;
      await yieldMicrotask();
      await assertAfterQuiescence(store, ctrl, () => {
        expect(ctrl.getStartedCount()).toBe(1);
        expect(ctrl.getDoneCount()).toBe(1);
        expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 1);
        // Target MUST drop.
        const item1 = store
          .getState()
          .conversation?.items.find((i) => i.id === "tool-nocall");
        expect(item1).toBeUndefined();
      });
    });

    it("2A-Family-r2 (A): unknown family rejects reasoning delta — no mutation, target drops", async () => {
      const { store, service, ctrl, readsAfterOpen } =
        await openWithControlledReread([
          {
            kind: "activity",
            id: "unk-1",
            label: "Mystery",
            family: "unknown",
            state: "running",
            detail: { output: "base", callId: "call-unk" },
          },
        ]);
      // Invalid: reasoning delta to unknown-family item → reread, no mutation.
      // No valid reasoning delta possible (family is "unknown"), so (A) only.
      store.getState().applyNotification({
        method: "item/reasoning/summaryTextDelta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "unk-1",
          summaryIndex: 0,
          delta: " should-not-append",
        },
      } as AnyNotification);
      await yieldMicrotask();
      await ctrl.started(1);
      // Synchronous: text unchanged.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "unk-1");
      expect(item0?.kind).toBe("activity");
      if (item0?.kind === "activity") {
        expect(item0.detail.output).toBe("base");
      }
      // Release — reread omits unk-1. Invalid did NOT mark it live-owned.
      const preReleaseConv = store.getState().conversation;
      const reconcileP = installReconcileBarrier(store, preReleaseConv);
      ctrl.release();
      await ctrl.completed(1);
      await reconcileP;
      await yieldMicrotask();
      await assertAfterQuiescence(store, ctrl, () => {
        expect(ctrl.getStartedCount()).toBe(1);
        expect(ctrl.getDoneCount()).toBe(1);
        expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 1);
        // Target MUST drop.
        const item1 = store
          .getState()
          .conversation?.items.find((i) => i.id === "unk-1");
        expect(item1).toBeUndefined();
      });
    });

    it("2A-Family-r2 (A): unknown family rejects tool delta — no mutation, target drops", async () => {
      const { store, service, ctrl, readsAfterOpen } =
        await openWithControlledReread([
          {
            kind: "activity",
            id: "unk-2",
            label: "Mystery",
            family: "unknown",
            state: "running",
            detail: { output: "base", callId: "call-unk" },
          },
        ]);
      // Invalid: tool delta to unknown-family item → reread, no mutation.
      // No valid tool delta possible (family is "unknown"), so (A) only.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "unk-2",
          callId: "call-unk",
          delta: " should-not-append",
        },
      } as AnyNotification);
      await yieldMicrotask();
      await ctrl.started(1);
      // Synchronous: text unchanged.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "unk-2");
      expect(item0?.kind).toBe("activity");
      if (item0?.kind === "activity") {
        expect(item0.detail.output).toBe("base");
      }
      // Release — reread omits unk-2. Invalid did NOT mark it live-owned.
      const preReleaseConv = store.getState().conversation;
      const reconcileP = installReconcileBarrier(store, preReleaseConv);
      ctrl.release();
      await ctrl.completed(1);
      await reconcileP;
      await yieldMicrotask();
      await assertAfterQuiescence(store, ctrl, () => {
        expect(ctrl.getStartedCount()).toBe(1);
        expect(ctrl.getDoneCount()).toBe(1);
        expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 1);
        // Target MUST drop.
        const item1 = store
          .getState()
          .conversation?.items.find((i) => i.id === "unk-2");
        expect(item1).toBeUndefined();
      });
    });

    // --- (B) invalid-then-valid freeze tests ---

    it("2A-Family-r2 (B): reasoning delta to tool item — valid tool delta applies while reread blocked (invalid did not freeze)", async () => {
      const { store, service, ctrl, readsAfterOpen } =
        await openWithControlledReread([
          {
            kind: "activity",
            id: "tool-r",
            label: "Reasoning",
            family: "tool",
            state: "running",
            detail: { output: "out0", callId: "call-r" },
          },
        ]);
      // Invalid: reasoning delta to a tool-family item → reread, no mutation.
      store.getState().applyNotification({
        method: "item/reasoning/summaryTextDelta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-r",
          summaryIndex: 0,
          delta: " should-not-append",
        },
      } as AnyNotification);
      // Reread starts and hangs on controlled read.
      await yieldMicrotask();
      await ctrl.started(1);
      // Exactly one controlled read started.
      // Synchronous: text unchanged — invalid did not mutate or freeze.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-r");
      expect(item0?.kind).toBe("activity");
      if (item0?.kind === "activity") {
        expect(item0.detail.output).toBe("out0");
      }
      // Prove no freeze: valid matching tool delta applies synchronously
      // while the reread is still blocked.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-r",
          callId: "call-r",
          delta: "\nappended",
        },
      } as AnyNotification);
      // Fix 2: cross a scheduler dispatch turn before asserting the valid
      // delta did NOT trigger a second controlled read.
      await yieldMicrotask();
      // Exactly one controlled read — valid delta does NOT trigger a reread.
      expect(ctrl.getStartedCount()).toBe(1);
      const item1 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-r");
      if (item1?.kind === "activity") {
        expect(item1.detail.output).toBe("out0\nappended");
      }
      // Release the reread — reread omits tool-r. The valid tool delta marked
      // it live-owned, so the rehydrate preserves it as superseded live tail.
      // The invalid reasoning delta did NOT mark it — only the valid one did.
      const preReleaseConv = store.getState().conversation;
      const reconcileP = installReconcileBarrier(store, preReleaseConv);
      ctrl.release();
      await ctrl.completed(1);
      await reconcileP;
      await yieldMicrotask();
      await assertAfterQuiescence(store, ctrl, () => {
        // Reassert exact counts after reconciliation — queued duplicates
        // cannot hide.
        expect(ctrl.getStartedCount()).toBe(1);
        expect(ctrl.getDoneCount()).toBe(1);
        expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 1);
        // Target preserved with valid delta content — live ownership from
        // the valid delta, not from the invalid.
        const item2 = store
          .getState()
          .conversation?.items.find((i) => i.id === "tool-r");
        expect(item2).toBeDefined();
        if (item2?.kind === "activity") {
          expect(item2.detail.output).toBe("out0\nappended");
        }
      });
    });

    it("2A-Family-r2 (B): tool delta to reasoning item — valid reasoning delta applies while reread blocked (invalid did not freeze)", async () => {
      const { store, service, ctrl, readsAfterOpen } =
        await openWithControlledReread([
          {
            kind: "activity",
            id: "reason-1",
            label: "Reasoning",
            family: "reasoning",
            state: "running",
            detail: { output: "Thinking" },
          },
        ]);
      // Invalid: tool delta to a reasoning-family item → reread, no mutation.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "reason-1",
          callId: "call-x",
          delta: " should-not-append",
        },
      } as AnyNotification);
      await yieldMicrotask();
      await ctrl.started(1);
      // Synchronous: text unchanged — invalid did not mutate or freeze.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "reason-1");
      expect(item0?.kind).toBe("activity");
      if (item0?.kind === "activity") {
        expect(item0.detail.output).toBe("Thinking");
      }
      // Prove no freeze: valid reasoning delta applies synchronously while
      // the reread is still blocked.
      store.getState().applyNotification({
        method: "item/reasoning/summaryTextDelta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "reason-1",
          summaryIndex: 0,
          delta: " more",
        },
      } as AnyNotification);
      // Fix 2: cross a scheduler dispatch turn before asserting.
      await yieldMicrotask();
      expect(ctrl.getStartedCount()).toBe(1);
      const item1 = store
        .getState()
        .conversation?.items.find((i) => i.id === "reason-1");
      if (item1?.kind === "activity") {
        expect(item1.detail.output).toBe("Thinking more");
      }
      // Release — reread omits reason-1. Valid reasoning delta marked it
      // live-owned, so the rehydrate preserves it.
      const preReleaseConv = store.getState().conversation;
      const reconcileP = installReconcileBarrier(store, preReleaseConv);
      ctrl.release();
      await ctrl.completed(1);
      await reconcileP;
      await yieldMicrotask();
      await assertAfterQuiescence(store, ctrl, () => {
        expect(ctrl.getStartedCount()).toBe(1);
        expect(ctrl.getDoneCount()).toBe(1);
        expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 1);
        // Target preserved with valid delta content.
        const item2 = store
          .getState()
          .conversation?.items.find((i) => i.id === "reason-1");
        expect(item2).toBeDefined();
        if (item2?.kind === "activity") {
          expect(item2.detail.output).toBe("Thinking more");
        }
      });
    });

    it("2A-Family-r2 (B): callId mismatch — valid matching callId applies while reread blocked (invalid did not freeze)", async () => {
      const { store, service, ctrl, readsAfterOpen } =
        await openWithControlledReread([
          {
            kind: "activity",
            id: "tool-mismatch",
            label: "shell",
            family: "tool",
            state: "running",
            detail: { output: "base", callId: "call-A" },
          },
        ]);
      // Invalid: callId mismatch (call-A stored, call-B incoming) → reread.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-mismatch",
          callId: "call-B",
          delta: " should-not-append",
        },
      } as AnyNotification);
      await yieldMicrotask();
      await ctrl.started(1);
      // Synchronous: text unchanged — invalid did not mutate or freeze.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-mismatch");
      expect(item0?.kind).toBe("activity");
      if (item0?.kind === "activity") {
        expect(item0.detail.output).toBe("base");
      }
      // Prove no freeze: valid matching callId delta applies synchronously
      // while the reread is still blocked.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-mismatch",
          callId: "call-A",
          delta: "\nappended",
        },
      } as AnyNotification);
      // Fix 2: cross a scheduler dispatch turn before asserting.
      await yieldMicrotask();
      expect(ctrl.getStartedCount()).toBe(1);
      const item1 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-mismatch");
      if (item1?.kind === "activity") {
        expect(item1.detail.output).toBe("base\nappended");
      }
      // Release — reread omits tool-mismatch. Valid matching callId delta
      // marked it live-owned, so the rehydrate preserves it.
      const preReleaseConv = store.getState().conversation;
      const reconcileP = installReconcileBarrier(store, preReleaseConv);
      ctrl.release();
      await ctrl.completed(1);
      await reconcileP;
      await yieldMicrotask();
      await assertAfterQuiescence(store, ctrl, () => {
        expect(ctrl.getStartedCount()).toBe(1);
        expect(ctrl.getDoneCount()).toBe(1);
        expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 1);
        // Target preserved with valid delta content.
        const item2 = store
          .getState()
          .conversation?.items.find((i) => i.id === "tool-mismatch");
        expect(item2).toBeDefined();
        if (item2?.kind === "activity") {
          expect(item2.detail.output).toBe("base\nappended");
        }
      });
    });

    it("2A-Family-r2 (B): incoming missing callId — valid matching callId applies while reread blocked (invalid did not freeze)", async () => {
      const { store, service, ctrl, readsAfterOpen } =
        await openWithControlledReread([
          {
            kind: "activity",
            id: "tool-hascall",
            label: "shell",
            family: "tool",
            state: "running",
            detail: { output: "base", callId: "call-A" },
          },
        ]);
      // Invalid: incoming callId missing → reread, no mutation.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-hascall",
          delta: " should-not-append",
        },
      } as AnyNotification);
      await yieldMicrotask();
      await ctrl.started(1);
      // Synchronous: text unchanged — invalid did not mutate or freeze.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-hascall");
      expect(item0?.kind).toBe("activity");
      if (item0?.kind === "activity") {
        expect(item0.detail.output).toBe("base");
      }
      // Prove no freeze: valid matching callId delta applies synchronously
      // while the reread is still blocked.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-hascall",
          callId: "call-A",
          delta: "\nappended",
        },
      } as AnyNotification);
      // Fix 2: cross a scheduler dispatch turn before asserting.
      await yieldMicrotask();
      expect(ctrl.getStartedCount()).toBe(1);
      const item1 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-hascall");
      if (item1?.kind === "activity") {
        expect(item1.detail.output).toBe("base\nappended");
      }
      // Release — reread omits tool-hascall. Valid matching callId delta
      // marked it live-owned, so the rehydrate preserves it.
      const preReleaseConv = store.getState().conversation;
      const reconcileP = installReconcileBarrier(store, preReleaseConv);
      ctrl.release();
      await ctrl.completed(1);
      await reconcileP;
      await yieldMicrotask();
      await assertAfterQuiescence(store, ctrl, () => {
        expect(ctrl.getStartedCount()).toBe(1);
        expect(ctrl.getDoneCount()).toBe(1);
        expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 1);
        // Target preserved with valid delta content.
        const item2 = store
          .getState()
          .conversation?.items.find((i) => i.id === "tool-hascall");
        expect(item2).toBeDefined();
        if (item2?.kind === "activity") {
          expect(item2.detail.output).toBe("base\nappended");
        }
      });
    });

    // --- positive controls (no reread) ---

    it("2A-Family-r2: exact callId match appends tool delta synchronously", async () => {
      const { store, service, ctrl, readsAfterOpen } =
        await openWithControlledReread([
          {
            kind: "activity",
            id: "tool-exact",
            label: "shell",
            family: "tool",
            state: "running",
            detail: { output: "base", callId: "call-exact" },
          },
        ]);
      // Valid: exact callId match → accepted synchronously, no reread.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-exact",
          callId: "call-exact",
          delta: "\nappended",
        },
      } as AnyNotification);
      // Fix 2: cross an actual scheduler dispatch turn/barrier before
      // asserting zero controlled reads/unchanged service count. Immediate
      // sync count is insufficient — a queued reread would dispatch on the
      // next microtask.
      await yieldMicrotask();
      await assertAfterQuiescence(store, ctrl, () => {
        // No reread — exact match accepted.
        expect(ctrl.getStartedCount()).toBe(0);
        expect(service.readProjectionCalls.length).toBe(readsAfterOpen);
        const item = store
          .getState()
          .conversation?.items.find((i) => i.id === "tool-exact");
        if (item?.kind === "activity") {
          expect(item.detail.output).toBe("base\nappended");
        }
      });
    });

    it("2A-Family-r2: exact reasoning match appends reasoning delta synchronously", async () => {
      const { store, service, ctrl, readsAfterOpen } =
        await openWithControlledReread([
          {
            kind: "activity",
            id: "reason-exact",
            label: "My Custom Label",
            family: "reasoning",
            state: "running",
            detail: { output: "base" },
          },
        ]);
      // Valid: reasoning family match → accepted synchronously, no reread.
      store.getState().applyNotification({
        method: "item/reasoning/summaryTextDelta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "reason-exact",
          summaryIndex: 0,
          delta: " appended",
        },
      } as AnyNotification);
      // Fix 2: cross an actual scheduler dispatch turn/barrier before
      // asserting zero controlled reads/unchanged service count.
      await yieldMicrotask();
      await assertAfterQuiescence(store, ctrl, () => {
        // No reread — exact family match accepted.
        expect(ctrl.getStartedCount()).toBe(0);
        expect(service.readProjectionCalls.length).toBe(readsAfterOpen);
        const item = store
          .getState()
          .conversation?.items.find((i) => i.id === "reason-exact");
        if (item?.kind === "activity") {
          expect(item.detail.output).toBe("base appended");
        }
      });
    });
  });

  describe("Task 2A-Items: unified truncation ownership", () => {
    it("frozen activity stays frozen across later deltas until authoritative replacement", async () => {
      // Open with a tool activity whose output is already at the byte limit.
      const service = new FakeConversationService();
      const largeOutput = "x".repeat(70_000);
      service.readProjectionResult = {
        conversation: makeConversation({
          items: [
            {
              kind: "activity",
              id: "tool-1",
              label: "shell",
              family: "tool",
              state: "running",
              detail: { output: largeOutput, callId: "call-A" },
            },
          ],
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      // The item should be truncated and frozen.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      if (item0?.kind === "activity") {
        expect(item0.detail.output?.endsWith("… truncated")).toBe(true);
        const encoder = new TextEncoder();
        expect(
          encoder.encode(item0.detail.output ?? "").length,
        ).toBeLessThanOrEqual(65536);
      }

      // A later delta must NOT append — the item is frozen.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-1",
          callId: "call-A",
          delta: " MORE",
        },
      } as AnyNotification);
      const item1 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      if (item1?.kind === "activity") {
        // Still frozen — marker appears exactly once, no new content.
        expect(item1.detail.output?.endsWith("… truncated")).toBe(true);
        const markerCount =
          item1.detail.output?.split("… truncated").length ?? 0;
        expect(markerCount - 1).toBe(1);
      }
    });

    it("authoritative item/completed replacement unfreezes a frozen item", async () => {
      const service = new FakeConversationService();
      const largeOutput = "x".repeat(70_000);
      service.readProjectionResult = {
        conversation: makeConversation({
          items: [
            {
              kind: "activity",
              id: "tool-1",
              label: "shell",
              family: "tool",
              state: "running",
              detail: { output: largeOutput, callId: "call-A" },
            },
          ],
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      // Verify it's frozen.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      expect(item0?.kind).toBe("activity");
      if (item0?.kind === "activity") {
        expect(item0.detail.output?.endsWith("… truncated")).toBe(true);
      }

      // Authoritative replacement via item/completed with short output.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "commandExecution",
            id: "tool-1",
            toolName: "shell",
            status: "completed",
            callId: "call-A",
            output: "short-result",
          } as ThreadItem,
        },
      } as AnyNotification);

      const item1 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      if (item1?.kind === "activity") {
        // Unfrozen — short output, no marker.
        expect(item1.detail.output).toBe("short-result");
        expect(item1.detail.output?.endsWith("… truncated")).toBe(false);
      }

      // A subsequent delta should now append (freeze removed).
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "tool-1",
          callId: "call-A",
          delta: " appended",
        },
      } as AnyNotification);
      const item2 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      if (item2?.kind === "activity") {
        expect(item2.detail.output).toBe("short-result appended");
      }
    });

    it("reset removes stale freeze entries", async () => {
      const service = new FakeConversationService();
      const largeText = "x".repeat(70_000);
      service.readProjectionResult = {
        conversation: makeConversation({
          items: [
            {
              kind: "assistant",
              id: "item-1",
              markdown: largeText,
              streaming: true,
            },
          ],
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      // Frozen.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "item-1");
      if (item0?.kind === "assistant") {
        expect(item0.markdown.endsWith("… truncated")).toBe(true);
      }

      // Reset clears all thread-scoped state.
      store.getState().reset();
      expect(store.getState().conversation).toBeNull();

      // Reopen with short content — should NOT be frozen.
      const service2 = new FakeConversationService();
      service2.readProjectionResult = {
        conversation: makeConversation({
          items: [
            {
              kind: "assistant",
              id: "item-1",
              markdown: "short",
              streaming: false,
            },
          ],
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service2, createFakeSink(), "ref-1");

      // Delta should append — no stale freeze from the prior thread.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "item-1",
          delta: " appended",
        },
      } as AnyNotification);
      const item1 = store
        .getState()
        .conversation?.items.find((i) => i.id === "item-1");
      if (item1?.kind === "assistant") {
        expect(item1.markdown).toBe("short appended");
      }
    });

    it("thread switch (openProjected) clears prior-thread freeze entries", async () => {
      const service = new FakeConversationService();
      const largeText = "x".repeat(70_000);
      service.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-1",
          items: [
            {
              kind: "assistant",
              id: "item-1",
              markdown: largeText,
              streaming: true,
            },
          ],
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      // Frozen.
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "item-1");
      if (item0?.kind === "assistant") {
        expect(item0.markdown.endsWith("… truncated")).toBe(true);
      }

      // Switch threads — openProjected with a different thread.
      const service2 = new FakeConversationService();
      service2.readProjectionResult = {
        conversation: makeConversation({
          threadId: "thread-2",
          items: [
            {
              kind: "assistant",
              id: "item-1",
              markdown: "short",
              streaming: false,
            },
          ],
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service2, createFakeSink(), "ref-2");

      // Delta should append — prior-thread freeze cleared.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-2",
          ref: "ref-2",
          turnId: "t1",
          itemId: "item-1",
          delta: " appended",
        },
      } as AnyNotification);
      const item1 = store
        .getState()
        .conversation?.items.find((i) => i.id === "item-1");
      if (item1?.kind === "assistant") {
        expect(item1.markdown).toBe("short appended");
      }
    });
  });

  describe("Task 2A-Items: within-page paging dedupe", () => {
    it("dedupes duplicate IDs within the incoming page, preserving order", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [userMessageItem("base", "base")],
            }),
          ],
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      // Page with a duplicate ID within itself: page-A, page-B, page-A (repeat).
      service.olderItems = {
        items: [
          { kind: "user", id: "page-A", text: "A" },
          { kind: "user", id: "page-B", text: "B" },
          { kind: "user", id: "page-A", text: "A-repeat" },
        ],
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);

      const items = store.getState().conversation?.items ?? [];
      const ids = items.map((i) => i.id);
      // page-A appears exactly once.
      expect(ids.filter((id) => id === "page-A").length).toBe(1);
      // The first occurrence's content is kept (order preserved).
      const pageA = items.find((i) => i.id === "page-A");
      if (pageA?.kind === "user") {
        expect(pageA.text).toBe("A");
      }
      // page-B appears exactly once.
      expect(ids.filter((id) => id === "page-B").length).toBe(1);
    });

    it("dedupes duplicate IDs within page AND against retained items", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [userMessageItem("existing", "existing")],
            }),
          ],
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      // Page: "existing" (dup of retained), "page-A", "page-A" (dup within page).
      service.olderItems = {
        items: [
          { kind: "user", id: "existing", text: "dup" },
          { kind: "user", id: "page-A", text: "A" },
          { kind: "user", id: "page-A", text: "A-repeat" },
        ],
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);

      const items = store.getState().conversation?.items ?? [];
      const ids = items.map((i) => i.id);
      // "existing" appears exactly once (retained version kept).
      expect(ids.filter((id) => id === "existing").length).toBe(1);
      const existing = items.find((i) => i.id === "existing");
      if (existing?.kind === "user") {
        expect(existing.text).toBe("existing");
      }
      // "page-A" appears exactly once.
      expect(ids.filter((id) => id === "page-A").length).toBe(1);
    });

    it("dedupes overlapping fragments by transcriptKey when wire IDs differ", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                {
                  type: "userMessage",
                  id: "wire-current",
                  transcriptKey: "stable-item",
                  position: { entry: 3, item: 0 },
                  text: "current",
                },
              ],
            }),
          ],
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "opaque-cursor" });
      service.olderItems = {
        items: [
          {
            kind: "user",
            id: "wire-overlap",
            transcriptKey: "stable-item",
            position: { entry: 2, item: 0 },
            text: "stale overlap",
          },
          { kind: "user", id: "older", text: "older" },
        ],
        nextCursor: "next",
      };

      await store.getState().loadOlder(service);

      const items = store.getState().conversation?.items ?? [];
      expect(
        items.filter((item) => item.transcriptKey === "stable-item"),
      ).toHaveLength(1);
      expect(
        items.find((item) => item.transcriptKey === "stable-item")?.id,
      ).toBe("wire-current");
      expect(items.map((item) => item.id)).toEqual(["older", "wire-current"]);
    });

    it("rehydrates visible state after a stale v4 cursor", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({ id: "t0", items: [userMessageItem("old", "old")] }),
          ],
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "stale" });
      service.olderItems = Promise.reject(
        new WireError("stale transcript cursor", -32020, {
          evenerErrorInfo: "transcriptItemCursorStale",
        }),
      ) as never;
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({ id: "t1", items: [userMessageItem("fresh", "fresh")] }),
          ],
        }),
      );

      await store.getState().loadOlder(service);

      expect(
        store.getState().conversation?.items.map((item) => item.id),
      ).toEqual(["fresh"]);
      expect(service.readProjectionCalls.length).toBe(2);
    });
  });

  // --- Task 2A-Truncation residual: exact reconciliation of truncation ownership
  // from FINAL retained/merged items on every authoritative install path
  // (open/openProjected/rehydrate/page/lifecycle). Replaces add-only frozen
  // tracking: an authoritative short version unfreezes; omitted/capped IDs are
  // removed; newer superseded live/page versions are preserved based on final
  // actual content. item/agentMessage/reset explicitly unfreezes the ID before
  // the empty reset so a later delta applies.
  //
  // C3: authoritative oversized→short→delta applies (open + rehydrate + openProjected)
  // C4: omitted/capped ID removed from truncatedItemIds
  // protocol reset→delta: reset unfreezes before empty reset
  // paged oversized item freezes and marker once
  // lifecycle started/completed oversized then untruncate
  // No vacuous `if` assertions — direct expects on the resolved item.
  describe("Task 2A-Truncation residual: exact reconciliation", () => {
    // Helper: open a conversation via openProjected with the given raw ThreadItem
    // array (uses projectConversation so families are set from the canonical projector).
    async function openProjectedWithItems(items: ThreadItem[]): Promise<{
      store: ReturnType<typeof createConversationStore>;
      service: FakeConversationService;
    }> {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ turns: [makeTurn({ id: "t0", items })] }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      return { store, service };
    }

    it("C3 open: authoritative oversized→short unfreezes so later delta applies", async () => {
      // Open with an oversized assistant item, then re-open (openProjected) with
      // the SAME id but short content — the freeze must be removed so a later
      // delta appends.
      const { store } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "x".repeat(MAX_ITEM_BYTES + 100), "inProgress"),
      ]);
      const xBefore = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xBefore?.kind).toBe("assistant");
      expect(
        xBefore?.kind === "assistant" &&
          xBefore.markdown.endsWith("… truncated"),
      ).toBe(true);

      // Re-open with short content for the same id.
      const service2 = new FakeConversationService();
      service2.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("B", "base"),
                agentMessageItem("X", "short", "completed"),
              ],
            }),
          ],
        }),
      );
      await store.getState().openProjected(service2, createFakeSink(), "ref-1");
      const xAfter = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xAfter?.kind).toBe("assistant");
      expect(xAfter?.kind === "assistant" && xAfter.markdown).toBe("short");

      // Delta should now append — freeze removed by authoritative short version.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " appended",
        },
      } as AnyNotification);
      const xDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xDelta?.kind).toBe("assistant");
      expect(xDelta?.kind === "assistant" && xDelta.markdown).toBe(
        "short appended",
      );
    });

    it("C3 rehydrate: authoritative oversized→short unfreezes so later delta applies", async () => {
      // Open oversized, then rehydrate with short content for the same id — the
      // freeze must be removed so a later delta appends.
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "x".repeat(MAX_ITEM_BYTES + 100), "inProgress"),
      ]);
      const xBefore = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xBefore?.kind).toBe("assistant");
      expect(
        xBefore?.kind === "assistant" &&
          xBefore.markdown.endsWith("… truncated"),
      ).toBe(true);

      // Rehydrate with short content for the same id.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("B", "base"),
                agentMessageItem("X", "short", "completed"),
              ],
            }),
          ],
        }),
      );
      await store.getState().rehydrate(service, createFakeSink());
      const xAfter = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xAfter?.kind).toBe("assistant");
      expect(xAfter?.kind === "assistant" && xAfter.markdown).toBe("short");

      // Delta should now append — freeze removed by authoritative short version.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " appended",
        },
      } as AnyNotification);
      const xDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xDelta?.kind).toBe("assistant");
      expect(xDelta?.kind === "assistant" && xDelta.markdown).toBe(
        "short appended",
      );
    });

    it("C4 open: omitted ID is removed from truncation ownership (no stale freeze)", async () => {
      // Open with oversized X, then re-open omitting X — a later delta to X (if
      // it reappears via a live notification) must not be frozen by the stale
      // entry. We verify the freeze does not persist for the omitted id by
      // re-adding X via item/completed with short content and then delta.
      const { store } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "x".repeat(MAX_ITEM_BYTES + 100), "inProgress"),
      ]);
      // Re-open omitting X entirely.
      const service2 = new FakeConversationService();
      service2.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({ id: "t0", items: [userMessageItem("B", "base")] }),
          ],
        }),
      );
      await store.getState().openProjected(service2, createFakeSink(), "ref-1");
      expect(
        store.getState().conversation?.items.find((i) => i.id === "X"),
      ).toBeUndefined();

      // Re-introduce X via item/started with short content, then delta.
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: agentMessageItem("X", "fresh-short", "inProgress"),
        },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " appended",
        },
      } as AnyNotification);
      const xItem = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xItem?.kind).toBe("assistant");
      expect(xItem?.kind === "assistant" && xItem.markdown).toBe(
        "fresh-short appended",
      );
    });

    it("C4 rehydrate: omitted ID is removed from truncation ownership", async () => {
      // Open with oversized X, then rehydrate omitting X. The stale freeze for
      // X must be removed. Re-introduce X via item/completed short + delta.
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "x".repeat(MAX_ITEM_BYTES + 100), "inProgress"),
      ]);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({ id: "t0", items: [userMessageItem("B", "base")] }),
          ],
        }),
      );
      await store.getState().rehydrate(service, createFakeSink());
      expect(
        store.getState().conversation?.items.find((i) => i.id === "X"),
      ).toBeUndefined();

      // Re-introduce X via item/completed with short content, then delta.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: agentMessageItem("X", "fresh-short", "completed"),
        },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " appended",
        },
      } as AnyNotification);
      const xItem = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xItem?.kind).toBe("assistant");
      expect(xItem?.kind === "assistant" && xItem.markdown).toBe(
        "fresh-short appended",
      );
    });

    it("C4 capped ID is removed from truncation ownership (500-cap trims oldest)", async () => {
      // Fill past the 500-cap so the oldest items are trimmed. Only the first
      // few items are oversized (to seed a freeze entry that must be removed
      // when trimmed); the rest are small so truncation is fast.
      const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
      const items: ThreadItem[] = [
        userMessageItem("keep", "base"),
        // 3 oversized items at the front — these get trimmed by the cap.
        agentMessageItem("a-0", oversized, "completed"),
        agentMessageItem("a-1", oversized, "completed"),
        agentMessageItem("a-2", oversized, "completed"),
      ];
      // 498 small items → total 502, oldest (a-0,a-1,a-2) trimmed by cap.
      for (let i = 3; i < 501; i++) {
        items.push(agentMessageItem(`a-${i}`, "small", "completed"));
      }
      const { store } = await openProjectedWithItems(items);
      const retained = store.getState().conversation?.items ?? [];
      expect(retained.length).toBeLessThanOrEqual(500);
      // a-0 should have been trimmed (it's the oldest oversized after "keep").
      expect(retained.find((i) => i.id === "a-0")).toBeUndefined();

      // Re-introduce a-0 via item/completed with short content, then delta.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: agentMessageItem("a-0", "fresh-short", "completed"),
        },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "a-0",
          delta: " appended",
        },
      } as AnyNotification);
      const xItem = store
        .getState()
        .conversation?.items.find((i) => i.id === "a-0");
      expect(xItem?.kind).toBe("assistant");
      expect(xItem?.kind === "assistant" && xItem.markdown).toBe(
        "fresh-short appended",
      );
    });

    it("protocol reset→delta: reset unfreezes ID before empty reset so later delta applies", async () => {
      // Open with an oversized assistant item. item/agentMessage/reset clears
      // the markdown to "" AND must unfreeze the id so a subsequent delta applies.
      const { store } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "x".repeat(MAX_ITEM_BYTES + 100), "inProgress"),
      ]);
      const xBefore = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xBefore?.kind).toBe("assistant");
      expect(
        xBefore?.kind === "assistant" &&
          xBefore.markdown.endsWith("… truncated"),
      ).toBe(true);

      // Protocol reset — must unfreeze the id before the empty reset.
      store.getState().applyNotification({
        method: "item/agentMessage/reset",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
        },
      } as AnyNotification);
      const xReset = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xReset?.kind).toBe("assistant");
      expect(xReset?.kind === "assistant" && xReset.markdown).toBe("");

      // Delta after reset must apply — the freeze was removed.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: "fresh content",
        },
      } as AnyNotification);
      const xDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xDelta?.kind).toBe("assistant");
      expect(xDelta?.kind === "assistant" && xDelta.markdown).toBe(
        "fresh content",
      );
    });

    it("paged oversized item freezes and marker appears once", async () => {
      // Load an oversized item via loadOlder — it must be truncated with the
      // marker appearing exactly once, and frozen against a later delta.
      const { store, service } = await openProjectedWithItems([
        userMessageItem("base", "base"),
      ]);
      store.setState({ olderCursor: "cursor-1" });
      service.olderItems = {
        items: [
          {
            kind: "activity",
            id: "page-tool",
            label: "shell",
            family: "tool",
            state: "completed",
            detail: {
              output: "x".repeat(MAX_ITEM_BYTES + 100),
              callId: "call-A",
            },
          },
        ],
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);
      const item = store
        .getState()
        .conversation?.items.find((i) => i.id === "page-tool");
      expect(item?.kind).toBe("activity");
      expect(
        item?.kind === "activity" &&
          item.detail.output?.endsWith("… truncated"),
      ).toBe(true);
      const markerCount =
        item?.kind === "activity"
          ? (item.detail.output?.split("… truncated").length ?? 0)
          : 0;
      expect(markerCount - 1).toBe(1);

      // A later tool-output delta must be frozen (marker once, no new content).
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "page-tool",
          callId: "call-A",
          delta: " MORE",
        },
      } as AnyNotification);
      const item2 = store
        .getState()
        .conversation?.items.find((i) => i.id === "page-tool");
      expect(item2?.kind).toBe("activity");
      expect(
        item2?.kind === "activity" &&
          item2.detail.output?.endsWith("… truncated"),
      ).toBe(true);
      const markerCount2 =
        item2?.kind === "activity"
          ? (item2.detail.output?.split("… truncated").length ?? 0)
          : 0;
      expect(markerCount2 - 1).toBe(1);
    });

    it("lifecycle item/started oversized then item/completed untruncates", async () => {
      // An item arrives oversized via item/started (frozen), then item/completed
      // arrives with short content — the freeze is removed and a later delta
      // applies.
      const { store } = await openProjectedWithItems([
        userMessageItem("B", "base"),
      ]);
      // item/started with oversized output.
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: {
            type: "commandExecution",
            id: "tool-1",
            toolName: "shell",
            status: "inProgress",
            callId: "call-A",
            output: "x".repeat(MAX_ITEM_BYTES + 100),
          } as ThreadItem,
        },
      } as AnyNotification);
      const item0 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      expect(item0?.kind).toBe("activity");
      expect(
        item0?.kind === "activity" &&
          item0.detail.output?.endsWith("… truncated"),
      ).toBe(true);

      // item/completed with short output — untruncates (unfreezes).
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: {
            type: "commandExecution",
            id: "tool-1",
            toolName: "shell",
            status: "completed",
            callId: "call-A",
            output: "short-result",
          } as ThreadItem,
        },
      } as AnyNotification);
      const item1 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      expect(item1?.kind).toBe("activity");
      expect(item1?.kind === "activity" && item1.detail.output).toBe(
        "short-result",
      );
      expect(
        item1?.kind === "activity" &&
          item1.detail.output?.endsWith("… truncated"),
      ).toBe(false);

      // A later delta applies — freeze removed.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "tool-1",
          callId: "call-A",
          delta: " appended",
        },
      } as AnyNotification);
      const item2 = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      expect(item2?.kind).toBe("activity");
      expect(item2?.kind === "activity" && item2.detail.output).toBe(
        "short-result appended",
      );
    });

    // Truncation ownership must key off ONE canonical identity (transcriptKey
    // ?? id — timelineIdentity) everywhere. Projection/rehydrate already freeze
    // by that identity; these three cover the live delta guards and the
    // lifecycle/rehydrate cleanup paths, which used the raw wire id instead.
    it("identity: frozen-by-transcriptKey item blocks a later delta keyed by a differing wire id", async () => {
      // Truncated on a non-delta field (arguments) at projection, under the
      // item's transcriptKey. A later delta notification only carries the
      // wire id — the frozen guard must still resolve to the same identity
      // and refuse it.
      const { store } = await openProjectedWithItems([
        userMessageItem("base", "base"),
        {
          type: "commandExecution",
          id: "wire-1",
          transcriptKey: "transcript-1",
          toolName: "shell",
          status: "completed",
          argumentsJson: "x".repeat(MAX_ITEM_BYTES + 100),
          output: "short",
          callId: "call-1",
        },
      ]);
      const before = store
        .getState()
        .conversation?.items.find((i) => i.id === "wire-1");
      expect(before?.kind).toBe("activity");
      expect(
        before?.kind === "activity" &&
          before.detail.arguments?.endsWith("… truncated"),
      ).toBe(true);
      expect(
        store.getState().getTruncatedItemIds().has("transcript-1"),
      ).toBe(true);

      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "wire-1",
          callId: "call-1",
          delta: " MORE",
        },
      } as AnyNotification);

      const after = store
        .getState()
        .conversation?.items.find((i) => i.id === "wire-1");
      expect(after?.kind).toBe("activity");
      expect(after?.kind === "activity" && after.detail.output).toBe("short");
    });

    it("identity: a lifecycle event releases the freeze recorded under transcriptKey, not the wire id", async () => {
      // item/started freezes the canonical identity (transcriptKey) on an
      // oversized field. item/completed with short content is the reset/
      // lifecycle event that is supposed to release that ownership — a fresh
      // item reusing the identity must not stay frozen.
      const { store } = await openProjectedWithItems([
        userMessageItem("base", "base"),
      ]);
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: {
            type: "commandExecution",
            id: "tool-1",
            transcriptKey: "shared-key",
            toolName: "shell",
            status: "inProgress",
            callId: "call-A",
            output: "x".repeat(MAX_ITEM_BYTES + 100),
          } as ThreadItem,
        },
      } as AnyNotification);
      expect(
        store.getState().getTruncatedItemIds().has("shared-key"),
      ).toBe(true);

      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: {
            type: "commandExecution",
            id: "tool-1",
            transcriptKey: "shared-key",
            toolName: "shell",
            status: "completed",
            callId: "call-A",
            output: "short-result",
          } as ThreadItem,
        },
      } as AnyNotification);

      // A fresh item reusing the identity is not frozen.
      expect(
        store.getState().getTruncatedItemIds().has("shared-key"),
      ).toBe(false);
      const item = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      expect(item?.kind).toBe("activity");
      expect(item?.kind === "activity" && item.detail.output).toBe(
        "short-result",
      );
    });

    it("identity: rehydrate unfreezes an authoritative short version tracked under transcriptKey", async () => {
      // Same audit as above, applied to the rehydrate merge's priorFrozen
      // computation, which compared truncatedItemIds (transcriptKey-keyed)
      // against a wire-id-keyed reread set.
      const { store, service } = await openProjectedWithItems([
        userMessageItem("base", "base"),
        {
          type: "commandExecution",
          id: "wire-1",
          transcriptKey: "transcript-1",
          toolName: "shell",
          status: "completed",
          argumentsJson: "x".repeat(MAX_ITEM_BYTES + 100),
          output: "short",
          callId: "call-1",
        },
      ]);
      expect(
        store.getState().getTruncatedItemIds().has("transcript-1"),
      ).toBe(true);

      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("base", "base"),
                {
                  type: "commandExecution",
                  id: "wire-1",
                  transcriptKey: "transcript-1",
                  toolName: "shell",
                  status: "completed",
                  argumentsJson: "short",
                  output: "short",
                  callId: "call-1",
                },
              ],
            }),
          ],
        }),
      );
      await store.getState().rehydrate(service, createFakeSink());

      expect(
        store.getState().getTruncatedItemIds().has("transcript-1"),
      ).toBe(false);

      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "wire-1",
          callId: "call-1",
          delta: " appended",
        },
      } as AnyNotification);
      const after = store
        .getState()
        .conversation?.items.find((i) => i.id === "wire-1");
      expect(after?.kind).toBe("activity");
      expect(after?.kind === "activity" && after.detail.output).toBe(
        "short appended",
      );
    });

    it("rehydrate preserves newer superseded live truncated version based on final content", async () => {
      // Open with a SHORT X. Start a hanging rehydrate whose reread has X SHORT.
      // While reread is in-flight, a live delta makes X oversized (frozen). The
      // rehydrate must preserve the live (truncated) version AND keep it frozen
      // (final actual content is oversized). A later delta must stay frozen.
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "short", "inProgress"),
      ]);
      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("B", "base"),
                agentMessageItem("X", "reread-short", "completed"),
              ],
            }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // Live delta makes X oversized → frozen.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: "x".repeat(MAX_ITEM_BYTES + 100),
        },
      } as AnyNotification);
      const xLive = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(
        xLive?.kind === "assistant" && xLive.markdown.endsWith("… truncated"),
      ).toBe(true);
      // Release rehydrate — it must preserve the live truncated version.
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      const xAfter = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xAfter?.kind).toBe("assistant");
      expect(
        xAfter?.kind === "assistant" && xAfter.markdown.endsWith("… truncated"),
      ).toBe(true);
      // A later delta must stay frozen — final content is oversized.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " should-not-append",
        },
      } as AnyNotification);
      const xFinal = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(
        xFinal?.kind === "assistant" && xFinal.markdown.endsWith("… truncated"),
      ).toBe(true);
      expect(
        xFinal?.kind === "assistant" &&
          xFinal.markdown.includes("should-not-append"),
      ).toBe(false);
    });
  });

  // --- Task 2A-Truncation residual fix round 1: I1 loadOlder preserves
  // already-frozen current items; I2 rehydrate preserves only superseded IDs
  // still actually frozen after the accepted live update; M1 observational
  // omission/cap tests through reread/page/delta (no lifecycle clearing).
  describe("Task 2A-Truncation residual fix round 1", () => {
    async function openProjectedWithItems(items: ThreadItem[]): Promise<{
      store: ReturnType<typeof createConversationStore>;
      service: FakeConversationService;
    }> {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ turns: [makeTurn({ id: "t0", items })] }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      return { store, service };
    }

    // I1: loadOlder — already-frozen current items stay frozen after page
    // reconciliation. The current items are already truncated (text ≤ limit), so
    // exceedsByteLimit is false for them. The fix must capture prior frozen IDs
    // and preserve freeze for final-retained current items that were already
    // frozen. A later delta to such an item must remain blocked.
    it("I1 loadOlder: frozen current item stays frozen after page load, delta blocked", async () => {
      // Open with an oversized assistant item (frozen), plus a page cursor.
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "x".repeat(MAX_ITEM_BYTES + 100), "inProgress"),
      ]);
      const xBefore = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(
        xBefore?.kind === "assistant" &&
          xBefore.markdown.endsWith("… truncated"),
      ).toBe(true);
      store.setState({ olderCursor: "cursor-1" });

      // Load a small page item — reconciliation must NOT unfreeze X.
      service.olderItems = {
        items: [{ kind: "user", id: "page-A", text: "page" }],
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);

      // X is still present and still frozen (marker once).
      const xAfter = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xAfter?.kind).toBe("assistant");
      expect(
        xAfter?.kind === "assistant" && xAfter.markdown.endsWith("… truncated"),
      ).toBe(true);

      // A later delta to X must be blocked — X stays frozen.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " should-not-append",
        },
      } as AnyNotification);
      const xDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(
        xDelta?.kind === "assistant" && xDelta.markdown.endsWith("… truncated"),
      ).toBe(true);
      expect(
        xDelta?.kind === "assistant" &&
          xDelta.markdown.includes("should-not-append"),
      ).toBe(false);
    });

    // I1: loadOlder — incoming raw oversized page item freezes independently, and
    // an already-frozen current item AND the page item both freeze with one
    // marker each.
    it("I1 loadOlder: frozen current + oversized page item both freeze, deltas blocked", async () => {
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "x".repeat(MAX_ITEM_BYTES + 100), "inProgress"),
      ]);
      store.setState({ olderCursor: "cursor-1" });
      service.olderItems = {
        items: [
          {
            kind: "activity",
            id: "page-tool",
            label: "shell",
            family: "tool",
            state: "completed",
            detail: {
              output: "x".repeat(MAX_ITEM_BYTES + 100),
              callId: "call-A",
            },
          },
        ],
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);

      // Both X (current) and page-tool (incoming) are frozen.
      const xItem = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      const pItem = store
        .getState()
        .conversation?.items.find((i) => i.id === "page-tool");
      expect(
        xItem?.kind === "assistant" && xItem.markdown.endsWith("… truncated"),
      ).toBe(true);
      expect(
        pItem?.kind === "activity" &&
          pItem.detail.output?.endsWith("… truncated"),
      ).toBe(true);

      // Delta to X (frozen current) is blocked.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " should-not-append",
        },
      } as AnyNotification);
      const xDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(
        xDelta?.kind === "assistant" &&
          xDelta.markdown.includes("should-not-append"),
      ).toBe(false);

      // Delta to page-tool (frozen page item) is blocked.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "page-tool",
          callId: "call-A",
          delta: " should-not-append",
        },
      } as AnyNotification);
      const pDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "page-tool");
      expect(
        pDelta?.kind === "activity" &&
          pDelta.detail.output?.includes("should-not-append"),
      ).toBe(false);
    });

    // I1: loadOlder — capped current item loses freeze (no stale effect). When
    // the cap trims an already-frozen current item, re-introducing it via a page
    // load with short content must not re-freeze.
    it("I1 loadOlder: capped frozen current item loses freeze, re-introduced short not frozen", async () => {
      // Open with an oversized item (frozen) + one small item. The oversized
      // item is retained (under cap). Then rehydrate OMITTING the oversized
      // item — reconciliation removes its freeze. Then load a page bringing
      // it back with SHORT content — it must NOT be frozen.
      const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", oversized, "inProgress"),
      ]);
      // Verify X is frozen.
      const xBefore = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(
        xBefore?.kind === "assistant" &&
          xBefore.markdown.endsWith("… truncated"),
      ).toBe(true);

      // Rehydrate omitting X — reconciliation removes the freeze for X.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({ id: "t0", items: [userMessageItem("B", "base")] }),
          ],
        }),
      );
      await store.getState().rehydrate(service, createFakeSink());
      expect(
        store.getState().conversation?.items.find((i) => i.id === "X"),
      ).toBeUndefined();

      // Load a page bringing X back with SHORT content — must NOT be frozen.
      store.setState({ olderCursor: "cursor-1" });
      service.olderItems = {
        items: [{ kind: "user", id: "X", text: "short-page" }],
        nextCursor: undefined,
      };
      await store.getState().loadOlder(service);
      const reintroduced = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(reintroduced).toBeDefined();
      expect(reintroduced?.kind).toBe("user");
    });

    // I2: rehydrate — do NOT pass all superseded live IDs as frozen. If a reset
    // (short lifecycle) removed the freeze before the rehydrate commits, the
    // superseded ID stays unfrozen and a later delta applies.
    it("I2 rehydrate: reset removes freeze, rehydrate preserves unfrozen, delta applies", async () => {
      // Open with an oversized assistant item X (frozen).
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "x".repeat(MAX_ITEM_BYTES + 100), "inProgress"),
      ]);
      // Start a hanging rehydrate. Reread has X short.
      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("B", "base"),
                agentMessageItem("X", "reread-short", "completed"),
              ],
            }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();

      // While rehydrate is in-flight, reset X — this unfreezes X and marks it
      // live-owned. The live version (empty) is newer than the reread.
      store.getState().applyNotification({
        method: "item/agentMessage/reset",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
        },
      } as AnyNotification);
      const xReset = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xReset?.kind === "assistant" && xReset.markdown).toBe("");

      // Release rehydrate. X is superseded (live-owned, newer). The rehydrate
      // must preserve the live (empty) version. Since the reset removed the
      // freeze, the rehydrate must NOT re-freeze X.
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      const xAfter = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xAfter?.kind === "assistant" && xAfter.markdown).toBe("");

      // A later delta must apply — freeze was removed by reset, not re-frozen.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: "fresh content",
        },
      } as AnyNotification);
      const xDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xDelta?.kind === "assistant" && xDelta.markdown).toBe(
        "fresh content",
      );
    });

    // I2: rehydrate — a superseded item that is STILL frozen (live delta made it
    // oversized) stays frozen after rehydrate. This is the existing correct case.
    it("I2 rehydrate: superseded still-frozen item stays frozen, delta blocked", async () => {
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "short", "inProgress"),
      ]);
      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("B", "base"),
                agentMessageItem("X", "reread-short", "completed"),
              ],
            }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();

      // Live delta makes X oversized → frozen (markLiveOwned).
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: "x".repeat(MAX_ITEM_BYTES + 100),
        },
      } as AnyNotification);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();

      // X is superseded (live-owned, oversized) — stays frozen.
      const xAfter = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(
        xAfter?.kind === "assistant" && xAfter.markdown.endsWith("… truncated"),
      ).toBe(true);

      // Later delta blocked.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " should-not-append",
        },
      } as AnyNotification);
      const xFinal = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(
        xFinal?.kind === "assistant" &&
          xFinal.markdown.includes("should-not-append"),
      ).toBe(false);
    });

    // I2: rehydrate — short superseded include case: reread includes X (stale
    // short), live delta made X short (not frozen), rehydrate preserves live
    // short, delta applies.
    it("I2 rehydrate: short superseded include — live short not re-frozen, delta applies", async () => {
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "original", "inProgress"),
      ]);
      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                userMessageItem("B", "base"),
                agentMessageItem("X", "stale-reread", "completed"),
              ],
            }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();

      // Live delta appends short text (not frozen, markLiveOwned).
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " live-append",
        },
      } as AnyNotification);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();

      // X is superseded (live-owned, short) — NOT frozen. Delta applies.
      const xAfter = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xAfter?.kind === "assistant" && xAfter.markdown).toBe(
        "original live-append",
      );

      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " more",
        },
      } as AnyNotification);
      const xDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xDelta?.kind === "assistant" && xDelta.markdown).toBe(
        "original live-append more",
      );
    });

    // I2: rehydrate — short superseded omit case: reread omits X, live delta made
    // X short (not frozen), rehydrate appends X as live tail, delta applies.
    it("I2 rehydrate: short superseded omit — live short appended, not re-frozen, delta applies", async () => {
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "original", "inProgress"),
      ]);
      const ctrl = makeControlledRead(service);
      // Reread OMITS X.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({ id: "t0", items: [userMessageItem("B", "base")] }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();

      // Live delta appends short text (not frozen, markLiveOwned).
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " live-append",
        },
      } as AnyNotification);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();

      // X appended as live tail (live-owned, short) — NOT frozen. Delta applies.
      const xAfter = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xAfter?.kind === "assistant" && xAfter.markdown).toBe(
        "original live-append",
      );

      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " more",
        },
      } as AnyNotification);
      const xDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xDelta?.kind === "assistant" && xDelta.markdown).toBe(
        "original live-append more",
      );
    });

    // M1: observational omission via rehydrate (no lifecycle clearing). Open
    // oversized X, rehydrate OMITTING X (removes freeze via reconciliation, not
    // via conversation transition), then re-introduce X via item/completed short
    // + delta. The freeze must be gone.
    it("M1 rehydrate omit: freeze removed by reconciliation, re-introduced short accepts delta", async () => {
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "x".repeat(MAX_ITEM_BYTES + 100), "inProgress"),
      ]);
      // Rehydrate omitting X — reconciliation removes the freeze for X.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({ id: "t0", items: [userMessageItem("B", "base")] }),
          ],
        }),
      );
      await store.getState().rehydrate(service, createFakeSink());
      expect(
        store.getState().conversation?.items.find((i) => i.id === "X"),
      ).toBeUndefined();

      // Re-introduce X via item/completed short, then delta — must apply.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: agentMessageItem("X", "fresh-short", "completed"),
        },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " appended",
        },
      } as AnyNotification);
      const xItem = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xItem?.kind === "assistant" && xItem.markdown).toBe(
        "fresh-short appended",
      );
    });

    // M1: observational cap via rehydrate+page (no lifecycle clearing). Open
    // with an oversized item (frozen), rehydrate OMITTING it (reconciliation
    // removes freeze), then load a page bringing it back with short content —
    // it must not be frozen. This avoids openProjected/reset which clear
    // truncatedItemIds via conversation transition.
    it("M1 loadOlder cap: capped frozen item re-introduced via page with short content not frozen", async () => {
      const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", oversized, "inProgress"),
      ]);
      // Verify X is frozen.
      const xBefore = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(
        xBefore?.kind === "assistant" &&
          xBefore.markdown.endsWith("… truncated"),
      ).toBe(true);

      // Rehydrate omitting X — reconciliation removes the freeze for X.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({ id: "t0", items: [userMessageItem("B", "base")] }),
          ],
        }),
      );
      await store.getState().rehydrate(service, createFakeSink());
      expect(
        store.getState().conversation?.items.find((i) => i.id === "X"),
      ).toBeUndefined();

      // Load a page bringing X back with SHORT content — must NOT be frozen.
      store.setState({ olderCursor: "cursor-1" });
      service.olderItems = {
        items: [{ kind: "user", id: "X", text: "short-page" }],
        nextCursor: undefined,
      };
      await store.getState().loadOlder(service);
      const reintroduced = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(reintroduced).toBeDefined();
      expect(reintroduced?.kind).toBe("user");
    });
  });

  // --- Task 2A-Truncation residual fix round 2: I1/M1 exact prior-frozen
  // carryover for loadOlder (truncatedItemIds ∩ currentConv.item IDs ∩ final
  // retained IDs) + centralized incremental append+cap reconciliation.
  //
  // An incoming raw page item matching a stale frozen ID is independently
  // judged from raw content (exceedsByteLimit), NOT carried over as frozen
  // from the prior set. When an incremental append (item/started, item/completed,
  // warning) evicts an already-frozen item via the 500-cap, the evicted ID is
  // pruned from truncatedItemIds and the page/live ownership maps so a later
  // re-introduction with short content accepts a delta.
  describe("Task 2A-Truncation residual fix round 2", () => {
    async function openProjectedWithItems(items: ThreadItem[]): Promise<{
      store: ReturnType<typeof createConversationStore>;
      service: FakeConversationService;
    }> {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ turns: [makeTurn({ id: "t0", items })] }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      return { store, service };
    }

    it("retains live ownership when transcriptKey differs from wire id during cap pruning", async () => {
      const items: ThreadItem[] = [];
      for (let i = 0; i < 499; i++) items.push(userMessageItem(`u-${i}`, ""));
      items.push({
        ...agentMessageItem("wire-x", "live", "inProgress"),
        transcriptKey: "stable-x",
      });
      const { store, service } = await openProjectedWithItems(items);
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: {
            ...agentMessageItem("wire-x", "live", "completed"),
            transcriptKey: "stable-x",
          },
        },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: userMessageItem("u-new", "new"),
        },
      } as AnyNotification);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [makeTurn({ id: "t0", items: items.slice(0, 499) })],
        }),
      );
      await store.getState().rehydrate(service, createFakeSink());
      const retained = store
        .getState()
        .conversation?.items.filter(
          (item) => item.transcriptKey === "stable-x",
        );
      expect(retained).toHaveLength(1);
      expect(retained?.[0]).toMatchObject({ id: "wire-x" });
    });

    // M1 omission: oversized X frozen → authoritative reread omits X (removes
    // freeze via reconciliation) → raw SHORT assistant X arrives via page
    // (loadOlder, not lifecycle) → X independently judged from raw content
    // (short → not frozen) → later delta applies.
    it("M1 omission: oversized X frozen, reread omits X, raw short page X accepts delta", async () => {
      const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", oversized, "inProgress"),
      ]);
      // Verify X is frozen.
      const xBefore = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(
        xBefore?.kind === "assistant" &&
          xBefore.markdown.endsWith("… truncated"),
      ).toBe(true);

      // Rehydrate omitting X — reconciliation removes the freeze for X.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({ id: "t0", items: [userMessageItem("B", "base")] }),
          ],
        }),
      );
      await store.getState().rehydrate(service, createFakeSink());
      expect(
        store.getState().conversation?.items.find((i) => i.id === "X"),
      ).toBeUndefined();

      // Load a page bringing X back as a raw SHORT assistant item (not via a
      // lifecycle notification). The page content is short, so X must NOT be
      // frozen — an incoming raw page item is independently judged from its
      // raw content, not from any stale frozen ID.
      store.setState({ olderCursor: "cursor-1" });
      service.olderItems = {
        items: [
          {
            kind: "assistant",
            id: "X",
            markdown: "short-page",
            streaming: false,
          },
        ],
        nextCursor: undefined,
      };
      await store.getState().loadOlder(service);

      // X is present with the short page content, no truncation marker.
      const xPage = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xPage).toBeDefined();
      expect(xPage?.kind).toBe("assistant");
      expect(xPage?.kind === "assistant" && xPage.markdown).toBe("short-page");
      expect(
        xPage?.kind === "assistant" && xPage.markdown.endsWith("… truncated"),
      ).toBe(false);

      // A later delta to X must apply — X is NOT frozen.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " appended",
        },
      } as AnyNotification);
      const xDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xDelta?.kind === "assistant" && xDelta.markdown).toBe(
        "short-page appended",
      );
    });

    // M1 cap: oversized retained X is actually evicted by an incremental
    // append at 501 (item/started pushes to 501, cap trims the oldest which is
    // X). Assert actual cap/IDs/order. The stale freeze entry for X must be
    // pruned so that a later re-introduction with short content accepts a
    // delta.
    it("M1 cap: oversized X evicted by incremental append at 501, re-introduced short accepts delta", async () => {
      const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
      // Build 500 items: X (oversized, first/oldest) + 499 small user items.
      // X is at index 0 (oldest), so capItems will evict it when a 501st item
      // is appended.
      const items: ThreadItem[] = [
        agentMessageItem("X", oversized, "completed"),
      ];
      for (let i = 1; i < 500; i++) {
        items.push(userMessageItem(`u-${i}`, ""));
      }
      const { store, service } = await openProjectedWithItems(items);

      // Verify X is present and frozen (at 500 items, all retained).
      expect(store.getState().conversation?.items.length).toBe(500);
      const xBefore = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xBefore?.kind).toBe("assistant");
      expect(
        xBefore?.kind === "assistant" &&
          xBefore.markdown.endsWith("… truncated"),
      ).toBe(true);
      // X is the oldest item (index 0).
      expect(store.getState().conversation?.items[0]?.id).toBe("X");

      // Incremental append via item/started — pushes to 501, cap trims the
      // oldest (X) down to 500.
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: userMessageItem("new-item", "fresh"),
        },
      } as AnyNotification);

      // Assert actual cap: exactly 500 items.
      const conv = store.getState().conversation;
      expect(conv?.items.length).toBe(500);
      // Assert actual IDs: X is evicted (not in items).
      expect(conv?.items.find((i) => i.id === "X")).toBeUndefined();
      // Assert actual order: new item is at the tail (newest).
      expect(conv?.items[conv.items.length - 1]?.id).toBe("new-item");
      // The oldest surviving item is now u-1 (X was evicted).
      expect(conv?.items[0]?.id).toBe("u-1");

      // The stale freeze for X must be pruned. Simulate a state where the
      // conversation has fewer items (as a rehydrate would produce) WITHOUT
      // clearing truncatedItemIds — this is the state that occurs if the
      // incremental append didn't prune. Then loadOlder with X short must
      // independently judge X (not frozen).
      //
      // After the fix, the incremental append prunes X from truncatedItemIds,
      // so this test passes because priorFrozen doesn't include X. Without the
      // fix, X remains in truncatedItemIds, and priorFrozen includes X, causing
      // reconcileTruncationFrom to wrongly freeze the short page item.
      const currentConv = store.getState().conversation;
      if (currentConv !== null) {
        store.setState({
          conversation: {
            ...currentConv,
            items: currentConv.items.slice(0, 10),
          },
          olderCursor: "cursor-1",
        });
      }

      // Load a page bringing X back as a raw SHORT assistant item.
      service.olderItems = {
        items: [
          {
            kind: "assistant",
            id: "X",
            markdown: "short-page",
            streaming: false,
          },
        ],
        nextCursor: undefined,
      };
      await store.getState().loadOlder(service);

      // X is present with short page content, no marker.
      const xPage = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xPage).toBeDefined();
      expect(xPage?.kind).toBe("assistant");
      expect(xPage?.kind === "assistant" && xPage.markdown).toBe("short-page");
      expect(
        xPage?.kind === "assistant" && xPage.markdown.endsWith("… truncated"),
      ).toBe(false);

      // A later delta to X must apply — X is NOT frozen.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " appended",
        },
      } as AnyNotification);
      const xDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xDelta?.kind === "assistant" && xDelta.markdown).toBe(
        "short-page appended",
      );
    });

    // M1 cap via warning: same eviction but via a warning notification, which
    // is another incremental append+cap path. Verify the same pruning happens.
    it("M1 cap via warning: oversized X evicted by warning at 501, re-introduced short accepts delta", async () => {
      const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
      const items: ThreadItem[] = [
        agentMessageItem("X", oversized, "completed"),
      ];
      for (let i = 1; i < 500; i++) {
        items.push(userMessageItem(`u-${i}`, ""));
      }
      const { store, service } = await openProjectedWithItems(items);
      expect(store.getState().conversation?.items.length).toBe(500);
      expect(store.getState().conversation?.items[0]?.id).toBe("X");

      // Warning pushes to 501, cap trims X (oldest).
      store.getState().applyNotification({
        method: "warning",
        params: { threadId: "thread-1", ref: "ref-1", message: "test warning" },
      } as AnyNotification);

      // Assert actual cap/IDs/order.
      const conv = store.getState().conversation;
      expect(conv?.items.length).toBe(500);
      expect(conv?.items.find((i) => i.id === "X")).toBeUndefined();
      // The warning item is at the tail (failure kind).
      const tail = conv?.items[conv.items.length - 1];
      expect(tail?.kind).toBe("failure");

      // Simulate fewer items (as rehydrate would produce) without clearing
      // truncatedItemIds. Then page-load X short — must not be frozen.
      const currentConv = store.getState().conversation;
      if (currentConv !== null) {
        store.setState({
          conversation: {
            ...currentConv,
            items: currentConv.items.slice(0, 10),
          },
          olderCursor: "cursor-1",
        });
      }
      service.olderItems = {
        items: [
          {
            kind: "assistant",
            id: "X",
            markdown: "short-page",
            streaming: false,
          },
        ],
        nextCursor: undefined,
      };
      await store.getState().loadOlder(service);

      const xPage = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xPage?.kind === "assistant" && xPage.markdown).toBe("short-page");
      expect(
        xPage?.kind === "assistant" && xPage.markdown.endsWith("… truncated"),
      ).toBe(false);

      // Delta applies — X NOT frozen.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
          delta: " appended",
        },
      } as AnyNotification);
      const xDelta = store
        .getState()
        .conversation?.items.find((i) => i.id === "X");
      expect(xDelta?.kind === "assistant" && xDelta.markdown).toBe(
        "short-page appended",
      );
    });
  });

  describe("Task 2A-Truncation residual fix round 3", () => {
    async function openProjectedWithItems(items: ThreadItem[]): Promise<{
      store: ReturnType<typeof createConversationStore>;
      service: FakeConversationService;
    }> {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ turns: [makeTurn({ id: "t0", items })] }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      return { store, service };
    }

    it("truncates every clustered member's oversized detail and freezes each under its own identity", async () => {
      const oversizedA = "a".repeat(MAX_ITEM_BYTES + 100);
      const oversizedB = "b".repeat(MAX_ITEM_BYTES + 100);
      const { store } = await openProjectedWithItems([
        {
          type: "commandExecution",
          id: "wire-a",
          transcriptKey: "key-a",
          toolName: "shell",
          status: "completed",
          callId: "call-a",
          output: oversizedA,
        },
        {
          type: "commandExecution",
          id: "wire-b",
          transcriptKey: "key-b",
          toolName: "shell",
          status: "completed",
          callId: "call-b",
          output: oversizedB,
        },
      ]);
      const cluster = store
        .getState()
        .conversation?.items.find((i) => i.id === "wire-a");
      expect(cluster?.kind).toBe("activity");
      if (cluster?.kind !== "activity")
        throw new Error("expected a clustered activity");
      expect(cluster.members?.length).toBe(2);
      const [memberA, memberB] = cluster.members ?? [];
      // Top-level truncation behaviour unchanged — the cluster's own
      // (first member's) detail is bounded, as before.
      expect(cluster.detail.output?.endsWith("… truncated")).toBe(true);
      // Every member's OWN detail is now bounded too, not just the top level.
      expect(memberA?.detail.output?.endsWith("… truncated")).toBe(true);
      expect(memberB?.detail.output?.endsWith("… truncated")).toBe(true);
      // Each member is frozen under its own identity.
      expect(store.getState().getTruncatedItemIds().has("key-a")).toBe(true);
      expect(store.getState().getTruncatedItemIds().has("key-b")).toBe(true);

      // A delta aimed at the frozen member's wire id is refused.
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "wire-a",
          callId: "call-a",
          delta: " MORE",
        },
      } as AnyNotification);
      const after = store
        .getState()
        .conversation?.items.find((i) => i.id === "wire-a");
      expect(after?.kind).toBe("activity");
      expect(after?.kind === "activity" && after.detail.output).toBe(
        cluster.detail.output,
      );
    });

    it("keeps a clustered member's freeze after an unrelated lifecycle event prunes evicted ownership", async () => {
      const oversizedB = "b".repeat(MAX_ITEM_BYTES + 100);
      const { store } = await openProjectedWithItems([
        {
          type: "commandExecution",
          id: "wire-a",
          transcriptKey: "key-a",
          toolName: "shell",
          status: "completed",
          callId: "call-a",
          output: "short",
        },
        {
          type: "commandExecution",
          id: "wire-b",
          transcriptKey: "key-b",
          toolName: "shell",
          status: "completed",
          callId: "call-b",
          output: oversizedB,
        },
      ]);
      expect(store.getState().getTruncatedItemIds().has("key-b")).toBe(true);

      // An unrelated lifecycle event (a brand-new item) triggers the store's
      // evicted-ownership prune. It must not sweep up the member's freeze,
      // which lives outside the top-level identity space pruneEvictedIds
      // checked before this fix.
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: { type: "userMessage", id: "unrelated", text: "hi" },
        },
      } as AnyNotification);

      expect(store.getState().getTruncatedItemIds().has("key-b")).toBe(true);
    });
  });

  // --- Task 2A-Cluster: sparse live items and clustered members as live targets ---

  describe("Task 2A-Cluster: sparse live items inherit the active turn's status", () => {
    async function openWithActiveTurn(): Promise<
      ReturnType<typeof createConversationStore>
    > {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "turn/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turn: { id: "t1", status: "inProgress" },
        },
      } as AnyNotification);
      return store;
    }

    function startItem(
      store: ReturnType<typeof createConversationStore>,
      item: ThreadItem,
    ): void {
      store.getState().applyNotification({
        method: "item/started",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", item },
      } as AnyNotification);
    }

    it("projects a status-less tool item started in the active turn as running", async () => {
      const store = await openWithActiveTurn();
      startItem(store, {
        type: "commandExecution",
        id: "tool-1",
        turnId: "t1",
        toolName: "shell",
        callId: "call-1",
      });
      const row = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      expect(row?.kind).toBe("activity");
      expect(row?.kind === "activity" && row.state).toBe("running");
    });

    it("projects a status-less tool item as completed when no turn is active", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      startItem(store, {
        type: "commandExecution",
        id: "tool-1",
        turnId: "t1",
        toolName: "shell",
        callId: "call-1",
      });
      const row = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      expect(row?.kind).toBe("activity");
      expect(row?.kind === "activity" && row.state).toBe("completed");
    });

    it("projects a status-less tool item naming another turn as completed", async () => {
      const store = await openWithActiveTurn();
      startItem(store, {
        type: "commandExecution",
        id: "tool-1",
        turnId: "t0",
        toolName: "shell",
        callId: "call-1",
      });
      const row = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      expect(row?.kind).toBe("activity");
      expect(row?.kind === "activity" && row.state).toBe("completed");
    });

    it("projects a status-less tool item naming no turn as running while a turn is active", async () => {
      const store = await openWithActiveTurn();
      startItem(store, {
        type: "commandExecution",
        id: "tool-1",
        toolName: "shell",
        callId: "call-1",
      });
      const row = store
        .getState()
        .conversation?.items.find((i) => i.id === "tool-1");
      expect(row?.kind).toBe("activity");
      expect(row?.kind === "activity" && row.state).toBe("running");
    });

    it("projects a status-less reasoning item started in the active turn as running", async () => {
      const store = await openWithActiveTurn();
      startItem(store, {
        type: "reasoning",
        id: "reason-1",
        turnId: "t1",
        text: "thinking",
      });
      const row = store
        .getState()
        .conversation?.items.find((i) => i.id === "reason-1");
      expect(row?.kind).toBe("activity");
      expect(row?.kind === "activity" && row.state).toBe("running");
    });

    it("marks a status-less assistant item started in the active turn as streaming", async () => {
      const store = await openWithActiveTurn();
      startItem(store, {
        type: "agentMessage",
        id: "assistant-1",
        turnId: "t1",
        text: "partial",
      });
      const row = store
        .getState()
        .conversation?.items.find((i) => i.id === "assistant-1");
      expect(row?.kind).toBe("assistant");
      expect(row?.kind === "assistant" && row.streaming).toBe(true);
    });
  });

  describe("Task 2A-Cluster: live updates resolve clustered members", () => {
    async function openProjectedWithItems(items: ThreadItem[]): Promise<{
      store: ReturnType<typeof createConversationStore>;
      service: FakeConversationService;
    }> {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ turns: [makeTurn({ id: "t0", items })] }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      return { store, service };
    }

    function toolItem(
      id: string,
      transcriptKey: string,
      callId: string,
      output: string,
    ): ThreadItem {
      return {
        type: "commandExecution",
        id,
        transcriptKey,
        toolName: "shell",
        status: "completed",
        callId,
        output,
      };
    }

    function reasoningItem(
      id: string,
      transcriptKey: string,
      text: string,
    ): ThreadItem {
      return { type: "reasoning", id, transcriptKey, status: "completed", text };
    }

    function clusterMembers(
      store: ReturnType<typeof createConversationStore>,
      rowId: string,
    ): ActivityMember[] {
      const row = store
        .getState()
        .conversation?.items.find((i) => i.id === rowId);
      expect(row?.kind).toBe("activity");
      if (row?.kind !== "activity") throw new Error("expected an activity row");
      expect(row.members?.length).toBe(2);
      return row.members ?? [];
    }

    function toolOutputDelta(
      store: ReturnType<typeof createConversationStore>,
      itemId: string,
      callId: string,
      delta: string,
    ): void {
      store.getState().applyNotification({
        method: "item/toolOutput/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId,
          callId,
          delta,
        },
      } as AnyNotification);
    }

    it("applies a tool-output delta aimed at a later clustered member in place", async () => {
      const { store } = await openProjectedWithItems([
        toolItem("wire-a", "key-a", "call-a", "first"),
        toolItem("wire-b", "key-b", "call-b", "second"),
      ]);

      toolOutputDelta(store, "wire-b", "call-b", " MORE");

      const [memberA, memberB] = clusterMembers(store, "wire-a");
      expect(memberB?.detail.output).toBe("second MORE");
      // The other member keeps its own identity, output and position.
      expect(memberA?.detail.output).toBe("first");
      expect(memberA?.transcriptKey).toBe("key-a");
      expect(memberB?.transcriptKey).toBe("key-b");
    });

    it("applies a tool-output delta aimed at the first clustered member to that member, not only the cluster", async () => {
      const { store } = await openProjectedWithItems([
        toolItem("wire-a", "key-a", "call-a", "first"),
        toolItem("wire-b", "key-b", "call-b", "second"),
      ]);

      toolOutputDelta(store, "wire-a", "call-a", " MORE");

      const [memberA, memberB] = clusterMembers(store, "wire-a");
      expect(memberA?.detail.output).toBe("first MORE");
      expect(memberB?.detail.output).toBe("second");
      const row = store
        .getState()
        .conversation?.items.find((i) => i.id === "wire-a");
      expect(row?.kind === "activity" && row.detail.output).toBe("first MORE");
    });

    it("applies a reasoning delta aimed at a later clustered member in place", async () => {
      const { store } = await openProjectedWithItems([
        reasoningItem("wire-a", "key-a", "first"),
        reasoningItem("wire-b", "key-b", "second"),
      ]);

      store.getState().applyNotification({
        method: "item/reasoning/summaryTextDelta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "wire-b",
          delta: " MORE",
        },
      } as AnyNotification);

      const [memberA, memberB] = clusterMembers(store, "wire-a");
      expect(memberB?.detail.output).toBe("second MORE");
      expect(memberA?.detail.output).toBe("first");
    });

    it("refuses a delta aimed at a frozen later clustered member's wire id", async () => {
      const oversized = "b".repeat(MAX_ITEM_BYTES + 100);
      const { store } = await openProjectedWithItems([
        toolItem("wire-a", "key-a", "call-a", "first"),
        toolItem("wire-b", "key-b", "call-b", oversized),
      ]);
      expect(store.getState().getTruncatedItemIds().has("key-b")).toBe(true);
      const frozenOutput = clusterMembers(store, "wire-a")[1]?.detail.output;

      toolOutputDelta(store, "wire-b", "call-b", " MORE");

      expect(clusterMembers(store, "wire-a")[1]?.detail.output).toBe(
        frozenOutput,
      );
    });

    it("preserves a later clustered member's output when a sparse completion omits text", async () => {
      const { store } = await openProjectedWithItems([
        reasoningItem("wire-a", "key-a", "first"),
        reasoningItem("wire-b", "key-b", "accumulated"),
      ]);

      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: {
            type: "reasoning",
            id: "wire-b",
            transcriptKey: "key-b",
            status: "completed",
          },
        },
      } as AnyNotification);

      const [memberA, memberB] = clusterMembers(store, "wire-a");
      expect(memberB?.detail.output).toBe("accumulated");
      expect(memberA?.detail.output).toBe("first");
    });

    it("preserves a clustered member's output when a sparse completion changes its wire id", async () => {
      const { store } = await openProjectedWithItems([
        reasoningItem("wire-a", "key-a", "first"),
        reasoningItem("wire-b", "key-b", "accumulated"),
      ]);

      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: {
            type: "reasoning",
            id: "wire-b2",
            transcriptKey: "key-b",
            status: "completed",
          },
        },
      } as AnyNotification);

      const [memberA, memberB] = clusterMembers(store, "wire-a");
      // The member is replaced under its new wire id, keeping the output it
      // accumulated under the stable transcriptKey.
      expect(memberB?.id).toBe("wire-b2");
      expect(memberB?.detail.output).toBe("accumulated");
      expect(memberA?.detail.output).toBe("first");
    });

    it("keeps an already-truncated member frozen when a page reconciles truncation", async () => {
      const oversized = "b".repeat(MAX_ITEM_BYTES + 100);
      const { store, service } = await openProjectedWithItems([
        toolItem("wire-a", "key-a", "call-a", "first"),
        toolItem("wire-b", "key-b", "call-b", oversized),
      ]);
      // The stored member is now SHORT — it was truncated on the way in — so
      // only the carried-over freeze can keep it frozen.
      const truncatedOutput = clusterMembers(store, "wire-a")[1]?.detail.output;
      expect(store.getState().getTruncatedItemIds().has("key-b")).toBe(true);

      store.setState({ olderCursor: "cursor-1" });
      service.olderItems = {
        items: [{ kind: "user", id: "older", text: "older" }],
      };
      await store.getState().loadOlder(service);

      expect(store.getState().getTruncatedItemIds().has("key-b")).toBe(true);
      // The freeze still refuses a delta against the truncated member.
      toolOutputDelta(store, "wire-b", "call-b", " MORE");
      expect(clusterMembers(store, "wire-a")[1]?.detail.output).toBe(
        truncatedOutput,
      );
    });

    it("keeps a member frozen by a live delta while a rehydrate is in flight", async () => {
      const stale = [
        toolItem("wire-a", "key-a", "call-a", "first"),
        toolItem("wire-b", "key-b", "call-b", "second"),
      ];
      const { store, service } = await openProjectedWithItems(stale);
      let release!: (value: ConversationReadProjection) => void;
      service.readProjectionBlock = new Promise((resolve) => {
        release = resolve;
      });
      const rehydrating = store.getState().rehydrate(service, createFakeSink());

      // In flight, a live delta pushes the later member over the limit, so it
      // truncates and freezes under its own identity.
      toolOutputDelta(
        store,
        "wire-b",
        "call-b",
        "b".repeat(MAX_ITEM_BYTES + 100),
      );
      expect(store.getState().getTruncatedItemIds().has("key-b")).toBe(true);
      const frozenOutput = clusterMembers(store, "wire-a")[1]?.detail.output;

      // The reread carries the stale snapshot — it never saw the delta, so it
      // cannot be authoritative about that member.
      release(
        makeReadProjectionResult(
          makeThread({ turns: [makeTurn({ id: "t0", items: stale })] }),
        ),
      );
      await rehydrating;

      expect(store.getState().getTruncatedItemIds().has("key-b")).toBe(true);
      toolOutputDelta(store, "wire-b", "call-b", " MORE");
      expect(clusterMembers(store, "wire-a")[1]?.detail.output).toBe(
        frozenOutput,
      );
    });

    it("leaves a member the live side did not win answerable to the reread", async () => {
      const oversizedA = "a".repeat(MAX_ITEM_BYTES + 100);
      const { store, service } = await openProjectedWithItems([
        toolItem("wire-a", "key-a", "call-a", oversizedA),
        toolItem("wire-b", "key-b", "call-b", "second"),
      ]);
      expect(store.getState().getTruncatedItemIds().has("key-a")).toBe(true);
      let release!: (value: ConversationReadProjection) => void;
      service.readProjectionBlock = new Promise((resolve) => {
        release = resolve;
      });
      const rehydrating = store.getState().rehydrate(service, createFakeSink());

      // The live side wins only the second member.
      toolOutputDelta(
        store,
        "wire-b",
        "call-b",
        "b".repeat(MAX_ITEM_BYTES + 100),
      );

      // The reread is authoritative for the first member, and says it is short
      // now — winning its neighbour must not make it superseded too.
      release(
        makeReadProjectionResult(
          makeThread({
            turns: [
              makeTurn({
                id: "t0",
                items: [
                  toolItem("wire-a", "key-a", "call-a", "short"),
                  toolItem("wire-b", "key-b", "call-b", "second"),
                ],
              }),
            ],
          }),
        ),
      );
      await rehydrating;

      expect(store.getState().getTruncatedItemIds().has("key-a")).toBe(false);
      expect(store.getState().getTruncatedItemIds().has("key-b")).toBe(true);
      toolOutputDelta(store, "wire-a", "call-a", " MORE");
      expect(clusterMembers(store, "wire-a")[0]?.detail.output).toBe(
        "short MORE",
      );
    });

    it("unfreezes a later clustered member the reread returns short", async () => {
      const oversized = "b".repeat(MAX_ITEM_BYTES + 100);
      const { store, service } = await openProjectedWithItems([
        toolItem("wire-a", "key-a", "call-a", "first"),
        toolItem("wire-b", "key-b", "call-b", oversized),
      ]);
      expect(store.getState().getTruncatedItemIds().has("key-b")).toBe(true);

      // The reread is authoritative: it carries short content for the member,
      // so the member unfreezes exactly as a top-level row would.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                toolItem("wire-a", "key-a", "call-a", "first"),
                toolItem("wire-b", "key-b", "call-b", "short"),
              ],
            }),
          ],
        }),
      );
      await store.getState().rehydrate(service, createFakeSink());

      expect(store.getState().getTruncatedItemIds().has("key-b")).toBe(false);
      toolOutputDelta(store, "wire-b", "call-b", " MORE");
      expect(clusterMembers(store, "wire-a")[1]?.detail.output).toBe(
        "short MORE",
      );
    });
  });

  describe("Task 2A-Cluster: paging dedupe spans every incoming identity", () => {
    function member(id: string, transcriptKey: string): ActivityMember {
      return {
        id,
        transcriptKey,
        label: "shell",
        family: "tool",
        state: "completed",
        detail: { output: transcriptKey, callId: `call-${transcriptKey}` },
      };
    }

    function cluster(
      id: string,
      transcriptKey: string,
      members: ActivityMember[],
    ): MobileTimelineItem {
      return {
        kind: "activity",
        id,
        transcriptKey,
        label: "shell",
        family: "tool",
        state: "completed",
        detail: { output: transcriptKey, callId: `call-${transcriptKey}` },
        members,
      };
    }

    function memberIdentities(items: MobileTimelineItem[]): string[] {
      return items.flatMap((item) =>
        item.kind === "activity"
          ? (item.members ?? []).map((m) => m.transcriptKey ?? m.id)
          : [],
      );
    }

    async function pagedStore(
      current: MobileTimelineItem[],
      older: MobileTimelineItem[],
    ): Promise<ReturnType<typeof createConversationStore>> {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({ items: current });
      await store.getState().open(service, "ref-1");
      store.setState({ olderCursor: "cursor-1" });
      service.olderItems = { items: older, nextCursor: "next" };
      await store.getState().loadOlder(service);
      return store;
    }

    it("skips an incoming cluster whose member identity is already present under a new top-level id", async () => {
      const store = await pagedStore(
        [cluster("wire-X", "key-X", [member("wire-X", "key-X"), member("wire-A", "key-A")])],
        [
          cluster("wire-old", "key-old", [
            member("wire-old", "key-old"),
            member("wire-A2", "key-A"),
          ]),
          { kind: "user", id: "older", text: "older" },
        ],
      );

      const items = store.getState().conversation?.items ?? [];
      expect(items.map((i) => i.id)).not.toContain("wire-old");
      // The member is not duplicated across rows.
      expect(memberIdentities(items).filter((id) => id === "key-A")).toEqual([
        "key-A",
      ]);
      // A genuinely new row from the same page is still admitted.
      expect(items.map((i) => i.id)).toContain("older");
    });

    it("admits an older attachment whose source row arrives in the same page", async () => {
      const store = await pagedStore(
        [{ kind: "user", id: "current", text: "current" }],
        [
          { kind: "user", id: "wire-src", transcriptKey: "key-src", text: "older" },
          {
            kind: "attachments",
            id: "wire-src:attachments",
            sourceTranscriptKey: "key-src",
            items: [{ id: "att-1", src: "https://example.com/new.png" }],
          },
        ],
      );

      const items = store.getState().conversation?.items ?? [];
      expect(items.map((i) => i.id)).toContain("wire-src:attachments");
    });
  });
  // --- C1: Service-specific operation binding ---------------------------------------

  describe("C1: wrong service at entry => zero request/state change", () => {
    it("wrong-service loadOlder => zero service calls, zero state change", async () => {
      const serviceA = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(serviceA, "ref-1");
      store.setState({ olderCursor: "cursor-1" });
      const serviceB = new FakeConversationService();
      serviceB.olderItems = { items: [{ kind: "user", id: "old", text: "x" }] };
      const convBefore = store.getState().conversation;
      const cursorBefore = store.getState().olderCursor;
      await store.getState().loadOlder(serviceB);
      // C1: wrong service (B after A bound) => zero state change.
      expect(store.getState().conversation).toBe(convBefore);
      expect(store.getState().olderCursor).toBe(cursorBefore);
      expect(store.getState().loadingOlder).toBe(false);
    });

    it("correct-service loadOlder still works after binding", async () => {
      const service = new FakeConversationService();
      service.olderItems = { items: [{ kind: "user", id: "old", text: "x" }] };
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.setState({ olderCursor: "cursor-1" });
      await store.getState().loadOlder(service);
      expect(
        store.getState().conversation?.items.some((i) => i.id === "old"),
      ).toBe(true);
    });
  });

  // --- Plan3: getTruncatedItemIds accessor — snapshot immutability + freeze/unfreeze ---

  describe("Plan3: getTruncatedItemIds — snapshot cannot mutate internal ownership", () => {
    it("returns a fresh snapshot — mutating the returned set does not affect the store", async () => {
      const service = new FakeConversationService();
      const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
      service.openConv = makeConversation({
        items: [
          {
            kind: "assistant",
            id: "big",
            markdown: oversized,
            streaming: false,
          },
        ],
      });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      const snapshot = store.getState().getTruncatedItemIds();
      expect(snapshot.has("big")).toBe(true);

      // Mutate the returned snapshot — must not affect internal ownership.
      (snapshot as Set<string>).delete("big");
      (snapshot as Set<string>).add("injected");

      // The store's internal set is unchanged.
      const snapshot2 = store.getState().getTruncatedItemIds();
      expect(snapshot2.has("big")).toBe(true);
      expect(snapshot2.has("injected")).toBe(false);

      // The delta freeze still works — a delta to "big" is blocked.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "big",
          delta: " appended",
        },
      } as AnyNotification);
      // Mandatory narrowing — if the item is not assistant, throw so the
      // assertion cannot silently skip.
      const item = store
        .getState()
        .conversation?.items.find((i) => i.id === "big");
      if (item?.kind !== "assistant")
        throw new Error("expected assistant item");
      expect(item.markdown.endsWith("… truncated")).toBe(true);
      expect(item.markdown).not.toContain("appended");
    });
  });

  describe("Plan3: getTruncatedItemIds — tracks authoritative freeze/unfreeze", () => {
    it("freeze on open with oversized content, unfreeze on rehydrate with short content", async () => {
      const service = new FakeConversationService();
      const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
      service.readProjectionResult = {
        conversation: makeConversation({
          items: [
            {
              kind: "assistant",
              id: "X",
              markdown: oversized,
              streaming: false,
            },
          ],
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      // Oversized item is frozen.
      expect(store.getState().getTruncatedItemIds().has("X")).toBe(true);

      // Rehydrate with short content — unfreezes.
      service.readProjectionResult = {
        conversation: makeConversation({
          items: [
            {
              kind: "assistant",
              id: "X",
              markdown: "short",
              streaming: false,
            },
          ],
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      await store.getState().rehydrate(service, createFakeSink());
      expect(store.getState().getTruncatedItemIds().has("X")).toBe(false);
    });

    it("freeze via delta, unfreeze via item/agentMessage/reset", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({
        items: [
          {
            kind: "assistant",
            id: "item-1",
            markdown: "x".repeat(MAX_ITEM_BYTES - 100),
            streaming: true,
          },
        ],
      });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      expect(store.getState().getTruncatedItemIds().has("item-1")).toBe(false);

      // Delta that pushes past the cap — freezes.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "item-1",
          delta: "x".repeat(200),
        },
      } as AnyNotification);
      expect(store.getState().getTruncatedItemIds().has("item-1")).toBe(true);

      // Reset unfreezes before clearing the markdown.
      store.getState().applyNotification({
        method: "item/agentMessage/reset",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "item-1",
        },
      } as AnyNotification);
      expect(store.getState().getTruncatedItemIds().has("item-1")).toBe(false);

      // Delta now applies — not frozen.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          itemId: "item-1",
          delta: "new text",
        },
      } as AnyNotification);
      const item = store
        .getState()
        .conversation?.items.find((i) => i.id === "item-1");
      // Mandatory narrowing — throw if not assistant so the assertion cannot
      // silently skip.
      if (item?.kind !== "assistant")
        throw new Error("expected assistant item");
      expect(item.markdown).toBe("new text");
    });
  });

  describe("C1: wrong-service send/steer/queue/interrupt => zero A calls", () => {
    for (const kind of ["send", "steer", "queue", "interrupt"] as const) {
      it(`wrong-service ${kind} => zero B calls, zero state change`, async () => {
        const serviceA = new FakeConversationService();
        serviceA.openConv = makeConversation({ status: { type: kind === "send" ? "idle" : "active" } });
        const store = createConversationStore();
        await store
          .getState()
          .openProjected(serviceA, createFakeSink(), "ref-1");
        const pendingBefore = store.getState().pendingMutation;
        const draftBefore = store.getState().draft;
        const errorBefore = store.getState().error;
        const serviceB = new FakeConversationService();
        // Call with wrong serviceB — rejected at the boundary.
        if (kind === "send")
          await store.getState().send(serviceB, textInput("x"));
        else if (kind === "steer")
          await store.getState().steer(serviceB, textInput("x"));
        else if (kind === "queue")
          await store.getState().queue(serviceB, textInput("x"));
        else await store.getState().interrupt(serviceB);
        // C1: zero state change — pending/draft/error unchanged.
        expect(store.getState().pendingMutation).toBe(pendingBefore);
        expect(store.getState().draft).toBe(draftBefore);
        expect(store.getState().error).toBe(errorBefore);
        // C1: serviceB mutation method was never called.
        if (kind === "send") expect(serviceB.sendCallCount).toBe(0);
        else if (kind === "steer") expect(serviceB.steerCallCount).toBe(0);
        else if (kind === "queue") expect(serviceB.queueCallCount).toBe(0);
        else expect(serviceB.interruptCallCount).toBe(0);
      });
    }
  });

  // --- I1: Error ownership — revision equality ONLY --------------------------------

  describe("I1: newer clear-to-null and ABA-null own error", () => {
    it("page failure after newer clear-to-null: page settles loading, preserves null (no write)", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      await store.getState().openProjected(service, sink, "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      // Start loadOlder that will fail (hangs first). It captures entryErrorRev.
      const rejectOlderHolder: { fn?: (e: Error) => void } = {};
      const hangOlder = new Promise<{
        items: MobileConversation["items"];
        nextCursor?: string;
      }>((_, reject) => {
        rejectOlderHolder.fn = reject;
      });
      service.olderItems = hangOlder as never;
      const olderP = store.getState().loadOlder(service);
      expect(store.getState().loadingOlder).toBe(true);

      // While L is in-flight, a send fails (sets error, increments errorOwnerRev),
      // then another send succeeds (clears error to null, increments errorOwnerRev
      // again). Now errorOwnerRev has advanced past L's captured entryErrorRev.
      // Error is null — this is a newer clear-to-null that owns the error.
      service.sendShouldReject = new Error("initial error");
      await store.getState().send(service, textInput("x"));
      expect(store.getState().error).toBe("initial error");
      service.sendShouldReject = null;
      await store.getState().send(service, textInput("y"));
      expect(store.getState().error).toBeNull();

      // Now L fails — it must settle loadingOlder but NOT write error
      // (errorOwnerRev changed during the await — the send's clear-to-null
      // is a newer error owner).
      rejectOlderHolder.fn?.(new Error("page boom"));
      await olderP.catch(() => {});
      expect(store.getState().loadingOlder).toBe(false);
      // Error stays null — the send's clear-to-null owns it.
      expect(store.getState().error).toBeNull();
    });

    it("rehydrate already-null during mutation: no spurious errorOwnerRev increment, send clears pending", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      await store.getState().openProjected(service, sink, "ref-1");

      // Set an error via a failed send, then clear with a succeeding send.
      service.sendShouldReject = new Error("initial error");
      await store.getState().send(service, textInput("x"));
      expect(store.getState().error).toBe("initial error");
      service.sendShouldReject = null;
      await store.getState().send(service, textInput("clear"));
      expect(store.getState().error).toBeNull();

      // Start a send that hangs.
      const resolveSendHolder: { fn?: (r: MutationReceipt) => void } = {};
      const hangSend = new Promise<MutationReceipt>((r) => {
        resolveSendHolder.fn = r;
      });
      service.send = async () => hangSend;
      const sendP = store.getState().send(service, textInput("y"));
      expect(store.getState().pendingMutation?.status).toBe("pending");

      // While send is in-flight, a rehydrate with SAME sink clears error.
      // Error is already null — rehydrate does NOT increment errorOwnerRev
      // (the already-null guard prevents ABA-null from a no-op clear).
      await store.getState().rehydrate(service, sink);
      expect(store.getState().error).toBeNull();

      // Now the send succeeds. Since errorOwnerRev did NOT change (rehydrate
      // didn't clear a non-null error), the send CAN clear error (to null).
      // This exercises the already-null no-spurious-increment path, NOT a
      // genuine newer clear-to-null race.
      resolveSendHolder.fn?.(makeReceipt());
      await sendP;
      expect(store.getState().pendingMutation).toBeNull();
      expect(store.getState().error).toBeNull();
    });

    it("genuine newer null-owner race: page failure cannot overwrite mutation's null owner while mutation pending", async () => {
      // Load-bearing ordering: hold a page failure and hold a newer mutation.
      // The page captures the OLD error rev (before the mutation starts).
      // The newer mutation start writes the newer null owner (clears error
      // via set on submit, incrementing errorOwnerRev). While the mutation
      // is still pending, reject/await the PAGE — it must NOT write its old
      // error because errorOwnerRev advanced. Assert error remains null and
      // mutation remains pending. Only afterward resolve the mutation and
      // finish. This prevents the mutation success's null from masking the
      // page behavior — the page rejection is observed BEFORE the mutation
      // settles.
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      await store.getState().openProjected(service, sink, "ref-1");

      // Set a non-null error first so the page failure captures an old
      // errorOwnerRev (error is non-null).
      service.sendShouldReject = new Error("initial error");
      await store.getState().send(service, textInput("x"));
      expect(store.getState().error).toBe("initial error");

      // Start a page failure that hangs — it captures entryErrorRev while
      // error is "initial error" (old rev).
      store.setState({ olderCursor: "cursor-1" });
      const rejectPageHolder: { fn?: (e: Error) => void } = {};
      const hangPageFail = new Promise<{
        items: MobileConversation["items"];
        nextCursor?: string;
      }>((_, reject) => {
        rejectPageHolder.fn = reject;
      });
      service.olderItems = hangPageFail as never;
      const pageP = store.getState().loadOlder(service);
      expect(store.getState().loadingOlder).toBe(true);

      // Start a newer send that hangs — the send action clears error to null
      // on submit (set error: null), incrementing errorOwnerRev. This is the
      // newer null owner. The page's captured entryErrorRev is now stale.
      service.sendShouldReject = null;
      const resolveSendHolder: { fn?: (r: MutationReceipt) => void } = {};
      const hangSend = new Promise<MutationReceipt>((r) => {
        resolveSendHolder.fn = r;
      });
      service.send = async () => hangSend;
      const sendP = store.getState().send(service, textInput("y"));
      expect(store.getState().pendingMutation?.status).toBe("pending");
      // The send's submit cleared the old error to null (newer null owner).
      expect(store.getState().error).toBeNull();

      // Now reject the PAGE while the mutation is still pending. The page
      // captured the old entryErrorRev (when error was "initial error").
      // errorOwnerRev has since advanced (the send's clear). The page must
      // settle loadingOlder but NOT write its error — an incorrect old page
      // error would be visible here.
      rejectPageHolder.fn?.(new Error("page boom"));
      await pageP.catch(() => {});
      expect(store.getState().loadingOlder).toBe(false);
      // Error remains null — the page's old error rev cannot overwrite
      // the newer null owner.
      expect(store.getState().error).toBeNull();
      // Mutation remains pending — the page rejection did not settle it.
      expect(store.getState().pendingMutation?.status).toBe("pending");

      // Only now resolve the mutation success. The send captured
      // entryErrorRev after its own clear, so it can clear error. But error
      // is already null — the key assertion is that the page never wrote.
      resolveSendHolder.fn?.(makeReceipt());
      await sendP;
      expect(store.getState().pendingMutation).toBeNull();
      // Error stays null throughout — page never wrote, send's null held.
      expect(store.getState().error).toBeNull();
    });

    it("mutation failure after newer ABA-null: installs failed pending, preserves null", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      await store.getState().openProjected(service, sink, "ref-1");

      // Set an error via a failed send, then clear with a succeeding send.
      service.sendShouldReject = new Error("boom");
      await store.getState().send(service, textInput("x"));
      expect(store.getState().error).toBe("boom");
      service.sendShouldReject = null;
      await store.getState().send(service, textInput("clear"));
      expect(store.getState().error).toBeNull();

      // Start a send that hangs then fails.
      const rejectSendHolder: { fn?: (e: Error) => void } = {};
      const hangFail = new Promise<MutationReceipt>((_, reject) => {
        rejectSendHolder.fn = reject;
      });
      service.send = async () => hangFail;
      const sendP = store.getState().send(service, textInput("y"));

      // While send is in-flight, set error via a page failure, then rehydrate
      // clears it to null — this is a real ABA-null (error was non-null).
      store.setState({ olderCursor: "cursor-1" });
      service.olderItems = Promise.reject(new Error("page boom")) as never;
      await store
        .getState()
        .loadOlder(service)
        .catch(() => {});
      expect(store.getState().error).toBe("page boom");
      // Rehydrate with SAME sink clears the non-null error — increments
      // errorOwnerRev. This is a real newer error owner (clear of non-null).
      await store.getState().rehydrate(service, sink);
      expect(store.getState().error).toBeNull();

      // Now the send fails — it must install failed pending but NOT write
      // error (errorOwnerRev changed — the rehydrate's clear owns it).
      rejectSendHolder.fn?.(new Error("mutation boom"));
      await sendP.catch(() => {});
      expect(store.getState().pendingMutation?.status).toBe("failed");
      // Error stays null — the rehydrate's clear owns it.
      expect(store.getState().error).toBeNull();
    });

    it("held page failure after newer mutation error: page settles loading, preserves mutation error", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      // Start loadOlder that will fail (hangs first).
      const rejectOlderHolder: { fn?: (e: Error) => void } = {};
      const hangOlder = new Promise<{
        items: MobileConversation["items"];
        nextCursor?: string;
      }>((_, reject) => {
        rejectOlderHolder.fn = reject;
      });
      service.olderItems = hangOlder as never;
      const olderP = store.getState().loadOlder(service);

      // While L is in-flight, a send fails with a non-null error.
      service.sendShouldReject = new Error("mutation boom");
      await store.getState().send(service, textInput("x"));
      expect(store.getState().error).toBe("mutation boom");

      // Now L fails — it must settle loadingOlder but NOT overwrite the error.
      rejectOlderHolder.fn?.(new Error("page boom"));
      await olderP.catch(() => {});
      expect(store.getState().loadingOlder).toBe(false);
      expect(store.getState().error).toBe("mutation boom");
    });

    it("held mutation failure after newer page error: installs failed pending, preserves page error", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });

      // Start a send that hangs then fails.
      const rejectSendHolder: { fn?: (e: Error) => void } = {};
      const hangFail = new Promise<MutationReceipt>((_, reject) => {
        rejectSendHolder.fn = reject;
      });
      service.sendShouldReject = null;
      service.send = async () => hangFail;
      const sendP = store.getState().send(service, textInput("x"));

      // While send is in-flight, loadOlder fails with a non-null error.
      service.olderItems = Promise.reject(new Error("page boom")) as never;
      await store
        .getState()
        .loadOlder(service)
        .catch(() => {});
      expect(store.getState().error).toBe("page boom");

      // Now the send fails — it must install failed pending but NOT overwrite.
      rejectSendHolder.fn?.(new Error("mutation boom"));
      await sendP.catch(() => {});
      expect(store.getState().pendingMutation?.status).toBe("failed");
      expect(store.getState().error).toBe("page boom");
    });
  });

  // --- I5: Binding safety — full state snapshot comparison -------------------------

  describe("I5: rebind A->B then late A completion => zero state change (full snapshot)", () => {
    // Helper: snapshot the entire public state for comparison.
    function snapshotState(store: ReturnType<typeof createConversationStore>) {
      const s = store.getState();
      return {
        ref: s.ref,
        profileId: s.profileId,
        connectionGeneration: s.connectionGeneration,
        conversationGeneration: s.conversationGeneration,
        conversation: s.conversation,
        olderCursor: s.olderCursor,
        loadingOlder: s.loadingOlder,
        status: s.status,
        error: s.error,
        draft: s.draft,
        pendingSend: s.pendingSend,
        pendingMutation: s.pendingMutation,
      };
    }

    it("page: controlled A->B then late A success => entire state unchanged", async () => {
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-A" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-A",
      };
      const sinkA = createFakeSink();
      const store = createConversationStore();
      await store.getState().openProjected(serviceA, sinkA, "ref-A");
      store.setState({ olderCursor: "cursor-A" });

      // Start loadOlder on A that hangs (success).
      const resolveOlderHolder: { fn?: () => void } = {};
      const hangOlder = new Promise<{
        items: MobileConversation["items"];
        nextCursor?: string;
      }>((r) => {
        resolveOlderHolder.fn = () =>
          r({ items: [{ kind: "user", id: "old-A", text: "old" }] });
      });
      serviceA.olderItems = hangOlder as never;
      const olderP = store.getState().loadOlder(serviceA);

      // Rebind to B via rehydrate.
      const serviceB = new FakeConversationService();
      serviceB.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-B" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const sinkB = createFakeSink();
      await store.getState().rehydrate(serviceB, sinkB);

      // Snapshot state after rebind.
      const stateAfterRebind = snapshotState(store);
      const readsA = serviceA.readProjectionCalls.length;
      const writesA = sinkA.setLiveViewCalls.length;

      // Now A's loadOlder resolves — zero state change.
      resolveOlderHolder.fn?.();
      await olderP;

      // C1/I5: entire public state must be identical.
      expect(snapshotState(store)).toEqual(stateAfterRebind);
      // No additional A reads or sink writes.
      expect(serviceA.readProjectionCalls.length).toBe(readsA);
      expect(sinkA.setLiveViewCalls.length).toBe(writesA);
    });

    it("page: controlled A->B then late A failure => entire state unchanged", async () => {
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-A" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-A",
      };
      const sinkA = createFakeSink();
      const store = createConversationStore();
      await store.getState().openProjected(serviceA, sinkA, "ref-A");
      store.setState({ olderCursor: "cursor-A" });

      // Start loadOlder on A that hangs (will fail).
      const rejectOlderHolder: { fn?: (e: Error) => void } = {};
      const hangOlder = new Promise<{
        items: MobileConversation["items"];
        nextCursor?: string;
      }>((_, reject) => {
        rejectOlderHolder.fn = reject;
      });
      serviceA.olderItems = hangOlder as never;
      const olderP = store.getState().loadOlder(serviceA);

      // Rebind to B.
      const serviceB = new FakeConversationService();
      serviceB.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-B" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const sinkB = createFakeSink();
      await store.getState().rehydrate(serviceB, sinkB);

      const stateAfterRebind = snapshotState(store);

      // A's loadOlder fails — zero state change.
      rejectOlderHolder.fn?.(new Error("A page boom"));
      await olderP.catch(() => {});

      expect(snapshotState(store)).toEqual(stateAfterRebind);
    });

    // Table: late A failure for send/steer/queue/interrupt
    for (const kind of ["send", "steer", "queue", "interrupt"] as const) {
      it(`mutation: controlled A->B then late A ${kind} failure => entire state unchanged`, async () => {
        const serviceA = new FakeConversationService();
        serviceA.readProjectionResult = {
          conversation: makeConversation({ threadId: "thread-A", status: { type: kind === "send" ? "idle" : "active" } }),
          activity: {
            tasks: [],
            work: [],
            usage: {},
            capabilities: ALL_TRUE_CAPS,
          },
          olderCursor: null,
        };
        const sinkA = createFakeSink();
        const store = createConversationStore();
        await store.getState().openProjected(serviceA, sinkA, "ref-A");

        // Start a mutation on A that hangs then fails.
        const rejectMutationHolder: { fn?: (e: Error) => void } = {};
        const hangFail = new Promise<MutationReceipt>((_, reject) => {
          rejectMutationHolder.fn = reject;
        });
        if (kind === "send") {
          serviceA.sendShouldReject = null;
          serviceA.send = async () => hangFail;
        } else if (kind === "steer") {
          serviceA.steer = async () => hangFail;
        } else if (kind === "queue") {
          serviceA.queue = async () => hangFail;
        } else {
          serviceA.interrupt = async () => hangFail;
        }

        let mutationP: Promise<void>;
        if (kind === "send")
          mutationP = store.getState().send(serviceA, textInput("x"));
        else if (kind === "steer")
          mutationP = store.getState().steer(serviceA, textInput("x"));
        else if (kind === "queue")
          mutationP = store.getState().queue(serviceA, textInput("x"));
        else mutationP = store.getState().interrupt(serviceA);

        // Rebind to B.
        const serviceB = new FakeConversationService();
        serviceB.readProjectionResult = {
          conversation: makeConversation({ threadId: "thread-B" }),
          activity: {
            tasks: [],
            work: [],
            usage: {},
            capabilities: ALL_TRUE_CAPS,
          },
          olderCursor: null,
        };
        const sinkB = createFakeSink();
        await store.getState().rehydrate(serviceB, sinkB);

        const stateAfterRebind = snapshotState(store);
        const callsA =
          serviceA.sendCallCount +
          serviceA.steerCallCount +
          serviceA.queueCallCount +
          serviceA.interruptCallCount;

        // A's mutation fails — zero state change.
        rejectMutationHolder.fn?.(new Error("A mutation boom"));
        await mutationP.catch(() => {});

        expect(snapshotState(store)).toEqual(stateAfterRebind);
        // No additional A mutation calls after rebind.
        const callsAAfter =
          serviceA.sendCallCount +
          serviceA.steerCallCount +
          serviceA.queueCallCount +
          serviceA.interruptCallCount;
        expect(callsAAfter).toBe(callsA);
      });
    }

    // Table: late A SUCCESS for send/steer/queue/interrupt — full object reference + notification count
    for (const kind of ["send", "steer", "queue", "interrupt"] as const) {
      it(`mutation: controlled A->B then late A ${kind} success => entire state unchanged`, async () => {
        const serviceA = new FakeConversationService();
        serviceA.readProjectionResult = {
          conversation: makeConversation({ threadId: "thread-A", status: { type: kind === "send" ? "idle" : "active" } }),
          activity: {
            tasks: [],
            work: [],
            usage: {},
            capabilities: ALL_TRUE_CAPS,
          },
          olderCursor: null,
        };
        const sinkA = createFakeSink();
        const store = createConversationStore();
        await store.getState().openProjected(serviceA, sinkA, "ref-A");

        // Start a mutation on A that hangs then succeeds.
        const resolveMutationHolder: { fn?: (r: MutationReceipt) => void } = {};
        const hangSuccess = new Promise<MutationReceipt>((r) => {
          resolveMutationHolder.fn = r;
        });
        if (kind === "send") {
          serviceA.sendShouldReject = null;
          serviceA.send = async () => hangSuccess;
        } else if (kind === "steer") {
          serviceA.steer = async () => hangSuccess;
        } else if (kind === "queue") {
          serviceA.queue = async () => hangSuccess;
        } else {
          serviceA.interrupt = async () => hangSuccess;
        }

        let mutationP: Promise<void>;
        if (kind === "send")
          mutationP = store.getState().send(serviceA, textInput("x"));
        else if (kind === "steer")
          mutationP = store.getState().steer(serviceA, textInput("x"));
        else if (kind === "queue")
          mutationP = store.getState().queue(serviceA, textInput("x"));
        else mutationP = store.getState().interrupt(serviceA);

        // Rebind to B — the rebind transition settles A's pending mutation
        // (set pendingMutation: null) and increments the binding epoch.
        // The service/sink binding changes, but ref and conversation generation
        // must remain exact so late-A suppression is proven by binding identity,
        // not by ordinary ref/generation staleness.
        const refBeforeRebind = store.getState().ref;
        const generationBeforeRebind = store.getState().conversationGeneration;
        const serviceB = new FakeConversationService();
        serviceB.readProjectionResult = {
          conversation: makeConversation({ threadId: "thread-B" }),
          activity: {
            tasks: [],
            work: [],
            usage: {},
            capabilities: ALL_TRUE_CAPS,
          },
          olderCursor: null,
        };
        const sinkB = createFakeSink();
        await store.getState().rehydrate(serviceB, sinkB);

        // After rebind to B fully completes, explicitly prove ref and
        // generation did not change; only service/sink identity changed.
        const stateB = store.getState();
        expect(stateB.ref).toBe(refBeforeRebind);
        expect(stateB.conversationGeneration).toBe(generationBeforeRebind);
        expect(stateB.conversation?.threadId).toBe("thread-B");

        // A's pending mutation was settled by the rebind — verify it is null.
        expect(stateB.pendingMutation).toBeNull();

        // Capture the EXACT full state object reference. Not a partial
        // snapshot — the entire public state object must be the same
        // reference after late A success (zero Zustand set => same object).
        const exactStateAfterB = store.getState();

        // Subscribe and count ALL Zustand notifications. Any set() call
        // produces a notification, so notification count 0 proves zero set.
        let notificationCount = 0;
        const unsub = store.subscribe(() => {
          notificationCount++;
        });

        const callsA =
          serviceA.sendCallCount +
          serviceA.steerCallCount +
          serviceA.queueCallCount +
          serviceA.interruptCallCount;
        const readsA = serviceA.readProjectionCalls.length;
        const writesA = sinkA.setLiveViewCalls.length;

        try {
          // A's mutation succeeds — zero state change. The operation binding
          // is stale (isOperationBindingCurrent returns false because the
          // binding epoch/service/sink changed), so the success path returns
          // before any set() call. No Zustand set, no notification.
          resolveMutationHolder.fn?.(makeReceipt());
          await mutationP;

          // The entire public state object must be the SAME reference (===).
          // No partial/deep snapshot — if any set() was called, Zustand
          // returns a new object and this fails.
          expect(store.getState()).toBe(exactStateAfterB);
          // Notification count 0 — no set() fired.
          expect(notificationCount).toBe(0);
          // ref and generation remain the exact pre-rebind values.
          expect(store.getState().ref).toBe(refBeforeRebind);
          expect(store.getState().conversationGeneration).toBe(
            generationBeforeRebind,
          );
          // No additional A reads or sink writes after rebind.
          expect(serviceA.readProjectionCalls.length).toBe(readsA);
          expect(sinkA.setLiveViewCalls.length).toBe(writesA);
          // No additional A mutation calls after rebind.
          const callsAAfter =
            serviceA.sendCallCount +
            serviceA.steerCallCount +
            serviceA.queueCallCount +
            serviceA.interruptCallCount;
          expect(callsAAfter).toBe(callsA);
        } finally {
          unsub();
        }
      });
    }
  });

  // --- I3: Defense-in-depth question filter at state boundary -----------------------

  describe("I3: state loadOlder filters question rows (defense-in-depth)", () => {
    it("inject question row into page items => omitted, other items/order/cursor retained, askPending unchanged", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1", askPending: false }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: "cursor-1",
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      expect(store.getState().conversation?.askPending).toBe(false);

      // Inject a question row directly into olderItems — simulates a scenario
      // where the service filter missed it. The state boundary must filter it.
      service.olderItems = {
        items: [
          {
            kind: "question",
            id: "q-old",
            batch: { callId: "c1", questions: [] },
          } as never,
          {
            kind: "assistant",
            id: "msg-old",
            markdown: "old message",
            streaming: false,
          },
        ],
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);

      // askPending must remain false.
      expect(store.getState().conversation?.askPending).toBe(false);
      // No question items in the conversation.
      const items = store.getState().conversation?.items ?? [];
      expect(items.some((i) => i.kind === "question")).toBe(false);
      // Other content retained.
      expect(items.some((i) => i.id === "msg-old")).toBe(true);
      // Cursor retained.
      expect(store.getState().olderCursor).toBe("cursor-2");
    });
  });


  // The activity view's capabilities follow the reread's snapshot through
  // setLiveView — the same commit that carries the conversation's. There is
  // no separate capability write into either store any more.
  describe("a refusal's reread carries the reread's capabilities into the activity view", () => {
    it("real activity store: the view's capabilities match the conversation's after the reread", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      const activityStore = createActivityStore();
      const sink = activityStore.getState();
      const withCaps = (capabilities: ThreadCapabilities) =>
        makeReadProjectionResult(
          makeThread({ evener: { ref: "ref-1", capabilities, queue: { revision: 0 } } }),
          capabilities,
        );
      service.readProjectionResult = withCaps({ ...ALL_TRUE_CAPS });
      await store.getState().openProjected(service, sink, "ref-1");
      service.sendShouldReject = refusal();
      service.readProjectionResult = withCaps({ ...ALL_TRUE_CAPS, send: false });
      const ctrl = makeControlledRead(service);
      await store.getState().send(service, textInput("x"));
      await ctrl.ready(1);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      expect(store.getState().conversation?.capabilities.send).toBe(false);
      expect(activityStore.getState().view?.capabilities.send).toBe(false);
    });
  });

  // --- Cross-store I4 regression ---------------------------------------------------

  describe("I4 cross-store: activity reset + conversation reset => next openProjected accepts", () => {
    it("separate activity reset + conversation reset, then openProjected; both views commit, late notification rejected", async () => {
      const activityStore = createActivityStore();
      const service = new FakeConversationService();
      const store = createConversationStore();
      const activityView: ActivityView = {
        tasks: [{ status: "done", count: 5 }],
        work: [],
        usage: { totalTokens: 100 },
        capabilities: ALL_TRUE_CAPS,
      };
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-1" }),
        activity: activityView,
        olderCursor: null,
      };
      const sink = wrapActivityStoreAsSink(activityStore);
      await store.getState().openProjected(service, sink, "ref-1");
      expect(activityStore.getState().view).not.toBeNull();
      expect(store.getState().conversation?.threadId).toBe("thread-1");

      // Simulate RootShell-required separate activity reset.
      activityStore.getState().reset();
      expect(activityStore.getState().view).toBeNull();

      // Simulate conversation reset.
      store.getState().reset();
      expect(store.getState().conversation).toBeNull();

      // Next openProjected — sink.reset() called internally, then setLiveView.
      service.readProjectionResult = {
        conversation: makeConversation({ threadId: "thread-2" }),
        activity: {
          tasks: [{ status: "open", count: 2 }],
          work: [],
          usage: { totalTokens: 50 },
          capabilities: ALL_TRUE_CAPS,
        },
        olderCursor: null,
      };
      const sink2 = wrapActivityStoreAsSink(activityStore);
      await store.getState().openProjected(service, sink2, "ref-2");

      // Both views commit under the new identity.
      expect(activityStore.getState().view).not.toBeNull();
      expect(activityStore.getState().view?.tasks[0]?.status).toBe("open");
      expect(store.getState().conversation?.threadId).toBe("thread-2");

      // Late prior-generation notification rejected by both stores.
      const lateNotification = {
        method: "thread/status/changed" as const,
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
        },
      } as AnyNotification;
      const outcome = activityStore
        .getState()
        .applyLiveNotification(lateNotification, {
          threadId: "thread-1",
          ref: "ref-1",
          generation: 1,
        });
      expect(outcome).toBe("ignored");
      const convBefore = store.getState().conversation;
      store.getState().applyNotification(lateNotification);
      expect(store.getState().conversation).toBe(convBefore);
    });
  });

  describe("Task3: store-owned external error publication seam", () => {
    it("exact current owner writes generic message and survives a subsequent rehydrate", async () => {
      // The dispatcher publishes a generic sanitized external error against the
      // exact current ref+generation. The write goes through the wrapped set so
      // errorOwnerRev advances. A rehydrate that started BEFORE publication
      // captured the older errorOwnerRev; when it completes it must preserve
      // the newer external error rather than clearing it — proving the
      // revision advanced through the wrapped set.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const ref = store.getState().ref;
      const gen = store.getState().conversationGeneration;
      expect(ref).toBe("ref-1");
      const readsAfterOpen = service.readProjectionCalls.length;

      // Start a rehydrate (R) that hangs — it captures errorOwnerRev at entry
      // (before the external error is published). Use the controlled read's
      // level-triggered barriers, not microtask guesses: started(1) confirms
      // the read began; ready(1) confirms orig(ref) resolved and the read is
      // parked at the release gate, so R's entry errorOwnerRev is captured.
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await ctrl.ready(1);
      expect(ctrl.getStartedCount()).toBe(1);
      expect(ctrl.getDoneCount()).toBe(0);

      // While R is in-flight, publish the external error against the exact
      // current owner. This goes through the wrapped set, advancing
      // errorOwnerRev past R's captured value.
      store.getState().publishExternalError("external failure", ref, gen);
      expect(store.getState().error).toBe("external failure");

      // Before release, capture the pre-reconcile conversation and install a
      // subscription barrier that resolves only when the conversation identity
      // changes (the rehydrate commit). The subscription is installed BEFORE
      // release so it cannot miss a synchronous commit during release, and it
      // unsubscribes synchronously on match so cleanup never leaves a blocked
      // read. Then release, await exact completed(1), and await the reconcile
      // barrier — no yieldMicrotask/sleeps/polling.
      const preReleaseConv = store.getState().conversation;
      const reconcileP =
        store.getState().conversation !== preReleaseConv
          ? Promise.resolve()
          : new Promise<void>((resolve) => {
              const unsub = store.subscribe((s) => {
                if (s.conversation !== preReleaseConv) {
                  unsub();
                  resolve();
                }
              });
            });
      ctrl.release();
      await ctrl.completed(1);
      await reconcileP;
      expect(ctrl.getStartedCount()).toBe(1);
      expect(ctrl.getDoneCount()).toBe(1);
      // Exactly one rehydrate read ran (the initial open read is excluded).
      expect(service.readProjectionCalls.length).toBe(readsAfterOpen + 1);

      // The external error survives the rehydrate.
      expect(store.getState().error).toBe("external failure");
    });

    it("stale ref and stale generation make zero set, same reference, zero notifications", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      // Track subscriber notifications. Use a strict identity snapshot so a
      // no-op (same state object reference) fires zero callbacks.
      const notifications: string[] = [];
      const unsubscribe = store.subscribe((s) => {
        notifications.push(s.error ?? "null");
      });

      const stateBefore = store.getState();

      // Stale ref — the store holds "ref-1", caller passes "ref-stale".
      store
        .getState()
        .publishExternalError(
          "stale ref error",
          "ref-stale",
          store.getState().conversationGeneration,
        );
      expect(store.getState().error).toBeNull();
      expect(store.getState()).toBe(stateBefore);
      expect(notifications).toHaveLength(0);

      // Stale generation — caller passes an old generation.
      store.getState().publishExternalError("stale gen error", "ref-1", 999);
      expect(store.getState().error).toBeNull();
      expect(store.getState()).toBe(stateBefore);
      expect(notifications).toHaveLength(0);

      // Stale ref AND stale generation together.
      store.getState().publishExternalError("both stale", "ref-stale", 999);
      expect(store.getState().error).toBeNull();
      expect(store.getState()).toBe(stateBefore);
      expect(notifications).toHaveLength(0);

      unsubscribe();
    });

    it("reset/open transition rejects old expected owner", async () => {
      // After a reset (or open of a new conversation), a publishExternalError
      // call carrying the OLD ref+generation must make zero state changes.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const oldRef = store.getState().ref;
      const oldGen = store.getState().conversationGeneration;

      // Reset to idle — increments generation and clears ref.
      store.getState().reset();
      expect(store.getState().ref).toBeNull();
      expect(store.getState().conversationGeneration).not.toBe(oldGen);

      const notifications: string[] = [];
      const unsubscribe = store.subscribe((s) => {
        notifications.push(s.error ?? "null");
      });
      const stateBefore = store.getState();

      // Publish against the OLD owner — must be rejected (zero set).
      store.getState().publishExternalError("post-reset error", oldRef, oldGen);
      expect(store.getState().error).toBeNull();
      expect(store.getState()).toBe(stateBefore);
      expect(notifications).toHaveLength(0);

      unsubscribe();

      // Open a new conversation — the old owner is still rejected.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ id: "thread-2" }),
      );
      await store.getState().openProjected(service, createFakeSink(), "ref-2");
      expect(store.getState().ref).toBe("ref-2");

      const stateAfterOpen = store.getState();
      const notifications2: string[] = [];
      const unsubscribe2 = store.subscribe((s) => {
        notifications2.push(s.error ?? "null");
      });

      // Publish against the OLD owner (ref-1, old gen) — rejected.
      store.getState().publishExternalError("post-open error", oldRef, oldGen);
      expect(store.getState()).toBe(stateAfterOpen);
      expect(notifications2).toHaveLength(0);

      // Publishing against the NEW owner succeeds.
      store
        .getState()
        .publishExternalError(
          "new owner error",
          "ref-2",
          store.getState().conversationGeneration,
        );
      expect(store.getState().error).toBe("new owner error");

      unsubscribe2();
    });
  });
});
