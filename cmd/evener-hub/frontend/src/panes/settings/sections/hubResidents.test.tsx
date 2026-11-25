// hubResidents.test.tsx — rendered-row, detail-sheet, and safety-invariant
// tests for Settings -> Hub -> Resident daemons.

import type { DaemonIdentity, DaemonListResponse, DaemonResident, DaemonRetireResponse } from "@evener/appwire-client";
import { friendlyErrorMessage, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "../../../stores/connection";
import { daemonResidentsStore, resetDaemonResidentsStoreForTests } from "../../../stores/daemonResidents";
import { hubUpdateStore, resetHubUpdateStoreForTests } from "../../../stores/hubUpdate";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { Toast } from "../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { HubResidents } from "./hubResidents";

const POLL_INTERVAL_MS = 2000;

function isDisabled(el: Element): boolean {
  return (el as HTMLButtonElement).disabled;
}

const IDENTITY_FIXTURE: DaemonIdentity = {
  ref: "local:resident-fixture",
  pid: 101,
  startedAt: "2026-09-10T00:00:00Z",
  generation: "fixture-generation",
};

function makeDaemon(overrides: Partial<DaemonResident> = {}): DaemonResident {
  return {
    identity: IDENTITY_FIXTURE,
    name: "Sample daemon",
    protocol: "evener-appwire-v5",
    compatibility: "compatible",
    archived: false,
    probeState: "current",
    lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
    canRetire: true,
    canForceStop: true,
    ...overrides,
  };
}

function renderResidents() {
  return render(
    <>
      <HubResidents />
      <Toast />
    </>,
  );
}

function rowItem(nameOrRef: RegExp | string): HTMLElement {
  const button = screen.getByRole("button", { name: nameOrRef });
  const li = button.closest("li");
  if (!li) throw new Error(`Expected listitem ancestor for ${String(nameOrRef)}`);
  return li;
}

beforeEach(() => {
  resetConnectionStoreForTests();
  resetDaemonResidentsStoreForTests();
  resetHubUpdateStoreForTests({ fetchImpl: vi.fn() as unknown as typeof fetch, reload: vi.fn() });
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

test("renders compact header with count badge and default timeout without verbose intro paragraphs", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [makeDaemon()],
  }));

  renderResidents();

  const region = await screen.findByRole("region", { name: "Resident daemons" });
  expect(screen.getByTestId("hub-default-timeout").textContent).toContain("1h");
  expect(region.querySelector("table")).toBeNull();
  expect(region.querySelectorAll("p")).toHaveLength(0);
});

test("archived incompatible residents remain visible, offer Force stop, and cannot safely retire", async () => {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      makeDaemon({
        name: "Archived fixture",
        protocol: "evener-appwire-v3",
        compatibility: "incompatible",
        archived: true,
        probeState: "unknown",
        lifecycle: undefined,
        canRetire: false,
        canForceStop: true,
      }),
    ],
  }));

  renderResidents();
  await screen.findByRole("button", { name: /Archived fixture/ });
  const item = rowItem(/Archived fixture/);
  expect(within(item).queryByRole("button", { name: "Retire" })).toBeNull();
  expect(isDisabled(within(item).getByRole("button", { name: "Force stop" }))).toBe(false);

  // Opening the detail sheet confirms Retire now is disabled and Force stop is enabled.
  await userEvent.click(screen.getByRole("button", { name: /Archived fixture/ }));
  const sheet = await screen.findByRole("dialog");
  expect(isDisabled(within(sheet).getByRole("button", { name: "Retire now" }))).toBe(true);
  expect(isDisabled(within(sheet).getByRole("button", { name: "Force stop…" }))).toBe(false);
});

test("opening a daemon row shows full ref, protocol, timeout, and exact blocker ids in its detail sheet", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      makeDaemon({
        name: "Blocked daemon",
        lifecycle: {
          phase: "resident",
          timeoutMillis: 3600000,
          blockers: [
            { category: "turn", sessionId: "session-xyz" },
            { category: "delegate", sessionId: "root-session-1", delegateId: "dlg-42" },
            { category: "delegate", sessionId: "", delegateId: "dlg-only-7" },
          ],
        },
        canRetire: false,
      }),
    ],
  }));

  renderResidents();
  const trigger = await screen.findByRole("button", { name: /Blocked daemon/ });
  await userEvent.click(trigger);

  const sheet = await screen.findByRole("dialog");
  expect(sheet.textContent).toContain(IDENTITY_FIXTURE.ref);
  expect(sheet.textContent).toContain("evener-appwire-v5");
  const blockers = within(sheet).getByTestId("resident-blockers").textContent ?? "";
  expect(blockers).toContain("turn (session-xyz)");
  expect(blockers).toContain("delegate (root-session-1/dlg-42)");
  expect(blockers).toContain("delegate (dlg-only-7)");
});

