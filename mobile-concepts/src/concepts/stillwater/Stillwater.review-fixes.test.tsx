// @ts-expect-error Test-only source inspection runs in Node; app code omits Node ambient types.
import { readFileSync } from "node:fs";
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
import { getPlatformPrimitives } from "../../core/platform";
import { renderConcept } from "../../test/renderConcept";
import { stillwaterModule } from "./index";

const stillwaterCss = readFileSync(
  "src/concepts/stillwater/stillwater.css",
  "utf8",
);

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function requireFixtureSession(
  relation: (sessionId: string) => boolean,
): string {
  const session = canonicalFixture.sessions.find(({ id }) => relation(id));
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
    const item = canonicalFixture.transcript.find(
      (candidate) => candidate.kind === "tool",
    );
    if (!item) throw new Error("Review test needs transcript ownership");
    return { kind: "conversation", sessionId: item.sessionId };
  }
  if (name === "work") {
    const node = canonicalFixture.work[0];
    if (!node) throw new Error("Review test needs Work ownership");
    return { kind: "work", sessionId: node.sessionId };
  }
  return {
    kind: "voice",
    sessionId: requireFixtureSession(() => true),
  };
}

type ReviewRoute = RootTab | "conversation" | "work" | "voice";

type BlockingScenario = "loading" | "empty" | "error";

const blockingCases: readonly [ReviewRoute, BlockingScenario][] = [
  ["sessions", "loading"],
  ["sessions", "empty"],
  ["sessions", "error"],
  ["search", "loading"],
  ["search", "empty"],
  ["search", "error"],
  ["new", "loading"],
  ["new", "empty"],
  ["new", "error"],
  ["settings", "loading"],
  ["settings", "empty"],
  ["settings", "error"],
  ["conversation", "loading"],
  ["conversation", "error"],
  ["work", "loading"],
  ["work", "error"],
  ["voice", "loading"],
  ["voice", "error"],
];

const offlineRoutes: readonly ReviewRoute[] = [
  "sessions",
  "search",
  "new",
  "settings",
  "conversation",
  "work",
  "voice",
];

function renderRoute(
  name: ReviewRoute,
  scenario: ScenarioId,
  platform: Platform = "ios",
) {
  return renderConcept(stillwaterModule, {
    platform,
    scenario,
    route: routeFor(name),
  });
}

function requireMain(route: ReviewRoute): HTMLElement {
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
    target: { value: "Attempt a consequential offline mutation" },
  });
  return screen.getByRole("button", { name: "Start session" });
}

function parseHex(hex: string): [number, number, number] {
  const value = hex.replace("#", "");
  if (!/^[0-9a-f]{6}$/i.test(value)) throw new Error(`Invalid color ${hex}`);
  return [0, 2, 4].map((offset) =>
    Number.parseInt(value.slice(offset, offset + 2), 16),
  ) as [number, number, number];
}

function luminance(hex: string): number {
  const channels = parseHex(hex).map((channel) => {
    const normalized = channel / 255;
    return normalized <= 0.04045
      ? normalized / 12.92
      : ((normalized + 0.055) / 1.055) ** 2.4;
  });
  const [red, green, blue] = channels;
  if (red === undefined || green === undefined || blue === undefined) {
    throw new Error("Color needs three channels");
  }
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
}

function contrast(foreground: string, background: string): number {
  const first = luminance(foreground);
  const second = luminance(background);
  const lighter = Math.max(first, second);
  const darker = Math.min(first, second);
  return (lighter + 0.05) / (darker + 0.05);
}

function cssRule(selector: string): string {
  const start = stillwaterCss.indexOf(`${selector} {`);
  if (start < 0) throw new Error(`Missing CSS rule ${selector}`);
  const bodyStart = stillwaterCss.indexOf("{", start) + 1;
  const end = stillwaterCss.indexOf("}", bodyStart);
  return stillwaterCss.slice(bodyStart, end);
}

function token(rule: string, name: string): string {
  const match = rule.match(new RegExp(`--${name}:\\s*(#[0-9a-f]{6})`, "i"));
  const value = match?.[1];
  if (!value) throw new Error(`Missing token ${name}`);
  return value;
}

function setFocusedSearchQuery(result: ReturnType<typeof renderRoute>): {
  itemId: string;
  title: string;
} {
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
  return { itemId: fixtureResult.itemId, title: fixtureResult.title };
}

function replaceWorkParent(work: readonly WorkNode[]): {
  work: readonly WorkNode[];
  movedId: string;
  parentId: string;
} {
  const parent = work.find(({ kind }) => kind === "job");
  const moved = [...work].reverse().find(({ kind }) => kind === "task");
  if (!parent || !moved) throw new Error("Review test needs nonstandard nodes");
  return {
    work: work.map((node) =>
      node.id === moved.id ? { ...node, parentId: parent.id } : node,
    ),
    movedId: moved.id,
    parentId: parent.id,
  };
}

