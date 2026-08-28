import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type {
  SandboxEscalationRequested,
  Thread,
  ThreadCapabilities,
  ThreadReadResponse,
} from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import { SandboxEscalationCard, SandboxEscalationRail, useSandboxEscalations } from "./sandboxEscalation";

// Ground truth: evener/sandbox/escalation/requested + EvenerThread.
// pendingEscalations are thread-level, not item-level - so, unlike every
// other T3 surface, there is no registerToolRenderer/registerItemRenderer
// integration point for this at all (confirmed by reading ToolCallItem.tsx:
// it receives `turn` but never forwards it). This builds the card + the
// data hook as standalone, fully tested units, ready for a Session.tsx-
// level mount (outside transcript/tools/**'s ownership).
//
// R3 closed both gaps the original version of this file's hook could not:
// protocol/reducer.ts now projects thread.evener.pendingEscalations into
// ThreadModel at hydrate (hydrateThread) and live-updates it from the
// notification (applyNotification's own case + upsertPendingEscalation).
// This hook now reads that model directly (via the threads store) instead
// of keeping its own local pending list/notification subscription, so a
// cold-open/reconnect's already-pending escalations render for free, and
// resolve() delegates to the threads store's own resolveEscalation action
// (stores/threads.ts) rather than calling the wire itself.

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

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

// connectFakeClient wires a fresh FakeClient through connectionStore
// directly - the threads store's own requireClient() rides that, not React
// context, so no <ClientProvider> is needed anywhere in this file.
function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function readResponseWithEscalation(ref: string, escalation: SandboxEscalationRequested): ThreadReadResponse {
  return readResponse(ref, {
    evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
  // The beforeEach above only resets threadsStore BEFORE each test. Several
  // tests here call ensureThread("ref_a") directly for setup - useSandboxEscalations
  // doesn't itself release it, so cleanup()'s unmount leaves "ref_a"
  // refcounted after the LAST test. Under isolate:false that is what a
  // later file's own connectionStore.connect() re-triggers via rewireClient.
  resetThreadsStoreForTests();
});

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

// --- useSandboxEscalations (reads ThreadModel.pendingEscalations, resolve
// delegates to the threads-store action) -----------------------------------

test("starts with no pending escalations when the tracked model has none", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  const { result } = renderHook(() => useSandboxEscalations("ref_a"));
  expect(result.current.pending).toEqual([]);
});

test("a cold-open snapshot's pendingEscalations render immediately - the exact case the old hook could not handle", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponseWithEscalation("ref_a", requested()));
  await threadsStore.getState().ensureThread("ref_a");

  const { result } = renderHook(() => useSandboxEscalations("ref_a"));
  expect(result.current.pending).toHaveLength(1);
  expect(result.current.pending[0]?.escalationId).toBe("esc_1");
});

test("a live evener/sandbox/escalation/requested notification, folded through the store, appears in `pending`", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  const { result } = renderHook(() => useSandboxEscalations("ref_a"));
  expect(result.current.pending).toEqual([]);

  act(() => {
    fake.emitNotification({ method: "evener/sandbox/escalation/requested", params: requested() });
  });

  expect(result.current.pending).toHaveLength(1);
  expect(result.current.pending[0]?.escalationId).toBe("esc_1");
});

test("a notification for a DIFFERENT ref is ignored", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  const { result } = renderHook(() => useSandboxEscalations("ref_a"));
  act(() => {
    fake.emitNotification({
      method: "evener/sandbox/escalation/requested",
      params: requested({ ref: "ref_other" }),
    });
  });

  expect(result.current.pending).toEqual([]);
});

