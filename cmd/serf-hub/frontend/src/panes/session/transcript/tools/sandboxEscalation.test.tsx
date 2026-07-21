import { afterEach, test, expect, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderHook, act } from "@testing-library/react";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import { ClientProvider } from "../../../../shell/clientContext";
import { SandboxEscalationCard, SandboxEscalationRail, useSandboxEscalations } from "./sandboxEscalation";
import type { AnyNotification, SandboxEscalationRequested } from "../../../../protocol/types.gen";
import { type ReactNode } from "react";

afterEach(cleanup);

// Ground truth (see the wave-4 task-3 report for the full trail):
// serf/sandbox/escalation/requested + SerfThread.pendingEscalations are
// thread-level, not item-level - protocol/reducer.ts has NO case for the
// notification (falls to `default`, a no-op) and hydrateThread never
// reads thread.serf.pendingEscalations. Neither ItemRenderProps nor
// ToolRenderProps carries a ref or the owning ThreadModel (confirmed by
// reading ToolCallItem.tsx: it receives `turn` but never forwards it),
// so there is no registerToolRenderer/registerItemRenderer integration
// point for this at all - unlike every other T3 surface. This builds the
// card + the data hook as standalone, fully tested units; wiring them
// into the live tree needs a Session.tsx-level mount (outside
// transcript/tools/**'s ownership) and, for the reconnect/already-pending
// case specifically, a reducer projection of pendingEscalations (also
// outside this stream's ownership) - both documented as a handoff, not
// silently skipped.

function requested(overrides: Partial<SandboxEscalationRequested> = {}): SandboxEscalationRequested {
  return {
    ref: "ref_a",
    threadId: "thr_a",
    escalationId: "esc_1",
    mode: "workspace-write",
    tool: "shell",
    kind: "shell",
    deniedPath: "/etc/hosts",
    ...overrides,
  };
}

// --- SandboxEscalationCard (presentational) -------------------------------

test("renders the harness-prompt copy naming the tool, path, and sandbox mode", () => {
  render(<SandboxEscalationCard escalation={requested()} onApprove={() => {}} onDeny={() => {}} resolved={false} />);
  expect(screen.getByText(/sandbox approval/i)).toBeTruthy();
  expect(screen.getByText(/shell/)).toBeTruthy();
  expect(screen.getByText(/\/etc\/hosts/)).toBeTruthy();
  expect(screen.getByText(/workspace-write/)).toBeTruthy();
});

test("approve/deny buttons call their handlers", async () => {
  const user = userEvent.setup();
  const onApprove = vi.fn();
  const onDeny = vi.fn();
  render(<SandboxEscalationCard escalation={requested()} onApprove={onApprove} onDeny={onDeny} resolved={false} />);

  await user.click(screen.getByRole("button", { name: /allow/i }));
  expect(onApprove).toHaveBeenCalledTimes(1);

  await user.click(screen.getByRole("button", { name: /deny/i }));
  expect(onDeny).toHaveBeenCalledTimes(1);
});

test("both buttons are disabled once resolved", () => {
  render(<SandboxEscalationCard escalation={requested()} onApprove={() => {}} onDeny={() => {}} resolved={true} />);
  const allow = screen.getByRole("button", { name: /allow/i }) as HTMLButtonElement;
  const deny = screen.getByRole("button", { name: /deny/i }) as HTMLButtonElement;
  expect(allow.disabled).toBe(true);
  expect(deny.disabled).toBe(true);
});

// --- useSandboxEscalations (data hook, TDD with FakeClient) --------------

function withClient(client: FakeClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <ClientProvider client={client}>{children}</ClientProvider>;
  };
}

test("starts with no pending escalations", () => {
  const fake = new FakeClient("ready");
  const { result } = renderHook(() => useSandboxEscalations("ref_a"), { wrapper: withClient(fake) });
  expect(result.current.pending).toEqual([]);
});

