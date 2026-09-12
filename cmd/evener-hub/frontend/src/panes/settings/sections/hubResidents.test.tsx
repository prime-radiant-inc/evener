// hubResidents.test.tsx — rendered-row and interaction tests for the
// HubResidents component. Store-level logic lives in daemonResidents.test.ts.
//
// Pattern: FakeClient + connectionStore at RTL boundary; Vitest fake timers
// for polling; reset store+connection+cleanup after each test.
//
// Note: this codebase has no jest-dom matcher setup (vite.config.ts's own
// `test.setupFiles: []`), so disabled state is checked via the DOM property
// directly, matching the convention established in QueueStrip.test.tsx and
// the widget tests.

import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { DaemonIdentity, DaemonListResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetDaemonResidentsStoreForTests } from "../../../stores/daemonResidents";
import { HubResidents } from "./hubResidents";

const POLL_INTERVAL_MS = 2000;

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

// Checks the HTML disabled property — no jest-dom in this project.
// Mirrors QueueStrip.test.tsx's isDisabled helper.
function isDisabled(el: Element): boolean {
  return (el as HTMLButtonElement).disabled;
}

const IDENTITY_FIXTURE: DaemonIdentity = {
  ref: "local:resident-fixture",
  pid: 101,
  startedAt: "2026-09-10T00:00:00Z",
  generation: "fixture-generation",
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetDaemonResidentsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

// ─── Plan Step-1 verbatim fixture test ───────────────────────────────────────

test("archived incompatible residents remain visible and cannot safely retire", async () => {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: {
          ref: "local:resident-fixture",
          pid: 101,
          startedAt: "2026-09-10T00:00:00Z",
          generation: "fixture-generation",
        },
        name: "Archived fixture",
        protocol: "evener-appwire-v3",
        compatibility: "incompatible",
        archived: true,
        probeState: "unknown",
        canRetire: false,
        canForceStop: true,
      },
    ],
  }));
  render(<HubResidents />);
  const row = await screen.findByRole("row", { name: /Archived fixture/ });
  expect(isDisabled(within(row).getByRole("button", { name: "Retire now" }))).toBe(true);
  expect(isDisabled(within(row).getByRole("button", { name: "Force stop" }))).toBe(false);
});

// ─── Polling: no further calls after unmount ──────────────────────────────────

test("polling stops after unmount: no further calls after unmount", async () => {
  vi.useFakeTimers();
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 1000,
    daemons: [],
  }));

  const { unmount } = render(<HubResidents />);

  // Drain initial request microtasks (initial refresh fires on mount).
  // With fake timers, Promises still resolve via the real microtask queue.
  await act(async () => {
    // Multiple ticks to let: request handler be called → response arrive →
    // setState fire → React re-render queue drain.
    for (let i = 0; i < 5; i++) await Promise.resolve();
  });

  expect(fake.calls.filter((c) => c.method === "evener/daemon/list")).toHaveLength(1);

  // Advance interval → fires another refresh
  await act(async () => {
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
  });

  expect(fake.calls.filter((c) => c.method === "evener/daemon/list")).toHaveLength(2);

  unmount();

  // After unmount, advancing timers must not trigger any new calls
  await act(async () => {
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
  });

  expect(fake.calls.filter((c) => c.method === "evener/daemon/list")).toHaveLength(2);
});

// ─── Failed refresh: old rows retained, stale indicator shown ────────────────

test("failed refresh retains old rows and shows a stale-data indicator", async () => {
  const fake = connectFakeClient();
  const firstResponse: DaemonListResponse = {
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Kept resident",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: "current",
        lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
        canRetire: true,
        canForceStop: true,
      },
    ],
  };

  fake.on("evener/daemon/list", () => firstResponse);
  render(<HubResidents />);

  // Wait for first render with data
  await screen.findByRole("row", { name: /Kept resident/ });

  // Now fail subsequent refreshes
  fake.on("evener/daemon/list", () => {
    throw new Error("network down");
  });

  // Trigger a refresh via the store to simulate polling failure
  await act(async () => {
    await import("../../../stores/daemonResidents").then((m) => m.daemonResidentsStore.getState().refresh());
  });

  // Old rows must still be visible
  expect(screen.getByRole("row", { name: /Kept resident/ })).toBeTruthy();
  // A stale/error indicator must be shown
  expect(screen.getByRole("status")).toBeTruthy();
});

