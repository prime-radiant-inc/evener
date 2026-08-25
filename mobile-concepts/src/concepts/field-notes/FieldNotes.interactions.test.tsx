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
import type { Platform, Route, ScenarioId, WorkNode } from "../../core/model";
import { getPlatformPrimitives } from "../../core/platform";
import { renderConcept } from "../../test/renderConcept";
import { fieldNotesModule } from "./index";

const fieldNotesCss = readFileSync(
  "src/concepts/field-notes/field-notes.css",
  "utf8",
);

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  delete document.documentElement.dataset.reducedMotion;
});

function conversationRoute(scenario: ScenarioId = "baseline"): Route {
  const item =
    scenario === "long-content"
      ? canonicalFixture.transcript.find(
          (candidate) =>
            candidate.kind === "tool" && candidate.output.length > 80,
        )
      : canonicalFixture.transcript.find(
          (candidate) => candidate.kind === "tool",
        );
  if (!item) throw new Error("Field Notes test needs a tool transcript");
  return { kind: "conversation", sessionId: item.sessionId };
}

function workRoute(): Route {
  const node = canonicalFixture.work[0];
  if (!node) throw new Error("Field Notes test needs Work fixtures");
  return { kind: "work", sessionId: node.sessionId };
}

function renderRoute(
  route: Route,
  platform: Platform = "ios",
  scenario: ScenarioId = "baseline",
) {
  return renderConcept(fieldNotesModule, { platform, scenario, route });
}

function mainFor(route: string): HTMLElement {
  const main = screen.getByRole("main");
  expect(main).toHaveAttribute("data-route", route);
  return main;
}

function replaceWorkParent(work: readonly WorkNode[]) {
  const parent = work.find(({ kind }) => kind === "job");
  const moved = [...work].reverse().find(({ kind }) => kind === "task");
  if (!parent || !moved) throw new Error("Field Notes test needs nested Work");
  return {
    work: work.map((node) =>
      node.id === moved.id ? { ...node, parentId: parent.id } : node,
    ),
    movedId: moved.id,
    parentId: parent.id,
  };
}

