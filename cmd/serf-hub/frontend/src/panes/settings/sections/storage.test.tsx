import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { SettingsOverviewResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetSettingsOverviewStoreForTests } from "../../../stores/settingsOverview";
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

afterEach(cleanup);

test("renders State dir (from storage, not hub), Run dir (from hub), and the static hub.toml row", async () => {
  const fake = connectFakeClient();
  const response: SettingsOverviewResponse = {
    hub: { runDir: "/tmp/serf-run" },
    storage: { stateDir: "/home/user/.serf" },
  };
  fake.on("serf/settings/overview", () => response);

  render(<StorageSection />);

  expect(await screen.findByText("/home/user/.serf")).toBeTruthy();
  expect(screen.getByText("/tmp/serf-run")).toBeTruthy();
  expect(screen.getByText("State dir").tagName).toBe("DT");
  expect(screen.getByText("~/.serf/hub.toml")).toBeTruthy();
  expect(
    screen.getByText("Main configuration file. Edit it to change addresses, providers, and spawn defaults."),
  ).toBeTruthy();
});

test("Hub config's dd carries no 'edit to change' dim hint (unlike General's own row)", async () => {
  const fake = connectFakeClient();
  fake.on("serf/settings/overview", () => ({ hub: {}, storage: {} }) as SettingsOverviewResponse);

  render(<StorageSection />);

  const configValue = await screen.findByText("~/.serf/hub.toml");
  expect(configValue.textContent).toBe("~/.serf/hub.toml");
});

test("omits the Past index row entirely when pastIndex is absent (no configured past-session index)", async () => {
  const fake = connectFakeClient();
  fake.on("serf/settings/overview", () => ({ hub: {}, storage: {} }) as SettingsOverviewResponse);

  render(<StorageSection />);

  await screen.findByText("~/.serf/hub.toml"); // wait for load to settle
  expect(screen.queryByText("Past index")).toBeNull();
});

test("renders the Past index row with its size and a pluralized live session count when present", async () => {
  const fake = connectFakeClient();
  const response: SettingsOverviewResponse = {
    hub: { pastIndex: { path: "~/.serf/past.db", size: "48 MB", perPage: 20, count: 3 } },
    storage: { stateDir: "/home/user/.serf" },
  };
  fake.on("serf/settings/overview", () => response);

  render(<StorageSection />);

  expect((await screen.findByText("Past index")).tagName).toBe("DT");
  expect(screen.getByText("48 MB")).toBeTruthy();
  expect(screen.getByText(/Currently tracking 3 sessions\./)).toBeTruthy();
});

test("pluralizes 'session' (not 'sessions') when the count is exactly 1", async () => {
  const fake = connectFakeClient();
  const response: SettingsOverviewResponse = {
    hub: { pastIndex: { path: "~/.serf/past.db", perPage: 20, count: 1 } },
    storage: {},
  };
  fake.on("serf/settings/overview", () => response);

  render(<StorageSection />);

  expect(await screen.findByText(/Currently tracking 1 session\./)).toBeTruthy();
});
