import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ConceptModule } from "../concepts/contract";
import { conceptRegistry } from "../concepts/registry";
import { canonicalFixture } from "../core/fixtures";
import {
  createNavigationController,
  type NavigationController,
} from "../core/history";
import type {
  Appearance,
  ConceptId,
  Platform,
  Route,
  ScenarioId,
  TextScale,
} from "../core/model";
import { defaultPreferences, preferenceStorageKey } from "../core/persistence";
import { getPlatformPrimitives } from "../core/platform";
import { scenarioIds } from "../core/scenarios";
import {
  createPrototypeStore,
  PrototypeProvider,
  usePrototypeState,
} from "../core/store";

interface MatrixCase {
  appearance: Exclude<Appearance, "system">;
  concept: ConceptId;
  module: ConceptModule;
  platform: Platform;
  reducedMotion: boolean;
  scenario: ScenarioId;
  textScale: Extract<TextScale, "standard" | "accessibility">;
}

const literalConceptIds = [
  "stillwater",
  "constellation",
  "field-notes",
] as const satisfies readonly ConceptId[];
const literalScenarioIds = [
  "baseline",
  "loading",
  "empty",
  "offline",
  "error",
  "needs-attention",
  "multi-agent",
  "question",
  "completed",
  "voice",
  "long-content",
] as const satisfies readonly ScenarioId[];
const literalVoiceStates = [
  "idle",
  "ready",
  "listening",
  "processing",
  "speaking",
  "interrupted",
  "denied",
  "error",
] as const;

const routeByScenario: Readonly<Record<ScenarioId, Route>> = {
  baseline: { kind: "root", tab: "sessions" },
  loading: { kind: "root", tab: "search" },
  empty: { kind: "root", tab: "new" },
  offline: { kind: "root", tab: "settings" },
  error: { kind: "conversation", sessionId: "session-native-client" },
  "needs-attention": {
    kind: "conversation",
    sessionId: "session-mobile-release",
  },
  "multi-agent": { kind: "work", sessionId: "session-native-client" },
  question: { kind: "conversation", sessionId: "session-mobile-release" },
  completed: { kind: "conversation", sessionId: "session-pairing-pr" },
  voice: { kind: "voice", sessionId: "session-native-client" },
  "long-content": { kind: "root", tab: "sessions" },
};

const surfaceSelectorByScenario: Readonly<Record<ScenarioId, string>> = {
  baseline: '[data-session-group-id="needs-you"]',
  loading: '[data-screen-state="loading"]',
  empty: '[data-screen-state="empty"]',
  offline: '[data-offline-prototype="true"]',
  error: '[data-screen-state="error"]',
  "needs-attention": '[data-question-id="question-release-focus"]',
  "multi-agent": "[data-work-usage]",
  question: 'form[data-question-id="question-release-focus"]',
  completed: '[aria-label="Transcript"]',
  voice: "[data-voice-state]",
  "long-content": '[data-session-id="session-native-client"]',
};

const matrixCases: MatrixCase[] = [];
for (const module of Object.values(conceptRegistry)) {
  for (const platform of ["ios", "android"] as const) {
    for (const scenario of scenarioIds) {
      for (const appearance of ["light", "dark"] as const) {
        for (const textScale of ["standard", "accessibility"] as const) {
          for (const reducedMotion of [false, true] as const) {
            matrixCases.push({
              appearance,
              concept: module.id,
              module,
              platform,
              reducedMotion,
              scenario,
              textScale,
            });
          }
        }
      }
    }
  }
}

const controllers: NavigationController[] = [];

function storageFor(testCase: MatrixCase) {
  const values = new Map<string, string>([
    [
      preferenceStorageKey,
      JSON.stringify({
        ...defaultPreferences,
        concept: testCase.concept,
        scenario: testCase.scenario,
        appearance: testCase.appearance,
        textScale: testCase.textScale,
        reducedMotion: testCase.reducedMotion,
      }),
    ],
  ]);
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
    removeItem: (key: string) => values.delete(key),
  };
}

function ConnectedMatrixRenderer({
  module,
  controller,
}: {
  module: ConceptModule;
  controller: NavigationController;
}) {
  const state = usePrototypeState((current) => current);
  const { Renderer } = module;
  const primitives = getPlatformPrimitives(state.platform);
  return (
    <Renderer
      state={state}
      dispatch={controller.dispatch}
      platform={state.platform}
      primitives={primitives}
      onOpenConceptSwitcher={() =>
        controller.dispatch({
          type: "openOverlay",
          overlay: "concept-switcher",
        })
      }
      onOpenLabControls={() =>
        controller.dispatch({ type: "openOverlay", overlay: "lab-controls" })
      }
    />
  );
}

