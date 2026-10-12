import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import TranscriptDisclosuresSurfaceSection from "./transcript-disclosures";

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

test("renders every folded transcript row, collapsed and expanded, in both themes", () => {
  render(<TranscriptDisclosuresSurfaceSection />);
  // Two themes x two expansion states.
  expect(screen.getAllByTestId("intent-group")).toHaveLength(4);
  expect(screen.getAllByTestId("think-block")).toHaveLength(4);
  expect(screen.getAllByTestId("steering-item")).toHaveLength(4);
  expect(screen.getAllByTestId("notification-card")).toHaveLength(8);
  expect(screen.getAllByTestId("system-notice-scaffold")).toHaveLength(4);
  expect(screen.getAllByTestId("system-notice-group")).toHaveLength(4);
});

test("every folded transcript row is a quiet Disclosure, and every intent group a rule one", () => {
  const { container } = render(<TranscriptDisclosuresSurfaceSection />);
  const intentGroups = screen.getAllByTestId("intent-group");
  expect(intentGroups).toHaveLength(4);
  for (const group of intentGroups) expect(group.getAttribute("data-variant")).toBe("rule");
  const rows = [
    ...screen.getAllByTestId("steering-item"),
    ...screen.getAllByTestId("system-notice-scaffold"),
    ...screen.getAllByTestId("system-notice-group"),
    ...screen.getAllByTestId("think-block").map((block) => block.querySelector("details")),
    ...screen.getAllByTestId("notification-card").map((card) => card.querySelector("details")),
  ];
  expect(rows.length).toBe(24);
  for (const row of rows) expect(row?.getAttribute("data-variant")).toBe("quiet");
  // Nested disclosures (the raw notification payload) are not transcript rows.
  expect(container.querySelectorAll('details[data-variant="default"]')).toHaveLength(0);
});