// ─── Force-stop dialog ────────────────────────────────────────────────────────

describe("force-stop dialog", () => {
  test("clicking Force stop opens an accessible confirmation dialog with ref/PID and interruption warning", async () => {
    const fake = connectFakeClient();
    fake.on("evener/daemon/list", () => ({
      defaultTimeoutMillis: 3600000,
      daemons: [
        {
          identity: IDENTITY_FIXTURE,
          name: "Target resident",
          protocol: "evener-appwire-v5",
          compatibility: "compatible",
          archived: false,
          probeState: "current",
          lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
          canRetire: true,
          canForceStop: true,
        },
      ],
    }));
    const user = userEvent.setup();
    render(<HubResidents />);

    const row = await screen.findByRole("row", { name: /Target resident/ });
    await user.click(within(row).getByRole("button", { name: "Force stop" }));

    // Dialog must be visible and accessible
    const dialog = screen.getByRole("dialog");
    expect(dialog).toBeTruthy();
    // Dialog must name the root ref or PID
    expect(dialog.textContent).toMatch(/101|local:resident-fixture/);
    // Dialog must warn about interrupted work/watches
    expect(dialog.textContent).toMatch(/work|watches|interrupt/i);
  });

  test("cancelling force-stop dialog sends no RPC", async () => {
    const fake = connectFakeClient();
    fake.on("evener/daemon/list", () => ({
      defaultTimeoutMillis: 3600000,
      daemons: [
        {
          identity: IDENTITY_FIXTURE,
          name: "Cancel target",
          protocol: "evener-appwire-v5",
          compatibility: "compatible",
          archived: false,
          probeState: "current",
          lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
          canRetire: true,
          canForceStop: true,
        },
      ],
    }));
    const user = userEvent.setup();
    render(<HubResidents />);

    const row = await screen.findByRole("row", { name: /Cancel target/ });
    await user.click(within(row).getByRole("button", { name: "Force stop" }));

    const forceStopCallsBefore = fake.calls.filter((c) => c.method === "evener/thread/forceStop").length;
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    // Dialog must be gone
    expect(screen.queryByRole("dialog")).toBeNull();
    // No forceStop RPC must have been sent
    expect(fake.calls.filter((c) => c.method === "evener/thread/forceStop")).toHaveLength(forceStopCallsBefore);
  });

  test("confirming force-stop sends evener/thread/forceStop with the exact original ExpectedDaemon", async () => {
    const fake = connectFakeClient();
    fake.on("evener/daemon/list", () => ({
      defaultTimeoutMillis: 3600000,
      daemons: [
        {
          identity: IDENTITY_FIXTURE,
          name: "Confirm target",
          protocol: "evener-appwire-v5",
          compatibility: "compatible",
          archived: false,
          probeState: "current",
          lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
          canRetire: true,
          canForceStop: true,
        },
      ],
    }));
    fake.on("evener/thread/forceStop", () => ({}));
    const user = userEvent.setup();
    render(<HubResidents />);

    const row = await screen.findByRole("row", { name: /Confirm target/ });
    await user.click(within(row).getByRole("button", { name: "Force stop" }));

    // Scope confirm click to the dialog — aria-modal is not honored by jsdom
    // so background elements remain queryable; within() scopes to the dialog.
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Force stop" }));

    // The RPC must carry the exact identity the row was rendered from
    const fsCall = fake.calls.find((c) => c.method === "evener/thread/forceStop");
    expect(fsCall).toBeTruthy();
    expect((fsCall!.params as { ref: string; expectedDaemon: DaemonIdentity }).ref).toBe(IDENTITY_FIXTURE.ref);
    expect((fsCall!.params as { ref: string; expectedDaemon: DaemonIdentity }).expectedDaemon).toEqual(
      IDENTITY_FIXTURE,
    );
  });

  test("changing the list row behind the open dialog does not silently retarget the action", async () => {
    const fake = connectFakeClient();
    const identityA: DaemonIdentity = {
      ref: "local:daemon-x",
      pid: 200,
      startedAt: "2026-09-10T01:00:00Z",
      generation: "gen-A",
    };
    const identityB: DaemonIdentity = {
      ref: "local:daemon-x",
      pid: 201,
      startedAt: "2026-09-10T02:00:00Z",
      generation: "gen-B",
    };

    fake.on("evener/daemon/list", () => ({
      defaultTimeoutMillis: 3600000,
      daemons: [
        {
          identity: identityA,
          name: "Shifting daemon",
          protocol: "evener-appwire-v5",
          compatibility: "compatible",
          archived: false,
          probeState: "current",
          lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
          canRetire: true,
          canForceStop: true,
        },
      ],
    }));
    fake.on("evener/thread/forceStop", () => ({}));
    const user = userEvent.setup();
    render(<HubResidents />);

    const row = await screen.findByRole("row", { name: /Shifting daemon/ });
    await user.click(within(row).getByRole("button", { name: "Force stop" }));

    // Dialog is now open with identityA captured. Simulate a background list
    // refresh that updates the row to identityB.
    await act(async () => {
      fake.on("evener/daemon/list", () => ({
        defaultTimeoutMillis: 3600000,
        daemons: [
          {
            identity: identityB,
            name: "Shifting daemon",
            protocol: "evener-appwire-v5",
            compatibility: "compatible",
            archived: false,
            probeState: "current",
            lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
            canRetire: true,
            canForceStop: true,
          },
        ],
      }));
      await import("../../../stores/daemonResidents").then((m) => m.daemonResidentsStore.getState().refresh());
    });

    // Confirm force stop — must use original identityA, not identityB.
    // Scope to the dialog (jsdom does not honor aria-modal visibility hiding).
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Force stop" }));

    const fsCall = fake.calls.find((c) => c.method === "evener/thread/forceStop");
    expect(fsCall).toBeTruthy();
    expect((fsCall!.params as { expectedDaemon: DaemonIdentity }).expectedDaemon).toEqual(identityA);
    expect((fsCall!.params as { expectedDaemon: DaemonIdentity }).expectedDaemon).not.toEqual(identityB);
  });

  test("Force stop button is disabled while the action is pending", async () => {
    const fake = connectFakeClient();
    let resolveForceStop!: () => void;
    fake.on("evener/daemon/list", () => ({
      defaultTimeoutMillis: 3600000,
      daemons: [
        {
          identity: IDENTITY_FIXTURE,
          name: "Pending target",
          protocol: "evener-appwire-v5",
          compatibility: "compatible",
          archived: false,
          probeState: "current",
          lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
          canRetire: true,
          canForceStop: true,
        },
      ],
    }));
    fake.on("evener/thread/forceStop", () => new Promise<void>((r) => (resolveForceStop = r)));
    const user = userEvent.setup();
    render(<HubResidents />);

    const row = await screen.findByRole("row", { name: /Pending target/ });
    await user.click(within(row).getByRole("button", { name: "Force stop" }));

    // Confirm via the dialog — scope to avoid multiple "Force stop" matches
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Force stop" }));

    // While pending, Force stop button in the row must be disabled
    await act(async () => {
      // Drain microtasks to let React apply the pending state update
      for (let i = 0; i < 5; i++) await Promise.resolve();
    });
    const forceStopBtn = within(screen.getByRole("row", { name: /Pending target/ })).getByRole("button", {
      name: "Force stop",
    });
    expect(isDisabled(forceStopBtn)).toBe(true);

    // Clean up: resolve the forceStop so the test doesn't leak async work
    resolveForceStop();
  });
});

