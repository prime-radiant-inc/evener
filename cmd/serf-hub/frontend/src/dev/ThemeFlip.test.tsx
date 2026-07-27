import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { ThemeFlip } from "./ThemeFlip";

afterEach(() => {
  cleanup();
  document.documentElement.removeAttribute("data-theme");
});

test("renders its children twice: once ambient (dark), once under data-theme=light", () => {
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