test("a live serf/sandbox/escalation/requested notification for this ref appears in `pending`", () => {
  const fake = new FakeClient("ready");
  const { result } = renderHook(() => useSandboxEscalations("ref_a"), { wrapper: withClient(fake) });

  act(() => {
    fake.emitNotification({
      method: "serf/sandbox/escalation/requested",
      params: requested(),
    } as AnyNotification);
  });

  expect(result.current.pending).toHaveLength(1);
  expect(result.current.pending[0]?.escalationId).toBe("esc_1");
});

test("a notification for a DIFFERENT ref is ignored", () => {
  const fake = new FakeClient("ready");
  const { result } = renderHook(() => useSandboxEscalations("ref_a"), { wrapper: withClient(fake) });

  act(() => {
    fake.emitNotification({
      method: "serf/sandbox/escalation/requested",
      params: requested({ ref: "ref_other" }),
    } as AnyNotification);
  });

  expect(result.current.pending).toEqual([]);
});

test("resolve(escalationId, true) calls serf/sandbox/escalation/resolve with approve:true and removes it from pending", async () => {
  const fake = new FakeClient("ready");
  fake.on("serf/sandbox/escalation/resolve", () => ({}));
  const { result } = renderHook(() => useSandboxEscalations("ref_a"), { wrapper: withClient(fake) });

  act(() => {
    fake.emitNotification({ method: "serf/sandbox/escalation/requested", params: requested() } as AnyNotification);
  });
  expect(result.current.pending).toHaveLength(1);

  await act(async () => {
    await result.current.resolve("esc_1", true);
  });

  const call = fake.calls.find((c) => c.method === "serf/sandbox/escalation/resolve");
  expect(call?.params).toEqual({ ref: "ref_a", escalationId: "esc_1", approve: true });
  expect(result.current.pending).toEqual([]);
});

test("resolve(escalationId, false) sends approve:false", async () => {
  const fake = new FakeClient("ready");
  fake.on("serf/sandbox/escalation/resolve", () => ({}));
  const { result } = renderHook(() => useSandboxEscalations("ref_a"), { wrapper: withClient(fake) });

  act(() => {
    fake.emitNotification({ method: "serf/sandbox/escalation/requested", params: requested() } as AnyNotification);
  });

  await act(async () => {
    await result.current.resolve("esc_1", false);
  });

  const call = fake.calls.find((c) => c.method === "serf/sandbox/escalation/resolve");
  expect(call?.params).toEqual({ ref: "ref_a", escalationId: "esc_1", approve: false });
});

// --- SandboxEscalationRail (hook + card wired together) -------------------

