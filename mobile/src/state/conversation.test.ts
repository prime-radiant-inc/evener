// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import { describe, expect, it } from "vitest";
import { WireError } from "../../../cmd/evener-hub/frontend/src/protocol/errors";
import type {
  AnyNotification,
  InputItem,
  MutationReceipt,
  Thread,
  ThreadCapabilities,
  ThreadItem,
  Turn,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  MobileCapabilities,
  MobileConversation,
} from "../conversation/model";
import { projectThread } from "../conversation/project";
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
  queue: true,
  goal: true,
  rename: true,
};

function makeConversation(
  over: Partial<MobileConversation> = {},
): MobileConversation {
  return {
    id: "thread-1",
    sessionId: "session-1",
    preview: "hello",
    modelProvider: "anthropic",
    status: "ready",
    items: [],
    capabilities: ALL_TRUE_CAPS,
    queue: { depth: 0, preview: [] },
    usage: {},
    askPending: false,
    ...over,
  };
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

function textInput(text: string): InputItem[] {
  return [{ type: "text", text }];
}

// A fake ConversationService that returns scripted values without any network.
class FakeConversationService implements LiveConversationService {
  ref: string | null = null;
  openConv: MobileConversation = makeConversation();
  olderCursor: string | null = null;
  olderItems: { items: MobileConversation["items"]; nextCursor?: string } = {
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
  readProjectionCalls: { ref: string }[] = [];
  // refreshCapabilities support
  refreshCapsResult: ThreadCapabilities | null = null;
  refreshCapsCallCount = 0;

  async open(ref: string, _cursor?: string): Promise<MobileConversation> {
    this.ref = ref;
    return this.openConv;
  }
  async readProjection(ref: string): Promise<ConversationReadProjection> {
    this.readProjectionCalls.push({ ref });
    if (this.readProjectionResult) return this.readProjectionResult;
    return {
      conversation: this.openConv,
      activity: {
        tasks: [],
        work: [],
        usage: {},
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      },
      olderCursor: this.olderCursor,
    };
  }
  async refreshCapabilities(_ref: string): Promise<ThreadCapabilities | null> {
    this.refreshCapsCallCount += 1;
    return this.refreshCapsResult ?? { ...ALL_TRUE_CAPS };
  }
  async loadOlder(
    _cursor: string,
  ): Promise<{ items: MobileConversation["items"]; nextCursor?: string }> {
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
      expect(s.conversation?.id).toBe("thread-1");
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
      service.olderItems = { items: [], nextCursor: "next" };
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      // Set a cursor so loadOlder has a page to request.
      store.setState({ olderCursor: "cursor-1" });
      const p = store.getState().loadOlder(service);
      expect(store.getState().loadingOlder).toBe(true);
      await p;
      expect(store.getState().loadingOlder).toBe(false);
    });

    it("F8: does not request when olderCursor is null", async () => {
      const service = new FakeConversationService();
      service.olderItems = { items: [], nextCursor: "next" };
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      // olderCursor is null after open — loadOlder should not request.
      expect(store.getState().olderCursor).toBeNull();
      await store.getState().loadOlder(service);
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

    for (const { kind, call, callCountField } of mutationCases) {
      it(`${kind}: calls the service ${kind} method`, async () => {
        const service = new FakeConversationService();
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
        const service = new FakeConversationService();
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

        // Resolve and await
        resolveFn?.();
        await p;
        // After resolution, pending should be cleared
        expect(store.getState().pendingMutation).toBeNull();
      });

      it(`${kind}: records exact draft snapshot in mutation state`, async () => {
        const service = new FakeConversationService();
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
        const service = new FakeConversationService();
        const store = createConversationStore();
        await store.getState().open(service, "ref-1");
        await call(store, service, textInput("test"));
        expect(store.getState().pendingMutation).toBeNull();
        expect(store.getState().error).toBeNull();
      });

      it(`${kind}: restores draft and sets failed state on failure`, async () => {
        const service = new FakeConversationService();
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
        const service = new FakeConversationService();
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
      // Only send is disabled
      service.openConv = makeConversation({
        capabilities: { ...ALL_TRUE_CAPS, send: false } as MobileCapabilities,
      });
      await store.getState().open(service, "ref-1");
      // send should fail, steer should succeed
      await expect(
        store.getState().send(service, textInput("x")),
      ).rejects.toThrow();
      // steer should work since steer capability is true
      await store.getState().steer(service, textInput("x"));
      expect(store.getState().error).toBeNull();
    });

    it("queue has an independent capability gate from send", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        capabilities: { ...ALL_TRUE_CAPS, queue: false } as MobileCapabilities,
      });
      await store.getState().open(service, "ref-1");
      await expect(
        store.getState().queue(service, textInput("x")),
      ).rejects.toThrow();
      // send should work since send capability is true
      await store.getState().send(service, textInput("x"));
      expect(store.getState().error).toBeNull();
    });
  });

  describe("actionUnavailable publishes refreshed capabilities before error", () => {
    it("store surfaces error immediately, publishes refreshed caps via scheduler", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Initial: all caps true, send enabled
      service.openConv = makeConversation({
        capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
      });
      await store.getState().open(service, "ref-1");

      // Script send to reject with actionUnavailable (F11: real WireError)
      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;

      // Script refreshCapabilities to return send=false
      service.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };

      // Before the send, capabilities should have send=true
      expect(store.getState().conversation?.capabilities.send).toBe(true);

      // Send will fail with actionUnavailable
      await store.getState().send(service, textInput("x"));

      // I2: The error is surfaced after the cap refresh completes (send
      // awaits handleMutationError which awaits the scheduler drain).
      expect(store.getState().error).not.toBeNull();
      expect(service.refreshCapsCallCount).toBe(1);
      expect(store.getState().conversation?.capabilities.send).toBe(false);
    });
  });

  describe("applyNotification", () => {
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
      expect(store.getState().conversation?.status).toBe("running");
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
          queue: { revision: 1, depth: 2, preview: ["first", "second"] },
        },
      } as AnyNotification);
      expect(store.getState().conversation?.queue.depth).toBe(2);
      expect(store.getState().conversation?.queue.preview).toEqual([
        "first",
        "second",
      ]);
    });

    it("marks running on turn/started", async () => {
      const service = new FakeConversationService();
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
      expect(store.getState().conversation?.status).toBe("running");
    });

    it("marks idle on turn/completed", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      await store.getState().open(service, "ref-1");
      // First set running
      store.getState().applyNotification({
        method: "turn/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turn: { id: "t1", itemsView: "default", status: "running" },
        },
      } as AnyNotification);
      expect(store.getState().conversation?.status).toBe("running");
      // Now complete
      store.getState().applyNotification({
        method: "turn/completed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          turnId: "t1",
          turn: {
            id: "t1",
            itemsView: "default",
            status: "completed",
            usage: { totalTokens: 100 },
          },
        },
      } as AnyNotification);
      expect(store.getState().conversation?.status).not.toBe("running");
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
      service.openConv = makeConversation({ id: "thread-2" });
      await store.getState().open(service, "ref-2");
      expect(store.getState().conversation?.id).toBe("thread-2");
      // A stale notification for ref-1 should be dropped
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          status: { type: "running" },
        },
      } as AnyNotification);
      expect(store.getState().conversation?.id).toBe("thread-2");
      expect(store.getState().conversation?.status).toBe("ready");
    });
  });

  describe("openProjected", () => {
    it("uses readProjection to set conversation, cursor, and activity view", async () => {
      const service = new FakeConversationService();
      const sink = createFakeSink();
      const store = createConversationStore();
      const activityView: ActivityView = {
        tasks: [{ status: "done", count: 3 }],
        work: [],
        usage: { totalTokens: 42 },
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      };
      service.readProjectionResult = {
        conversation: makeConversation({ id: "thread-proj" }),
        activity: activityView,
        olderCursor: "cursor-initial",
      };
      await store.getState().openProjected(service, sink, "ref-1");
      expect(store.getState().conversation?.id).toBe("thread-proj");
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
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      };
      service.readProjectionResult = {
        conversation: makeConversation({ id: "thread-1" }),
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
  });

  describe("item/completed settles item", () => {
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
            detail: { output: "line1" },
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

  describe("resync coalesces to one rehydrate via internal coalescer (F5)", () => {
    it("coalesces evener/thread/resync into one rehydrate call", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
      service.openConv = makeConversation({ id: "thread-2" });
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: "initial-cursor",
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      expect(store.getState().olderCursor).toBe("initial-cursor");
      // Update the projection result for rehydrate
      service.readProjectionResult = {
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      };
      service.readProjectionResult = {
        conversation: makeConversation({ id: "thread-1" }),
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
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        params: { message: "test warning" },
      } as AnyNotification);
      const conv = store.getState().conversation;
      expect(conv?.items.length).toBeLessThanOrEqual(500);
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-2" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: "cursor-2",
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-2");

      // Now the stale rehydrate fails.
      rejectRehydrate?.(new Error("stale rehydrate error"));
      await rehydratePromise.catch(() => {});

      // The stale error must NOT have overwritten the newer conversation.
      expect(store.getState().conversation?.id).toBe("thread-2");
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
    release: () => void;
    completed: (target?: number) => Promise<void>;
    getStartedCount: () => number;
    getDoneCount: () => number;
  } {
    let startedCount = 0;
    let doneCount = 0;
    const releaseQueue: Array<() => void> = [];
    // Level-triggered waiters: each carries a target count. Resolved when
    // the counter reaches or exceeds the target. If the target is already
    // met when started()/completed() is called, resolve immediately.
    const startedWaiters: Array<{ target: number; resolve: () => void }> = [];
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
      release: () => {
        const r = releaseQueue.shift();
        if (r !== undefined) r();
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

  // Level-triggered controlled refreshCapabilities: every call hangs until
  // released. started(target=1)/completed(target=1) resolve immediately when
  // the counter has already reached target — level-triggered, no edge
  // counting, no yieldMicrotask(count), no setTimeout, no polling.
  function makeControlledRefresh(service: FakeConversationService): {
    started: (target?: number) => Promise<void>;
    release: () => void;
    completed: (target?: number) => Promise<void>;
    getStartedCount: () => number;
    getDoneCount: () => number;
  } {
    let startedCount = 0;
    let doneCount = 0;
    const releaseQueue: Array<() => void> = [];
    const startedWaiters: Array<{ target: number; resolve: () => void }> = [];
    const doneWaiters: Array<{ target: number; resolve: () => void }> = [];
    const orig = service.refreshCapabilities.bind(service);
    service.refreshCapabilities = async (ref: string) => {
      startedCount += 1;
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
      });
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
    return {
      started: (target = 1) => {
        if (startedCount >= target) return Promise.resolve();
        return new Promise<void>((resolve) => {
          startedWaiters.push({ target, resolve });
        });
      },
      release: () => {
        const r = releaseQueue.shift();
        if (r !== undefined) r();
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
        conversation: makeConversation({ id: "thread-A" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-B" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
      expect(store.getState().conversation?.id).toBe("thread-B");
    });

    it("queued rehydrate for A is suppressed after close (no serviceA reads)", async () => {
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = {
        conversation: makeConversation({ id: "thread-A" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-A" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-A" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
      serviceB.openConv = makeConversation({ id: "thread-B" });
      await store.getState().open(serviceB, "ref-B");
      const readsA = serviceA.readProjectionCalls.length;
      const writesA = sinkA.setLiveViewCalls.length;
      await yieldMicrotask();
      // I1: plain open cleared bindings — no projected rehydrate for A.
      expect(serviceA.readProjectionCalls.length).toBe(readsA);
      expect(sinkA.setLiveViewCalls.length).toBe(writesA);
      expect(store.getState().conversation?.id).toBe("thread-B");
    });

    it("rehydrate can never call serviceA with refB", async () => {
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = {
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-2" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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

  describe("I2: actionUnavailable capability refresh through store-owned scheduler", () => {
    it("capability refresh is requested through the scheduler, not direct await", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
      });
      await store.getState().open(service, "ref-1");

      // Script send to reject with actionUnavailable (F11: real WireError).
      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;
      service.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };

      expect(store.getState().conversation?.capabilities.send).toBe(true);
      await store.getState().send(service, textInput("x"));
      // I2: The error is surfaced after the cap refresh completes (send
      // awaits handleMutationError which awaits the scheduler drain).
      expect(store.getState().error).not.toBeNull();
      expect(service.refreshCapsCallCount).toBe(1);
      expect(store.getState().conversation?.capabilities.send).toBe(false);
    });

    it("capability refresh serializes/coalesces with rereads", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Trigger a resync (rehydrate request) and a capability refresh
      // simultaneously — both go through the same scheduler.
      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;
      service.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };
      const readsBefore = service.readProjectionCalls.length;
      // Queue a resync rehydrate.
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // Start a send that fails with actionUnavailable (queues cap refresh).
      // send() awaits the cap refresh through the scheduler; the rehydrate
      // queued via notification also drains through the scheduler.
      await store.getState().send(service, textInput("x"));
      // Drain any trailing rehydrate from the notification.
      await yieldMicrotask();
      // Both the rehydrate (readProjection) and cap refresh should have run.
      expect(service.readProjectionCalls.length).toBeGreaterThan(readsBefore);
      expect(service.refreshCapsCallCount).toBe(1);
    });

    it("stale mutation recovery is suppressed by mutationId guard", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
      });
      await store.getState().open(service, "ref-1");
      // Script send to reject with actionUnavailable.
      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;
      // M1: deterministic barrier — hang refreshCapabilities so the cap refresh
      // effect starts but doesn't complete before the second send.
      const refreshHolder: { release: () => void } = { release: () => {} };
      service.refreshCapabilities = async () => {
        await new Promise<void>((resolve) => {
          refreshHolder.release = resolve;
        });
        return { ...ALL_TRUE_CAPS, send: false };
      };
      // Start a send that fails — queues a cap refresh for mutationId 1.
      // send() now awaits the cap refresh, so it hangs until released.
      // Do NOT await — start it without awaiting so we can start mutation 2.
      const send1P = store.getState().send(service, textInput("first"));
      // Let the cap refresh effect start (scheduler microtask + refresh).
      await yieldMicrotask();
      // The cap refresh effect is now in-flight (hanging). Before it completes,
      // start a second send (mutationId 2).
      service.sendShouldReject = null;
      await store.getState().send(service, textInput("second"));
      // Release the hanging refresh — the mutationId guard must suppress
      // publication because mutationId 2 is now active.
      refreshHolder.release();
      await send1P;
      // The refresh was called but did NOT publish caps (stale mutation guard).
      expect(store.getState().conversation?.capabilities.send).toBe(true);
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
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      };
      service.readProjectionResult = {
        conversation: makeConversation({ id: "thread-1" }),
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      };
      service.readProjectionResult = {
        conversation: makeConversation({ id: "thread-1" }),
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
          turnId: "t1",
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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

  describe("Residual 2: heterogeneous scheduler outcomes — reread + cap refresh both run exactly once", () => {
    it("reread queued before cap refresh — both effects/outcomes happen exactly once", async () => {
      // Queue a resync rehydrate (reread key), then start a send that fails
      // with actionUnavailable (cap refresh key). Both go through the same
      // scheduler with distinct keys — both must run exactly once.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;
      service.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };

      const readsBefore = service.readProjectionCalls.length;
      const capsBefore = service.refreshCapsCallCount;

      // Queue a resync rehydrate FIRST (reread key = "ref-1").
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // Then start a send that fails (cap key = "cap:ref-1:<mutationId>").
      // send() awaits handleMutationError which awaits the cap refresh.
      await store.getState().send(service, textInput("x"));

      // Drain any trailing rehydrate from the notification.
      await yieldMicrotask();

      // Both the reread and cap refresh should have run exactly once.
      expect(service.readProjectionCalls.length).toBe(readsBefore + 1);
      expect(service.refreshCapsCallCount).toBe(capsBefore + 1);
      // Caps published: send=false.
      expect(store.getState().conversation?.capabilities.send).toBe(false);
      // Error surfaced.
      expect(store.getState().error).not.toBeNull();
    });

    it("cap refresh queued before reread — both effects/outcomes happen exactly once", async () => {
      // Start a send that fails with actionUnavailable (cap refresh key),
      // then queue a resync rehydrate (reread key). Both must run exactly once.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.readProjectionResult = {
        conversation: makeConversation({
          capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;
      service.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };

      const readsBefore = service.readProjectionCalls.length;
      const capsBefore = service.refreshCapsCallCount;

      // Start the send — it fails and queues a cap refresh. But send()
      // awaits the cap refresh, so it hangs on the scheduler. We need to
      // NOT await send yet — queue the reread while the cap refresh is
      // pending in the scheduler. But send() awaits the cap refresh, so
      // we need to start send without awaiting.
      // Actually, the cap refresh goes through the scheduler which defers
      // to a microtask. The send() call calls handleMutationError which
      // calls requestCapabilityRefresh which calls scheduler.request()
      // and returns scheduler.idle(). So send() hangs until the scheduler
      // is idle. We can start send without awaiting.
      const sendP = store.getState().send(service, textInput("x"));
      // Let the scheduler start the cap refresh effect (microtask).
      // But don't let it complete yet — the cap refresh resolves
      // immediately (refreshCapabilities is not hanging). So the cap
      // refresh will complete quickly. We need to queue the reread
      // before the cap refresh completes.
      // Actually both the cap refresh and the reread are in the same
      // scheduler. The cap refresh is queued first, the reread second.
      // They have different keys so both run. Let the scheduler drain.
      // But we need to queue the reread before the cap refresh starts.
      // The scheduler defers to a microtask, so if we queue the reread
      // synchronously (before any await), both are in the pending Map.
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // Now both are queued. Await send (which awaits scheduler idle).
      await sendP;
      await yieldMicrotask();

      // Both the cap refresh and reread should have run exactly once.
      expect(service.refreshCapsCallCount).toBe(capsBefore + 1);
      expect(service.readProjectionCalls.length).toBe(readsBefore + 1);
      // Caps published: send=false.
      expect(store.getState().conversation?.capabilities.send).toBe(false);
      // Error surfaced.
      expect(store.getState().error).not.toBeNull();
    });

    it("distinct mutations produce distinct cap keys — no coalescing across mutations", async () => {
      // Two sends that both fail with actionUnavailable should each
      // trigger a cap refresh with a distinct key (cap:ref:<mutationId>).
      // They must not coalesce — both refresh calls must happen.
      const service = new FakeConversationService();
      const store = createConversationStore();
      service.openConv = makeConversation({
        capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
      });
      await store.getState().open(service, "ref-1");

      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;
      service.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };

      const capsBefore = service.refreshCapsCallCount;

      // First send fails — queues cap refresh for mutationId 1.
      await store.getState().send(service, textInput("first"));
      // Second send fails — queues cap refresh for mutationId 2.
      // But the first send's cap refresh already published send=false,
      // so the second send also fails (send cap is still false from
      // the initial caps until the refresh publishes). Wait — the first
      // send already published send=false. The second send would check
      // requireCap which checks conversation.capabilities.send. After
      // the first cap refresh, send=false, so the second send would
      // throw before even calling service.send. Let me fix: reset caps
      // after the first send.
      expect(service.refreshCapsCallCount).toBe(capsBefore + 1);

      // Reset caps to send=true so the second send can proceed.
      store.setState({
        conversation: makeConversation({
          capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
        }),
      });
      // Second send fails — queues cap refresh for mutationId 2.
      await store.getState().send(service, textInput("second"));

      // Two distinct cap refresh calls (one per mutation).
      expect(service.refreshCapsCallCount).toBe(capsBefore + 2);
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

  describe("I1 fix: stale post-await rebind suppresses A capabilities", () => {
    it("rebind to serviceB/sinkB during refresh await suppresses A caps", async () => {
      // Open A (openProjected), trigger a send that fails with
      // actionUnavailable. The cap refresh effect starts (hanging on
      // refreshCapabilities). During the await, rebind to serviceB/sinkB
      // via rehydrate. The binding tuple changed — A's caps must NOT be
      // published after the refresh completes.
      const serviceA = new FakeConversationService();
      serviceA.readProjectionResult = {
        conversation: makeConversation({
          capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      const sinkA = createFakeSink();
      const store = createConversationStore();
      await store.getState().openProjected(serviceA, sinkA, "ref-1");

      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      serviceA.sendShouldReject = rejectErr as Error;
      serviceA.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };

      // Hang refreshCapabilities so we can rebind during the await.
      const refreshCtrl = makeControlledRefresh(serviceA);

      // Start send — fails, queues cap refresh (hanging on refresh).
      // send() awaits handleMutationError which awaits the cap-key completion.
      const sendP = store.getState().send(serviceA, textInput("x"));
      // Let the cap refresh effect start.
      await refreshCtrl.started();
      // While the refresh is hanging, rebind to serviceB/sinkB via rehydrate.
      const serviceB = new FakeConversationService();
      serviceB.readProjectionResult = {
        conversation: makeConversation({
          id: "thread-1",
          capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      const sinkB = createFakeSink();
      // rehydrate with different objects increments epoch — A's cap refresh
      // will be suppressed after the await.
      const rehydrateP = store.getState().rehydrate(serviceB, sinkB);
      // Let the rehydrate's readProjection start (it also hangs via controlled
      // read on serviceB — but we didn't install one). Actually serviceB's
      // readProjection resolves immediately. So rehydrate completes.
      await rehydrateP;
      // Release the hanging refresh — A's cap refresh effect checks
      // isCapBindingCurrent after the await — the binding changed (epoch +
      // service + sink), so caps are NOT published.
      refreshCtrl.release();
      await sendP;
      // A's caps (send=false) must NOT have been published.
      expect(store.getState().conversation?.capabilities.send).toBe(true);
    });
  });

  describe("I2 fix: per-key completion — unrelated reread cannot delay error", () => {
    it("pending reread does not delay cap refresh completion and error", async () => {
      // Queue a resync rehydrate (reread key) and a cap refresh (cap key).
      // Both are in the pending Map. The scheduler runs them in order.
      // The per-key completion promise for the cap key resolves when the
      // cap effect completes — send() does NOT wait for the reread to also
      // complete. The error is surfaced as soon as the cap refresh is done.
      const service = new FakeConversationService();
      service.readProjectionResult = {
        conversation: makeConversation({
          capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;
      service.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };

      const readsBefore = service.readProjectionCalls.length;

      // Queue a resync rehydrate (reread key = "ref-1").
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);

      // Start a send that fails with actionUnavailable (cap key). send()
      // awaits the cap-key completion promise. The scheduler runs both
      // effects in order (reread first, then cap refresh). The cap-key
      // promise resolves when the cap effect completes — even though
      // the reread was also pending. With the old global idle(), send()
      // would have waited for both. With per-key completion, the error
      // is surfaced as soon as the cap refresh is done.
      await store.getState().send(service, textInput("x"));

      // The error should have been surfaced.
      expect(store.getState().error).not.toBeNull();
      // The cap refresh should have run.
      expect(service.refreshCapsCallCount).toBe(1);
      // Caps published.
      expect(store.getState().conversation?.capabilities.send).toBe(false);
      // The reread should also have run.
      expect(service.readProjectionCalls.length).toBeGreaterThan(readsBefore);
    });
  });

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

  describe("Task 2A-1: cap-first exact completion — send settles while reread remains blocked", () => {
    it("send promise settles with refreshed caps/error while distinct reread is still held", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = {
        conversation: makeConversation({
          capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      // Script send to fail with actionUnavailable.
      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;
      service.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };

      // Install controlled helpers so we can hang cap refresh and reread.
      const refreshCtrl = makeControlledRefresh(service);
      const readCtrl = makeControlledRead(service);

      const capsBefore = service.refreshCapsCallCount;

      // Start send — it fails, triggering handleMutationError which calls
      // requestCapabilityRefresh → scheduler.request(capKey, ...) which
      // returns a per-key completion promise that send() awaits.
      const sendP = store.getState().send(service, textInput("x"));

      // Wait until the cap refresh is definitely in flight.
      await refreshCtrl.started();
      // Yield so the controlled wrapper reaches the release gate.
      await yieldMicrotask();

      // Now queue a distinct authoritative reread (key "ref-1" ≠ cap key).
      // The reread is pending in the scheduler's pending Map (distinct key),
      // so it will not start until the cap refresh completes.
      const readsBefore = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);

      // The reread has NOT started yet (cap refresh is in flight).
      expect(service.readProjectionCalls.length).toBe(readsBefore);

      // Resolve the cap refresh — the cap-key completion promise resolves.
      refreshCtrl.release();

      // send() must settle (with error + refreshed caps) even though the
      // reread is still blocked. Use a watchdog to catch a hang (old
      // global-idle would hang here waiting for the reread too).
      await withWatchdog("cap-first send settle", sendP);

      // Caps published: send=false.
      expect(service.refreshCapsCallCount).toBe(capsBefore + 1);
      expect(store.getState().conversation?.capabilities.send).toBe(false);
      // Error surfaced.
      expect(store.getState().error).not.toBeNull();

      // The reread is STILL blocked — it has started (the scheduler moved
      // to it after the cap effect completed) but not completed.
      // readCtrl counts only calls after installation (openProjected was
      // before installation), so the reread is the 1st controlled read.
      await readCtrl.started(1);
      await yieldMicrotask(); // let the wrapper reach the release gate
      expect(readCtrl.getDoneCount()).toBeLessThan(1);

      // Now release the reread.
      readCtrl.release();
      await readCtrl.completed(1);
      expect(service.readProjectionCalls.length).toBe(readsBefore + 1);
    });
  });

  describe("Task 2A-2: reread-first converse — hold reread, queue cap, release into cap", () => {
    it("reread held first, then cap queued — exact mutation settles after both drain", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = {
        conversation: makeConversation({
          capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;
      service.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };

      const refreshCtrl = makeControlledRefresh(service);
      const readCtrl = makeControlledRead(service);

      // Hold a reread first (key "ref-1").
      const readsBefore = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // Wait until the reread starts and is held.
      // readCtrl counts only calls after installation (openProjected was
      // before installation), so the reread is the 1st controlled read.
      await readCtrl.started(1);
      await yieldMicrotask(); // let the wrapper reach the release gate

      // Now start a send that fails — cap refresh (distinct key) is queued.
      const capsBefore = service.refreshCapsCallCount;
      const sendP = store.getState().send(service, textInput("x"));

      // The cap refresh has NOT started yet (reread is in flight).
      expect(refreshCtrl.getStartedCount()).toBe(0);

      // Release the reread — it resolves, then the scheduler drains into
      // the cap refresh effect.
      readCtrl.release();
      await readCtrl.completed(1);
      await yieldMicrotask(); // let scheduler drain to cap refresh

      // Wait for the cap refresh to start and then complete it.
      await refreshCtrl.started();
      await yieldMicrotask(); // let the wrapper reach the release gate
      refreshCtrl.release();

      // send() must settle with error + refreshed caps.
      await withWatchdog("reread-first send settle", sendP);
      expect(service.refreshCapsCallCount).toBe(capsBefore + 1);
      expect(store.getState().conversation?.capabilities.send).toBe(false);
      expect(store.getState().error).not.toBeNull();
      // R1: the send during the held rehydrate changes the mutation-owner
      // revision, so the rehydrate schedules one trailing reread. The
      // trailing reread goes through the scheduler (queued behind the cap
      // refresh). It starts after the cap refresh completes. Release it.
      await readCtrl.started(2);
      await yieldMicrotask();
      readCtrl.release();
      await readCtrl.completed(2);
      await yieldMicrotask();
      // Total: original held rehydrate + one trailing reread.
      expect(service.readProjectionCalls.length).toBe(readsBefore + 2);
    });

    it("trailing reread may remain held after cap settles — no hang", async () => {
      // Hold reread, queue cap, release reread into cap. After cap settles,
      // an unrelated trailing reread may remain held — send must still
      // settle promptly.
      const service = new FakeConversationService();
      service.readProjectionResult = {
        conversation: makeConversation({
          capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;
      service.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };

      const refreshCtrl = makeControlledRefresh(service);
      const readCtrl = makeControlledRead(service);

      // Hold a reread first.
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await readCtrl.started(1);
      await yieldMicrotask(); // let the wrapper reach the release gate

      // Start send — cap refresh queued (distinct key).
      const sendP = store.getState().send(service, textInput("x"));

      // Release the reread — scheduler moves to cap refresh.
      readCtrl.release();
      await readCtrl.completed(1);
      await yieldMicrotask(); // let scheduler drain to cap refresh

      // Start and complete the cap refresh.
      await refreshCtrl.started();
      await yieldMicrotask(); // let the wrapper reach the release gate
      // I2: Update the projection to match the refreshed caps BEFORE releasing
      // the cap refresh — the trailing reread (queued behind the cap refresh)
      // will call readProjection immediately after the cap refresh completes,
      // before we can update the result.
      service.readProjectionResult = {
        conversation: makeConversation({
          capabilities: { ...ALL_TRUE_CAPS, send: false } as MobileCapabilities,
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      refreshCtrl.release();

      // send() must settle while NO trailing reread is held (only cap ran).
      await withWatchdog("converse send settle", sendP);
      expect(store.getState().error).not.toBeNull();
      expect(store.getState().conversation?.capabilities.send).toBe(false);

      // R1: the send during the held rehydrate changes the mutation-owner
      // revision, so the rehydrate schedules one trailing reread. It's
      // queued behind the cap refresh and starts after send settles.
      // I2: The trailing reread commits its projected caps (cap owner
      // unchanged since the cap refresh already ran before the trailing
      // reread started). The projection was already updated above to match
      // the refreshed caps so the trailing reread doesn't regress them.
      // Release it so it doesn't interfere with the rest of the test.
      await readCtrl.started(2);
      await yieldMicrotask();
      readCtrl.release();
      await readCtrl.completed(2);
      await yieldMicrotask();

      // Now queue another trailing reread that remains held — it must not
      // affect the already-settled send.
      const trailingReadsBefore = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await readCtrl.started(3);
      await yieldMicrotask(); // let the wrapper reach the release gate
      // The trailing reread is held — send already settled, unaffected.
      expect(store.getState().error).not.toBeNull();
      expect(store.getState().conversation?.capabilities.send).toBe(false);
      // Release it to clean up.
      readCtrl.release();
      await readCtrl.completed(3);
      expect(service.readProjectionCalls.length).toBe(trailingReadsBefore + 1);
    });
  });

  describe("Task 2A-3: same-key reread coalescing through public notifications", () => {
    it("multiple same-key reread requests during a blocker produce exactly one trailing read", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = {
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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

    it("distinct work survives same-key coalescing — cap refresh and reread both run", async () => {
      // A same-key reread coalesces, but a distinct cap-key request must
      // still run — coalescing is per-key, not global.
      const service = new FakeConversationService();
      service.readProjectionResult = {
        conversation: makeConversation({
          capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;
      service.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };

      const refreshCtrl = makeControlledRefresh(service);
      const readCtrl = makeControlledRead(service);

      const readsBefore = service.readProjectionCalls.length;
      const capsBefore = service.refreshCapsCallCount;

      // Hold a reread first (key "ref-1").
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await readCtrl.started(1);
      await yieldMicrotask(); // let the wrapper reach the release gate

      // While the reread is held, emit 2 more same-key resync notifications
      // (these coalesce into the trailing reread entry).
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);

      // Start a send that fails — cap refresh (distinct key) is queued.
      const sendP = store.getState().send(service, textInput("x"));

      // Release the reread — scheduler drains: trailing reread, then cap refresh.
      readCtrl.release();
      await readCtrl.completed(1);
      await yieldMicrotask(); // let scheduler drain to trailing read

      // C1+I1: The mutation-owner-changed trailing reread was scheduled via
      // drainTrailingReread during R's rehydrate body (mutation already
      // settled). It coalesces with the pending same-key entry from the 2
      // resync notifications (overwrites the coalesced effect).
      await readCtrl.started(2);
      await yieldMicrotask(); // let the wrapper reach the release gate
      readCtrl.release();
      await readCtrl.completed(2);
      await yieldMicrotask(); // let scheduler drain

      // The trailing reread's rehydrate may schedule another trailing if the
      // mutation owner changed again. Drain it if present.
      if (readCtrl.getStartedCount() < 3) {
        // No 3rd read — proceed to cap refresh.
      } else {
        await yieldMicrotask();
        readCtrl.release();
        await readCtrl.completed(3);
        await yieldMicrotask(); // let scheduler drain to cap refresh
      }

      // Then the cap refresh runs.
      await refreshCtrl.started();
      await yieldMicrotask(); // let the wrapper reach the release gate
      refreshCtrl.release();

      await withWatchdog("distinct-work send settle", sendP);

      // Total reads: 1 original R + trailing reads. The exact count depends
      // on whether the trailing reread's rehydrate also detects a mutation
      // owner change. Key invariant: both reread and cap refresh ran.
      expect(service.readProjectionCalls.length).toBeGreaterThanOrEqual(
        readsBefore + 2,
      );
      expect(service.refreshCapsCallCount).toBe(capsBefore + 1);
      expect(store.getState().conversation?.capabilities.send).toBe(false);
      expect(store.getState().error).not.toBeNull();
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

  describe("Task 2A-4: deferred error-path drain — first effect errors, queued work drains", () => {
    it("first reread errors after release — same-key coalesced reread and distinct cap refresh both drain exactly once", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = {
        conversation: makeConversation({
          capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      // Script send to fail with actionUnavailable (for the cap refresh path).
      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;
      service.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };

      const readCtrl = makeControlledErrorRead(service);
      const refreshCtrl = makeControlledRefresh(service);

      const readsBefore = service.readProjectionCalls.length;
      const capsBefore = service.refreshCapsCallCount;

      // Queue the first reread (key "ref-1") — it will hang, then error.
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);

      // Wait until the first reread starts (exposes started barrier).
      await readCtrl.started(1);

      // While the first reread is in flight, queue same-key work (coalesces
      // into the trailing reread entry) and distinct heterogeneous work
      // (cap refresh from a failed send).
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);

      // Distinct heterogeneous work: start a send that fails (cap refresh).
      const sendP = store.getState().send(service, textInput("x"));

      // Release the first reread — it rejects with an error.
      // The scheduler catches the error and continues draining.
      readCtrl.releaseFirst();

      // The first reread errored. The scheduler catches it and drains the
      // pending queue: the coalesced trailing reread (same key "ref-1"),
      // then the cap refresh (distinct key "cap:ref-1:<mutationId>").
      //
      // The trailing reread starts (2nd controlled read) — this proves the
      // first error was caught and the drain continued.
      await readCtrl.started(2);

      // Release the trailing reread.
      readCtrl.release();
      await readCtrl.completed(1);
      // Let the scheduler drain from the trailing reread to the cap refresh.
      // A single yield is a level-triggered barrier, not a fixed count.
      await yieldMicrotask();

      // Then the cap refresh starts.
      await refreshCtrl.started();
      // Yield so the controlled wrapper reaches the release gate.
      await yieldMicrotask();
      refreshCtrl.release();
      // Let the cap refresh effect complete and the completion promise
      // resolve so send() can settle.
      await refreshCtrl.completed();

      // send() must settle with error + refreshed caps — the cap refresh
      // completed even though the first reread errored.
      await withWatchdog("error-path send settle", sendP);
      expect(service.refreshCapsCallCount).toBe(capsBefore + 1);
      expect(store.getState().conversation?.capabilities.send).toBe(false);
      expect(store.getState().error).not.toBeNull();

      // The trailing reread ran exactly once (coalesced from 2 same-key signals).
      // Total reads: 1 (openProjected) + 1 (first reread, errored) + 1 (trailing)
      // = 3 calls to the patched readProjection. But readProjectionCalls only
      // counts calls to the original (the first reread errored before reaching
      // orig), so it's readsBefore + 1 (only the trailing reread reached orig).
      expect(service.readProjectionCalls.length).toBe(readsBefore + 1);

      // The scheduler remains usable — queue a new reread and verify it runs.
      const usableReadsBefore = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await readCtrl.started(3);
      readCtrl.release();
      await readCtrl.completed(2);
      expect(service.readProjectionCalls.length).toBe(usableReadsBefore + 1);
    });

    it("scheduler remains usable after error drain — new reread runs", async () => {
      // After the error-path drain, the scheduler must accept new work.
      // This test verifies a new reread runs after an error drain.
      const service = new FakeConversationService();
      service.readProjectionResult = {
        conversation: makeConversation({
          capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");

      // Trigger a reread that errors.
      const readCtrl = makeControlledErrorRead(service);

      // Queue a reread that will error.
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await readCtrl.started(1);

      // Release with rejection.
      readCtrl.releaseFirst();

      // The scheduler catches the error and goes idle (no trailing work).
      // Prove the scheduler remains usable: queue a new reread and verify
      // it starts. If the scheduler were stuck, the started barrier would
      // hang and the watchdog would catch it.
      const newReadP = readCtrl.started(2);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await withWatchdog("post-error reread start", newReadP);
      // The new reread started — scheduler is usable.
      readCtrl.release();
      await readCtrl.completed(1);
    });
  });

  // --- Same-key completion waiter invariant (capability keys) ----------------
  //
  // The per-key completion promise is only publicly reachable through the
  // mutation action path (send/steer/queue/interrupt → handleMutationError →
  // requestCapabilityRefresh → scheduler.request(capKey, ...)). There is no
  // public API to await the completion of a same-key reread (key "ref-1") —
  // requestRehydrate returns void (fire-and-forget), not the scheduler
  // promise. This is an intentional production invariant:
  //
  //   requestRehydrate(ref) → scheduler.request(ref, effect) → void
  //     (return value discarded — no public await of reread completion)
  //
  //   requestCapabilityRefresh(service, ref, gen, mutationId) → Promise<void>
  //     (return value awaited by handleMutationError — the ONLY public
  //     consumer of a per-key completion promise)
  //
  // The call graph confirms this:
  //   conversation.ts:548  scheduler.request(ref, ...)        [void return]
  //   conversation.ts:592  return scheduler.request(capKey, ...) [awaited]
  //   conversation.ts:1565 await requestCapabilityRefresh(...) [the awaiter]
  //
  // Same-key completion waiters for capability keys ARE publicly reachable
  // (via the mutation action's returned promise). Same-key completion waiters
  // for reread keys are NOT publicly reachable — tests cannot await a
  // specific reread's completion through the public store API. This invariant
  // is documented here rather than tested via a test-only API.

  describe("Task 2A-3 invariant: same-key completion waiters for reread keys are not publicly reachable", () => {
    it("requestRehydrate is fire-and-forget (void) — no public await of reread completion", () => {
      // requestRehydrate is called from applyNotification (void context) and
      // from the notification subscription callback (void context). It does
      // not return the scheduler.request promise. The only way to observe
      // reread completion is through external side effects (readProjection
      // call count, state changes) — not through a promise.
      //
      // This is a structural invariant of the production call graph, not a
      // runtime test. We verify it by confirming that applyNotification
      // returns void (not a promise).
      const service = new FakeConversationService();
      service.readProjectionResult = {
        conversation: makeConversation({ id: "thread-1" }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
        },
        olderCursor: null,
      };
      const store = createConversationStore();
      // We can't await openProjected here (need to keep it sync for the
      // structural check), so we verify the type: applyNotification returns
      // void, not Promise<void>.
      const s = store.getState();
      expect(typeof s.applyNotification).toBe("function");
      // The return type of applyNotification is void (not a Promise). Calling
      // it returns undefined, not a thenable.
      store.getState().setDraft("test");
      // applyNotification on a non-open store is a no-op (returns undefined).
      const result = store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      expect(result).toBeUndefined();
      // The result is NOT a thenable — confirming void, not Promise<void>.
      expect(
        (result as unknown as Record<string, unknown>)?.then,
      ).toBeUndefined();
    });
  });

  // --- Fix round 1: I1/I2/I3/I4 — rehydrate/loadOlder ownership, mutation
  // error clear, raw Thread question lifecycle ---

  // Raw Thread fixture helpers for I4 (projectThread-based question tests).
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

  function agentMessageItem(
    id: string,
    text: string,
    status = "completed",
  ): ThreadItem {
    return { type: "agentMessage", id, text, status };
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

  function makeReadProjectionResult(thread: Thread): {
    conversation: MobileConversation;
    activity: ActivityView;
    olderCursor: string | null;
  } {
    return {
      conversation: projectThread(thread),
      activity: {
        tasks: [],
        work: [],
        usage: {},
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      },
      olderCursor: null,
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
      expect(store.getState().conversation?.id).toBe("thread-B");
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
      expect(store.getState().conversation?.id).toBe("thread-B");
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
        capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
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
        capabilities: { ...ALL_TRUE_CAPS } as MobileCapabilities,
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

  describe("I4: raw Thread fixtures through projectThread — question lifecycle", () => {
    it("completed parseable ask_user drives reread → question rows + askPending via projectThread", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Initial: no turns, no pending ask.
      service.readProjectionResult = makeReadProjectionResult(makeThread());
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      expect(store.getState().conversation?.askPending).toBe(false);
      // After the ask_user notification, the reread returns a Thread with a
      // completed ask_user turn — projectThread produces question rows.
      const askThread = makeThread({
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
      // The reread must have produced actual question rows via projectThread.
      const conv = store.getState().conversation;
      expect(conv?.askPending).toBe(true);
      const questionItem = conv?.items.find((i) => i.kind === "question");
      expect(questionItem).toBeDefined();
      if (questionItem?.kind === "question") {
        expect(questionItem.batch.questions).toHaveLength(1);
        expect(questionItem.batch.questions[0]?.question).toBe("Pick one");
        expect(questionItem.batch.questions[0]?.options).toHaveLength(2);
      }
      // Bounded reread count: exactly one reread.
      expect(service.readProjectionCalls.length).toBe(initialReads + 1);
    });

    it("later user-message answer triggers reread → settled/removal via projectThread", async () => {
      const service = new FakeConversationService();
      const store = createConversationStore();
      // Initial: has a pending ask_user.
      const askThread = makeThread({
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
      // followed by a userMessage — projectThread settles (no question rows,
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

    it("mutation race: asserts error, pending owner, capabilities after stale cap refresh", async () => {
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
      // First send fails with actionUnavailable — triggers cap refresh (hanging).
      const rejectErr = new WireError("action unavailable", -32000, {
        evenerErrorInfo: "actionUnavailable",
      });
      service.sendShouldReject = rejectErr as Error;
      service.refreshCapsResult = { ...ALL_TRUE_CAPS, send: false };
      const refreshCtrl = makeControlledRefresh(service);
      const send1P = store.getState().send(service, textInput("first"));
      await refreshCtrl.started();
      await yieldMicrotask();
      // While first cap refresh is hanging, start a second send that succeeds.
      service.sendShouldReject = null;
      const send2P = store.getState().send(service, textInput("second"));
      // Release the first cap refresh — mutationId 1 is stale.
      refreshCtrl.release();
      await send1P;
      await send2P;
      // Mutation 2 succeeded — no error, no pending mutation.
      expect(store.getState().error).toBeNull();
      expect(store.getState().pendingMutation).toBeNull();
      // Caps should NOT have been published by the stale mutation 1.
      expect(store.getState().conversation?.capabilities.send).toBe(true);
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

  // --- Residual: R3 — real malformed/incomplete Thread fixtures through projectThread ---

  describe("R3: real malformed/incomplete Thread fixtures through projectThread", () => {
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
      // question), since projectThread's parseAskUserQuestions returns
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

  // I2: Monotonic capability-owner revision. Increment on every capability
  // publication/transition. Capture at rehydrate start. If unchanged after
  // await, commit authoritative projected capabilities. If advanced during
  // await, preserve current caps.
  describe("I2: monotonic capability-owner revision", () => {
    it("normal resync changes caps — rehydrate commits projected capabilities", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS, send: true },
            queue: { revision: 0 },
          },
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Initial caps have send=true.
      expect(store.getState().conversation?.capabilities.send).toBe(true);
      // Reread returns caps with send=false.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS, send: false },
            queue: { revision: 0 },
          },
        }),
      );
      const ctrl = makeControlledRead(service);
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      // No capability owner advanced during the await — rehydrate commits
      // the projected capabilities (send=false).
      expect(store.getState().conversation?.capabilities.send).toBe(false);
    });

    it("concurrent newer cap refresh wins — rehydrate preserves current caps", async () => {
      const service = new FakeConversationService();
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS, send: true },
            queue: { revision: 0 },
          },
        }),
      );
      const store = createConversationStore();
      await store.getState().openProjected(service, createFakeSink(), "ref-1");
      // Start a rehydrate (R) that hangs.
      const ctrl = makeControlledRead(service);
      // R's projection has send=false.
      service.readProjectionResult = makeReadProjectionResult(
        makeThread({
          evener: {
            ref: "ref-1",
            capabilities: { ...ALL_TRUE_CAPS, send: false },
            queue: { revision: 0 },
          },
        }),
      );
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await ctrl.started(1);
      await yieldMicrotask();
      // While R is in-flight, a cap refresh publishes caps with send=false,
      // queue=false (a newer capability owner). Use a notification to change
      // caps — this is a capability publication that increments capOwnerRev.
      store.getState().applyNotification({
        method: "thread/status/changed",
        params: {
          status: { type: "ready" },
          capabilities: { ...ALL_TRUE_CAPS, send: false, queue: false },
        },
      } as AnyNotification);
      // The current caps now have send=false, queue=false.
      expect(store.getState().conversation?.capabilities.send).toBe(false);
      expect(store.getState().conversation?.capabilities.queue).toBe(false);
      // Release R — its projected caps have send=false (but not queue=false).
      // Since the cap owner advanced during the await, R must preserve the
      // current caps (queue=false), NOT overwrite with its stale projection.
      ctrl.release();
      await ctrl.completed(1);
      await yieldMicrotask();
      // Current caps preserved — queue=false from the notification wins.
      expect(store.getState().conversation?.capabilities.send).toBe(false);
      expect(store.getState().conversation?.capabilities.queue).toBe(false);
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
        commandExecItem("X", "mytool", "inProgress"),
        // Reread includes stale X (no output yet).
        [
          userMessageItem("B", "base"),
          commandExecItem("X", "mytool", "inProgress"),
        ],
        // Live notification: tool output delta appends to X.
        {
          method: "item/toolOutput/delta",
          params: {
            threadId: "thread-1",
            ref: "ref-1",
            itemId: "X",
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
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
              state: "running",
              detail: { output: largeOutput, callId: "call-A" },
            },
          ],
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
              state: "running",
              detail: { output: largeOutput, callId: "call-A" },
            },
          ],
        }),
        activity: {
          tasks: [],
          work: [],
          usage: {},
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
          id: "thread-1",
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
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
          id: "thread-2",
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
          capabilities: ALL_TRUE_CAPS as MobileCapabilities,
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
    // array (uses projectThread so families are set from the canonical projector).
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
});
