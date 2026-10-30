import type { HostRow, LaunchConfigResolved } from "@evener/appwire-client";
import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { installLocalStorage } from "../../../storageTestUtils";
import { resetConnectionStoreForTests } from "../../../stores/connection";
import { hostsStore } from "../../../stores/hosts";
import { resetLaunchConfigHostStoresForTests } from "../../../stores/launchConfig";
import { resetSettingsHostForTests, setSettingsHost } from "../../../stores/settingsHost";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { HostScopedSurface } from "./hostScopedSurface";
import { RepositoryConfigSection } from "./inrepo";

function RepositoryHostScope() {
  return <HostScopedSurface>{(host) => <RepositoryConfigSection host={host} cwd="/repo" />}</HostScopedSurface>;
}

// Repository review uses the shared settings host surface and launch gateway.
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
  // @ts-expect-error minimal storage fixture implements only the used methods
  installLocalStorage(new MemoryStorage());
});

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return {
    generation: 1,
    incarnationId: "inc-1",
    origin: "hub.toml",
    attached: false,
    midAttach: false,
    removed: false,
    ...overrides,
  };
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
  resetConnectionStoreForTests();
  resetLaunchConfigHostStoresForTests();
  resetToastStoreForTests();
  hostsStore.getState().resetForTests();
  resetSettingsHostForTests();
  localStorage.clear();
  window.history.pushState({}, "", "/settings/project?cwd=/repo");
  setSettingsHost("local");
});

afterEach(() => {
  cleanup();
  resetLaunchConfigHostStoresForTests();
  resetConnectionStoreForTests();
  window.history.pushState({}, "", "/");
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

test("with this hub selected the section issues the plain launch calls and never the proxy", async () => {
  localStorage.setItem("lastCwd", "/repo");
  const fake = connectReadyFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/resolve", () =>
    resolvedWithRepo({ path: ".evener/launch.toml", trust: "trusted", hash: "controller-hash" }),
  );
  fake.on("evener/path/validate", ({ path }) => ({ valid: true, path }));
  fake.on("evener/paths/complete", () => ({ data: [] }));
  fake.on("evener/host/request", () => {
    throw new Error("the local hub must never route through the proxy");
  });

  render(<RepositoryHostScope />);

  await screen.findByText("controller-hash");
  expect(fake.calls.some((call) => call.method === "evener/host/request")).toBe(false);
  expect(controllerLaunchCalls(fake)).toContain("evener/launch/resolve");
});

test("selecting a remote host resolves THAT host's in-repo config, never the controller's", async () => {
  localStorage.setItem("lastCwd", "/repo");
  const fake = connectReadyFakeClient();
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
  render(<RepositoryHostScope />);

  await screen.findByText("beta-hash");
  expect(controllerLaunchCalls(fake)).toEqual([]);
});

test("trusting a file on a remote host writes through the proxy, never this hub's trustRepo", async () => {
  localStorage.setItem("lastCwd", "/repo");
  const fake = connectReadyFakeClient();
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
  render(<RepositoryHostScope />);
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

test("a pending remote approval cannot refresh or change the newly selected local scope", async () => {
  const fake = connectReadyFakeClient();
  let release: (value: LaunchConfigResolved) => void = () => {
    throw new Error("approval not started");
  };
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/resolve", () =>
    resolvedWithRepo({ path: ".evener/launch.toml", trust: "trusted", hash: "local-hash" }),
  );
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { host: string; method: string; params: unknown };
    expect(forwarded.host).toBe("beta");
    if (forwarded.method === "evener/launch/resolve")
      return resolvedWithRepo({ path: ".evener/launch.toml", trust: "untrusted", hash: "beta-hash" }) as never;
    if (forwarded.method === "evener/launch/trustRepo") {
      expect(forwarded.params).toEqual({ cwd: "/repo", hash: "beta-hash" });
      return new Promise<LaunchConfigResolved>((resolve) => {
        release = resolve;
      }) as never;
    }
    throw new Error(`unexpected method ${forwarded.method}`);
  });
  setSettingsHost("beta");
  render(<RepositoryHostScope />);
  await userEvent.setup().click(await screen.findByRole("button", { name: /trust this file/i }));
  act(() => setSettingsHost("local"));
  await screen.findByText("local-hash");
  const reads = forwardedResolveCalls(fake, "beta");
  await act(async () =>
    release(resolvedWithRepo({ path: ".evener/launch.toml", trust: "trusted", hash: "beta-hash" })),
  );
  expect(screen.queryByText("beta-hash")).toBeNull();
  expect(screen.getByText("local-hash")).toBeTruthy();
  expect(forwardedResolveCalls(fake, "beta")).toBe(reads);
});

/** The resolve() reads the pane forwarded to `host` through the proxy. */
function forwardedResolveCalls(fake: FakeClient, host: string): number {
  return fake.calls.filter(
    (call) =>
      call.method === "evener/host/request" &&
      (call.params as { host: string }).host === host &&
      (call.params as { method: string }).method === "evener/launch/resolve",
  ).length;
}

// The in-repo pane converges too: the SELECTED host's own evener/launch/updated
// re-resolves this working directory, so a launch.toml that changed on the host
// (or a trust decision made elsewhere) reaches the pane.
test("a launch-config change for the selected host re-resolves the in-repo config", async () => {
  // The re-read waits out a 250ms debounce (useConnectedEffect.ts). Fake
  // timers own that clock, and the stubbed `jest` global lets Testing
  // Library's findBy/waitFor polls advance it, so the wait costs no real time.
  vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  localStorage.setItem("lastCwd", "/repo");
  const fake = connectReadyFakeClient();
  let hash = "beta-hash";
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string };
    if (forwarded.method === "evener/launch/resolve")
      return resolvedWithRepo({ path: ".evener/launch.toml", trust: "trusted", hash }) as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<RepositoryHostScope />);
  await screen.findByText("beta-hash");
  const reads = forwardedResolveCalls(fake, "beta");

  hash = "beta-hash-2";
  fake.emitNotification({
    method: "evener/host/notification",
    params: { host: "beta", method: "evener/launch/updated", params: {} },
  });

  await screen.findByText("beta-hash-2");
  expect(forwardedResolveCalls(fake, "beta")).toBe(reads + 1);
});
