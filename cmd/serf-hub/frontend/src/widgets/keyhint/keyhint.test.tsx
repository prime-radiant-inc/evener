import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { KeyHint } from "./index";

function setPlatform(platform: string) {
  Object.defineProperty(window.navigator, "platform", { value: platform, configurable: true });
}

const ORIGINAL_PLATFORM = window.navigator.platform;

afterEach(() => {
  cleanup();
  setPlatform(ORIGINAL_PLATFORM);
});

test("renders one kbd element per key", () => {
  render(<KeyHint keys={["Shift", "K"]} />);
  expect(document.querySelectorAll("kbd")).toHaveLength(2);
});

test("renders a non-Mod key verbatim", () => {
  render(<KeyHint keys={["K"]} />);
  expect(screen.getByText("K")).toBeTruthy();
});

test("splits Mod to the platform symbol: Mac shows the command glyph", () => {
  setPlatform("MacIntel");
  render(<KeyHint keys={["Mod", "K"]} />);
  expect(screen.getByText("⌘")).toBeTruthy();
  expect(screen.queryByText("Ctrl")).toBeNull();
});

test("splits Mod to the platform symbol: non-Mac shows Ctrl", () => {
  setPlatform("Win32");
  render(<KeyHint keys={["Mod", "K"]} />);
  expect(screen.getByText("Ctrl")).toBeTruthy();
  expect(screen.queryByText("⌘")).toBeNull();
});

test("separates multiple keys visibly", () => {
  render(<KeyHint keys={["Mod", "Shift", "K"]} />);
  // three kbd elements, two literal "+" separators between them
  expect(document.querySelectorAll("kbd")).toHaveLength(3);
  expect(screen.getAllByText("+")).toHaveLength(2);
});

test("renders a single key with no separator", () => {
  render(<KeyHint keys={["Enter"]} />);
  expect(document.querySelectorAll("kbd")).toHaveLength(1);
  expect(screen.queryByText("+")).toBeNull();
});
