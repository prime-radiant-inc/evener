import type { SettingsOverviewResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { resetSettingsOverviewStoreForTests } from "../../../stores/settingsOverview";
import { statusReasons } from "./reasonTestUtils";
import { settingsFieldHelp, settingsFieldIds, settingsFieldValue } from "./settingsFieldTestUtils";
import { StorageSection } from "./storage";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetSettingsOverviewStoreForTests();
});

test("renders State dir (from storage, not hub), Run dir (from hub), and the static hub.toml row", async () => {
  const fake = connectFakeClient();
  const response: SettingsOverviewResponse = {
    hub: { runDir: "/tmp/evener-run", daemonIdleTimeoutMillis: 3600000 },
    storage: { stateDir: "/home/user/.evener" },
  };
  fake.on("evener/settings/overview", () => response);

  render(<StorageSection />);

  expect(await screen.findByText("/home/user/.evener")).toBeTruthy();
  expect(screen.getByText("/tmp/evener-run")).toBeTruthy();
  expect(settingsFieldIds()).toEqual(["state-dir", "run-dir", "hub-config", "session-cache"]);
  expect(settingsFieldValue("state-dir").textContent).toBe("/home/user/.evener");
  expect(settingsFieldHelp("hub-config")).not.toBeNull();
});

test("Hub config's dd carries no 'edit to change' dim hint (unlike General's own row)", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => ({ hub: {}, storage: {} }) as SettingsOverviewResponse);

  render(<StorageSection />);

  await waitFor(() => expect(settingsFieldIds()).toContain("hub-config"));
  // The value is the path alone, with no note beside it.
  const configValue = settingsFieldValue("hub-config");
  expect(configValue.textContent).toBe(configValue.querySelector("code")?.textContent);
});

test("omits the Past index row entirely when pastIndex is absent (no configured past-session index)", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => ({ hub: {}, storage: {} }) as SettingsOverviewResponse);

  render(<StorageSection />);

  await waitFor(() => expect(settingsFieldIds()).toEqual(["state-dir", "run-dir", "hub-config", "session-cache"]));
});

test("renders the Past index row with its size and a pluralized live session count when present", async () => {
  const fake = connectFakeClient();
  const response: SettingsOverviewResponse = {
    hub: {
      pastIndex: { path: "~/.evener/past.db", size: "size-from-hub", perPage: 20, count: 3 },
      daemonIdleTimeoutMillis: 3600000,
    },
    storage: { stateDir: "/home/user/.evener" },
  };
  fake.on("evener/settings/overview", () => response);

  render(<StorageSection />);

  await waitFor(() => expect(settingsFieldIds()).toContain("past-index"));
  expect(within(settingsFieldValue("past-index")).getByText("size-from-hub")).toBeTruthy();
  expect(screen.getByText(/Currently tracking 3 sessions\./)).toBeTruthy();
});

test("pluralizes 'session' (not 'sessions') when the count is exactly 1", async () => {
  const fake = connectFakeClient();
  const response: SettingsOverviewResponse = {
    hub: {
      pastIndex: { path: "~/.evener/past.db", perPage: 20, count: 1 },
      daemonIdleTimeoutMillis: 3600000,
    },
    storage: {},
  };
  fake.on("evener/settings/overview", () => response);

  render(<StorageSection />);

  expect(await screen.findByText(/Currently tracking 1 session\./)).toBeTruthy();
});

test("a load failure is a polite danger notice whose Retry re-requests the overview", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => {
    throw new Error("raw-overview-failure");
  });
  const user = userEvent.setup();

  render(<StorageSection />);

  await waitFor(() => expect(statusReasons(document.body)).toEqual(["unavailable"]));
  expect(screen.queryByTestId("empty-state")).toBeNull();
  expect(screen.queryByText("raw-overview-failure", { exact: false })).toBeNull();
  const notice = document.querySelector<HTMLElement>('[role="status"][data-reason="unavailable"]');
  if (notice === null) throw new Error("load failure shows no notice");

  fake.on("evener/settings/overview", () => ({ hub: {}, storage: {} }) as SettingsOverviewResponse);
  await user.click(within(notice).getByRole("button", { name: "Retry" }));

  await waitFor(() => expect(settingsFieldIds()).toContain("hub-config"));
  expect(fake.calls.filter((call) => call.method === "evener/settings/overview")).toHaveLength(2);
});
