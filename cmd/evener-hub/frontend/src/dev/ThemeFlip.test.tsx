import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { ThemeFlip } from "./ThemeFlip";

afterEach(() => {
  cleanup();
  document.documentElement.removeAttribute("data-theme");
});

test("renders its children twice: once under data-theme=dark, once under data-theme=light", () => {
  render(
    <ThemeFlip>
      <button type="button">Go</button>
    </ThemeFlip>,
  );
  const buttons = screen.getAllByRole("button", { name: "Go" });
  expect(buttons).toHaveLength(2);

  const lightWrapper = buttons[1]!.closest('[data-theme="light"]');
  expect(lightWrapper).not.toBeNull();

  const darkWrapper = buttons[0]!.closest('[data-theme="light"]');
  expect(darkWrapper).toBeNull();
});

test("labels the dark and light panes", () => {
  render(
    <ThemeFlip>
      <span>content</span>
    </ThemeFlip>,
  );
  expect(screen.getByText("Dark")).toBeTruthy();
  expect(screen.getByText("Light")).toBeTruthy();
});

test("explicitly scopes both panes when the gallery is nested under a light root", () => {
  document.documentElement.dataset.theme = "light";
  render(
    <ThemeFlip>
      <span>content</span>
    </ThemeFlip>,
  );

  expect(screen.getByText("Dark").parentElement?.getAttribute("data-theme")).toBe("dark");
  expect(screen.getByText("Light").parentElement?.getAttribute("data-theme")).toBe("light");
});

// A themed wrapper must reset ink alongside surface: a pane that flips the
// token scope but inherits `color` from the ambient theme hands every
// currentColor consumer (ToolIcon, Chevron - stroke="currentColor" line art)
// the WRONG theme's ink - near-black glyphs on the dark pane whenever the
// ambient theme is light. Asserted against the stylesheet text (the same
// off-disk technique token-contract.test.ts uses) because jsdom does not
// resolve CSS Modules classes to computed styles.
test("each pane resets color to the scoped theme's ink, not the ambient theme's", async () => {
  const { readFileSync } = await import("node:fs");
  const { dirname, join } = await import("node:path");
  const { fileURLToPath } = await import("node:url");
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "theme-flip.module.css"), "utf8");
  const paneRule = /\.pane\s*\{[^}]*\}/.exec(css)?.[0];
  expect(paneRule).toBeDefined();
  expect(paneRule).toMatch(/color:\s*var\(--ink-hi\)/);
});
