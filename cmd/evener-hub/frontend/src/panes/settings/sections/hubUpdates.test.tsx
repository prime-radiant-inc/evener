import type { SettingsOverviewResponse, UpdateCheckResponse } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { resetDaemonResidentsStoreForTests } from "../../../stores/daemonResidents";
import { APPLY_TIMEOUT_MS, resetHubUpdateStoreForTests } from "../../../stores/hubUpdate";
import { resetSettingsOverviewStoreForTests, settingsOverviewStore } from "../../../stores/settingsOverview";
import { HubSection } from "./hub";

function updateStatus(): HTMLElement {
  return screen.getByTestId("hub-update-status");
}

async function updateStateIs(state: string): Promise<void> {
  await waitFor(() => expect(updateStatus().getAttribute("data-state")).toBe(state));
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function overview(buildChannel: string): SettingsOverviewResponse {
  return {
    hub: {
      version: "3b1c5f8",
      commit: "3b1c5f8",
      buildChannel,
      listenAddr: "127.0.0.1:9180",
      runDir: "/tmp/run",
      spawnTimeout: "30s",
      daemonIdleTimeoutMillis: 3600000,
    },
  };
}

const UP_TO_DATE: UpdateCheckResponse = {
  channel: "snapshot",
  buildChannel: "snapshot",
  currentVersion: "3b1c5f8",
  currentCommit: "3b1c5f8",
  latestTag: "snapshot",
  latestCommit: "3b1c5f8ffffffff",
  updateAvailable: false,
  applicable: true,
  onDiskCommit: "3b1c5f8",
  installAvailable: false,
  restartPending: false,
  startedAt: "2026-10-21T12:00:00Z",
  restartSupported: true,
  installTargetMatchesLaunch: true,
};

const UPDATE_AVAILABLE: UpdateCheckResponse = {
  ...UP_TO_DATE,
  updateAvailable: true,
  installAvailable: true,
  latestCommit: "be7002918fdc",
};

const INSTALLED: UpdateCheckResponse = {
  ...UP_TO_DATE,
  onDiskCommit: "be70029",
  installAvailable: false,
  restartPending: true,
  latestCommit: "be7002918fdc",
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetSettingsOverviewStoreForTests();
  resetHubUpdateStoreForTests({ fetchImpl: vi.fn() as unknown as typeof fetch, reload: vi.fn() });
  resetDaemonResidentsStoreForTests();
});

test("dev build shows the rebuild note, the on-disk build, and Restart without Install", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("dev"));
  fake.on("evener/update/check", () => ({
    ...UP_TO_DATE,
    buildChannel: "dev",
    applicable: false,
    updateAvailable: false,
    installAvailable: false,
    restartSupported: true,
    installTargetMatchesLaunch: true,
  }));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);

  expect(await screen.findByTestId("hub-update-dev-note")).toBeTruthy();
  expect(screen.getByTestId("hub-on-disk-build").textContent).toContain("3b1c5f8");
  expect(screen.getByRole("button", { name: "Restart hub" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Install update" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Check for updates" })).toBeNull();
});

test("dev build shows restart progress, timeout, and failure states", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("dev"));
  fake.on("evener/update/check", () => ({
    ...UP_TO_DATE,
    buildChannel: "dev",
    applicable: false,
    updateAvailable: false,
    installAvailable: false,
    restartSupported: true,
    installTargetMatchesLaunch: true,
  }));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  expect(await screen.findByTestId("hub-update-dev-note")).toBeTruthy();

  const { hubUpdateStore } = await import("../../../stores/hubUpdate");
  act(() => hubUpdateStore.setState({ restarting: true }));
  expect(await screen.findByTestId("hub-dev-restarting")).toBeTruthy();

  act(() => hubUpdateStore.setState({ restarting: false, restartTimedOut: true }));
  expect(await screen.findByTestId("hub-dev-restart-timeout")).toBeTruthy();

  act(() => hubUpdateStore.setState({ restartTimedOut: false, restartError: "no launch path" }));
  expect(await screen.findByTestId("hub-dev-restart-error")).toBeTruthy();
});

