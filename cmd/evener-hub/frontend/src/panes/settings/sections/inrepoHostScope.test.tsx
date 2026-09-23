import type { HostRow, LaunchConfigResolved } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { installLocalStorage } from "../../../storageTestUtils";
import { connectionStore } from "../../../stores/connection";
import { hostsStore } from "../../../stores/hosts";
import { resetLaunchConfigHostStoresForTests } from "../../../stores/launchConfig";
import { resetSettingsHostForTests, setSettingsHost } from "../../../stores/settingsHost";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { InRepoHostScope } from "./inrepo";

// The In-repo config settings surface's host scope (component 07b): the shared
// HostPicker over the selected host, whose own in-repo launch.toml resolution,
// trust write and path helpers both show and change from here. Local is today's
// plain-call section; a remote selection routes every call through
// evener/host/request and never touches the controller's own filesystem or
// launch config.

// Node 26 shadows jsdom's real window.localStorage with its own (non-functional
// under vitest) global - the same MemoryStorage stand-in inrepo.test.tsx
// documents, needed here because the pane pre-fills its cwd from lastCwd.
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

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: false, midAttach: false, removed: false, ...overrides };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function resolvedWithRepo(repo: LaunchConfigResolved["repo"]): LaunchConfigResolved {
  return { effective: {}, layers: {}, provenance: {}, repo };
}

/** The calls this section makes about the launch config and the path helpers it
 * uses - the controller's own, i.e. NOT via the proxy. */
function controllerLaunchCalls(fake: FakeClient): string[] {
  return fake.calls
    .map((call) => call.method)
    .filter((method) => method.startsWith("evener/launch/") || method.startsWith("evener/path"));
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetLaunchConfigHostStoresForTests();
  resetToastStoreForTests();
  hostsStore.getState().resetForTests();
  resetSettingsHostForTests();
  localStorage.clear();
  window.history.pushState({}, "", "/settings/inrepo");
  setSettingsHost("local");
});

afterEach(() => {
  cleanup();
  resetLaunchConfigHostStoresForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  window.history.pushState({}, "", "/");
});

test("with this hub selected the section issues the plain launch calls and never the proxy", async () => {
  localStorage.setItem("lastCwd", "/repo");
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/resolve", () =>
    resolvedWithRepo({ path: ".evener/launch.toml", trust: "trusted", hash: "controller-hash" }),
  );
  fake.on("evener/path/validate", ({ path }) => ({ valid: true, path }));
  fake.on("evener/paths/complete", () => ({ data: [] }));
  fake.on("evener/host/request", () => {
    throw new Error("the local hub must never route through the proxy");
  });

  render(<InRepoHostScope sectionId="inrepo" />);

  await screen.findByText("controller-hash");
  expect(fake.calls.some((call) => call.method === "evener/host/request")).toBe(false);
  expect(controllerLaunchCalls(fake)).toContain("evener/launch/resolve");
});

test("selecting a remote host resolves THAT host's in-repo config, never the controller's", async () => {
  localStorage.setItem("lastCwd", "/repo");
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  // The controller's own handlers are booby-trapped: reaching any of them while
  // beta is selected is a host-scoping bug.
  fake.on("evener/launch/resolve", () => {
    throw new Error("a remote selection must not resolve against this hub");
  });
  fake.on("evener/path/validate", () => {
    throw new Error("a remote selection must not validate a path on this hub");
  });
  fake.on("evener/paths/complete", () => {
    throw new Error("a remote selection must not complete a path on this hub");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { host: string; method: string };
    expect(forwarded.host).toBe("beta");
    if (forwarded.method === "evener/launch/resolve")
      return resolvedWithRepo({ path: ".evener/launch.toml", trust: "trusted", hash: "beta-hash" }) as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<InRepoHostScope sectionId="inrepo" />);

  await screen.findByText("beta-hash");
  expect(controllerLaunchCalls(fake)).toEqual([]);
});

test("trusting a file on a remote host writes through the proxy, never this hub's trustRepo", async () => {
  localStorage.setItem("lastCwd", "/repo");
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/trustRepo", () => {
    throw new Error("a remote selection must not trust a file on this hub");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string };
    if (forwarded.method === "evener/launch/resolve")
      return resolvedWithRepo({ path: ".evener/launch.toml", trust: "untrusted", hash: "beta-hash" }) as never;
    if (forwarded.method === "evener/launch/trustRepo")
      return resolvedWithRepo({ path: ".evener/launch.toml", trust: "trusted", hash: "beta-hash" }) as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<InRepoHostScope sectionId="inrepo" />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: /trust this file/i }));

  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) =>
          call.method === "evener/host/request" &&
          (call.params as { method: string }).method === "evener/launch/trustRepo",
      ),
    ).toBe(true),
  );
  expect(fake.calls.some((call) => call.method === "evener/launch/trustRepo")).toBe(false);
});

test("browsing for a directory asks the selected host's filesystem, never the controller's", async () => {
  localStorage.setItem("lastCwd", "/repo");
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/resolve", () => {
    throw new Error("a remote selection must not resolve against this hub");
  });
  fake.on("evener/path/validate", () => {
    throw new Error("a remote selection must not validate a path on this hub");
  });
  fake.on("evener/paths/complete", () => {
    throw new Error("a remote selection must not complete a path on this hub");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string };
    if (forwarded.method === "evener/launch/resolve")
      return resolvedWithRepo({ path: ".evener/launch.toml", trust: "absent" }) as never;
    if (forwarded.method === "evener/path/validate") return { valid: true, path: "/repo" } as never;
    if (forwarded.method === "evener/paths/complete") return { data: [] } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<InRepoHostScope sectionId="inrepo" />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: /working dir/i }));

  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) =>
          call.method === "evener/host/request" &&
          (call.params as { method: string }).method === "evener/paths/complete",
      ),
    ).toBe(true),
  );
  expect(fake.calls.some((call) => call.method === "evener/path/validate")).toBe(false);
  expect(fake.calls.some((call) => call.method === "evener/paths/complete")).toBe(false);
});