test("residents sharing a ref but differing in generation render distinctly and isolate retire state", async () => {
  const genOld: DaemonIdentity = {
    ref: "local:shared-ref",
    pid: 201,
    startedAt: "2026-09-10T00:00:00Z",
    generation: "gen-old",
  };
  const genNew: DaemonIdentity = {
    ref: "local:shared-ref",
    pid: 202,
    startedAt: "2026-09-10T00:01:00Z",
    generation: "gen-new",
  };

  let resolveRetire!: (resp: DaemonRetireResponse) => void;
  const fake = connectReadyFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      makeDaemon({ identity: genOld, name: "Shared Name" }),
      makeDaemon({ identity: genNew, name: "Shared Name" }),
    ],
  }));
  fake.on(
    "evener/daemon/retire",
    () =>
      new Promise<DaemonRetireResponse>((resolve) => {
        resolveRetire = resolve;
      }),
  );

  renderResidents();
  const rows = await screen.findAllByRole("button", { name: /Shared Name/ });
  expect(rows).toHaveLength(2);
  const firstLi = rows[0]!.closest("li")!;
  const secondLi = rows[1]!.closest("li")!;

  // Duplicate names disambiguate with ref in the meta line.
  expect(firstLi.textContent).toContain("PID 201");
  expect(secondLi.textContent).toContain("PID 202");

  // Starting retire on generation 1 disables only generation 1's Retire button.
  await userEvent.click(within(firstLi).getByRole("button", { name: "Retire" }));
  await waitFor(() => expect(isDisabled(within(firstLi).getByRole("button", { name: "Retire" }))).toBe(true));
  expect(isDisabled(within(secondLi).getByRole("button", { name: "Retire" }))).toBe(false);

  // Refusing generation 1 keeps the refusal scoped to generation 1 only.
  act(() => {
    resolveRetire({
      accepted: false,
      lifecycle: {
        phase: "resident",
        timeoutMillis: 3600000,
        blockers: [{ category: "turn", sessionId: "turn-only-on-old" }],
      },
    });
  });
  await waitFor(() => expect(getToasts().some((t) => t.kind === "warning")).toBe(true));

  await userEvent.click(rows[0]!);
  const sheet1 = await screen.findByRole("dialog");
  expect(within(sheet1).getByTestId("resident-blockers").textContent).toContain("turn-only-on-old");
  await userEvent.click(within(sheet1).getByRole("button", { name: "Close" }));

  await userEvent.click(rows[1]!);
  const sheet2 = await screen.findByRole("dialog");
  expect(within(sheet2).queryByTestId("resident-blockers")).toBeNull();
});

test("accepted retire shows retiring state and survives a cached resident or stale probe until phase changes or departure", async () => {
  const fake = connectReadyFakeClient();
  let daemons: DaemonResident[] = [makeDaemon({ name: "Retiring target" })];
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons }));
  fake.on("evener/daemon/retire", () => ({
    accepted: true,
    lifecycle: { phase: "retiring", timeoutMillis: 3600000, blockers: [] },
  }));

  renderResidents();
  await screen.findByRole("button", { name: /Retiring target/ });
  const item = rowItem(/Retiring target/);

  await userEvent.click(within(item).getByRole("button", { name: "Retire" }));
  await waitFor(() => expect(within(item).queryByRole("button", { name: "Retire" })).toBeNull());

  // Stale probe with no lifecycle does not revert accepted retiring state.
  daemons = [makeDaemon({ name: "Retiring target", probeState: "stale", lifecycle: undefined, canRetire: false })];
  await act(async () => {
    await daemonResidentsStore.getState().refresh();
  });
  expect(within(item).queryByRole("button", { name: "Retire" })).toBeNull();

  // Cached resident probe also does not revert accepted retiring state.
  daemons = [makeDaemon({ name: "Retiring target", probeState: "current", canRetire: true })];
  await act(async () => {
    await daemonResidentsStore.getState().refresh();
  });
  expect(within(item).queryByRole("button", { name: "Retire" })).toBeNull();

  // Departure clears the row and closes any open detail sheet.
  daemons = [];
  await act(async () => {
    await daemonResidentsStore.getState().refresh();
  });
  expect(screen.queryByRole("button", { name: /Retiring target/ })).toBeNull();
});

