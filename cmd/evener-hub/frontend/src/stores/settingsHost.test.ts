import { afterEach, expect, test } from "vitest";
import { LOCAL_HOST } from "./hostRouting";
import {
  adoptSettingsHostFromURL,
  initialSettingsHost,
  normalizeHost,
  readHostFromSearch,
  resetSettingsHostForTests,
  setSettingsHost,
  settingsHostStore,
} from "./settingsHost";

afterEach(() => {
  resetSettingsHostForTests();
  window.history.pushState({}, "", "/");
});

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
  window.history.pushState({}, "", "/settings/credentials?host=beta");
  setSettingsHost(LOCAL_HOST);
  adoptSettingsHostFromURL();
  expect(settingsHostStore.getState().host).toBe("beta");
});

// The route is the authority: a settings URL with no host names the local hub.
// That is safe to adopt because the app's own settings navigations always carry
// the host (shell/routing.ts's navigate), so absence only ever comes from a
// deliberate local URL or a URL the app did not build.
test("a settings URL that names no host means the local hub", () => {
  window.history.pushState({}, "", "/settings/credentials");
  setSettingsHost("beta");
  adoptSettingsHostFromURL();
  expect(settingsHostStore.getState().host).toBe(LOCAL_HOST);

  window.history.pushState({}, "", "/settings/credentials?cwd=/x");
  setSettingsHost("beta");
  adoptSettingsHostFromURL();
  expect(settingsHostStore.getState().host).toBe(LOCAL_HOST);
});

// The route gate belongs to the store, not to its caller: this is the third
// variant of "read or write the host context without asking whether the current
// route is a settings route", so the one function that adopts from the URL now
// asks it itself and cannot be called around.
test("adopting is refused on a non-settings route", () => {
  setSettingsHost("beta");
  window.history.pushState({}, "", "/new?host=ghost");

  adoptSettingsHostFromURL();

  expect(settingsHostStore.getState().host).toBe("beta");
});

// L-1 (round 4): the store seeded from the query on ANY route, while the URL sync
// deliberately adopts only on a settings route. `host` is not a
// settings-exclusive parameter, so a non-settings URL that carries one (a
// hand-typed /new?host=ghost) seeded a remote selection that the pane's first
// render then fetched - a spurious evener/host/request for a host that is not
// configured, and a flash of its refusal, before the shell's own adoption ran.
test("a fresh load seeds a host only from a settings route", () => {
  expect(initialSettingsHost("/new", "?host=ghost")).toBe(LOCAL_HOST);
  expect(initialSettingsHost("/", "?host=ghost")).toBe(LOCAL_HOST);
  expect(initialSettingsHost("/s/local:abc", "?host=ghost")).toBe(LOCAL_HOST);
});

test("a fresh load seeds what a settings URL names, aliases included", () => {
  expect(initialSettingsHost("/settings/credentials", "?host=beta")).toBe("beta");
  expect(initialSettingsHost("/settings", "?host=beta")).toBe("beta");
  expect(initialSettingsHost("/credentials", "?host=beta")).toBe("beta");
  expect(initialSettingsHost("/settings/providers", "?host=beta")).toBe("beta");
  // A settings URL that names no host (or the local one) is the local hub.
  expect(initialSettingsHost("/settings/credentials", "")).toBe(LOCAL_HOST);
  expect(initialSettingsHost("/settings/credentials", "?host=local")).toBe(LOCAL_HOST);
});
