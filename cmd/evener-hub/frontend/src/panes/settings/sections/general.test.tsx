import type { SettingsOverviewResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { resetSettingsOverviewStoreForTests } from "../../../stores/settingsOverview";
import { emptyStateReasons } from "../../../widgets/emptystate/emptyStateTestUtils";
import { GeneralSection } from "./general";
import { settingsFieldIds, settingsFieldValue } from "./settingsFieldTestUtils";

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
    bearerTokenAge: "token-age-from-hub",
    daemonIdleTimeoutMillis: 3600000,
    pastIndex: { path: "~/.evener/past.db", size: "size-from-hub", perPage: 20, count: 7 },
  },
  storage: { stateDir: "/home/user/.evener" },
};

/** The load-failure explanation's text; throws when the hint is missing. */
function loadFailureHint(): string {
  const hint = screen.getByTestId("empty-state").querySelector('[data-slot="hint"]');
  if (hint === null) throw new Error("load failure shows no hint");
  return hint.textContent?.trim() ?? "";
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetSettingsOverviewStoreForTests();
});

test("renders every field in the documented order with the cross-referenced State dir from storage", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => FULL_RESPONSE);

  render(<GeneralSection />);
  await screen.findByText("127.0.0.1:9180");

  expect(settingsFieldIds()).toEqual([
    "hub-address",
    "bearer-token",
    "run-dir",
    "state-dir",
    "past-index",
    "spawn-timeout",
    "past-results-per-page",
    "hub-config",
    "hub-version",
  ]);
  // State dir is General's own field per SettingsStorageOverview - but reads
  // through settingsOverview.storage.stateDir, not any hub.* field.
  expect(settingsFieldValue("state-dir").textContent).toBe("/home/user/.evener");
});

test("never renders the bearer token value itself - only a fixed mask plus the optional age", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => FULL_RESPONSE);

  render(<GeneralSection />);
  await screen.findByText("127.0.0.1:9180");

  const token = settingsFieldValue("bearer-token");
  expect(within(token).getByText("token-age-from-hub")).toBeTruthy();
  expect(token.textContent).toContain("••••••••••••");
});

test("Hub config's static value carries the 'edit to change' dim hint (unlike Storage's own row)", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => FULL_RESPONSE);

  render(<GeneralSection />);
  await screen.findByText("127.0.0.1:9180");

  // The value carries a note beside the path itself.
  const configValue = settingsFieldValue("hub-config");
  expect(configValue.textContent).not.toBe(configValue.querySelector("code")?.textContent);
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
    hub: { version: "1.2.3", listenAddr: "127.0.0.1:9180", daemonIdleTimeoutMillis: 3600000 },
    storage: { stateDir: "/home/user/.evener" },
  };
  fake.on("evener/settings/overview", () => response);

  render(<GeneralSection />);
  await screen.findByText("127.0.0.1:9180");

  expect(settingsFieldIds()).not.toContain("past-index");
  expect(settingsFieldIds()).not.toContain("past-results-per-page");
});

// A plain Error's own message is internal detail (not something the hub
// wrote for a person to read), so friendlyErrorMessage replaces it with a
// generic sentence - see protocol/errors.test.ts for that contract.
test("shows an inline error state with retry on load failure, not a toast", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => {
    throw new Error("raw-overview-failure");
  });

  render(<GeneralSection />);

  await waitFor(() => expect(emptyStateReasons(document.body)).toEqual(["unavailable"]));
  expect(loadFailureHint()).toMatch(/\S/);
  expect(screen.queryByText("raw-overview-failure", { exact: false })).toBeNull();
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

  await waitFor(() => expect(emptyStateReasons(document.body)).toEqual(["unavailable"]));
  expect(loadFailureHint()).toMatch(/\S/);
  expect(screen.queryByText(/AppwireClient/i)).toBeNull();
});
