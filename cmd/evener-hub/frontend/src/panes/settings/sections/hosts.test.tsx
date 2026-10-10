import {
  HOST_CHANGED_MESSAGE,
  type HostPlan,
  type HostRow,
  type OperationRecord,
  RequestTimeoutError,
  WireError,
} from "@evener/appwire-client";
import { FakeClient, gateSettlements } from "@evener/appwire-client/testing/fakeClient";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { hostOpsStore } from "../../../stores/hostOps";
import { hostsStore } from "../../../stores/hosts";
import { enterText } from "../../../textEntryTestUtils";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { HOST_POLL_MS, HostsSection, OPERATION_POLL_MS } from "./hosts";

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

test("an edit made while someone else changed the host is refused and never saved over theirs", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let current = row({ name: "beta", address: "b.example", generation: 1, incarnationId: "inc-1" });
  fake.on("evener/host/list", () => ({ hosts: [current] }));
  // The hub's guard: only the current pair may change the entry.
  const sentGenerations: number[] = [];
  fake.on("evener/host/update", (params: { expectedGeneration: number }) => {
    sentGenerations.push(params.expectedGeneration);
    if (params.expectedGeneration !== current.generation) {
      throw new WireError('host "beta": the entry moved', -32013, {
        evenerErrorInfo: "stale-entry",
        binding: "generation",
      });
    }
    return { outcome: "committed", host: current };
  });
  render(<HostsSection sectionId="hosts" />);
  const rowEl = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(rowEl).getByRole("button", { name: "Edit" }));
  const dialog = await screen.findByRole("dialog");

  // Another client edits beta, and the section's poll brings it in while this
  // dialog is still open on generation 1.
  current = row({ name: "beta", address: "b-other.example", generation: 2, incarnationId: "inc-1" });
  await act(async () => {
    await hostsStore.getState().refresh();
  });

  await user.clear(within(dialog).getByLabelText("SSH address"));
  await enterText(user, within(dialog).getByLabelText("SSH address"), "b2.example");
  await user.click(within(dialog).getByRole("button", { name: "Save" }));

  const alert = await within(dialog).findByRole("alert");
  expect(alert.textContent).toBe(HOST_CHANGED_MESSAGE);
  // One attempt, carrying the pair of the row the dialog opened on.
  expect(sentGenerations).toEqual([1]);
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

test("remnant-open names the blocking remnant and offers teardown-retry, never Connect or re-plan", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let plans = 0;
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/plan", () => {
    plans += 1;
    return {
      outcome: "no-token",
      staleFacts: {
        message: 'host "beta" is fenced by an open teardown remnant',
        attached: true,
        reason: "remnant-open",
      },
      terminal: false,
      remnantId: "remnant-7",
    };
  });
  fake.on("evener/host/teardown-retry", () => ({
    outcome: "teardown-complete",
    hostKind: "live",
    host: row({ name: "beta", address: "b.example" }),
    remnantId: "remnant-7",
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  // §13: surfaces the blocking remnantId with a teardown-retry affordance;
  // never Connect, never re-plan (a re-plan mints nothing while the remnant is
  // open).
  await waitFor(() => expect(within(dialog).getByText(/remnant-7/)).toBeTruthy());
  expect(within(dialog).getByText(/fenced by an open teardown remnant/)).toBeTruthy();
  expect(within(dialog).queryByRole("button", { name: /Connect/ })).toBeNull();
  expect(within(dialog).queryByRole("button", { name: /Retry plan/ })).toBeNull();
  expect((within(dialog).getByRole("button", { name: "Deploy" }) as HTMLButtonElement).disabled).toBe(true);

  // Submitting the repair calls teardown-retry with exactly the remnant id and
  // renders the arm it answers.
  await user.click(within(dialog).getByRole("button", { name: "Teardown retry" }));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/teardown-retry")).toHaveLength(1));
  expect(fake.calls.find((c) => c.method === "evener/host/teardown-retry")?.params).toEqual({ remnantId: "remnant-7" });
  await waitFor(() =>
    expect(within(dialog).getByText(/Teardown completed; remnant remnant-7 is resolved\./)).toBeTruthy(),
  );
  // Resolved: re-planning is the allowed next step, and only now.
  await user.click(within(dialog).getByRole("button", { name: "Plan again" }));
  await waitFor(() => expect(plans).toBe(2));
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

test("a host-detached restart offers Connect and restart, never a bare repeat", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let advanced = false;
  fake.on("evener/host/list", () => ({
    hosts: [
      {
        ...row({ name: "beta", address: "b.example" }),
        generation: advanced ? 4 : 1,
        incarnationId: advanced ? "inc-4" : "inc-1",
      },
    ],
  }));
  let restarts = 0;
  fake.on("evener/host/restart", () => {
    restarts += 1;
    if (restarts === 1) {
      throw new WireError("no live attached channel", -32014, { evenerErrorInfo: "host-detached" });
    }
    return { id: "op-13", clientOperationId: "client-op-13", state: "pending" };
  });
  fake.on("evener/host/attach", () => ({ attached: true, host: "beta" }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Restart" }));
  const dialog = await screen.findByRole("dialog", { name: "Restart beta?" });
  await user.click(within(dialog).getByRole("button", { name: "Restart" }));

  await waitFor(() => expect(within(dialog).getByText(/The host has no live attached channel\./)).toBeTruthy());
  // No bare repeat: the confirm is disabled and the supported recovery is the
  // Connect action.
  expect((within(dialog).getByRole("button", { name: "Restart" }) as HTMLButtonElement).disabled).toBe(true);

  // The registration moved while the host was detached.
  advanced = true;
  await user.click(within(dialog).getByRole("button", { name: "Connect and restart" }));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/attach")).toHaveLength(1));
  // The confirmation is re-seeded against the current pair.
  const confirm = await within(dialog).findByRole("button", { name: "Restart" });
  await waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false));
  await user.click(confirm);

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/restart")).toHaveLength(2));
  const second = fake.calls.filter((c) => c.method === "evener/host/restart")[1]!;
  expect(second.params as { generation: number; incarnationId: string }).toMatchObject({
    generation: 4,
    incarnationId: "inc-4",
  });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("the Deploy confirmation cannot submit before a plan is rendered", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  const plans = gateSettlements(fake, "evener/host/plan");
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });

  // The plan is still in flight: there is no confirmed plan to submit.
  await waitFor(() => expect(plans.length).toBe(1));
  expect((within(dialog).getByRole("button", { name: "Deploy" }) as HTMLButtonElement).disabled).toBe(true);

  plans[0]!.resolve({ outcome: "planned", plan: planFixture(), token: "tok-1" });
  await waitFor(() =>
    expect((within(dialog).getByRole("button", { name: "Deploy" }) as HTMLButtonElement).disabled).toBe(false),
  );
});

test("the Restart confirm is disabled while no attempt is seeded, so it can never no-op", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/restart", () => ({ id: "op-15", clientOperationId: "client-op-15", state: "pending" }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Restart" }));
  const dialog = await screen.findByRole("dialog", { name: "Restart beta?" });
  // No attempt to submit: the confirm is inert (restart() is a no-op without
  // one), exactly as it is on the first frame before the seeding effect runs.
  act(() => {
    hostOpsStore.getState().discardRestart("beta");
  });
  await waitFor(() =>
    expect((within(dialog).getByRole("button", { name: "Restart" }) as HTMLButtonElement).disabled).toBe(true),
  );
  fireEvent.click(within(dialog).getByRole("button", { name: "Restart" }));
  expect(fake.calls.filter((c) => c.method === "evener/host/restart")).toHaveLength(0);
});