// ─── Timeout rendering ────────────────────────────────────────────────────────

test("unknown timeout (lifecycle null) renders differently from disabled zero (timeoutMillis=0)", async () => {
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: { ref: "local:no-lc", pid: 300, startedAt: "2026-09-10T00:00:00Z", generation: "gen-nolc" },
        name: "Unknown timeout",
        protocol: "evener-appwire-v5",
        compatibility: "unknown",
        archived: false,
        probeState: "unknown",
        // no lifecycle field → timeout is unknown
        canRetire: false,
        canForceStop: false,
      },
      {
        identity: { ref: "local:zero-lc", pid: 301, startedAt: "2026-09-10T00:00:00Z", generation: "gen-zero" },
        name: "Disabled timeout",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: "current",
        lifecycle: { phase: "resident", timeoutMillis: 0, blockers: [] },
        canRetire: false,
        canForceStop: true,
      },
    ],
  }));
  render(<HubResidents />);

  const unknownRow = await screen.findByRole("row", { name: /Unknown timeout/ });
  const disabledRow = await screen.findByRole("row", { name: /Disabled timeout/ });

  // The timeout cell in the unknown row must contain "unknown".
  // getAllByText handles multiple matches (daemon name prefix also contains "unknown").
  expect(within(unknownRow).getAllByText(/unknown/i).length).toBeGreaterThan(0);

  // The timeout cell in the disabled row must contain "disabled".
  // getAllByText handles multiple matches (daemon name contains "Disabled").
  expect(within(disabledRow).getAllByText(/disabled/i).length).toBeGreaterThan(0);

  // The timeout column itself must differ: unknown row has no "disabled",
  // disabled row has no word-level "unknown" (compatibility is "compatible").
  // Check via the exact text content of the effective-timeout cell (4th <td>).
  const unknownCells = within(unknownRow).getAllByRole("cell");
  const disabledCells = within(disabledRow).getAllByRole("cell");
  // Column 3 (0-indexed) = Effective timeout
  expect(unknownCells[3]!.textContent).toBe("unknown");
  expect(disabledCells[3]!.textContent).toBe("disabled");
});

