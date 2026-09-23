import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { LOCAL_HOST } from "../../stores/hostRouting";
import { resetSettingsHostForTests, settingsHostStore } from "../../stores/settingsHost";
import { settingsURL, useSettingsHostURLSync } from "./settingsHost";

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  resetSettingsHostForTests();
});

// The default must leave today's URLs byte-for-byte unchanged, so `local`
// emits exactly what paneToURL already emitted.
test("local is exactly the URL paneToURL already emitted", () => {
  expect(settingsURL("credentials", LOCAL_HOST)).toBe("/settings/credentials");
  expect(settingsURL(undefined, LOCAL_HOST)).toBe("/settings");
});

test("a remote host is carried as the host query parameter", () => {
  expect(settingsURL("credentials", "beta")).toBe("/settings/credentials?host=beta");
  expect(settingsURL(undefined, "beta")).toBe("/settings?host=beta");
});

test("a host name is percent-encoded", () => {
  expect(settingsURL("credentials", "a b")).toBe("/settings/credentials?host=a%20b");
});

// M-3: only a SETTINGS route may adopt a host. navigate() announces its own
// navigation with a popstate, so without the pathname check, navigating away
// from settings to a hostless URL reset the stored selection to local.
test("a popstate onto a non-settings route does not adopt its host", () => {
  settingsHostStore.setState({ host: "beta" });
  window.history.pushState({}, "", "/new?host=ghost");
  renderHook(() => useSettingsHostURLSync());

  act(() => {
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  expect(settingsHostStore.getState().host).toBe("beta");
});

test("a popstate onto a settings route still adopts", () => {
  window.history.pushState({}, "", "/settings/credentials?host=beta");
  renderHook(() => useSettingsHostURLSync());

  expect(settingsHostStore.getState().host).toBe("beta");

  act(() => {
    window.history.pushState({}, "", "/settings/credentials");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  expect(settingsHostStore.getState().host).toBe(LOCAL_HOST);
});

// A non-settings page that merely carries a host parameter must not seed the
// settings selection either: mounting the sync there adopts nothing.
test("mounting the sync away from settings adopts nothing", () => {
  window.history.pushState({}, "", "/new?host=ghost");
  renderHook(() => useSettingsHostURLSync());

  expect(settingsHostStore.getState().host).toBe(LOCAL_HOST);
});
