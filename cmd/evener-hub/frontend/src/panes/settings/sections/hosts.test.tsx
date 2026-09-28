import { type HostPlan, type HostRow, RequestTimeoutError, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { hostOpsStore } from "../../../stores/hostOps";
import { hostsStore } from "../../../stores/hosts";
import { enterText } from "../../../textEntryTestUtils";
import { HOST_POLL_MS, HostsSection } from "./hosts";

function row(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return {
    generation: 1,
    incarnationId: "inc-1",
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

// planFixture is the controller-minted plan a scripted `evener/host/plan`
// answers with (deploy-pipeline spec 08b §10).
function planFixture(overrides: Partial<HostPlan> = {}): HostPlan {
  return {
    host: "beta",
    generation: 1,
    targetPath: "/srv/evener/evener",
    controllerRevision: "controller-rev-9",
    restartFollows: true,
    factsRevision: "facts-rev-1",
    hubTomlFingerprint: "fp-1",
    factsCapturedAt: "2026-09-28T07:59:00Z",
    factsAgeSec: 42,
    runningVersion: "1.4.2",
    runningHealthy: true,
    ...overrides,
  };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  hostsStore.getState().resetForTests();
  hostOpsStore.getState().resetForTests();
});

afterEach(cleanup);

test("lists hosts with online/offline state chips", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [
      row({
        name: "alpha",
        address: "a.example",
        attached: true,
        hubVersion: "1.2.3",
        generation: 1,
        incarnationId: "inc-1",
        origin: "hub.toml",
      }),
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
  // The registry mutations return the mutation-result union (registry spec 08
  // §11), so a fake answers with the committed arm rather than a bare row.
  fake.on("evener/host/add", () => ({ outcome: "committed", host: row({ name: "gamma", address: "g.example" }) }));
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
  fake.on("evener/host/update", () => ({
    outcome: "committed",
    host: row({ name: "beta", address: "b2.example" }),
  }));
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
  fake.on("evener/host/list", () => ({
    hosts: [row({ name: "alpha", address: "a.example", generation: 1, incarnationId: "inc-1", origin: "hub.toml" })],
  }));
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
  // remove's committed arm carries the dedicated removed row, whose shape
  // spells `attached: false` and `midEnsure: false` explicitly.
  fake.on("evener/host/remove", () => ({
    outcome: "committed",
    host: { ...row({ name: "beta" }), removed: true, attached: false, midEnsure: false },
  }));
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

// --- S14: the Deploy/Restart actions, plan confirmation, and refusals --------

test("a live row offers Deploy and Restart; a tombstone row renders both disabled and they never fire", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [row({ name: "beta", address: "b.example" }), row({ name: "gamma", removed: true })],
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  const gammaRow = screen.getByText("gamma").closest("li")!;
  expect(within(betaRow).getByRole("button", { name: "Deploy" })).toBeTruthy();
  expect(within(betaRow).getByRole("button", { name: "Restart" })).toBeTruthy();
  // Registry spec 08 §13: on tombstone rows Connect, Deploy, Restart, Edit and
  // Remove render disabled and never fire.
  const deploy = within(gammaRow).getByRole("button", { name: "Deploy" }) as HTMLButtonElement;
  const restart = within(gammaRow).getByRole("button", { name: "Restart" }) as HTMLButtonElement;
  expect(deploy.disabled).toBe(true);
  expect(restart.disabled).toBe(true);
  fireEvent.click(deploy);
  fireEvent.click(restart);
  expect(fake.calls.some((c) => c.method === "evener/host/plan" || c.method === "evener/host/restart")).toBe(false);
});

test("Deploy opens the plan confirmation and renders the controller-minted plan", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/plan", () => ({ outcome: "planned", plan: planFixture(), token: "tok-1" }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));

  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText("/srv/evener/evener")).toBeTruthy());
  // Registry spec 08 §13: target host, controller revision, resolved remote
  // target path, whether a restart follows, the running build, and facts
  // freshness from the plan's factsCapturedAt/factsAgeSec.
  expect(within(dialog).getByText("Target host")).toBeTruthy();
  expect(within(dialog).getByText("beta")).toBeTruthy();
  expect(within(dialog).getByText("controller-rev-9")).toBeTruthy();
  expect(within(dialog).getByText("yes")).toBeTruthy();
  expect(within(dialog).getByText("1.4.2")).toBeTruthy();
  expect(within(dialog).getByText(/42s ago/)).toBeTruthy();
  expect((within(dialog).getByRole("button", { name: "Deploy" }) as HTMLButtonElement).disabled).toBe(false);
});

