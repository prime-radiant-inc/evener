import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { SeenDivider } from "./SeenDivider";

afterEach(() => {
  cleanup();
});

test("renders a labelled marker naming this as new content, not a bare line", () => {
  render(<SeenDivider />);
  const row = screen.getByTestId("seen-divider");
  expect(row.textContent!.toLowerCase()).toContain("new");
});

test("carries no interactive role - it's a passive marker, unlike NewContentPill's button", () => {
  render(<SeenDivider />);
  expect(screen.queryByRole("button")).toBeNull();
});