test("dev build retries a failed check when the connection becomes ready", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("dev"));
  let attempts = 0;
  fake.on("evener/update/check", () => {
    attempts += 1;
    if (attempts === 1) throw new WireError("socket hung up", -1);
    return { ...UP_TO_DATE, buildChannel: "dev", applicable: false };
  });
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  await waitFor(() => expect(attempts).toBe(1));
  expect(await screen.findByTestId("hub-dev-check-error")).toBeTruthy();

  act(() => connectionStore.setState({ state: "reconnecting" }));
  act(() => connectionStore.setState({ state: "ready" }));
  await waitFor(() => expect(attempts).toBe(2));
  expect(screen.queryByTestId("hub-dev-check-error")).toBeNull();
});

test("re-checks when the connection becomes ready again", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  let checks = 0;
  fake.on("evener/update/check", () => {
    checks += 1;
    return { ...UPDATE_AVAILABLE };
  });
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  await updateStateIs("update-available");
  expect(checks).toBe(1);

  act(() => connectionStore.setState({ state: "reconnecting" }));
  act(() => connectionStore.setState({ state: "ready" }));
  await waitFor(() => expect(checks).toBe(2));
});

test("release build checks on mount and separates Install from Restart", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => UP_TO_DATE);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);

  await updateStateIs("up-to-date");
  expect(fake.calls).toContainEqual({ method: "evener/update/check", params: { channel: "snapshot" } });
  expect(screen.getByRole("radio", { name: "Snapshot" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.getByRole("button", { name: "Install update" })).toHaveProperty("disabled", true);
  expect(screen.getByRole("button", { name: "Restart hub" })).toHaveProperty("disabled", false);
});

test("shows dirty build versions and never falls back to the product version", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => ({
    hub: { ...overview("snapshot").hub!, version: "0.1.0", commit: "3b1c5f8" },
  }));
  fake.on("evener/update/check", () => ({
    ...UP_TO_DATE,
    currentVersion: "3b1c5f8-dirty",
    currentCommit: "3b1c5f8",
    onDiskCommit: "3b1c5f8-dirty",
  }));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  await updateStateIs("up-to-date");

  const updates = screen.getByRole("region", { name: "Updates" });
  expect(updates.textContent).toContain("3b1c5f8-dirty");
  expect(updates.textContent).not.toContain("0.1.0");
});

test("keeps Restart available when only the channel check fails", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => ({
    ...UP_TO_DATE,
    checkError: "rate limit exceeded",
    updateAvailable: false,
    installAvailable: false,
    restartSupported: true,
    installTargetMatchesLaunch: true,
  }));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  await updateStateIs("check-failed");
  expect(screen.getByRole("button", { name: "Restart hub" })).toBeTruthy();
});

test("disables Install when the managed target differs from the running binary", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => ({
    ...UPDATE_AVAILABLE,
    installAvailable: false,
    installTarget: "/opt/evener",
    installTargetMatchesLaunch: false,
  }));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  await updateStateIs("update-available");
  expect(screen.getByRole("button", { name: "Install update" })).toHaveProperty("disabled", true);
  expect(screen.getByTestId("hub-install-target-mismatch").textContent).toContain("/opt/evener");
});

test("does not warn about the install target on a build that cannot install", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("dev"));
  fake.on("evener/update/check", () => ({
    ...UP_TO_DATE,
    buildChannel: "dev",
    applicable: false,
    installTarget: "/opt/evener",
    installTargetMatchesLaunch: false,
  }));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  expect(await screen.findByTestId("hub-update-dev-note")).toBeTruthy();
  expect(screen.queryByTestId("hub-install-target-mismatch")).toBeNull();
});

test("does not fall back to the combined action on a current hub with restart disabled", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => ({
    ...UPDATE_AVAILABLE,
    restartSupported: false,
    installAvailable: false,
    installTarget: "/opt/evener",
    installTargetMatchesLaunch: false,
  }));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  await updateStateIs("update-available");
  expect(screen.queryByRole("button", { name: "Update and restart" })).toBeNull();
  expect(screen.getByRole("button", { name: "Install update" })).toHaveProperty("disabled", true);
  expect(screen.queryByRole("button", { name: "Restart hub" })).toBeNull();
  expect(screen.getByTestId("hub-install-target-mismatch")).toBeTruthy();
});

