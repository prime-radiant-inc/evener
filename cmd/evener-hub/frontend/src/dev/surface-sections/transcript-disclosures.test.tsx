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