test("retire refusal persists across a fresh snapshot with empty blockers until phase changes or departure", async () => {
  const fake = connectReadyFakeClient();
  let daemons: DaemonResident[] = [makeDaemon({ name: "Refused daemon" })];
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons }));
  fake.on("evener/daemon/retire", () => ({
    accepted: false,
    lifecycle: {
      phase: "resident",
      timeoutMillis: 3600000,
      blockers: [{ category: "watch", sessionId: "watch-99" }],
    },
  }));

  renderResidents();
  const trigger = await screen.findByRole("button", { name: /Refused daemon/ });
  const item = rowItem(/Refused daemon/);
  await userEvent.click(within(item).getByRole("button", { name: "Retire" }));
  await waitFor(() => expect(getToasts().some((t) => t.kind === "warning")).toBe(true));

  // Fresh current snapshot with empty blockers must not discard the refusal.
  await act(async () => {
    await daemonResidentsStore.getState().refresh();
  });
  await userEvent.click(trigger);
  const sheet = await screen.findByRole("dialog");
  expect(within(sheet).getByTestId("resident-blockers").textContent).toContain("watch (watch-99)");

  // Phase change clears the refusal.
  daemons = [
    makeDaemon({
      name: "Refused daemon",
      lifecycle: { phase: "preparing", timeoutMillis: 3600000, blockers: [] },
      canRetire: false,
    }),
  ];
  await act(async () => {
    await daemonResidentsStore.getState().refresh();
  });
  await waitFor(() => expect(within(sheet).queryByTestId("resident-blockers")).toBeNull());
});

test("pauses polling and suppresses error toasts while the hub is restarting during an update", async () => {
  vi.useFakeTimers();
  const fake = connectReadyFakeClient();
  let listCalls = 0;
  fake.on("evener/daemon/list", () => {
    listCalls += 1;
    if (listCalls > 1) throw new Error("Not connected to hub");
    return { defaultTimeoutMillis: 3600000, daemons: [makeDaemon({ name: "Surviving daemon" })] };
  });

  renderResidents();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByRole("button", { name: /Surviving daemon/ })).toBeTruthy();
  expect(listCalls).toBe(1);

  // Enter hub restarting state: polling must pause and zero error toasts or alerts appear.
  act(() => hubUpdateStore.setState({ restarting: true }));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * 3);
  });
  expect(listCalls).toBe(1);
  expect(getToasts()).toEqual([]);
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.getByRole("button", { name: /Surviving daemon/ })).toBeTruthy();
});

test("failed refresh outside hub restart keeps last known rows and pushes a single deduplicated error toast", async () => {
  vi.useFakeTimers();
  const fake = connectReadyFakeClient();
  let calls = 0;
  fake.on("evener/daemon/list", () => {
    calls += 1;
    if (calls === 1) return { defaultTimeoutMillis: 3600000, daemons: [makeDaemon({ name: "Retained daemon" })] };
    throw new WireError("roster read failed", -32000);
  });

  renderResidents();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByRole("button", { name: /Retained daemon/ })).toBeTruthy();

  await act(async () => {
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
  });
  expect(screen.getByRole("button", { name: /Retained daemon/ })).toBeTruthy();
  expect(getToasts().filter((t) => t.kind === "error")).toHaveLength(1);
  expect(getToasts()[0]?.text).toContain("roster read failed");

  // Repeated poll with the exact same failure does not spam another toast.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
  });
  expect(getToasts().filter((t) => t.kind === "error")).toHaveLength(1);
});

