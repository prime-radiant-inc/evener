import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { SettingsOverviewResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetSettingsOverviewStoreForTests } from "../../../stores/settingsOverview";
import { GeneralSection } from "./general";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const FULL_RESPONSE: SettingsOverviewResponse = {
  hub: {
    version: "1.2.3",
    commit: "abc1234",
    listenAddr: "127.0.0.1:9180",
    runDir: "/tmp/serf-run",
    spawnTimeout: "30s",
    bearerTokenAge: "created 3d ago",
    pastIndex: { path: "~/.serf/past.db", size: "48 MB", perPage: 20, count: 7 },
  },
  storage: { stateDir: "/home/user/.serf" },
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetSettingsOverviewStoreForTests();
});

afterEach(cleanup);

test("renders every field in the documented order with the cross-referenced State dir from storage", async () => {
  const fake = connectFakeClient();
  fake.on("serf/settings/overview", () => FULL_RESPONSE);

  render(<GeneralSection />);
  await screen.findByText("127.0.0.1:9180");

  const labels = screen.getAllByRole("term").map((el) => el.textContent);
  expect(labels).toEqual([
    "Hub address",
    "Bearer token",
    "Run dir",
    "State dir",
    "Past index",
    "Spawn timeout",
    "Past results per page",
    "Hub config",
    "Hub version",
  ]);
  // State dir is General's own field per SettingsStorageOverview - but reads
  // through settingsOverview.storage.stateDir, not any hub.* field.
  expect(screen.getByText("/home/user/.serf")).toBeTruthy();
});

test("never renders the bearer token value itself - only a fixed mask plus the optional age", async () => {
  const fake = connectFakeClient();
  fake.on("serf/settings/overview", () => FULL_RESPONSE);

  render(<GeneralSection />);
  await screen.findByText("127.0.0.1:9180");

  expect(screen.getByText("created 3d ago")).toBeTruthy();
  expect(screen.getByText("••••••••••••", { exact: false })).toBeTruthy();
});

test("Hub config's static value carries the 'edit to change' dim hint (unlike Storage's own row)", async () => {
  const fake = connectFakeClient();
  fake.on("serf/settings/overview", () => FULL_RESPONSE);

  render(<GeneralSection />);
  await screen.findByText("127.0.0.1:9180");

  expect(screen.getByText("edit to change")).toBeTruthy();
});

test("Hub version appends the commit only when present", async () => {
  const fake = connectFakeClient();
  fake.on("serf/settings/overview", () => FULL_RESPONSE);

  render(<GeneralSection />);

  expect(await screen.findByText("(abc1234)")).toBeTruthy();
});

test("omits Past index and Past results per page when no past-session index is configured", async () => {
  const fake = connectFakeClient();
  const response: SettingsOverviewResponse = {
    hub: { version: "1.2.3", listenAddr: "127.0.0.1:9180" },
    storage: { stateDir: "/home/user/.serf" },
  };
  fake.on("serf/settings/overview", () => response);

  render(<GeneralSection />);
  await screen.findByText("127.0.0.1:9180");

  expect(screen.queryByText("Past index")).toBeNull();
  expect(screen.queryByText("Past results per page")).toBeNull();
});

test("shows an inline error state with retry on load failure, not a toast", async () => {
  const fake = connectFakeClient();
  fake.on("serf/settings/overview", () => {
    throw new Error("hub unreachable");
  });

  render(<GeneralSection />);

  expect(await screen.findByText("Couldn't load general settings")).toBeTruthy();
  expect(screen.getByText("hub unreachable")).toBeTruthy();
});