test("both the Hub default timeout and the row's effective timeout are displayed", async () => {
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000, // 1h
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Timeout display",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: "current",
        lifecycle: { phase: "resident", timeoutMillis: 120000, blockers: [] }, // 2m effective
        canRetire: true,
        canForceStop: true,
      },
    ],
  }));
  render(<HubResidents />);

  await screen.findByRole("row", { name: /Timeout display/ });

  // Hub default timeout must appear somewhere in the component
  expect(screen.getByText(/1h|3600/)).toBeTruthy();
  // Row's effective timeout must appear
  expect(screen.getByRole("row", { name: /Timeout display/ }).textContent).toMatch(/2m|120/);
});

// ─── Accepted:true shows "retiring" ──────────────────────────────────────────

test("Accepted:true retire response displays retiring state without removing the row", async () => {
  const fake = connectFakeClient();
  // Initial list: daemon is resident
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Soon retiring",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: "current",
        lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
        canRetire: true,
        canForceStop: true,
      },
    ],
  }));
  fake.on("evener/daemon/retire", () => ({
    accepted: true,
    lifecycle: { phase: "retiring", timeoutMillis: 3600000, blockers: [] },
  }));
  const user = userEvent.setup();
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Soon retiring/ });
  const retireBtn = within(row).getByRole("button", { name: "Retire now" });
  await user.click(retireBtn);

  // After clicking, the row must still be visible (not removed)
  // and must show some indication of retiring state
  await screen.findByRole("row", { name: /Soon retiring/ });
  // "retiring" text must appear in the row (phase cell shows "retiring")
  const updatedRow = screen.getByRole("row", { name: /Soon retiring/ });
  expect(updatedRow.textContent).toMatch(/retiring/i);
});

// ─── Retire refusal: blockers displayed, row kept ────────────────────────────

test("fresh retire refusal displays returned blockers without removing the row", async () => {
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Blocked resident",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: "current",
        lifecycle: {
          phase: "resident",
          timeoutMillis: 3600000,
          blockers: [],
        },
        canRetire: true,
        canForceStop: true,
      },
    ],
  }));
  fake.on("evener/daemon/retire", () => ({
    accepted: false,
    lifecycle: {
      phase: "resident",
      timeoutMillis: 3600000,
      blockers: [{ category: "turn", sessionId: "session-abc" }],
    },
  }));
  const user = userEvent.setup();
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Blocked resident/ });
  await user.click(within(row).getByRole("button", { name: "Retire now" }));

  // Row must still be visible after the refusal
  await screen.findByRole("row", { name: /Blocked resident/ });
  // Blocker category must be shown
  expect(screen.getByText(/turn/)).toBeTruthy();
});
