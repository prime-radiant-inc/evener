import { beforeEach, describe, expect, it, vi } from "vitest";
import { canonicalFixture } from "./fixtures";
import {
  type ConceptHistoryState,
  createNavigationController,
} from "./history";
import type { PrototypeAction } from "./state";
import { createPrototypeStore, type PreferenceStorage } from "./store";

function memoryStorage(): PreferenceStorage {
  const values = new Map<string, string>();
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  };
}

function createStore() {
  const store = createPrototypeStore({
    platform: "android",
    storage: memoryStorage(),
    fixtureInput: canonicalFixture,
    diagnostics: { report: vi.fn() },
  });
  store.getState().dispatch({ type: "selectConcept", concept: "stillwater" });
  store.getState().dispatch({ type: "navigateRoot", tab: "sessions" });
  return store;
}

function owned(depth: number, key = `test-${depth}`): ConceptHistoryState {
  return { owner: "evener-concepts", depth, key };
}

function nextPopState(target: Window = window): Promise<PopStateEvent> {
  return new Promise((resolve, reject) => {
    const timeout = window.setTimeout(() => {
      target.removeEventListener("popstate", onPopState);
      reject(new Error("timed out waiting for popstate"));
    }, 1_000);
    const onPopState = (event: PopStateEvent) => {
      window.clearTimeout(timeout);
      target.removeEventListener("popstate", onPopState);
      resolve(event);
    };
    target.addEventListener("popstate", onPopState);
  });
}

async function expectNoPopState(action: () => void): Promise<void> {
  let observed = false;
  const onPopState = () => {
    observed = true;
  };
  window.addEventListener("popstate", onPopState);
  action();
  await new Promise((resolve) => window.setTimeout(resolve, 30));
  window.removeEventListener("popstate", onPopState);
  expect(observed).toBe(false);
}

function openDeepStack(
  controller: ReturnType<typeof createNavigationController>,
) {
  controller.dispatch({
    type: "openSession",
    sessionId: "session-native-client",
  });
  controller.dispatch({ type: "openWork", sessionId: "session-native-client" });
  controller.dispatch({
    type: "openVoice",
    sessionId: "session-native-client",
  });
  controller.dispatch({ type: "openOverlay", overlay: "lab-controls" });
}

beforeEach(() => {
  window.history.replaceState(null, "", "/");
  vi.restoreAllMocks();
});

