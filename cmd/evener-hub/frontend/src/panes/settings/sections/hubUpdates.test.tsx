import type { SettingsOverviewResponse, UpdateCheckResponse } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "../../../stores/connection";
import { resetDaemonResidentsStoreForTests } from "../../../stores/daemonResidents";
import { APPLY_TIMEOUT_MS, resetHubUpdateStoreForTests } from "../../../stores/hubUpdate";
import { resetSettingsOverviewStoreForTests, settingsOverviewStore } from "../../../stores/settingsOverview";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { Toast } from "../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { HubSection } from "./hub";

function updateStatus(): HTMLElement {
  return screen.getByTestId("hub-update-status");
}

async function updateStateIs(state: string): Promise<void> {
  await waitFor(() => expect(updateStatus().getAttribute("data-state")).toBe(state));
}

function overview(buildChannel: string): SettingsOverviewResponse {
  return {
    hub: {
      version: "3b1c5f8",
      commit: "3b1c5f8",
      buildChannel,
      buildTime: "2026-11-24T14:22:08Z",
      listenAddr: "127.0.0.1:9180",
      runDir: "/tmp/run",
      spawnTimeout: "30s",
      daemonIdleTimeoutMillis: 3600000,
    },
  };
}

const REPO = "https://github.com/prime-radiant-inc/evener";

const UP_TO_DATE: UpdateCheckResponse = {
  channel: "snapshot",
  buildChannel: "snapshot",
  currentVersion: "3b1c5f8",
  currentCommit: "3b1c5f8",
  currentBuildTime: "2026-11-24T14:22:08Z",
  latestTag: "snapshot",
  latestCommit: "3b1c5f8ffffffff",
  latestPublishedAt: "2026-11-24T14:22:08Z",
  repoUrl: REPO,
  updateAvailable: false,
  applicable: true,
  onDiskCommit: "3b1c5f8",
  onDiskBuildTime: "2026-11-24T14:22:08Z",
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
  latestPublishedAt: "2026-11-25T02:10:14Z",
};

const INSTALLED_LATEST: UpdateCheckResponse = {
  ...UP_TO_DATE,
  onDiskCommit: "be70029",
  onDiskBuildTime: "2026-11-25T02:10:14Z",
  installAvailable: false,
  restartPending: true,
  latestCommit: "be7002918fdc",
  latestPublishedAt: "2026-11-25T02:10:14Z",
};

const INSTALLED_DISTINCT: UpdateCheckResponse = {
  ...UPDATE_AVAILABLE,
  onDiskCommit: "c48d112",
  onDiskBuildTime: "2026-11-25T01:30:00Z",
  restartPending: true,
};

function renderHub() {
  return render(
    <>
      <HubSection />
      <Toast />
    </>,
  );
}

beforeEach(() => {
  resetConnectionStoreForTests();
  resetSettingsOverviewStoreForTests();
  resetHubUpdateStoreForTests({ fetchImpl: vi.fn() as unknown as typeof fetch, reload: vi.fn() });
  resetDaemonResidentsStoreForTests();
  resetToastStoreForTests();
});

test("renders unified version links, compare link, and ISO date stamps for release updates", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => ({
    hub: { ...overview("release").hub!, commit: "8e12c04", releaseTag: "v0.4.1", buildTime: "2026-11-18T16:05:30Z" },
  }));
  fake.on("evener/update/check", () => ({
    ...UPDATE_AVAILABLE,
    channel: "release",
    buildChannel: "release",
    currentVersion: "8e12c04",
    currentCommit: "8e12c04",
    currentReleaseTag: "v0.4.1",
    currentBuildTime: "2026-11-18T16:05:30Z",
    latestTag: "v0.4.2",
    latestCommit: "9d4e21a8c01f",
    latestPublishedAt: "2026-11-22T18:40:00Z",
  }));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderHub();
  await updateStateIs("update-available");

  const hrefs = screen.getAllByRole("link").map((el) => el.getAttribute("href"));
  expect(hrefs).toContain(`${REPO}/releases/tag/v0.4.1`);
  expect(hrefs).toContain(`${REPO}/commit/8e12c04`);
  expect(hrefs).toContain(`${REPO}/releases/tag/v0.4.2`);
  expect(hrefs).toContain(`${REPO}/commit/9d4e21a8c01f`);
  expect(hrefs).toContain(`${REPO}/compare/8e12c04...9d4e21a8c01f`);

  const times = [...document.querySelectorAll("time")].map((el) => el.getAttribute("datetime"));
  expect(times).toContain("2026-11-18T16:05:30Z");
  expect(times).toContain("2026-11-22T18:40:00Z");
  // On disk matches running, so no separate on-disk row is shown.
  expect(screen.queryByTestId("hub-on-disk-build")).toBeNull();
});

