import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { StoreApi } from "zustand/vanilla";
import { canonicalFixture } from "../core/fixtures";
import {
  createNavigationController,
  type NavigationController,
} from "../core/history";
import type { ConceptId, Platform } from "../core/model";
import { preferenceStorageKey } from "../core/persistence";
import { getPlatformPrimitives } from "../core/platform";
import {
  createPrototypeStore,
  type PreferenceStorage,
  PrototypeProvider,
  type PrototypeStore,
} from "../core/store";
import { RootApp } from "./RootApp";

interface MemoryStorage extends PreferenceStorage {
  values: Map<string, string>;
}

function memoryStorage(concept: ConceptId | null = null): MemoryStorage {
  const values = new Map<string, string>();
  if (concept !== null) {
    values.set(
      preferenceStorageKey,
      JSON.stringify({
        version: 1,
        concept,
        appearance: "system",
        textScale: "standard",
        reducedMotion: false,
        scenario: "baseline",
      }),
    );
  }
  return {
    values,
    getItem: (key) => values.get(key) ?? null,
    setItem: vi.fn((key: string, value: string) => values.set(key, value)),
    removeItem: vi.fn((key: string) => values.delete(key)),
  };
}

interface RenderedRoot {
  store: StoreApi<PrototypeStore>;
  navigation: NavigationController;
  unmount(): void;
  rerender(ui: ReactElement): void;
}

function renderRoot({
  concept = null,
  platform = "ios",
  storage = memoryStorage(concept),
}: {
  concept?: ConceptId | null;
  platform?: Platform;
  storage?: MemoryStorage;
} = {}): RenderedRoot & { storage: MemoryStorage } {
  const store = createPrototypeStore({
    platform,
    storage,
    fixtureInput: canonicalFixture,
    diagnostics: { report: vi.fn() },
  });
  const navigation = createNavigationController(store, window);
  const view = render(
    <PrototypeProvider store={store}>
      <RootApp
        dispatch={navigation.dispatch}
        primitives={getPlatformPrimitives(platform)}
      />
    </PrototypeProvider>,
  );
  return { store, storage, navigation, ...view };
}

function nextPopState(): Promise<PopStateEvent> {
  return new Promise((resolve, reject) => {
    const timeout = window.setTimeout(() => {
      window.removeEventListener("popstate", onPopState);
      reject(new Error("timed out waiting for popstate"));
    }, 1_000);
    const onPopState = (event: PopStateEvent) => {
      window.clearTimeout(timeout);
      window.removeEventListener("popstate", onPopState);
      resolve(event);
    };
    window.addEventListener("popstate", onPopState);
  });
}

beforeEach(() => window.history.replaceState(null, "", "/"));
afterEach(() => cleanup());

