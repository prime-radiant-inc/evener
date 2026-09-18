import type { HostRow } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { hostsStore } from "../../../stores/hosts";
import { HOST_ATTACH_POLL_MS, HostsSection } from "./hosts";

function row(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return {
    origin: "sidecar",
    attached: false,
    midAttach: false,
    removed: false,
    ...overrides,
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  hostsStore.getState().resetForTests();
});

afterEach(cleanup);

test("lists hosts with online/offline state chips", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [
      row({ name: "alpha", address: "a.example", attached: true, hubVersion: "1.2.3", origin: "hub.toml" }),
      row({ name: "beta", address: "b.example", attached: false }),
    ],
  }));
  render(<HostsSection sectionId="hosts" />);
  expect(await screen.findByText("alpha")).toBeTruthy();
  expect(await screen.findByText("beta")).toBeTruthy();
  expect(screen.getByText("online")).toBeTruthy();
  expect(screen.getByText("offline")).toBeTruthy();
  // hub.toml rows show the address and origin; no Remove for hub.toml rows.
  expect(screen.getByText(/a\.example/)).toBeTruthy();
  const alphaRow = screen.getByText("alpha").closest("li")!;
  expect(within(alphaRow).queryByRole("button", { name: "Remove" })).toBeNull();
  const betaRow = screen.getByText("beta").closest("li")!;
  // beta is a sidecar row: it offers both Connect and Remove.
  expect(within(betaRow).getByRole("button", { name: "Remove" })).toBeTruthy();
  expect(within(betaRow).getByRole("button", { name: "Connect" })).toBeTruthy();
});

test("add dialog submits name, address, and key", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [] }));
  fake.on("evener/host/add", () => row({ name: "gamma", address: "g.example", keyPath: "/keys/g" }));
  render(<HostsSection sectionId="hosts" />);
  await user.click(await screen.findByRole("button", { name: "Add host" }));
  await user.type(screen.getByLabelText("Name"), "gamma");
  await user.type(screen.getByLabelText("SSH address"), "g.example");
  await user.type(screen.getByLabelText("Key path"), "/keys/g");
  const dialog = screen.getByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: "Add host" }));
  await waitFor(() => {
    expect(fake.calls.filter((c) => c.method === "evener/host/add")).toHaveLength(1);
  });
  expect(fake.calls.find((c) => c.method === "evener/host/add")?.params).toMatchObject({
    name: "gamma",
    address: "g.example",
    keyPath: "/keys/g",
  });
});

test("add validation error renders inline", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [] }));
  fake.on("evener/host/add", () => {
    throw new Error("host gamma: invalid host name");
  });
  render(<HostsSection sectionId="hosts" />);
  await user.click(await screen.findByRole("button", { name: "Add host" }));
  await user.type(screen.getByLabelText("Name"), "gamma");
  await user.type(screen.getByLabelText("SSH address"), "g.example");
  const dialog = screen.getByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: "Add host" }));
  expect(await within(dialog).findByRole("alert")).toBeTruthy();
});

test("Connect drives evener/host/attach and fails with retry intact", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  let attaches = 0;
  fake.on("evener/host/attach", () => {
    attaches += 1;
    throw new Error("dial tcp: connection refused");
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Connect" }));
  await waitFor(() => expect(attaches).toBe(1));
  // Retry affordance: the button is back after the failure.
  await waitFor(() => expect(within(betaRow).getByRole("button", { name: "Connect" })).toBeTruthy());
});

test("a server-reported midAttach row renders connecting and disables Connect", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [row({ name: "beta", address: "b.example", midAttach: true })],
  }));
  render(<HostsSection sectionId="hosts" />);
  expect(await screen.findByText("beta")).toBeTruthy();
  // The chip reports the server's in-progress attach, not offline.
  expect(screen.getByText("connecting")).toBeTruthy();
  expect(screen.queryByText("offline")).toBeNull();
  // Connect is disabled while the server-side attach is in flight — the same
  // disabled action the local connecting state gets.
  const betaRow = screen.getByText("beta").closest("li")!;
  const connect = within(betaRow).getByRole("button", { name: "Connecting…" }) as HTMLButtonElement;
  expect(connect.disabled).toBe(true);
});

test("remove confirms then calls evener/host/remove", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/remove", () => ({ host: row({ name: "beta", removed: true }) }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Remove" }));
  const dialog = await screen.findByRole("dialog", { name: /Remove beta/ });
  await user.click(within(dialog).getByRole("button", { name: "Remove" }));
  await waitFor(() => {
    expect(fake.calls.filter((c) => c.method === "evener/host/remove")).toHaveLength(1);
  });
  expect(fake.calls.find((c) => c.method === "evener/host/remove")?.params).toMatchObject({ name: "beta" });
});

test("load failure shows retry", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => {
    throw new Error("hub unavailable");
  });
  render(<HostsSection sectionId="hosts" />);
  expect(await screen.findByText("Couldn't load hosts")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  vi.useFakeTimers();
  try {
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  } finally {
    vi.useRealTimers();
  }
});

test("a server-side mid-attach row settles via the poll: in-progress -> failed re-enables Connect", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const fake = connectFakeClient();
    let settled = false;
    fake.on("evener/host/list", () => ({
      hosts: [
        settled
          ? row({ name: "beta", address: "b.example", lastAttachError: "dial tcp: connection refused" })
          : row({ name: "beta", address: "b.example", midAttach: true }),
      ],
    }));
    render(<HostsSection sectionId="hosts" />);
    const betaRow = (await screen.findByText("beta")).closest("li")!;
    const connecting = within(betaRow).getByRole("button", { name: "Connecting…" }) as HTMLButtonElement;
    expect(connecting.disabled).toBe(true);

    // The attach settles out-of-band — the SSH supervisor's background
    // reconnect, another client's Connect — so no local action triggers a
    // refresh. The mid-attach poll re-reads the row.
    settled = true;
    await vi.advanceTimersByTimeAsync(HOST_ATTACH_POLL_MS);

    const settledRow = screen.getByText("beta").closest("li")!;
    await waitFor(() => {
      const connect = within(settledRow).getByRole("button", { name: "Connect" }) as HTMLButtonElement;
      expect(connect.disabled).toBe(false);
    });
    expect(screen.getByText("offline")).toBeTruthy();
    expect(screen.queryByText("connecting")).toBeNull();
    expect(screen.getByText(/dial tcp: connection refused/)).toBeTruthy();
  } finally {
    vi.useRealTimers();
  }
});
