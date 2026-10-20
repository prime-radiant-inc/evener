import { friendlyErrorMessage, type SettingsOverviewResponse, WireError } from "@evener/appwire-client";
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
  await userEvent.setup().click(await screen.findByText("Technical details", { selector: "summary" }));

  expect(await screen.findByText("/home/user/.evener")).toBeTruthy();
  expect(screen.getByText("/tmp/evener-run")).toBeTruthy();
  expect(settingsFieldIds()).toEqual(["session-cache", "state-dir", "run-dir", "hub-config"]);
  expect(settingsFieldValue("state-dir").textContent).toBe("/home/user/.evener");
  expect(settingsFieldHelp("hub-config")).not.toBeNull();
});

test("Hub config renders its path without a separate value hint", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => ({ hub: {}, storage: {} }) as SettingsOverviewResponse);

  render(<StorageSection />);
  await userEvent.setup().click(await screen.findByText("Technical details", { selector: "summary" }));

  await waitFor(() => expect(settingsFieldIds()).toContain("hub-config"));
  // The value is the path alone, with no note beside it.
  const configValue = settingsFieldValue("hub-config");
  expect(configValue.textContent).toBe(configValue.querySelector("code")?.textContent);
});

test("omits the Past index row entirely when pastIndex is absent (no configured past-session index)", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => ({ hub: {}, storage: {} }) as SettingsOverviewResponse);

  render(<StorageSection />);
  await userEvent.setup().click(await screen.findByText("Technical details", { selector: "summary" }));

  await waitFor(() => expect(settingsFieldIds()).toEqual(["session-cache", "state-dir", "run-dir", "hub-config"]));
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
  await userEvent.setup().click(await screen.findByText("Technical details", { selector: "summary" }));

  await waitFor(() => expect(settingsFieldIds()).toContain("past-index"));
  expect(within(settingsFieldValue("past-index")).getByText("size-from-hub")).toBeTruthy();
  expect(screen.getByText(/Currently tracking 3 sessions\./)).toBeTruthy();
  expect(settingsFieldValue("past-results-per-page").textContent).toBe("20");
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
  await userEvent.setup().click(await screen.findByText("Technical details", { selector: "summary" }));

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
  await user.click(await screen.findByText("Technical details", { selector: "summary" }));

  await waitFor(() => expect(settingsFieldIds()).toContain("hub-config"));
  expect(fake.calls.filter((call) => call.method === "evener/settings/overview")).toHaveLength(2);
});

test("a hub-reported WireError shows that error's friendly form", async () => {
  const fake = connectFakeClient();
  const err = new WireError("overview index is rebuilding", -32000);
  fake.on("evener/settings/overview", () => {
    throw err;
  });

  render(<StorageSection />);
  await waitFor(() => expect(statusReasons(document.body)).toEqual(["unavailable"]));
  const notice = document.querySelector('[role="status"][data-reason="unavailable"]');
  expect(notice?.textContent).toContain(friendlyErrorMessage(err));
});

test("preserves the wire zero default when an existing index omits its per-page counter", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => ({
    hub: { daemonIdleTimeoutMillis: 3600000, pastIndex: { path: "/fixture/past.db" } },
  }));
  render(<StorageSection />);
  await userEvent.setup().click(await screen.findByText("Technical details", { selector: "summary" }));
  await waitFor(() => expect(settingsFieldIds()).toContain("past-index"));
  expect(settingsFieldValue("past-results-per-page").textContent).toBe("0");
});

test("storage paths start folded while the browser cache stays available", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => ({
    hub: { runDir: "/fixture/run", daemonIdleTimeoutMillis: 3600000 },
    storage: { stateDir: "/fixture/state" },
  }));
  render(<StorageSection />);
  const toggle = await screen.findByText("Technical details", { selector: "summary" });
  expect(toggle.closest("details")?.open).toBe(false);
  expect(settingsFieldIds()).toEqual(["session-cache"]);
  await userEvent.setup().click(toggle);
  expect(settingsFieldValue("run-dir").textContent).toBe("/fixture/run");
  await userEvent.setup().click(toggle);
  expect(settingsFieldIds()).toEqual(["session-cache"]);
});
