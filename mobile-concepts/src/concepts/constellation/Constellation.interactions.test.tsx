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
import type { Platform, Route, ScenarioId } from "../../core/model";
import { getPlatformPrimitives } from "../../core/platform";
import { renderConcept } from "../../test/renderConcept";
import { constellationModule } from "./index";

const constellationCss = readFileSync(
  "src/concepts/constellation/constellation.css",
  "utf8",
);

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function fixtureSession(predicate: (sessionId: string) => boolean): string {
  const session = canonicalFixture.sessions.find(({ id }) => predicate(id));
  if (!session) throw new Error("Constellation test needs a fixture session");
  return session.id;
}

function routeFor(kind: "sessions" | "conversation" | "work" | "voice"): Route {
  if (kind === "sessions") return { kind: "root", tab: "sessions" };
  if (kind === "work") {
    const node = canonicalFixture.work[0];
    if (!node) throw new Error("Constellation test needs Work fixtures");
    return { kind: "work", sessionId: node.sessionId };
  }
  if (kind === "conversation") {
    const item = canonicalFixture.transcript.find(
      (candidate) => candidate.kind === "tool",
    );
    if (!item) throw new Error("Constellation test needs a tool transcript");
    return { kind: "conversation", sessionId: item.sessionId };
  }
  return {
    kind: "voice",
    sessionId: fixtureSession(() => true),
  };
}

function renderRoute(
  kind: "sessions" | "conversation" | "work" | "voice",
  platform: Platform = "ios",
  scenario: ScenarioId = "baseline",
) {
  return renderConcept(constellationModule, {
    platform,
    scenario,
    route: routeFor(kind),
  });
}

function mainFor(route: string): HTMLElement {
  const main = screen.getByRole("main");
  expect(main).toHaveAttribute("data-route", route);
  return main;
}