test("a surfaced restart stale-entry names the operation, not a plan", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  // The pair never moves, so the single automatic retry refuses stale-entry
  // again and the refusal surfaces in the dialog.
  fake.on("evener/host/restart", () => {
    throw new WireError('host "beta": registration moved; retry', -32013, { evenerErrorInfo: "stale-entry" });
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Restart" }));
  const dialog = await screen.findByRole("dialog", { name: "Restart beta?" });
  await user.click(within(dialog).getByRole("button", { name: "Restart" }));

  await waitFor(() =>
    expect(within(dialog).getByText(/The host changed since this operation was prepared\./)).toBeTruthy(),
  );
});

// --- S15: operations polling, progress/terminal render, replay ---------------

// operationRecord is one `evener/host/operations` record (deploy-pipeline spec
// 08b §10).
function operationRecord(overrides: Partial<OperationRecord> = {}): OperationRecord {
  return {
    id: "op-1",
    clientOperationId: "client-op-1",
    host: "beta",
    generation: 1,
    incarnationId: "inc-1",
    kind: "deploy",
    state: "running",
    progress: [],
    createdAt: "2026-09-28T08:00:00Z",
    updatedAt: "2026-09-28T08:00:05Z",
    hostRemoved: false,
    ...overrides,
  };
}

// startDeploy drives one Deploy dialog to a started operation, the row the
// operations poll then tracks.
async function startDeploy(fake: FakeClient): Promise<{ betaRow: HTMLElement; view: ReturnType<typeof render> }> {
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/plan", () => ({ outcome: "planned", plan: planFixture(), token: "tok-1" }));
  fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "pending" }));
  const view = render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  fireEvent.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText("/srv/evener/evener")).toBeTruthy());
  fireEvent.click(within(dialog).getByRole("button", { name: "Deploy" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  return { betaRow, view };
}

const operationReads = (fake: FakeClient): number =>
  fake.calls.filter((call) => call.method === "evener/host/operations").length;

test("an operation's progress renders on its host row, then its failure renders verbatim", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const fake = connectFakeClient();
    let record = operationRecord({
      state: "running",
      progress: [{ ts: "2026-09-28T08:00:05Z", message: "pushing evener to /srv/evener/evener" }],
    });
    fake.on("evener/host/operations", () => ({ operations: [record] }));
    const { betaRow } = await startDeploy(fake);

    // The pane's mounted poll reads the started operation and renders its
    // progress on the operation's own row (registry spec 08 §13).
    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS));
    await waitFor(() => expect(within(betaRow).getByText("pushing evener to /srv/evener/evener")).toBeTruthy());
    expect(within(betaRow).getByText("Deploying…")).toBeTruthy();

    // The operation fails with the worker's 04b error: the row surfaces it
    // VERBATIM — never a rewritten or generic message.
    const verbatim = "deploy failed: the remote evener service exited with status 1 after the swap";
    record = operationRecord({ state: "failed", result: { ok: false, message: verbatim } });
    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS));
    await waitFor(() => expect(within(betaRow).getByText(verbatim)).toBeTruthy());
    expect(within(betaRow).getByText("Deploy failed")).toBeTruthy();
    expect(within(betaRow).queryByText("Deploying…")).toBeNull();
  } finally {
    vi.useRealTimers();
  }
});

