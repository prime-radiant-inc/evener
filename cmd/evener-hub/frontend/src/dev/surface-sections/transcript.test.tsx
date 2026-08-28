import { cleanup, render, screen } from "@testing-library/react";
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
  return item.querySelector('[data-testid="tool-row-trigger"]')?.getAttribute("aria-expanded") === "true";
}

test("renders the shared deterministic fixture through the preview surface", () => {
  render(<TranscriptSurfaceSection />);
  expect(screen.getAllByText("Inspect the transcript display flow").length).toBeGreaterThan(0);
  expect(screen.getAllByText("The transcript display flow is ready.").length).toBeGreaterThan(0);
  expect(screen.getAllByText("I will inspect the display projection and its test coverage.").length).toBeGreaterThan(0);
  expect(screen.getAllByText("Working tree environment is ready.").length).toBeGreaterThan(0);
});

test("renders both successful and failed production tool rows", () => {
  render(<TranscriptSurfaceSection />);
  expect(rowFor("read_file")).toBeTruthy();
  const failed = rowFor("shell");
  expect(failed.dataset.failed).toBe("true");
  expect(isOpen(failed)).toBe(true);
});

test("uses fixed fixture timestamps and no live streaming markers", () => {
  render(<TranscriptSurfaceSection />);
  const rows = screen.getAllByTestId("tool-call-item");
  expect(rows.length).toBeGreaterThan(0);
  expect(screen.queryByText(/elapsed|streaming/i)).toBeNull();
});
