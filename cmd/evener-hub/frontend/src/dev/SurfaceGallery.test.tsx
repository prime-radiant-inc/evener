import { act, cleanup, render, screen } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetAskDockStoreForTests } from "../panes/session/composer/askDock/askDockStore";
import { resetPendingTurnsStoreForTests } from "../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import * as composerSources from "../panes/session/composer/sourceState";
import { getBrowserDraftRepository, resetBrowserDraftRepositoryForTests } from "../stores/browserDraftRepository";
import { resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetThreadsStoreForTests } from "../stores/threads";
import { resetDisclosureStoreForTests } from "../widgets/disclosure/disclosureStore";
import SurfaceGallery, { SURFACE_GALLERY_SECTIONS } from "./SurfaceGallery";

const sources: composerSources.ComposerSourceState[] = [];
beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  resetBrowserDraftRepositoryForTests();
  const create = composerSources.createComposerSourceState;
  vi.spyOn(composerSources, "createComposerSourceState").mockImplementation((...args) => {
    const source = create(...args);
    sources.push(source);
    return source;
  });
});

afterEach(async () => {
  cleanup();
  await act(async () => Promise.all(sources.map((source) => source.whenSettled())));
  await flushPendingTurnsProjectionForTests();
  act(() => {
    resetThreadsStoreForTests();
    resetPendingTurnsStoreForTests();
    resetAskDockStoreForTests();
    resetDisclosureStoreForTests();
    resetNavigationStoreForTests();
  });
  sources.length = 0;
  vi.restoreAllMocks();
  resetBrowserDraftRepositoryForTests();
  localStorage.clear();
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
  const seed = vi.spyOn(getBrowserDraftRepository(), "seedDraft");
  await act(async () => {
    render(<SurfaceGallery sections={[section]} />);
  });
  await act(async () => {
    await Promise.all(seed.mock.results.map((result) => result.value));
  });
  await act(async () => {
    await Promise.all(sources.map((source) => source.ready()));
    await Promise.all(sources.map((source) => source.whenSettled()));
  });
  await flushPendingTurnsProjectionForTests();
  expect(screen.getAllByRole("heading", { level: 2 }).length).toBeGreaterThan(0);
  const requiredHeading = REQUIRED_SECTION_HEADINGS[section.path];
  if (requiredHeading) expect(screen.getByRole("heading", { level: 2, name: requiredHeading })).toBeTruthy();
});

test.each(Object.keys(REQUIRED_SECTION_HEADINGS))("the section %s is registered", (path) => {
  expect(SURFACE_GALLERY_SECTIONS.map((section) => section.path)).toContain(path);
});
