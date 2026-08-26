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
  const ui = (open: boolean) => (
    <div>
      <button type="button" data-concept-switch-trigger="true">
        Switch concept
      </button>
      <PrototypeProvider store={store}>
        <ConceptSwitcher
          open={open}
          dispatch={dispatch}
          onClose={onClose}
          primitives={getPlatformPrimitives(platform)}
        />
      </PrototypeProvider>
    </div>
  );
  const view = render(ui(true));
  const opener = view.container.querySelector<HTMLButtonElement>(
    "[data-concept-switch-trigger='true']",
  );
  if (!opener) throw new Error("switcher test opener was not rendered");
  return {
    dispatch,
    onClose,
    opener,
    closeSwitcher: () => view.rerender(ui(false)),
    ...view,
  };
}

afterEach(() => {
  cleanup();
  expect(
    document.querySelector("[data-concept-switch-trigger='true']"),
  ).toBeNull();
});

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

  it("traps Tab and makes selection plus every repeated close request idempotent", async () => {
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
    expect(close).toBeDisabled();
    for (const choice of screen.getAllByRole("button", { name: /^Select / })) {
      expect(choice).toBeDisabled();
    }

    fireEvent.click(close);
    fireEvent.keyDown(screen.getByRole("dialog", { name: "Switch concept" }), {
      key: "Escape",
    });
    fireEvent.click(fieldNotes);
    expect(dispatch).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("keeps focus inside while open and restores the invoking opener on close", async () => {
    const { opener, closeSwitcher } = renderSwitcher();
    const close = screen.getByRole("button", {
      name: "Close concept switcher",
    });
    await waitFor(() => expect(close).toHaveFocus());

    opener.focus();
    expect(close).toHaveFocus();

    closeSwitcher();
    await waitFor(() => expect(opener).toHaveFocus());
  });
});
