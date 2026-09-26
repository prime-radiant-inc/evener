import { type HostRow, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { hostsStore } from "../../../stores/hosts";
import { enterText } from "../../../textEntryTestUtils";
import { HOST_POLL_MS, HostsSection } from "./hosts";

function row(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return {
    origin: "hub.toml",
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
  // Every row shows the address and the one origin; every live host is
  // editable and removable (the file is machine-managed).
  expect(screen.getByText(/a\.example/)).toBeTruthy();
  const alphaRow = screen.getByText("alpha").closest("li")!;
  expect(within(alphaRow).getByRole("button", { name: "Remove" })).toBeTruthy();
  expect(within(alphaRow).getByRole("button", { name: "Edit" })).toBeTruthy();
  const betaRow = screen.getByText("beta").closest("li")!;
  expect(within(betaRow).getByRole("button", { name: "Remove" })).toBeTruthy();
  expect(within(betaRow).getByRole("button", { name: "Connect" })).toBeTruthy();
});

test("an offline row keeps rendering its retained facts", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [
      // Offline rows carry the last-known facts the attach machinery retains
      // (HostRow's wire contract); the pane must keep displaying them once
      // the host goes offline instead of dropping version/OS/arch.
      row({ name: "beta", address: "b.example", attached: false, hubVersion: "1.4.2", os: "linux", arch: "arm64" }),
    ],
  }));
  render(<HostsSection sectionId="hosts" />);
  expect(await screen.findByText("beta")).toBeTruthy();
  expect(screen.getByText("offline")).toBeTruthy();
  expect(screen.getByText(/hub 1\.4\.2/)).toBeTruthy();
  expect(screen.getByText(/linux\/arm64/)).toBeTruthy();
});

test("the add dialog submits every entry field", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [] }));
  fake.on("evener/host/add", () => row({ name: "gamma", address: "g.example" }));
  render(<HostsSection sectionId="hosts" />);
  await user.click(await screen.findByRole("button", { name: "Add host" }));
  await user.type(screen.getByLabelText("Name"), "gamma");
  await enterText(user, screen.getByLabelText("SSH address"), "g.example");
  await enterText(user, screen.getByLabelText("User"), "operator");
  await user.type(screen.getByLabelText("Key path"), "/keys/g");
  await enterText(user, screen.getByLabelText("Evener path"), "/opt/evener");
  await enterText(user, screen.getByLabelText("Hub config path"), "/etc/evener/hub.toml");
  await enterText(user, screen.getByLabelText("Hub address"), "127.0.0.1:9180");
  await enterText(user, screen.getByLabelText("Roots"), "/srv/one\n/srv/two");
  const dialog = screen.getByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: "Add host" }));
  await waitFor(() => {
    expect(fake.calls.filter((c) => c.method === "evener/host/add")).toHaveLength(1);
  });
  expect(fake.calls.find((c) => c.method === "evener/host/add")?.params).toMatchObject({
    entry: {
      name: "gamma",
      address: "g.example",
      user: "operator",
      keyPath: "/keys/g",
      evenerPath: "/opt/evener",
      configPath: "/etc/evener/hub.toml",
      addr: "127.0.0.1:9180",
      roots: ["/srv/one", "/srv/two"],
    },
  });
});

test("a host row offers Edit, prefills the whole entry, and sends no name input", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [row({ name: "beta", address: "b.example", user: "bob", roots: ["/srv/b"] })],
  }));
  fake.on("evener/host/update", () => ({ host: row({ name: "beta", address: "b2.example" }) }));
  render(<HostsSection sectionId="hosts" />);
  const rowEl = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(rowEl).getByRole("button", { name: "Edit" }));

  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).getByText("Edit beta")).toBeTruthy();
  // The prefilled entry, and no name input: the name is the dialog's title.
  expect((within(dialog).getByLabelText("SSH address") as HTMLInputElement).value).toBe("b.example");
  expect((within(dialog).getByLabelText("User") as HTMLInputElement).value).toBe("bob");
  expect((within(dialog).getByLabelText("Roots") as HTMLTextAreaElement).value).toBe("/srv/b");
  expect(within(dialog).queryByLabelText("Name")).toBeNull();

  await user.clear(within(dialog).getByLabelText("SSH address"));
  await enterText(user, within(dialog).getByLabelText("SSH address"), "b2.example");
  await user.click(within(dialog).getByRole("button", { name: "Save" }));
  await waitFor(() => {
    expect(fake.calls.filter((c) => c.method === "evener/host/update")).toHaveLength(1);
  });
  expect(fake.calls.find((c) => c.method === "evener/host/update")?.params).toMatchObject({
    name: "beta",
    entry: { address: "b2.example" },
  });
});