test("resolve(escalationId, true) delegates to the threads store, sending approve:true and removing it from `pending`", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponseWithEscalation("ref_a", requested()));
  fake.on("evener/sandbox/escalation/resolve", () => ({}));
  await threadsStore.getState().ensureThread("ref_a");

  const { result } = renderHook(() => useSandboxEscalations("ref_a"));
  expect(result.current.pending).toHaveLength(1);

  await act(async () => {
    await result.current.resolve("esc_1", true);
  });

  const call = fake.calls.find((c) => c.method === "evener/sandbox/escalation/resolve");
  expect(call?.params).toEqual({ ref: "ref_a", escalationId: "esc_1", approve: true });
  expect(result.current.pending).toEqual([]);
});

test("resolve(escalationId, false) sends approve:false", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponseWithEscalation("ref_a", requested()));
  fake.on("evener/sandbox/escalation/resolve", () => ({}));
  await threadsStore.getState().ensureThread("ref_a");

  const { result } = renderHook(() => useSandboxEscalations("ref_a"));
  await act(async () => {
    await result.current.resolve("esc_1", false);
  });

  const call = fake.calls.find((c) => c.method === "evener/sandbox/escalation/resolve");
  expect(call?.params).toEqual({ ref: "ref_a", escalationId: "esc_1", approve: false });
});

test("a rejected resolve() propagates to the caller and leaves `pending` untouched", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponseWithEscalation("ref_a", requested()));
  fake.on("evener/sandbox/escalation/resolve", () => Promise.reject(new Error("sandbox offline")));
  await threadsStore.getState().ensureThread("ref_a");

  const { result } = renderHook(() => useSandboxEscalations("ref_a"));
  await expect(result.current.resolve("esc_1", true)).rejects.toThrow("sandbox offline");
  expect(result.current.pending).toHaveLength(1);
});

test("two distinct escalations both surface independently", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  const { result } = renderHook(() => useSandboxEscalations("ref_a"));
  act(() => {
    fake.emitNotification({
      method: "evener/sandbox/escalation/requested",
      params: requested({ escalationId: "esc_1" }),
    });
    fake.emitNotification({
      method: "evener/sandbox/escalation/requested",
      params: requested({ escalationId: "esc_2", deniedPath: "/etc/shadow" }),
    });
  });

  expect(result.current.pending.map((e) => e.escalationId)).toEqual(["esc_1", "esc_2"]);
});

test("a duplicate notification for the same escalationId is de-duplicated, not appended twice (protocol/reducer.ts's upsertPendingEscalation)", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  const { result } = renderHook(() => useSandboxEscalations("ref_a"));
  act(() => {
    fake.emitNotification({ method: "evener/sandbox/escalation/requested", params: requested() });
    fake.emitNotification({ method: "evener/sandbox/escalation/requested", params: requested() });
  });

  expect(result.current.pending).toHaveLength(1);
});

// --- SandboxEscalationRail (hook + card wired together) -------------------
// The per-escalation "resolving"/"errors" component-local state (disable on
// click, surface a rejection, clear on retry) is this component's own logic,
// unrelated to where `pending`/`resolve` come from - preserved verbatim
// across the store-backed rewrite above, re-verified here.