test("switching channel re-checks with the new channel", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", (params) => ({
    ...UP_TO_DATE,
    channel: params.channel ?? "",
    latestTag: params.channel === "release" ? "v0.1.0" : "snapshot",
  }));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  await updateStateIs("up-to-date");

  await userEvent.click(screen.getByRole("radio", { name: "Release" }));

  await waitFor(() => expect(updateStatus().textContent).toContain("release"));
  expect(updateStatus().getAttribute("data-state")).toBe("up-to-date");
  expect(fake.calls).toContainEqual({ method: "evener/update/check", params: { channel: "release" } });
});

test("Install sends evener/upgrade, re-checks, and shows the installed-but-not-running state", async () => {
  const fake = connectFakeClient();
  let checks = 0;
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => {
    checks += 1;
    return checks === 1 ? UPDATE_AVAILABLE : INSTALLED;
  });
  fake.on("evener/upgrade", () => ({}) as never);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  await updateStateIs("update-available");

  await userEvent.click(screen.getByRole("button", { name: "Install update" }));

  await waitFor(() =>
    expect(fake.calls).toContainEqual({
      method: "evener/upgrade",
      params: { requested: "snapshot" },
      opts: { timeoutMs: APPLY_TIMEOUT_MS },
    }),
  );
  await waitFor(() => expect(screen.getByTestId("hub-restart-pending").textContent).toContain("be70029"));
  expect(screen.getByRole("button", { name: "Install update" })).toHaveProperty("disabled", true);
  expect(screen.getByRole("button", { name: "Restart hub" })).toHaveProperty("disabled", false);
});

test("Restart confirms the target build and sends evener/hub/restart", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => INSTALLED);
  fake.on("evener/hub/restart", () => new Promise(() => {})); // pending: assert the request and the busy state
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  await updateStateIs("up-to-date");
  expect(screen.getByTestId("hub-restart-pending").textContent).toContain("be70029");

  await userEvent.click(screen.getByRole("button", { name: "Restart hub" }));
  const confirm = await screen.findByRole("button", { name: "Yes, restart" });
  expect(screen.getByRole("dialog").textContent).toContain("be70029");
  await userEvent.click(confirm);

  await waitFor(() =>
    expect(fake.calls).toContainEqual({
      method: "evener/hub/restart",
      params: {},
      opts: { timeoutMs: 30_000 },
    }),
  );
  expect(screen.getByRole("button", { name: "Restart hub" })).toHaveProperty("disabled", true);
});

test("an older hub without restart support falls back to the combined update action", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => ({
    ...UPDATE_AVAILABLE,
    installAvailable: undefined as unknown as boolean,
    restartSupported: undefined as unknown as boolean,
  }));
  fake.on("evener/update/apply", () => new Promise(() => {}));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  await updateStateIs("update-available");

  expect(screen.queryByRole("button", { name: "Restart hub" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Install update" })).toBeNull();
  const combined = screen.getByRole("button", { name: "Update and restart" });
  expect(combined).toHaveProperty("disabled", false);
  await userEvent.click(combined);
  await userEvent.click(await screen.findByRole("button", { name: "Yes, update and restart" }));
  await waitFor(() =>
    expect(fake.calls).toContainEqual({
      method: "evener/update/apply",
      params: { channel: "snapshot" },
      opts: { timeoutMs: APPLY_TIMEOUT_MS },
    }),
  );
});

test("restarting and timed-out states render their messages", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => UP_TO_DATE);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));
  render(<HubSection />);
  await updateStateIs("up-to-date");

  const { hubUpdateStore } = await import("../../../stores/hubUpdate");
  act(() => hubUpdateStore.setState({ restarting: true }));
  await updateStateIs("restarting");

  act(() => hubUpdateStore.setState({ restarting: false, restartTimedOut: true }));
  await updateStateIs("restart-timed-out");
});

test("check failure shows the error and a working Check for updates button", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("release"));
  let attempts = 0;
  fake.on("evener/update/check", () => {
    attempts += 1;
    if (attempts === 1) throw new WireError("GET x: 403 Forbidden: API rate limit exceeded", -1);
    return { ...UP_TO_DATE, channel: "release", latestTag: "v0.1.0" };
  });
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  await updateStateIs("check-failed");
  expect(updateStatus().textContent).toContain("403 Forbidden");

  await userEvent.click(screen.getByRole("button", { name: "Check for updates" }));

  await updateStateIs("up-to-date");
  expect(settingsOverviewStore.getState().data).not.toBeNull();
});
