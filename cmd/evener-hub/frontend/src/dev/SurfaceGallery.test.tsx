import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetAskDockStoreForTests } from "../panes/session/composer/askDock/askDockStore";
import { resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetThreadsStoreForTests } from "../stores/threads";
import { resetDisclosureStoreForTests } from "../widgets/disclosure/disclosureStore";
import SurfaceGallery, { SURFACE_GALLERY_SECTIONS } from "./SurfaceGallery";
import ChromeSurfaceSection from "./surface-sections/chrome";

test.each([
  ["confirmed-off", false],
  ["confirmed-on", true],
  ["unknown", false],
  ["read-only", true],
])("Manager gallery %s renders the real disconnected Details panel", (state, checked) => {
  render(<ChromeSurfaceSection />);
  const row = screen.getAllByTestId(`manager-fixture-${state}`)[0];
  fireEvent.click(within(row!).getByRole("button", { name: "Details" }));
  const toggle = screen.getByRole("switch", { name: "Manager" });
  expect(toggle.getAttribute("aria-checked")).toBe(String(checked));
  expect(toggle.hasAttribute("disabled")).toBe(true);
  if (state === "unknown") expect(screen.queryByTestId("session-details-manager-hub")).toBeNull();
  else
    expect(screen.getByTestId("session-details-manager-hub").textContent).toBe(
      state === "read-only" ? "fixture-other-hub" : "fixture-local-hub",
    );
});

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