test("clicking Allow immediately disables that card (before the resolve response arrives), preventing a double-submit", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  const box: { resolveCall: (() => void) | null } = { resolveCall: null };
  fake.on("evener/sandbox/escalation/resolve", () => new Promise((res) => (box.resolveCall = () => res({}))));
  const user = userEvent.setup();
  await threadsStore.getState().ensureThread("ref_a");

  render(<SandboxEscalationRail sessionRef="ref_a" />);
  act(() => {
    fake.emitNotification({ method: "evener/sandbox/escalation/requested", params: requested() });
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
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  fake.on("evener/sandbox/escalation/resolve", () => Promise.reject(new Error("sandbox offline")));
  const user = userEvent.setup();
  await threadsStore.getState().ensureThread("ref_a");

  render(<SandboxEscalationRail sessionRef="ref_a" />);
  act(() => {
    fake.emitNotification({ method: "evener/sandbox/escalation/requested", params: requested() });
  });

  const allow = await screen.findByRole("button", { name: /allow/i });
  await user.click(allow);

  expect(await screen.findByText(/sandbox offline/)).toBeTruthy();
  expect((screen.getByRole("button", { name: /allow/i }) as HTMLButtonElement).disabled).toBe(false);
  expect((screen.getByRole("button", { name: /deny/i }) as HTMLButtonElement).disabled).toBe(false);
  // The card stays: a rejection is local UI state, not a store mutation.
  expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toHaveLength(1);
});

test("a subsequent successful resolve clears a previously shown error", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  let shouldFail = true;
  fake.on("evener/sandbox/escalation/resolve", () => (shouldFail ? Promise.reject(new Error("sandbox offline")) : {}));
  const user = userEvent.setup();
  await threadsStore.getState().ensureThread("ref_a");

  render(<SandboxEscalationRail sessionRef="ref_a" />);
  act(() => {
    fake.emitNotification({ method: "evener/sandbox/escalation/requested", params: requested() });
  });

  const allow = await screen.findByRole("button", { name: /allow/i });
  await user.click(allow);
  expect(await screen.findByText(/sandbox offline/)).toBeTruthy();

  shouldFail = false;
  await user.click(screen.getByRole("button", { name: /allow/i }));

  await waitFor(() => expect(screen.queryByText(/sandbox offline/)).toBeNull());
});

test("a cold-open snapshot's pendingEscalations render as cards with no live notification needed", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponseWithEscalation("ref_a", requested()));
  await threadsStore.getState().ensureThread("ref_a");

  render(<SandboxEscalationRail sessionRef="ref_a" />);
  expect(await screen.findByText(/sandbox approval/i)).toBeTruthy();
});

test("SandboxEscalationRail renders one card per pending escalation, keyed by escalationId", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  render(<SandboxEscalationRail sessionRef="ref_a" />);
  act(() => {
    fake.emitNotification({
      method: "evener/sandbox/escalation/requested",
      params: requested({ escalationId: "esc_1" }),
    });
    fake.emitNotification({
      method: "evener/sandbox/escalation/requested",
      params: requested({ escalationId: "esc_2" }),
    });
  });
  expect(screen.getAllByText(/sandbox approval/i)).toHaveLength(2);
});

// --- reviewed UX fixes: amber envelope, sticky pin, autofocus, Mod+Enter --

function sandboxEscalationCss(): string {
  const path = join(dirname(fileURLToPath(import.meta.url)), "sandboxescalation.module.css");
  return readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

test("the card's container carries the amber attention envelope, not a neutral surface (declaration-level)", () => {
  const rule = /\.card\s*\{([^}]*)\}/.exec(sandboxEscalationCss());
  expect(rule).not.toBeNull();
  expect(rule![1]).toContain("border: 1px solid var(--attention-edge)");
  expect(rule![1]).toContain("background: var(--attention-bg)");
});

test("the rail caps its own height and scrolls internally, so several pending escalations can't squeeze the transcript out (declaration-level)", () => {
  const rule = /\.rail\s*\{([^}]*)\}/.exec(sandboxEscalationCss());
  expect(rule).not.toBeNull();
  expect(rule![1]).toContain("max-height: 40vh");
  expect(rule![1]).toContain("overflow-y: auto");
});

test("nothing pending renders no sticky rail wrapper at all", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  const { container } = render(<SandboxEscalationRail sessionRef="ref_a" />);
  expect(container.firstChild).toBeNull();
});

test("an escalation arriving focuses the Allow button", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  render(<SandboxEscalationRail sessionRef="ref_a" />);
  act(() => {
    fake.emitNotification({ method: "evener/sandbox/escalation/requested", params: requested() });
  });

  const allow = await screen.findByRole("button", { name: /allow/i });
  expect(document.activeElement).toBe(allow);
});