test("a replayed operation renders its past terminal outcome, never a fresh Deploying row", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: planFixture(), token: "tok-1" }));
    // The dedup hit answers the existing record (08b §10): the retry reuses the
    // client operation ID and the hub returns this operation, not a new one.
    fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "failed" }));
    const verbatim = "deploy failed: the remote service did not come back healthy";
    // A tombstoned replay (S6's `compacted` marker) carries the retained result.
    fake.on("evener/host/operations", () => ({
      operations: [
        {
          ...operationRecord({ state: "failed", result: { ok: false, message: verbatim } }),
          compacted: true,
        } as unknown as OperationRecord,
      ],
    }));
    render(<HostsSection sectionId="hosts" />);
    const betaRow = (await screen.findByText("beta")).closest("li")!;
    fireEvent.click(within(betaRow).getByRole("button", { name: "Deploy" }));
    const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
    await waitFor(() => expect(within(dialog).getByText("/srv/evener/evener")).toBeTruthy());
    fireEvent.click(within(dialog).getByRole("button", { name: "Deploy" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    // The terminal seed is read once for its retained body: the row renders the
    // past outcome, marked as the compacted replay — not a running operation.
    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS));
    await waitFor(() => expect(within(betaRow).getByText(verbatim)).toBeTruthy());
    expect(within(betaRow).getByText("Deploy failed")).toBeTruthy();
    expect(within(betaRow).getByText(/replayed from a compacted operation/)).toBeTruthy();
    expect(within(betaRow).queryByText("Deploying…")).toBeNull();
  } finally {
    vi.useRealTimers();
  }
});

test("the mounted poll stops reading once the record is terminal", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const fake = connectFakeClient();
    fake.on("evener/host/operations", () => ({
      operations: [operationRecord({ state: "complete", result: { ok: true, message: "deployed 1.5.0" } })],
    }));
    const { betaRow } = await startDeploy(fake);

    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS));
    await waitFor(() => expect(within(betaRow).getByText("Deploy complete")).toBeTruthy());
    const reads = operationReads(fake);
    expect(reads).toBe(1);

    // A terminal record ends the loop: further ticks issue no reads.
    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS * 3));
    expect(operationReads(fake)).toBe(reads);
  } finally {
    vi.useRealTimers();
  }
});

test("unmounting the section stops the operation poll", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const fake = connectFakeClient();
    fake.on("evener/host/operations", () => ({
      operations: [operationRecord({ state: "running", progress: [{ ts: "t", message: "installing" }] })],
    }));
    const { view } = await startDeploy(fake);
    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS));
    await waitFor(() => expect(screen.getByText("installing")).toBeTruthy());

    const reads = operationReads(fake);
    view.unmount();
    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS * 3));
    expect(operationReads(fake)).toBe(reads);
  } finally {
    vi.useRealTimers();
  }
});

test("a stopped read renders a visible progress state on the row, never a silent stall", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const fake = connectFakeClient();
    fake.on("evener/host/operations", () => ({
      operations: [operationRecord({ state: "running", progress: [{ ts: "t", message: "installing" }] })],
    }));
    const { betaRow } = await startDeploy(fake);
    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS));
    await waitFor(() => expect(within(betaRow).getByText("installing")).toBeTruthy());

    // The connection drops: the last-known progress stays, and the row says the
    // updates stopped instead of pretending the operation is still reporting.
    connectionStore.setState({ client: null });
    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS));
    await waitFor(() => expect(within(betaRow).getByText(/Progress updates stopped/)).toBeTruthy());
    expect(within(betaRow).getByText("installing")).toBeTruthy();
  } finally {
    vi.useRealTimers();
  }
});

test("a slow read is not re-issued by later ticks, and still publishes when it lands", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: planFixture(), token: "tok-1" }));
    fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "pending" }));
    const settlements = gateSettlements(fake, "evener/host/operations");
    render(<HostsSection sectionId="hosts" />);
    const betaRow = (await screen.findByText("beta")).closest("li")!;
    fireEvent.click(within(betaRow).getByRole("button", { name: "Deploy" }));
    const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
    await waitFor(() => expect(within(dialog).getByText("/srv/evener/evener")).toBeTruthy());
    fireEvent.click(within(dialog).getByRole("button", { name: "Deploy" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    // The first tick's read hangs. Later ticks must not pile more reads on top
    // of it (one outstanding read per operation)...
    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS * 4));
    expect(operationReads(fake)).toBe(1);

    // ...and when it lands, its record still publishes: the tick never issued a
    // newer read that would have starved this one.
    await act(async () => {
      settlements[0]!.resolve({
        operations: [operationRecord({ state: "complete", result: { ok: true, message: "deployed 1.5.0" } })],
      });
    });
    await waitFor(() => expect(within(betaRow).getByText("deployed 1.5.0")).toBeTruthy());
    expect(within(betaRow).getByText("Deploy complete")).toBeTruthy();
  } finally {
    vi.useRealTimers();
  }
});

test("an operation from a previous incarnation is not rendered, and still settles", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const fake = connectFakeClient();
    let reCreated = false;
    let record = operationRecord({
      state: "running",
      progress: [{ ts: "t", message: "old incarnation work" }],
    });
    fake.on("evener/host/list", () => ({
      hosts: [
        reCreated
          ? row({ name: "beta", address: "b.example", generation: 9, incarnationId: "inc-9" })
          : row({ name: "beta", address: "b.example" }),
      ],
    }));
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: planFixture(), token: "tok-1" }));
    fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "pending" }));
    fake.on("evener/host/operations", () => ({ operations: [record] }));
    render(<HostsSection sectionId="hosts" />);
    const betaRow = (await screen.findByText("beta")).closest("li")!;
    fireEvent.click(within(betaRow).getByRole("button", { name: "Deploy" }));
    const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
    await waitFor(() => expect(within(dialog).getByText("/srv/evener/evener")).toBeTruthy());
    fireEvent.click(within(dialog).getByRole("button", { name: "Deploy" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    // The operation runs on the incarnation the row displays (generation 1):
    // its progress renders.
    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS));
    await waitFor(() => expect(hostOpsStore.getState().operations.beta?.fetched).toBe(true));
    expect(within(betaRow).getByText("old incarnation work")).toBeTruthy();

    // The name is removed and re-added (a new generation): the old operation
    // no longer belongs to the row and stops rendering...
    reCreated = true;
    await act(() => vi.advanceTimersByTimeAsync(HOST_POLL_MS));
    await waitFor(() => expect(within(betaRow).queryByText("old incarnation work")).toBeNull());
    expect(within(betaRow).queryByText("Deploying…")).toBeNull();

    // ...but it is still polled to its terminal state, so the ref can settle
    // and never lingers non-terminal while the name exists.
    record = operationRecord({ state: "failed", result: { ok: false, message: "push failed" } });
    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS));
    await waitFor(() => expect(hostOpsStore.getState().operations.beta?.state).toBe("failed"));
    expect(within(betaRow).queryByText("old incarnation work")).toBeNull();
    expect(within(betaRow).queryByText("Deploy failed")).toBeNull();
  } finally {
    vi.useRealTimers();
  }
});