test("shows a separate On disk row only when on-disk differs from both running and channel latest", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => INSTALLED_DISTINCT);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderHub();
  await updateStateIs("update-available");

  const onDiskRow = screen.getByTestId("hub-on-disk-build");
  expect(onDiskRow.textContent).toContain("c48d112");
  expect(screen.getByTestId("hub-restart-pending")).toBeTruthy();
});

test("Update and restart installs and automatically restarts the hub", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => UPDATE_AVAILABLE);
  fake.on("evener/upgrade", () => ({}) as never);
  fake.on("evener/hub/restart", () => new Promise(() => {}));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderHub();
  await updateStateIs("update-available");

  await userEvent.click(screen.getByRole("button", { name: "Update and restart" }));

  await waitFor(() =>
    expect(fake.calls).toContainEqual({
      method: "evener/upgrade",
      params: { requested: "snapshot" },
      opts: { timeoutMs: APPLY_TIMEOUT_MS },
    }),
  );
  await waitFor(() =>
    expect(fake.calls).toContainEqual({
      method: "evener/hub/restart",
      params: {},
      opts: { timeoutMs: 30_000 },
    }),
  );
  await updateStateIs("restarting");
});

test("local build shows channel controls and installs published build with automatic restart", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => overview("dev"));
  fake.on("evener/update/check", (params) => ({
    ...UP_TO_DATE,
    channel: params.channel ?? "",
    latestTag: "v0.1.0",
    buildChannel: "dev",
    applicable: true,
    updateAvailable: false,
    installAvailable: true,
    restartSupported: true,
    installTargetMatchesLaunch: true,
  }));
  fake.on("evener/upgrade", () => ({}) as never);
  fake.on("evener/hub/restart", () => new Promise(() => {}));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderHub();
  expect(await screen.findByTestId("hub-update-dev-note")).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Release" }).getAttribute("aria-checked")).toBe("true");
  expect(fake.calls).toContainEqual({ method: "evener/update/check", params: { channel: "release" } });

  const action = screen.getByRole("button", { name: "Update and restart" });
  expect(action).toHaveProperty("disabled", false);
  await userEvent.click(action);

  await waitFor(() =>
    expect(fake.calls).toContainEqual({
      method: "evener/upgrade",
      params: { requested: "release" },
      opts: { timeoutMs: APPLY_TIMEOUT_MS },
    }),
  );
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/hub/restart")).toBe(true));
});

test("offers Install update without restarting when the hub cannot restart itself", async () => {
  const fake = connectReadyFakeClient();
  let checks = 0;
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => {
    checks += 1;
    return checks === 1
      ? { ...UPDATE_AVAILABLE, restartSupported: false }
      : { ...INSTALLED_LATEST, restartSupported: false };
  });
  fake.on("evener/upgrade", () => ({}) as never);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderHub();
  await updateStateIs("update-available");
  expect(screen.queryByRole("button", { name: "Restart hub" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Update and restart" })).toBeNull();

  await userEvent.click(screen.getByRole("button", { name: "Install update" }));

  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/upgrade")).toBe(true));
  expect(fake.calls.some((c) => c.method === "evener/hub/restart")).toBe(false);
  await waitFor(() => expect(screen.getByTestId("hub-restart-pending")).toBeTruthy());
});

test("shows dirty build versions as plain text without broken GitHub links and never falls back to product version", async () => {
  const fake = connectReadyFakeClient();
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

  renderHub();
  await updateStateIs("up-to-date");

  const updates = screen.getByRole("region", { name: "Updates" });
  expect(updates.textContent).toContain("3b1c5f8-dirty");
  expect(updates.textContent).not.toContain("0.1.0");
  expect(screen.queryAllByRole("link")).toHaveLength(0);
});

test("re-checks when the connection becomes ready again", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  let checks = 0;
  fake.on("evener/update/check", () => {
    checks += 1;
    return { ...UPDATE_AVAILABLE };
  });
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderHub();
  await updateStateIs("update-available");
  expect(checks).toBe(1);

  act(() => connectionStore.setState({ state: "reconnecting" }));
  act(() => connectionStore.setState({ state: "ready" }));
  await waitFor(() => expect(checks).toBe(2));
});

test("up-to-date hub hides the update button and keeps Check for updates and Restart hub enabled", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => UP_TO_DATE);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderHub();
  await updateStateIs("up-to-date");
  expect(screen.getByRole("radio", { name: "Snapshot" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.queryByRole("button", { name: "Update and restart" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Install update" })).toBeNull();
  expect(screen.getByRole("button", { name: "Check for updates" })).toHaveProperty("disabled", false);
  expect(screen.getByRole("button", { name: "Restart hub" })).toHaveProperty("disabled", false);
});

test("check failure pushes an error toast without rendering an inline danger notice, and keeps Check and Restart working", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => overview("release"));
  let attempts = 0;
  fake.on("evener/update/check", () => {
    attempts += 1;
    if (attempts === 1) throw new WireError("GET x: 403 Forbidden: API rate limit exceeded", -1);
    return { ...UP_TO_DATE, channel: "release", latestTag: "v0.1.0" };
  });
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderHub();
  await updateStateIs("check-failed");

  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text.includes("403 Forbidden"))).toBe(true),
  );
  expect(screen.queryByRole("alert")).toBeNull();

  await userEvent.click(screen.getByRole("button", { name: "Check for updates" }));
  await updateStateIs("up-to-date");
  expect(settingsOverviewStore.getState().data).not.toBeNull();
});

