import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { StoreApi } from "zustand/vanilla";
import { App } from "../app/App";
import { LabControls } from "../app/LabControls";
import { RootApp } from "../app/RootApp";
import type { ConceptModule } from "../concepts/contract";
import { conceptRegistry } from "../concepts/registry";
import { canonicalFixture } from "../core/fixtures";
import {
  createNavigationController,
  type NavigationController,
} from "../core/history";
import type { Platform } from "../core/model";
import { defaultPreferences, preferenceStorageKey } from "../core/persistence";
import {
  getPlatformPrimitives,
  type PlatformDetectionInput,
} from "../core/platform";
import {
  createPrototypeStore,
  type PreferenceStorage,
  PrototypeProvider,
  type PrototypeStore,
} from "../core/store";

interface SemanticCase {
  module: ConceptModule;
  platform: Platform;
}

const semanticCases: SemanticCase[] = Object.values(conceptRegistry).flatMap(
  (module) =>
    (["ios", "android"] as const).map((platform) => ({ module, platform })),
);
const controllers: NavigationController[] = [];
const pendingPopstateWaiters = new Set<() => void>();

function literalMinimumTarget(platform: Platform): "44" | "48" {
  return platform === "ios" ? "44" : "48";
}

function persistedStorage(module: ConceptModule): PreferenceStorage {
  const values = new Map<string, string>([
    [
      preferenceStorageKey,
      JSON.stringify({ ...defaultPreferences, concept: module.id }),
    ],
  ]);
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  };
}

function renderHarness({
  module,
  platform,
}: SemanticCase): StoreApi<PrototypeStore> {
  const values = new Map<string, string>([
    [
      preferenceStorageKey,
      JSON.stringify({ ...defaultPreferences, concept: module.id }),
    ],
  ]);
  const store = createPrototypeStore({
    platform,
    storage: {
      getItem: (key) => values.get(key) ?? null,
      setItem: (key, value) => values.set(key, value),
      removeItem: (key) => values.delete(key),
    },
    fixtureInput: canonicalFixture,
    diagnostics: { report: vi.fn() },
  });
  const controller = createNavigationController(store, window);
  controllers.push(controller);
  const primitives = getPlatformPrimitives(platform);
  render(
    <PrototypeProvider store={store}>
      <RootApp dispatch={controller.dispatch} primitives={primitives} />
      <LabControls dispatch={controller.dispatch} primitives={primitives} />
    </PrototypeProvider>,
  );
  return store;
}

function main(route: string): HTMLElement {
  const mains = screen.getAllByRole("main");
  expect(mains).toHaveLength(1);
  const current = mains[0];
  if (!current) throw new Error("expected one main landmark");
  expect(current).toHaveAttribute("data-route", route);
  return current;
}

function domainControl(scope: ParentNode, selector: string): HTMLElement {
  const matches = scope.querySelectorAll(selector);
  expect(matches).toHaveLength(1);
  const match = matches[0];
  if (!(match instanceof HTMLElement)) throw new Error(`missing ${selector}`);
  if (match.matches("button, a[href]")) return match;
  const controls = match.querySelectorAll("button, a[href]");
  expect(controls).toHaveLength(1);
  const control = controls[0];
  if (!(control instanceof HTMLElement))
    throw new Error(`missing ${selector} control`);
  return control;
}

function nextPopState(): Promise<PopStateEvent> {
  return new Promise((resolve, reject) => {
    let settled = false;
    const cancel = () => {
      if (settled) return;
      settled = true;
      window.clearTimeout(timeout);
      window.removeEventListener("popstate", onPopState);
      pendingPopstateWaiters.delete(cancel);
    };
    const timeout = window.setTimeout(() => {
      cancel();
      reject(new Error("timed out awaiting semantic-contract popstate"));
    }, 1_000);
    const onPopState = (event: PopStateEvent) => {
      cancel();
      resolve(event);
    };
    pendingPopstateWaiters.add(cancel);
    window.addEventListener("popstate", onPopState);
  });
}

