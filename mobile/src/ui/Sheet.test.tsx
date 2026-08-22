import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { __resetSheetHistory, __sheetHistorySettled, Sheet } from "./Sheet";

afterEach(async () => {
  cleanup();
  await __sheetHistorySettled();
  __resetSheetHistory();
  history.replaceState(null, "");
});

describe("Sheet — lifecycle and accessibility", () => {
  it("renders nothing when closed", () => {
    const { container } = render(
      <Sheet open={false} onClose={() => {}} title="Servers">
        <p>content</p>
      </Sheet>,
    );
    expect(container.firstChild).toBeNull();
  });

  it("renders a portal with title and content when open", () => {
    render(
      <Sheet open onClose={() => {}} title="Servers">
        <p>sheet body</p>
      </Sheet>,
    );
    const dialog = screen.getByRole("dialog");
    expect(dialog).toBeInTheDocument();
    expect(within(dialog).getByText("Servers")).toBeInTheDocument();
    expect(within(dialog).getByText("sheet body")).toBeInTheDocument();
  });

  it("Escape closes the sheet", () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
  });

  it("focuses the first focusable control (Done) when open", async () => {
    render(
      <Sheet open onClose={() => {}} title="Servers">
        <button type="button">First</button>
        <button type="button">Second</button>
      </Sheet>,
    );
    const done = screen.getByRole("button", { name: "Done" });
    await vi.waitFor(() => expect(done).toHaveFocus());
  });

  it("Tab wraps from last focusable to first (focus trap)", async () => {
    render(
      <Sheet open onClose={() => {}} title="Servers">
        <button type="button">First</button>
        <button type="button">Last</button>
      </Sheet>,
    );
    const dialog = screen.getByRole("dialog");
    const _first = within(dialog).getByText("First");
    const last = within(dialog).getByText("Last");
    // Wait for initial focus to settle.
    await vi.waitFor(() =>
      expect(screen.getByRole("button", { name: "Done" })).toHaveFocus(),
    );
    // Shift-Tab on the first content button wraps to the last (Done is first).
    fireEvent.keyDown(last, { key: "Tab" });
    // Tab on last wraps to first focusable (Done).
    expect(screen.getByRole("button", { name: "Done" })).toHaveFocus();
    // Shift-Tab on Done wraps to last.
    fireEvent.keyDown(screen.getByRole("button", { name: "Done" }), {
      key: "Tab",
      shiftKey: true,
    });
    expect(last).toHaveFocus();
    // Tab on Last wraps to Done (first).
    fireEvent.keyDown(last, { key: "Tab" });
    expect(screen.getByRole("button", { name: "Done" })).toHaveFocus();
  });

  it("renders a Done/Close button", () => {
    render(
      <Sheet open onClose={() => {}} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    const dialog = screen.getByRole("dialog");
    expect(
      within(dialog).getByRole("button", { name: "Done" }),
    ).toBeInTheDocument();
  });
});