test("clicking Allow immediately disables that card (before the resolve response arrives), preventing a double-submit", async () => {
  const fake = new FakeClient("ready");
  const box: { resolveCall: (() => void) | null } = { resolveCall: null };
  fake.on("serf/sandbox/escalation/resolve", () => new Promise((res) => (box.resolveCall = () => res({}))));
  const user = userEvent.setup();

  render(<SandboxEscalationRail sessionRef="ref_a" />, { wrapper: withClient(fake) });
  act(() => {
    fake.emitNotification({ method: "serf/sandbox/escalation/requested", params: requested() } as AnyNotification);
  });

  const allow = await screen.findByRole("button", { name: /allow/i });
  await user.click(allow);

  expect((screen.getByRole("button", { name: /allow/i }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole("button", { name: /deny/i }) as HTMLButtonElement).disabled).toBe(true);

  await act(async () => {
    box.resolveCall?.();
    await Promise.resolve();
  });
  expect(screen.queryByRole("button", { name: /allow/i })).toBeNull(); // resolved -> removed entirely
});

// handleResolve's onApprove/onDeny call sites fire-and-forget
// (`() => void handleResolve(...)`), so a rejection MUST be caught inside
// handleResolve itself - an uncaught one becomes an unhandled promise
// rejection (vitest fails the run on those by default) and the user sees
// nothing at all. This is the wave's only interactive surface, so a failure
// has to be visible and the card has to stay answerable (retry possible).
test("a rejected resolve surfaces an error on the card instead of an unhandled rejection, and re-enables the buttons", async () => {
  const fake = new FakeClient("ready");
  fake.on("serf/sandbox/escalation/resolve", () => Promise.reject(new Error("sandbox offline")));
  const user = userEvent.setup();

  render(<SandboxEscalationRail sessionRef="ref_a" />, { wrapper: withClient(fake) });
  act(() => {
    fake.emitNotification({ method: "serf/sandbox/escalation/requested", params: requested() } as AnyNotification);
  });

  const allow = await screen.findByRole("button", { name: /allow/i });
  await user.click(allow);

  expect(await screen.findByText(/sandbox offline/)).toBeTruthy();
  expect((screen.getByRole("button", { name: /allow/i }) as HTMLButtonElement).disabled).toBe(false);
  expect((screen.getByRole("button", { name: /deny/i }) as HTMLButtonElement).disabled).toBe(false);
});

test("a subsequent successful resolve clears a previously shown error", async () => {
  const fake = new FakeClient("ready");
  let shouldFail = true;
  fake.on("serf/sandbox/escalation/resolve", () => (shouldFail ? Promise.reject(new Error("sandbox offline")) : {}));
  const user = userEvent.setup();

  render(<SandboxEscalationRail sessionRef="ref_a" />, { wrapper: withClient(fake) });
  act(() => {
    fake.emitNotification({ method: "serf/sandbox/escalation/requested", params: requested() } as AnyNotification);
  });

  const allow = await screen.findByRole("button", { name: /allow/i });
  await user.click(allow);
  expect(await screen.findByText(/sandbox offline/)).toBeTruthy();

  shouldFail = false;
  await user.click(screen.getByRole("button", { name: /allow/i }));

  await waitFor(() => expect(screen.queryByText(/sandbox offline/)).toBeNull());
});

test("SandboxEscalationRail renders one card per pending escalation, keyed by escalationId", () => {
  const fake = new FakeClient("ready");
  render(<SandboxEscalationRail sessionRef="ref_a" />, { wrapper: withClient(fake) });
  act(() => {
    fake.emitNotification({ method: "serf/sandbox/escalation/requested", params: requested({ escalationId: "esc_1" }) } as AnyNotification);
    fake.emitNotification({ method: "serf/sandbox/escalation/requested", params: requested({ escalationId: "esc_2" }) } as AnyNotification);
  });
  expect(screen.getAllByText(/sandbox approval/i)).toHaveLength(2);
});

test("two distinct escalations both surface independently", () => {
  const fake = new FakeClient("ready");
  const { result } = renderHook(() => useSandboxEscalations("ref_a"), { wrapper: withClient(fake) });

  act(() => {
    fake.emitNotification({ method: "serf/sandbox/escalation/requested", params: requested({ escalationId: "esc_1" }) } as AnyNotification);
    fake.emitNotification({ method: "serf/sandbox/escalation/requested", params: requested({ escalationId: "esc_2", deniedPath: "/etc/shadow" }) } as AnyNotification);
  });

  expect(result.current.pending.map((e) => e.escalationId)).toEqual(["esc_1", "esc_2"]);
});

test("a duplicate notification for the same escalationId is de-duplicated, not appended twice", () => {
  const fake = new FakeClient("ready");
  const { result } = renderHook(() => useSandboxEscalations("ref_a"), { wrapper: withClient(fake) });

  act(() => {
    fake.emitNotification({ method: "serf/sandbox/escalation/requested", params: requested() } as AnyNotification);
    fake.emitNotification({ method: "serf/sandbox/escalation/requested", params: requested() } as AnyNotification);
  });

  expect(result.current.pending).toHaveLength(1);
});
