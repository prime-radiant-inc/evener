import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { CommandPalette } from "./CommandPalette";
import { openPalette, paletteStore } from "./paletteController";

beforeEach(() => {
  paletteStore.setState({ open: false, query: "" });
});
afterEach(cleanup);

test("renders no overlay while the palette store is closed", () => {
  render(<CommandPalette />);
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("opens an overlay when the palette store opens", () => {
  render(<CommandPalette />);
  act(() => {
    openPalette();
  });
  expect(screen.getByRole("dialog", { name: "Command palette" })).toBeTruthy();
});

test("Escape closes the overlay and the palette store", async () => {
  const user = userEvent.setup();
  render(<CommandPalette />);
  act(() => {
    openPalette();
  });

  await user.keyboard("{Escape}");

  expect(screen.queryByRole("dialog")).toBeNull();
  expect(paletteStore.getState().open).toBe(false);
});