test("retire RPC failure pushes an error toast instead of an inline row alert", async () => {
  const fake = connectReadyFakeClient();
  const err = new WireError("daemon ownership changed", -32000);
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [makeDaemon({ name: "Failing retire" })],
  }));
  fake.on("evener/daemon/retire", () => {
    throw err;
  });

  renderResidents();
  await screen.findByRole("button", { name: /Failing retire/ });
  const item = rowItem(/Failing retire/);
  await userEvent.click(within(item).getByRole("button", { name: "Retire" }));

  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text.includes(friendlyErrorMessage(err)))).toBe(true),
  );
  expect(screen.queryByRole("alert")).toBeNull();
});

test("polling stops after unmount", async () => {
  vi.useFakeTimers();
  const fake = connectReadyFakeClient();
  let calls = 0;
  fake.on("evener/daemon/list", () => {
    calls += 1;
    return { defaultTimeoutMillis: 3600000, daemons: [] };
  });

  const { unmount } = renderResidents();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(calls).toBe(1);

  unmount();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * 3);
  });
  expect(calls).toBe(1);
});

describe("force-stop dialog", () => {
  test("captures the exact rendered DaemonIdentity even if a background refresh replaces the row", async () => {
    const oldIdentity: DaemonIdentity = {
      ref: "local:capture-target",
      pid: 501,
      startedAt: "2026-09-10T00:00:00Z",
      generation: "gen-501",
    };
    const newIdentity: DaemonIdentity = {
      ref: "local:capture-target",
      pid: 999,
      startedAt: "2026-09-10T01:00:00Z",
      generation: "gen-999",
    };

    let current = oldIdentity;
    const fake = connectReadyFakeClient();
    fake.on("evener/daemon/list", () => ({
      defaultTimeoutMillis: 3600000,
      daemons: [
        makeDaemon({
          identity: current,
          name: "Captured target",
          compatibility: "incompatible",
          canRetire: false,
        }),
      ],
    }));
    fake.on("evener/thread/forceStop", () => ({}));

    renderResidents();
    await screen.findByRole("button", { name: /Captured target/ });
    const item = rowItem(/Captured target/);
    await userEvent.click(within(item).getByRole("button", { name: "Force stop" }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain("501");

    // Background refresh swaps in PID 999 while the confirmation dialog is open.
    current = newIdentity;
    await act(async () => {
      await daemonResidentsStore.getState().refresh();
    });

    await userEvent.click(within(dialog).getByRole("button", { name: "Force stop" }));
    await waitFor(() =>
      expect(fake.calls).toContainEqual({
        method: "evener/thread/forceStop",
        params: { ref: oldIdentity.ref, expectedDaemon: oldIdentity },
      }),
    );
  });
});

test("distinguishes unknown, disabled, and sub-second timeouts in the detail sheet", async () => {
  const fake = connectReadyFakeClient();
  const list: DaemonListResponse = {
    defaultTimeoutMillis: 3600000,
    daemons: [
      makeDaemon({
        identity: { ...IDENTITY_FIXTURE, pid: 1, generation: "g-unknown" },
        name: "Unknown timeout",
        lifecycle: undefined,
        canRetire: false,
      }),
      makeDaemon({
        identity: { ...IDENTITY_FIXTURE, pid: 2, generation: "g-disabled" },
        name: "Disabled timeout",
        lifecycle: { phase: "resident", timeoutMillis: 0, blockers: [] },
      }),
      makeDaemon({
        identity: { ...IDENTITY_FIXTURE, pid: 3, generation: "g-subsec" },
        name: "Sub-second timeout",
        lifecycle: { phase: "resident", timeoutMillis: 500, blockers: [] },
      }),
    ],
  };
  fake.on("evener/daemon/list", () => list);

  renderResidents();

  await userEvent.click(await screen.findByRole("button", { name: /Unknown timeout/ }));
  expect((await screen.findByRole("dialog")).textContent).toContain("unknown");
  await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Close" }));

  await userEvent.click(screen.getByRole("button", { name: /Disabled timeout/ }));
  expect((await screen.findByRole("dialog")).textContent).toContain("disabled");
  await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Close" }));

  await userEvent.click(screen.getByRole("button", { name: /Sub-second timeout/ }));
  expect((await screen.findByRole("dialog")).textContent).toContain("<1s");
});

test("empty state survives background polls instead of blanking", async () => {
  vi.useFakeTimers();
  const fake = connectReadyFakeClient();
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  renderResidents();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(screen.getByTestId("empty-state")).toBeTruthy();

  await act(async () => {
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
  });
  expect(screen.getByTestId("empty-state")).toBeTruthy();
});
