import { beforeEach, expect, test } from "vitest";
import { closePalette, openPalette, paletteStore } from "./paletteController";

beforeEach(() => {
  paletteStore.setState({ open: false, query: "" });
});

test("openPalette opens the palette with an empty query by default", () => {
  openPalette();
  expect(paletteStore.getState().open).toBe(true);
  expect(paletteStore.getState().query).toBe("");
});

test("openPalette seeds the initial query (the leading-slash composer entry)", () => {
  openPalette("/");
  expect(paletteStore.getState().open).toBe(true);
  expect(paletteStore.getState().query).toBe("/");
});

test("closePalette closes the palette", () => {
  openPalette("/model");
  closePalette();
  expect(paletteStore.getState().open).toBe(false);
});

test("re-opening with no argument reseeds the query to empty", () => {
  openPalette("/model");
  closePalette();
  openPalette();
  expect(paletteStore.getState().query).toBe("");
});