describe("RootApp", () => {
  it("starts at the gallery without a persisted concept and opens live Sessions on first selection", () => {
    const { store, navigation } = renderRoot();
    expect(screen.getByTestId("concept-gallery")).toBeVisible();
    expect(
      screen.queryByText("Mobile release checklist"),
    ).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Select Stillwater" }));

    expect(screen.queryByTestId("concept-gallery")).not.toBeInTheDocument();
    expect(document.querySelector(".concept-stillwater")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Sessions" })).toBeVisible();
    expect(screen.getByText("Mobile release checklist")).toBeVisible();
    expect(store.getState().route).toEqual({ kind: "root", tab: "sessions" });
    expect(store.getState().concept).toBe("stillwater");
    navigation.dispose();
  });

  it.each([
    ["stillwater", ".concept-stillwater"],
    ["constellation", ".concept-constellation"],
    ["field-notes", ".concept-field-notes"],
  ] as const)(
    "renders the live %s module with the shared platform and state",
    (concept, selector) => {
      const { store, navigation } = renderRoot({
        concept,
        platform: "android",
      });
      act(() => {
        store
          .getState()
          .dispatch({ type: "setAppearance", appearance: "dark" });
      });

      const root = document.querySelector(selector);
      expect(root).toHaveAttribute("data-concept-root");
      expect(root).toHaveAttribute("data-platform", "android");
      expect(root).toHaveAttribute(
        "data-navigation",
        "material-navigation-bar",
      );
      expect(root).toHaveAttribute("data-sheet", "material-modal-bottom-sheet");
      expect(root).toHaveAttribute("data-appearance", "dark");
      expect(root?.querySelector("main")).toHaveAttribute(
        "data-route",
        "sessions",
      );
      expect(screen.getByRole("button", { name: "Sessions" })).toHaveAttribute(
        "aria-current",
        "page",
      );
      navigation.dispose();
    },
  );

  it.each([
    ["Sessions", null],
    [
      "Conversation",
      { type: "openSession", sessionId: "session-native-client" },
    ],
    ["Work", { type: "openWork", sessionId: "session-native-client" }],
    ["Voice", { type: "openVoice", sessionId: "session-native-client" }],
  ] as const)(
    "opens one owned modal entry from %s and real popstate closes it before the route",
    async (_name, routeAction) => {
      const { store, navigation } = renderRoot({ concept: "stillwater" });
      if (routeAction !== null) navigation.dispatch(routeAction);
      const routeBefore = store.getState().route;
      const depthBefore = window.history.state.depth;
      const opener = screen.getByRole("button", { name: "Switch concept" });
      opener.focus();

      fireEvent.click(opener);

      expect(
        screen.getByRole("dialog", { name: "Switch concept" }),
      ).toBeVisible();
      expect(window.history.state.depth).toBe(depthBefore + 1);
      expect(screen.getByTestId("foundation-background")).toHaveAttribute(
        "inert",
      );
      const popped = nextPopState();
      act(() => window.history.back());
      await popped;

      await waitFor(() =>
        expect(
          screen.queryByRole("dialog", { name: "Switch concept" }),
        ).not.toBeInTheDocument(),
      );
      expect(store.getState().route).toEqual(routeBefore);
      expect(opener).toHaveFocus();
      navigation.dispose();
    },
  );

  it("switches concepts without changing conversation, scenario, focus, draft, answers, disclosures, work, or voice state", async () => {
    const { store, navigation } = renderRoot({ concept: "stillwater" });
    navigation.dispatch({ type: "setScenario", scenario: "question" });
    navigation.dispatch({
      type: "openSession",
      sessionId: "session-mobile-release",
      focusItemId: "item-question-release-focus",
    });
    navigation.dispatch({ type: "setDraft", value: "preserve this draft" });
    navigation.dispatch({
      type: "setQuestionOption",
      questionId: "question-release-focus",
      optionId: "option-navigation",
      selected: true,
    });
    navigation.dispatch({ type: "toggleTool", itemId: "item-tool-inspect" });
    navigation.dispatch({ type: "toggleWork", nodeId: "work-task-shell" });
    navigation.dispatch({ type: "toggleVoiceMute" });
    navigation.dispatch({ type: "setSpeechRate", rate: "fast" });
    const before = store.getState();

    fireEvent.click(screen.getByRole("button", { name: "Switch concept" }));
    const closed = nextPopState();
    fireEvent.click(
      screen.getByRole("button", { name: "Select Constellation" }),
    );
    await closed;

    const after = store.getState();
    expect(after.overlay).toBeNull();
    expect({
      route: after.route,
      scenario: after.scenario,
      draft: after.draft,
    }).toEqual({
      route: before.route,
      scenario: before.scenario,
      draft: before.draft,
    });
    expect(after.concept).toBe("constellation");
    expect(after.history).toEqual(before.history);
    expect(after.selectedSessionId).toBe(before.selectedSessionId);
    expect(after.focusedItemId).toBe(before.focusedItemId);
    expect(after.answers).toEqual(before.answers);
    expect(after.expandedToolIds).toEqual(before.expandedToolIds);
    expect(after.expandedWorkIds).toEqual(before.expandedWorkIds);
    expect(after.voice).toEqual(before.voice);
    expect(after.voicePreferences).toEqual(before.voicePreferences);
    expect(
      document.querySelector(".concept-constellation"),
    ).toBeInTheDocument();
    navigation.dispose();
  });

  it("closing without selection changes only the overlay and restores focus", async () => {
    const { store, navigation } = renderRoot({ concept: "field-notes" });
    navigation.dispatch({
      type: "openSession",
      sessionId: "session-native-client",
      focusItemId: "item-tool-inspect",
    });
    navigation.dispatch({ type: "setDraft", value: "unchanged" });
    const before = store.getState();
    const opener = screen.getByRole("button", { name: "Switch concept" });
    await waitFor(() =>
      expect(document.querySelector("[data-focused='true']")).toHaveFocus(),
    );
    opener.focus();

    fireEvent.click(opener);
    const closed = nextPopState();
    fireEvent.click(
      screen.getByRole("button", { name: "Close concept switcher" }),
    );
    await closed;

    const after = store.getState();
    expect({ ...after, overlay: before.overlay }).toEqual(before);
    await waitFor(() => expect(opener).toHaveFocus());
    navigation.dispose();
  });

  it("persists selection but relaunches at canonical Sessions without route, draft, answers, disclosures, work, or voice state", () => {
    const storage = memoryStorage();
    const first = renderRoot({ storage });
    fireEvent.click(screen.getByRole("button", { name: "Select Field Notes" }));
    first.navigation.dispatch({
      type: "openSession",
      sessionId: "session-mobile-release",
    });
    first.navigation.dispatch({ type: "setDraft", value: "do not restore" });
    first.navigation.dispatch({
      type: "setQuestionOption",
      questionId: "question-release-focus",
      optionId: "option-navigation",
      selected: true,
    });
    first.navigation.dispatch({
      type: "toggleTool",
      itemId: "item-tool-inspect",
    });
    first.navigation.dispatch({
      type: "toggleWork",
      nodeId: "work-task-shell",
    });
    first.navigation.dispatch({ type: "toggleVoiceMute" });
    first.navigation.dispose();
    first.unmount();
    window.history.replaceState(null, "", "/");

    const relaunched = renderRoot({ storage });
    const state = relaunched.store.getState();
    expect(state.concept).toBe("field-notes");
    expect(state.route).toEqual({ kind: "root", tab: "sessions" });
    expect(state.history).toEqual([]);
    expect(state.overlay).toBeNull();
    expect(state.selectedSessionId).toBeNull();
    expect(state.focusedItemId).toBeNull();
    expect(state.draft).toBe("");
    expect(state.answers).toEqual({});
    expect(state.expandedToolIds).toEqual(new Set());
    expect(state.expandedWorkIds).toEqual(new Set());
    expect(state.voice).toEqual({
      stepIndex: 0,
      muted: false,
      stopped: false,
      ended: false,
    });
    expect(document.querySelector(".concept-field-notes main")).toHaveAttribute(
      "data-route",
      "sessions",
    );
    expect(screen.getByRole("button", { name: "Sessions" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    relaunched.navigation.dispose();
  });

  it("Reset clears persistence, returns to gallery, and reconciles owned history to depth zero", async () => {
    const { store, storage, navigation } = renderRoot({
      concept: "stillwater",
    });
    navigation.dispatch({
      type: "openSession",
      sessionId: "session-native-client",
    });
    fireEvent.click(screen.getByRole("button", { name: "Switch concept" }));
    const reconciled = nextPopState();

    act(() => navigation.dispatch({ type: "reset" }));

    expect(storage.removeItem).toHaveBeenCalledWith(preferenceStorageKey);
    expect(store.getState().concept).toBeNull();
    expect(store.getState().route).toEqual({ kind: "gallery" });
    expect(store.getState().history).toEqual([]);
    expect(screen.getByTestId("concept-gallery")).toBeVisible();
    await reconciled;
    expect(window.history.state.depth).toBe(0);
    navigation.dispose();
  });
});
