import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetAskDockStoreForTests } from "../panes/session/composer/askDock/askDockStore";
import { resetThreadsStoreForTests } from "../stores/threads";
import { resetTreeStoreForTests } from "../stores/tree";
import { resetDisclosureStoreForTests } from "../widgets/disclosure/disclosureStore";
import SurfaceGallery from "./SurfaceGallery";

afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  resetAskDockStoreForTests();
  resetDisclosureStoreForTests();
  resetTreeStoreForTests();
});

// Unlike WidgetGallery.test.tsx's widgets<->gallery-sections completeness
// contract, surfaces are opt-in (this task's own instructions): this test
// only asserts the gallery renders and mounts every currently-registered
// section without throwing, plus that the priority surfaces named in the
// task exist by their own heading. No 1:1 manifest is enforced.
test("renders without throwing, with the intro note and every discovered section", () => {
  render(<SurfaceGallery />);
  expect(screen.getByText(/surface gallery/i)).toBeTruthy();
  // Every section renders an <h2> heading (WidgetGallery's own convention,
  // reused here) - if any section threw during render, this whole test
  // would already have failed before reaching this assertion.
  const headings = screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent);
  expect(headings.length).toBeGreaterThan(0);
});

test("the transcript section is registered", () => {
  render(<SurfaceGallery />);
  expect(screen.getByRole("heading", { level: 2, name: "Transcript" })).toBeTruthy();
});

test("the session chrome section is registered", () => {
  render(<SurfaceGallery />);
  expect(screen.getByRole("heading", { level: 2, name: "Session chrome" })).toBeTruthy();
});