test("the confirmation submits exactly the displayed plan's token and operation ID", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/plan", () => ({ outcome: "planned", plan: planFixture(), token: "tok-1" }));
  fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "pending" }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText("/srv/evener/evener")).toBeTruthy());
  await user.click(within(dialog).getByRole("button", { name: "Deploy" }));

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/deploy")).toHaveLength(1));
  const call = fake.calls.find((c) => c.method === "evener/host/deploy")!;
  expect(call.params).toMatchObject({ name: "beta", token: "tok-1" });
  expect((call.params as { operationId: string }).operationId).not.toBe("");
  // The started operation closes the confirmation.
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("a stale token re-plans and re-renders the confirmation before any retry", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  let plans = 0;
  fake.on("evener/host/plan", () => {
    plans += 1;
    return { outcome: "planned", plan: planFixture({ targetPath: `/t${plans}` }), token: `tok-${plans}` };
  });
  let deploys = 0;
  fake.on("evener/host/deploy", () => {
    deploys += 1;
    if (deploys === 1) {
      throw new WireError('host "beta": the confirmation token expired', -32013, { evenerErrorInfo: "token-expired" });
    }
    return { id: "op-2", clientOperationId: "client-op-2", state: "pending" };
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText("/t1")).toBeTruthy());

  await user.click(within(dialog).getByRole("button", { name: "Deploy" }));

  // §13: deploy rejected the token as stale, so the UI re-plans and re-renders
  // the confirmation from the new response before any retry.
  await waitFor(() => expect(plans).toBe(2));
  await waitFor(() => expect(within(dialog).getByText("/t2")).toBeTruthy());
  expect(within(dialog).getByText(/This plan expired before the deploy was submitted/)).toBeTruthy();
  expect(within(dialog).getByText(/fresh plan is shown below/)).toBeTruthy();
  expect(fake.calls.filter((c) => c.method === "evener/host/deploy")).toHaveLength(1);

  // The re-rendered confirmation deploys under the fresh token.
  await user.click(within(dialog).getByRole("button", { name: "Deploy" }));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/deploy")).toHaveLength(2));
  expect(fake.calls.filter((c) => c.method === "evener/host/deploy")[1]!.params).toMatchObject({ token: "tok-2" });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("the no-token unattached arm directs Connect first", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  let plans = 0;
  fake.on("evener/host/plan", () => {
    plans += 1;
    if (plans === 1) {
      return {
        outcome: "no-token",
        staleFacts: {
          message: 'host "beta" is not attached; connect it and plan again',
          attached: false,
          reason: "unattached",
        },
        terminal: false,
      };
    }
    return { outcome: "planned", plan: planFixture(), token: "tok-2" };
  });
  fake.on("evener/host/attach", () => ({ attached: true, host: "beta" }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText(/is not attached; connect it and plan again/)).toBeTruthy());
  // No token was minted, so the confirmation cannot proceed.
  expect((within(dialog).getByRole("button", { name: "Deploy" }) as HTMLButtonElement).disabled).toBe(true);

  await user.click(within(dialog).getByRole("button", { name: "Connect and plan" }));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/attach")).toHaveLength(1));
  await waitFor(() => expect(plans).toBe(2));
  await waitFor(() => expect(within(dialog).getByText("/srv/evener/evener")).toBeTruthy());
});

