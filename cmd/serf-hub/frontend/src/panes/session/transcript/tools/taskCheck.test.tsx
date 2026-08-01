import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { TaskCheck, TOUCHES } from "./taskCheck";

afterEach(cleanup);

test("every touch renders one square, aria-hidden checkbox glyph tagged with its touch", () => {
  for (const touch of TOUCHES) {
    const { unmount } = render(<TaskCheck touch={touch} />);
    const svg = screen.getByTestId("task-check");
    expect(svg.getAttribute("data-touch")).toBe(touch);
    expect(svg.getAttribute("aria-hidden")).toBe("true");
    expect(svg.getAttribute("width")).toBe(svg.getAttribute("height"));
    // getAttribute("class"), not .className: under jsdom an SVG element's
    // className is an SVGAnimatedString, not a string.
    expect(svg.getAttribute("class")).toContain(touch); // per-touch color modifier class
    unmount();
  }
});

test("the default box is 16px and a caller can override the size", () => {
  const { unmount } = render(<TaskCheck touch="done" />);
  expect(screen.getByTestId("task-check").getAttribute("width")).toBe("16");
  unmount();
  render(<TaskCheck touch="done" size={20} />);
  expect(screen.getByTestId("task-check").getAttribute("width")).toBe("20");
});
