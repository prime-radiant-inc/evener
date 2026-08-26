import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { canonicalFixture } from "../core/fixtures";
import { getPlatformPrimitives } from "../core/platform";
import { createPrototypeStore, PrototypeProvider } from "../core/store";
import { ConceptSwitcher } from "./ConceptSwitcher";

function renderSwitcher(platform: "ios" | "android" = "ios") {
  const store = createPrototypeStore({
    platform,
    storage: {
      getItem: () => null,
      setItem: vi.fn(),
      removeItem: vi.fn(),
    },
    fixtureInput: canonicalFixture,
    diagnostics: { report: vi.fn() },
  });
  const dispatch = vi.fn();
  const onClose = vi.fn();
  const opener = document.createElement("button");
  opener.textContent = "Switch concept";
  document.body.append(opener);
  opener.focus();

  return {
    dispatch,
    onClose,
    opener,
    ...render(
      <PrototypeProvider store={store}>
        <ConceptSwitcher
          open
          dispatch={dispatch}
          onClose={onClose}
          primitives={getPlatformPrimitives(platform)}
        />
      </PrototypeProvider>,
    ),
  };
}

afterEach(() => cleanup());

describe("ConceptSwitcher", () => {
  it("renders shared metadata in a platform sheet with real modal semantics and initial focus", async () => {
    renderSwitcher("android");

    const dialog = screen.getByRole("dialog", { name: "Switch concept" });
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog).toHaveAttribute("data-sheet", "material-modal-bottom-sheet");
    expect(screen.getAllByRole("article")).toHaveLength(3);
    expect(screen.getByText("Quiet Instrument")).toBeVisible();
    expect(screen.getByText("Living System")).toBeVisible();
    expect(screen.getByText("Transcript Studio")).toBeVisible();
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Close concept switcher" }),
      ).toHaveFocus(),
    );
  });

  it("traps Tab, delegates Close, and dispatches selection before closing", async () => {
    const { dispatch, onClose } = renderSwitcher();
    const close = screen.getByRole("button", {
      name: "Close concept switcher",
    });
    const fieldNotes = screen.getByRole("button", {
      name: "Select Field Notes",
    });
    await waitFor(() => expect(close).toHaveFocus());

    fieldNotes.focus();
    fireEvent.keyDown(fieldNotes, { key: "Tab" });
    expect(close).toHaveFocus();
    fireEvent.keyDown(close, { key: "Tab", shiftKey: true });
    expect(fieldNotes).toHaveFocus();

    fireEvent.click(fieldNotes);
    expect(dispatch).toHaveBeenCalledWith({
      type: "selectConcept",
      concept: "field-notes",
    });
    expect(onClose).toHaveBeenCalledOnce();

    fireEvent.click(close);
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("keeps focus inside while open and restores the invoking opener on close", async () => {
    const { opener, rerender } = renderSwitcher();
    const close = screen.getByRole("button", {
      name: "Close concept switcher",
    });
    await waitFor(() => expect(close).toHaveFocus());

    opener.focus();
    expect(close).toHaveFocus();

    rerender(null);
    await waitFor(() => expect(opener).toHaveFocus());
  });
});