describe("Constellation living-system composition", () => {
  it("makes attention and active-session relationships visible in text and connection markers", () => {
    renderRoute("sessions", "ios", "multi-agent");
    const main = mainFor("sessions");
    const attention = main.querySelector('[data-attention-signal="true"]');
    expect(attention).toBeVisible();
    expect(attention).toHaveTextContent(/needs attention/i);
    expect(attention).toHaveAttribute("data-attention-state");

    const workSessionIds = new Set(
      canonicalFixture.work.map(({ sessionId }) => sessionId),
    );
    for (const sessionId of workSessionIds) {
      const row = main.querySelector(`[data-session-id="${sessionId}"]`);
      expect(row).toBeVisible();
      const relation = row?.querySelector("[data-relationship-rail]");
      expect(relation).toBeVisible();
      expect(relation).toHaveTextContent(/connected work item/i);
      expect(relation?.querySelector("[data-connection-marker]")).toBeVisible();
    }
  });

  it("orders Work from parentId as nested semantic lists with text equivalents", () => {
    renderRoute("work");
    const main = mainFor("work");
    const sessionId = (routeFor("work") as { sessionId: string }).sessionId;
    const nodes = canonicalFixture.work.filter(
      (node) => node.sessionId === sessionId,
    );
    for (const node of nodes) {
      const element = main.querySelector(`[data-work-node-id="${node.id}"]`);
      expect(element).toBeVisible();
      expect(element).toHaveAttribute(
        "data-work-parent-id",
        node.parentId ?? "root",
      );
      expect(element).toHaveTextContent(/level \d+ · parent:/i);
      if (node.parentId) {
        const parent = main.querySelector(
          `[data-work-node-id="${node.parentId}"]`,
        );
        expect(
          element?.closest("li")?.parentElement?.closest("li"),
        ).toContainElement(parent as HTMLElement);
      }
    }
    expect(
      main.querySelectorAll("ul[data-relationship-list]").length,
    ).toBeGreaterThan(0);
  });

  it("preserves relationship and state semantics under simulated reduced motion", async () => {
    const result = renderRoute("sessions", "ios", "multi-agent");
    const beforeRelations = document.querySelectorAll(
      "[data-relationship-rail]",
    ).length;
    const beforeStates = document.querySelectorAll("[data-state-label]").length;
    result.store
      .getState()
      .dispatch({ type: "setReducedMotion", reducedMotion: true });
    await waitFor(() => {
      const root = document.querySelector("[data-concept-root]");
      expect(root).toHaveAttribute("data-reduced-motion", "true");
      expect(root).toHaveAttribute("data-motion", "reduced");
      expect(
        document.querySelectorAll("[data-relationship-rail]"),
      ).toHaveLength(beforeRelations);
      expect(document.querySelectorAll("[data-state-label]")).toHaveLength(
        beforeStates,
      );
    });
    expect(constellationCss).toContain("prefers-reduced-motion: reduce");
    expect(constellationCss).toContain('[data-reduced-motion="true"]');
  });

  it("retains forced-colors-friendly text and shape attention semantics", () => {
    renderRoute("sessions", "android", "needs-attention");
    const attention = document.querySelector('[data-attention-signal="true"]');
    expect(attention).toHaveTextContent(/needs attention/i);
    expect(
      attention?.querySelector('[aria-hidden="true"]'),
    ).toBeInTheDocument();
    expect(constellationCss).toContain("@media (forced-colors: active)");
    expect(constellationCss).toContain("forced-color-adjust");
  });

  it("keeps expanded tools in the opaque transcript reading flow", () => {
    renderRoute("conversation");
    const main = mainFor("conversation");
    const tool = canonicalFixture.transcript.find(
      (item) => item.kind === "tool",
    );
    if (tool?.kind !== "tool")
      throw new Error("Constellation test needs tool content");
    const button = within(main).getByRole("button", { name: tool.label });
    fireEvent.click(button);
    const item = main.querySelector(`[data-transcript-item-id="${tool.id}"]`);
    expect(item).toHaveAttribute("data-spatial-presentation", "reading-flow");
    expect(within(item as HTMLElement).getByText("Arguments")).toBeVisible();
    expect(within(item as HTMLElement).getByText("Output")).toBeVisible();
    expect(item?.querySelector("canvas, svg[role='img']")).toBeNull();
  });

  it("uses Android system Back through Work, Conversation, and Voice route order", async () => {
    const result = renderRoute("sessions", "android");
    const workNode = canonicalFixture.work[0];
    if (!workNode) throw new Error("Constellation test needs Work ownership");
    const session = document.querySelector(
      `[data-session-id="${workNode.sessionId}"]`,
    );
    fireEvent.click(within(session as HTMLElement).getByRole("button"));
    let main = mainFor("conversation");

    fireEvent.click(within(main).getByRole("button", { name: "Work" }));
    await waitFor(() =>
      expect(result.store.getState().route.kind).toBe("work"),
    );
    main = mainFor("work");
    fireEvent.click(within(main).getByRole("button", { name: "Back" }));
    await waitFor(() =>
      expect(result.store.getState().route.kind).toBe("conversation"),
    );

    main = mainFor("conversation");
    fireEvent.click(within(main).getByRole("button", { name: "Voice" }));
    await waitFor(() =>
      expect(result.store.getState().route.kind).toBe("voice"),
    );
    main = mainFor("voice");
    fireEvent.click(within(main).getByRole("button", { name: "Close" }));
    await waitFor(() =>
      expect(result.store.getState().route.kind).toBe("conversation"),
    );

    main = mainFor("conversation");
    fireEvent.click(within(main).getByRole("button", { name: "Back" }));
    await waitFor(() =>
      expect(result.store.getState().route).toEqual({
        kind: "root",
        tab: "sessions",
      }),
    );
  });

  it.each(["ios", "android"] as const)(
    "expresses %s structure through primitive hooks including touch active feedback",
    (platform) => {
      renderRoute("work", platform);
      const primitives = getPlatformPrimitives(platform);
      const root = document.querySelector("[data-concept-root]");
      expect(root).toHaveAttribute("data-navigation", primitives.navigation);
      expect(root).toHaveAttribute("data-title", primitives.title);
      expect(root).toHaveAttribute("data-sheet", primitives.sheet);
      expect(root).toHaveAttribute("data-elevation", primitives.elevation);
      expect(root).toHaveAttribute("data-feedback", primitives.feedback);
      expect(root).toHaveAttribute("data-back", primitives.back);
      expect(root).toHaveAttribute("data-safe-area", primitives.safeArea);
      expect(root).toHaveAttribute(
        "data-minimum-target",
        String(primitives.minimumTarget),
      );
      expect(
        mainFor("work").querySelector("[data-work-presentation]"),
      ).toHaveAttribute("data-work-presentation", primitives.sheet);
      expect(constellationCss).toContain(`[data-title="${primitives.title}"]`);
      expect(constellationCss).toContain(`[data-sheet="${primitives.sheet}"]`);
      expect(constellationCss).toContain(
        `[data-elevation="${primitives.elevation}"]`,
      );
      expect(constellationCss).toContain(
        `[data-feedback="${primitives.feedback}"]`,
      );
      expect(constellationCss).toMatch(
        new RegExp(
          `data-feedback=.[^\\]]*${primitives.feedback}[^\\]]*.].*:active`,
        ),
      );
    },
  );

  it("focuses Search targets and honors simulated plus effective OS motion", async () => {
    const result = renderConcept(constellationModule, {
      platform: "ios",
      scenario: "baseline",
      route: { kind: "root", tab: "search" },
    });
    const main = mainFor("search");
    const scrollTo = vi.fn();
    Object.defineProperty(main, "scrollTo", {
      configurable: true,
      value: scrollTo,
    });
    const fixtureResult = canonicalFixture.search.find(
      ({ itemId }) => itemId !== null,
    );
    if (!fixtureResult?.itemId)
      throw new Error("Constellation test needs a focus target");
    result.store
      .getState()
      .dispatch({ type: "setReducedMotion", reducedMotion: true });
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
  });

  it("uses one scroll owner, viewport/safe/keyboard vars, wrapping text, and no remote assets", () => {
    expect(constellationCss).toContain(
      "height: var(--visual-viewport-height, 100dvh)",
    );
    expect(constellationCss).toContain("var(--safe-area-left)");
    expect(constellationCss).toContain("var(--safe-area-right)");
    expect(constellationCss).toContain("var(--keyboard-inset-height");
    expect(constellationCss).toContain("var(--keyboard-inset, 0px)");
    expect(constellationCss).toContain("overflow-wrap: anywhere");
    expect(constellationCss).not.toMatch(/url\s*\(/i);
    expect(constellationCss).not.toContain("backdrop-filter");
    expect(constellationCss).not.toContain("position: fixed");
  });
});
