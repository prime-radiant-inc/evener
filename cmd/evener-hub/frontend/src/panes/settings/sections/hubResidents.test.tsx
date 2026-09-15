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

import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type {
  DaemonIdentity,
  DaemonListResponse,
  DaemonResident,
  DaemonRetireResponse,
} from "../../../protocol/types.gen";
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

// ─── I-1: Row shows identity.ref ─────────────────────────────────────────────

test("row displays identity.ref under the daemon name", async () => {
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Ref display daemon",
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
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Ref display daemon/ });
  // identity.ref must be visible in the row (in addition to the name)
  expect(within(row).getByText(IDENTITY_FIXTURE.ref)).toBeTruthy();
});

// ─── I-2: List-snapshot blockers visible before any retire attempt ────────────

test("list-snapshot lifecycle blockers are shown in the row before any retire attempt", async () => {
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Pre-blocked daemon",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: "current",
        lifecycle: {
          phase: "resident",
          timeoutMillis: 3600000,
          blockers: [{ category: "turn", sessionId: "session-xyz" }],
        },
        canRetire: false,
        canForceStop: true,
      },
    ],
  }));
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Pre-blocked daemon/ });
  // Blocker from the list snapshot must be visible without attempting retire
  expect(within(row).getByText(/session-xyz/)).toBeTruthy();
});

test("delegate blocker with both ids renders the root session id and the delegate id", async () => {
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Delegate-blocked daemon",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: "current",
        lifecycle: {
          phase: "resident",
          timeoutMillis: 3600000,
          blockers: [{ category: "delegate", sessionId: "session-root", delegateId: "dlg-xyz" }],
        },
        canRetire: false,
        canForceStop: true,
      },
    ],
  }));
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Delegate-blocked daemon/ });
  // Both the root session id and the delegate id must be visible: dropping
  // either leaves the operator unable to tell which delegate blocks retirement.
  expect(within(row).getByText(/delegate \(session-root\/dlg-xyz\)/)).toBeTruthy();
});

test("delegate blocker with an empty session id falls through to the delegate id", async () => {
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Delegate-only blocked daemon",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: "current",
        lifecycle: {
          phase: "resident",
          timeoutMillis: 3600000,
          // An empty-string sessionId (the TS type admits it) must fall through
          // to the delegate id; the Go producer normally omits the field via
          // `omitempty`, but ?? would still drop the delegate id here.
          blockers: [{ category: "delegate", sessionId: "", delegateId: "dlg-only" }],
        },
        canRetire: false,
        canForceStop: true,
      },
    ],
  }));
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Delegate-only blocked daemon/ });
  // The empty session id must not swallow the delegate id: the operator has to
  // be able to tell which delegate is blocking retirement.
  expect(within(row).getByText(/delegate \(dlg-only\)/)).toBeTruthy();
});

test("residents sharing a ref but differing in generation render without duplicate React keys", async () => {
  const fake = connectFakeClient();
  const sharedRef = "local:replaced-resident";
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: { ref: sharedRef, pid: 401, startedAt: "2026-09-10T00:00:00Z", generation: "gen-old" },
        name: "Replacee old",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: "current",
        lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
        canRetire: true,
        canForceStop: true,
      },
      {
        identity: { ref: sharedRef, pid: 402, startedAt: "2026-09-10T00:01:00Z", generation: "gen-new" },
        name: "Replacee new",
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

  // listDaemons dedups by generation (fingerprint), not ref, so a replacement
  // in flight legitimately yields two rows with one ref. React keys must still
  // be unique; the duplicate-key warning is the observable symptom.
  const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
  try {
    render(<HubResidents />);
    await screen.findByRole("row", { name: /Replacee old/ });
    await screen.findByRole("row", { name: /Replacee new/ });
    const duplicates = errorSpy.mock.calls.filter((call) =>
      call.some((arg) => typeof arg === "string" && arg.includes("same key")),
    );
    expect(duplicates).toEqual([]);
  } finally {
    errorSpy.mockRestore();
  }
});

// ─── Same-ref, different-generation rows ─────────────────────────────────────
//
// listDaemons dedups by identity.generation, not identity.ref, so during a
// replacement or a retire-vs-resume overlap two live daemons legitimately
// share one ref. Every piece of per-row state must therefore follow the
// generation that owns it, not the shared ref.

const SHARED_REF = "local:shared-ref-resident";

