import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { SteeringGlyph } from ".";

afterEach(cleanup);

test("renders the mark", () => {
  render(<SteeringGlyph />);
  expect(screen.getByTestId("steering-glyph")).toBeTruthy();
});

// The row's own text ("System steered: Tasks done") is the summary's
// accessible name and already says what the glyph says. Unlike FailureGlyph,
// which is often the only failure signal on its row, this is never the only
// signal - so naming it would make a screen reader say it twice.
test("is decorative - no accessible name of its own", () => {
  const { container } = render(<SteeringGlyph />);
  const el = container.querySelector('[data-testid="steering-glyph"]');
  expect(el?.getAttribute("aria-hidden")).toBe("true");
  expect(el?.getAttribute("aria-label")).toBeNull();
  expect(el?.getAttribute("role")).toBeNull();
});

// U+203B is outside the IBM Plex latin1 subset (global.css:23-24), so a
// literal would render from a system fallback font.
test("draws SVG, never the ※ character", () => {
  const { container } = render(<SteeringGlyph />);
  expect(container.querySelector("svg")).toBeTruthy();
  expect(container.textContent).not.toContain("※");
});
