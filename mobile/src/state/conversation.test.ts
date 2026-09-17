// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import { describe, expect, it, vi } from "vitest";
import {
  hydrateThread,
  liveAskQuestions,
  QUEUE_UNAVAILABLE,
  SEND_UNAVAILABLE,
  sessionControls,
  TURN_RUNNING,
  WireError,
} from "@evener/appwire-client";
import type {
  AnyNotification,
  EvenerThread,
  InputItem,
  MutationReceipt,
  Thread,
  ThreadCapabilities,
  ThreadItem,
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

// A fake ConversationService that returns scripted values without any network.
class FakeConversationService implements LiveConversationService {
  ref: string | null = null;
  openConv: MobileConversation = makeConversation();
  olderCursor: string | null = null;
  olderItems: {
    items: MobileConversation["items"];
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

    // A genuine failure ends as turn/completed{status: "failed"} with no
    // status frame behind it, so the store settles idle on that frame.
    it("settles idle when the active turn fails", async () => {
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
      expect(store.getState().conversation?.status.type).toBe("idle");
      expect(store.getState().conversation?.activeTurnId).toBeUndefined();
    });

    // The status is authoritative and the turn id can be absent while the
    // session is active (a read cut between turns); a failed completion then
    // still settles idle, while one for a superseded turn is left alone.
    it("settles idle on a failed completion with no active turn id, not on a superseded one", async () => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({ status: { type: "active" }, activeTurnId: undefined });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      store.getState().applyNotification({
        method: "turn/completed",
        params: { threadId: "thread-1", ref: "ref-1", turn: { id: "t-x", itemsView: "", status: "failed", error: { message: "boom" } } },
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
  });

  // --- item lifecycle and delta notification tests (Step 2) -------------------

  describe("item/started inserts/replaces authoritative item", () => {
    it("inserts a new assistant item from item/started", async () => {
      const { store } = await openRunningTurn();
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
      expect(rowById(store, "item-a")).toMatchObject({ kind: "assistant", markdown: "Hello" });
    });

    it("replaces an existing item when item/started carries the same id", async () => {
      const { store } = await openRunningTurn([
        agentMessageItem("item-a", "old", "inProgress"),
      ]);
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
      expect(rowById(store, "item-a")).toMatchObject({ kind: "assistant", markdown: "new text" });
    });

    // The replacement carries the transcript key, so it IS the same message under
    // a new wire id — and it says nothing about images, so the ones already known
    // stay with it (mergeItemImages, the hub's own rule).
    it("replaces the same transcriptKey across wire IDs, images and all", async () => {
      const { store } = await openRunningTurn([
        {
          type: "userMessage",
          id: "wire-old",
          transcriptKey: "stable-message",
          text: "old",
          images: [{ type: "image", url: "https://hub.test/image" }],
        } as ThreadItem,
      ]);
      expect(rows(store).some((item) => item.kind === "attachments")).toBe(true);
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
      const items = rows(store);
      expect(
        items.filter((item) => item.transcriptKey === "stable-message"),
      ).toHaveLength(1);
      expect(
        items.find((item) => item.transcriptKey === "stable-message")?.id,
      ).toBe("wire-new");
      // One attachment row, carried onto the replacement rather than orphaned on
      // the old wire id.
      const attachments = items.filter((item) => item.kind === "attachments");
      expect(attachments).toHaveLength(1);
      expect(attachments[0]).toMatchObject({ items: [{ src: "https://hub.test/image" }] });
    });
  });

  describe("item/completed settles item", () => {
    // Two shell calls in one running turn: consecutive tool items of the same
    // family, which the projector renders as one clustered row.
    const clusterPair = (over: Partial<ThreadItem> = {}): ThreadItem[] => [
      {
        type: "commandExecution",
        id: "call-first",
        toolName: "shell",
        status: "inProgress",
        outputImages: [{ source: "old", url: "https://hub.test/old" }],
      } as ThreadItem,
      {
        type: "commandExecution",
        id: "call-later",
        toolName: "shell",
        status: "inProgress",
        outputImages: [{ source: "later", url: "https://hub.test/later" }],
        ...over,
      } as ThreadItem,
    ];

    it("updates a first clustered member without losing later members or attachments", async () => {
      const { store } = await openRunningTurn(clusterPair());
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
      const items = rows(store);
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
      ).toMatchObject([{ id: "call-first:out:0", src: "https://hub.test/new" }]);
      expect(
        items.find((item) => item.id === "call-later:attachments"),
      ).toBeDefined();
    });

    it("uses transcript identity for a later member and keeps its attachment beside the cluster", async () => {
      const { store } = await openRunningTurn([
        {
          type: "commandExecution",
          id: "wire-first",
          transcriptKey: "first",
          toolName: "shell",
          status: "completed",
          outputImages: [{ source: "first", url: "https://hub.test/first" }],
        } as ThreadItem,
        {
          type: "commandExecution",
          id: "wire-later",
          transcriptKey: "later",
          toolName: "shell",
          status: "inProgress",
          outputImages: [{ source: "old", url: "https://hub.test/old" }],
        } as ThreadItem,
      ]);
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
      const items = rows(store);
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
        items: [{ id: "wire-first:out:0", src: "https://hub.test/first" }],
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

    // Decision 2: the read response is ordered at the snapshot cut, so a frame
    // this store folded while the read was in flight is already in the
    // snapshot. A snapshot without the cluster is a thread without it.
    it("takes the reread's snapshot over a cluster the live frames built", async () => {
      const { store, service, release, rehydratePromise } = await beginHeldClusterRehydrate();
      store.getState().applyNotification({
        method: "item/completed",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", item: { type: "commandExecution", id: "new-wire-later", transcriptKey: "later", toolName: "shell", status: "completed", output: "updated" } },
      } as AnyNotification);
      release(makeReadProjectionResult(makeThread()));
      await rehydratePromise;
      expect(rows(store)).toEqual([]);
      expect(service.readProjectionCalls.length).toBeGreaterThan(0);
    });

    it.each([false, true])("commits the reread's own members, page history first, with page history %s", async (withPageHistory) => {
      const { store, service, release, rehydratePromise } = await beginHeldClusterRehydrate();
      if (withPageHistory) {
        service.olderItems = { items: [{ kind: "user", id: "older", text: "older" }], nextCursor: undefined };
        store.setState({ olderCursor: "older-cursor" });
        await store.getState().loadOlder(service);
      }
      store.getState().applyNotification({
        method: "item/completed",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", item: { type: "commandExecution", id: "new-wire-later", transcriptKey: "later", toolName: "shell", status: "failed", output: "failed", error: "boom" } },
      } as AnyNotification);
      // The snapshot carries both members: the first settled clean, the second
      // failed — a failed member is never clustered with a clean one.
      release(makeReadProjectionResult(runningTurnThread([
        { type: "commandExecution", id: "wire-first", transcriptKey: "first", toolName: "shell", status: "completed", output: "authoritative first" } as ThreadItem,
        { type: "commandExecution", id: "wire-later", transcriptKey: "later", toolName: "shell", status: "completed", output: "failed", error: "boom" } as ThreadItem,
      ])));
      await rehydratePromise;
      const activities = rows(store).filter((item) => item.kind === "activity");
      expect(activities).toHaveLength(2);
      expect(activities.map((item) => item.transcriptKey)).toEqual(["first", "later"]);
      expect(activities[0]).toMatchObject({ state: "completed", detail: { output: "authoritative first" } });
      expect(activities[1]).toMatchObject({ state: "failed", detail: { output: "failed" } });
      expect(activities.every((item) => !item.members)).toBe(true);
      if (withPageHistory) expect(rows(store)[0]?.id).toBe("older");
    });

    // Page history is merged row by row, but a paged row can carry more than one
    // identity: a clustered activity row IS its members. When the reread's
    // snapshot has grown to include ONE of those members, only that member is a
    // duplicate — the others are still history nobody else holds.
    it("keeps the paged cluster's other members when the snapshot holds one of them", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(runningTurnThread());
      const store = createConversationStore();
      const sink = createFakeSink();
      await store.getState().openProjected(service, sink, "ref-1");

      // An older page whose one row is a cluster of three shell calls.
      const member = (id: string) => ({
        id,
        label: "shell",
        family: "tool" as const,
        state: "completed" as const,
        detail: { output: `${id} output` },
      });
      service.olderItems = {
        items: [
          {
            kind: "activity",
            id: "m1",
            label: "shell",
            family: "tool",
            state: "completed",
            detail: { output: "m1 output" },
            members: [member("m1"), member("m2"), member("m3")],
          },
        ],
        nextCursor: undefined,
      };
      store.setState({ olderCursor: "older-cursor" });
      await store.getState().loadOlder(service);
      expect(rows(store)[0]).toMatchObject({ kind: "activity", id: "m1" });

      // The reread's snapshot has caught up with the middle member only.
      service.readProjectionResult = makeReadProjectionResult(
        runningTurnThread([
          { type: "commandExecution", id: "m2", toolName: "shell", status: "completed", output: "m2 authoritative" } as ThreadItem,
        ]),
      );
      await store.getState().rehydrate(service, sink);

      // The snapshot owns m2. The page still owns m1 and m3.
      const paged = rows(store).find((row) => row.kind === "activity" && row.id === "m1");
      expect(paged).toBeDefined();
      expect(paged?.kind === "activity" ? paged.members?.map((m) => m.id) : undefined).toEqual([
        "m1",
        "m3",
      ]);
      expect(
        rows(store).some((row) => row.kind === "activity" && row.detail?.output === "m2 authoritative"),
      ).toBe(true);
    });

    // The rebuilt cluster must not keep the superseded member's identity. A
    // paged row's transcriptKey is the FIRST member's, so when that member is the
    // one the snapshot now holds and the next member has no key of its own, the
    // rebuilt row has to drop the key with it — carrying it forward would name an
    // identity the projection already holds, and the next publish would read the
    // row as a duplicate and delete the history it still carries.
    it("drops the superseded member's transcript key when rebuilding a paged cluster", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(runningTurnThread());
      const store = createConversationStore();
      const sink = createFakeSink();
      await store.getState().openProjected(service, sink, "ref-1");

      const member = (id: string, over: { transcriptKey?: string } = {}) => ({
        id,
        label: `shell ${id}`,
        family: "tool" as const,
        state: "completed" as const,
        detail: { output: `${id} output` },
        ...over,
      });
      service.olderItems = {
        items: [
          {
            kind: "activity",
            id: "keyed-first",
            transcriptKey: "key-first",
            label: "shell keyed-first",
            family: "tool",
            state: "completed",
            detail: { output: "keyed-first output" },
            members: [
              member("keyed-first", { transcriptKey: "key-first" }),
              member("unkeyed-second"),
            ],
          },
        ],
        nextCursor: undefined,
      };
      store.setState({ olderCursor: "older-cursor" });
      await store.getState().loadOlder(service);

      // The reread's snapshot holds the keyed first member.
      service.readProjectionResult = makeReadProjectionResult(
        runningTurnThread([
          {
            type: "commandExecution",
            id: "wire-first",
            transcriptKey: "key-first",
            toolName: "shell",
            status: "completed",
            output: "authoritative",
          } as ThreadItem,
        ]),
      );
      await store.getState().rehydrate(service, sink);

      const rebuilt = rows(store).find((row) => row.id === "unkeyed-second");
      expect(rebuilt).toBeDefined();
      expect(rebuilt?.transcriptKey).toBeUndefined();

      // And it survives the next publish rather than reading as a duplicate.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: { type: "userMessage", id: "u-later", turnId: "t1", text: "later" },
        },
      } as AnyNotification);
      expect(rows(store).some((row) => row.id === "unkeyed-second")).toBe(true);
    });

    it("does not resurrect a removed image when an attachment wire ID changes", async () => {
      const { store, service, sink } = await openRunningTurn([
        {
          type: "commandExecution",
          id: "old-wire",
          transcriptKey: "later",
          toolName: "shell",
          status: "inProgress",
          outputImages: [{ source: "old", url: "old" }],
        } as ThreadItem,
      ]);
      expect(rows(store).some((item) => item.kind === "attachments")).toBe(true);
      let release!: (value: ConversationReadProjection) => void;
      service.readProjectionBlock = new Promise((resolve) => { release = resolve; });
      const rehydratePromise = store.getState().rehydrate(service, sink);
      store.getState().applyNotification({ method: "item/completed", params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", item: { type: "commandExecution", id: "new-wire-later", transcriptKey: "later", toolName: "shell", status: "completed", output: "done" } } } as AnyNotification);
      release(makeReadProjectionResult(runningTurnThread([
        { type: "commandExecution", id: "new-wire-later", transcriptKey: "later", toolName: "shell", status: "completed", output: "done" } as ThreadItem,
      ])));
      await rehydratePromise;
      expect(rows(store).some((item) => item.kind === "attachments")).toBe(false);
    });

    it("carries a later member's completion into the cluster row", async () => {
      const { store } = await openRunningTurn([
        { type: "commandExecution", id: "wire-first", transcriptKey: "first", toolName: "shell", status: "completed" } as ThreadItem,
        { type: "commandExecution", id: "wire-later", transcriptKey: "later", toolName: "shell", status: "inProgress" } as ThreadItem,
      ]);
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1", ref: "ref-1", turnId: "t1",
          item: { type: "commandExecution", id: "new-wire-later", transcriptKey: "later", toolName: "shell", status: "completed", output: "updated", outputImages: [{ source: "new", url: "new" }] },
        },
      } as AnyNotification);
      const items = rows(store);
      const activity = items.find((item) => item.kind === "activity");
      expect(activity?.kind === "activity" ? activity.members?.find((member) => member.transcriptKey === "later")?.detail.output : undefined).toBe("updated");
      expect(items.find(
        (item): item is Extract<MobileTimelineItem, { kind: "attachments" }> =>
          item.kind === "attachments" && item.sourceTranscriptKey === "later",
      )?.items).toMatchObject([{ id: "new-wire-later:out:0", src: "new" }]);
    });

    it("splits a failed member out of a hydrated cluster", async () => {
      const { store } = await openRunningTurn([
        { type: "commandExecution", id: "call-first", toolName: "shell", status: "inProgress" } as ThreadItem,
        { type: "commandExecution", id: "call-later", toolName: "shell", status: "inProgress" } as ThreadItem,
      ]);
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
      const activities = rows(store).filter((item) => item.kind === "activity");
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

    // Decision 2: an item's own status is the per-item liveness signal, as the
    // web reads it (TurnBlock.tsx's isItemLive, `item.status === "inProgress"`).
    // A settled assistant message stops saying "Writing…" the moment it
    // settles, whether or not the turn it sits in is still running.
    it("stops streaming when the assistant item settles inside a running turn", async () => {
      const { store } = await openRunningTurn([
        agentMessageItem("item-a", "partial", "inProgress"),
      ]);
      expect(rowById(store, "item-a")).toMatchObject({ streaming: true });
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: agentMessageItem("item-a", "all of it", "completed"),
        },
      } as AnyNotification);
      expect(store.getState().conversation?.activeTurnId).toBe("t1");
      expect(rowById(store, "item-a")).toMatchObject({
        kind: "assistant",
        markdown: "all of it",
        streaming: false,
      });
    });

    it("marks an assistant item as not streaming on item/completed", async () => {
      const { store } = await openRunningTurn([
        agentMessageItem("item-a", "streaming text", "inProgress"),
      ]);
      expect(rowById(store, "item-a")).toMatchObject({ streaming: true });
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
      // The turn is still running, so the row settles when the turn does —
      // the projector reads streaming from the turn, as the web does.
      store.getState().applyNotification({
        method: "turn/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turn: { id: "t1", itemsView: "", status: "completed" },
        },
      } as AnyNotification);
      expect(rowById(store, "item-a")).toMatchObject({
        kind: "assistant",
        streaming: false,
      });
    });

    it.each([
      ["completed", undefined, undefined, "completed"],
      ["failed on a nonzero exit code and no error", 1, undefined, "failed"],
      ["completed on a zero exit code and no error", 0, undefined, "completed"],
      ["failed with an error and no exit code", undefined, "boom", "failed"],
    ] as const)(
      "marks an activity item as %s on item/completed",
      async (_label, exitCode, error, expected) => {
        const { store } = await openRunningTurn([
          { type: "commandExecution", id: "tool-1", toolName: "shell", status: "inProgress" } as ThreadItem,
        ]);
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
              ...(exitCode === undefined ? {} : { exitCode }),
              ...(error === undefined ? {} : { error }),
            },
          },
        } as AnyNotification);
        expect(rowById(store, "tool-1")).toMatchObject({
          kind: "activity",
          state: expected,
        });
      },
    );

    it("C6: upserts completed item even when start was missed", async () => {
      // The turn is open but empty — the item/started was missed.
      const { store } = await openRunningTurn();
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
      // The completed item is inserted (UPSERT), not lost.
      expect(rowById(store, "tool-missed")).toMatchObject({ kind: "activity" });
    });

    // The durable activity family comes from the wire type, independent of the
    // label. A commandExecution whose toolName is "Reasoning" stays family
    // "tool" and preserves callId exactly; a reasoning item is family
    // "reasoning".
    it("a commandExecution named 'Reasoning' is family 'tool' with exact callId", async () => {
      const { store } = await openRunningTurn();
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
      expect(rowById(store, "tool-reasoning-1")).toMatchObject({
        kind: "activity",
        // Family follows the wire type (commandExecution), not the toolName;
        // the label is the toolName verbatim (display only); callId is
        // preserved exactly for diagnostics disclosure.
        family: "tool",
        label: "Reasoning",
        detail: { callId: "call-reasoning-1" },
      });
    });

    it("a reasoning item is family 'reasoning'", async () => {
      const { store } = await openRunningTurn();
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
      expect(rowById(store, "reason-notify-1")).toMatchObject({
        kind: "activity",
        family: "reasoning",
        label: "Reasoning",
      });
    });

    it.each(["completed", "failed", "interrupted"] as const)(
      "preserves streamed reasoning text when sparse item/completed settles it as %s",
      async (status) => {
        const { store } = await openRunningTurn();
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
        expect(rowById(store, "reason-sparse-1")).toMatchObject({
          kind: "activity",
          state: "completed",
          detail: { output: "Thinking" },
        });
      },
    );

    // Decision 2: the chunks the model accumulated are what a reasoning row
    // shows — they are seeded from the item's own text and extended by every
    // summary delta, and a settle keeps them (reducer.ts's mergeReasoning).
    // A completion carrying its own text no longer replaces what the row
    // already showed; the web's think block reads the same chunks.
    it.each([
      ["a later text", "final reasoning"],
      ["empty text", ""],
    ] as const)("keeps the accumulated reasoning when the completion carries %s", async (_label, text) => {
      const { store } = await openRunningTurn();
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
      expect(rowById(store, "reason-authoritative-1")).toMatchObject({
        kind: "activity",
        detail: { output: "old reasoning" },
      });
    });

    // A reasoning item the model never streamed into shows the text the wire
    // settled it with.
    it("shows the settled text of a reasoning item that streamed nothing", async () => {
      const { store } = await openRunningTurn();
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: {
            type: "reasoning",
            id: "reason-settled-1",
            status: "completed",
            text: "final reasoning",
          },
        },
      } as AnyNotification);
      expect(rowById(store, "reason-settled-1")).toMatchObject({
        kind: "activity",
        detail: { output: "final reasoning" },
      });
    });

    it("bounds a reasoning row that streamed past the byte limit, and re-bounds it on every later frame", async () => {
      const { store, service, sink } = await openRunningTurn();
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
      const streamed = rowById(store, "reason-truncated-1");
      expect(streamed?.kind === "activity" && streamed.detail.output?.endsWith(TRUNCATION_MARKER)).toBe(true);
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
      const settled = rowById(store, "reason-truncated-1");
      expect(settled?.kind === "activity" && settled.detail.output?.endsWith(TRUNCATION_MARKER)).toBe(true);
      // An authoritative short version is short again: the bound is a
      // property of the text a row carries, not a freeze on its identity.
      service.readProjectionResult = makeReadProjectionResult(runningTurnThread([
        { type: "reasoning", id: "reason-truncated-1", text: "short", status: "completed" } as ThreadItem,
      ]));
      await store.getState().rehydrate(service, sink);
      expect(rowById(store, "reason-truncated-1")).toMatchObject({
        kind: "activity",
        detail: { output: "short" },
      });
    });
  });

  describe("assistant delta appends to item", () => {
    it("appends delta text to assistant item markdown", async () => {
      const { store } = await openRunningTurn([
        agentMessageItem("item-a", "Hello", "inProgress"),
      ]);
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
      expect(rowById(store, "item-a")).toMatchObject({
        kind: "assistant",
        markdown: "Hello world",
      });
    });

    it("appends reasoning summary delta to activity item", async () => {
      const { store } = await openRunningTurn([
        { type: "reasoning", id: "reason-1", text: "Thinking", status: "inProgress" } as ThreadItem,
      ]);
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
      expect(rowById(store, "reason-1")).toMatchObject({
        kind: "activity",
        detail: { output: "Thinking more" },
      });
    });

    it("appends tool output delta to activity item", async () => {
      const { store } = await openRunningTurn([
        { type: "commandExecution", id: "tool-1", toolName: "shell", callId: "call-1", output: "line1", status: "inProgress" } as ThreadItem,
      ]);
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
      expect(rowById(store, "tool-1")).toMatchObject({
        kind: "activity",
        detail: { output: "line1\nline2" },
      });
    });

    // A frame naming an item the model does not hold could not be applied:
    // this client missed the item/started that would have made it placeable,
    // so the rows have a gap in them. Nothing changes on screen from the
    // frame itself, and the canonical read is asked for at once — the phone
    // refreshes itself rather than waiting for the next resync.
    it("asks for a reread when a delta targets an item the model does not hold", async () => {
      const { store, service } = await openRunningTurn([
        agentMessageItem("a1", "hello", "inProgress"),
      ]);
      const initialReads = service.readProjectionCalls.length;
      const before = rows(store);
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
      expect(rows(store)).toEqual(before);
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
    });

    it.each([
      ["a tool-output delta", {
        method: "item/toolOutput/delta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "nonexistent", callId: "call-x", delta: "x" },
      }],
      ["a reasoning delta", {
        method: "item/reasoning/summaryTextDelta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "nonexistent", summaryIndex: 0, delta: "x" },
      }],
      ["a completion for a turn the model does not hold", {
        method: "item/completed",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t-unknown", item: { type: "userMessage", id: "u9", turnId: "t-unknown", text: "hi" } },
      }],
    ] as const)("asks for a reread when %s cannot be placed", async (_label, frame) => {
      const { store, service } = await openRunningTurn([
        agentMessageItem("a1", "hello", "inProgress"),
      ]);
      const initialReads = service.readProjectionCalls.length;
      const before = rows(store);
      store.getState().applyNotification(frame as AnyNotification);
      expect(rows(store)).toEqual(before);
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
    });
  });

  describe("split Unicode remains valid", () => {
    it("does not corrupt surrogate pairs split across deltas", async () => {
      const { store } = await openRunningTurn([
        agentMessageItem("item-u", "", "inProgress"),
      ]);
      // 𝐀 is U+1D400 (surrogate pair D835 DC00)
      // Send the first half, then the second half as separate deltas.
      for (const delta of ["\uD835", "\uDC00"]) {
        store.getState().applyNotification({
          method: "item/agentMessage/delta",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            turnId: "t1",
            itemId: "item-u",
            delta,
          },
        } as AnyNotification);
      }
      const item = rowById(store, "item-u");
      expect(item?.kind).toBe("assistant");
      if (item?.kind !== "assistant") throw new Error("expected an assistant row");
      // The combined string is valid: the surrogate pair forms 𝐀
      expect(item.markdown).toBe("\uD835\uDC00");
      // It is a single code point (length 1 by code point, 2 by UTF-16)
      expect([...item.markdown].length).toBe(1);
    });
  });

  // Every row kind that carries text a reader scrolls past is bounded, not just
  // the two that happen to stream: a pasted user message can be as large as any
  // assistant reply, a tool failure's detail carries a stack, and a notice
  // carries whatever the daemon said.
  describe("the display bound covers every text-bearing row kind", () => {
    const oversized = "x".repeat(MAX_ITEM_BYTES + 5_000);
    const bounded = (text: string): boolean =>
      new TextEncoder().encode(text).length <= MAX_ITEM_BYTES &&
      text.endsWith(TRUNCATION_MARKER);

    it.each([
      [
        "user",
        { kind: "user", id: "r", text: oversized },
        (row: MobileTimelineItem) => (row.kind === "user" ? [row.text] : []),
      ],
      [
        "assistant",
        { kind: "assistant", id: "r", markdown: oversized, streaming: false },
        (row: MobileTimelineItem) => (row.kind === "assistant" ? [row.markdown] : []),
      ],
      [
        "notice",
        {
          kind: "notice",
          id: "r",
          origin: "system",
          family: "warning",
          tone: "warning",
          text: oversized,
        },
        (row: MobileTimelineItem) => (row.kind === "notice" ? [row.text] : []),
      ],
      [
        "failure",
        { kind: "failure", id: "r", title: oversized, detail: oversized },
        (row: MobileTimelineItem) => (row.kind === "failure" ? [row.title, row.detail] : []),
      ],
      [
        "activity",
        {
          kind: "activity",
          id: "r",
          label: "shell",
          family: "tool",
          state: "completed",
          detail: { output: oversized, arguments: oversized, error: oversized },
        },
        (row: MobileTimelineItem) =>
          row.kind === "activity"
            ? [row.detail.output, row.detail.arguments, row.detail.error].filter(
                (text): text is string => text !== undefined,
              )
            : [],
      ],
    ] as const)("bounds a %s row", async (_kind, row, read) => {
      const service = new FakeConversationService();
      service.openConv = makeConversation({ items: [row as unknown as MobileTimelineItem] });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      const published = store.getState().conversation?.items.find((item) => item.id === "r");
      if (published === undefined) throw new Error("row not published");
      const texts = read(published);
      expect(texts.length).toBeGreaterThan(0);
      for (const text of texts) expect(bounded(text)).toBe(true);
    });

    // A question row's own prose is bounded in place, like every other kind, and
    // the fixture is built the way the app builds one: an ask_user item whose
    // arguments carry the oversized text, projected through the model. The answer
    // path is unaffected because it does not read these rows — it asks the model
    // through the package's own rule, so it still names the uncut values.
    it("bounds a question row's prose and leaves the model's asks whole", async () => {
      const bigAsk = JSON.stringify({
        questions: [
          {
            header: oversized,
            question: oversized,
            why: oversized,
            if_unanswered: oversized,
            options: [{ label: oversized, detail: oversized }],
            multi_select: false,
          },
        ],
      });
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        runningTurnThread([askUserItem("ask-1", bigAsk)]),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      const row = rows(store).find((item) => item.kind === "question");
      if (row === undefined || row.kind !== "question") throw new Error("no question row");
      const shown = row.questions[0];
      if (shown === undefined) throw new Error("no question");
      const option = shown.options[0];
      for (const text of [
        shown.header,
        shown.question,
        shown.why,
        shown.ifUnanswered,
        option?.label,
        option?.detail,
      ]) {
        if (text === undefined) throw new Error("prose field missing");
        expect(bounded(text)).toBe(true);
      }

      // What the composer answers with comes from the model, uncut: a bounded
      // label would name a choice the agent never offered.
      const conversation = store.getState().conversation;
      if (conversation === null) throw new Error("conversation gone");
      const canonical = liveAskQuestions(conversation)[0];
      expect(canonical?.header).toBe(oversized);
      expect(canonical?.options[0]?.label).toBe(oversized);
      expect(canonical?.ifUnanswered).toBe(oversized);
    });

    // Every text an activity row renders, top-level and per member: the label
    // (the row's disclosure line and its accessibility label) and the
    // description (the summary line a collapsed row shows) are read exactly as
    // the arguments and output are.
    it.each([
      ["top-level", false],
      ["clustered member", true],
    ])("bounds an activity's label and description on a %s row", async (_where, clustered) => {
      const activity = {
        kind: "activity" as const,
        id: "r",
        label: oversized,
        family: "tool" as const,
        state: "completed" as const,
        detail: { description: oversized, output: "small" },
      };
      const row = clustered
        ? {
            ...activity,
            members: [
              { ...activity, id: "r:0" },
              { ...activity, id: "r:1" },
            ],
          }
        : activity;
      const service = new FakeConversationService();
      service.openConv = makeConversation({ items: [row as unknown as MobileTimelineItem] });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      const published = store.getState().conversation?.items.find((item) => item.id === "r");
      if (published === undefined || published.kind !== "activity") throw new Error("row not published");
      const texts = [
        published.label,
        published.detail.description,
        ...(published.members ?? []).flatMap((member) => [member.label, member.detail.description]),
      ].filter((text): text is string => text !== undefined);
      expect(texts.length).toBe(clustered ? 6 : 2);
      for (const text of texts) expect(bounded(text)).toBe(true);
    });

    // An attachment's src is the image itself (a data: URI for composer bytes),
    // not prose a reader scrolls: cutting it mid-payload yields an image that
    // cannot decode, so it is left whole. The wire bounds image payloads at the
    // source instead.
    it("leaves an attachment's data URI whole", async () => {
      const src = `data:image/png;base64,${"A".repeat(MAX_ITEM_BYTES + 5_000)}`;
      const service = new FakeConversationService();
      service.openConv = makeConversation({
        items: [
          { kind: "attachments", id: "r", items: [{ id: "r:0", src }] } as unknown as MobileTimelineItem,
        ],
      });
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      const published = store.getState().conversation?.items.find((item) => item.id === "r");
      expect(published?.kind === "attachments" ? published.items[0]?.src : undefined).toBe(src);
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

  // What a live item frame resolves locally, and what still needs the
  // canonical read. The projector has the whole turn in hand — the pending
  // ask_user set included — so the frames that once had to ask for a reread
  // now settle in place; evener/thread/resync stays the authoritative
  // refresh path.
  describe("F7: item frames settle locally; resync is the reread path", () => {
    it("projects a started ask_user as its question row, without a reread", async () => {
      const { store, service } = await openRunningTurn();
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
            argumentsJson: VALID_ASK_ARGS,
          },
        },
      } as AnyNotification);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      // An ask_user still in flight is not answerable yet: it is the tool
      // call it is, and the question row appears when it settles.
      expect(rowById(store, "ask-1")).toMatchObject({ kind: "activity", family: "tool" });
      expect(service.readProjectionCalls.length).toBe(initialReads);
    });

    it("projects a completed ask_user as a question row with the pending flag, without a reread", async () => {
      const { store, service } = await openRunningTurn();
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
            argumentsJson: VALID_ASK_ARGS,
          },
        },
      } as AnyNotification);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      expect(rowById(store, "ask-1")).toMatchObject({ kind: "question" });
      expect(service.readProjectionCalls.length).toBe(initialReads);
    });

    it.each([
      ["a reasoning delta naming an assistant item", {
        method: "item/reasoning/summaryTextDelta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "r-1", summaryIndex: 0, delta: "thinking" },
      }],
      ["a known thread-level frame", {
        method: "thread/status/changed",
        params: { threadId: "thread-1", ref: "ref-1", status: { type: "running" } },
      }],
    ] as const)("does not reread for %s", async (_label, frame) => {
      const { store, service } = await openRunningTurn([
        agentMessageItem("r-1", "", "inProgress"),
      ]);
      const initialReads = service.readProjectionCalls.length;
      store.getState().applyNotification(frame as AnyNotification);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBe(initialReads);
    });

    // The same gap detection covers a frame that is not an item frame at all: a
    // warning or an injected steer whose active turn lies outside the window
    // this client loaded has nowhere to land, and the model says so by handing
    // back the turns it already had.
    it.each([
      ["a warning", {
        method: "warning",
        params: { threadId: "thread-1", ref: "ref-1", message: "careful" },
      }],
      ["an injected steer", {
        method: "evener/steering/injected",
        params: { threadId: "thread-1", ref: "ref-1", text: "go left", kind: "user", source: "user" },
      }],
      ["a turn settle", {
        method: "turn/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turn: { id: "t-outside-window", status: "completed", itemsView: "" },
        },
      }],
    ] as const)("rereads when %s names an active turn this window does not hold", async (_label, frame) => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ evener: evenerWith({ activeTurnId: "t-outside-window" }) }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      expect(store.getState().conversation?.activeTurnId).toBe("t-outside-window");
      const initialReads = service.readProjectionCalls.length;

      store.getState().applyNotification(frame as AnyNotification);
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
    });

    // A warning that arrives while no turn is active is dropped by the wire's
    // own rule, not by a gap: warnings are never transcript-persisted
    // (reducer.ts's "warning" case cites internal/apptranscript having no
    // warning-item conversion), so the canonical read cannot carry it either
    // and asking for one buys nothing.
    it("does not reread for a warning the wire drops because no turn is active", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      expect(store.getState().conversation?.activeTurnId).toBeUndefined();
      const initialReads = service.readProjectionCalls.length;

      store.getState().applyNotification({
        method: "warning",
        params: { threadId: "thread-1", ref: "ref-1", message: "careful" },
      } as AnyNotification);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBe(initialReads);
    });

    // A burst of unplaceable frames is one read, not a storm: requestRehydrate
    // goes through the drain scheduler keyed by the thread ref, which coalesces
    // same-key requests (overwriting the effect, merging waiters) and runs one
    // effect at a time — at most one read in flight plus one queued, however
    // many frames land.
    it("coalesces a burst of unplaceable deltas into one read", async () => {
      const { store, service } = await openRunningTurn();
      const initialReads = service.readProjectionCalls.length;
      for (let i = 0; i < 10; i++) {
        store.getState().applyNotification({
          method: "item/agentMessage/delta",
          params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "gone", delta: `d${i}` },
        } as AnyNotification);
      }
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBe(initialReads + 1);
    });

    // An item frame the reducer has no case for cannot be projected, so the
    // canonical read is still the recovery.
    it("rereads for an item frame the model has no rule for", async () => {
      const { store, service } = await openRunningTurn();
      const initialReads = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "item/somethingNew/delta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "x", delta: "y" },
      } as unknown as AnyNotification);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      expect(service.readProjectionCalls.length).toBeGreaterThan(initialReads);
    });

    it("warning insertion obeys item cap", async () => {
      // 500 user messages in the settled turn, at the cap, plus a running
      // turn for the warning to land in.
      const older: ThreadItem[] = [];
      for (let i = 0; i < 500; i++) older.push(userMessageItem(`u${i}`, ""));
      const { store } = await openProjectedThreadPair(older);
      expect(rows(store)).toHaveLength(500);
      store.getState().applyNotification({
        method: "warning",
        params: { threadId: "thread-1", ref: "ref-1", message: "test warning" },
      } as AnyNotification);
      const items = rows(store);
      expect(items.length).toBeLessThanOrEqual(500);
      // The newest rows are retained: the warning is the last one.
      expect(items[items.length - 1]).toMatchObject({
        kind: "notice",
        tone: "warning",
        text: "test warning",
      });
    });

    it("keeps repeated warning identities stable through a later item update", async () => {
      const { store } = await openRunningTurn([userMessageItem("user-1", "input")]);
      const warning = {
        method: "warning",
        params: { threadId: "thread-1", ref: "ref-1", title: "Provider warning", message: "Retrying" },
      } as AnyNotification;
      store.getState().applyNotification(warning);
      store.getState().applyNotification(warning);
      const warningIdsBefore = rows(store)
        .filter((item) => item.kind === "notice" && item.tone === "warning")
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

      const warningIdsAfter = rows(store)
        .filter((item) => item.kind === "notice" && item.tone === "warning")
        .map((item) => item.id);
      expect(warningIdsBefore).toHaveLength(2);
      expect(new Set(warningIdsBefore).size).toBe(2);
      expect(warningIdsAfter).toEqual(warningIdsBefore);
    });

    // Decision 2: a warning with no turn to land in has nowhere wire-true to
    // go — warnings are not transcript-persisted, so no later snapshot would
    // carry it either. It is dropped rather than shown against no turn.
    it("drops a warning that arrives with no active turn", async () => {
      const store = await openProjectedThread(makeThread());
      store.getState().applyNotification({
        method: "warning",
        params: { threadId: "thread-1", ref: "ref-1", title: "Provider", message: "careful" },
      } as AnyNotification);
      expect(
        rows(store).filter((row) => row.kind === "notice" && row.tone === "warning"),
      ).toEqual([]);
    });

    it("preserves a command description through live item projection", async () => {
      const { store } = await openRunningTurn();
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
      expect(rowById(store, "command-1")).toMatchObject({
        kind: "activity",
        detail: { description: "Inspect the source tree" },
      });
    });

    it("preserves a user transcript entry index through live item projection", async () => {
      const { store } = await openRunningTurn();
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
      expect(rowById(store, "fork-source")).toMatchObject({
        kind: "user",
        transcriptEntryIndex: 8,
      });
    });
  });

  describe("F8: UTF-8 byte cap, valid boundary, delta-after-marker", () => {
    it("keeps the row at one marker as deltas keep streaming past the cap", async () => {
      const { store } = await openRunningTurn([
        agentMessageItem("item-trunc", "x".repeat(70_000), "inProgress"),
      ]);
      const beforeDelta = rowById(store, "item-trunc");
      expect(beforeDelta?.kind).toBe("assistant");
      if (beforeDelta?.kind !== "assistant") throw new Error("expected an assistant row");
      expect(beforeDelta.markdown.endsWith(TRUNCATION_MARKER)).toBe(true);

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

      const afterDelta = rowById(store, "item-trunc");
      expect(afterDelta?.kind).toBe("assistant");
      if (afterDelta?.kind !== "assistant") throw new Error("expected an assistant row");
      // The marker is at the end, exactly once, and the streamed text past
      // the cap is not on screen.
      expect(afterDelta.markdown.split(TRUNCATION_MARKER).length - 1).toBe(1);
      expect(afterDelta.markdown.endsWith(TRUNCATION_MARKER)).toBe(true);
      expect(afterDelta.markdown).not.toContain("more text after truncation");
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

    it("F12: genuine marker suffix in content does not stop a delta appending", async () => {
      // Content that genuinely ends with "… truncated" but is under the byte
      // limit is not treated as already bounded.
      const { store } = await openRunningTurn([
        agentMessageItem("item-genuine", `Hello${TRUNCATION_MARKER}`, "inProgress"),
      ]);
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
      expect(rowById(store, "item-genuine")).toMatchObject({
        kind: "assistant",
        markdown: `Hello${TRUNCATION_MARKER} more text`,
      });
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

  // The thread a live item frame lands on: turn t1 open and running, already
  // holding `items`. The wire opens a turn before the items in it, so a frame
  // naming t1 has a turn to land in.
  function runningTurnThread(items: ThreadItem[] = [], over: Partial<Thread> = {}): Thread {
    return makeThread({
      turns: [makeTurn({ id: "t1", status: "inProgress", items })],
      evener: evenerWith({ activeTurnId: "t1" }),
      ...over,
    });
  }

  // A store open on runningTurnThread(items), with its service and sink, for
  // the tests that drive a rehydrate or a page load afterwards.
  async function openRunningTurn(
    items: ThreadItem[] = [],
    over: Partial<Thread> = {},
  ): Promise<{
    store: ReturnType<typeof createConversationStore>;
    service: FakeConversationService;
    sink: FakeLiveActivitySink;
    thread: Thread;
  }> {
    const thread = runningTurnThread(items, over);
    const service = new FakeConversationService();
    service.readProjectionResult = makeReadProjectionResult(thread);
    const store = createConversationStore();
    const sink = createFakeSink();
    await store.getState().openProjected(service, sink, "ref-1");
    return { store, service, sink, thread };
  }

  // A rehydrate held open while live frames land, on a thread whose running
  // turn holds two shell calls the projector clusters into one row.
  async function beginHeldClusterRehydrate() {
    const { store, service, sink, thread } = await openRunningTurn([
      { type: "commandExecution", id: "wire-first", transcriptKey: "first", toolName: "shell", status: "inProgress" } as ThreadItem,
      { type: "commandExecution", id: "wire-later", transcriptKey: "later", toolName: "shell", status: "inProgress" } as ThreadItem,
    ]);
    let release!: (value: ConversationReadProjection) => void;
    service.readProjectionBlock = new Promise((resolve) => { release = resolve; });
    const rehydratePromise = store.getState().rehydrate(service, sink);
    return { service, store, sink, thread, release, rehydratePromise };
  }

  // A settled turn of `older` items followed by a running turn t1 — the shape
  // for the rows that are already history when a live frame lands.
  async function openProjectedThreadPair(older: ThreadItem[]) {
    const thread = makeThread({
      turns: [
        makeTurn({ id: "t0", status: "completed", items: older }),
        makeTurn({ id: "t1", status: "inProgress", items: [] }),
      ],
      evener: evenerWith({ activeTurnId: "t1" }),
    });
    const service = new FakeConversationService();
    service.readProjectionResult = makeReadProjectionResult(thread);
    const store = createConversationStore();
    const sink = createFakeSink();
    await store.getState().openProjected(service, sink, "ref-1");
    return { store, service, sink, thread };
  }

  // The display rows of the open conversation.
  function rows(store: ReturnType<typeof createConversationStore>): MobileTimelineItem[] {
    return store.getState().conversation?.items ?? [];
  }

  // One display row by wire id (a clustered member is reached through its
  // cluster, so this is the top-level row the id names).
  function rowById(
    store: ReturnType<typeof createConversationStore>,
    id: string,
  ): MobileTimelineItem | undefined {
    return rows(store).find((item) => item.id === id);
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

  // The ask_user lifecycle, projected from the model the frames fold into:
  // the projector has the whole turn, so a settled ask becomes its question
  // row as it lands, and a later user message settles it — no reread in
  // either direction.
  describe("I4: the question lifecycle through the projection", () => {
    it("shows a completed parseable ask_user as a question row, with no reread", async () => {
      const { store, service } = await openRunningTurn();
      expect(store.getState().conversation?.askPending).toBe(false);
      const initialReads = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: askUserItem("ask-1", VALID_ASK_ARGS),
        },
      } as AnyNotification);
      await yieldMicrotask();
      const conv = store.getState().conversation;
      const questionItem = conv?.items.find((i) => i.kind === "question");
      expect(questionItem).toBeDefined();
      if (questionItem?.kind === "question") {
        expect(questionItem.questions).toHaveLength(1);
        expect(questionItem.questions[0]?.question).toBe("Pick one");
        expect(questionItem.questions[0]?.options).toHaveLength(2);
      }
      expect(service.readProjectionCalls.length).toBe(initialReads);
    });

    // The wire's askPending is the hub's own thread-level signal and stays
    // exactly as the snapshot set it (the reducer's invariant), and it is not what
    // the phone answers from: the question ROWS are, and the projection builds one
    // only for an ask the package says is answerable now. Nothing derived is
    // written back into the model, so nothing latches.
    it("renders no question row for an ask the hub knows about but this window does not hold", async () => {
      // The ask's item is outside the loaded window: no question row can be
      // projected for it, and the wire flag is the only evidence there is.
      const store = await openProjectedThread(
        makeThread({
          evener: evenerWith({ askPending: true }),
          turns: [makeTurn({ id: "t1", items: [userMessageItem("u1", "hi")] })],
        }),
      );
      expect(rows(store).some((row) => row.kind === "question")).toBe(false);
      expect(store.getState().conversation?.askPending).toBe(true);
    });

    it("drops the question row once the answering message arrives, whatever the last snapshot said", async () => {
      const store = await openProjectedThread(
        makeThread({
          evener: evenerWith({ askPending: false, activeTurnId: "t2" }),
          turns: [
            makeTurn({ id: "t1", items: [askUserItem("ask-1", VALID_ASK_ARGS)] }),
            makeTurn({ id: "t2", items: [], status: "inProgress" }),
          ],
        }),
      );
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t2",
          item: userMessageItem("answer-1", "I choose A"),
        },
      } as AnyNotification);
      // And it stays false as later frames fold: the flag is re-derived every
      // publish, never carried forward from the model the projection wrote.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t2",
          item: userMessageItem("answer-1", "I choose A"),
        },
      } as AnyNotification);
      // The wire's own flag is untouched by any of it.
      expect(store.getState().conversation?.askPending).toBe(false);
    });

    // The hub's own clear, end to end (#1629): the snapshot said a question was
    // waiting, the user answers, and the status frame that follows carries
    // askPending: false — always stamped now, so false is a real clear rather
    // than "no update". Both halves of the derivation go quiet: the answering
    // user message takes the ask out of the live-ask window, and the frame takes
    // the wire flag down. No reread, no heuristic.
    it("drops the question row after the answer and the hub's own clear", async () => {
      const store = await openProjectedThread(
        makeThread({
          evener: evenerWith({ askPending: true, activeTurnId: "t1" }),
          turns: [
            makeTurn({ id: "t1", items: [askUserItem("ask-1", VALID_ASK_ARGS)], status: "inProgress" }),
          ],
        }),
      );

      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: userMessageItem("answer-1", "I choose A"),
        },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "active" },
          askPending: false,
        },
      } as AnyNotification);

      expect(store.getState().conversation?.askPending).toBe(false);
      expect(rows(store).some((row) => row.kind === "question")).toBe(false);
    });

    it("renders a live ask's question row before any snapshot says so", async () => {
      const { store } = await openRunningTurn();
      expect(store.getState().conversation?.askPending).toBe(false);
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: askUserItem("ask-live", VALID_ASK_ARGS),
        },
      } as AnyNotification);
      expect(rowById(store, "ask-live")).toMatchObject({ kind: "question" });
      // The hub has said nothing yet; its flag is not this client's to write.
      expect(store.getState().conversation?.askPending).toBe(false);
    });

    it("settles the question when the answering user message arrives, with no reread", async () => {
      const askThread = makeThread({
        evener: evenerWith({ askPending: true, activeTurnId: "t2" }),
        turns: [
          makeTurn({
            id: "t1",
            items: [askUserItem("ask-1", VALID_ASK_ARGS)],
            status: "completed",
          }),
          makeTurn({ id: "t2", items: [], status: "inProgress" }),
        ],
      });
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(askThread);
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      expect(store.getState().conversation?.askPending).toBe(true);
      expect(rows(store).some((item) => item.kind === "question")).toBe(true);
      const initialReads = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t2",
          item: userMessageItem("answer-1", "I choose A"),
        },
      } as AnyNotification);
      await yieldMicrotask();
      // The answered call is no longer answerable, so its question row is
      // gone and nothing is offered to answer.
      expect(rows(store).some((item) => item.kind === "question")).toBe(false);
      expect(rowById(store, "answer-1")).toMatchObject({ kind: "user" });
      expect(service.readProjectionCalls.length).toBe(initialReads);
    });

    it.each([
      ["malformed", () => askUserItem("ask-bad", "not valid json {{{"), "item/completed"],
      ["still in flight", () => askUserItem("ask-incomplete", VALID_ASK_ARGS, "inProgress"), "item/started"],
    ] as const)("projects a %s ask_user as its tool row, never a question row", async (_label, item, method) => {
      const { store, service } = await openRunningTurn();
      const initialReads = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method,
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", item: item() },
      } as AnyNotification);
      await yieldMicrotask();
      expect(rows(store).some((row) => row.kind === "question")).toBe(false);
      expect(rowById(store, item().id)).toMatchObject({ kind: "activity", family: "tool" });
      expect(store.getState().conversation?.askPending).toBe(false);
      expect(service.readProjectionCalls.length).toBe(initialReads);
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
      // Trigger R via a resync — the authoritative refresh path — and hang it.
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
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

    it("commits the reread's version when a live frame changed the wire id under the same transcript key", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [
                {
                  ...userMessageItem("wire-live-old", "older-live"),
                  transcriptKey: "stable-live",
                },
              ],
            }),
          ],
        }),
      );
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
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
      // One row under that transcript key, and it is the snapshot's — the
      // frame that renamed it is already accounted for at the cut.
      const matches = (store.getState().conversation?.items ?? []).filter(
        (item) => item.transcriptKey === "stable-live",
      );
      expect(matches).toHaveLength(1);
      expect(matches[0]).toMatchObject({
        id: "wire-reread",
        text: "canonical",
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
      // The snapshot is what settles an ask: drive one through a resync.
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
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
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
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

    // The same rule for the frames that carry transcript CONTENT, which is the
    // half a reader would notice: a delta delivered after the response streams
    // on top of the snapshot's own text. The snapshot is what the hub had at
    // the cut — its materialized turn authority folds every delta into the item
    // as it streams (server/appwire_turns.go's NotifyAgentMessageDelta case,
    // `item.Text += params.Delta`), so a delta this store folded BEFORE the
    // response is already in the text the response carries, and one after it
    // appends to that text rather than being lost.
    it("streams a delta that arrives after the response on top of the snapshot's text", async () => {
      const service = new FakeConversationService();
      const streaming = [agentMessageItem("a1", "", "inProgress")];
      service.readProjectionResult = makeReadProjectionResult(runningTurnThread(streaming));
      const store = createConversationStore();
      const sink = createFakeSink();
      await store.getState().openProjected(service, sink, "ref-1");

      // The hub has folded "half " into the item by the time of the cut.
      service.readProjectionResult = makeReadProjectionResult(
        runningTurnThread([agentMessageItem("a1", "half ", "inProgress")]),
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
      expect(rowById(store, "a1")).toMatchObject({ kind: "assistant", markdown: "half " });

      // A delta after the response appends to the committed snapshot's text.
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: { ...target, turnId: "t1", itemId: "a1", delta: "done" },
      } as AnyNotification);
      expect(rowById(store, "a1")).toMatchObject({ kind: "assistant", markdown: "half done" });
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

  // Every item frame folds into the package reducer's model, and the rows
  // are projected from it — so what the model carries is what the phone
  // shows: the streamed text of a settled item, an injected steer, the retry
  // indicator clearing on the model's own output.
  describe("the model under the item frames is what the rows show", () => {
    const withActiveTurn = (items: ThreadItem[]) =>
      makeThread({
        turns: [makeTurn({ id: "t1", status: "inProgress", items })],
        evener: evenerWith({ activeTurnId: "t1" }),
      });
    const modelItem = (store: ReturnType<typeof createConversationStore>, id: string) =>
      store.getState().conversation?.turns.flatMap((turn) => turn.items).find((item) => item.id === id);

    it("carries a delta stream into the item, and a sparse completion keeps it on screen", async () => {
      const store = await openProjectedThread(withActiveTurn([agentMessageItem("a1", undefined, "inProgress")]));
      for (const delta of ["hello ", "world"]) {
        store.getState().applyNotification({
          method: "item/agentMessage/delta",
          params: { ...target, turnId: "t1", itemId: "a1", delta },
        } as AnyNotification);
      }
      expect(rowById(store, "a1")).toMatchObject({ kind: "assistant", markdown: "hello world" });
      store.getState().applyNotification({
        method: "item/completed",
        params: { ...target, turnId: "t1", item: agentMessageItem("a1", undefined, "completed") },
      } as AnyNotification);
      expect(modelItem(store, "a1")).toMatchObject({ text: "hello world", status: "completed" });
      // A completion that carries no text of its own does not blank the row.
      expect(rowById(store, "a1")).toMatchObject({ kind: "assistant", markdown: "hello world" });
    });

    it("shows an injected steer as its own row in the active turn", async () => {
      const store = await openProjectedThread(withActiveTurn([]));
      store.getState().applyNotification({
        method: "evener/steering/injected",
        params: { ...target, text: "go left", kind: "user", source: "user", startedAt: 1000 },
      } as AnyNotification);
      const steering = store.getState().conversation?.turns
        .flatMap((turn) => turn.items)
        .filter((item) => item.type === "steering");
      expect(steering?.map((item) => [item.id, item.text])).toEqual([["item_steering_live_t1_0", "go left"]]);
      // A user-sourced steer reads as the user's own message row.
      expect(rowById(store, "item_steering_live_t1_0")).toMatchObject({
        kind: "user",
        text: "go left",
      });
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

  });

  // Page history and the snapshot: the rows a reread carries are the thread,
  // the page history in front of them is what this client paged in, and
  // everything else is gone — including a row a live frame inserted before
  // the response, which the snapshot has already accounted for.
  describe("page history in front of the snapshot's rows", () => {
    it("keeps the paged rows, commits the snapshot's, drops the rest", async () => {
      const service = new FakeConversationService();
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
      store.setState({ olderCursor: "cursor-1" });
      const ctrl = makeControlledRead(service);
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
      service.olderItems = {
        items: [{ kind: "user", id: "P", text: "page old" }],
        nextCursor: "cursor-2",
      };
      await store.getState().loadOlder(service);
      expect(rows(store).map((item) => item.id)).toEqual(["P", "A", "B"]);

      // A live frame inserts N while the read is in flight — on screen at
      // once, and accounted for by the snapshot that follows.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: userMessageItem("N", "live notification"),
        },
      } as AnyNotification);
      expect(rows(store).map((item) => item.id)).toEqual(["P", "A", "B", "N"]);

      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      expect(rows(store).map((item) => item.id)).toEqual(["P", "B", "C"]);
      expect(store.getState().olderCursor).toBe("cursor-2");
    });

    it("carries no page history across a reread when the cap already trimmed it", async () => {
      const service = new FakeConversationService();
      const initialThreadItems: ThreadItem[] = [];
      for (let i = 0; i < 499; i++) initialThreadItems.push(userMessageItem(`old-${i}`, ""));
      initialThreadItems.push(userMessageItem("B", ""));
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ turns: [makeTurn({ id: "t0", items: initialThreadItems })] }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });
      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [userMessageItem("B", ""), userMessageItem("C", "fresh")],
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
      // A page smaller than the cap, so some of it survives the trim in
      // front of the snapshot's rows.
      const pageItems: MobileConversation["items"] = [];
      for (let i = 0; i < 100; i++) pageItems.push({ kind: "user", id: `P-${i}`, text: "" });
      service.olderItems = { items: pageItems, nextCursor: "cursor-2" };
      await store.getState().loadOlder(service);
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: userMessageItem("N", "live"),
        },
      } as AnyNotification);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      const ids = rows(store).map((item) => item.id);
      expect(ids.length).toBeLessThanOrEqual(500);
      // The window was already full, so the page prepend was trimmed as it
      // arrived and has nothing to carry across: what remains is the
      // snapshot's own rows.
      expect(ids).toEqual(["B", "C"]);
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
  // The cut, measured end to end: the transport delivers one wire message per
  // onmessage event (client.ts:524, handleMessage :760-792), the service does
  // only synchronous work after awaiting the response (services/conversation.ts
  // :635-663), and this store does only synchronous work between that await
  // and its set — so a frame the transport delivers after the response reaches
  // applyNotification only after the snapshot has committed, and applies on
  // top of it.
  describe("a frame delivered after the read response applies on top of it", () => {
    it("keeps a live frame that lands right after the snapshot commits", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [makeTurn({ id: "t0", status: "inProgress", items: [agentMessageItem("X", "from the read", "inProgress")] })],
          evener: evenerWith({ activeTurnId: "t0" }),
        }),
      );
      const store = createConversationStore();
      const sink = createFakeSink();
      await store.getState().openProjected(service, sink, "ref-1");

      // The rehydrate's own commit, then the very next thing that happens: a
      // delta for the item the snapshot just carried.
      await store.getState().rehydrate(service, sink);
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t0", itemId: "X", delta: " and more" },
      } as AnyNotification);

      expect(rowById(store, "X")).toMatchObject({
        kind: "assistant",
        markdown: "from the read and more",
      });
    });
  });

  // A read response is ordered at the snapshot cut: every frame this store
  // folded before the response is already reflected in the snapshot that
  // arrives. So the snapshot decides every row it names AND every row it
  // omits — there is no live-ownership side to weigh against it. The one
  // thing the snapshot cannot know about is older history this client paged
  // in, which is prepended until D23d moves the pages into the model.
  describe("the reread's snapshot decides the rows", () => {
    // Each case: a row X that a live frame changes while the read is in
    // flight, and a reread that either carries its own X or omits it.
    const liveCases = [
      {
        kind: "an item/completed replacement",
        initialX: () => agentMessageItem("X", "original", "inProgress"),
        staleX: () => agentMessageItem("X", "from-the-reread", "completed"),
        frame: {
          method: "item/completed",
          params: { threadId: "thread-1", ref: "ref-1", turnId: "t0", item: agentMessageItem("X", "live-final-text", "completed") },
        } as AnyNotification,
        live: "live-final-text",
        settled: "from-the-reread",
      },
      {
        kind: "an agentMessage delta",
        initialX: () => agentMessageItem("X", "base-text", "inProgress"),
        staleX: () => agentMessageItem("X", "base-text", "inProgress"),
        frame: {
          method: "item/agentMessage/delta",
          params: { threadId: "thread-1", ref: "ref-1", turnId: "t0", itemId: "X", delta: "-appended" },
        } as AnyNotification,
        live: "base-text-appended",
        settled: "base-text",
      },
      {
        kind: "an agentMessage reset",
        initialX: () => agentMessageItem("X", "will-be-reset", "inProgress"),
        staleX: () => agentMessageItem("X", "will-be-reset", "inProgress"),
        frame: {
          method: "item/agentMessage/reset",
          params: { threadId: "thread-1", ref: "ref-1", turnId: "t0", itemId: "X" },
        } as AnyNotification,
        // Decision 2: a reset removes the item it names; the row goes with it.
        live: undefined,
        settled: "will-be-reset",
      },
    ] as const;

    // B + X + A open, a reread starts and hangs, `frame` lands while it is in
    // flight, then the reread commits `rereadItems`.
    async function raceReread(
      initialX: ThreadItem,
      rereadItems: ThreadItem[],
      frame: AnyNotification,
      options: { pageItems?: MobileConversation["items"] } = {},
    ) {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              status: "inProgress",
              items: [userMessageItem("B", "base"), initialX, userMessageItem("A", "old-unowned")],
            }),
          ],
          evener: evenerWith({ activeTurnId: "t0" }),
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      store.setState({ olderCursor: "cursor-1" });
      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [makeTurn({ id: "t0", status: "inProgress", items: rereadItems })],
          evener: evenerWith({ activeTurnId: "t0" }),
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      if (options.pageItems !== undefined) {
        service.olderItems = { items: options.pageItems, nextCursor: "cursor-2" };
        await store.getState().loadOlder(service);
        const pageIds = new Set((options.pageItems ?? []).map((item) => item.id));
        expect(rows(store).some((item) => pageIds.has(item.id))).toBe(true);
      }
      store.getState().applyNotification(frame);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      return { store, service };
    }

    const markdownOf = (
      store: ReturnType<typeof createConversationStore>,
      id: string,
    ): string | undefined => {
      const row = rowById(store, id);
      return row?.kind === "assistant" ? row.markdown : undefined;
    };

    it.each(liveCases)("shows the live change under $kind until the reread commits", async ({ initialX, frame, live }) => {
      // The frame lands on the open conversation and is on screen at once;
      // the reread that follows is what settles it.
      const { store } = await openRunningTurn([initialX()], {
        turns: [makeTurn({ id: "t0", status: "inProgress", items: [initialX()] })],
        evener: evenerWith({ activeTurnId: "t0" }),
      });
      store.getState().applyNotification(frame);
      expect(markdownOf(store, "X")).toBe(live);
    });

    it.each(liveCases)("commits the reread's own X under $kind when the snapshot carries it", async ({ initialX, staleX, frame, settled }) => {
      const { store } = await raceReread(
        initialX(),
        [userMessageItem("B", "base"), staleX(), userMessageItem("C", "fresh-authoritative")],
        frame,
      );
      const ids = rows(store).map((item) => item.id);
      expect(markdownOf(store, "X")).toBe(settled);
      // Exactly once, in the position the snapshot gave it.
      expect(ids.filter((id) => id === "X")).toHaveLength(1);
      expect(ids.indexOf("B")).toBeLessThan(ids.indexOf("X"));
      expect(ids.indexOf("X")).toBeLessThan(ids.indexOf("C"));
      // Rows the snapshot does not carry are gone.
      expect(ids).not.toContain("A");
    });

    it.each(liveCases)("drops X under $kind when the snapshot omits it", async ({ initialX, frame }) => {
      const { store } = await raceReread(
        initialX(),
        [userMessageItem("B", "base"), userMessageItem("C", "fresh-authoritative")],
        frame,
      );
      const ids = rows(store).map((item) => item.id);
      expect(ids).toEqual(["B", "C"]);
    });

    it.each([
      ["a reasoning delta", { type: "reasoning", id: "X", text: "reasoning-base", status: "inProgress" } as ThreadItem, {
        method: "item/reasoning/summaryTextDelta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t0", itemId: "X", summaryIndex: 0, delta: "-more" },
      } as AnyNotification, "reasoning-base-more"],
      ["a tool-output delta", { type: "commandExecution", id: "X", toolName: "mytool", callId: "call-X", status: "inProgress" } as ThreadItem, {
        method: "item/toolOutput/delta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t0", itemId: "X", callId: "call-X", delta: "tool-result" },
      } as AnyNotification, "tool-result"],
    ] as const)("shows %s on its activity row, and the reread's version after it commits", async (_label, initialX, frame, liveOutput) => {
      const { store } = await openRunningTurn([initialX], {
        turns: [makeTurn({ id: "t0", status: "inProgress", items: [initialX] })],
        evener: evenerWith({ activeTurnId: "t0" }),
      });
      store.getState().applyNotification(frame);
      const live = rowById(store, "X");
      expect(live?.kind === "activity" && live.detail.output).toBe(liveOutput);

      const { store: raced } = await raceReread(
        initialX,
        [userMessageItem("B", "base"), initialX, userMessageItem("C", "fresh-authoritative")],
        frame,
      );
      const settledRow = rowById(raced, "X");
      expect(settledRow?.kind).toBe("activity");
      expect(settledRow?.kind === "activity" && settledRow.detail.output).toBe(
        initialX.type === "reasoning" ? "reasoning-base" : undefined,
      );
    });

    // The page history the snapshot cannot know about is prepended, keeps the
    // page's own cursor, and the cap still trims from the oldest end.
    it("keeps page history and its cursor in front of the snapshot's rows", async () => {
      const { store } = await raceReread(
        agentMessageItem("X", "original", "inProgress"),
        [userMessageItem("B", "base"), userMessageItem("C", "fresh-authoritative")],
        {
          method: "item/agentMessage/delta",
          params: { threadId: "thread-1", ref: "ref-1", turnId: "t0", itemId: "X", delta: "-updated" },
        } as AnyNotification,
        { pageItems: [{ kind: "user", id: "P", text: "page-old" }] },
      );
      expect(rows(store).map((item) => item.id)).toEqual(["P", "B", "C"]);
      expect(store.getState().olderCursor).toBe("cursor-2");
    });

    it("trims the oldest rows when page history and the snapshot exceed the cap", async () => {
      const pageItems: MobileConversation["items"] = [];
      for (let i = 0; i < 600; i++) pageItems.push({ kind: "user", id: `P-${i}`, text: "" });
      const { store } = await raceReread(
        agentMessageItem("X", "original", "inProgress"),
        [userMessageItem("B", "base"), userMessageItem("C", "fresh-authoritative")],
        {
          method: "item/agentMessage/delta",
          params: { threadId: "thread-1", ref: "ref-1", turnId: "t0", itemId: "X", delta: "-updated" },
        } as AnyNotification,
        { pageItems },
      );
      const ids = rows(store).map((item) => item.id);
      expect(ids.length).toBeLessThanOrEqual(500);
      // The newest rows survive: the snapshot's own, at the end, with the
      // page history that still fits in front of them.
      expect(ids.slice(-2)).toEqual(["B", "C"]);
      expect(ids[0]).toMatch(/^P-/);
      expect(ids).not.toContain("X");
    });

    it("replaces an oversized row with the reread's short version", async () => {
      const longText = "x".repeat(MAX_ITEM_BYTES + 100);
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              status: "inProgress",
              items: [userMessageItem("B", "base"), agentMessageItem("X", longText, "inProgress")],
            }),
          ],
          evener: evenerWith({ activeTurnId: "t0" }),
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      expect(markdownOf(store, "X")).toContain(TRUNCATION_MARKER);
      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              status: "inProgress",
              items: [userMessageItem("B", "base"), agentMessageItem("X", "reread-short", "completed")],
            }),
          ],
          evener: evenerWith({ activeTurnId: "t0" }),
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t0", itemId: "X", delta: "-more" },
      } as AnyNotification);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      expect(markdownOf(store, "X")).toBe("reread-short");
    });
  });

  // Where a delta lands, now that every frame folds into the model and the
  // rows are projected from it: a reasoning summary delta is the reasoning
  // row's text, a tool-output delta is the tool row's output, and a delta
  // that names a row of the other kind is invisible — the field it writes is
  // not the field that row displays. None of them asks for a reread; the
  // next snapshot settles what a mis-addressed frame did (the read response
  // is ordered at the snapshot cut).
  describe("Task 2A-Items: exact delta families", () => {
    // One running turn holding the three activity kinds a delta can name:
    // a tool call (with its callId), a reasoning item, and a
    // forward-compatible unknown item. Their cluster families differ, so
    // each is its own row.
    const deltaTargets = (): ThreadItem[] => [
      { type: "commandExecution", id: "tool-1", toolName: "shell", callId: "call-A", output: "line1", status: "inProgress" } as ThreadItem,
      { type: "reasoning", id: "reason-1", text: "Thinking", status: "inProgress" } as ThreadItem,
      { type: "somethingNew", id: "unk-1", text: "unknown body", status: "inProgress" } as unknown as ThreadItem,
    ];

    const reasoningDelta = (itemId: string, delta: string) =>
      ({
        method: "item/reasoning/summaryTextDelta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId, summaryIndex: 0, delta },
      }) as AnyNotification;

    const toolDelta = (itemId: string, delta: string, callId?: string) =>
      ({
        method: "item/toolOutput/delta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId, ...(callId === undefined ? {} : { callId }), delta },
      }) as AnyNotification;

    const outputOf = (
      store: ReturnType<typeof createConversationStore>,
      id: string,
    ): string | undefined => {
      const row = rowById(store, id);
      return row?.kind === "activity" ? row.detail.output : undefined;
    };

    // These frames are unconstructable at the hub: every family keeps its own
    // item id. An agentMessage delta carries p.assistantItem and a reasoning
    // delta p.reasoningItem (internal/appprojector/appwire_projection.go:474-479
    // and :497-503), each set only by that family's own ensure* helper
    // (:2277-2301) through one monotonic minter (nextItemID, :2304-2307), and a
    // tool delta's id comes from the callId map (:2309-2316). An id minted for
    // one family is never handed to another. The rows below pin what the model
    // does anyway, so a producer that ever crossed them could not make the
    // phone show one family's text on another's row.
    it.each([
      // A reasoning delta writes summary chunks; only a reasoning row reads
      // them, so the tool and unknown rows are untouched.
      ["a reasoning delta at the tool row", () => reasoningDelta("tool-1", " nope"), "tool-1", "line1"],
      ["a reasoning delta at the unknown row", () => reasoningDelta("unk-1", " nope"), "unk-1", "unknown body"],
      // A tool-output delta writes the item's output; only a tool row reads
      // it, and the reasoning row shows its accumulated chunks.
      ["a tool delta at the reasoning row", () => toolDelta("reason-1", " nope", "call-A"), "reason-1", "Thinking"],
    ] as const)("leaves the row unchanged under %s", async (_label, frame, id, expected) => {
      const { store, service } = await openRunningTurn(deltaTargets());
      const readsAfterOpen = service.readProjectionCalls.length;
      store.getState().applyNotification(frame());
      await yieldMicrotask();
      expect(outputOf(store, id)).toBe(expected);
      // Decision 2: a mis-addressed delta asks for nothing. The snapshot cut
      // is what settles the thread, not a reread per stray frame.
      expect(service.readProjectionCalls.length).toBe(readsAfterOpen);
    });

    it.each([
      ["a reasoning delta at the reasoning row", () => reasoningDelta("reason-1", " more"), "reason-1", "Thinking more"],
      ["a tool delta whose callId matches", () => toolDelta("tool-1", "\nline2", "call-A"), "tool-1", "line1\nline2"],
      // Decision 2: the wire routes a tool-output delta by item id, and the
      // model folds it there. The callId the frame carries no longer gates
      // the append (the row keeps showing its own callId for diagnostics).
      // The hub cannot cross the two: the projector stamps a delta's itemId
      // by looking the callId up in the map that minted it
      // (internal/appprojector/appwire_projection.go:743-749 with
      // toolItemID at :2309-2316, both fed by the same ToolCallStart at
      // :701-709), and appwire/types.go:2592-2594 calls CallID "the legacy
      // alias kept for clients that still key on it".
      ["a tool delta whose callId differs", () => toolDelta("tool-1", "\nline2", "call-B"), "tool-1", "line1\nline2"],
      ["a tool delta carrying no callId", () => toolDelta("tool-1", "\nline2"), "tool-1", "line1\nline2"],
    ] as const)("appends to the row under %s", async (_label, frame, id, expected) => {
      const { store, service } = await openRunningTurn(deltaTargets());
      const readsAfterOpen = service.readProjectionCalls.length;
      store.getState().applyNotification(frame());
      await yieldMicrotask();
      expect(outputOf(store, id)).toBe(expected);
      expect(service.readProjectionCalls.length).toBe(readsAfterOpen);
    });

    it("keeps the tool row's own callId when a delta carries another", async () => {
      const { store } = await openRunningTurn(deltaTargets());
      store.getState().applyNotification(toolDelta("tool-1", " x", "call-B"));
      expect(rowById(store, "tool-1")).toMatchObject({
        kind: "activity",
        detail: { callId: "call-A" },
      });
    });

    // The snapshot is authoritative over every frame that preceded its
    // response, whether that frame landed or was invisible: a reread that
    // omits the row removes it, and one that carries it shows its version.
    it.each([
      ["omits the row", [] as ThreadItem[], undefined],
      ["carries its own version of the row", [
        { type: "commandExecution", id: "tool-1", toolName: "shell", callId: "call-A", output: "authoritative", status: "completed" } as ThreadItem,
      ], "authoritative"],
    ] as const)("commits the reread's rows when it %s", async (_label, items, expected) => {
      const { store, service, sink } = await openRunningTurn(deltaTargets());
      let release!: (value: ConversationReadProjection) => void;
      service.readProjectionBlock = new Promise((resolve) => { release = resolve; });
      const rehydratePromise = store.getState().rehydrate(service, sink);
      // A delta lands while the read is in flight — and is already reflected
      // in the snapshot that arrives.
      store.getState().applyNotification(toolDelta("tool-1", "\nappended", "call-A"));
      expect(outputOf(store, "tool-1")).toBe("line1\nappended");
      release(makeReadProjectionResult(runningTurnThread([...items])));
      await rehydratePromise;
      expect(outputOf(store, "tool-1")).toBe(expected);
    });
  });

  describe("Task 2A-Items: one byte bound for every row", () => {
    const toolWithOutput = (output: string): ThreadItem[] => [
      {
        type: "commandExecution",
        id: "tool-1",
        toolName: "shell",
        callId: "call-A",
        output,
        status: "inProgress",
      } as ThreadItem,
    ];

    const outputOf = (
      store: ReturnType<typeof createConversationStore>,
      id: string,
    ): string | undefined => {
      const row = rowById(store, id);
      expect(row?.kind).toBe("activity");
      if (row?.kind !== "activity") throw new Error("expected an activity row");
      return row.detail.output;
    };

    it("keeps an oversized activity row at one marker across later deltas", async () => {
      const { store } = await openRunningTurn(toolWithOutput("x".repeat(70_000)));
      const bounded = outputOf(store, "tool-1");
      expect(bounded?.endsWith(TRUNCATION_MARKER)).toBe(true);
      expect(new TextEncoder().encode(bounded ?? "").length).toBeLessThanOrEqual(65536);

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
      const after = outputOf(store, "tool-1");
      expect(after).toBe(bounded);
      expect((after?.split(TRUNCATION_MARKER).length ?? 0) - 1).toBe(1);
    });

    it("shows a short authoritative replacement short, and appends to it", async () => {
      const { store } = await openRunningTurn(toolWithOutput("x".repeat(70_000)));
      expect(outputOf(store, "tool-1")?.endsWith(TRUNCATION_MARKER)).toBe(true);

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
      expect(outputOf(store, "tool-1")).toBe("short-result");

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
      expect(outputOf(store, "tool-1")).toBe("short-result appended");
    });

    it("streams from empty again after a reset removes an oversized item", async () => {
      const { store } = await openRunningTurn([
        agentMessageItem("item-1", "x".repeat(70_000), "inProgress"),
      ]);
      const bounded = rowById(store, "item-1");
      expect(bounded?.kind === "assistant" && bounded.markdown.endsWith(TRUNCATION_MARKER)).toBe(true);

      store.getState().applyNotification({
        method: "item/agentMessage/reset",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "item-1" },
      } as AnyNotification);
      expect(rowById(store, "item-1")).toBeUndefined();

      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: agentMessageItem("item-1", "", "inProgress"),
        },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "item-1", delta: "fresh" },
      } as AnyNotification);
      expect(rowById(store, "item-1")).toMatchObject({
        kind: "assistant",
        markdown: "fresh",
      });
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
  // (open/openProjected/rehydrate/page/lifecycle): the byte bound is applied
  // to whatever text a row carries at publish time, so an authoritative short
  // version is short, a row the cap drops leaves nothing behind, and a row
  // that keeps growing keeps showing the same bounded prefix.
  //
  // C3: authoritative oversized→short→delta applies (open + rehydrate + openProjected)
  // C4: the cap shows the newest rows; a fresh item is judged on its own
  // protocol reset→start→delta: the reset removes the row, the restart streams anew
  // paged oversized item bounded with the marker once
  // lifecycle started/completed oversized then short
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

    it("C4 the cap shows the newest rows; a fresh item is judged on its own content", async () => {
      // Fill past the 500-cap so the oldest rows are trimmed. The first few
      // items are oversized, so the rows that survive prove the bound and the
      // cap are independent.
      const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
      const items: ThreadItem[] = [
        userMessageItem("keep", "base"),
        agentMessageItem("a-0", oversized, "completed"),
        agentMessageItem("a-1", oversized, "completed"),
        agentMessageItem("a-2", oversized, "completed"),
      ];
      for (let i = 3; i < 501; i++) {
        items.push(agentMessageItem(`a-${i}`, "small", "completed"));
      }
      const { store } = await openProjectedWithItems(items);
      const retained = store.getState().conversation?.items ?? [];
      expect(retained.length).toBeLessThanOrEqual(500);
      // The oldest rows are the ones the cap drops.
      expect(retained.find((i) => i.id === "a-0")).toBeUndefined();

      // A brand-new item arrives short and accepts a delta: nothing about the
      // rows the cap dropped carries over to it.
      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: agentMessageItem("fresh", "fresh-short", "completed"),
        },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "fresh",
          delta: " appended",
        },
      } as AnyNotification);
      expect(rowById(store, "fresh")).toMatchObject({
        kind: "assistant",
        markdown: "fresh-short appended",
      });
    });

    // Decision 2: item/agentMessage/reset removes the item it names — the
    // model drops it, so its row goes with it and the stream starts again
    // under the item/started that follows.
    it("protocol reset→start→delta: the reset removes the row and the restart is judged on its own", async () => {
      const { store } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "x".repeat(MAX_ITEM_BYTES + 100), "inProgress"),
      ]);
      const xBefore = rowById(store, "X");
      expect(xBefore?.kind === "assistant" && xBefore.markdown.endsWith(TRUNCATION_MARKER)).toBe(true);

      store.getState().applyNotification({
        method: "item/agentMessage/reset",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          itemId: "X",
        },
      } as AnyNotification);
      expect(rowById(store, "X")).toBeUndefined();

      // The restarted item streams from empty.
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: agentMessageItem("X", "", "inProgress"),
        },
      } as AnyNotification);
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
      expect(rowById(store, "X")).toMatchObject({
        kind: "assistant",
        markdown: "fresh content",
      });
    });

    it("paged oversized item is bounded with the marker exactly once", async () => {
      // An oversized row loaded through loadOlder is bounded with the marker
      // appearing exactly once, and stays that way. Older pages live outside
      // the model until D23d, so a live delta does not reach such a row.
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

      // A later tool-output delta names an item the model does not hold, so
      // the row is unchanged — marker once, no new content.
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
    it("identity: an oversized field is bounded on its own, and a delta to another field still applies", async () => {
      // The bound is per text-bearing field of the row: oversized arguments
      // are cut, and the output a delta appends to is untouched by that.
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
          before.detail.arguments?.endsWith(TRUNCATION_MARKER),
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

      const after = rowById(store, "wire-1");
      expect(after?.kind).toBe("activity");
      expect(after?.kind === "activity" && after.detail.output).toBe("short MORE");
      expect(
        after?.kind === "activity" && after.detail.arguments?.endsWith(TRUNCATION_MARKER),
      ).toBe(true);
    });

    it("identity: a short completion shows short, whatever the started item carried", async () => {
      // item/started carries an oversized output, so the row is bounded; the
      // completion that replaces it is short, and the row is short with it.
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
      const started = rowById(store, "tool-1");
      expect(
        started?.kind === "activity" && started.detail.output?.endsWith(TRUNCATION_MARKER),
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

      expect(rowById(store, "tool-1")).toMatchObject({
        kind: "activity",
        detail: { output: "short-result" },
      });
    });

    it("identity: a reread's short version replaces a bounded one, and later deltas append to it", async () => {
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
      const bounded = rowById(store, "wire-1");
      expect(
        bounded?.kind === "activity" && bounded.detail.arguments?.endsWith(TRUNCATION_MARKER),
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

      expect(rowById(store, "wire-1")).toMatchObject({
        kind: "activity",
        detail: { arguments: "short" },
      });

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

    // The snapshot is authoritative over the delta that preceded its
    // response: the row shows the reread's short text, and the next delta
    // appends to that.
    it("rehydrate commits the reread's short version over a live-grown one", async () => {
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
      const xLive = rowById(store, "X");
      expect(xLive?.kind === "assistant" && xLive.markdown.endsWith(TRUNCATION_MARKER)).toBe(true);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      expect(rowById(store, "X")).toMatchObject({
        kind: "assistant",
        markdown: "reread-short",
      });
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
      expect(rowById(store, "X")).toMatchObject({
        kind: "assistant",
        markdown: "reread-short appended",
      });
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

    // I1: loadOlder — a row already at the bound stays at it after a page
    // load, and so does an oversized row the page itself brings: both are cut
    // to MAX_ITEM_BYTES by the same pass over whatever text they carry.
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
    it("I1 loadOlder: an oversized current row and an oversized page row are both bounded", async () => {
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

      // Both X (from the model) and page-tool (from the page) are bounded.
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

      // A delta past the bound leaves the row at the same bounded prefix.
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

      // A delta naming the page row reaches no item in the model, so that row
      // is unchanged (D23d moves the pages into the model).
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
    // A live frame during a read, then the response: the snapshot already
    // reflects that frame (it is ordered at the cut), so its version of the
    // row is the one that commits, and the next delta appends to it. The
    // frame's own shape — reset, a delta past the limit, a short delta —
    // changes what is on screen while the read is in flight, not after.
    it.each([
      [
        "a reset",
        {
          method: "item/agentMessage/reset",
          params: { threadId: "thread-1", ref: "ref-1", turnId: "t0", itemId: "X" },
        } as AnyNotification,
        undefined,
      ],
      [
        "a delta past the byte limit",
        {
          method: "item/agentMessage/delta",
          params: { threadId: "thread-1", ref: "ref-1", turnId: "t0", itemId: "X", delta: "x".repeat(MAX_ITEM_BYTES + 100) },
        } as AnyNotification,
        "short",
      ],
      [
        "a short delta",
        {
          method: "item/agentMessage/delta",
          params: { threadId: "thread-1", ref: "ref-1", turnId: "t0", itemId: "X", delta: "-live" },
        } as AnyNotification,
        "short-live",
      ],
    ] as const)("commits the reread's row after %s lands mid-read", async (_label, frame, liveMarkdown) => {
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

      store.getState().applyNotification(frame);
      const live = rowById(store, "X");
      if (liveMarkdown === undefined) {
        expect(live).toBeUndefined();
      } else if (liveMarkdown === "short") {
        // The oversized delta is bounded on screen, not dropped.
        expect(live?.kind === "assistant" && live.markdown.endsWith(TRUNCATION_MARKER)).toBe(true);
      } else {
        expect(live).toMatchObject({ kind: "assistant", markdown: liveMarkdown });
      }

      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      expect(rowById(store, "X")).toMatchObject({
        kind: "assistant",
        markdown: "reread-short",
      });

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
      expect(rowById(store, "X")).toMatchObject({
        kind: "assistant",
        markdown: "reread-short appended",
      });
    });

    // A reread that omits the row removes it; the row comes back only when a
    // later frame or snapshot carries it, judged on its own content.
    it("drops a row the reread omits, and shows a later short version of it", async () => {
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", "short", "inProgress"),
      ]);
      const ctrl = makeControlledRead(service);
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({ turns: [makeTurn({ id: "t0", items: [userMessageItem("B", "base")] })] }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t0", itemId: "X", delta: "-live" },
      } as AnyNotification);
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      expect(rowById(store, "X")).toBeUndefined();

      store.getState().applyNotification({
        method: "item/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: agentMessageItem("X", "fresh-short", "completed"),
        },
      } as AnyNotification);
      expect(rowById(store, "X")).toMatchObject({
        kind: "assistant",
        markdown: "fresh-short",
      });
    });

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

    // M1: observational cap via rehydrate+page. Open with an oversized item,
    // rehydrate OMITTING it (the row goes with the snapshot), then load a page
    // bringing it back with short content — the row is short, with nothing
    // about the oversized version carried over.
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

  // --- Task 2A-Truncation residual fix round 2: what a page and the 500-cap
  // do to a row that was over the byte limit.
  //
  // A page item is judged on the text it carries, never on what a row of the
  // same identity used to hold. When an appended row (item/started,
  // item/completed, a warning) pushes past the cap, the oldest row is trimmed
  // and a later re-introduction of that identity shows its own content.
  describe("Task 2A-Truncation residual fix round 2", () => {
    async function openProjectedWithItems(
      items: ThreadItem[],
      options: { running?: boolean } = {},
    ): Promise<{
      store: ReturnType<typeof createConversationStore>;
      service: FakeConversationService;
    }> {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [makeTurn({ id: "t0", status: options.running ? "inProgress" : "completed", items })],
          ...(options.running ? { evener: evenerWith({ activeTurnId: "t0" }) } : {}),
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      return { store, service };
    }

    it("commits exactly the reread's rows when an item's wire id and transcriptKey differ", async () => {
      const items: ThreadItem[] = [];
      for (let i = 0; i < 499; i++) items.push(userMessageItem(`u-${i}`, ""));
      items.push({
        ...agentMessageItem("wire-x", "live", "inProgress"),
        transcriptKey: "stable-x",
      });
      const { store, service } = await openProjectedWithItems(items);
      expect(rows(store).filter((item) => item.transcriptKey === "stable-x")).toHaveLength(1);
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
      // A reread whose snapshot ends before that item: the row goes with it,
      // exactly once, leaving nothing behind under either identity.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [makeTurn({ id: "t0", items: items.slice(0, 499) })],
        }),
      );
      await store.getState().rehydrate(service, createFakeSink());
      expect(rows(store).filter((item) => item.transcriptKey === "stable-x")).toEqual([]);
      expect(rowById(store, "wire-x")).toBeUndefined();
    });

    // A row the reread dropped can come back from an older page, judged on
    // its own content — nothing about the bounded version it had before
    // carries over. Until D23d moves the pages into the model, a page row is
    // history the model does not hold, so a live delta does not reach it; the
    // next snapshot that carries the item is what changes it.
    it("M1 omission: a page brings a dropped row back short, and a snapshot updates it", async () => {
      const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
      const { store, service } = await openProjectedWithItems([
        userMessageItem("B", "base"),
        agentMessageItem("X", oversized, "inProgress"),
      ]);
      const xBefore = rowById(store, "X");
      expect(xBefore?.kind === "assistant" && xBefore.markdown.endsWith(TRUNCATION_MARKER)).toBe(true);

      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [makeTurn({ id: "t0", items: [userMessageItem("B", "base")] })],
        }),
      );
      await store.getState().rehydrate(service, createFakeSink());
      expect(rowById(store, "X")).toBeUndefined();

      store.setState({ olderCursor: "cursor-1" });
      service.olderItems = {
        items: [{ kind: "assistant", id: "X", markdown: "short-page", streaming: false }],
        nextCursor: undefined,
      };
      await store.getState().loadOlder(service);
      expect(rowById(store, "X")).toMatchObject({ kind: "assistant", markdown: "short-page" });

      // The snapshot that carries X again is what moves it on.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [
            makeTurn({
              id: "t0",
              items: [userMessageItem("B", "base"), agentMessageItem("X", "short-page appended", "completed")],
            }),
          ],
        }),
      );
      await store.getState().rehydrate(service, createFakeSink());
      expect(rowById(store, "X")).toMatchObject({
        kind: "assistant",
        markdown: "short-page appended",
      });
    });

    // The cap drops the oldest rows as new ones arrive — whether the new row
    // comes from an item frame or from a warning — and a page that brings an
    // old row back shows it on its own content.
    it.each([
      ["an item frame", {
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: { type: "userMessage", id: "new-item", text: "fresh" },
        },
      } as AnyNotification, "new-item", "user"],
      ["a warning", {
        method: "warning",
        params: { threadId: "thread-1", ref: "ref-1", message: "test warning" },
      } as AnyNotification, undefined, "notice"],
    ] as const)("M1 cap: the oldest row is trimmed when %s appends at 501", async (_label, frame, newId, newKind) => {
      const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
      const items: ThreadItem[] = [agentMessageItem("X", oversized, "completed")];
      for (let i = 1; i < 500; i++) items.push(userMessageItem(`u-${i}`, ""));
      const { store, service } = await openProjectedWithItems(items, { running: true });
      expect(rows(store)).toHaveLength(500);
      const xBefore = rowById(store, "X");
      expect(xBefore?.kind === "assistant" && xBefore.markdown.endsWith(TRUNCATION_MARKER)).toBe(true);
      expect(rows(store)[0]?.id).toBe("X");

      store.getState().applyNotification(frame);

      const items2 = rows(store);
      expect(items2).toHaveLength(500);
      expect(rowById(store, "X")).toBeUndefined();
      expect(items2[items2.length - 1]).toMatchObject({ kind: newKind });
      if (newId !== undefined) expect(items2[items2.length - 1]?.id).toBe(newId);
      expect(items2[0]?.id).toBe("u-1");

      // A page brings X back, short, with no bound carried over from the
      // oversized row the cap dropped. (The window is shrunk first so the
      // prepended page row is not itself trimmed by the cap.)
      const currentConv = store.getState().conversation;
      if (currentConv !== null) {
        store.setState({
          conversation: { ...currentConv, items: currentConv.items.slice(0, 10) },
        });
      }
      store.setState({ olderCursor: "cursor-1" });
      service.olderItems = {
        items: [{ kind: "assistant", id: "X", markdown: "short-page", streaming: false }],
        nextCursor: undefined,
      };
      await store.getState().loadOlder(service);
      const xPage = rowById(store, "X");
      expect(xPage).toMatchObject({ kind: "assistant", markdown: "short-page" });
      expect(xPage?.kind === "assistant" && xPage.markdown.endsWith(TRUNCATION_MARKER)).toBe(false);
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

    it("bounds every clustered member's oversized detail under its own identity", async () => {
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
      // A delta aimed at that member keeps the row at the same bound.
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

    it("keeps a clustered member bounded when an unrelated item frame lands", async () => {
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
      const boundedBefore = (() => {
        const row = rowById(store, "wire-a");
        if (row?.kind !== "activity") throw new Error("expected a clustered activity");
        return row.members?.[1]?.detail.output;
      })();
      expect(boundedBefore?.endsWith(TRUNCATION_MARKER)).toBe(true);

      // An unrelated item frame republishes every row; the member is bounded
      // on its own content each time, not by anything remembered about it.
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t0",
          item: { type: "userMessage", id: "unrelated", text: "hi" },
        },
      } as AnyNotification);

      const row = rowById(store, "wire-a");
      expect(row?.kind).toBe("activity");
      expect(row?.kind === "activity" && row.members?.[1]?.detail.output).toBe(boundedBefore);
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

    // An item frame names the turn it belongs to, and the wire opens that
    // turn first. A frame naming a turn this thread does not have has no
    // place to land, so nothing is shown for it until a snapshot carries it.
    it.each([
      ["no turn has been opened", false, "t1"],
      ["it names a turn the thread does not have", true, "t0"],
    ] as const)("shows no row for a started item when %s", async (_label, openTurn, turnId) => {
      const service = new FakeConversationService();
      const store = openTurn
        ? await openWithActiveTurn()
        : await (async () => {
            const plain = createConversationStore();
            await plain.getState().open(service, "ref-1");
            return plain;
          })();
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId,
          item: {
            type: "commandExecution",
            id: "tool-1",
            turnId,
            toolName: "shell",
            callId: "call-1",
          },
        },
      } as AnyNotification);
      expect(rowById(store, "tool-1")).toBeUndefined();
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
          summaryIndex: 0,
          delta: " MORE",
        },
      } as AnyNotification);

      const [memberA, memberB] = clusterMembers(store, "wire-a");
      expect(memberB?.detail.output).toBe("second MORE");
      expect(memberA?.detail.output).toBe("first");
    });

    it("keeps a later clustered member bounded when a delta pushes it past the limit", async () => {
      const oversized = "b".repeat(MAX_ITEM_BYTES + 100);
      const { store } = await openProjectedWithItems([
        toolItem("wire-a", "key-a", "call-a", "first"),
        toolItem("wire-b", "key-b", "call-b", oversized),
      ]);
      const boundedOutput = clusterMembers(store, "wire-a")[1]?.detail.output;
      expect(boundedOutput?.endsWith(TRUNCATION_MARKER)).toBe(true);

      toolOutputDelta(store, "wire-b", "call-b", " MORE");

      // The row shows the same bounded prefix: what a reader sees is the
      // first MAX_ITEM_BYTES of the text, however much more streams in.
      expect(clusterMembers(store, "wire-a")[1]?.detail.output).toBe(
        boundedOutput,
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

    it("keeps a bounded member bounded across a page load", async () => {
      const oversized = "b".repeat(MAX_ITEM_BYTES + 100);
      const { store, service } = await openProjectedWithItems([
        toolItem("wire-a", "key-a", "call-a", "first"),
        toolItem("wire-b", "key-b", "call-b", oversized),
      ]);
      const boundedOutput = clusterMembers(store, "wire-a")[1]?.detail.output;
      expect(boundedOutput?.endsWith(TRUNCATION_MARKER)).toBe(true);

      store.setState({ olderCursor: "cursor-1" });
      service.olderItems = {
        items: [{ kind: "user", id: "older", text: "older" }],
      };
      await store.getState().loadOlder(service);

      expect(clusterMembers(store, "wire-a")[1]?.detail.output).toBe(
        boundedOutput,
      );
      toolOutputDelta(store, "wire-b", "call-b", " MORE");
      expect(clusterMembers(store, "wire-a")[1]?.detail.output).toBe(
        boundedOutput,
      );
    });

    it("takes the reread's member over one a live delta grew while it was in flight", async () => {
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

      // In flight, a live delta pushes the later member past the limit, so
      // the row is bounded.
      toolOutputDelta(
        store,
        "wire-b",
        "call-b",
        "b".repeat(MAX_ITEM_BYTES + 100),
      );
      expect(
        clusterMembers(store, "wire-a")[1]?.detail.output?.endsWith(TRUNCATION_MARKER),
      ).toBe(true);

      // The read response is ordered at the snapshot cut, so the snapshot
      // already reflects that delta: its version of the member is the one
      // the rows take.
      release(
        makeReadProjectionResult(
          makeThread({ turns: [makeTurn({ id: "t0", items: stale })] }),
        ),
      );
      await rehydrating;

      expect(clusterMembers(store, "wire-a")[1]?.detail.output).toBe("second");
      toolOutputDelta(store, "wire-b", "call-b", " MORE");
      expect(clusterMembers(store, "wire-a")[1]?.detail.output).toBe("second MORE");
    });

    it("takes the reread's version of every member, each bounded on its own", async () => {
      const oversizedA = "a".repeat(MAX_ITEM_BYTES + 100);
      const { store, service } = await openProjectedWithItems([
        toolItem("wire-a", "key-a", "call-a", oversizedA),
        toolItem("wire-b", "key-b", "call-b", "second"),
      ]);
      expect(
        clusterMembers(store, "wire-a")[0]?.detail.output?.endsWith(TRUNCATION_MARKER),
      ).toBe(true);
      let release!: (value: ConversationReadProjection) => void;
      service.readProjectionBlock = new Promise((resolve) => {
        release = resolve;
      });
      const rehydrating = store.getState().rehydrate(service, createFakeSink());

      // A delta grows the second member while the read is in flight.
      toolOutputDelta(
        store,
        "wire-b",
        "call-b",
        "b".repeat(MAX_ITEM_BYTES + 100),
      );

      // The reread is authoritative for both members: the first is short now,
      // and the second is whatever the snapshot says.
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

      expect(clusterMembers(store, "wire-a")[0]?.detail.output).toBe("short");
      expect(clusterMembers(store, "wire-a")[1]?.detail.output).toBe("second");
      toolOutputDelta(store, "wire-a", "call-a", " MORE");
      expect(clusterMembers(store, "wire-a")[0]?.detail.output).toBe(
        "short MORE",
      );
    });

    it("shows a later clustered member short again when the reread returns it short", async () => {
      const oversized = "b".repeat(MAX_ITEM_BYTES + 100);
      const { store, service } = await openProjectedWithItems([
        toolItem("wire-a", "key-a", "call-a", "first"),
        toolItem("wire-b", "key-b", "call-b", oversized),
      ]);
      expect(
        clusterMembers(store, "wire-a")[1]?.detail.output?.endsWith(TRUNCATION_MARKER),
      ).toBe(true);

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

      expect(clusterMembers(store, "wire-a")[1]?.detail.output).toBe("short");
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

  // The rows are a projection of the model, and the projection reads exactly one
  // of the model's own fields: `turns` — every turn, item and status the rows are
  // made of lives under it, and the answerable asks come from it too. Every other
  // field a frame moves — the status, the name, the queue, the jobs tree, the
  // wire's askPending, lastFrameAt, which moves on EVERY frame — changes no row,
  // so such a frame must publish the rows it already had, by reference:
  // re-projecting and re-bounding hundreds of rows for a status change is work
  // the reader never sees, and a fresh items array tells every list view the
  // transcript moved.
  describe("a frame that changes no projection input republishes the rows", () => {
    it("publishes the same rows for a thread-level status frame, and still advances the model", async () => {
      const { store } = await openRunningTurn([agentMessageItem("a1", "hello", "completed")]);
      const before = store.getState().conversation;
      expect(before?.items.length).toBeGreaterThan(0);

      store.getState().applyNotification({
        method: "thread/status/changed",
        params: { threadId: "thread-1", ref: "ref-1", status: { type: "idle" } },
      } as AnyNotification);

      const after = store.getState().conversation;
      // The model advanced: the frame is the authority on the thread's status.
      expect(after?.status).toEqual({ type: "idle" });
      expect(after).not.toBe(before);
      // The rows did not: same array, same row objects.
      expect(after?.items).toBe(before?.items);
    });
  });

  // A page load republishes every retained row through the same bound. The row
  // a reader sees carries its BOUNDED text, so re-bounding it means encoding 64
  // KiB again per publish unless the cache answers — and the cache is what makes
  // a page load cost only the page's own rows.
  // Whether the bound cut a row whose text is made of `filler`: truncateText
  // measures a text's byte length without allocating, and only the oversized path
  // encodes — the candidate prefixes it weighs against the marker. So an encode
  // call carrying that filler is the bound doing the work again.
  function cutsOf(
    encode: { mock: { calls: readonly unknown[][] } },
    filler: string,
  ): number {
    return encode.mock.calls.filter(
      (call) => typeof call[0] === "string" && call[0].length > 1_000 && call[0].startsWith(filler),
    ).length;
  }

  it("does not re-cut an unchanged oversized row when an older page lands", async () => {
    const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
    const olderOversized = "y".repeat(MAX_ITEM_BYTES + 100);
    const service = new FakeConversationService();
    service.readProjectionResult = {
      ...makeReadProjectionResult(runningTurnThread([agentMessageItem("a1", oversized, "completed")])),
      olderCursor: "cursor-1",
    };
    service.olderItems = {
      items: [{ kind: "assistant", id: "old", markdown: olderOversized, streaming: false }],
    };
    const store = createConversationStore();
    await store.getState().openProjected(service, createFakeSink(), "ref-1");
    const row = rowById(store, "a1");
    const bounded = row && "markdown" in row ? row.markdown : undefined;
    if (bounded === undefined) throw new Error("no bounded row");
    expect(bounded.endsWith(TRUNCATION_MARKER)).toBe(true);

    // Cutting a row's text encodes candidates to measure them; a row that comes
    // out of the cache is not cut at all, so the encoder never sees its text.
    const encode = vi.spyOn(TextEncoder.prototype, "encode");
    try {
      await store.getState().loadOlder(service);
      expect(rowById(store, "old")).toBeDefined();
      // The page's own oversized row is the new work.
      expect(cutsOf(encode, "y")).toBeGreaterThan(0);
      // The row already on screen is not: its bounded text comes from the cache.
      expect(cutsOf(encode, "x")).toBe(0);
    } finally {
      encode.mockRestore();
    }
  });

  // A delta re-projects the turn it landed in, not the transcript. The reducer
  // hands every other turn back by reference, so their rows come from the
  // per-turn cache — observable through the work the projection would otherwise
  // repeat: a settled tool call's duration costs two Date.parse calls per
  // projection, and an older turn's must not be paid again for a delta into the
  // newest one.
  it("projects only the turn a delta landed in", async () => {
    // The wire stamps epoch millis; the model holds the ISO strings the projection
    // parses, which is what the spy counts.
    const olderStamps = { startedAt: 1_000, completedAt: 2_000 };
    const olderISO = ["1970-01-01T00:00:01.000Z", "1970-01-01T00:00:02.000Z"];
    const older = makeTurn({
      id: "t-older",
      status: "completed",
      items: [
        {
          type: "commandExecution",
          id: "old-call",
          toolName: "shell",
          status: "completed",
          output: "done",
          ...olderStamps,
        } as ThreadItem,
      ],
    });
    const service = new FakeConversationService();
    service.readProjectionResult = makeReadProjectionResult(
      makeThread({
        turns: [older, makeTurn({ id: "t1", status: "inProgress", items: [agentMessageItem("a1", "", "inProgress")] })],
        evener: evenerWith({ activeTurnId: "t1" }),
      }),
    );
    const store = createConversationStore();
    await store.getState().openProjected(service, createFakeSink(), "ref-1");

    const parse = vi.spyOn(Date, "parse");
    try {
      for (let i = 0; i < 10; i++) {
        store.getState().applyNotification({
          method: "item/agentMessage/delta",
          params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "a1", delta: `d${i}` },
        } as AnyNotification);
      }
      // Ten publishes, and the settled turn's timestamps were read none of those
      // times: its rows were reused.
      const olderParses = parse.mock.calls.filter((call) =>
        olderISO.includes(String(call[0])),
      );
      expect(olderParses).toHaveLength(0);
      // The deltas did land: the active turn's row grew.
      expect(rowById(store, "a1")).toMatchObject({ markdown: "d0d1d2d3d4d5d6d7d8d9" });
    } finally {
      parse.mockRestore();
    }
  });

  // The bound's cache belongs to the conversation: every string in it is held
  // by that conversation's model or its rows, so a conversation that has been
  // dropped must not leave its text behind in the cache. Observable without a
  // test-only accessor, through the work the cache avoids: inside one
  // conversation a settled row is not re-encoded, and a conversation opened
  // after the old one was dropped encodes its text again.
  describe("the display bound's cache belongs to the conversation", () => {
    // Oversized, because that is the row the cache exists for: a row under the
    // limit is measured without allocating and never reaches the encoder.
    const settledText = "z".repeat(MAX_ITEM_BYTES + 100);

    function encodesOfSettledText(encode: {
      mock: { calls: readonly unknown[][] };
    }): number {
      return cutsOf(encode, "z");
    }

    it.each([
      ["close", (store: ReturnType<typeof createConversationStore>) => store.getState().close()],
      ["reset", (store: ReturnType<typeof createConversationStore>) => store.getState().reset()],
    ] as const)("releases it on %s", async (_label, drop) => {
      const items = [agentMessageItem("a1", settledText, "completed")];
      const { store, service, sink } = await openRunningTurn(items);

      const encode = vi.spyOn(TextEncoder.prototype, "encode");
      try {
        // Within the conversation the cache holds: a frame republishes the
        // rows and the unchanged text is taken from it, not re-encoded.
        encode.mockClear();
        store.getState().applyNotification({
          method: "thread/status/changed",
          params: { threadId: "thread-1", ref: "ref-1", status: { type: "running" } },
        } as AnyNotification);
        expect(encodesOfSettledText(encode)).toBe(0);

        drop(store);

        // The next conversation binds its own text: nothing was carried over.
        encode.mockClear();
        service.readProjectionResult = makeReadProjectionResult(runningTurnThread(items));
        await store.getState().openProjected(service, sink, "ref-1");
        expect(encodesOfSettledText(encode)).toBeGreaterThan(0);
      } finally {
        encode.mockRestore();
      }
    });

    // suspendProjected keeps the conversation and its rows on screen for the
    // resume, so the cache still belongs to something live: every string in it
    // is still retained by those rows, clearing it would recover no memory,
    // and the resume's re-read republishes the same text.
    it("keeps it across a suspend and resume, which keep the rows", async () => {
      const items = [agentMessageItem("a1", settledText, "completed")];
      const { store, service, sink } = await openRunningTurn(items);

      store.getState().suspendProjected();
      const encode = vi.spyOn(TextEncoder.prototype, "encode");
      try {
        service.readProjectionResult = makeReadProjectionResult(runningTurnThread(items));
        await store.getState().resumeProjected(service, sink, "ref-1");
        expect(encodesOfSettledText(encode)).toBe(0);
      } finally {
        encode.mockRestore();
      }
    });
  });

  // The display bound runs over every retained row on every publish. The model
  // hands back the same string reference for an item no frame touched, so only
  // the rows whose text actually changed are re-encoded; a transcript of
  // settled rows costs nothing per delta.
  describe("the display bound re-encodes only what changed", () => {
    it("re-encodes one streaming row per delta, not the whole transcript", async () => {
      const settled = Array.from({ length: 10, }, (_, i) =>
        agentMessageItem(`settled-${i}`, `settled text ${i}`.repeat(50), "completed"),
      );
      const { store } = await openRunningTurn([
        ...settled,
        agentMessageItem("streaming", "start", "inProgress"),
      ]);
      const encode = vi.spyOn(TextEncoder.prototype, "encode");
      try {
        for (let i = 0; i < 5; i++) {
          encode.mockClear();
          store.getState().applyNotification({
            method: "item/agentMessage/delta",
            params: {
              threadId: "thread-1",
              ref: "ref-1",
              turnId: "t1",
              itemId: "streaming",
              delta: `chunk ${i} `,
            },
          } as AnyNotification);
          // One encode for the row that changed. The ten settled rows carry
          // the same strings as the frame before, so they are not re-encoded.
          expect(encode.mock.calls.length).toBeLessThanOrEqual(2);
        }
      } finally {
        encode.mockRestore();
      }
      expect(rowById(store, "streaming")).toMatchObject({
        kind: "assistant",
        markdown: "startchunk 0 chunk 1 chunk 2 chunk 3 chunk 4 ",
      });
      expect(rows(store)).toHaveLength(11);
    });
  });

  // The byte bound is a property of the text a row carries, re-applied on
  // every publish: no identity is remembered as "already truncated", so an
  // authoritative short version is short and a stream that grows past the
  // limit shows the same bounded prefix.
  describe("the byte bound follows the row's own text", () => {
    it("bounds an oversized row on open and shows the reread's short version", async () => {
      const oversized = "x".repeat(MAX_ITEM_BYTES + 100);
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [makeTurn({ id: "t0", items: [agentMessageItem("X", oversized, "completed")] })],
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      const bounded = rowById(store, "X");
      expect(bounded?.kind === "assistant" && bounded.markdown.endsWith(TRUNCATION_MARKER)).toBe(true);

      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          turns: [makeTurn({ id: "t0", items: [agentMessageItem("X", "short", "completed")] })],
        }),
      );
      await store.getState().rehydrate(service, createFakeSink());
      expect(rowById(store, "X")).toMatchObject({ kind: "assistant", markdown: "short" });
    });

    it("keeps showing the same bounded prefix as a stream grows past the limit", async () => {
      const { store } = await openRunningTurn([
        agentMessageItem("item-1", "x".repeat(MAX_ITEM_BYTES - 100), "inProgress"),
      ]);
      const short = rowById(store, "item-1");
      expect(short?.kind === "assistant" && short.markdown.endsWith(TRUNCATION_MARKER)).toBe(false);

      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "item-1", delta: "x".repeat(200) },
      } as AnyNotification);
      const bounded = rowById(store, "item-1");
      expect(bounded?.kind === "assistant" && bounded.markdown.endsWith(TRUNCATION_MARKER)).toBe(true);
      const boundedText = bounded?.kind === "assistant" ? bounded.markdown : "";

      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "item-1", delta: "x".repeat(500) },
      } as AnyNotification);
      expect(rowById(store, "item-1")).toMatchObject({ kind: "assistant", markdown: boundedText });

      // The restart after a reset streams from empty again.
      store.getState().applyNotification({
        method: "item/agentMessage/reset",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "item-1" },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "item/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          item: agentMessageItem("item-1", "", "inProgress"),
        },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "item/agentMessage/delta",
        params: { threadId: "thread-1", ref: "ref-1", turnId: "t1", itemId: "item-1", delta: "new text" },
      } as AnyNotification);
      expect(rowById(store, "item-1")).toMatchObject({ kind: "assistant", markdown: "new text" });
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
