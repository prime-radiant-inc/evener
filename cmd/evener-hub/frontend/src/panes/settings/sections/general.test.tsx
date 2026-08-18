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
    runDir: "/tmp/evener-run",
    spawnTimeout: "30s",
    bearerTokenAge: "created 3d ago",
    pastIndex: { path: "~/.evener/past.db", size: "48 MB", perPage: 20, count: 7 },
  },
  storage: { stateDir: "/home/user/.evener" },
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetSettingsOverviewStoreForTests();
});

afterEach(cleanup);

test("renders every field in the documented order with the cross-referenced State dir from storage", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => FULL_RESPONSE);

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
  expect(screen.getByText("/home/user/.evener")).toBeTruthy();
});

test("never renders the bearer token value itself - only a fixed mask plus the optional age", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => FULL_RESPONSE);

  render(<GeneralSection />);
  await screen.findByText("127.0.0.1:9180");

  expect(screen.getByText("created 3d ago")).toBeTruthy();
  expect(screen.getByText("••••••••••••", { exact: false })).toBeTruthy();
});

test("Hub config's static value carries the 'edit to change' dim hint (unlike Storage's own row)", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => FULL_RESPONSE);

  render(<GeneralSection />);
  await screen.findByText("127.0.0.1:9180");

  expect(screen.getByText("edit to change")).toBeTruthy();
});

test("Hub version appends the commit only when present", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => FULL_RESPONSE);

  render(<GeneralSection />);

  expect(await screen.findByText("(abc1234)")).toBeTruthy();
});

test("omits Past index and Past results per page when no past-session index is configured", async () => {
  const fake = connectFakeClient();
  const response: SettingsOverviewResponse = {
    hub: { version: "1.2.3", listenAddr: "127.0.0.1:9180" },
    storage: { stateDir: "/home/user/.evener" },
  };
  fake.on("evener/settings/overview", () => response);

  render(<GeneralSection />);
  await screen.findByText("127.0.0.1:9180");

  expect(screen.queryByText("Past index")).toBeNull();
  expect(screen.queryByText("Past results per page")).toBeNull();
});

// A plain Error's own message is internal detail (not something the hub
// wrote for a person to read), so friendlyErrorMessage replaces it with a
// generic sentence - see protocol/errors.test.ts for that contract.
test("shows an inline error state with retry on load failure, not a toast", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => {
    throw new Error("hub unreachable");
  });

  render(<GeneralSection />);

  expect(await screen.findByText("Couldn't load general settings")).toBeTruthy();
  expect(await screen.findByText("Something went wrong.")).toBeTruthy();
  expect(screen.queryByText("hub unreachable")).toBeNull();
});

// The verbatim bug report (user testing): this section showed "AppwireClient:
// cannot call "evener/settings/overview" while state is "closed"" when the
// fetch landed while the client was tearing down - internal wiring detail,
// never meant for the screen.
test("a client-unreachable rejection (the reported bug) shows the friendly hub-unreachable sentence", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => {
    throw new Error('AppwireClient: cannot call "evener/settings/overview" while state is "closed"');
  });

  render(<GeneralSection />);

  expect(await screen.findByText("Couldn't load general settings")).toBeTruthy();
  expect(await screen.findByText("Can't reach the hub right now.")).toBeTruthy();
  expect(screen.queryByText(/AppwireClient/i)).toBeNull();
});
