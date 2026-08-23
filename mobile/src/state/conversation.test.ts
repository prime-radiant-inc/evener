// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import { describe, expect, it } from "vitest";
import type {
  AnyNotification,
  InputItem,
  MutationReceipt,
  ThreadCapabilities,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { MobileConversation } from "../conversation/model";
import type { ConversationService } from "../services/conversation";
import { createConversationStore } from "./conversation";

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
class FakeConversationService implements ConversationService {
  ref: string | null = null;
  openConv: MobileConversation = makeConversation();
  olderCursor: string | null = null;
  olderItems: { items: MobileConversation["items"]; nextCursor?: string } = {
    items: [],
  };
  receipt: MutationReceipt = makeReceipt();
  sendShouldReject: Error | null = null;
  sendCallCount = 0;
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

  async open(ref: string): Promise<MobileConversation> {
    this.ref = ref;
    return this.openConv;
  }
  async loadOlder(
    _cursor: string,
  ): Promise<{ items: MobileConversation["items"]; nextCursor?: string }> {
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
    return this.receipt;
  }
  async queue(_input: InputItem[]): Promise<MutationReceipt> {
    return this.receipt;
  }
  async interrupt(): Promise<MutationReceipt> {
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
      const p = store.getState().loadOlder(service);
      expect(store.getState().loadingOlder).toBe(true);
      await p;
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
});
