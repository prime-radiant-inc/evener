import { act, cleanup, fireEvent, render, within } from "@testing-library/react";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetAskDockStoreForTests } from "../../panes/session/composer/askDock/askDockStore";
import { resetPendingTurnsStoreForTests } from "../../panes/session/composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "../../panes/session/composer/queue/testing/flushPendingTurnsProjection";
import * as composerSources from "../../panes/session/composer/sourceState";
import { replaceEditorText } from "../../panes/session/testing/editor";
import { getBrowserDraftRepository, resetBrowserDraftRepositoryForTests } from "../../stores/browserDraftRepository";
import { resetThreadsStoreForTests } from "../../stores/threads";
import ComposerSurfaceSection from "./composer";

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
  });
  sources.length = 0;
  vi.restoreAllMocks();
  resetBrowserDraftRepositoryForTests();
  vi.unstubAllGlobals();
  localStorage.clear();
});

test("each themed composer has a dedicated pane-width fixture and retains editable drafts and question selection", async () => {
  const seed = vi.spyOn(getBrowserDraftRepository(), "seedDraft");
  const { container } = render(<ComposerSurfaceSection />);
  // Join the real fixture commit before React exposes its real sources.
  await act(async () => Promise.all(seed.mock.results.map((result) => result.value)));
  await act(async () => {
    await Promise.all(sources.map((source) => source.ready()));
    await Promise.all(sources.map((source) => source.whenSettled()));
  });
  await flushPendingTurnsProjectionForTests();
  const panes = container.querySelectorAll("[data-theme]");
  expect(panes).toHaveLength(2);
  for (const pane of panes) {
    const resting = within(pane as HTMLElement).getByText("resting").parentElement;
    const drafted = within(pane as HTMLElement).getByText("drafted").parentElement;
    const pending = within(pane as HTMLElement).getByText("ask pending (transcript trailing row)").parentElement;
    for (const fixture of [resting, drafted, pending]) {
      expect(fixture?.className).toMatch(/paneFixture/);
    }
    if (!drafted || !pending) throw new Error("missing gallery fixture");
    const input = within(drafted).getByRole("textbox");
    expect(input.textContent).toContain("CHANGELOG");
    replaceEditorText(input, "Keep this editable");
    expect(input.textContent).toBe("Keep this editable");
    expect(document.activeElement).toBe(input);
    expect(within(drafted).getByRole("button", { name: /^Send$/ })).toBeTruthy();
    const choice = within(pending).getByRole("radio", { name: /No, main only/ }) as HTMLInputElement;
    fireEvent.click(choice);
    expect(choice.checked).toBe(true);
  }
});

test("missing IndexedDB reports the composer fixture failure without publishing sources or an unhandled rejection", async () => {
  // Exercise the actual constructor throw, not a fabricated seed outcome.
  resetBrowserDraftRepositoryForTests();
  vi.stubGlobal("indexedDB", undefined);
  const view = render(<ComposerSurfaceSection />);
  try {
    expect((await view.findByRole("alert")).textContent).toBe("Couldn't save the composer fixture locally");
  } finally {
    expect(sources).toEqual([]);
    expect(view.queryAllByRole("textbox")).toEqual([]);
  }
  // Vitest's unchanged unhandled-error gate must also remain clean.
});
