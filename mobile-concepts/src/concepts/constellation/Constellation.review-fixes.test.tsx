import {
  cleanup,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { canonicalFixture } from "../../core/fixtures";
import type {
  Platform,
  RootTab,
  Route,
  ScenarioId,
  WorkNode,
} from "../../core/model";
import { renderConcept } from "../../test/renderConcept";
import { constellationModule } from "./index";

type ReviewRoute = RootTab | "conversation" | "work" | "voice";
type BlockingScenario = "loading" | "empty" | "error";

const reviewRoutes: readonly ReviewRoute[] = [
  "sessions",
  "search",
  "new",
  "settings",
  "conversation",
  "work",
  "voice",
];

const blockingCases: readonly (readonly [ReviewRoute, BlockingScenario])[] = [
  ...(["sessions", "search", "new", "settings"] as const).flatMap((route) =>
    (["loading", "empty", "error"] as const).map(
      (scenario) => [route, scenario] as const,
    ),
  ),
  ...(["conversation", "work", "voice"] as const).flatMap((route) =>
    (["loading", "error"] as const).map(
      (scenario) => [route, scenario] as const,
    ),
  ),
];

function requireSession(predicate: (sessionId: string) => boolean): string {
  const session = canonicalFixture.sessions.find(({ id }) => predicate(id));
  if (!session) throw new Error("Review test needs a fixture-owned session");
  return session.id;
}

function routeFor(name: ReviewRoute): Route {
  if (
    name === "sessions" ||
    name === "search" ||
    name === "new" ||
    name === "settings"
  ) {
    return { kind: "root", tab: name };
  }
  if (name === "conversation") {
    const tool = canonicalFixture.transcript.find(
      (item) => item.kind === "tool",
    );
    if (!tool) throw new Error("Review test needs a tool transcript");
    return { kind: "conversation", sessionId: tool.sessionId };
  }
  if (name === "work") {
    const node = canonicalFixture.work[0];
    if (!node) throw new Error("Review test needs Work ownership");
    return { kind: "work", sessionId: node.sessionId };
  }
  return {
    kind: "voice",
    sessionId: requireSession(() => true),
  };
}

function renderRoute(
  name: ReviewRoute,
  scenario: ScenarioId = "baseline",
  platform: Platform = "ios",
) {
  return renderConcept(constellationModule, {
    platform,
    scenario,
    route: routeFor(name),
  });
}

function mainFor(route: ReviewRoute): HTMLElement {
  const main = screen.getByRole("main");
  expect(main).toHaveAttribute("data-route", route);
  return main;
}

function fillOfflineNewSession(
  result: ReturnType<typeof renderRoute>,
): HTMLButtonElement {
  const project = result.store.getState().projection.fixture.recentProjects[0];
  if (!project) throw new Error("Review test needs a recent project");
  fireEvent.change(screen.getByRole("textbox", { name: "Project path" }), {
    target: { value: project.path },
  });
  fireEvent.change(screen.getByRole("textbox", { name: "Prompt" }), {
    target: { value: "Attempt an offline mutation" },
  });
  return screen.getByRole("button", { name: "Start session" });
}

function setFocusedSearchQuery(result: ReturnType<typeof renderRoute>) {
  const fixtureResult = result.store
    .getState()
    .projection.fixture.search.find(({ itemId }) => itemId !== null);
  if (!fixtureResult?.itemId)
    throw new Error("Review test needs focused search");
  fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), {
    target: { value: fixtureResult.title },
  });
  fireEvent.click(
    document.querySelector(
      `[data-search-result-id="${fixtureResult.id}"]`,
    ) as HTMLElement,
  );
  return fixtureResult.itemId;
}