test("refresh-failed retries the plan and never offers Connect", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  let plans = 0;
  fake.on("evener/host/plan", () => {
    plans += 1;
    if (plans === 1) {
      return {
        outcome: "no-token",
        staleFacts: {
          message: 'refreshing host "beta"\'s preflight facts failed: ssh timeout',
          attached: true,
          reason: "refresh-failed",
        },
        terminal: false,
      };
    }
    return { outcome: "planned", plan: planFixture(), token: "tok-2" };
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText(/preflight facts failed: ssh timeout/)).toBeTruthy());
  expect(within(dialog).queryByRole("button", { name: /Connect/ })).toBeNull();

  await user.click(within(dialog).getByRole("button", { name: "Retry plan" }));
  await waitFor(() => expect(plans).toBe(2));
  await waitFor(() => expect(within(dialog).getByText("/srv/evener/evener")).toBeTruthy());
});

test("handler-absent surfaces the one-time migration step, never Connect", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/plan", () => ({
    outcome: "no-token",
    staleFacts: {
      message:
        "host \"beta\"'s hub does not serve the deploy pipeline's running probe yet, so nothing could be probed; upgrade the host's build before planning",
      attached: true,
      reason: "handler-absent",
    },
    terminal: false,
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText(/upgrade the host's build before planning/)).toBeTruthy());
  expect(within(dialog).queryByRole("button", { name: /Connect/ })).toBeNull();
  expect(within(dialog).getByRole("button", { name: "Retry plan" })).toBeTruthy();
});

test("remnant-open names the blocking remnant and offers neither Connect nor re-plan", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/plan", () => ({
    outcome: "no-token",
    staleFacts: {
      message: 'host "beta" is fenced by an open teardown remnant',
      attached: true,
      reason: "remnant-open",
    },
    terminal: false,
    remnantId: "remnant-7",
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  // §13: surfaces the blocking remnantId; never Connect, never re-plan (a
  // re-plan mints nothing while the remnant is open).
  await waitFor(() => expect(within(dialog).getByText(/remnant-7/)).toBeTruthy());
  expect(within(dialog).getByText(/fenced by an open teardown remnant/)).toBeTruthy();
  expect(within(dialog).queryByRole("button", { name: /Connect/ })).toBeNull();
  expect(within(dialog).queryByRole("button", { name: /Retry plan/ })).toBeNull();
  expect((within(dialog).getByRole("button", { name: "Deploy" }) as HTMLButtonElement).disabled).toBe(true);
});

test("a terminal no-token reason disables the deploy button and offers no retry", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/plan", () => ({
    outcome: "no-token",
    staleFacts: {
      message: "the controller is dirty; rebuild from a clean tree before deploying",
      attached: true,
      reason: "controller-dirty",
    },
    terminal: true,
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText(/rebuild from a clean tree/)).toBeTruthy());
  // Terminal refusals disable the confirmation; there is no retry affordance.
  expect((within(dialog).getByRole("button", { name: "Deploy" }) as HTMLButtonElement).disabled).toBe(true);
  expect(within(dialog).queryByRole("button", { name: /Retry plan/ })).toBeNull();
  expect(within(dialog).queryByRole("button", { name: /Connect/ })).toBeNull();
});

test("host-busy-operation renders the running operation and offers no bare retry", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/plan", () => ({ outcome: "planned", plan: planFixture(), token: "tok-1" }));
  fake.on("evener/host/deploy", () => {
    throw new WireError("busy", -32013, { evenerErrorInfo: "host-busy-operation", operationId: "op-42" });
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText("/srv/evener/evener")).toBeTruthy());
  await user.click(within(dialog).getByRole("button", { name: "Deploy" }));

  await waitFor(() =>
    expect(within(dialog).getByText(/Another operation is running on this host \(op-42\)\./)).toBeTruthy(),
  );
  // The open/wait affordance is S15's; this slice shows the concrete refusal
  // and never a bare retry.
  expect(within(dialog).queryByRole("button", { name: "Retry deploy" })).toBeNull();
  expect((within(dialog).getByRole("button", { name: "Deploy" }) as HTMLButtonElement).disabled).toBe(true);
});

