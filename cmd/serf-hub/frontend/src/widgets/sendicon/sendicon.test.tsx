import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { SendIcon } from ".";

afterEach(cleanup);

// The button beside the glyph carries the verb ("Send" / "Start") as its
// accessible name - on mobile it is the only thing that does - so the glyph
// itself stays decorative; naming it would make a screen reader say it twice.
test("is decorative - no accessible name of its own", () => {
  const { container } = render(<SendIcon />);
  const el = container.querySelector("svg");
  expect(el).toBeTruthy();
  expect(el?.getAttribute("aria-hidden")).toBe("true");
  expect(el?.getAttribute("aria-label")).toBeNull();
  expect(el?.getAttribute("role")).toBeNull();
});

// Drawn as SVG in the app's stroke grammar so it inherits the button
// variant's currentColor rather than hardcoding a token of its own.
test("draws an SVG stroked with currentColor", () => {
  const { container } = render(<SendIcon />);
  const path = container.querySelector("svg path");
  expect(path?.getAttribute("fill")).toBe("none");
  expect(path?.getAttribute("stroke")).toBe("currentColor");
});