// sharedRefDaemon builds one row of such a pair: both rows carry SHARED_REF,
// with a distinct generation/pid/name each so the row under test is addressable.
function sharedRefDaemon(opts: {
  generation: string;
  pid: number;
  name: string;
  phase?: string;
  stale?: boolean;
  canRetire?: boolean;
}): DaemonResident {
  return {
    identity: {
      ref: SHARED_REF,
      pid: opts.pid,
      startedAt: "2026-09-10T00:00:00Z",
      generation: opts.generation,
    },
    name: opts.name,
    protocol: "evener-appwire-v5",
    compatibility: "compatible",
    archived: false,
    probeState: opts.stale ? "stale" : "current",
    // A stale probe carries no lifecycle — the server only sets it while fresh.
    ...(opts.stale ? {} : { lifecycle: { phase: opts.phase ?? "resident", timeoutMillis: 3600000, blockers: [] } }),
    canRetire: !opts.stale && (opts.canRetire ?? true),
    canForceStop: true,
  };
}

test("a retire refusal for one generation does not appear on the sibling generation sharing its ref", async () => {
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      sharedRefDaemon({ generation: "gen-old", pid: 401, name: "Overlap old" }),
      sharedRefDaemon({ generation: "gen-new", pid: 402, name: "Overlap new" }),
    ],
  }));
  fake.on("evener/daemon/retire", () => ({
    accepted: false,
    lifecycle: {
      phase: "resident",
      timeoutMillis: 3600000,
      blockers: [{ category: "turn", sessionId: "session-old" }],
    },
  }));
  const user = userEvent.setup();
  render(<HubResidents />);

  const oldRow = await screen.findByRole("row", { name: /Overlap old/ });
  await user.click(within(oldRow).getByRole("button", { name: "Retire now" }));

  // The refusal is stored against the generation that acted.
  await waitFor(() => {
    expect(within(screen.getByRole("row", { name: /Overlap old/ })).getByText(/session-old/)).toBeTruthy();
  });
  // The sibling generation must not inherit the refusal's blockers.
  expect(within(screen.getByRole("row", { name: /Overlap new/ })).queryByText(/session-old/)).toBeNull();
});

test("an in-flight retire on one generation does not disable the sibling generation sharing its ref", async () => {
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      sharedRefDaemon({ generation: "gen-old", pid: 401, name: "In-flight old" }),
      sharedRefDaemon({ generation: "gen-new", pid: 402, name: "In-flight new" }),
    ],
  }));
  let releaseRetire!: () => void;
  fake.on(
    "evener/daemon/retire",
    () =>
      new Promise<DaemonRetireResponse>((resolve) => {
        releaseRetire = () =>
          resolve({ accepted: true, lifecycle: { phase: "retiring", timeoutMillis: 3600000, blockers: [] } });
      }),
  );
  const user = userEvent.setup();
  render(<HubResidents />);

  const oldRow = await screen.findByRole("row", { name: /In-flight old/ });
  await user.click(within(oldRow).getByRole("button", { name: "Retire now" }));
  await act(async () => {
    for (let i = 0; i < 5; i++) await Promise.resolve();
  });

  // The acting generation's own buttons are disabled while pending...
  const pendingRow = screen.getByRole("row", { name: /In-flight old/ });
  expect(isDisabled(within(pendingRow).getByRole("button", { name: "Retire now" }))).toBe(true);
  // ...and the sibling generation, which shares the ref, stays actionable.
  const siblingRow = screen.getByRole("row", { name: /In-flight new/ });
  expect(isDisabled(within(siblingRow).getByRole("button", { name: "Retire now" }))).toBe(false);
  expect(isDisabled(within(siblingRow).getByRole("button", { name: "Force stop" }))).toBe(false);

  // Clean up: resolve the held retire so the test does not leak async work.
  await act(async () => {
    releaseRetire();
    for (let i = 0; i < 5; i++) await Promise.resolve();
  });
});

test("a failed retire on one generation does not show its error on the sibling generation sharing its ref", async () => {
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      sharedRefDaemon({ generation: "gen-old", pid: 401, name: "Errored old" }),
      sharedRefDaemon({ generation: "gen-new", pid: 402, name: "Errored new" }),
    ],
  }));
  fake.on("evener/daemon/retire", () => {
    throw new Error("network error");
  });
  const user = userEvent.setup();
  render(<HubResidents />);

  const oldRow = await screen.findByRole("row", { name: /Errored old/ });
  await user.click(within(oldRow).getByRole("button", { name: "Retire now" }));

  await waitFor(() => {
    expect(screen.getByRole("row", { name: /Errored old/ }).querySelector("[role='alert']")).toBeTruthy();
  });
  // The sibling generation must not inherit the failed action's error.
  expect(screen.getByRole("row", { name: /Errored new/ }).querySelector("[role='alert']")).toBeNull();
});