describe("Field Notes transcript studio", () => {
  it("keeps the canonical transcript in chronological DOM order", () => {
    const route = conversationRoute();
    renderRoute(route);
    const main = mainFor("conversation");
    if (route.kind !== "conversation") throw new Error("Expected conversation");
    const expected = canonicalFixture.transcript
      .filter(({ sessionId }) => sessionId === route.sessionId)
      .map(({ id }) => id);
    const actual = [...main.querySelectorAll("[data-transcript-item-id]")].map(
      (item) => item.getAttribute("data-transcript-item-id"),
    );
    expect(actual).toEqual(expected);
    expect(main.querySelector("[data-chronology-rail]")).toBeVisible();
  });

  it.each([
    [{ kind: "root", tab: "sessions" } as const, "sessions"],
    [conversationRoute(), "conversation"],
    [workRoute(), "work"],
  ])("forms one coherent heading outline on %s", (route, routeName) => {
    renderRoute(route);
    const main = mainFor(routeName);
    const headings = within(main).getAllByRole("heading");
    const levels = headings.map((heading) => Number(heading.tagName.slice(1)));
    expect(levels[0]).toBe(1);
    for (const [index, level] of levels.entries()) {
      if (index === 0) continue;
      const previous = levels[index - 1];
      if (previous === undefined) throw new Error("Missing previous heading");
      expect(level).toBeLessThanOrEqual(previous + 1);
    }
    expect(main.querySelectorAll("h1")).toHaveLength(1);
  });

  it("uses parentId lists and visible ledger annotations for Work nesting", () => {
    const route = workRoute();
    renderRoute(route);
    const main = mainFor("work");
    if (route.kind !== "work") throw new Error("Expected Work route");
    const nodes = canonicalFixture.work.filter(
      ({ sessionId }) => sessionId === route.sessionId,
    );
    for (const node of nodes) {
      const article = main.querySelector(`[data-work-node-id="${node.id}"]`);
      expect(article).toBeVisible();
      expect(article).toHaveAttribute(
        "data-work-parent-id",
        node.parentId ?? "root",
      );
      expect(article?.querySelector("[data-ledger-annotation]")).toBeVisible();
      if (node.parentId) {
        const parent = main.querySelector(
          `[data-work-node-id="${node.parentId}"]`,
        );
        expect(
          article?.closest("li")?.parentElement?.closest("li"),
        ).toContainElement(parent as HTMLElement);
      }
    }
    expect(main.querySelector("ul[data-work-ledger]")).toBeVisible();
  });

  it("rebuilds the ledger from a changed parentId rather than node kind", async () => {
    const route = workRoute();
    const result = renderRoute(route);
    if (route.kind !== "work") throw new Error("Expected Work route");
    const fixture = result.store.getState().projection.fixture;
    const owned = fixture.work.filter(
      ({ sessionId }) => sessionId === route.sessionId,
    );
    const replacement = replaceWorkParent(owned);
    const ownedIds = new Set(owned.map(({ id }) => id));
    result.store.setState((state) => ({
      ...state,
      projection: {
        ...state.projection,
        fixture: {
          ...state.projection.fixture,
          work: [
            ...state.projection.fixture.work.filter(
              ({ id }) => !ownedIds.has(id),
            ),
            ...replacement.work,
          ],
        },
      },
    }));
    await waitFor(() => {
      const moved = document.querySelector(
        `[data-work-node-id="${replacement.movedId}"]`,
      );
      const parent = document.querySelector(
        `[data-work-node-id="${replacement.parentId}"]`,
      );
      expect(moved).toHaveAttribute(
        "data-work-parent-id",
        replacement.parentId,
      );
      expect(
        moved?.closest("li")?.parentElement?.closest("li"),
      ).toContainElement(parent as HTMLElement);
    });
  });

  it("uses Android sans body roles while preserving editorial hierarchy", () => {
    renderRoute(conversationRoute(), "android");
    const root = document.querySelector("[data-concept-root]");
    expect(root).toHaveAttribute("data-platform", "android");
    expect(root).toHaveAttribute("data-reading-role", "editorial-sans");
    expect(
      mainFor("conversation").querySelector("[data-editorial-reading]"),
    ).toBeVisible();
    expect(fieldNotesCss).toContain('[data-platform="android"] .fn-editorial');
    expect(fieldNotesCss).toMatch(/Roboto[^;]*system-ui/);
    expect(fieldNotesCss).toContain("letter-spacing");
  });

  it("keeps active and attention states immediate rather than archival", () => {
    renderRoute({ kind: "root", tab: "sessions" }, "ios", "needs-attention");
    const main = mainFor("sessions");
    const attention = main.querySelector('[data-attention-signal="true"]');
    expect(attention).toBeVisible();
    expect(attention).toHaveTextContent(/needs (you|attention|decision)/i);
    expect(attention).toHaveAttribute("aria-live", "polite");
    const current = main.querySelector('[data-current-state="true"]');
    expect(current).toBeVisible();
    expect(current).toHaveTextContent(/running|waiting|needs/i);
  });

  it("bounds long tool output and gives only its code block internal scrolling", () => {
    const route = conversationRoute("long-content");
    renderRoute(route, "ios", "long-content");
    const main = mainFor("conversation");
    const tool = canonicalFixture.transcript.find(
      (item) =>
        item.kind === "tool" &&
        route.kind === "conversation" &&
        item.sessionId === route.sessionId,
    );
    if (tool?.kind !== "tool") throw new Error("Expected long tool output");
    const disclosure = within(main).getByRole("button", { name: tool.label });
    fireEvent.click(disclosure);
    const output = main.querySelector("[data-long-tool-output]");
    expect(output).toBeVisible();
    expect(output?.querySelector("pre")?.textContent).toBe(tool.output);
    expect(fieldNotesCss).toMatch(
      /\.fn-tool-output\s*\{[^}]*max-height:[^;}]+;[^}]*overflow:\s*auto/s,
    );
    expect(fieldNotesCss).toMatch(
      /\.fn-scroll-owner\s*\{[^}]*overflow-y:\s*auto/s,
    );
    expect(fieldNotesCss).not.toMatch(
      /\.fn-transcript-item\s*\{[^}]*overflow[^}]*auto/s,
    );
  });

  it.each(["simulated", "os"] as const)(
    "focuses a found passage and uses auto scrolling under %s reduced motion",
    async (motionSource) => {
      const result = renderRoute({ kind: "root", tab: "search" });
      const main = mainFor("search");
      const scrollTo = vi.fn();
      Object.defineProperty(main, "scrollTo", {
        configurable: true,
        value: scrollTo,
      });
      if (motionSource === "simulated") {
        result.store
          .getState()
          .dispatch({ type: "setReducedMotion", reducedMotion: true });
      } else {
        document.documentElement.dataset.reducedMotion = "true";
      }
      const fixtureResult = canonicalFixture.search.find(
        ({ itemId }) => itemId !== null,
      );
      if (!fixtureResult?.itemId)
        throw new Error("Field Notes test needs a transcript search result");
      fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), {
        target: { value: fixtureResult.title },
      });
      fireEvent.click(
        document.querySelector(
          `[data-search-result-id="${fixtureResult.id}"]`,
        ) as HTMLElement,
      );
      await waitFor(() => {
        expect(
          document.querySelector(
            `[data-transcript-item-id="${fixtureResult.itemId}"]`,
          ),
        ).toHaveFocus();
        expect(scrollTo).toHaveBeenCalledWith(
          expect.objectContaining({ behavior: "auto" }),
        );
      });
    },
  );

  it("keeps collapsed Work and tool states visible and described", () => {
    renderRoute(workRoute());
    const runningNode = canonicalFixture.work.find(
      ({ state }) => state === "running",
    );
    if (!runningNode) throw new Error("Field Notes test needs current Work");
    const workControl = within(
      document.querySelector(
        `[data-work-node-id="${runningNode.id}"]`,
      ) as HTMLElement,
    ).getByRole("button", {
      name: runningNode.title,
      description: "Running",
    });
    expect(workControl).toHaveAttribute("aria-expanded", "false");
    expect(
      workControl.querySelector('[data-status-state="running"]'),
    ).toBeVisible();

    cleanup();
    renderRoute(conversationRoute());
    const tool = canonicalFixture.transcript.find(
      (item) => item.kind === "tool" && item.status === "completed",
    );
    if (tool?.kind !== "tool") throw new Error("Field Notes test needs a tool");
    const toolControl = within(
      document.querySelector(
        `[data-transcript-item-id="${tool.id}"]`,
      ) as HTMLElement,
    ).getByRole("button", { name: tool.label, description: "Completed" });
    expect(toolControl).toHaveAttribute("aria-expanded", "false");
    expect(
      toolControl.querySelector('[data-status-state="complete"]'),
    ).toBeVisible();
  });

  it("shows offline policy and keeps domain mutations blocked without local state", () => {
    const result = renderRoute({ kind: "root", tab: "new" }, "ios", "offline");
    const main = mainFor("new");
    const policy = main.querySelector('[data-offline-policy="read-only"]');
    expect(policy).toBeVisible();
    expect(
      within(policy as HTMLElement).getByRole("button", { name: "Retry" }),
    ).toBeVisible();
    const project =
      result.store.getState().projection.fixture.recentProjects[0];
    if (!project) throw new Error("Field Notes test needs a project");
    fireEvent.change(screen.getByRole("textbox", { name: "Project path" }), {
      target: { value: project.path },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Prompt" }), {
      target: { value: "Attempt an offline mutation" },
    });
    expect(
      screen.getByRole("button", { name: "Start session" }),
    ).toBeDisabled();
    const before = result.store.getState().newSession;
    result.store.getState().dispatch({ type: "submitNewSession" });
    expect(result.store.getState().newSession).toEqual(before);
  });

  it("reports zero Voice level after mute and stop", () => {
    const session = canonicalFixture.sessions[0];
    if (!session) throw new Error("Field Notes test needs a session");
    renderRoute({ kind: "voice", sessionId: session.id }, "ios", "voice");
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
    "maps %s primitive presentation and square action targets",
    (platform) => {
      renderRoute(conversationRoute(), platform);
      const primitives = getPlatformPrimitives(platform);
      const root = document.querySelector("[data-concept-root]");
      expect(root).toHaveAttribute("data-title", primitives.title);
      expect(root).toHaveAttribute("data-sheet", primitives.sheet);
      expect(root).toHaveAttribute("data-elevation", primitives.elevation);
      expect(root).toHaveAttribute("data-feedback", primitives.feedback);
      expect(root).toHaveAttribute("data-back", primitives.back);
      const back = within(mainFor("conversation")).getByRole("button", {
        name: "Back",
      });
      expect(back.style.width).toBe(`${primitives.minimumTarget}px`);
      expect(back.style.height).toBe(`${primitives.minimumTarget}px`);
      expect(fieldNotesCss).toContain(`[data-sheet="${primitives.sheet}"]`);
      expect(fieldNotesCss).toMatch(
        new RegExp(
          `data-feedback=.[^\\]]*${primitives.feedback}[^\\]]*.].*:active`,
        ),
      );
      const edgePrefix =
        platform === "ios" ? "env(safe-area-inset-" : "var(--safe-area-";
      if (!(root instanceof HTMLElement))
        throw new Error("Missing concept root");
      for (const edge of ["top", "right", "bottom", "left"]) {
        expect(root.style.getPropertyValue(`--fn-edge-${edge}`)).toContain(
          `${edgePrefix}${edge}`,
        );
      }
    },
  );

  it("uses viewport and keyboard variables, wrapping titles, local texture, and contrast fallbacks", () => {
    expect(fieldNotesCss).toContain(
      "height: var(--visual-viewport-height, 100dvh)",
    );
    expect(fieldNotesCss).toContain("var(--keyboard-inset-height");
    expect(fieldNotesCss).toContain("overflow-wrap: anywhere");
    expect(fieldNotesCss).toContain("prefers-reduced-transparency: reduce");
    expect(fieldNotesCss).toContain("@media (forced-colors: active)");
    expect(fieldNotesCss).toContain("#fffaf1");
    expect(fieldNotesCss).toContain("#1d1916");
    expect(fieldNotesCss).not.toMatch(/url\s*\(/i);
    expect(fieldNotesCss).not.toContain("backdrop-filter");
    expect(fieldNotesCss).not.toContain("position: fixed");
  });
});
