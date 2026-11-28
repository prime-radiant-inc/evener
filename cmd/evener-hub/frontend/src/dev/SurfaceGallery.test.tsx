import { act, cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetAskDockStoreForTests } from "../panes/session/composer/askDock/askDockStore";
import { resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetThreadsStoreForTests } from "../stores/threads";
import { resetDisclosureStoreForTests } from "../widgets/disclosure/disclosureStore";
import { resetToastStoreForTests } from "../widgets/toast/store";
import SurfaceGallery, { SURFACE_GALLERY_SECTIONS } from "./SurfaceGallery";
import ChromeSurfaceSection from "./surface-sections/chrome";

test.each([
  ["ordinary", false],
  ["manager", true],
  ["existing", false],
])("Manager gallery %s projects a read-only session role", async (state, manager) => {
  await act(async () => {
    render(<ChromeSurfaceSection />);
  });
  const row = screen.getAllByTestId(`manager-fixture-${state}`)[0];
  if (!row) throw new Error("missing fixture row");
  expect(within(row).queryByRole("switch")).toBeNull();
  expect(within(row).queryByTestId("session-details-manager") !== null).toBe(manager);
});

afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  resetAskDockStoreForTests();
  resetDisclosureStoreForTests();
  resetNavigationStoreForTests();
  resetToastStoreForTests();
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
