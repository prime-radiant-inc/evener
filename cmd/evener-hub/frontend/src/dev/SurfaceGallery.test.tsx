import { cleanup, render, screen } from "@testing-library/react";
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

test.each(SURFACE_GALLERY_SECTIONS)("mounts discovered section $path without throwing", (section) => {
  render(<SurfaceGallery sections={[section]} />);
  expect(screen.getAllByRole("heading", { level: 2 }).length).toBeGreaterThan(0);
});

test("the transcript section is registered", () => {
  const transcript = SURFACE_GALLERY_SECTIONS.find(({ path }) => path.endsWith("/transcript.tsx"));
  if (!transcript) throw new Error("transcript section is not registered");
  render(<SurfaceGallery sections={[transcript]} />);
  expect(screen.getByRole("heading", { level: 2, name: "Transcript" })).toBeTruthy();
});

test("the session chrome section is registered", () => {
  const chrome = SURFACE_GALLERY_SECTIONS.find(({ path }) => path.endsWith("/chrome.tsx"));
  if (!chrome) throw new Error("session chrome section is not registered");
  render(<SurfaceGallery sections={[chrome]} />);
  expect(screen.getByRole("heading", { level: 2, name: "Session chrome" })).toBeTruthy();
});