test("host-detached offers Connect and re-plan", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  let plans = 0;
  fake.on("evener/host/plan", () => {
    plans += 1;
    return { outcome: "planned", plan: planFixture(), token: `tok-${plans}` };
  });
  fake.on("evener/host/deploy", () => {
    throw new WireError("no live attached channel", -32014, { evenerErrorInfo: "host-detached" });
  });
  fake.on("evener/host/attach", () => ({ attached: true, host: "beta" }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText("/srv/evener/evener")).toBeTruthy());
  await user.click(within(dialog).getByRole("button", { name: "Deploy" }));

  await waitFor(() => expect(within(dialog).getByText(/The host has no live attached channel\./)).toBeTruthy());
  await user.click(within(dialog).getByRole("button", { name: "Connect and plan" }));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/attach")).toHaveLength(1));
  await waitFor(() => expect(plans).toBe(2));
});

test("a lost response can be retried under the same operation ID", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/plan", () => ({ outcome: "planned", plan: planFixture(), token: "tok-1" }));
  let deploys = 0;
  fake.on("evener/host/deploy", () => {
    deploys += 1;
    if (deploys === 1) throw new RequestTimeoutError("no response");
    return { id: "op-1", clientOperationId: "client-op-1", state: "pending" };
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText("/srv/evener/evener")).toBeTruthy());
  await user.click(within(dialog).getByRole("button", { name: "Deploy" }));

  await waitFor(() => expect(within(dialog).getByText(/did not answer before the request timed out/)).toBeTruthy());
  await user.click(within(dialog).getByRole("button", { name: "Retry deploy" }));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/deploy")).toHaveLength(2));
  const calls = fake.calls.filter((c) => c.method === "evener/host/deploy");
  // §13: a retry after a lost response reuses the same operation ID.
  expect((calls[0]!.params as { operationId: string }).operationId).toBe(
    (calls[1]!.params as { operationId: string }).operationId,
  );
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("Restart confirms then submits the intended pair and a client operation ID", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/restart", () => ({ id: "op-9", clientOperationId: "client-op-9", state: "pending" }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Restart" }));
  const dialog = await screen.findByRole("dialog", { name: "Restart beta?" });
  await user.click(within(dialog).getByRole("button", { name: "Restart" }));

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/restart")).toHaveLength(1));
  const call = fake.calls.find((c) => c.method === "evener/host/restart")!;
  const params = call.params as { operationId: string; generation: number; incarnationId: string };
  expect(call.params).toMatchObject({ name: "beta", generation: 1, incarnationId: "inc-1" });
  expect(params.operationId).not.toBe("");
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("a restart busy refusal renders concretely and disables its retry", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/restart", () => {
    throw new WireError("busy", -32013, { evenerErrorInfo: "host-busy-operation", operationId: "op-42" });
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Restart" }));
  const dialog = await screen.findByRole("dialog", { name: "Restart beta?" });
  await user.click(within(dialog).getByRole("button", { name: "Restart" }));

  await waitFor(() =>
    expect(within(dialog).getByText(/Another operation is running on this host \(op-42\)\./)).toBeTruthy(),
  );
  expect((within(dialog).getByRole("button", { name: "Restart" }) as HTMLButtonElement).disabled).toBe(true);
});

test("a restart stale-entry re-reads and retries once with the fresh pair", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [row({ name: "beta", address: "b.example", generation: 4, incarnationId: "inc-4" })],
  }));
  let restarts = 0;
  fake.on("evener/host/restart", () => {
    restarts += 1;
    if (restarts === 1) {
      throw new WireError('host "beta": registration moved; retry', -32013, { evenerErrorInfo: "stale-entry" });
    }
    return { id: "op-10", clientOperationId: "client-op-10", state: "pending" };
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Restart" }));
  const dialog = await screen.findByRole("dialog", { name: "Restart beta?" });
  await user.click(within(dialog).getByRole("button", { name: "Restart" }));

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/restart")).toHaveLength(2));
  const calls = fake.calls.filter((c) => c.method === "evener/host/restart");
  expect(calls[1]!.params as { generation: number; incarnationId: string }).toMatchObject({
    generation: 4,
    incarnationId: "inc-4",
  });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});