async function back(name: "Back" | "Close"): Promise<void> {
  const popped = nextPopState();
  fireEvent.click(screen.getByRole("button", { name }));
  await popped;
}

function expectUniqueIds(scope: ParentNode = document): void {
  const ids = Array.from(
    scope.querySelectorAll<HTMLElement>("[id]"),
    ({ id }) => id,
  );
  expect(new Set(ids)).toHaveLength(ids.length);
}

function expectHeadingOrder(scope: ParentNode): void {
  const levels = Array.from(
    scope.querySelectorAll<HTMLElement>("h1, h2, h3, h4, h5, h6"),
    (heading) => Number(heading.tagName.slice(1)),
  );
  expect(levels[0]).toBe(1);
  for (let index = 1; index < levels.length; index += 1) {
    const previous = levels[index - 1];
    const current = levels[index];
    if (previous === undefined || current === undefined) continue;
    expect(current).toBeLessThanOrEqual(previous + 1);
  }
}

function expectRootNavigation(
  tab: "sessions" | "search" | "new" | "settings",
): void {
  const label = {
    sessions: "Sessions",
    search: "Search",
    new: "New Session",
    settings: "Settings",
  }[tab];
  const navigation = screen.getByRole("navigation", { name: "Primary" });
  expect(within(navigation).getAllByRole("button")).toHaveLength(4);
  expect(navigation.querySelectorAll('[aria-current="page"]')).toHaveLength(1);
  expect(
    within(navigation).getByRole("button", { name: label }),
  ).toHaveAttribute("aria-current", "page");
}

function iconOnlyButtons(scope: ParentNode): HTMLButtonElement[] {
  return Array.from(scope.querySelectorAll<HTMLButtonElement>("button"))
    .filter((button) => button.querySelector("svg") !== null)
    .filter((button) => button.textContent?.trim() === "");
}

function expectIconOnlyNames(scope: ParentNode, expectedCount: number): void {
  const iconOnly = iconOnlyButtons(scope);
  expect(iconOnly).toHaveLength(expectedCount);
  for (const button of iconOnly) expect(button).toHaveAccessibleName();
}

function expectIconTargetHooks(
  scope: ParentNode,
  platform: Platform,
  expectedCount: number,
): void {
  const iconOnly = iconOnlyButtons(scope);
  expect(iconOnly).toHaveLength(expectedCount);
  for (const button of iconOnly) {
    expect(button).toHaveAttribute(
      "data-icon-target",
      `${literalMinimumTarget(platform)}px`,
    );
  }
}

function expectConcreteTargetSurface(
  surface: HTMLElement,
  platform: Platform,
  controls: readonly HTMLElement[],
  expectedControlCount: number,
): void {
  expect(surface).toHaveAttribute(
    "data-minimum-target",
    literalMinimumTarget(platform),
  );
  expect(controls).toHaveLength(expectedControlCount);
  for (const control of controls) {
    expect(control).toHaveAccessibleName();
  }
}

function expectStatusText(
  scope: ParentNode,
  expectedCount: number,
  foundStates: Set<string>,
  expectedVisibleCount = expectedCount,
): void {
  const statuses = scope.querySelectorAll<HTMLElement>("[data-status-state]");
  expect(statuses).toHaveLength(expectedCount);
  for (const status of statuses) {
    expect(status.textContent?.trim().length).toBeGreaterThan(0);
  }
  const visibleStatuses = Array.from(statuses).filter(
    (status) => status.closest('[class*="visually-hidden"]') === null,
  );
  expect(visibleStatuses).toHaveLength(expectedVisibleCount);
  for (const status of visibleStatuses) {
    expect(status).toBeVisible();
    const state = status.dataset.statusState;
    expect(state).toBeTruthy();
    if (state) foundStates.add(state);
  }
}

