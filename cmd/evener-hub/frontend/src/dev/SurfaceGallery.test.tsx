import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetAskDockStoreForTests } from "../panes/session/composer/askDock/askDockStore";
import { resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetThreadsStoreForTests } from "../stores/threads";
import { resetDisclosureStoreForTests } from "../widgets/disclosure/disclosureStore";
import SurfaceGallery, { SURFACE_GALLERY_SECTIONS } from "./SurfaceGallery";

afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  resetAskDockStoreForTests();
  resetDisclosureStoreForTests();
  resetNavigationStoreForTests();
});

test("renders without throwing, with the intro note", () => {
  render(<SurfaceGallery sections={[]} />);
  expect(screen.getByText(/surface gallery/i)).toBeTruthy();
});

// Sections that must stay registered, with the heading each one renders.
const REQUIRED_SECTION_HEADINGS: Record<string, string> = {
  "./surface-sections/transcript.tsx": "Transcript",
  "./surface-sections/chrome.tsx": "Session chrome",
};

test.each(SURFACE_GALLERY_SECTIONS)("mounts discovered section $path without throwing", async (section) => {
  await act(async () => {
    render(<SurfaceGallery sections={[section]} />);
  });
  expect(screen.getAllByRole("heading", { level: 2 }).length).toBeGreaterThan(0);
  const requiredHeading = REQUIRED_SECTION_HEADINGS[section.path];
  if (requiredHeading) expect(screen.getByRole("heading", { level: 2, name: requiredHeading })).toBeTruthy();
});

test.each(Object.keys(REQUIRED_SECTION_HEADINGS))("the section %s is registered", (path) => {
  expect(SURFACE_GALLERY_SECTIONS.map((section) => section.path)).toContain(path);
});
