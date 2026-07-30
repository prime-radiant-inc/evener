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