function replaceWorkParent(work: readonly WorkNode[]) {
  const parent = work.find(({ kind }) => kind === "job");
  const moved = [...work].reverse().find(({ kind }) => kind === "task");
  if (!parent || !moved)
    throw new Error("Review test needs non-kind hierarchy");
  return {
    work: work.map((node) =>
      node.id === moved.id ? { ...node, parentId: parent.id } : node,
    ),
    movedId: moved.id,
    parentId: parent.id,
  };
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  delete document.documentElement.dataset.reducedMotion;
  delete document.documentElement.dataset.forcedColors;
});

describe("Constellation review fixes", () => {
  it.each(["ios", "android"] as const)(
    "gives the %s Back and Close actions primitive-driven square target dimensions",
    (platform) => {
      const target = platform === "ios" ? "44px" : "48px";
      renderRoute("conversation", "baseline", platform);
      let iconAction = within(mainFor("conversation")).getByRole("button", {
        name: "Back",
      });
      expect(iconAction.style.minWidth).toBe(target);
      expect(iconAction.style.width).toBe(target);
      expect(iconAction.style.minHeight).toBe(target);
      expect(iconAction.style.height).toBe(target);
      expect(iconAction).toHaveAttribute("data-icon-target", target);

      cleanup();
      renderRoute("voice", "voice", platform);
      iconAction = within(mainFor("voice")).getByRole("button", {
        name: "Close",
      });
      expect(iconAction.style.minWidth).toBe(target);
      expect(iconAction.style.width).toBe(target);
      expect(iconAction.style.minHeight).toBe(target);
      expect(iconAction.style.height).toBe(target);
      expect(iconAction).toHaveAttribute("data-icon-target", target);
    },
  );

  it.each([
    ["default", false, "standard", false],
    ["reduced motion", true, "standard", false],
    ["accessibility text", false, "accessibility", false],
    ["forced colors", false, "standard", true],
  ] as const)(
    "keeps collapsed current Work and tool state visible with text and shape under %s",
    async (_label, reducedMotion, textScale, forcedColors) => {
      if (forcedColors)
        document.documentElement.dataset.forcedColors = "active";
      const work = renderRoute("work");
      work.store.getState().dispatch({
        type: "setReducedMotion",
        reducedMotion,
      });
      work.store.getState().dispatch({ type: "setTextScale", textScale });
      const runningNode = canonicalFixture.work.find(
        ({ state }) => state === "running",
      );
      if (!runningNode) throw new Error("Review test needs current Work");
      await waitFor(() => {
        const article = document.querySelector(
          `[data-work-node-id="${runningNode.id}"]`,
        );
        const summary = within(article as HTMLElement).getByRole("button", {
          name: new RegExp(runningNode.title, "i"),
        });
        const status = summary.querySelector('[data-status-state="running"]');
        expect(status).toBeVisible();
        expect(status).toHaveTextContent("Running");
        expect(
          status?.querySelector('svg[aria-hidden="true"]'),
        ).toBeInTheDocument();
      });

      cleanup();
      const conversation = renderRoute("conversation");
      conversation.store.getState().dispatch({
        type: "setReducedMotion",
        reducedMotion,
      });
      conversation.store
        .getState()
        .dispatch({ type: "setTextScale", textScale });
      const tool = canonicalFixture.transcript.find(
        (item) => item.kind === "tool",
      );
      if (tool?.kind !== "tool") throw new Error("Review test needs a tool");
      await waitFor(() => {
        const summary = within(mainFor("conversation")).getByRole("button", {
          name: new RegExp(tool.label, "i"),
        });
        const status = summary.querySelector("[data-status-state]");
        expect(status).toBeVisible();
        expect(status).toHaveTextContent(/completed|failed|running|waiting/i);
        expect(
          status?.querySelector('svg[aria-hidden="true"]'),
        ).toBeInTheDocument();
      });
    },
  );

  it.each(blockingCases)(
    "uses controller recovery for %s in the %s scenario",
    async (route, scenario) => {
      const result = renderRoute(route, scenario);
      const main = mainFor(route);
      expect(
        main.querySelector(`[data-screen-state="${scenario}"]`),
      ).toBeVisible();
      if (scenario === "loading") {
        expect(main.querySelector(".co-state-skeleton")).toBeVisible();
      }
      if (scenario === "error") {
        fireEvent.click(within(main).getByRole("button", { name: "Retry" }));
        await waitFor(() => {
          expect(result.store.getState()).toMatchObject({
            scenario: "baseline",
            route: { kind: "root", tab: "sessions" },
            history: [],
          });
          expect(main).toHaveAttribute("data-route", "sessions");
          expect(main.querySelector('[data-screen-state="error"]')).toBeNull();
        });
      }
    },
  );

  it.each(reviewRoutes)(
    "shows offline age, scope, and controller Retry for %s",
    async (route) => {
      const result = renderRoute(route, "offline");
      const main = mainFor(route);
      const policy = main.querySelector('[data-offline-policy="read-only"]');
      expect(policy).toBeVisible();
      expect(policy).toHaveTextContent(/last synced/i);
      expect(policy).toHaveTextContent(/read-only/i);
      fireEvent.click(
        within(policy as HTMLElement).getByRole("button", { name: "Retry" }),
      );
      await waitFor(() => {
        expect(result.store.getState()).toMatchObject({
          scenario: "baseline",
          route: { kind: "root", tab: "sessions" },
          history: [],
        });
        expect(main).toHaveAttribute("data-route", "sessions");
      });
    },
  );

  it("keeps offline domain controls read-only while local settings remain usable", () => {
    const newSession = renderRoute("new", "offline");
    expect(fillOfflineNewSession(newSession)).toBeDisabled();
    expect(mainFor("new")).toHaveTextContent(/cannot start.*offline/i);

    cleanup();
    renderRoute("conversation", "offline");
    let main = mainFor("conversation");
    expect(
      within(main).getByRole("textbox", { name: "Message" }),
    ).toBeDisabled();
    expect(
      within(main).getByRole("button", { name: "Submit message" }),
    ).toBeDisabled();
    expect(main).toHaveTextContent(/cannot send.*offline/i);

    cleanup();
    const questionItem = canonicalFixture.transcript.find(
      (item) => item.kind === "question",
    );
    if (!questionItem) throw new Error("Review test needs a question");
    renderConcept(constellationModule, {
      platform: "ios",
      scenario: "offline",
      route: { kind: "conversation", sessionId: questionItem.sessionId },
    });
    main = mainFor("conversation");
    const form = main.querySelector("form[data-question-id]");
    if (!(form instanceof HTMLElement))
      throw new Error("Missing question form");
    for (const control of within(form).getAllByRole("button")) {
      expect(control).toBeDisabled();
    }
    for (const option of within(form).getAllByRole("radio")) {
      expect(option).toBeDisabled();
    }

    cleanup();
    renderRoute("voice", "offline");
    main = mainFor("voice");
    for (const control of within(main).getAllByRole("button", {
      name: /set voice state:/i,
    })) {
      expect(control).toBeDisabled();
    }
    for (const name of ["Advance voice state", "Mute", "Stop", "End"]) {
      expect(within(main).getByRole("button", { name })).toBeDisabled();
    }

    cleanup();
    const settings = renderRoute("settings", "offline");
    const appearance = screen.getByRole("combobox", { name: "Appearance" });
    expect(appearance).toBeEnabled();
    fireEvent.change(appearance, { target: { value: "dark" } });
    expect(settings.store.getState().appearance).toBe("dark");
  });

  it("uses OS-only reduced motion for focused Search scrolling", async () => {
    document.documentElement.dataset.reducedMotion = "true";
    const result = renderRoute("search");
    expect(result.store.getState().reducedMotion).toBe(false);
    const main = mainFor("search");
    const scrollTo = vi.fn();
    Object.defineProperty(main, "scrollTo", {
      configurable: true,
      value: scrollTo,
    });
    const itemId = setFocusedSearchQuery(result);
    await waitFor(() => {
      expect(
        document.querySelector(`[data-transcript-item-id="${itemId}"]`),
      ).toHaveFocus();
      expect(scrollTo).toHaveBeenCalledWith(
        expect.objectContaining({ behavior: "auto" }),
      );
    });
  });

  it("builds hierarchy from a mutated parentId rather than node kind", async () => {
    const result = renderRoute("work");
    const replacement = replaceWorkParent(
      result.store.getState().projection.fixture.work,
    );
    result.store.setState((state) => ({
      ...state,
      projection: {
        ...state.projection,
        fixture: { ...state.projection.fixture, work: replacement.work },
      },
    }));
    await waitFor(() => {
      const moved = document.querySelector(
        `[data-work-node-id="${replacement.movedId}"]`,
      );
      const parent = document.querySelector(
        `[data-work-node-id="${replacement.parentId}"]`,
      );
      expect(moved).toHaveAttribute("data-work-depth", "3");
      expect(moved).toHaveTextContent(/level 4 · parent:/i);
      expect(
        moved?.closest("li")?.parentElement?.closest("li"),
      ).toContainElement(parent as HTMLElement);
    });
  });

  it("reports zero displayed Voice level after Mute and Stop", () => {
    renderRoute("voice", "voice");
    const main = mainFor("voice");
    fireEvent.click(
      within(main).getByRole("button", { name: "Advance voice state" }),
    );
    const meter = within(main).getByRole("progressbar", {
      name: "Voice level",
    });
    expect(Number(meter.getAttribute("aria-valuenow"))).toBeGreaterThan(0);
    fireEvent.click(within(main).getByRole("button", { name: "Mute" }));
    expect(meter).toHaveAttribute("aria-valuenow", "0");
    fireEvent.click(within(main).getByRole("button", { name: "Stop" }));
    expect(meter).toHaveAttribute("aria-valuenow", "0");
  });

  it.each(["ios", "android"] as const)(
    "maps %s safe-area primitive to independent consumed edge variables",
    (platform) => {
      renderRoute("sessions", "baseline", platform);
      const root = document.querySelector<HTMLElement>("[data-concept-root]");
      if (!root) throw new Error("Missing concept root");
      const prefix =
        platform === "ios" ? "env(safe-area-inset-" : "var(--safe-area-";
      for (const edge of ["top", "right", "bottom", "left"] as const) {
        const value = root.style.getPropertyValue(`--co-edge-${edge}`);
        expect(value).toContain(`${prefix}${edge}`);
      }
      expect(
        new Set([
          root.style.getPropertyValue("--co-edge-top"),
          root.style.getPropertyValue("--co-edge-right"),
          root.style.getPropertyValue("--co-edge-bottom"),
          root.style.getPropertyValue("--co-edge-left"),
        ]).size,
      ).toBe(4);

      const topbar = document.querySelector<HTMLElement>(".co-topbar");
      const content = document.querySelector<HTMLElement>(".co-route-content");
      const nav = screen.getByRole("navigation", { name: "Primary" });
      expect(topbar?.style.paddingTop).toContain("var(--co-edge-top)");
      expect(topbar?.style.paddingRight).toContain("var(--co-edge-right)");
      expect(topbar?.style.paddingLeft).toContain("var(--co-edge-left)");
      expect(content?.style.paddingRight).toContain("var(--co-edge-right)");
      expect(content?.style.paddingLeft).toContain("var(--co-edge-left)");
      expect(nav.style.paddingRight).toContain("var(--co-edge-right)");
      expect(nav.style.paddingBottom).toContain("var(--co-edge-bottom)");
      expect(nav.style.paddingLeft).toContain("var(--co-edge-left)");
      for (const declaration of [
        topbar?.getAttribute("style"),
        content?.getAttribute("style"),
        nav.getAttribute("style"),
      ]) {
        expect(declaration).not.toMatch(/var\(--safe-area-/);
      }
    },
  );
});
