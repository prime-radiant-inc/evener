import type { HostRow } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../stores/connection";
import { LOCAL_HOST } from "../../stores/hostRouting";
import { hostsStore } from "../../stores/hosts";
import { resetSettingsHostForTests, settingsHostStore } from "../../stores/settingsHost";
import { HostPicker } from "./HostPicker";

// The shared picker on the settings route: the local hub plus every configured
// remote host, writing the one shared selection and the URL.

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: false, midAttach: false, removed: false, ...overrides };
}

function connectFakeClient(hosts: HostRow[]): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("evener/host/list", () => ({ hosts }));
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  hostsStore.getState().resetForTests();
  resetSettingsHostForTests();
  // A settings route, so selectHost rewrites the host on the section it is on.
  window.history.pushState({}, "", "/settings/credentials");
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  window.history.pushState({}, "", "/");
});

test("offers this hub plus every configured remote host, defaulting to this hub", async () => {
  connectFakeClient([hostRow({ name: "beta", attached: true }), hostRow({ name: "gamma", attached: false })]);
  render(<HostPicker />);

  await screen.findByRole("option", { name: "gamma (offline)" });
  expect(screen.getAllByRole("option").map((option) => (option as HTMLOptionElement).value)).toEqual([
    LOCAL_HOST,
    "beta",
    "gamma",
  ]);
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe(LOCAL_HOST);
  expect(settingsHostStore.getState().host).toBe(LOCAL_HOST);
});

test("selecting a remote host writes the shared selection and the route", async () => {
  connectFakeClient([hostRow({ name: "beta", attached: true })]);
  render(<HostPicker />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });

  await user.selectOptions(select, "beta");

  expect(settingsHostStore.getState().host).toBe("beta");
  expect(window.location.pathname).toBe("/settings/credentials");
  expect(window.location.search).toBe("?host=beta");
});

test("selecting this hub drops the host from the route", async () => {
  connectFakeClient([hostRow({ name: "beta", attached: true })]);
  settingsHostStore.setState({ host: "beta" });
  render(<HostPicker />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });

  await user.selectOptions(select, "local");

  expect(settingsHostStore.getState().host).toBe(LOCAL_HOST);
  expect(window.location.pathname).toBe("/settings/credentials");
  expect(window.location.search).toBe("");
});

// A host that has left the registry keeps an option, so the current selection
// stays visible rather than the select silently showing blank.
test("a selected host that has left the registry keeps an option", async () => {
  connectFakeClient([]);
  settingsHostStore.setState({ host: "gone" });
  render(<HostPicker />);

  await screen.findByRole("option", { name: "gone" });
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("gone");
});
