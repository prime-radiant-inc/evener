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
import { getPlatformPrimitives } from "../core/platform";
import {
  createPrototypeStore,
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
    const timeout = window.setTimeout(() => {
      window.removeEventListener("popstate", onPopState);
      reject(new Error("timed out awaiting semantic-contract popstate"));
    }, 1_000);
    const onPopState = (event: PopStateEvent) => {
      window.clearTimeout(timeout);
      window.removeEventListener("popstate", onPopState);
      resolve(event);
    };
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

function expectIconOnlyNames(scope: ParentNode = document): void {
  const iconOnly = Array.from(
    scope.querySelectorAll<HTMLButtonElement>("button"),
  )
    .filter((button) => button.querySelector("svg") !== null)
    .filter((button) => button.textContent?.trim() === "");
  for (const button of iconOnly) expect(button).toHaveAccessibleName();
}

function expectTargetHooks(
  platform: Platform,
  scope: ParentNode = document,
): void {
  const minimumTarget = String(getPlatformPrimitives(platform).minimumTarget);
  const surfaces = [
    ...(scope instanceof Element && scope.matches("[data-minimum-target]")
      ? [scope]
      : []),
    ...scope.querySelectorAll("[data-minimum-target]"),
  ];
  expect(surfaces.length).toBeGreaterThan(0);
  for (const surface of surfaces) {
    expect(surface).toHaveAttribute("data-minimum-target", minimumTarget);
  }
  for (const target of scope.querySelectorAll("[data-icon-target]")) {
    expect(target).toHaveAttribute("data-icon-target", minimumTarget);
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

beforeEach(() => window.history.replaceState(null, "", "/"));
afterEach(() => {
  cleanup();
  for (const controller of controllers.splice(0)) controller.dispose();
  vi.restoreAllMocks();
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
      expectIconOnlyNames();
      expectTargetHooks(testCase.platform);
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
      expectTargetHooks(testCase.platform, conceptDialog);
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
      expectTargetHooks(testCase.platform, labDialog);
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
      expectIconOnlyNames(main("conversation"));
      const sessionTools = canonicalFixture.transcript.filter(
        (item) =>
          item.sessionId === "session-native-client" && item.kind === "tool",
      );
      expect(sessionTools).toHaveLength(2);
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
        const controlledId = disclosure.getAttribute("aria-controls");
        expect(controlledId).toBeTruthy();
        expect(disclosure).toHaveAttribute("aria-expanded", "false");
        fireEvent.click(disclosure);
        expect(disclosure).toHaveAttribute("aria-expanded", "true");
        expect(
          document.getElementById(controlledId ?? "missing"),
        ).toHaveAttribute("aria-labelledby", disclosure.id);
      }
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

  it("covers each concept and platform exactly once", () => {
    expect(semanticCases).toHaveLength(Object.keys(conceptRegistry).length * 2);
    expect(
      new Set(
        semanticCases.map(({ module, platform }) => `${module.id}:${platform}`),
      ),
    ).toHaveLength(semanticCases.length);
  });
});