test("same-ref rows of different generations each display their own lifecycle phase", async () => {
  const fake = connectFakeClient();
  // The old generation exits as its retire is accepted, so its own probe goes
  // stale (no lifecycle) while the replacement at the shared ref stays current
  // and resident. The accepted retire's phase must show only on its own row.
  let oldStale = false;
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    // The retired generation is listed last: a ref-keyed prune resolves the
    // shared ref to it, and its stale probe cannot supersede the stored result,
    // so the leak onto the sibling is observable rather than pruned away.
    daemons: [
      sharedRefDaemon({ generation: "gen-new", pid: 402, name: "Phase new" }),
      sharedRefDaemon({ generation: "gen-old", pid: 401, name: "Phase old", stale: oldStale }),
    ],
  }));
  fake.on("evener/daemon/retire", () => {
    oldStale = true;
    return { accepted: true, lifecycle: { phase: "retiring", timeoutMillis: 3600000, blockers: [] } };
  });
  const user = userEvent.setup();
  render(<HubResidents />);

  const oldRow = await screen.findByRole("row", { name: /Phase old/ });
  await user.click(within(oldRow).getByRole("button", { name: "Retire now" }));

  // The accepted retire shows on the generation that was retired...
  await waitFor(() => {
    expect(within(screen.getByRole("row", { name: /Phase old/ })).getAllByRole("cell")[4]!.textContent).toBe(
      "retiring",
    );
  });
  // ...and the same-ref replacement, whose own probe still reports resident,
  // must not inherit the retired generation's phase.
  expect(within(screen.getByRole("row", { name: /Phase new/ })).getAllByRole("cell")[4]!.textContent).toBe("resident");
});

test("a refusal clears when its own generation's phase changes despite a later same-ref row at the old phase", async () => {
  const fake = connectFakeClient();
  const newResident = sharedRefDaemon({ generation: "gen-new", pid: 402, name: "Prune new" });
  // The replacement is listed last: a ref-keyed map would resolve the shared ref
  // to this row and read the old generation's phase change as no change at all.
  let daemons: DaemonResident[] = [
    sharedRefDaemon({ generation: "gen-old", pid: 401, name: "Prune old" }),
    newResident,
  ];
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons }));
  fake.on("evener/daemon/retire", () => ({
    accepted: false,
    lifecycle: {
      phase: "resident",
      timeoutMillis: 3600000,
      blockers: [{ category: "turn", sessionId: "session-old" }],
    },
  }));
  const user = userEvent.setup();
  render(<HubResidents />);

  const oldRow = await screen.findByRole("row", { name: /Prune old/ });
  await user.click(within(oldRow).getByRole("button", { name: "Retire now" }));
  await waitFor(() => {
    expect(within(screen.getByRole("row", { name: /Prune old/ })).getByText(/session-old/)).toBeTruthy();
  });

  // The retired generation's own probe moves to "retiring" (an external or
  // concurrent retire) while the replacement at the shared ref stays resident.
  daemons = [
    sharedRefDaemon({ generation: "gen-old", pid: 401, name: "Prune old", phase: "retiring", canRetire: false }),
    newResident,
  ];
  await act(async () => {
    await import("../../../stores/daemonResidents").then((m) => m.daemonResidentsStore.getState().refresh());
  });

  // The phase change supersedes the refusal, so no row may still render it.
  expect(screen.queryAllByText(/session-old/)).toHaveLength(0);
});

