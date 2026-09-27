import type { HostRow } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { hostsStore } from "../../../stores/hosts";
import { resetSettingsHostForTests, setSettingsHost } from "../../../stores/settingsHost";
import { HostScopedSurface } from "./hostScopedSurface";

// The shared host-scoped frame's own contract (component 07b, round 8): its
// body belongs to ONE host. Every pane body this frame renders holds per-host
// in-memory state - a launch form's draft, an AGENTS.md draft and baseline, a
// marketplace selection and its editor draft, an MCP add-form's draft, a
// pending Remove confirmation - and none of it is the next host's. A switch
// therefore has to REMOUNT the body, not merely re-render it: a re-render
// leaves that state in place, where it can be shown against the new host's
// data and written to the new host.

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "hub.toml", attached: false, midAttach: false, removed: false, ...overrides };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

/** Body stands in for any pane's host-scoped body: it renders the host it was
 * handed and holds one draft of its own. */
function Body({ host }: { host: string }) {
  const [draft, setDraft] = useState("");
  return (
    <div>
      <p data-testid="body-host">{host}</p>
      <input aria-label="draft" value={draft} onChange={(event) => setDraft(event.target.value)} />
    </div>
  );
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetSettingsHostForTests();
  hostsStore.getState().resetForTests();
  window.history.pushState({}, "", "/settings/launch-evener");
  setSettingsHost("local");
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  window.history.pushState({}, "", "/");
});

test("switching hosts remounts the body, so no draft survives into the new host", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));

  render(<HostScopedSurface>{(host) => <Body host={host} />}</HostScopedSurface>);
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("draft"), "typed for this hub");
  expect((screen.getByLabelText("draft") as HTMLInputElement).value).toBe("typed for this hub");

  const select = screen.getByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");

  expect(screen.getByTestId("body-host").textContent).toBe("beta");
  expect((screen.getByLabelText("draft") as HTMLInputElement).value).toBe("");
});

test("a re-render for the SAME host does not remount the body", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));

  render(<HostScopedSurface>{(host) => <Body host={host} />}</HostScopedSurface>);
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("draft"), "typed");
  // The registry re-publishes on every materially different snapshot (here:
  // beta attaching), which re-renders the frame above the body. Re-rendering
  // is not switching: the body's own state is not the registry's business.
  act(() => hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", attached: true })] } }));

  expect((screen.getByLabelText("draft") as HTMLInputElement).value).toBe("typed");
});

// The registry's own row is what REGISTERS a host: a host removed and re-added
// under the same name - even between two polls, so the client never sees an
// empty list - is a DIFFERENT registration, and every per-host store instance is
// rebuilt for it (stores/hosts.ts's hostRegistrationChanged). The body has to be
// remounted with them: its pane-local state (a launch form's draft above all) is
// the previous registration's, and left in place it is shown against the new
// registration's data and submitted to it by Save. A host STRING cannot express
// that - it is unchanged across the swap - so the body is keyed on the host's
// registration identity instead.
test("a host re-registered under the same name remounts the body, so no draft survives into the new registration", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({
    hosts: [hostRow({ name: "beta", address: "old.example:22", attached: true })],
  }));

  render(<HostScopedSurface>{(host) => <Body host={host} />}</HostScopedSurface>);
  const user = userEvent.setup();
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(screen.getByLabelText("Host"), "beta");
  await user.type(screen.getByLabelText("draft"), "typed for the old registration");
  expect((screen.getByLabelText("draft") as HTMLInputElement).value).toBe("typed for the old registration");

  act(() =>
    hostsStore.setState({
      load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "new.example:22", attached: true })] },
    }),
  );

  expect(screen.getByTestId("body-host").textContent).toBe("beta");
  expect((screen.getByLabelText("draft") as HTMLInputElement).value).toBe("");
});

// The other edge of the same rule: a body whose host was resolved BEFORE the
// registry answered (a deep link mounts the pane while the picker's own list
// read is still out) is not remounted by that first answer arriving. Nothing has
// named that host's registration differently - it is the same host, identified.
test("the registry's first answer for the host does not remount the body", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  setSettingsHost("beta");

  render(<HostScopedSurface>{(host) => <Body host={host} />}</HostScopedSurface>);
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("draft"), "typed before the list landed");
  await screen.findByRole("option", { name: "beta" });

  expect(screen.getByTestId("body-host").textContent).toBe("beta");
  expect((screen.getByLabelText("draft") as HTMLInputElement).value).toBe("typed before the list landed");
});

// And the third: the live session state the hub reports on the same row - an
// attach, a version report - moves without the registration moving, so the poll
// re-rendering the frame above the body is not a switch either.
test("a live-state change on an unchanged registration does not remount the body", async () => {
  const fake = connectFakeClient();
  const registered = hostRow({ name: "beta", address: "beta.example:22", attached: true });
  fake.on("evener/host/list", () => ({ hosts: [registered] }));

  render(<HostScopedSurface>{(host) => <Body host={host} />}</HostScopedSurface>);
  const user = userEvent.setup();
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(screen.getByLabelText("Host"), "beta");
  await user.type(screen.getByLabelText("draft"), "typed");
  expect((screen.getByLabelText("draft") as HTMLInputElement).value).toBe("typed");

  act(() =>
    hostsStore.setState({
      load: {
        phase: "ready",
        hosts: [{ ...registered, serverVersion: "1.2.3", hubVersion: "9.9.9", midAttach: true }],
      },
    }),
  );

  expect((screen.getByLabelText("draft") as HTMLInputElement).value).toBe("typed");
});