function navigateThroughController(
  controller: NavigationController,
  route: Route,
): void {
  switch (route.kind) {
    case "gallery":
      return;
    case "root":
      controller.dispatch({ type: "navigateRoot", tab: route.tab });
      return;
    case "conversation":
      controller.dispatch({
        type: "openSession",
        sessionId: route.sessionId,
        focusItemId: route.focusItemId,
      });
      return;
    case "work":
      controller.dispatch({ type: "openWork", sessionId: route.sessionId });
      return;
    case "voice":
      controller.dispatch({ type: "openVoice", sessionId: route.sessionId });
  }
}

function renderMatrixCase(testCase: MatrixCase) {
  const store = createPrototypeStore({
    platform: testCase.platform,
    storage: storageFor(testCase),
    fixtureInput: canonicalFixture,
    diagnostics: { report: vi.fn() },
  });
  const controller = createNavigationController(store, window);
  controllers.push(controller);
  navigateThroughController(controller, routeByScenario[testCase.scenario]);
  render(
    <PrototypeProvider store={store}>
      <ConnectedMatrixRenderer
        module={testCase.module}
        controller={controller}
      />
    </PrototypeProvider>,
  );
  return store;
}

beforeEach(() => window.history.replaceState(null, "", "/"));
afterEach(() => {
  cleanup();
  for (const controller of controllers.splice(0)) controller.dispose();
  vi.restoreAllMocks();
});

describe("scenario render matrix", () => {
  it.each(matrixCases)(
    "$concept $platform $scenario $appearance $textScale reduced=$reducedMotion",
    (testCase) => {
      const uncaught: unknown[] = [];
      const recordError = (event: ErrorEvent | PromiseRejectionEvent) => {
        uncaught.push(
          "error" in event
            ? event.error
            : (event as PromiseRejectionEvent).reason,
        );
      };
      window.addEventListener("error", recordError);
      window.addEventListener("unhandledrejection", recordError);
      try {
        const store = renderMatrixCase(testCase);
        const mains = screen.getAllByRole("main");
        const roots = document.querySelectorAll("[data-concept-root]");
        const root = roots[0];
        const switches = screen.getAllByRole("button", {
          name: "Switch concept",
        });
        const labs = screen.getAllByRole("button", { name: "Lab Controls" });
        const state = store.getState();
        const expectedRoute = routeByScenario[testCase.scenario];
        const expectedRouteName =
          expectedRoute.kind === "root"
            ? expectedRoute.tab
            : expectedRoute.kind;
        const routeMain = mains[0];
        expect({
          appearanceAttribute: root?.getAttribute("data-appearance"),
          conceptClass: root?.classList.contains(`concept-${testCase.concept}`),
          conceptState: state.concept,
          labControls: labs.length,
          mainCount: mains.length,
          motionAttribute: root?.getAttribute("data-reduced-motion"),
          platformAttribute: root?.getAttribute("data-platform"),
          projectionScenario: state.projection.id,
          routeAttribute: routeMain?.getAttribute("data-route"),
          routeState: state.route,
          rootCount: roots.length,
          scenarioState: state.scenario,
          switchConcept: switches.length,
          surfaceCount: routeMain?.querySelectorAll(
            surfaceSelectorByScenario[testCase.scenario],
          ).length,
          textScaleState: state.textScale,
          uncaught,
        }).toEqual({
          appearanceAttribute: testCase.appearance,
          conceptClass: true,
          conceptState: testCase.concept,
          labControls: 1,
          mainCount: 1,
          motionAttribute: String(testCase.reducedMotion),
          platformAttribute: testCase.platform,
          projectionScenario: testCase.scenario,
          routeAttribute: expectedRouteName,
          routeState: expectedRoute,
          rootCount: 1,
          scenarioState: testCase.scenario,
          switchConcept: 1,
          surfaceCount: 1,
          textScaleState: testCase.textScale,
          uncaught: [],
        });
      } finally {
        window.removeEventListener("error", recordError);
        window.removeEventListener("unhandledrejection", recordError);
      }
    },
  );

  it("covers the exact bounded cartesian product", () => {
    expect(Object.keys(conceptRegistry)).toEqual(literalConceptIds);
    expect(scenarioIds).toEqual(literalScenarioIds);
    expect(canonicalFixture.voiceSteps.map(({ state }) => state)).toEqual(
      literalVoiceStates,
    );
    expect(matrixCases).toHaveLength(528);
    expect(
      new Set(
        matrixCases.map((testCase) =>
          JSON.stringify(testCase, [
            "concept",
            "platform",
            "scenario",
            "appearance",
            "textScale",
            "reducedMotion",
          ]),
        ),
      ),
    ).toHaveLength(528);
    expect(
      new Set(
        literalScenarioIds.map((scenario) => {
          const route = routeByScenario[scenario];
          return route.kind === "root" ? route.tab : route.kind;
        }),
      ),
    ).toEqual(
      new Set([
        "sessions",
        "search",
        "new",
        "settings",
        "conversation",
        "work",
        "voice",
      ]),
    );
  });
});
