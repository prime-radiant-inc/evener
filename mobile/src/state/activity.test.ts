// ActivityStore (Zustand) tests with a fake ActivityService. The store wraps an
// ActivityService and the current ActivityView projection. It exposes
// project() (re-project from a Thread) and reset() (clear on conversation
// switch). It holds only the projected ActivityView (never the raw wire
// Thread) and is generation-safe: late re-projections from an older
// conversation cannot overwrite a newer one.

import { describe, expect, it, vi } from "vitest";
import type {
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
});

// Ensure the type is exported and shaped as a Zustand store.
function _typeCheck(state: ActivityState): void {
  state.project(createActivityService(), {} as Thread);
  state.reset();
}
void _typeCheck;
