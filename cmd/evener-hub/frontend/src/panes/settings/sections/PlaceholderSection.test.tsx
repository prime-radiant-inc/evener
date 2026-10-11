import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { PlaceholderSection } from "./PlaceholderSection";

test("shows the resolved section label as its title", () => {
  render(<PlaceholderSection sectionId="theme" />);
  expect(screen.getByText("Theme")).toBeTruthy();
});

test("falls back to the raw id for an unrecognized section", () => {
  render(<PlaceholderSection sectionId="not-a-real-section" />);
  expect(screen.getByText("not-a-real-section")).toBeTruthy();
});

test("shows a not-yet-built hint", () => {
  render(<PlaceholderSection sectionId="hub" />);
  expect(screen.getByText(/hasn't been built yet/i)).toBeTruthy();
});
