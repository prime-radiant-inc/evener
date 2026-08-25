import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { canonicalFixture } from "./fixtures";
import { preferenceStorageKey } from "./persistence";
import {
  createPrototypeStore,
  PrototypeProvider,
  usePrototypeActions,
  usePrototypeState,
} from "./store";

function storage(initial: string | null = null) {
  let value = initial;
  return {
    getItem: vi.fn(() => value),
    setItem: vi.fn((_key: string, next: string) => {
      value = next;
    }),
    removeItem: vi.fn(() => {
      value = null;
    }),
  };
}

function diagnostics() {
  return { report: vi.fn() };
}

describe("prototype store", () => {
  it("decodes preferences and fixture exactly once while creating state", () => {
    const persistence = storage(
      JSON.stringify({
        version: 1,
        concept: "field-notes",
        appearance: "light",
        textScale: "large",
        reducedMotion: true,
        scenario: "question",
      }),
    );
    const sink = diagnostics();
    const store = createPrototypeStore({
      platform: "android",
      storage: persistence,
      fixtureInput: canonicalFixture,
      diagnostics: sink,
    });
    const first = store.getState();
    const second = store.getState();

    expect(first).toBe(second);
    expect(first).toMatchObject({
      platform: "android",
      concept: "field-notes",
      appearance: "light",
      scenario: "question",
      route: { kind: "root", tab: "sessions" },
    });
    expect(persistence.getItem).toHaveBeenCalledOnce();
    expect(persistence.getItem).toHaveBeenCalledWith(preferenceStorageKey);
    expect(sink.report).not.toHaveBeenCalled();
  });

  it("uses canonical fallback and reports through the supplied sink", () => {
    const sink = diagnostics();
    const store = createPrototypeStore({
      platform: "ios",
      storage: storage(),
      fixtureInput: { version: 2 },
      diagnostics: sink,
    });
    expect(store.getState().projection.fixture).toEqual(canonicalFixture);
    expect(sink.report).toHaveBeenCalledWith({
      code: "fixture-version",
      path: "$.version",
    });
  });

  it("persists only allowlisted preference actions", () => {
    const persistence = storage();
    const store = createPrototypeStore({
      platform: "ios",
      storage: persistence,
      fixtureInput: canonicalFixture,
      diagnostics: diagnostics(),
    });

    for (const action of [
      { type: "setDraft", value: "private" },
      { type: "setGlobalQuery", value: "query" },
      { type: "openSession", sessionId: "session-native-client" },
      { type: "toggleTool", itemId: "item-tool-inspect" },
      { type: "setNewSessionProject", value: "/workspace/aurora" },
      { type: "setSpeakResponses", enabled: false },
      { type: "advanceVoice" },
    ] as const) {
      store.getState().dispatch(action);
    }
    expect(persistence.setItem).not.toHaveBeenCalled();

    store.getState().dispatch({ type: "selectConcept", concept: "stillwater" });
    store.getState().dispatch({ type: "setAppearance", appearance: "dark" });
    store
      .getState()
      .dispatch({ type: "setTextScale", textScale: "accessibility" });
    store
      .getState()
      .dispatch({ type: "setReducedMotion", reducedMotion: true });
    store.getState().dispatch({ type: "setScenario", scenario: "offline" });

    expect(persistence.setItem).toHaveBeenCalledTimes(5);
    for (const [key, encoded] of persistence.setItem.mock.calls) {
      expect(key).toBe(preferenceStorageKey);
      const record = JSON.parse(encoded);
      expect(Object.keys(record)).toEqual([
        "version",
        "concept",
        "appearance",
        "textScale",
        "reducedMotion",
        "scenario",
      ]);
      expect(encoded).not.toContain("private");
      expect(encoded).not.toContain("query");
      expect(encoded).not.toContain("voicePreferences");
    }
  });

  it("resetPrototype removes storage before dispatching reset", () => {
    const calls: string[] = [];
    const persistence = {
      getItem: vi.fn(() => null),
      setItem: vi.fn(),
      removeItem: vi.fn(() => calls.push("remove")),
    };
    const store = createPrototypeStore({
      platform: "ios",
      storage: persistence,
      fixtureInput: canonicalFixture,
      diagnostics: diagnostics(),
    });
    store.subscribe((state) => {
      if (state.resetGeneration > 0) calls.push("reset");
    });
    store.getState().dispatch({ type: "setDraft", value: "discard" });
    store.getState().resetPrototype();

    expect(calls).toEqual(["remove", "reset"]);
    expect(persistence.removeItem).toHaveBeenCalledWith(preferenceStorageKey);
    expect(store.getState()).toMatchObject({
      concept: null,
      route: { kind: "gallery" },
      draft: "",
      resetGeneration: 1,
    });
    expect(persistence.setItem).not.toHaveBeenCalled();
  });

  it("provides selector state and stable exact actions without recreating the store", () => {
    const store = createPrototypeStore({
      platform: "ios",
      storage: storage(),
      fixtureInput: canonicalFixture,
      diagnostics: diagnostics(),
    });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <PrototypeProvider store={store}>{children}</PrototypeProvider>
    );
    const stateHook = renderHook(
      () => usePrototypeState((state) => state.draft),
      {
        wrapper,
      },
    );
    const actionsHook = renderHook(() => usePrototypeActions(), { wrapper });
    const firstActions = actionsHook.result.current;

    act(() => firstActions.dispatch({ type: "setDraft", value: "from hook" }));
    expect(stateHook.result.current).toBe("from hook");
    actionsHook.rerender();
    expect(actionsHook.result.current).toBe(firstActions);
    expect(Object.keys(firstActions).sort()).toEqual([
      "dispatch",
      "resetPrototype",
    ]);
  });
});
