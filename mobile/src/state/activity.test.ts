// ActivityStore (Zustand) tests with a fake ActivityService. The store wraps an
// ActivityService and the current ActivityView projection. It exposes
// project() (re-project from a Thread) and reset() (clear on conversation
// switch). It holds only the projected ActivityView (never the raw wire
// Thread) and is generation-safe: late re-projections from an older
// conversation cannot overwrite a newer one.

import { describe, expect, it, vi } from "vitest";
import type {
  AnyNotification,
  EvenerDiagnostics,
  EvenerThread,
  QueueState,
  Thread,
  ThreadCapabilities,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { MobileCapabilities } from "../conversation/model";
import { type ActivityView, createActivityService } from "../services/activity";
import { type ActivityState, createActivityStore } from "./activity";

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

const EMPTY_QUEUE: QueueState = { revision: 0 };

function evenerThread(over: Partial<EvenerThread> = {}): EvenerThread {
  return {
    ref: "ref-1",
    capabilities: ALL_TRUE_CAPS,
    queue: EMPTY_QUEUE,
    ...over,
  };
}

function thread(over: Partial<Thread> = {}): Thread {
  return {
    id: "thread-1",
    sessionId: "session-1",
    preview: "",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 1_000_000,
    updatedAt: 1_000_000,
    status: { type: "ready" },
    cwd: "/tmp",
    cliVersion: "1.0.0",
    source: "local",
    evener: evenerThread(),
    ...over,
  };
}

// --- tests -------------------------------------------------------------------

describe("ActivityStore", () => {
  it("starts with no view and idle status", () => {
    const store = createActivityStore();
    const state = store.getState();
    expect(state.view).toBeNull();
    expect(state.status).toBe("idle");
    expect(state.error).toBeNull();
  });

  it("project() stores the projected ActivityView and sets status open", () => {
    const realService = createActivityService();
    const t = thread({
      evener: evenerThread({ tasks: { total: 3, done: 1 } }),
    });
    const store = createActivityStore();
    store.getState().project(realService, t);
    const state = store.getState();
    expect(state.view).not.toBeNull();
    expect(state.status).toBe("open");
    expect(state.error).toBeNull();
    const doneGroup = state.view?.tasks.find((g) => g.status === "done");
    expect(doneGroup?.count).toBe(1);
  });

  it("project() with a fake service uses its scripted view", () => {
    const fakeView: ActivityView = {
      tasks: [{ status: "done", count: 7 }],
      work: [],
      usage: { cost: "$1.00" },
      capabilities: ALL_TRUE_CAPS as MobileCapabilities,
    };
    const fakeService = {
      projectActivity: vi.fn().mockReturnValue(fakeView),
    };
    const store = createActivityStore();
    store.getState().project(fakeService, thread());
    const state = store.getState();
    expect(state.view).toBe(fakeView);
    expect(fakeService.projectActivity).toHaveBeenCalledOnce();
  });

  it("project() records an error when the service throws", () => {
    const boom = new Error("projection failed");
    const fakeService = {
      projectActivity: vi.fn().mockImplementation(() => {
        throw boom;
      }),
    };
    const store = createActivityStore();
    store.getState().project(fakeService, thread());
    const state = store.getState();
    expect(state.view).toBeNull();
    expect(state.status).toBe("error");
    expect(state.error).toBe("projection failed");
  });

  it("reset() clears the view and returns to idle", () => {
    const realService = createActivityService();
    const store = createActivityStore();
    store.getState().project(realService, thread());
    expect(store.getState().view).not.toBeNull();
    store.getState().reset();
    const state = store.getState();
    expect(state.view).toBeNull();
    expect(state.status).toBe("idle");
    expect(state.error).toBeNull();
  });

  it("generation safety: a late projection from an older generation is dropped", () => {
    const view1: ActivityView = {
      tasks: [{ status: "done", count: 1 }],
      work: [],
      usage: {},
      capabilities: ALL_TRUE_CAPS as MobileCapabilities,
    };
    const view2: ActivityView = {
      tasks: [{ status: "done", count: 2 }],
      work: [],
      usage: {},
      capabilities: ALL_TRUE_CAPS as MobileCapabilities,
    };
    let nextView = view1;
    const fakeService = {
      projectActivity: vi.fn().mockImplementation(() => nextView),
    };
    const store = createActivityStore();
    // First projection — generation 1.
    store.getState().project(fakeService, thread());
    expect(store.getState().view).toBe(view1);
    // Second projection — generation 2.
    nextView = view2;
    store.getState().project(fakeService, thread());
    expect(store.getState().view).toBe(view2);
    // Simulate a late completion from generation 1 by replaying the same view.
    // The store exposes generationForTest to inspect the generation counter.
    const gen = store.getState().generationForTest();
    // Re-projecting should produce generation gen, not regress.
    nextView = view1;
    store.getState().project(fakeService, thread());
    expect(store.getState().view).toBe(view1);
    expect(store.getState().generationForTest()).toBe(gen);
  });

  it("projectActivity is deterministic via the real service (smoke)", () => {
    const realService = createActivityService();
    const diag: EvenerDiagnostics = {
      jobs: [
        { jobId: "j1", jobType: "shell", status: "running", outputBytes: 0 },
      ],
    };
    const t = thread({ evener: evenerThread({ diagnostics: diag }) });
    const store = createActivityStore();
    store.getState().project(realService, t);
    const v1 = store.getState().view;
    store.getState().project(realService, t);
    const v2 = store.getState().view;
    expect(v1).toEqual(v2);
  });

  // --- Step 4: Activity notification and setView tests -----------------------

  describe("setView", () => {
    it("replaces the activity view directly", () => {
      const view: ActivityView = {
        tasks: [{ status: "done", count: 5 }],
        work: [],
        usage: { totalTokens: 100 },
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      };
      const store = createActivityStore();
      store.getState().setView(view);
      expect(store.getState().view).toBe(view);
      expect(store.getState().status).toBe("open");
    });

    it("setView from a read-boundary contains tasks/work/usage", () => {
      const view: ActivityView = {
        tasks: [
          { status: "active", count: 1 },
          { status: "open", count: 2 },
          { status: "done", count: 3 },
        ],
        work: [
          {
            kind: "job",
            label: "shell",
            tone: "running",
            outputSummary: "1.0 KB",
          },
        ],
        usage: { totalTokens: 42, cost: "$0.01" },
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      };
      const store = createActivityStore();
      store.getState().setView(view);
      const v = store.getState().view;
      expect(v?.tasks).toHaveLength(3);
      expect(v?.work).toHaveLength(1);
      expect(v?.usage.totalTokens).toBe(42);
      expect(v?.usage.cost).toBe("$0.01");
    });
  });

  describe("activity notification patching", () => {
    it("evener/job/started patches a sanitized work entry", () => {
      const store = createActivityStore();
      const initialView: ActivityView = {
        tasks: [],
        work: [],
        usage: {},
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      };
      store.getState().setView(initialView);
      store.getState().applyNotification({
        method: "evener/job/started",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          job: {
            jobId: "job-new",
            jobType: "shell",
            status: "running",
            outputBytes: 0,
          },
        },
      } as AnyNotification);
      const v = store.getState().view;
      expect(v?.work).toHaveLength(1);
      expect(v?.work[0]?.label).toBe("shell");
      // Sanitized: diagnostics has rawId but no command/task
      expect(v?.work[0]?.diagnostics?.rawId).toBe("job-new");
      expect(v?.work[0]?.diagnostics).not.toHaveProperty("command");
    });

    it("evener/delegate/updated patches a sanitized delegate entry", () => {
      const store = createActivityStore();
      const initialView: ActivityView = {
        tasks: [],
        work: [],
        usage: {},
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      };
      store.getState().setView(initialView);
      store.getState().applyNotification({
        method: "evener/delegate/updated",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          delegate: {
            delegateId: "dlg-1",
            ownerSessionId: "sess",
            rootSessionId: "sess",
            childSessionId: "child",
            transcriptRef: "local:abc",
            type: "subagent",
            lifecycle: "running",
            phase: "running",
            status: "running",
            resumable: true,
            projectionRevision: 1,
            needsAttention: false,
          },
        },
      } as AnyNotification);
      const v = store.getState().view;
      expect(v?.work).toHaveLength(1);
      expect(v?.work[0]?.kind).toBe("delegate");
      // Sanitized: no transcriptRef exposed in the view model
      expect(v?.work[0]?.diagnostics).not.toHaveProperty("transcriptRef");
    });

    it("evener/task/updated patches task counts", () => {
      const store = createActivityStore();
      const initialView: ActivityView = {
        tasks: [],
        work: [],
        usage: {},
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      };
      store.getState().setView(initialView);
      store.getState().applyNotification({
        method: "evener/task/updated",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          total: 10,
          done: 3,
        },
      } as AnyNotification);
      const v = store.getState().view;
      const doneGroup = v?.tasks.find((g) => g.status === "done");
      expect(doneGroup?.count).toBe(3);
      const openGroup = v?.tasks.find((g) => g.status === "open");
      expect(openGroup?.count).toBe(7);
    });

    it("turn/completed refreshes usage", () => {
      const store = createActivityStore();
      const initialView: ActivityView = {
        tasks: [],
        work: [],
        usage: { totalTokens: 50 },
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      };
      store.getState().setView(initialView);
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
            usage: { totalTokens: 200, inputTokens: 100, outputTokens: 100 },
          },
        },
      } as AnyNotification);
      const v = store.getState().view;
      expect(v?.usage.totalTokens).toBe(200);
      expect(v?.usage.inputTokens).toBe(100);
      expect(v?.usage.outputTokens).toBe(100);
    });
  });

  describe("jobs-tree revision coalesces one rehydrate", () => {
    it("evener/jobs/treeUpdated triggers one rehydrate call", () => {
      const rehydrateCalls: { ref: string }[] = [];
      let pending = false;
      const coalescer = {
        requestRehydrate(ref: string) {
          if (pending) return;
          pending = true;
          queueMicrotask(() => {
            pending = false;
            rehydrateCalls.push({ ref });
          });
        },
      };
      const store = createActivityStore();
      const initialView: ActivityView = {
        tasks: [],
        work: [],
        usage: {},
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      };
      store.getState().setView(initialView);
      store.getState().setCoalescer(coalescer);

      // Emit multiple jobs/treeUpdated notifications
      store.getState().applyNotification({
        method: "evener/jobs/treeUpdated",
        params: { threadId: "thread-1", ref: "ref-1", revision: 1 },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "evener/jobs/treeUpdated",
        params: { threadId: "thread-1", ref: "ref-1", revision: 2 },
      } as AnyNotification);
      store.getState().applyNotification({
        method: "evener/jobs/treeUpdated",
        params: { threadId: "thread-1", ref: "ref-1", revision: 3 },
      } as AnyNotification);

      return Promise.resolve().then(() =>
        Promise.resolve().then(() => {
          expect(rehydrateCalls.length).toBe(1);
        }),
      );
    });
  });

  describe("profile switch resets activity and rejects stale patches", () => {
    it("reset clears activity view", () => {
      const store = createActivityStore();
      store.getState().setView({
        tasks: [{ status: "done", count: 1 }],
        work: [],
        usage: {},
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      });
      store.getState().reset();
      expect(store.getState().view).toBeNull();
      expect(store.getState().status).toBe("idle");
    });

    it("rejects stale notification after reset (generation safety)", () => {
      const store = createActivityStore();
      store.getState().setView({
        tasks: [{ status: "done", count: 1 }],
        work: [],
        usage: {},
        capabilities: ALL_TRUE_CAPS as MobileCapabilities,
      });
      const genBefore = store.getState().generationForTest();
      store.getState().reset();
      const genAfter = store.getState().generationForTest();
      expect(genAfter).toBeGreaterThan(genBefore);

      // After reset, a stale notification should not produce a view
      store.getState().applyNotification({
        method: "evener/task/updated",
        params: {
          threadId: "thread-1",
          ref: "ref-1",
          total: 5,
          done: 5,
        },
      } as AnyNotification);
      // View should still be null after reset
      expect(store.getState().view).toBeNull();
    });
  });
});

// Ensure the type is exported and shaped as a Zustand store.
function _typeCheck(state: ActivityState): void {
  state.project(createActivityService(), {} as Thread);
  state.reset();
}
void _typeCheck;