test("a generation rotation at one ref does not carry the old generation's refusal to its replacement", async () => {
  const fake = connectFakeClient();
  let daemons: DaemonResident[] = [sharedRefDaemon({ generation: "gen-old", pid: 401, name: "Rotating daemon" })];
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons }));
  fake.on("evener/daemon/retire", () => ({
    accepted: false,
    lifecycle: {
      phase: "resident",
      timeoutMillis: 3600000,
      blockers: [{ category: "turn", sessionId: "session-old" }],
    },
  }));
  const user = userEvent.setup();
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Rotating daemon/ });
  await user.click(within(row).getByRole("button", { name: "Retire now" }));
  await screen.findByText(/session-old/);

  // The daemon is replaced: same ref, new generation. The refusal belonged to
  // the generation that left, so the replacement must not render it.
  daemons = [sharedRefDaemon({ generation: "gen-new", pid: 402, name: "Rotating daemon" })];
  await act(async () => {
    await import("../../../stores/daemonResidents").then((m) => m.daemonResidentsStore.getState().refresh());
  });

  const replacement = await screen.findByRole("row", { name: /Rotating daemon/ });
  expect(within(replacement).queryByText(/session-old/)).toBeNull();
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
    // Both PID and root ref must appear in the dialog — not just one of the two
    expect(dialog.textContent).toContain("101");
    expect(dialog.textContent).toContain("local:resident-fixture");
    // Interruption warning must specifically mention being interrupted (not a
    // loose OR that matches any single word)
    expect(dialog.textContent).toContain("interrupted");
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

  // ─── I-3: Force-stop RPC failure shows error in row ──────────────────────

  test("force-stop RPC failure displays a friendly error in the row", async () => {
    const fake = connectFakeClient();
    fake.on("evener/daemon/list", () => ({
      defaultTimeoutMillis: 3600000,
      daemons: [
        {
          identity: IDENTITY_FIXTURE,
          name: "ForceStop error daemon",
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
    fake.on("evener/thread/forceStop", () => {
      throw new Error("network error");
    });
    const user = userEvent.setup();
    render(<HubResidents />);

    const row = await screen.findByRole("row", { name: /ForceStop error daemon/ });
    await user.click(within(row).getByRole("button", { name: "Force stop" }));

    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Force stop" }));

    // A friendly error must appear in the row (role="alert" for announcements)
    const updatedRow = await screen.findByRole("row", { name: /ForceStop error daemon/ });
    expect(updatedRow.querySelector("[role='alert']")).toBeTruthy();
    expect(updatedRow.textContent).toContain("Something went wrong");
  });
});

// ─── I-3: Retire RPC failure shows error in row ───────────────────────────────

test("retire RPC failure displays a friendly error in the row", async () => {
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Retire error daemon",
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
  fake.on("evener/daemon/retire", () => {
    throw new Error("network error");
  });
  const user = userEvent.setup();
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Retire error daemon/ });
  await user.click(within(row).getByRole("button", { name: "Retire now" }));

  // A friendly error must appear in the row after the RPC fails
  const updatedRow = await screen.findByRole("row", { name: /Retire error daemon/ });
  expect(updatedRow.querySelector("[role='alert']")).toBeTruthy();
  expect(updatedRow.textContent).toContain("Something went wrong");
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

  // Hub default timeout must appear formatted as "1h" — not as raw millis.
  // The meta paragraph also carries the future-launch scope note.
  const metaText = screen.getByText(/Hub default idle timeout/).textContent ?? "";
  expect(metaText).toContain("1h");
  expect(metaText).not.toContain("3600000");

  // M-5: Future-launch scope sentence must accompany the default timeout.
  expect(metaText).toContain("future spawns");

  // Row's effective timeout must appear formatted as "2m" — not as raw millis.
  // Follow the exact-cell pattern from the special-string assertions above.
  const cells = within(screen.getByRole("row", { name: /Timeout display/ })).getAllByRole("cell");
  expect(cells[3]!.textContent).toBe("2m");
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

// ─── N-1: accepted retire survives a stale probe ─────────────────────────────

test("accepted retire phase survives a later stale probe with no lifecycle", async () => {
  const fake = connectFakeClient();
  // Mutable server view: the daemon moves to "retiring" once the retire RPC is
  // accepted, then its probe goes stale (lifecycle omitted) as it exits.
  let phase: "resident" | "retiring" = "resident";
  let probeStale = false;
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Retiring through stale",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: probeStale ? "stale" : "current",
        ...(probeStale ? {} : { lifecycle: { phase, timeoutMillis: 3600000, blockers: [] } }),
        canRetire: !probeStale && phase === "resident",
        canForceStop: true,
      },
    ],
  }));
  fake.on("evener/daemon/retire", () => {
    phase = "retiring";
    return {
      accepted: true,
      lifecycle: { phase: "retiring", timeoutMillis: 3600000, blockers: [] },
    };
  });
  const user = userEvent.setup();
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Retiring through stale/ });
  await user.click(within(row).getByRole("button", { name: "Retire now" }));

  // The Hub accepted the retire; the phase cell shows the confirmed "retiring".
  await waitFor(() => {
    const acceptingRow = screen.getByRole("row", { name: /Retiring through stale/ });
    expect(within(acceptingRow).getAllByRole("cell")[4]!.textContent).toBe("retiring");
  });

  // The daemon then exits far enough that its probe fails: the server emits no
  // lifecycle and a stale probe state. That is not evidence the accepted retire
  // was undone, so the row must not regress to the em-dash phase.
  probeStale = true;
  await act(async () => {
    await import("../../../stores/daemonResidents").then((m) => m.daemonResidentsStore.getState().refresh());
  });

  const staleRow = screen.getByRole("row", { name: /Retiring through stale/ });
  const phaseCell = within(staleRow).getAllByRole("cell")[4]!.textContent;
  expect(phaseCell).toBe("retiring");
  expect(phaseCell).not.toBe("—");
});

// ─── Round-10 M3: a stale CURRENT snapshot does not supersede an accepted retire

