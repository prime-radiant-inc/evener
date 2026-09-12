import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../../protocol/errors";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { SettingsOverviewResponse, UpdateCheckResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { APPLY_TIMEOUT_MS, resetHubUpdateStoreForTests } from "../../../stores/hubUpdate";
import { resetSettingsOverviewStoreForTests, settingsOverviewStore } from "../../../stores/settingsOverview";
import { HubSection } from "./hub";

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
});

afterEach(cleanup);

test("dev build shows the rebuild note and no update controls", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("dev"));

  render(<HubSection />);

  expect(await screen.findByText(/Dev build/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Check for updates" })).toBeNull();
  expect(fake.calls.map((c) => c.method)).toEqual(["evener/settings/overview"]);
});

test("release build checks on mount with the build channel and reports up to date", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => UP_TO_DATE);

  render(<HubSection />);

  expect(await screen.findByText(/Up to date on snapshot/)).toBeTruthy();
  // No Loader is rendered in this (non-restarting) state, so this role="status" match is unambiguous.
  expect(screen.getByRole("status").textContent).toMatch(/Up to date on snapshot/);
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

  render(<HubSection />);
  await screen.findByText(/Up to date on snapshot/);

  await userEvent.click(screen.getByRole("radio", { name: "Release" }));

  expect(await screen.findByText(/Up to date on release/)).toBeTruthy();
  expect(fake.calls).toContainEqual({ method: "evener/update/check", params: { channel: "release" } });
});

test("update available enables the button; confirming applies and shows restarting", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => overview("snapshot"));
  fake.on("evener/update/check", () => ({ ...UP_TO_DATE, updateAvailable: true, latestCommit: "be7002918fdc" }));
  fake.on("evener/update/apply", () => new Promise(() => {})); // stays pending: we only assert the request and the busy state

  render(<HubSection />);
  expect(await screen.findByText(/Update available: snapshot be70029/)).toBeTruthy();

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
  render(<HubSection />);
  await screen.findByText(/Up to date on snapshot/);

  const { hubUpdateStore } = await import("../../../stores/hubUpdate");
  hubUpdateStore.setState({ restarting: true });
  expect(await screen.findByText(/Restarting hub/)).toBeTruthy();

  hubUpdateStore.setState({ restarting: false, restartTimedOut: true });
  expect(await screen.findByText(/didn't come back within 30s/)).toBeTruthy();
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

  render(<HubSection />);
  expect(await screen.findByText(/rate limit/)).toBeTruthy();

  await userEvent.click(screen.getByRole("button", { name: "Check for updates" }));

  expect(await screen.findByText(/Up to date on release/)).toBeTruthy();
  expect(settingsOverviewStore.getState().data).not.toBeNull();
});
