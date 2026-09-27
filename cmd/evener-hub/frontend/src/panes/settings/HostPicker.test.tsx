import type { HostRow } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../stores/connection";
import { LOCAL_HOST } from "../../stores/hostRouting";
import { hostsStore } from "../../stores/hosts";
import { resetSettingsHostForTests, settingsHostStore } from "../../stores/settingsHost";
import { HostPicker } from "./HostPicker";
import { HOST_POLL_MS } from "./sections/hosts";

// The shared picker on the settings route: the local hub plus every configured
// remote host, writing the one shared selection and the URL.

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "hub.toml", attached: false, midAttach: false, removed: false, ...overrides };
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

// The registry read must follow the connection: a reconnect or a client swap
// otherwise left the picker - and the identity every remote read keys on -
// describing the hub that was.
test("re-reads the registry on the connection that is current now", async () => {
  const fake = connectFakeClient([hostRow({ name: "beta", attached: true })]);
  render(<HostPicker />);
  await screen.findByRole("option", { name: "beta" });
  expect(fake.calls.filter((call) => call.method === "evener/host/list")).toHaveLength(1);

  const replacement = new FakeClient("ready");
  replacement.on("evener/host/list", () => ({ hosts: [hostRow({ name: "gamma", attached: true })] }));
  await act(async () => connectionStore.getState().connect(replacement));

  expect(await screen.findByRole("option", { name: "gamma" })).toBeTruthy();
  expect(replacement.calls.filter((call) => call.method === "evener/host/list")).toHaveLength(1);
  expect(screen.queryByRole("option", { name: "beta" })).toBeNull();
});

// A host added or removed by another client is observable here only by asking
// again - no controller-side host lifecycle notification exists on the wire, so
// the registry is re-read on the quiet cadence the Hosts section already polls
// on rather than on a cadence invented here.
test("re-reads the registry on the existing quiet-refresh cadence", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    let rows = [hostRow({ name: "beta", attached: true })];
    const fake = new FakeClient("ready");
    fake.on("evener/host/list", () => ({ hosts: rows }));
    connectionStore.getState().connect(fake);
    render(<HostPicker />);
    await screen.findByRole("option", { name: "beta" });

    // Out-of-band, with nothing in this tab acting: another client adds a host.
    rows = [hostRow({ name: "beta", attached: true }), hostRow({ name: "gamma", attached: true })];
    await act(() => vi.advanceTimersByTimeAsync(HOST_POLL_MS));

    await waitFor(() => expect(screen.getByRole("option", { name: "gamma" })).toBeTruthy());
  } finally {
    vi.useRealTimers();
  }
});
