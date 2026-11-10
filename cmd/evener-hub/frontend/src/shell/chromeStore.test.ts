import { expect, test } from "vitest";
import { chromeStore, resetChromeStoreForTests } from "./chromeStore";

test("paneTitle starts null - no pane has published yet", () => {
  resetChromeStoreForTests();
  expect(chromeStore.getState().paneTitle).toBeNull();
});

test("setPaneTitle publishes the focused pane's title", () => {
  resetChromeStoreForTests();
  chromeStore.getState().setPaneTitle("Move Side Open Control");
  expect(chromeStore.getState().paneTitle).toBe("Move Side Open Control");
});

test("setPaneTitle(null) clears the bar when the publishing pane unmounts", () => {
  resetChromeStoreForTests();
  chromeStore.getState().setPaneTitle("DocPane");
  chromeStore.getState().setPaneTitle(null);
  expect(chromeStore.getState().paneTitle).toBeNull();
});

test("resetChromeStoreForTests restores the null initial state", () => {
  chromeStore.getState().setPaneTitle("leftover");
  resetChromeStoreForTests();
  expect(chromeStore.getState().paneTitle).toBeNull();
});

test("setPaneDocumentsFor publishes a whole moved group in one write", () => {
  resetChromeStoreForTests();
  const opener = document.implementation.createHTMLDocument("opener");
  const popout = document.implementation.createHTMLDocument("popout");
  chromeStore.getState().setPaneDocumentsFor(["pane_a", "pane_b"], opener);
  const writes: ReadonlyMap<string, Document>[] = [];
  const unsubscribe = chromeStore.subscribe((state, previous) => {
    if (state.paneDocuments !== previous.paneDocuments) writes.push(state.paneDocuments);
  });
  chromeStore.getState().setPaneDocumentsFor(["pane_a", "pane_b"], popout);
  chromeStore.getState().setPaneDocumentsFor(["pane_a", "pane_b"], popout);
  chromeStore.getState().setPaneDocumentsFor(["pane_a", "pane_b"], null);
  unsubscribe();
  // The repeated move writes nothing: observers see one move, then one clear.
  expect(writes).toHaveLength(2);
  expect([...(writes[1] ?? new Map())]).toEqual([]);
});