test("an accepted retire survives a post-retire refresh that still reports resident", async () => {
  const fake = connectFakeClient();
  // The Hub's cached roster keeps reporting the pre-retire view after the retire
  // is accepted: a current probe, phase "resident", canRetire true. That is not
  // evidence the retire was undone, so the accepted result must not be pruned.
  const resident = {
    identity: IDENTITY_FIXTURE,
    name: "Cached-rostered resident",
    protocol: "evener-appwire-v5",
    compatibility: "compatible",
    archived: false,
    probeState: "current",
    lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
    canRetire: true,
    canForceStop: true,
  };
  // The first list renders the row; the post-retire refresh is held so the
  // accepted result can be observed before the cached snapshot arrives.
  let listCalls = 0;
  let releaseRefresh!: () => void;
  fake.on("evener/daemon/list", () => {
    listCalls += 1;
    if (listCalls === 1) {
      return { defaultTimeoutMillis: 3600000, daemons: [resident] };
    }
    return new Promise<DaemonListResponse>((resolve) => {
      releaseRefresh = () => resolve({ defaultTimeoutMillis: 3600000, daemons: [resident] });
    });
  });
  fake.on("evener/daemon/retire", () => ({
    accepted: true,
    lifecycle: { phase: "retiring", timeoutMillis: 3600000, blockers: [] },
  }));
  const user = userEvent.setup();
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Cached-rostered resident/ });
  await user.click(within(row).getByRole("button", { name: "Retire now" }));

  // The Hub accepted the retire: the row shows "retiring" and stays un-retirable
  // while the cached snapshot is still held.
  await waitFor(() => {
    const acceptingRow = screen.getByRole("row", { name: /Cached-rostered resident/ });
    expect(within(acceptingRow).getAllByRole("cell")[4]!.textContent).toBe("retiring");
  });
  expect(
    isDisabled(
      within(screen.getByRole("row", { name: /Cached-rostered resident/ })).getByRole("button", { name: "Retire now" }),
    ),
  ).toBe(true);

  // The cached snapshot lands still reporting phase "resident" with a current
  // probe. It must not supersede the accepted result: the row stays "retiring"
  // and the Retire button stays disabled rather than reverting to "resident" and
  // re-enabling a repeat click for a retirement that already succeeded.
  await waitFor(() => {
    expect(fake.calls.filter((c) => c.method === "evener/daemon/list").length).toBeGreaterThanOrEqual(2);
  });
  await act(async () => {
    releaseRefresh();
    await Promise.resolve();
  });
  const settledRow = screen.getByRole("row", { name: /Cached-rostered resident/ });
  expect(within(settledRow).getAllByRole("cell")[4]!.textContent).toBe("retiring");
  expect(isDisabled(within(settledRow).getByRole("button", { name: "Retire now" }))).toBe(true);
});

test("an accepted retire is cleared when its row leaves the roster", async () => {
  const fake = connectFakeClient();
  let present = true;
  const resident = {
    identity: IDENTITY_FIXTURE,
    name: "Accepted departing resident",
    protocol: "evener-appwire-v5",
    compatibility: "compatible",
    archived: false,
    probeState: "current",
    lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
    canRetire: true,
    canForceStop: true,
  };
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: present ? [resident] : [],
  }));
  fake.on("evener/daemon/retire", () => ({
    accepted: true,
    lifecycle: { phase: "retiring", timeoutMillis: 3600000, blockers: [] },
  }));
  const user = userEvent.setup();
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Accepted departing resident/ });
  await user.click(within(row).getByRole("button", { name: "Retire now" }));
  await waitFor(() => {
    expect(
      within(screen.getByRole("row", { name: /Accepted departing resident/ })).getAllByRole("cell")[4]!.textContent,
    ).toBe("retiring");
  });

  // The daemon departs the roster: the accepted result must be dropped, not
  // carried onto a later row that reuses the same generation/ref.
  present = false;
  await act(async () => {
    await import("../../../stores/daemonResidents").then((m) => m.daemonResidentsStore.getState().refresh());
  });
  expect(screen.queryByRole("row", { name: /Accepted departing resident/ })).toBeNull();

  present = true;
  await act(async () => {
    await import("../../../stores/daemonResidents").then((m) => m.daemonResidentsStore.getState().refresh());
  });
  const returned = await screen.findByRole("row", { name: /Accepted departing resident/ });
  expect(within(returned).getAllByRole("cell")[4]!.textContent).toBe("resident");
});

test("an accepted retire is cleared when the daemon reaches a genuinely different phase", async () => {
  const fake = connectFakeClient();
  // The row's own probe tracks its lifecycle; the accepted retire records
  // "retiring" locally before the list reports the daemon's next phase.
  let phase = "resident";
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Phase-moving resident",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: "current",
        lifecycle: { phase, timeoutMillis: 3600000, blockers: [] },
        canRetire: phase === "resident",
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

  const row = await screen.findByRole("row", { name: /Phase-moving resident/ });
  await user.click(within(row).getByRole("button", { name: "Retire now" }));
  await waitFor(() => {
    expect(
      within(screen.getByRole("row", { name: /Phase-moving resident/ })).getAllByRole("cell")[4]!.textContent,
    ).toBe("retiring");
  });

  // The daemon moves to a different phase the retire result does not describe:
  // that is a genuine transition, so the accepted result must give way to the
  // current probe rather than pin the row at "retiring".
  phase = "preparing";
  await act(async () => {
    await import("../../../stores/daemonResidents").then((m) => m.daemonResidentsStore.getState().refresh());
  });
  const moved = await screen.findByRole("row", { name: /Phase-moving resident/ });
  expect(within(moved).getAllByRole("cell")[4]!.textContent).toBe("preparing");
});