// Reviewed fix: the autofocus edge fires on mount too, not just on a later
// arrival, so a cold-open escalation showing up while the reader is already
// mid-keystroke in the composer must not steal focus onto Allow - the very
// next Enter/Space they type would approve an escalation they haven't read.
test("does not steal focus while a textarea is focused", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  const textarea = document.createElement("textarea");
  document.body.appendChild(textarea);
  textarea.focus();
  expect(document.activeElement).toBe(textarea);

  render(<SandboxEscalationRail sessionRef="ref_a" />);
  act(() => {
    fake.emitNotification({ method: "evener/sandbox/escalation/requested", params: requested() });
  });

  await screen.findByRole("button", { name: /allow/i });
  expect(document.activeElement).toBe(textarea);
  textarea.remove();
});

// Edge-triggered, mirroring AskDock.tsx's own dock-activation effect: a
// LATER escalation that only grows an already-open rail must never steal
// focus from something the reader is already interacting with (e.g. a
// second card's Deny button already focused).
test("a later escalation that only grows an already-open rail does not steal focus", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  const user = userEvent.setup();
  await threadsStore.getState().ensureThread("ref_a");

  render(<SandboxEscalationRail sessionRef="ref_a" />);
  act(() => {
    fake.emitNotification({
      method: "evener/sandbox/escalation/requested",
      params: requested({ escalationId: "esc_1" }),
    });
  });

  const firstDeny = await screen.findByRole("button", { name: /deny/i });
  await user.click(firstDeny); // moves focus onto the first card's Deny button
  firstDeny.focus();
  expect(document.activeElement).toBe(firstDeny);

  act(() => {
    fake.emitNotification({
      method: "evener/sandbox/escalation/requested",
      params: requested({ escalationId: "esc_2", deniedPath: "/etc/shadow" }),
    });
  });

  expect(document.activeElement).toBe(firstDeny);
});

test("Mod+Enter approves the card it's fired from", async () => {
  const onApprove = vi.fn();
  const onDeny = vi.fn();
  render(<SandboxEscalationCard escalation={requested()} onApprove={onApprove} onDeny={onDeny} resolved={false} />);

  fireEvent.keyDown(screen.getByRole("button", { name: /allow/i }), { key: "Enter", metaKey: true });
  expect(onApprove).toHaveBeenCalledTimes(1);
  expect(onDeny).not.toHaveBeenCalled();
});

test("Ctrl+Enter also approves (the non-Mac modifier)", async () => {
  const onApprove = vi.fn();
  render(<SandboxEscalationCard escalation={requested()} onApprove={onApprove} onDeny={() => {}} resolved={false} />);

  fireEvent.keyDown(screen.getByRole("button", { name: /allow/i }), { key: "Enter", ctrlKey: true });
  expect(onApprove).toHaveBeenCalledTimes(1);
});

test("a plain Enter (no modifier) does not approve - only Mod+Enter does", async () => {
  const onApprove = vi.fn();
  render(<SandboxEscalationCard escalation={requested()} onApprove={onApprove} onDeny={() => {}} resolved={false} />);

  fireEvent.keyDown(screen.getByRole("button", { name: /allow/i }), { key: "Enter" });
  expect(onApprove).not.toHaveBeenCalled();
});

// Denial must never be one accidental keypress away: Escape is deliberately
// NOT wired to Deny - it stays mouse/tab-reachable only.
test("Escape does not deny - denial is never a single accidental keypress", async () => {
  const onDeny = vi.fn();
  render(<SandboxEscalationCard escalation={requested()} onApprove={() => {}} onDeny={onDeny} resolved={false} />);

  fireEvent.keyDown(screen.getByRole("button", { name: /allow/i }), { key: "Escape" });
  expect(onDeny).not.toHaveBeenCalled();
});

test("Mod+Enter does nothing once the card is resolved", async () => {
  const onApprove = vi.fn();
  render(<SandboxEscalationCard escalation={requested()} onApprove={onApprove} onDeny={() => {}} resolved={true} />);

  fireEvent.keyDown(screen.getByRole("button", { name: /allow/i }), { key: "Enter", metaKey: true });
  expect(onApprove).not.toHaveBeenCalled();
});
