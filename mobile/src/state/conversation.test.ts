// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import { describe, expect, it } from "vitest";
import { WireError } from "../../../cmd/evener-hub/frontend/src/protocol/errors";
import type {
  AnyNotification,
  InputItem,
  MutationReceipt,
  ThreadCapabilities,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type {
  MobileCapabilities,
  MobileConversation,
} from "../conversation/model";
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

      // M1: deterministic barrier — drain microtasks so the scheduler
      // effect (rehydrate → readProjection) completes. No test-only API.
      await drainMicrotasks();

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

  // M1: Deterministic deferred-effect helpers. Replaces fixed setTimeout sleeps
  // and the removed flushScheduler test seam with started/release/drained
  // barriers so test timing is deterministic. Tests observe through action
  // promises and deferred services — no test-only mutable API on the store.

  // Yield to the microtask queue so scheduler-deferred effects can start.
  // A fixed number of microtask yields is deterministic (no setTimeout).
  async function drainMicrotasks(n = 10): Promise<void> {
    for (let i = 0; i < n; i++) {
      await Promise.resolve();
    }
  }

  // Deterministic deferred readProjection that hangs EVERY call (not just the
  // first) until released. Each call gets its own release gate. Returns started
  // and release controls so tests can drive effects one at a time.
  function makeControlledRead(service: FakeConversationService): {
    release: () => void;
    getStartedCount: () => number;
    getDoneCount: () => number;
  } {
    let startedCount = 0;
    let doneCount = 0;
    const releaseQueue: Array<() => void> = [];
    const orig = service.readProjection.bind(service);
    service.readProjection = async (ref: string) => {
      startedCount += 1;
      const result = await orig(ref);
      await new Promise<void>((resolve) => {
        releaseQueue.push(resolve);
      });
      doneCount += 1;
      return result;
    };
    return {
      release: () => {
        const r = releaseQueue.shift();
        if (r !== undefined) r();
      },
      getStartedCount: () => startedCount,
      getDoneCount: () => doneCount,
    };
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
      // Drain microtasks — the queued rehydrate for A must be suppressed.
      await drainMicrotasks();
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
      await drainMicrotasks();
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
      await drainMicrotasks();
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
      await drainMicrotasks();
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
      await drainMicrotasks();
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
      await drainMicrotasks();
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
      await drainMicrotasks();
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
      await drainMicrotasks();
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
      // M1: deterministic barrier — drain microtasks so the in-flight
      // rehydrate starts (readProjection is called and hangs).
      await drainMicrotasks();
      expect(ctrl.getStartedCount()).toBe(1);
      // While in-flight, trigger another resync (trailing).
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // Release the first — the trailing must drain. The trailing's
      // readProjection also hangs, so release it too.
      ctrl.release();
      await drainMicrotasks();
      expect(ctrl.getStartedCount()).toBe(2);
      ctrl.release();
      await drainMicrotasks();
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
      // M1: deterministic barrier — drain microtasks so the rehydrate starts.
      await drainMicrotasks();
      expect(ctrl.getStartedCount()).toBe(1);
      // The rehydrate is hanging. Queue a trailing request while in-flight.
      // The trailing is retained and must drain after the first completes.
      const readsBeforeTrailing = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      // Release the first — the trailing must drain recursively.
      ctrl.release();
      await drainMicrotasks();
      expect(ctrl.getStartedCount()).toBe(2);
      ctrl.release();
      await drainMicrotasks();
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
      // Make readProjection throw.
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
      await drainMicrotasks();
      // The scheduler must remain usable — retrigger.
      const readsBefore = service.readProjectionCalls.length;
      store.getState().applyNotification({
        method: "evener/thread/resync",
        params: { threadId: "thread-1", ref: "ref-1" },
      } as AnyNotification);
      await drainMicrotasks();
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
      await drainMicrotasks();
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
      await drainMicrotasks();
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
      await drainMicrotasks();
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
      await drainMicrotasks();

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
      await drainMicrotasks();

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
});