test("a hub.toml row offers Edit and Remove like every other row", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "alpha", address: "a.example", origin: "hub.toml" })] }));
  render(<HostsSection sectionId="hosts" />);
  const rowEl = (await screen.findByText("alpha")).closest("li")!;
  expect(within(rowEl).getByRole("button", { name: "Edit" })).toBeTruthy();
  expect(within(rowEl).getByRole("button", { name: "Remove" })).toBeTruthy();
});

test("a validation refusal lands on the input the hub blamed", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/update", () => {
    throw new WireError('host "beta": missing ssh destination', -32602, {
      evenerErrorInfo: "invalidHostField",
      field: "address",
    });
  });
  render(<HostsSection sectionId="hosts" />);
  const rowEl = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(rowEl).getByRole("button", { name: "Edit" }));
  const dialog = await screen.findByRole("dialog");
  await user.clear(within(dialog).getByLabelText("SSH address"));
  await user.click(within(dialog).getByRole("button", { name: "Save" }));

  // The message is the address row's own: the refusal named the wire field the
  // input is labelled for, so the operator sees it where the fix belongs.
  const addressInput = within(dialog).getByLabelText("SSH address");
  const addressLabel = within(dialog).getByText("SSH address", { selector: "label" });
  const addressRow = addressLabel.parentElement;
  if (addressRow === null) throw new Error("SSH address label has no FormRow parent");
  expect(within(addressRow).getByLabelText("SSH address")).toBe(addressInput);
  const inlineError = await within(addressRow).findByRole("alert");
  expect(inlineError.textContent).toMatch(/missing ssh destination/);
  expect(inlineError.id).toBe(`${addressInput.id}-error`);
  expect(within(dialog).queryByText(/Something went wrong/)).toBeNull();
});

test("an edit refusal blaming the unrendered name is form-level", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/update", () => {
    throw new WireError('host "beta": invalid host name', -32602, {
      evenerErrorInfo: "invalidHostField",
      field: "name",
    });
  });
  render(<HostsSection sectionId="hosts" />);
  const rowEl = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(rowEl).getByRole("button", { name: "Edit" }));
  const dialog = await screen.findByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: "Save" }));

  const alert = await within(dialog).findByRole("alert");
  expect(alert.textContent).toMatch(/invalid host name/);
  expect(alert.parentElement?.contains(within(dialog).getByLabelText("SSH address"))).toBe(true);
  expect(alert.parentElement?.contains(within(dialog).getByLabelText("Roots"))).toBe(true);
  expect(within(dialog).queryByLabelText("Name")).toBeNull();
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
  await enterText(user, screen.getByLabelText("SSH address"), "g.example");
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

test("an external detach converges via the mounted poll: online -> offline", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const fake = connectFakeClient();
    let detached = false;
    fake.on("evener/host/list", () => ({
      hosts: [
        detached
          ? row({ name: "beta", address: "b.example", attached: false })
          : row({ name: "beta", address: "b.example", attached: true }),
      ],
    }));
    render(<HostsSection sectionId="hosts" />);
    expect(await screen.findByText("online")).toBeTruthy();

    // The host drops out-of-band — a detach this tab observed no action for
    // (the SSH supervisor gave up, another client's remove). No row is
    // mid-attach, so only the pane's mounted poll re-reads the rows
    // (round-4 M3): without it the row stays "online" with no Connect button.
    detached = true;
    await act(() => vi.advanceTimersByTimeAsync(HOST_POLL_MS));

    await waitFor(() => expect(screen.getByText("offline")).toBeTruthy());
    expect(screen.queryByText("online")).toBeNull();
    const betaRow = screen.getByText("beta").closest("li")!;
    expect(within(betaRow).getByRole("button", { name: "Connect" })).toBeTruthy();
  } finally {
    vi.useRealTimers();
  }
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
  const reads = () => fake.calls.filter((call) => call.method === "evener/host/list").length;
  const before = reads();
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  // Retry re-reads the registry, and the test ends once that read has failed
  // again rather than letting its update land in the next test.
  await waitFor(() => {
    expect(reads()).toBe(before + 1);
    expect(hostsStore.getState().load.phase).toBe("error");
  });
  expect(screen.getByText("Couldn't load hosts")).toBeTruthy();
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
    await act(() => vi.advanceTimersByTimeAsync(HOST_POLL_MS));

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
