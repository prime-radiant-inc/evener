// Proves the transcript surface section (dw3s) gives a fresh checkout every
// state a tool-call row can be in without any throwaway harness: a shell row
// collapsed by default, one forced open to show the expanded state, one that
// failed (auto-expands on its own), an edit_file diff row, and the delegate
// row. Each assertion reads the SAME markers ToolCallItem itself renders
// (data-tool-name, data-failed, the <details open> attribute, the
// tool-call-body testid) rather than re-deriving them, so this test breaks
// the moment the fixture stops actually exercising the state it claims to.
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

test("a completed shell row starts collapsed", () => {
  render(<TranscriptSurfaceSection />);
  const rows = screen.getAllByTestId("tool-call-item").filter((el) => el.dataset.toolName === "shell");
  const collapsed = rows.find((el) => (el as HTMLDetailsElement).open === false);
  expect(collapsed).toBeTruthy();
});

test("one shell row is forced open to demonstrate the expanded state", () => {
  render(<TranscriptSurfaceSection />);
  const rows = screen.getAllByTestId("tool-call-item").filter((el) => el.dataset.toolName === "shell");
  const expandedNonFailed = rows.find(
    (el) => (el as HTMLDetailsElement).open === true && el.dataset.failed === undefined,
  );
  expect(expandedNonFailed).toBeTruthy();
});

test("a failed shell row auto-expands and carries data-failed", () => {
  render(<TranscriptSurfaceSection />);
  const rows = screen.getAllByTestId("tool-call-item").filter((el) => el.dataset.toolName === "shell");
  const failed = rows.find((el) => el.dataset.failed === "true");
  expect(failed).toBeTruthy();
  expect((failed as HTMLDetailsElement).open).toBe(true);
});

test("an edit_file row renders a diff body", () => {
  render(<TranscriptSurfaceSection />);
  const row = rowFor("edit_file");
  expect((row as HTMLDetailsElement).open).toBe(true);
  expect(row.querySelector('[data-testid="tool-call-body"]')).toBeTruthy();
});

test("the delegate row is registered", () => {
  render(<TranscriptSurfaceSection />);
  expect(rowFor("delegate")).toBeTruthy();
});
