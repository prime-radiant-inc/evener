import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { VisuallyHidden as FromBarrel } from "..";
import { VisuallyHidden } from ".";

afterEach(cleanup);

test("its text stays in the accessibility tree", () => {
  render(
    <button type="button">
      <span aria-hidden="true">×</span>
      <VisuallyHidden>Remove attachment</VisuallyHidden>
    </button>,
  );
  expect(screen.getByRole("button", { name: "Remove attachment" })).toBeTruthy();
});

test("it is exported from the widget barrel", () => {
  expect(FromBarrel).toBe(VisuallyHidden);
});