describe("createNavigationController", () => {
  it("uses real browser traversal for the full stack and reconciles root/reset to the original depth zero", async () => {
    const store = createStore();
    const controller = createNavigationController(store, window);
    openDeepStack(controller);
    expect(window.history.state).toMatchObject({ depth: 4 });

    for (const expected of ["voice", "work", "conversation", "root"] as const) {
      const popped = nextPopState();
      controller.dispatch({ type: "goBack" });
      await popped;
      expect(store.getState().overlay).toBeNull();
      expect(store.getState().route.kind).toBe(expected);
    }
    expect(window.history.state).toMatchObject({ depth: 0 });

    openDeepStack(controller);
    const rootTraversal = nextPopState();
    controller.dispatch({ type: "navigateRoot", tab: "search" });
    expect(store.getState().route).toEqual({ kind: "root", tab: "search" });
    await rootTraversal;
    expect(window.history.state).toMatchObject({ depth: 0 });
    await expectNoPopState(() => controller.dispatch({ type: "goBack" }));

    openDeepStack(controller);
    const resetTraversal = nextPopState();
    controller.dispatch({ type: "reset" });
    expect(store.getState().route).toEqual({ kind: "gallery" });
    expect(store.getState().concept).toBeNull();
    await resetTraversal;
    expect(window.history.state).toMatchObject({ depth: 0 });
    await expectNoPopState(() => controller.dispatch({ type: "goBack" }));
    controller.dispose();
  });

  it("initializes one owned depth-zero entry", () => {
    const replace = vi.spyOn(window.history, "replaceState");
    const controller = createNavigationController(createStore(), window);

    expect(replace).toHaveBeenCalledTimes(1);
    expect(replace.mock.calls[0]?.[0]).toMatchObject({
      owner: "evener-concepts",
      depth: 0,
    });
    controller.dispose();
  });

  it("pushes one unique entry for every successful forward class", () => {
    const store = createStore();
    const push = vi.spyOn(window.history, "pushState");
    const controller = createNavigationController(store, window);

    controller.dispatch({
      type: "openSession",
      sessionId: "session-native-client",
    });
    controller.dispatch({
      type: "openSearchResult",
      resultId: "search-transcript",
    });
    controller.dispatch({
      type: "openWork",
      sessionId: "session-native-client",
    });
    controller.dispatch({
      type: "openVoice",
      sessionId: "session-native-client",
    });
    controller.dispatch({ type: "openOverlay", overlay: "concept-switcher" });
    controller.dispatch({ type: "openOverlay", overlay: "lab-controls" });

    expect(push).toHaveBeenCalledTimes(6);
    const keys = push.mock.calls.map(
      ([state]) => (state as ConceptHistoryState).key,
    );
    expect(new Set(keys).size).toBe(6);

    controller.dispose();

    const newSessionStore = createStore();
    const newSessionController = createNavigationController(
      newSessionStore,
      window,
    );
    newSessionController.dispatch({ type: "navigateRoot", tab: "new" });
    newSessionController.dispatch({
      type: "setNewSessionProject",
      value: "/workspace/aurora",
    });
    newSessionController.dispatch({
      type: "setNewSessionPrompt",
      value: "New session",
    });
    newSessionController.dispatch({ type: "submitNewSession" });
    newSessionController.dispatch({
      type: "completeNewSession",
      result: "success",
    });
    expect(push).toHaveBeenCalledTimes(7);
    newSessionController.dispose();
  });

  it("does not push duplicate or failed forward state", () => {
    const store = createStore();
    const push = vi.spyOn(window.history, "pushState");
    const controller = createNavigationController(store, window);

    controller.dispatch({ type: "openOverlay", overlay: "lab-controls" });
    controller.dispatch({ type: "openOverlay", overlay: "lab-controls" });
    controller.dispatch({ type: "openSession", sessionId: "missing" });
    controller.dispatch({ type: "completeNewSession", result: "success" });
    expect(push).toHaveBeenCalledTimes(1);
    controller.dispose();
  });

  it.each([
    {
      name: "openSession",
      action: {
        type: "openSession",
        sessionId: "session-native-client",
        focusItemId: "item-assistant-plan",
      },
    },
    {
      name: "openSearchResult",
      action: { type: "openSearchResult", resultId: "search-transcript" },
    },
    {
      name: "openWork",
      action: { type: "openWork", sessionId: "session-native-client" },
    },
    {
      name: "openVoice",
      action: { type: "openVoice", sessionId: "session-native-client" },
    },
    {
      name: "openOverlay",
      action: { type: "openOverlay", overlay: "concept-switcher" },
    },
  ] as const satisfies readonly { name: string; action: PrototypeAction }[])(
    "treats duplicate $name destinations as identity no-ops",
    ({ action }) => {
      const store = createStore();
      const push = vi.spyOn(window.history, "pushState");
      const controller = createNavigationController(store, window);

      controller.dispatch(action);
      const afterFirst = store.getState();
      const reducerDepth = afterFirst.history.length;
      controller.dispatch(action);

      expect(store.getState()).toBe(afterFirst);
      expect(store.getState().history).toHaveLength(reducerDepth);
      expect(push).toHaveBeenCalledTimes(1);
      controller.dispose();
    },
  );

  it("does not push a completed new-session failure", () => {
    const store = createStore();
    const push = vi.spyOn(window.history, "pushState");
    const controller = createNavigationController(store, window);
    controller.dispatch({ type: "navigateRoot", tab: "new" });
    controller.dispatch({
      type: "setNewSessionProject",
      value: "/workspace/aurora",
    });
    controller.dispatch({ type: "setNewSessionPrompt", value: "New session" });
    controller.dispatch({ type: "submitNewSession" });
    controller.dispatch({ type: "completeNewSession", result: "failure" });

    expect(store.getState().newSession.outcome).toBe("failure");
    expect(push).not.toHaveBeenCalled();
    controller.dispose();
  });

  it("replaces root navigation and reset at owned depth zero", () => {
    const store = createStore();
    const replace = vi.spyOn(window.history, "replaceState");
    const push = vi.spyOn(window.history, "pushState");
    const controller = createNavigationController(store, window);

    controller.dispatch({ type: "navigateRoot", tab: "search" });
    expect(store.getState().route).toEqual({ kind: "root", tab: "search" });
    controller.dispatch({ type: "reset" });
    expect(store.getState().route).toEqual({ kind: "gallery" });
    expect(store.getState().concept).toBeNull();
    expect(replace).toHaveBeenCalledTimes(3);
    expect(replace.mock.calls.slice(1).map(([state]) => state)).toEqual([
      expect.objectContaining({ depth: 0 }),
      expect.objectContaining({ depth: 0 }),
    ]);
    expect(push).not.toHaveBeenCalled();
    controller.dispose();
  });

  it("asks browser history to go back without reducing immediately", () => {
    const store = createStore();
    const controller = createNavigationController(store, window);
    controller.dispatch({
      type: "openSession",
      sessionId: "session-native-client",
    });
    const before = store.getState().route;
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {});

    controller.dispatch({ type: "goBack" });
    expect(back).toHaveBeenCalledTimes(1);
    expect(store.getState().route).toBe(before);
    controller.dispose();
  });

  it("records Voice End without double-popping on its following browser Back", () => {
    const store = createStore();
    const controller = createNavigationController(store, window);
    controller.dispatch({
      type: "openSession",
      sessionId: "session-native-client",
    });
    controller.dispatch({
      type: "openWork",
      sessionId: "session-native-client",
    });
    controller.dispatch({
      type: "openVoice",
      sessionId: "session-native-client",
    });

    controller.dispatch({ type: "endVoice" });
    expect(store.getState().voice.ended).toBe(true);
    expect(store.getState().route.kind).toBe("work");
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {});
    controller.dispatch({ type: "goBack" });
    expect(back).toHaveBeenCalledTimes(1);

    window.dispatchEvent(new PopStateEvent("popstate", { state: owned(2) }));
    expect(store.getState().route.kind).toBe("work");
    window.dispatchEvent(new PopStateEvent("popstate", { state: owned(1) }));
    expect(store.getState().route.kind).toBe("conversation");
    controller.dispose();
  });

  it("closes an overlay then unwinds Voice, Work, and Conversation without repushing", () => {
    const store = createStore();
    const push = vi.spyOn(window.history, "pushState");
    const controller = createNavigationController(store, window);
    controller.dispatch({
      type: "openSession",
      sessionId: "session-native-client",
    });
    controller.dispatch({
      type: "openWork",
      sessionId: "session-native-client",
    });
    controller.dispatch({
      type: "openVoice",
      sessionId: "session-native-client",
    });
    controller.dispatch({ type: "openOverlay", overlay: "lab-controls" });
    expect(push).toHaveBeenCalledTimes(4);

    window.dispatchEvent(new PopStateEvent("popstate", { state: owned(3) }));
    expect(store.getState().overlay).toBeNull();
    expect(store.getState().route.kind).toBe("voice");
    window.dispatchEvent(new PopStateEvent("popstate", { state: owned(2) }));
    expect(store.getState().route.kind).toBe("work");
    window.dispatchEvent(new PopStateEvent("popstate", { state: owned(1) }));
    expect(store.getState().route.kind).toBe("conversation");
    window.dispatchEvent(new PopStateEvent("popstate", { state: owned(0) }));
    expect(store.getState().route).toEqual({ kind: "root", tab: "sessions" });
    expect(push).toHaveBeenCalledTimes(4);
    controller.dispose();
  });

  it.each([
    null,
    { owner: "another-app", depth: 0, key: "foreign" },
    { owner: "evener-concepts", depth: -1, key: "bad" },
    { owner: "evener-concepts", depth: 1.5, key: "bad" },
    { owner: "evener-concepts", depth: 0, key: 8 },
  ])("fails malformed or foreign state closed to root: %j", (state) => {
    const store = createStore();
    const replace = vi.spyOn(window.history, "replaceState");
    const controller = createNavigationController(store, window);
    controller.dispatch({
      type: "openSession",
      sessionId: "session-native-client",
    });
    controller.dispatch({ type: "openOverlay", overlay: "lab-controls" });

    window.dispatchEvent(new PopStateEvent("popstate", { state }));
    expect(store.getState().overlay).toBeNull();
    expect(store.getState().route).toEqual({ kind: "root", tab: "sessions" });
    expect(replace.mock.calls.at(-1)?.[0]).toMatchObject({
      owner: "evener-concepts",
      depth: 0,
    });
    controller.dispose();
  });

  it("removes its exact listener on dispose", () => {
    const store = createStore();
    const add = vi.spyOn(window, "addEventListener");
    const remove = vi.spyOn(window, "removeEventListener");
    const controller = createNavigationController(store, window);
    controller.dispatch({
      type: "openSession",
      sessionId: "session-native-client",
    });
    const route = store.getState().route;

    controller.dispose();
    expect(remove).toHaveBeenCalledWith(
      "popstate",
      add.mock.calls.find(([type]) => type === "popstate")?.[1],
    );
    window.dispatchEvent(new PopStateEvent("popstate", { state: owned(0) }));
    expect(store.getState().route).toBe(route);
  });
});
