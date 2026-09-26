import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import TranscriptSurfaceSection from "./transcript";

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

function rowFor(toolName: string): HTMLElement {
  const row = screen.getAllByTestId("tool-call-item").find((el) => el.dataset.toolName === toolName);
  if (!row) throw new Error(`no tool-call-item row for ${toolName}`);
  return row;
}

function isOpen(item: HTMLElement): boolean {
  // Intent-bearing rows use tool-row-body-trigger for body disclosure;
  // intent-less rows use tool-row-trigger. Prefer the body trigger when present.
  const bodyTrigger = item.querySelector('[data-testid="tool-row-body-trigger"]');
  if (bodyTrigger) return bodyTrigger.getAttribute("aria-expanded") === "true";
  return item.querySelector('[data-testid="tool-row-trigger"]')?.getAttribute("aria-expanded") === "true";
}

// Read-only checks of one render: the fixture's content, both tool-row
// outcomes, and the absence of live timing markers.
test("renders the shared deterministic fixture with fixed timestamps and both tool outcomes", () => {
  render(<TranscriptSurfaceSection />);
  expect(screen.getAllByText("Inspect the transcript display flow").length).toBeGreaterThan(0);
  expect(screen.getAllByText("The transcript display flow is ready.").length).toBeGreaterThan(0);
  expect(screen.getAllByText("I will inspect the display projection and its test coverage.").length).toBeGreaterThan(0);
  expect(screen.getAllByText("Working tree environment is ready.").length).toBeGreaterThan(0);

  expect(rowFor("read_file")).toBeTruthy();
  const failed = rowFor("shell");
  expect(failed.dataset.failed).toBe("true");
  expect(isOpen(failed)).toBe(true);

  expect(screen.queryByText(/elapsed|streaming/i)).toBeNull();
});

test("preview collaborators use the supplied owner projection, not the stale launch receipt", () => {
  const { container } = render(<TranscriptSurfaceSection />);
  for (const theme of ["light", "dark"]) {
    const view = within(container.querySelector<HTMLElement>(`[data-theme="${theme}"]`)!);
    const cards = view.getAllByTestId("subagent-row");
    expect(cards.map((card) => card.dataset.kind)).toEqual([
      "running",
      "done",
      "failed",
      "stopped",
      "failed",
      "unknown",
      "unknown",
    ]);
    expect(view.getAllByTestId("delegate-status-word").map((word) => word.textContent)).toEqual([
      "Running",
      "Idle · reported",
      "Failed",
      "Stopped",
      "Exhausted",
      "Status unavailable",
      "Status unavailable",
    ]);
    for (const card of cards)
      expect(within(card).getByTestId("subagent-stats").textContent).not.toMatch(/\d+ (turn|call)/);
  }
});
