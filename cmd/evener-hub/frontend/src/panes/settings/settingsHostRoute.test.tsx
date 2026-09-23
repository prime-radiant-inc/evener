import type { HostRow } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { resetChromeStoreForTests } from "../../shell/chromeStore";
import { navigate } from "../../shell/routing";
import { resetWorkspaceStoreForTests } from "../../shell/workspace";
import { installLocalStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { resetCredentialsStoreForTests, resetHostInstancesForTests } from "../../stores/credentials";
import { LOCAL_HOST } from "../../stores/hostRouting";
import { hostsStore } from "../../stores/hosts";
import { resetPrefsStoreForTests } from "../../stores/prefs";
import { resetSettingsHostForTests, settingsHostStore } from "../../stores/settingsHost";
import Settings from "./Settings";

// The selected host is part of the SETTINGS ROUTE: switching sections keeps it,
// a URL that carries it restores it, and `local` (the default) leaves today's
// URLs byte-for-byte unchanged.
//
// Node 26 shadows jsdom's real window.localStorage with its own (non-functional
// under vitest) global - the same MemoryStorage stand-in Settings.test.tsx
// documents, needed because the last-visited-section memory persists through
// localStorage.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  installLocalStorage(new MemoryStorage());
});

function stubMatchMedia(matches: boolean) {
  window.matchMedia = vi.fn().mockReturnValue({
    matches,
    media: "",
    addEventListener: () => {},
    removeEventListener: () => {},
  }) as unknown as typeof window.matchMedia;
}

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: true, midAttach: false, removed: false, ...overrides };
}

// A connected hub whose host registry lists beta and whose beta partition
// answers the picker's and the remote view's reads.
function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta" })] }));
  fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  fake.on("evener/host/request", () => ({ instances: [], availableProviders: [] }));
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  stubMatchMedia(false);
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  resetHostInstancesForTests();
  hostsStore.getState().resetForTests();
  resetSettingsHostForTests();
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  // @ts-expect-error restores jsdom's own honest default between tests.
  delete window.matchMedia;
  localStorage.clear();
  resetPrefsStoreForTests();
  resetWorkspaceStoreForTests();
  resetChromeStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  resetSettingsHostForTests();
});

test("the default is local, so switching sections emits today's URL with no host", async () => {
  connectFakeClient();
  window.history.pushState({}, "", "/settings/credentials");
  render(<Settings params={{ section: "credentials" }} paneId="settings-1" focused={true} />);
  const user = userEvent.setup();

  await user.click(screen.getByRole("button", { name: "Theme" }));

  expect(settingsHostStore.getState().host).toBe(LOCAL_HOST);
  expect(window.location.pathname).toBe("/settings/theme");
  expect(window.location.search).toBe("");
});

test("a URL that names a host restores it", async () => {
  connectFakeClient();
  window.history.pushState({}, "", "/settings/credentials?host=beta");
  render(<Settings params={{ section: "credentials" }} paneId="settings-1" focused={true} />);

  expect(settingsHostStore.getState().host).toBe("beta");
  expect(((await screen.findByLabelText("Host")) as HTMLSelectElement).value).toBe("beta");
  // And the pane scoped itself to that host's own listing.
  expect(await screen.findByRole("heading", { name: "Providers on beta" })).toBeTruthy();
});

test("the selection survives a section switch, and the route keeps carrying it", async () => {
  connectFakeClient();
  window.history.pushState({}, "", "/settings/credentials?host=beta");
  render(<Settings params={{ section: "credentials" }} paneId="settings-1" focused={true} />);
  const user = userEvent.setup();
  await screen.findByRole("heading", { name: "Providers on beta" });

  await user.click(screen.getByRole("button", { name: "Theme" }));

  expect(settingsHostStore.getState().host).toBe("beta");
  expect(window.location.pathname).toBe("/settings/theme");
  expect(window.location.search).toBe("?host=beta");
});

// back/forward must move the selection with the route, not leave the store on
// the host the user navigated away from.
test("a popstate that names another host moves the selection", async () => {
  connectFakeClient();
  window.history.pushState({}, "", "/settings/credentials?host=beta");
  render(<Settings params={{ section: "credentials" }} paneId="settings-1" focused={true} />);
  expect(settingsHostStore.getState().host).toBe("beta");

  act(() => {
    window.history.pushState({}, "", "/settings/credentials?host=gamma");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  expect(settingsHostStore.getState().host).toBe("gamma");
});

// Back from ?host=beta lands on the hostless settings URL that preceded it. The
// route is the authority, so the selection returns to this hub: the pane, the
// address bar, and a reload of that URL all agree.
test("Back to a settings URL that names no host returns the selection to this hub", async () => {
  connectFakeClient();
  window.history.pushState({}, "", "/settings/credentials?host=beta");
  render(<Settings params={{ section: "credentials" }} paneId="settings-1" focused={true} />);
  expect(settingsHostStore.getState().host).toBe("beta");

  act(() => {
    window.history.pushState({}, "", "/settings/credentials");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  expect(settingsHostStore.getState().host).toBe(LOCAL_HOST);
  expect(await screen.findByRole("button", { name: "Connect provider" })).toBeTruthy();
});

// The other half of that invariant: an app-built navigation to a settings URL
// (the palette's navigate("/settings"), the mobile shell's own URL sync, a
// hand-off from another pane) carries the host the route already names, so it
// never produces the hostless URL that would reset the selection.
test("an app navigation to a settings URL carries the host the route already names", async () => {
  connectFakeClient();
  window.history.pushState({}, "", "/settings/credentials?host=beta");
  render(<Settings params={{ section: "credentials" }} paneId="settings-1" focused={true} />);

  act(() => navigate("/settings/theme"));

  expect(window.location.pathname).toBe("/settings/theme");
  expect(window.location.search).toBe("?host=beta");
  expect(settingsHostStore.getState().host).toBe("beta");
});