beforeEach(() => {
  window.history.replaceState(null, "", "/");
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(() => true),
  }));
});
afterEach(() => {
  for (const cancel of [...pendingPopstateWaiters]) cancel();
  const errors: unknown[] = [];
  const attempt = (operation: () => void) => {
    try {
      operation();
    } catch (error) {
      errors.push(error);
    }
  };
  attempt(cleanup);
  for (const controller of controllers.splice(0)) {
    attempt(() => controller.dispose());
  }
  attempt(() => vi.unstubAllGlobals());
  attempt(() => vi.restoreAllMocks());
  if (errors.length > 0) {
    throw new AggregateError(errors, "accessibility cleanup failed");
  }
});

describe("semantic accessibility contract", () => {
  it.each(semanticCases)(
    "$module.id on $platform exposes the complete semantic matrix",
    async (testCase) => {
      const store = renderHarness(testCase);
      const foundStatuses = new Set<string>();

      // Landmark, IDs, heading hierarchy, root navigation, status text, and targets.
      expectUniqueIds();
      expectHeadingOrder(main("sessions"));
      expectRootNavigation("sessions");
      const conceptRoot = document.querySelector("[data-concept-root]");
      expect(conceptRoot).toBeInstanceOf(HTMLElement);
      if (!(conceptRoot instanceof HTMLElement)) return;
      const rootNavigation = screen.getByRole("navigation", {
        name: "Primary",
      });
      expectConcreteTargetSurface(
        conceptRoot,
        testCase.platform,
        within(rootNavigation).getAllByRole("button"),
        4,
      );
      expectStatusText(
        main("sessions"),
        canonicalFixture.sessions.length,
        foundStatuses,
      );

      // Both dialog surfaces are named/modal, enter focus, expose platform hooks,
      // close through real history, and restore the exact opener. Escape is proven
      // on the concept dialog; the Lab close control proves its equivalent path.
      const conceptOpener = screen.getByRole("button", {
        name: "Switch concept",
      });
      conceptOpener.focus();
      fireEvent.click(conceptOpener);
      const conceptDialogs = screen.getAllByRole("dialog");
      expect(conceptDialogs).toHaveLength(1);
      const conceptDialog = screen.getByRole("dialog", {
        name: "Switch concept",
      });
      expect(conceptDialog).toHaveAttribute("aria-modal", "true");
      expect(
        screen.getByRole("button", { name: "Close concept switcher" }),
      ).toHaveFocus();
      expectConcreteTargetSurface(
        conceptDialog,
        testCase.platform,
        within(conceptDialog).getAllByRole("button"),
        4,
      );
      const escaped = nextPopState();
      fireEvent.keyDown(conceptDialog, { key: "Escape" });
      await escaped;
      await waitFor(() => expect(conceptOpener).toHaveFocus());
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

      const labOpener = within(main("sessions")).getByRole("button", {
        name: "Lab Controls",
      });
      labOpener.focus();
      fireEvent.click(labOpener);
      const labDialogs = screen.getAllByRole("dialog");
      expect(labDialogs).toHaveLength(1);
      const labDialog = screen.getByRole("dialog", { name: "Lab Controls" });
      expect(labDialog).toHaveAttribute("aria-modal", "true");
      expect(
        screen.getByRole("button", { name: "Close Lab Controls" }),
      ).toHaveFocus();
      expectConcreteTargetSurface(
        labDialog,
        testCase.platform,
        within(labDialog).getAllByRole("button"),
        2,
      );
      const labClosed = nextPopState();
      fireEvent.click(
        screen.getByRole("button", { name: "Close Lab Controls" }),
      );
      await labClosed;
      await waitFor(() => expect(labOpener).toHaveFocus());

      // Each root destination preserves the heading and one-current-item contracts.
      for (const destination of [
        "search",
        "new",
        "settings",
        "sessions",
      ] as const) {
        const label = {
          sessions: "Sessions",
          search: "Search",
          new: "New Session",
          settings: "Settings",
        }[destination];
        fireEvent.click(screen.getByRole("button", { name: label }));
        expectHeadingOrder(main(destination));
        expectRootNavigation(destination);
        expectUniqueIds();
      }

      // Transcript and Work disclosures have exact expanded/controlled semantics.
      fireEvent.click(
        domainControl(
          main("sessions"),
          '[data-session-id="session-native-client"]',
        ),
      );
      await waitFor(() => expect(main("conversation")).toBeVisible());
      expect(
        screen.queryByRole("navigation", { name: "Primary" }),
      ).not.toBeInTheDocument();
      expectHeadingOrder(main("conversation"));
      expectUniqueIds();
      expectIconOnlyNames(main("conversation"), 1);
      const sessionTools = canonicalFixture.transcript.filter(
        (item) =>
          item.sessionId === "session-native-client" && item.kind === "tool",
      );
      expect(sessionTools).toHaveLength(2);
      expectConcreteTargetSurface(
        conceptRoot,
        testCase.platform,
        sessionTools.map((tool) =>
          within(main("conversation")).getByRole("button", {
            name: tool.kind === "tool" ? tool.label : "",
          }),
        ),
        2,
      );
      for (const tool of sessionTools) {
        if (tool.kind !== "tool") continue;
        const disclosure = within(main("conversation")).getByRole("button", {
          name: tool.label,
        });
        const controlledId = disclosure.getAttribute("aria-controls");
        expect(controlledId).toBeTruthy();
        expect(disclosure).toHaveAttribute("aria-expanded", "false");
        expect(document.getElementById(controlledId ?? "missing")).toBeNull();
        fireEvent.click(disclosure);
        expect(disclosure).toHaveAttribute("aria-expanded", "true");
        const region = document.getElementById(controlledId ?? "missing");
        expect(region).toHaveAttribute("aria-labelledby", disclosure.id);
      }

      fireEvent.click(screen.getByRole("button", { name: "Work" }));
      await waitFor(() => expect(main("work")).toBeVisible());
      const workDisclosureControls: HTMLElement[] = [];
      for (const node of canonicalFixture.work.filter(
        ({ sessionId }) => sessionId === "session-native-client",
      )) {
        const wrapper = main("work").querySelector(
          `[data-work-node-id="${node.id}"]`,
        );
        expect(wrapper).toBeInstanceOf(HTMLElement);
        if (!(wrapper instanceof HTMLElement)) continue;
        const disclosure = within(wrapper).getByRole("button", {
          name: node.title,
        });
        workDisclosureControls.push(disclosure);
        const controlledId = disclosure.getAttribute("aria-controls");
        expect(controlledId).toBeTruthy();
        expect(disclosure).toHaveAttribute("aria-expanded", "false");
        fireEvent.click(disclosure);
        expect(disclosure).toHaveAttribute("aria-expanded", "true");
        expect(
          document.getElementById(controlledId ?? "missing"),
        ).toHaveAttribute("aria-labelledby", disclosure.id);
      }
      expectConcreteTargetSurface(
        conceptRoot,
        testCase.platform,
        workDisclosureControls,
        7,
      );
      expectHeadingOrder(main("work"));
      expectUniqueIds();
      expectStatusText(
        main("work"),
        canonicalFixture.work.length *
          (testCase.module.id === "stillwater" ? 1 : 2),
        foundStatuses,
        canonicalFixture.work.length,
      );
      expect([...foundStatuses].sort()).toEqual([
        "attention",
        "complete",
        "failed",
        "running",
        "waiting",
      ]);

      await back("Back");
      await waitFor(() => expect(main("conversation")).toBeVisible());

      // Voice's continuously changing meter is not a live region; state controls
      // have exact selected state, and Close returns through a real popstate.
      fireEvent.click(screen.getByRole("button", { name: "Voice" }));
      await waitFor(() => expect(main("voice")).toBeVisible());
      const meter = screen.getByRole("progressbar", { name: "Voice level" });
      expect(meter).not.toHaveAttribute("aria-live");
      expect(meter.closest("section")).not.toHaveAttribute("aria-live");
      const listening = screen.getByRole("button", {
        name: "Set voice state: listening",
      });
      fireEvent.click(listening);
      expect(listening).toHaveAttribute("aria-pressed", "true");
      expect(screen.getAllByRole("button", { pressed: true })).toHaveLength(1);
      expect(main("voice").querySelectorAll("[aria-live]")).toHaveLength(0);
      await back("Close");
      await waitFor(() => expect(main("conversation")).toBeVisible());
      await back("Back");
      await waitFor(() => expect(main("sessions")).toBeVisible());

      // Change scenario through the real Lab UI and await its history reconciliation.
      fireEvent.click(
        within(main("sessions")).getByRole("button", {
          name: "Lab Controls",
        }),
      );
      const scenarioChanged = nextPopState();
      fireEvent.click(screen.getByRole("radio", { name: "question" }));
      await scenarioChanged;
      await waitFor(() => expect(store.getState().scenario).toBe("question"));
      expect(main("sessions")).toBeVisible();
      fireEvent.click(
        domainControl(
          main("sessions"),
          '[data-session-id="session-mobile-release"]',
        ),
      );
      await waitFor(() => expect(main("conversation")).toBeVisible());

      // Question forms/options expose exact cardinalities, labels, visible
      // descriptions, selected state, and one polite completion region per submit.
      const forms = main("conversation").querySelectorAll(
        "form[data-question-id]",
      );
      expect(forms).toHaveLength(canonicalFixture.questions.length);
      for (const question of canonicalFixture.questions) {
        const form = main("conversation").querySelector(
          `form[data-question-id="${question.id}"]`,
        );
        expect(form).toBeInstanceOf(HTMLFormElement);
        if (!(form instanceof HTMLFormElement)) continue;
        const role = question.mode === "single" ? "radio" : "checkbox";
        const controls = within(form).getAllByRole(role);
        expect(controls).toHaveLength(question.options.length);
        for (const option of question.options) {
          const control = within(form).getByRole(role, { name: option.label });
          expect(control).toHaveAccessibleName(option.label);
          expect(control).toHaveAccessibleDescription(option.detail);
          const descriptionIds =
            control.getAttribute("aria-describedby")?.trim().split(/\s+/) ?? [];
          expect(descriptionIds).toHaveLength(1);
          const description = document.getElementById(
            descriptionIds[0] ?? "missing-question-description",
          );
          expect(description).toHaveTextContent(option.detail);
          expect(
            main("conversation").querySelectorAll(
              `#${descriptionIds[0] ?? "missing-question-description"}`,
            ),
          ).toHaveLength(1);
          expect(control).not.toBeChecked();
          const label = control.closest("label");
          expect(label).toHaveTextContent(option.detail);
        }
        const selected = within(form).getByRole(role, {
          name: question.options[0]?.label,
        });
        fireEvent.click(selected);
        expect(selected).toBeChecked();
      }
      const firstQuestion = canonicalFixture.questions[0];
      expect(firstQuestion).toBeDefined();
      if (!firstQuestion) return;
      const firstForm = main("conversation").querySelector(
        `form[data-question-id="${firstQuestion.id}"]`,
      );
      expect(firstForm).toBeInstanceOf(HTMLFormElement);
      if (!(firstForm instanceof HTMLFormElement)) return;
      fireEvent.click(
        within(firstForm).getByRole("button", { name: "Submit answer" }),
      );
      const resolved = main("conversation").querySelector(
        `[data-question-id="${firstQuestion.id}"][data-question-resolution="answer"]`,
      );
      expect(resolved).toHaveAttribute("role", "status");
      expect(resolved).not.toHaveAttribute("aria-live", "assertive");
      expect(
        main("conversation").querySelectorAll('[role="status"]'),
      ).toHaveLength(1);
      expectHeadingOrder(main("conversation"));
      expectUniqueIds();
    },
    10_000,
  );

  it.each(semanticCases)(
    "$module.id on $platform applies the literal pushed-control target hook",
    async (testCase) => {
      renderHarness(testCase);
      const conceptRoot = document.querySelector("[data-concept-root]");
      expect(conceptRoot).toBeInstanceOf(HTMLElement);
      if (!(conceptRoot instanceof HTMLElement)) return;
      expectConcreteTargetSurface(conceptRoot, testCase.platform, [], 0);
      fireEvent.click(
        domainControl(
          main("sessions"),
          '[data-session-id="session-native-client"]',
        ),
      );
      await waitFor(() => expect(main("conversation")).toBeVisible());
      expectIconOnlyNames(main("conversation"), 1);
      expectIconTargetHooks(main("conversation"), testCase.platform, 1);
    },
  );

  it.each(semanticCases)(
    "$module.id on $platform dismisses both real-App dialogs by Escape and Close",
    async ({ module, platform }) => {
      const platformInput: PlatformDetectionInput = {
        userAgent: "Task 6 semantic matrix",
        allowOverride: true,
        override: platform,
        fallback: platform,
      };
      render(
        <App
          platformInput={platformInput}
          storage={persistedStorage(module)}
        />,
      );
      const routeMain = document.querySelector('main[data-route="sessions"]');
      expect(routeMain).toBeInstanceOf(HTMLElement);
      if (!(routeMain instanceof HTMLElement)) return;

      const conceptOpener = within(routeMain).getByRole("button", {
        name: "Switch concept",
      });
      conceptOpener.focus();
      fireEvent.click(conceptOpener);
      let dialog = screen.getByRole("dialog", { name: "Switch concept" });
      expectConcreteTargetSurface(
        dialog,
        platform,
        within(dialog).getAllByRole("button"),
        4,
      );
      expect(
        within(dialog).getByRole("button", {
          name: "Close concept switcher",
        }),
      ).toHaveFocus();
      let popped = nextPopState();
      fireEvent.keyDown(dialog, { key: "Escape" });
      await popped;
      await waitFor(() => expect(conceptOpener).toHaveFocus());
      expect(
        screen.queryByRole("dialog", { name: "Switch concept" }),
      ).not.toBeInTheDocument();

      fireEvent.click(conceptOpener);
      dialog = screen.getByRole("dialog", { name: "Switch concept" });
      popped = nextPopState();
      fireEvent.click(
        within(dialog).getByRole("button", {
          name: "Close concept switcher",
        }),
      );
      await popped;
      await waitFor(() => expect(conceptOpener).toHaveFocus());
      expect(
        screen.queryByRole("dialog", { name: "Switch concept" }),
      ).not.toBeInTheDocument();

      const labOpener = document.querySelector(".lab-controls-trigger");
      expect(labOpener).toBeInstanceOf(HTMLButtonElement);
      if (!(labOpener instanceof HTMLButtonElement)) return;
      labOpener.focus();
      fireEvent.click(labOpener);
      dialog = screen.getByRole("dialog", { name: "Lab Controls" });
      expectConcreteTargetSurface(
        dialog,
        platform,
        within(dialog).getAllByRole("button"),
        2,
      );
      expect(
        within(dialog).getByRole("button", { name: "Close Lab Controls" }),
      ).toHaveFocus();
      popped = nextPopState();
      fireEvent.keyDown(window, { key: "Escape" });
      await popped;
      await waitFor(() => expect(labOpener).toHaveFocus());
      expect(
        screen.queryByRole("dialog", { name: "Lab Controls" }),
      ).not.toBeInTheDocument();

      fireEvent.click(labOpener);
      dialog = screen.getByRole("dialog", { name: "Lab Controls" });
      popped = nextPopState();
      fireEvent.click(
        within(dialog).getByRole("button", { name: "Close Lab Controls" }),
      );
      await popped;
      await waitFor(() => expect(labOpener).toHaveFocus());
      expect(
        screen.queryByRole("dialog", { name: "Lab Controls" }),
      ).not.toBeInTheDocument();
    },
  );

  it("covers each concept and platform exactly once", () => {
    expect(Object.keys(conceptRegistry)).toEqual([
      "stillwater",
      "constellation",
      "field-notes",
    ]);
    expect(semanticCases).toHaveLength(6);
    expect(
      new Set(
        semanticCases.map(({ module, platform }) => `${module.id}:${platform}`),
      ),
    ).toHaveLength(semanticCases.length);
  });
});