describe("Stillwater review fixes", () => {
  it.each(blockingCases)(
    "applies the %s route %s policy with stable loading layout and error Retry",
    async (route, scenario) => {
      const result = renderRoute(route, scenario);
      const main = requireMain(route);
      expect(
        main.querySelector(`[data-screen-state="${scenario}"]`),
      ).toBeVisible();
      if (scenario === "loading") {
        expect(main.querySelector(".sw-state-skeleton")).toBeVisible();
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
          expect(main.querySelector("[data-session-group-id]")).toBeVisible();
        });
      }
    },
  );

  it.each(offlineRoutes)(
    "shows age, scope, and Retry for offline %s",
    async (route) => {
      const result = renderRoute(route, "offline");
      const main = requireMain(route);
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
        expect(
          main.querySelector('[data-offline-policy="read-only"]'),
        ).toBeNull();
        expect(main.querySelector("[data-session-group-id]")).toBeVisible();
      });
    },
  );

  it("guards consequential offline mutations while local settings remain usable", () => {
    const newSession = renderRoute("new", "offline");
    const start = fillOfflineNewSession(newSession);
    expect(start).toBeDisabled();
    expect(requireMain("new")).toHaveTextContent(/cannot start.*offline/i);

    cleanup();
    renderRoute("conversation", "offline");
    let main = requireMain("conversation");
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
    if (!questionItem)
      throw new Error("Review test needs a question transcript");
    const question = renderConcept(stillwaterModule, {
      platform: "ios",
      scenario: "offline",
      route: { kind: "conversation", sessionId: questionItem.sessionId },
    });
    main = requireMain("conversation");
    const form = main.querySelector("form[data-question-id]");
    if (!(form instanceof HTMLElement))
      throw new Error("Missing question form");
    for (const control of within(form).getAllByRole("button")) {
      expect(control).toBeDisabled();
    }
    for (const option of within(form).getAllByRole("radio")) {
      expect(option).toBeDisabled();
    }
    expect(question.store.getState().answers).toEqual({});

    cleanup();
    const settings = renderRoute("settings", "offline");
    const appearance = screen.getByRole("combobox", { name: "Appearance" });
    expect(appearance).toBeEnabled();
    fireEvent.change(appearance, { target: { value: "dark" } });
    expect(settings.store.getState().appearance).toBe("dark");
  });

  it.each(["ios", "android"] as const)(
    "uses functional %s root navigation and primitive-driven title, Work, elevation, and feedback hooks",
    async (platform) => {
      const result = renderRoute("sessions", "baseline", platform);
      const primitives = getPlatformPrimitives(platform);
      const root = document.querySelector("[data-concept-root]");
      expect(root).toHaveAttribute("data-title", primitives.title);
      expect(root).toHaveAttribute("data-sheet", primitives.sheet);
      expect(root).toHaveAttribute("data-elevation", primitives.elevation);
      expect(root).toHaveAttribute("data-feedback", primitives.feedback);
      const nav = screen.getByRole("navigation", { name: "Primary" });
      for (const [tab, label] of [
        ["search", "Search"],
        ["new", "New Session"],
        ["settings", "Settings"],
        ["sessions", "Sessions"],
      ] as const) {
        fireEvent.click(within(nav).getByRole("button", { name: label }));
        expect(result.store.getState().route).toEqual({ kind: "root", tab });
      }

      cleanup();
      renderRoute("work", "baseline", platform);
      const panel = requireMain("work").querySelector(
        "[data-work-presentation]",
      );
      expect(panel).toHaveAttribute("data-work-presentation", primitives.sheet);
      expect(stillwaterCss).toContain(`[data-title="${primitives.title}"]`);
      expect(stillwaterCss).toContain(
        `[data-work-presentation="${primitives.sheet}"]`,
      );
      expect(stillwaterCss).toContain(
        `[data-elevation="${primitives.elevation}"]`,
      );
      expect(stillwaterCss).toContain(
        `[data-feedback="${primitives.feedback}"]`,
      );
      expect(stillwaterCss).toMatch(
        new RegExp(
          `data-feedback=.[^\\]]*${primitives.feedback}[^\\]]*.].*:active`,
        ),
      );
      await Promise.resolve();
    },
  );

  it("focuses and scrolls a focused transcript result with motion-aware behavior", async () => {
    const smooth = renderRoute("search", "baseline");
    let main = requireMain("search");
    const smoothScroll = vi.fn();
    Object.defineProperty(main, "scrollTo", {
      configurable: true,
      value: smoothScroll,
    });
    const focused = setFocusedSearchQuery(smooth);
    await waitFor(() => {
      const target = document.querySelector(
        `[data-transcript-item-id="${focused.itemId}"]`,
      );
      expect(target).toHaveAttribute("tabindex", "-1");
      expect(target).toHaveFocus();
      expect(smoothScroll).toHaveBeenCalledWith(
        expect.objectContaining({ behavior: "smooth" }),
      );
    });

    cleanup();
    const reduced = renderRoute("search", "baseline");
    reduced.store.getState().dispatch({
      type: "setReducedMotion",
      reducedMotion: true,
    });
    main = requireMain("search");
    const reducedScroll = vi.fn();
    Object.defineProperty(main, "scrollTo", {
      configurable: true,
      value: reducedScroll,
    });
    setFocusedSearchQuery(reduced);
    await waitFor(() =>
      expect(reducedScroll).toHaveBeenCalledWith(
        expect.objectContaining({ behavior: "auto" }),
      ),
    );

    cleanup();
    const previousEffectiveMotion =
      document.documentElement.dataset.reducedMotion;
    document.documentElement.dataset.reducedMotion = "true";
    try {
      const systemReduced = renderRoute("search", "baseline");
      expect(systemReduced.store.getState().reducedMotion).toBe(false);
      main = requireMain("search");
      const systemReducedScroll = vi.fn();
      Object.defineProperty(main, "scrollTo", {
        configurable: true,
        value: systemReducedScroll,
      });
      setFocusedSearchQuery(systemReduced);
      await waitFor(() =>
        expect(systemReducedScroll).toHaveBeenCalledWith(
          expect.objectContaining({ behavior: "auto" }),
        ),
      );
    } finally {
      if (previousEffectiveMotion === undefined) {
        delete document.documentElement.dataset.reducedMotion;
      } else {
        document.documentElement.dataset.reducedMotion =
          previousEffectiveMotion;
      }
    }
  });

  it("uses visual viewport height, independent narrow safe areas, and wrapping titles", () => {
    const rootRule = cssRule(".concept-stillwater");
    expect(rootRule).toContain("height: var(--visual-viewport-height, 100dvh)");
    expect(rootRule).not.toMatch(/min-height:/);
    expect(stillwaterCss).toMatch(
      /padding-left:\s*max\([^;]*var\(--safe-area-left\)/,
    );
    expect(stillwaterCss).toMatch(
      /padding-right:\s*max\([^;]*var\(--safe-area-right\)/,
    );
    for (const selector of [
      ".sw-title-block h1",
      ".sw-session-row__copy strong",
    ]) {
      const rule = cssRule(selector);
      expect(rule).not.toContain("white-space: nowrap");
      expect(rule).not.toContain("text-overflow: ellipsis");
      expect(rule).toMatch(/overflow-wrap|white-space:\s*normal/);
    }

    const longItem = canonicalFixture.transcript.find(
      (item) => item.kind === "assistant" && item.body.length > 300,
    );
    if (longItem?.kind !== "assistant") {
      throw new Error("Review test needs long assistant content");
    }
    renderConcept(stillwaterModule, {
      platform: "ios",
      scenario: "long-content",
      route: { kind: "conversation", sessionId: longItem.sessionId },
    });
    expect(screen.getByText(longItem.body)).toBeVisible();
    expect(screen.getByRole("heading", { level: 1 })).toBeVisible();
  });

  it("keeps muted text at 4.5:1 and boundaries at 3:1 in light and dark tokens", () => {
    const light = cssRule(".concept-stillwater");
    const dark = cssRule(
      '.concept-stillwater[data-appearance="dark"],\n:root[data-appearance="dark"] .concept-stillwater',
    );
    for (const rule of [light, dark]) {
      const background = token(rule, "sw-bg");
      const surface = token(rule, "sw-surface");
      const muted = token(rule, "sw-muted");
      const line = token(rule, "sw-control-line");
      expect(contrast(muted, background)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(muted, surface)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(line, background)).toBeGreaterThanOrEqual(3);
      expect(contrast(line, surface)).toBeGreaterThanOrEqual(3);
    }
  });

  it("reports the displayed voice level after Mute and Stop", () => {
    renderRoute("voice", "voice");
    const main = requireMain("voice");
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

  it("builds Work as nested semantic lists from parentId rather than kind", async () => {
    const result = renderRoute("work", "baseline");
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
      expect(moved).toHaveTextContent(/level 4/i);
      expect(moved).toHaveTextContent(/parent:/i);
      expect(
        moved?.closest("li")?.parentElement?.closest("li"),
      ).toContainElement(parent as HTMLElement);
    });
    expect(
      requireMain("work").querySelectorAll("ul[aria-label]").length,
    ).toBeGreaterThan(0);
  });
});