// ─── Retire refusal: blockers displayed, row kept ────────────────────────────

test("retire refusal persists across an immediately-following fresh current snapshot with no blockers", async () => {
  const fake = connectFakeClient();
  const resident = {
    identity: IDENTITY_FIXTURE,
    name: "Blocked resident",
    protocol: "evener-appwire-v5",
    compatibility: "compatible",
    archived: false,
    probeState: "current",
    lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
    canRetire: true,
    canForceStop: true,
  };
  // The first list renders the row. The post-refusal refresh is held so the
  // refusal's own blockers can be asserted before a fresh snapshot arrives; the
  // release then reports a fresh current snapshot with no blockers.
  let listCalls = 0;
  let releaseRefresh!: () => void;
  fake.on("evener/daemon/list", () => {
    listCalls += 1;
    if (listCalls === 1) {
      return { defaultTimeoutMillis: 3600000, daemons: [resident] };
    }
    return new Promise<DaemonListResponse>((resolve) => {
      releaseRefresh = () => resolve({ defaultTimeoutMillis: 3600000, daemons: [resident] });
    });
  });
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

  // Row must still be visible after the refusal, and the returned blocker
  // category and sessionId must both be shown while the fresh snapshot is held.
  await screen.findByRole("row", { name: /Blocked resident/ });
  expect(screen.getByText(/turn/)).toBeTruthy();
  expect(screen.getByText(/session-abc/)).toBeTruthy();

  // The held refresh arrives as a fresh current snapshot with no blockers. The
  // list's lifecycle reports only in-flight leases, not the offline obligation
  // behind an accepted:false retire, so its empty blocker set does not prove
  // the refusal resolved: the refusal must persist.
  await act(async () => {
    releaseRefresh();
    await Promise.resolve();
  });
  expect(screen.getByText(/session-abc/)).toBeTruthy();
});

// ─── M-9: A fresh snapshot alone does not clear a refusal ────────────────────

test("retire refusal survives a fresh current snapshot that stops reporting the blocker", async () => {
  const fake = connectFakeClient();
  // The list's own probe tracks the blocker, as the server would report it.
  let blockers: { category: string; sessionId?: string }[] = [{ category: "turn", sessionId: "session-abc" }];
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Resolving blocker",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: "current",
        lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers },
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

  const row = await screen.findByRole("row", { name: /Resolving blocker/ });
  await user.click(within(row).getByRole("button", { name: "Retire now" }));
  await screen.findAllByText(/session-abc/);

  // The snapshot stops reporting the blocker at the SAME phase. That is not
  // evidence the refusal resolved — the snapshot never enumerated the offline
  // obligation in the first place — so the refusal's own copy must persist as
  // the only remaining report.
  blockers = [];
  await act(async () => {
    await import("../../../stores/daemonResidents").then((m) => m.daemonResidentsStore.getState().refresh());
  });

  expect(screen.getAllByText(/session-abc/)).toHaveLength(1);
});

// ─── Round-8 finding 6: no duplicate "Blocked by" lines after a refusal ──────

test("a refused retire whose blockers subsume the snapshot blockers renders one Blocked by line", async () => {
  const fake = connectFakeClient();
  // The list snapshot already reports the in-flight delegate lease, exactly as
  // the server's status snapshot does.
  const inFlightBlocker = { category: "delegate", sessionId: "session-root", delegateId: "dlg-xyz" };
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Duplicate blocked resident",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: "current",
        lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [inFlightBlocker] },
        canRetire: true,
        canForceStop: true,
      },
    ],
  }));
  // The refusal's lifecycle is the daemon's fresh claim snapshot: it carries the
  // same in-flight delegate lease plus the offline obligation behind the
  // refusal, so it subsumes the snapshot blocker above.
  fake.on("evener/daemon/retire", () => ({
    accepted: false,
    lifecycle: {
      phase: "resident",
      timeoutMillis: 3600000,
      blockers: [inFlightBlocker, { category: "job", sessionId: "session-root" }],
    },
  }));
  const user = userEvent.setup();
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Duplicate blocked resident/ });
  await user.click(within(row).getByRole("button", { name: "Retire now" }));

  // The refusal's blockers include the in-flight lease the snapshot also
  // reports. Rendering the snapshot-blockers div as well would duplicate the
  // same "Blocked by:" line, so exactly one such line must remain.
  await waitFor(() => {
    const updatedRow = screen.getByRole("row", { name: /Duplicate blocked resident/ });
    expect(within(updatedRow).getAllByText(/^Blocked by:/)).toHaveLength(1);
  });
  const updatedRow = screen.getByRole("row", { name: /Duplicate blocked resident/ });
  // The subsumed in-flight blocker must appear once, and the refusal-only
  // obligation must still be shown.
  expect(within(updatedRow).getAllByText(/dlg-xyz/)).toHaveLength(1);
  expect(within(updatedRow).getByText(/job \(session-root\)/)).toBeTruthy();
});

