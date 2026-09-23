import { afterEach, expect, test } from "vitest";
import { LOCAL_HOST } from "./hostRouting";
import {
  adoptSettingsHostFromURL,
  normalizeHost,
  readHostFromSearch,
  resetSettingsHostForTests,
  setSettingsHost,
  settingsHostStore,
} from "./settingsHost";

afterEach(resetSettingsHostForTests);

test("an absent, empty, or local spelling reads as the local hub", () => {
  expect(normalizeHost(null)).toBe(LOCAL_HOST);
  expect(normalizeHost("")).toBe(LOCAL_HOST);
  expect(normalizeHost(LOCAL_HOST)).toBe(LOCAL_HOST);
  expect(readHostFromSearch("")).toBe(LOCAL_HOST);
  expect(readHostFromSearch("?host=")).toBe(LOCAL_HOST);
  expect(readHostFromSearch("?host=local")).toBe(LOCAL_HOST);
});

test("a named host is read from the search string", () => {
  expect(readHostFromSearch("?host=beta")).toBe("beta");
  expect(readHostFromSearch("?cwd=/x&host=gamma")).toBe("gamma");
});

test("setSettingsHost normalizes and is the store's one writer", () => {
  setSettingsHost("beta");
  expect(settingsHostStore.getState().host).toBe("beta");
  setSettingsHost(LOCAL_HOST);
  expect(settingsHostStore.getState().host).toBe(LOCAL_HOST);
});

test("a URL that names a host is adopted", () => {
  setSettingsHost(LOCAL_HOST);
  adoptSettingsHostFromURL("?host=beta");
  expect(settingsHostStore.getState().host).toBe("beta");
});

// The route is the authority: a settings URL with no host names the local hub.
// That is safe to adopt because the app's own settings navigations always carry
// the host (shell/routing.ts's navigate), so absence only ever comes from a
// deliberate local URL or a URL the app did not build.
test("a settings URL that names no host means the local hub", () => {
  setSettingsHost("beta");
  adoptSettingsHostFromURL("");
  expect(settingsHostStore.getState().host).toBe(LOCAL_HOST);

  setSettingsHost("beta");
  adoptSettingsHostFromURL("?cwd=/x");
  expect(settingsHostStore.getState().host).toBe(LOCAL_HOST);
});
