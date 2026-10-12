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

/** Waits for the Updates status line to reach `state`. */
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
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetSettingsOverviewStoreForTests();
  resetHubUpdateStoreForTests({ fetchImpl: vi.fn() as unknown as typeof fetch, reload: vi.fn() });
  resetDaemonResidentsStoreForTests();
});

test("dev build shows the rebuild note and no update controls", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("dev"));
  // HubResidents now mounts inside HubSection after settings load and makes a
  // daemon/list call. Add a handler so the request succeeds and doesn't show
  // a spurious error banner; update the call assertion to include it.
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);

  expect(await screen.findByTestId("hub-update-dev-note")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Check for updates" })).toBeNull();
  await waitFor(() =>
    expect(fake.calls.map((c) => c.method)).toEqual(["evener/settings/overview", "evener/daemon/list"]),
  );
});

test("release build checks on mount with the build channel and reports up to date", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => UP_TO_DATE);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);

  await updateStateIs("up-to-date");
  // The status line lives in the Updates region, and names the checked channel.
  const updatesRegion = screen.getByRole("region", { name: "Updates" });
  expect(updatesRegion.contains(updateStatus())).toBe(true);
  expect(updateStatus().textContent).toContain("snapshot");
  expect(fake.calls).toContainEqual({ method: "evener/update/check", params: { channel: "snapshot" } });
  expect(screen.getByRole("radio", { name: "Snapshot" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.getByRole("button", { name: "Update and restart" })).toHaveProperty("disabled", true);
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

test("update available enables the button; confirming applies and shows restarting", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => ({ ...UP_TO_DATE, updateAvailable: true, latestCommit: "be7002918fdc" }));
  fake.on("evener/update/apply", () => new Promise(() => {})); // stays pending: we only assert the request and the busy state
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);
  await updateStateIs("update-available");
  expect(updateStatus().textContent).toContain("be70029");

  const button = screen.getByRole("button", { name: "Update and restart" });
  expect(button).toHaveProperty("disabled", false);
  await userEvent.click(button);
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
