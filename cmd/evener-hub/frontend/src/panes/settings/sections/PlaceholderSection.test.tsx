import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { emptyStateReasons } from "../../../widgets/emptystate/emptyStateTestUtils";
import { settingsSectionLabel } from "../sections";
import { PlaceholderSection } from "./PlaceholderSection";

function placeholderTitle(): string | null | undefined {
  return screen.getByTestId("empty-state").querySelector("p")?.textContent;
}

test("shows the resolved section label as its title", () => {
  render(<PlaceholderSection sectionId="theme" />);
  expect(placeholderTitle()).not.toBe("theme");
  expect(placeholderTitle()).toBe(settingsSectionLabel("theme"));
});

test("falls back to the raw id for an unrecognized section", () => {
  render(<PlaceholderSection sectionId="not-a-real-section" />);
  expect(placeholderTitle()).toBe("not-a-real-section");
});

test("marks itself as a not-yet-built section", () => {
  const { container } = render(<PlaceholderSection sectionId="hub" />);
  expect(emptyStateReasons(container)).toEqual(["not-built"]);
});
