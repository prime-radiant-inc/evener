import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { canonicalFixture } from "../core/fixtures";
import { getPlatformPrimitives } from "../core/platform";
import { scenarioIds } from "../core/scenarios";
import {
  createPrototypeStore,
  type PreferenceStorage,
  PrototypeProvider,
} from "../core/store";
import { LabControls } from "./LabControls";

function setup(open = true) {
  const storage: PreferenceStorage = {
    getItem: () => null,
    setItem: vi.fn(),
    removeItem: vi.fn(),
  };
  const store = createPrototypeStore({
    platform: "android",
    storage,
    fixtureInput: canonicalFixture,
    diagnostics: { report: vi.fn() },
  });
  if (open) {
    store.getState().dispatch({ type: "openOverlay", overlay: "lab-controls" });
  }
  const dispatch = vi.fn();
  render(
    <PrototypeProvider store={store}>
      <LabControls
        dispatch={dispatch}
        primitives={getPlatformPrimitives("android")}
      />
    </PrototypeProvider>,
  );
  return { dispatch, storage, store };
}

function setupInteractive() {
  const storage: PreferenceStorage = {
    getItem: () => null,
    setItem: vi.fn(),
    removeItem: vi.fn(),
  };
  const store = createPrototypeStore({
    platform: "android",
    storage,
    fixtureInput: canonicalFixture,
    diagnostics: { report: vi.fn() },
  });
  render(
    <PrototypeProvider store={store}>
      <LabControls
        dispatch={(action) => store.getState().dispatch(action)}
        primitives={getPlatformPrimitives("android")}
      />
    </PrototypeProvider>,
  );
  return { store };
}

describe("LabControls", () => {
  it("opens from a normal button and uses the platform dialog primitive", () => {
    const { dispatch } = setup(false);
    fireEvent.click(screen.getByRole("button", { name: "Lab Controls" }));
    expect(dispatch).toHaveBeenCalledWith({
      type: "openOverlay",
      overlay: "lab-controls",
    });
  });

  it("exposes every scenario in a fieldset", () => {
    setup();
    expect(
      screen.getByRole("dialog", { name: "Lab Controls" }),
    ).toHaveAttribute("data-dialog", "material-dialog");
    for (const scenario of scenarioIds) {
      expect(screen.getByRole("radio", { name: scenario })).toBeVisible();
    }
  });

  it("dispatches typed appearance, text, and motion actions", () => {
    const { dispatch } = setup();
    fireEvent.click(screen.getByRole("radio", { name: "dark" }));
    expect(dispatch).toHaveBeenCalledWith({
      type: "setAppearance",
      appearance: "dark",
    });
    fireEvent.click(screen.getByRole("radio", { name: "accessibility" }));
    expect(dispatch).toHaveBeenCalledWith({
      type: "setTextScale",
      textScale: "accessibility",
    });
    fireEvent.click(screen.getByRole("checkbox", { name: "Reduce motion" }));
    expect(dispatch).toHaveBeenCalledWith({
      type: "setReducedMotion",
      reducedMotion: true,
    });
  });

  it("dispatches scenario, close, and reset through navigation", () => {
    const { dispatch } = setup();
    fireEvent.click(screen.getByRole("radio", { name: "offline" }));
    expect(dispatch).toHaveBeenCalledWith({
      type: "setScenario",
      scenario: "offline",
    });
    fireEvent.click(screen.getByRole("button", { name: "Close Lab Controls" }));
    expect(dispatch).toHaveBeenCalledWith({ type: "goBack" });
    fireEvent.click(screen.getByRole("button", { name: "Reset prototype" }));
    expect(dispatch).toHaveBeenCalledWith({ type: "reset" });
  });

  it("keeps controls keyboard reachable at large text", () => {
    const { store } = setup();
    store.getState().dispatch({ type: "setTextScale", textScale: "large" });
    for (const control of screen.getAllByRole("radio")) {
      expect(control).not.toHaveAttribute("tabindex", "-1");
    }
    expect(
      screen.getByRole("button", { name: "Reset prototype" }),
    ).not.toHaveAttribute("tabindex", "-1");
  });

  it("moves focus inside, traps both Tab directions, and restores the opener", async () => {
    setupInteractive();
    const opener = screen.getByRole("button", { name: "Lab Controls" });
    opener.focus();
    fireEvent.click(opener);

    const close = screen.getByRole("button", { name: "Close Lab Controls" });
    const reset = screen.getByRole("button", { name: "Reset prototype" });
    await waitFor(() => expect(close).toHaveFocus());
    expect(opener).toHaveAttribute("inert");
    expect(opener).toHaveAttribute("tabindex", "-1");

    fireEvent.keyDown(close, { key: "Tab", shiftKey: true });
    expect(reset).toHaveFocus();
    fireEvent.keyDown(reset, { key: "Tab" });
    expect(close).toHaveFocus();
    opener.focus();
    expect(close).toHaveFocus();

    fireEvent.click(close);
    await waitFor(() => expect(opener).toHaveFocus());
  });
});