test("channel check error still keeps Restart hub available and reports the failure via toast", async () => {
  const fake = connectReadyFakeClient();
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

  renderHub();
  await updateStateIs("check-failed");
  expect(screen.getByRole("button", { name: "Restart hub" })).toBeTruthy();
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text.includes("rate limit exceeded"))).toBe(true),
  );
});

test("install failure reports via error toast without rendering an inline danger notice", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => UPDATE_AVAILABLE);
  fake.on("evener/upgrade", () => {
    throw new WireError("checksum mismatch", -32000);
  });
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderHub();
  await updateStateIs("update-available");
  await userEvent.click(screen.getByRole("button", { name: "Update and restart" }));

  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text.includes("checksum mismatch"))).toBe(true),
  );
  expect(screen.queryByRole("alert")).toBeNull();
});

test("hides update CTA and renders structural notice when managed target differs from running binary", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => ({
    ...UPDATE_AVAILABLE,
    installAvailable: false,
    installTarget: "/opt/evener",
    installTargetMatchesLaunch: false,
  }));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderHub();
  await updateStateIs("update-available");
  expect(screen.queryByRole("button", { name: "Update and restart" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Install update" })).toBeNull();
  expect(screen.getByTestId("hub-install-target-mismatch").textContent).toContain("/opt/evener");
});

test("switching channel re-checks with the new channel", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", (params) => ({
    ...UP_TO_DATE,
    channel: params.channel ?? "",
    latestTag: params.channel === "release" ? "v0.1.0" : "snapshot",
  }));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderHub();
  await updateStateIs("up-to-date");

  await userEvent.click(screen.getByRole("radio", { name: "Release" }));
  await updateStateIs("up-to-date");
  expect(fake.calls).toContainEqual({ method: "evener/update/check", params: { channel: "release" } });
});

test("staged on-disk build promotes Restart to apply and confirms before sending evener/hub/restart", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => INSTALLED_LATEST);
  fake.on("evener/hub/restart", () => new Promise(() => {}));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderHub();
  await updateStateIs("up-to-date");
  expect(screen.getByTestId("hub-restart-pending")).toBeTruthy();

  await userEvent.click(screen.getByRole("button", { name: "Restart to apply" }));
  const confirm = await screen.findByRole("button", { name: "Restart hub" });
  expect(screen.getByRole("dialog").textContent).toContain("be70029");
  await userEvent.click(confirm);

  await waitFor(() =>
    expect(fake.calls).toContainEqual({
      method: "evener/hub/restart",
      params: {},
      opts: { timeoutMs: 30_000 },
    }),
  );
  expect(screen.getByRole("button", { name: "Restart to apply" })).toHaveProperty("disabled", true);
});

test("an older hub without restart support falls back to the combined update action", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => ({
    ...UPDATE_AVAILABLE,
    installAvailable: undefined as unknown as boolean,
    restartSupported: undefined as unknown as boolean,
  }));
  fake.on("evener/update/apply", () => new Promise(() => {}));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderHub();
  await updateStateIs("update-available");

  expect(screen.queryByRole("button", { name: "Restart hub" })).toBeNull();
  const combined = screen.getByRole("button", { name: "Update and restart" });
  expect(combined).toHaveProperty("disabled", false);
  await userEvent.click(combined);
  await waitFor(() =>
    expect(fake.calls).toContainEqual({
      method: "evener/update/apply",
      params: { channel: "snapshot" },
      opts: { timeoutMs: APPLY_TIMEOUT_MS },
    }),
  );
});

test("restarting and timed-out states update status and push a timeout toast", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => UP_TO_DATE);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));
  renderHub();
  await updateStateIs("up-to-date");

  const { hubUpdateStore } = await import("../../../stores/hubUpdate");
  act(() => hubUpdateStore.setState({ restarting: true }));
  await updateStateIs("restarting");

  act(() => hubUpdateStore.setState({ restarting: false, restartTimedOut: true }));
  await updateStateIs("restart-timed-out");
  await waitFor(() => expect(getToasts().some((t) => t.kind === "error")).toBe(true));
});