// ─── M-6: Departed-daemon per-row state is pruned ────────────────────────────

test("a departed daemon's action error is pruned and does not resurface on a same-ref row", async () => {
  const fake = connectFakeClient();
  let present = true;
  const resident = {
    identity: IDENTITY_FIXTURE,
    name: "Departing daemon",
    protocol: "evener-appwire-v5",
    compatibility: "compatible",
    archived: false,
    probeState: "current",
    lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
    canRetire: true,
    canForceStop: true,
  };
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: present ? [resident] : [],
  }));
  fake.on("evener/thread/forceStop", () => {
    throw new Error("network error");
  });
  const user = userEvent.setup();
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Departing daemon/ });
  await user.click(within(row).getByRole("button", { name: "Force stop" }));
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Force stop" }));
  const erroredRow = await screen.findByRole("row", { name: /Departing daemon/ });
  expect(erroredRow.querySelector("[role='alert']")).toBeTruthy();

  // The daemon departs the list, then a row with the SAME ref returns.
  present = false;
  await act(async () => {
    await import("../../../stores/daemonResidents").then((m) => m.daemonResidentsStore.getState().refresh());
  });
  present = true;
  await act(async () => {
    await import("../../../stores/daemonResidents").then((m) => m.daemonResidentsStore.getState().refresh());
  });

  const freshRow = await screen.findByRole("row", { name: /Departing daemon/ });
  expect(freshRow.querySelector("[role='alert']")).toBeNull();
});

// ─── M-6: Stale retire-refusal blockers clear on lifecycle change ─────────────

test("retire refusal blockers clear when a newer snapshot changes the row lifecycle", async () => {
  const fake = connectFakeClient();
  const resident = {
    identity: IDENTITY_FIXTURE,
    name: "Lifecycle changing",
    protocol: "evener-appwire-v5",
    compatibility: "compatible",
    archived: false,
    probeState: "current",
    lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
    canRetire: true,
    canForceStop: true,
  };
  // Hold the post-refusal refresh so the refusal's blockers can be asserted
  // before the snapshot that supersedes them is released.
  let listCalls = 0;
  let releaseRefresh!: () => void;
  fake.on("evener/daemon/list", () => {
    listCalls += 1;
    if (listCalls === 1) {
      return { defaultTimeoutMillis: 3600000, daemons: [resident] };
    }
    return new Promise<DaemonListResponse>((resolve) => {
      releaseRefresh = () =>
        resolve({
          defaultTimeoutMillis: 3600000,
          daemons: [
            {
              ...resident,
              lifecycle: { phase: "retiring", timeoutMillis: 3600000, blockers: [] }, // phase changed
              canRetire: false,
            },
          ],
        });
    });
  });
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

  // Retire is refused; blockers must appear
  const row = await screen.findByRole("row", { name: /Lifecycle changing/ });
  await user.click(within(row).getByRole("button", { name: "Retire now" }));
  await screen.findByText(/session-abc/);

  // The held snapshot arrives with a changed lifecycle phase (e.g., an external
  // retire happened between polls).
  await act(async () => {
    releaseRefresh();
    await Promise.resolve();
  });

  // Blockers from the old refusal must have been cleared
  expect(screen.queryByText(/session-abc/)).toBeNull();
});

// ─── M-9: Refusal clears on the two remaining legitimate transitions ─────────

test("retire refusal clears when the daemon leaves the roster", async () => {
  const fake = connectFakeClient();
  let present = true;
  const resident = {
    identity: IDENTITY_FIXTURE,
    name: "Departing refused daemon",
    protocol: "evener-appwire-v5",
    compatibility: "compatible",
    archived: false,
    probeState: "current",
    lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
    canRetire: true,
    canForceStop: true,
  };
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: present ? [resident] : [],
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

  const row = await screen.findByRole("row", { name: /Departing refused daemon/ });
  await user.click(within(row).getByRole("button", { name: "Retire now" }));
  await screen.findByText(/session-abc/);

  // The daemon departs the roster: the refusal must not resurface on a later
  // row that happens to reuse the same ref.
  present = false;
  await act(async () => {
    await import("../../../stores/daemonResidents").then((m) => m.daemonResidentsStore.getState().refresh());
  });
  expect(screen.queryByText(/session-abc/)).toBeNull();

  present = true;
  await act(async () => {
    await import("../../../stores/daemonResidents").then((m) => m.daemonResidentsStore.getState().refresh());
  });
  const returned = await screen.findByRole("row", { name: /Departing refused daemon/ });
  expect(within(returned).queryByText(/session-abc/)).toBeNull();
});

