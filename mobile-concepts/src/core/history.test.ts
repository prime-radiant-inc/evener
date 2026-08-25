import { beforeEach, describe, expect, it, vi } from "vitest";
import { canonicalFixture } from "./fixtures";
import {
  type ConceptHistoryState,
  createNavigationController,
} from "./history";
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

beforeEach(() => {
  window.history.replaceState(null, "", "/");
  vi.restoreAllMocks();
});

describe("createNavigationController", () => {
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
    window.dispatchEvent(new PopStateEvent("popstate", { state: owned(4) }));
    controller.dispatch({ type: "openOverlay", overlay: "lab-controls" });

    expect(push).toHaveBeenCalledTimes(6);
    const keys = push.mock.calls.map(
      ([state]) => (state as ConceptHistoryState).key,
    );
    expect(new Set(keys).size).toBe(6);

    controller.dispatch({ type: "navigateRoot", tab: "new" });
    controller.dispatch({
      type: "setNewSessionProject",
      value: "/workspace/aurora",
    });
    controller.dispatch({ type: "setNewSessionPrompt", value: "New session" });
    controller.dispatch({ type: "submitNewSession" });
    controller.dispatch({ type: "completeNewSession", result: "success" });
    expect(push).toHaveBeenCalledTimes(7);
    controller.dispose();
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