test("a dedup hit that is still running does not toast a fresh start", async () => {
  resetToastStoreForTests();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  fake.on("evener/host/plan", () => ({ outcome: "planned", plan: planFixture(), token: "tok-1" }));
  // 08b §10: a fresh create reports `pending`; a dedup hit answers the existing
  // record's actual state — a still-running operation is a replay too.
  fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "running" }));
  fake.on("evener/host/operations", () => ({ operations: [operationRecord({ state: "running" })] }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  fireEvent.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText("/srv/evener/evener")).toBeTruthy());
  fireEvent.click(within(dialog).getByRole("button", { name: "Deploy" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  const texts = getToasts().map((toast) => toast.text);
  expect(texts).toContain("Deploy beta repeated its existing operation (running).");
  expect(texts.join(" ")).not.toContain("Deploy started");
  // The row renders the running operation it adopted.
  expect(within(betaRow).getByText("Deploying…")).toBeTruthy();
});

// --- S16: remnant retry/recover ----------------------------------------------

test("a remnant-gated row carries the teardown-retry affordance and submits its remnantId", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [row({ name: "alpha", address: "a.example" }), row({ name: "beta", openRemnantId: "remnant-7" })],
  }));
  fake.on("evener/host/teardown-retry", () => ({
    outcome: "teardown-complete",
    hostKind: "live",
    host: row({ name: "beta" }),
    remnantId: "remnant-7",
  }));
  render(<HostsSection sectionId="hosts" />);
  const alphaRow = (await screen.findByText("alpha")).closest("li")!;
  // Only a row whose name holds an open remnant carries the repair.
  expect(within(alphaRow).queryByRole("button", { name: "Teardown retry" })).toBeNull();

  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Teardown retry" }));
  const dialog = await screen.findByRole("dialog", { name: "Repair remnant remnant-7" });
  await user.click(within(dialog).getByRole("button", { name: "Teardown retry" }));

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/teardown-retry")).toHaveLength(1));
  expect(fake.calls.find((c) => c.method === "evener/host/teardown-retry")?.params).toEqual({ remnantId: "remnant-7" });
  await waitFor(() =>
    expect(within(dialog).getByText(/Teardown completed; remnant remnant-7 is resolved\./)).toBeTruthy(),
  );
});

test("a live-attempt busy refusal renders the typed busy with retry-later, never a silent no-op", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", openRemnantId: "remnant-7" })] }));
  fake.on("evener/host/teardown-retry", () => {
    throw new WireError('host "beta" is busy: a live teardown attempt owns the remnant', -32014, {
      evenerErrorInfo: "host-busy-transient",
    });
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Teardown retry" }));
  const dialog = await screen.findByRole("dialog", { name: "Repair remnant remnant-7" });
  await user.click(within(dialog).getByRole("button", { name: "Teardown retry" }));

  await waitFor(() => expect(within(dialog).getByText(/The host is busy right now\./)).toBeTruthy());
  expect(within(dialog).getByText(/a live teardown attempt owns the remnant/)).toBeTruthy();
  // Retry later is the affordance the busy class earns.
  expect(within(dialog).getByRole("button", { name: "Retry teardown" })).toBeTruthy();
});

test("an escalated row offers the audited recover and collects its attestation", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [row({ name: "beta", openRemnantId: "remnant-7", escalationAgeSec: 7200 })],
  }));
  fake.on("evener/host/teardown-recover", (params) => ({
    outcome: "recovered-cleared",
    remnantId: params.remnantId,
    clearedName: "beta",
    clearedAt: "2026-09-28T09:30:00Z",
    hostKind: "live",
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  // §13: past the escalation bound the row carries the recover affordance.
  await user.click(within(betaRow).getByRole("button", { name: "Recover remnant" }));
  const dialog = await screen.findByRole("dialog", { name: "Repair remnant remnant-7" });

  // The wire's attestation fields, field-for-field (registry spec 08 §11).
  expect((within(dialog).getByLabelText("Statement") as HTMLInputElement).value).toBe("teardown-verified-absent");
  await user.type(within(dialog).getByLabelText("Operator"), "operator-1");
  await user.click(within(dialog).getByRole("button", { name: "Recover remnant" }));

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/teardown-recover")).toHaveLength(1));
  const call = fake.calls.find((c) => c.method === "evener/host/teardown-recover")!;
  expect(call.params).toMatchObject({
    remnantId: "remnant-7",
    attestation: { operator: "operator-1", statement: "teardown-verified-absent" },
  });
  // observedAt is prefilled with the operator's current instant (RFC3339).
  expect((call.params as { attestation: { observedAt: string } }).attestation.observedAt).not.toBe("");
  await waitFor(() =>
    expect(
      within(dialog).getByText(/Recovered: cleared remnant remnant-7 for beta at 2026-09-28T09:30:00Z\./),
    ).toBeTruthy(),
  );
});

test("a tombstone row with an open remnant carries the retry affordance while live actions stay disabled", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [row({ name: "beta", removed: true, openRemnantId: "remnant-7", retainedRows: 0 })],
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;

  expect(within(betaRow).getByText("removed")).toBeTruthy();
  // §13: a tombstone row's live-host actions render disabled (never firing);
  // the remnant repair is its one action.
  expect((within(betaRow).getByRole("button", { name: "Deploy" }) as HTMLButtonElement).disabled).toBe(true);
  expect((within(betaRow).getByRole("button", { name: "Restart" }) as HTMLButtonElement).disabled).toBe(true);
  expect(within(betaRow).queryByRole("button", { name: "Edit" })).toBeNull();
  expect(within(betaRow).queryByRole("button", { name: "Remove" })).toBeNull();
  expect(within(betaRow).queryByRole("button", { name: "Connect" })).toBeNull();
  expect(within(betaRow).getByRole("button", { name: "Teardown retry" })).toBeTruthy();
});