test("initiating a new retire clears the previous refusal", async () => {
  const fake = connectFakeClient();
  let retireCalls = 0;
  let releaseSecondRetire!: () => void;
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Retried refused daemon",
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
  fake.on("evener/daemon/retire", () => {
    retireCalls += 1;
    if (retireCalls === 1) {
      return {
        accepted: false,
        lifecycle: {
          phase: "resident",
          timeoutMillis: 3600000,
          blockers: [{ category: "turn", sessionId: "session-abc" }],
        },
      };
    }
    // Hold the second attempt so the refusal can only disappear through the
    // new-action transition, not because a fresh response replaced it.
    return new Promise<DaemonRetireResponse>((resolve) => {
      releaseSecondRetire = () =>
        resolve({
          accepted: false,
          lifecycle: { phase: "resident", timeoutMillis: 3600000, blockers: [] },
        });
    });
  });
  const user = userEvent.setup();
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Retried refused daemon/ });
  await user.click(within(row).getByRole("button", { name: "Retire now" }));
  await screen.findByText(/session-abc/);

  // A new action is initiated: the old refusal must be dropped immediately,
  // before the second RPC resolves.
  await user.click(
    within(screen.getByRole("row", { name: /Retried refused daemon/ })).getByRole("button", { name: "Retire now" }),
  );
  await act(async () => {
    for (let i = 0; i < 5; i++) await Promise.resolve();
  });
  expect(screen.queryByText(/session-abc/)).toBeNull();

  // Clean up: resolve the held retire so the test does not leak async work.
  await act(async () => {
    releaseSecondRetire();
    for (let i = 0; i < 5; i++) await Promise.resolve();
  });
});

// ─── M-9: Keyboard focus reachability ────────────────────────────────────────

test("Retire now button is keyboard-focusable", async () => {
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: IDENTITY_FIXTURE,
        name: "Focus target",
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
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Focus target/ });
  const retireBtn = within(row).getByRole("button", { name: "Retire now" });

  // Programmatic focus must land on the button (keyboard reachability)
  retireBtn.focus();
  expect(document.activeElement).toBe(retireBtn);
});

// ─── Effective timeout: a sub-second value must not read as disabled ─────────

test("a sub-second timeout does not render as the disabled zero sentinel", async () => {
  const fake = connectFakeClient();
  fake.on("evener/daemon/list", () => ({
    defaultTimeoutMillis: 3600000,
    daemons: [
      {
        identity: { ref: "local:subsecond", pid: 302, startedAt: "2026-09-10T00:00:00Z", generation: "gen-sub" },
        name: "Subsecond timeout",
        protocol: "evener-appwire-v5",
        compatibility: "compatible",
        archived: false,
        probeState: "current",
        // daemon_idle_timeout is a duration, so hub.toml can express a
        // sub-second deadline. The daemon treats timeoutMillis === 0 as
        // "automatic retirement disabled", so flooring 500ms to "0s" tells an
        // operator retirement is off while it is actually armed.
        lifecycle: { phase: "resident", timeoutMillis: 500, blockers: [] },
        canRetire: true,
        canForceStop: true,
      },
    ],
  }));
  render(<HubResidents />);

  const row = await screen.findByRole("row", { name: /Subsecond timeout/ });
  const cells = within(row).getAllByRole("cell");
  // Column 3 (0-indexed) = Effective timeout.
  expect(cells[3]!.textContent).toBe("<1s");
});

// ─── Empty state survives a poll ─────────────────────────────────────────────

test("the empty state survives a background poll instead of blanking", async () => {
  vi.useFakeTimers();
  const fake = connectFakeClient();
  let calls = 0;
  let releasePoll!: () => void;
  fake.on("evener/daemon/list", () => {
    calls += 1;
    if (calls === 1) {
      return { defaultTimeoutMillis: 3600000, daemons: [] };
    }
    // Hold the poll open so loading stays true: that is the state the panel
    // blanked in.
    return new Promise<DaemonListResponse>((resolve) => {
      releasePoll = () => resolve({ defaultTimeoutMillis: 3600000, daemons: [] });
    });
  });

  render(<HubResidents />);
  await act(async () => {
    for (let i = 0; i < 5; i++) await Promise.resolve();
  });

  expect(screen.queryByTestId("empty-state")).not.toBeNull();

  // The store sets loading=true at the start of every load, and the component
  // polls every 2s. With zero daemons the populated-table branch is not taken
  // either, so gating the empty state on !loading leaves nothing rendered.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
  });
  expect(screen.queryByTestId("empty-state")).not.toBeNull();

  await act(async () => {
    releasePoll();
    for (let i = 0; i < 5; i++) await Promise.resolve();
  });
  expect(screen.queryByTestId("empty-state")).not.toBeNull();
});
