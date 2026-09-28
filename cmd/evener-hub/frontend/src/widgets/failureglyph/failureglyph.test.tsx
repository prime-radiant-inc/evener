import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { FailureGlyph } from "./index";

test("carries a real accessible name rather than a bare glyph character", () => {
  render(<FailureGlyph />);
  expect(screen.getByRole("img", { name: "Failed" })).toBeTruthy();
});

test("the drawn cross itself is hidden from assistive tech (the label speaks for it)", () => {
  const { container } = render(<FailureGlyph />);
  expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
});