test("a failed retry arm past the escalation bound escalates to the recover affordance", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  // The row was stamped inside the bound (escalationAgeSec absent); the retry
  // arm itself reports the remnant past it, so the escalation must still show.
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", openRemnantId: "remnant-7" })] }));
  fake.on("evener/host/teardown-retry", () => ({
    outcome: "committed-with-teardown-failure",
    hostKind: "live",
    host: row({ name: "beta", openRemnantId: "remnant-7" }),
    remnantId: "remnant-7",
    seam: "update-host",
    escalationAgeSec: 7200,
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  expect(within(betaRow).queryByRole("button", { name: "Recover remnant" })).toBeNull();

  await user.click(within(betaRow).getByRole("button", { name: "Teardown retry" }));
  const dialog = await screen.findByRole("dialog", { name: "Repair remnant remnant-7" });
  await user.click(within(dialog).getByRole("button", { name: "Teardown retry" }));

  await waitFor(() =>
    expect(
      within(dialog).getByText(/Teardown failed again at update-host; remnant remnant-7 is still open\./),
    ).toBeTruthy(),
  );
  // The failure arm is a result, not a refusal: retry again or escalate.
  expect(within(dialog).getByRole("button", { name: "Retry teardown" })).toBeTruthy();
  expect(within(dialog).getByRole("button", { name: "Recover remnant" })).toBeTruthy();
});

// --- S16 round-1 review fixes -------------------------------------------------

test("a failed retry arm converges the row, so the row-level recover affordance appears", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let escalation: number | undefined;
  fake.on("evener/host/list", () => ({
    hosts: [
      {
        ...row({ name: "beta", openRemnantId: "remnant-7" }),
        ...(escalation === undefined ? {} : { escalationAgeSec: escalation }),
      },
    ],
  }));
  fake.on("evener/host/teardown-retry", () => ({
    outcome: "committed-with-teardown-failure",
    hostKind: "live",
    host: row({ name: "beta", openRemnantId: "remnant-7" }),
    remnantId: "remnant-7",
    seam: "update-host",
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  expect(within(betaRow).queryByRole("button", { name: "Recover remnant" })).toBeNull();

  // The retry ran past the escalation bound: the hub stamps the row the next
  // list read answers, and the FAILURE arm must converge it — otherwise the
  // row-level recover affordance never appears.
  escalation = 7200;
  await user.click(within(betaRow).getByRole("button", { name: "Teardown retry" }));
  const dialog = await screen.findByRole("dialog", { name: "Repair remnant remnant-7" });
  await user.click(within(dialog).getByRole("button", { name: "Teardown retry" }));

  await waitFor(() => expect(within(betaRow).getByRole("button", { name: "Recover remnant" })).toBeTruthy());
});

test("the repair dialog follows the live row: an escalation lands while it is open", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let escalation: number | undefined;
  fake.on("evener/host/list", () => ({
    hosts: [
      {
        ...row({ name: "beta", openRemnantId: "remnant-7" }),
        ...(escalation === undefined ? {} : { escalationAgeSec: escalation }),
      },
    ],
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Teardown retry" }));
  const dialog = await screen.findByRole("dialog", { name: "Repair remnant remnant-7" });
  expect(within(dialog).queryByRole("button", { name: "Recover remnant" })).toBeNull();

  // The pane's poll learns the remnant passed the escalation bound: the dialog
  // renders the LIVE row, never the click-time snapshot.
  escalation = 7200;
  await act(async () => {
    await hostsStore.getState().refresh();
  });
  await waitFor(() => expect(within(dialog).getByRole("button", { name: "Recover remnant" })).toBeTruthy());
});

test("a different remnant on the name renders neutral, never the previous remnant's refusal", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let remnant = "remnant-7";
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", openRemnantId: remnant })] }));
  fake.on("evener/host/teardown-retry", () => {
    throw new WireError('host "beta" is busy: a live teardown attempt owns the remnant', -32014, {
      evenerErrorInfo: "host-busy-transient",
    });
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Teardown retry" }));
  const first = await screen.findByRole("dialog", { name: "Repair remnant remnant-7" });
  await user.click(within(first).getByRole("button", { name: "Teardown retry" }));
  await waitFor(() => expect(within(first).getByText(/The host is busy right now\./)).toBeTruthy());

  // A NEW remnant replaces the name's remnant while the dialog is open: the
  // old refusal belongs to the old remnant, and the dialog follows the live row.
  remnant = "remnant-8";
  await act(async () => {
    await hostsStore.getState().refresh();
  });
  const second = await screen.findByRole("dialog", { name: "Repair remnant remnant-8" });
  await waitFor(() => expect(within(second).getByRole("button", { name: "Teardown retry" })).toBeTruthy());
  expect(within(second).queryByText(/The host is busy right now\./)).toBeNull();
});

test("the repair dialog closes honestly when the remnant disappears", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let showRemnant = true;
  fake.on("evener/host/list", () => ({
    hosts: [showRemnant ? row({ name: "beta", openRemnantId: "remnant-7" }) : row({ name: "beta" })],
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Teardown retry" }));
  expect(await screen.findByRole("dialog", { name: "Repair remnant remnant-7" })).toBeTruthy();

  showRemnant = false;
  await act(async () => {
    await hostsStore.getState().refresh();
  });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("the restart surface continues after the remnant resolves, against a fresh pair", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  let fenced = true;
  fake.on("evener/host/restart", () => {
    if (fenced) {
      throw new WireError('host "beta" is fenced by an open teardown remnant', -32013, {
        evenerErrorInfo: "remnant-open",
        remnantId: "remnant-7",
      });
    }
    return { id: "op-9", clientOperationId: "client-op-9", state: "pending" };
  });
  fake.on("evener/host/teardown-retry", () => ({
    outcome: "teardown-complete",
    hostKind: "live",
    host: row({ name: "beta" }),
    remnantId: "remnant-7",
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Restart" }));
  const dialog = await screen.findByRole("dialog", { name: "Restart beta?" });
  await user.click(within(dialog).getByRole("button", { name: "Restart" }));
  await waitFor(() => expect(within(dialog).getByText(/remnant-7/)).toBeTruthy());
  expect((within(dialog).getByRole("button", { name: "Restart" }) as HTMLButtonElement).disabled).toBe(true);

  await user.click(within(dialog).getByRole("button", { name: "Teardown retry" }));
  await waitFor(() =>
    expect(within(dialog).getByText(/Teardown completed; remnant remnant-7 is resolved\./)).toBeTruthy(),
  );

  // The continuation re-seeds the confirmation against a freshly read pair,
  // under a fresh operation ID — never an empty action row.
  fenced = false;
  await user.click(within(dialog).getByRole("button", { name: "Continue restart" }));
  await waitFor(() =>
    expect((within(dialog).getByRole("button", { name: "Restart" }) as HTMLButtonElement).disabled).toBe(false),
  );
  await user.click(within(dialog).getByRole("button", { name: "Restart" }));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/restart")).toHaveLength(2));
  const calls = fake.calls.filter((c) => c.method === "evener/host/restart");
  expect((calls[0]!.params as { operationId: string }).operationId).not.toBe(
    (calls[1]!.params as { operationId: string }).operationId,
  );
});

test("a row whose remnant id is empty carries no repair affordance", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", openRemnantId: "" })] }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  expect(within(betaRow).queryByRole("button", { name: "Teardown retry" })).toBeNull();
  expect(within(betaRow).queryByRole("button", { name: "Recover remnant" })).toBeNull();
});

test("closing the repair dialog drops its refusal, so a later opening starts clean", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", openRemnantId: "remnant-7" })] }));
  fake.on("evener/host/teardown-retry", () => {
    throw new WireError('host "beta" is busy: a live teardown attempt owns the remnant', -32014, {
      evenerErrorInfo: "host-busy-transient",
    });
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Teardown retry" }));
  let dialog = await screen.findByRole("dialog", { name: "Repair remnant remnant-7" });
  await user.click(within(dialog).getByRole("button", { name: "Teardown retry" }));
  await waitFor(() => expect(within(dialog).getByText(/The host is busy right now\./)).toBeTruthy());

  // The dialog renders its panel close control and the footer Close: both call
  // the same onClose, which owns the repair-state clear.
  const closes = within(dialog).getAllByRole("button", { name: "Close" });
  await user.click(closes[closes.length - 1]!);
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(hostOpsStore.getState().repairs.beta).toBeUndefined();

  await user.click(within(betaRow).getByRole("button", { name: "Teardown retry" }));
  dialog = await screen.findByRole("dialog", { name: "Repair remnant remnant-7" });
  expect(within(dialog).queryByText(/The host is busy right now\./)).toBeNull();
  expect(within(dialog).getByRole("button", { name: "Teardown retry" })).toBeTruthy();
});

test("closing the deploy dialog drops its remnant repair state", async () => {
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
  fake.on("evener/host/teardown-retry", () => {
    throw new WireError('host "beta" is busy: a live teardown attempt owns the remnant', -32014, {
      evenerErrorInfo: "host-busy-transient",
    });
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText(/remnant-7/)).toBeTruthy());
  await user.click(within(dialog).getByRole("button", { name: "Teardown retry" }));
  await waitFor(() => expect(within(dialog).getByText(/The host is busy right now\./)).toBeTruthy());
  expect(hostOpsStore.getState().repairs.beta).toBeDefined();

  await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(hostOpsStore.getState().repairs.beta).toBeUndefined();
});

// --- S16 round-2 review fixes -------------------------------------------------

test("a resolved remnant's state never suppresses a new remnant's repair", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let remnant = "remnant-7";
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", openRemnantId: remnant })] }));
  fake.on("evener/host/teardown-retry", () => ({
    outcome: "teardown-complete",
    hostKind: "live",
    host: row({ name: "beta" }),
    remnantId: "remnant-7",
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Teardown retry" }));
  const dialog = await screen.findByRole("dialog", { name: "Repair remnant remnant-7" });
  await user.click(within(dialog).getByRole("button", { name: "Teardown retry" }));
  await waitFor(() =>
    expect(within(dialog).getByText(/Teardown completed; remnant remnant-7 is resolved\./)).toBeTruthy(),
  );

  // A NEW remnant opens on the name while the resolved arm is still stored:
  // the controls must render the new remnant's own state, never the stale arm.
  remnant = "remnant-8";
  await act(async () => {
    await hostsStore.getState().refresh();
  });
  const second = await screen.findByRole("dialog", { name: "Repair remnant remnant-8" });
  expect(within(second).queryByText(/remnant-7 is resolved/)).toBeNull();
  expect(within(second).getByRole("button", { name: "Teardown retry" })).toBeTruthy();
});

test("a dialog whose remnant disappeared does not auto-open for a later remnant", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let remnant: string | undefined = "remnant-7";
  fake.on("evener/host/list", () => ({
    hosts: [remnant === undefined ? row({ name: "beta" }) : row({ name: "beta", openRemnantId: remnant })],
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Teardown retry" }));
  expect(await screen.findByRole("dialog", { name: "Repair remnant remnant-7" })).toBeTruthy();

  // The remnant disappears: the dialog unmounts and must not stay armed.
  remnant = undefined;
  await act(async () => {
    await hostsStore.getState().refresh();
  });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  // A later remnant on the name must not re-open the dialog by itself.
  remnant = "remnant-8";
  await act(async () => {
    await hostsStore.getState().refresh();
  });
  await waitFor(() => expect(within(betaRow).getByRole("button", { name: "Teardown retry" })).toBeTruthy());
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("a resolved remnant's inline state never suppresses a new remnant's repair", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [row({ name: "beta", address: "b.example" })] }));
  let plans = 0;
  fake.on("evener/host/plan", () => {
    plans += 1;
    return {
      outcome: "no-token",
      staleFacts: {
        message: 'host "beta" is fenced by an open teardown remnant',
        attached: true,
        reason: "remnant-open",
      },
      terminal: false,
      remnantId: plans === 1 ? "remnant-7" : "remnant-8",
    };
  });
  fake.on("evener/host/teardown-retry", () => ({
    outcome: "teardown-complete",
    hostKind: "live",
    host: row({ name: "beta" }),
    remnantId: "remnant-7",
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Deploy" }));
  const dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
  await waitFor(() => expect(within(dialog).getByText(/remnant-7/)).toBeTruthy());
  await user.click(within(dialog).getByRole("button", { name: "Teardown retry" }));
  await waitFor(() =>
    expect(within(dialog).getByText(/Teardown completed; remnant remnant-7 is resolved\./)).toBeTruthy(),
  );

  // The re-plan answers a NEW remnant while the resolved arm is still stored:
  // the inline controls must render the new remnant's own state, never the
  // stale arm (which would show "resolved" and offer nothing to do).
  await user.click(within(dialog).getByRole("button", { name: "Plan again" }));
  await waitFor(() => expect(within(dialog).getByText(/remnant-8/)).toBeTruthy());
  expect(within(dialog).queryByText(/remnant-7 is resolved/)).toBeNull();
  expect(within(dialog).getByRole("button", { name: "Teardown retry" })).toBeTruthy();
});

test("a refusal that forbids recovery never offers the recover escalation", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [row({ name: "beta", openRemnantId: "remnant-7", escalationAgeSec: 7200 })],
  }));
  fake.on("evener/host/teardown-retry", () => {
    throw new WireError('teardown remnant "remnant-7" is unknown or purged', -32001, {
      evenerErrorInfo: "teardown-unknown-key",
      remnantId: "remnant-7",
    });
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Teardown retry" }));
  const dialog = await screen.findByRole("dialog", { name: "Repair remnant remnant-7" });
  await user.click(within(dialog).getByRole("button", { name: "Teardown retry" }));

  await waitFor(() => expect(within(dialog).getByText(/does not know teardown remnant remnant-7/)).toBeTruthy());
  // Both repairs name an id the hub has already refused: neither is offered.
  expect(within(dialog).queryByRole("button", { name: "Retry teardown" })).toBeNull();
  expect(within(dialog).queryByRole("button", { name: "Recover remnant" })).toBeNull();
});

test("a successful recovery leaves the attestation form and shows the continuation", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [row({ name: "beta", address: "b.example", openRemnantId: "remnant-7", escalationAgeSec: 7200 })],
  }));
  fake.on("evener/host/restart", () => {
    throw new WireError('host "beta" is fenced by an open teardown remnant', -32013, {
      evenerErrorInfo: "remnant-open",
      remnantId: "remnant-7",
    });
  });
  fake.on("evener/host/teardown-recover", (params) => ({
    outcome: "recovered-cleared",
    remnantId: params.remnantId,
    clearedName: "beta",
    clearedAt: "2026-09-28T09:30:00Z",
    hostKind: "live",
  }));
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Restart" }));
  const dialog = await screen.findByRole("dialog", { name: "Restart beta?" });
  await user.click(within(dialog).getByRole("button", { name: "Restart" }));
  await waitFor(() => expect(within(dialog).getByText(/remnant-7/)).toBeTruthy());

  await user.click(within(dialog).getByRole("button", { name: "Recover remnant" }));
  await user.type(within(dialog).getByLabelText("Operator"), "operator-1");
  await user.click(within(dialog).getByRole("button", { name: "Recover remnant" }));
  await waitFor(() =>
    expect(
      within(dialog).getByText(/Recovered: cleared remnant remnant-7 for beta at 2026-09-28T09:30:00Z\./),
    ).toBeTruthy(),
  );

  // The form is gone and the continuation is offered, not a stuck form.
  expect(within(dialog).queryByLabelText("Statement")).toBeNull();
  expect(within(dialog).getByRole("button", { name: "Continue restart" })).toBeTruthy();
});

test("a refused recovery resumes the recovery with its attestation, never a bare teardown retry", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [row({ name: "beta", openRemnantId: "remnant-7", escalationAgeSec: 7200 })],
  }));
  let recovers = 0;
  fake.on("evener/host/teardown-recover", (params) => {
    recovers += 1;
    if (recovers === 1) {
      throw new WireError('host "beta" is busy: a live teardown attempt owns the remnant', -32014, {
        evenerErrorInfo: "host-busy-transient",
      });
    }
    return {
      outcome: "recovered-cleared",
      remnantId: params.remnantId,
      clearedName: "beta",
      clearedAt: "2026-09-28T09:30:00Z",
      hostKind: "live",
    };
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Recover remnant" }));
  const dialog = await screen.findByRole("dialog", { name: "Repair remnant remnant-7" });
  await user.type(within(dialog).getByLabelText("Operator"), "operator-1");
  await user.click(within(dialog).getByRole("button", { name: "Recover remnant" }));
  await waitFor(() => expect(within(dialog).getByText(/The host is busy right now\./)).toBeTruthy());

  // Cancel the form: the retry affordance resumes the RECOVERY (with the
  // attestation it already collected), never a bare teardown retry.
  await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
  expect(within(dialog).queryByRole("button", { name: "Retry teardown" })).toBeNull();
  await user.click(within(dialog).getByRole("button", { name: "Retry recovery" }));
  await waitFor(() => expect(recovers).toBe(2));
  await waitFor(() => expect(within(dialog).getByText(/Recovered: cleared remnant remnant-7 for beta/)).toBeTruthy());
  expect(fake.calls.filter((c) => c.method === "evener/host/teardown-retry")).toHaveLength(0);
});

// --- S16 round-3 review fixes -------------------------------------------------

test("a resolved repair still offers its continuation after a later poll", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  try {
    const fake = connectFakeClient();
    let plans = 0;
    fake.on("evener/host/list", () => ({
      hosts: [row({ name: "beta", address: "b.example", openRemnantId: "remnant-7" })],
    }));
    fake.on("evener/host/operations", () => ({
      operations: [operationRecord({ state: "running" })],
    }));
    fake.on("evener/host/plan", () => {
      plans += 1;
      if (plans === 1) return { outcome: "planned", plan: planFixture(), token: "tok-1" };
      return {
        outcome: "no-token",
        staleFacts: {
          message: 'host "beta" is fenced by an open teardown remnant',
          attached: true,
          reason: "remnant-open",
        },
        terminal: false,
        remnantId: "remnant-7",
      };
    });
    fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "pending" }));
    fake.on("evener/host/teardown-retry", () => ({
      outcome: "teardown-complete",
      hostKind: "live",
      host: row({ name: "beta" }),
      remnantId: "remnant-7",
    }));
    render(<HostsSection sectionId="hosts" />);
    const betaRow = (await screen.findByText("beta")).closest("li")!;

    // A tracked operation exists (the deploy the row's dialog started).
    fireEvent.click(within(betaRow).getByRole("button", { name: "Deploy" }));
    let dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
    await waitFor(() => expect(within(dialog).getByText("/srv/evener/evener")).toBeTruthy());
    fireEvent.click(within(dialog).getByRole("button", { name: "Deploy" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS));
    await waitFor(() => expect(hostOpsStore.getState().operations.beta?.fetched).toBe(true));

    // The next plan refuses on the remnant: repair it from the inline controls.
    fireEvent.click(within(betaRow).getByRole("button", { name: "Deploy" }));
    dialog = await screen.findByRole("dialog", { name: "Deploy beta" });
    await waitFor(() => expect(within(dialog).getByText(/remnant-7/)).toBeTruthy());
    fireEvent.click(within(dialog).getByRole("button", { name: "Teardown retry" }));
    await waitFor(() =>
      expect(within(dialog).getByText(/Teardown completed; remnant remnant-7 is resolved\./)).toBeTruthy(),
    );
    expect(within(dialog).getByRole("button", { name: "Plan again" })).toBeTruthy();

    await act(() => vi.advanceTimersByTimeAsync(OPERATION_POLL_MS));
    expect(within(dialog).getByRole("button", { name: "Plan again" })).toBeTruthy();
  } finally {
    vi.useRealTimers();
  }
});

test("a teardown-unknown-key refusal offers a re-read path out, not a dead end", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let lists = 0;
  fake.on("evener/host/list", () => {
    lists += 1;
    return { hosts: [row({ name: "beta", openRemnantId: "remnant-7" })] };
  });
  fake.on("evener/host/teardown-retry", () => {
    throw new WireError('teardown remnant "remnant-7" is unknown or purged', -32001, {
      evenerErrorInfo: "teardown-unknown-key",
      remnantId: "remnant-7",
    });
  });
  render(<HostsSection sectionId="hosts" />);
  const betaRow = (await screen.findByText("beta")).closest("li")!;
  await user.click(within(betaRow).getByRole("button", { name: "Teardown retry" }));
  const dialog = await screen.findByRole("dialog", { name: "Repair remnant remnant-7" });
  await user.click(within(dialog).getByRole("button", { name: "Teardown retry" }));
  await waitFor(() => expect(within(dialog).getByText(/does not know teardown remnant remnant-7/)).toBeTruthy());

  const before = lists;
  await user.click(within(dialog).getByRole("button", { name: "Re-read host list" }));
  await waitFor(() => expect(lists).toBeGreaterThan(before));
  // The refusal is dropped with the re-read, so the operator can retry the id
  // the list still names instead of closing and reopening into it.
  await waitFor(() => expect(within(dialog).getByRole("button", { name: "Teardown retry" })).toBeTruthy());
});
