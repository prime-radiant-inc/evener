import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { resetGoalOverridesForTests } from "./GoalControl";
import { SessionChrome } from "./SessionChrome";

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
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
    source: "serf",
    serf: { ref, capabilities: CAPABILITIES, queue: {} },
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetGoalOverridesForTests();
});

afterEach(() => {
  cleanup();
});

// Wave 5 T1 carved this slot as an empty placeholder ("renders nothing (T1
// placeholder - T5 fills this in)"); this file supersedes that pin now that
// T5 has actually filled it in - see this stream's own report for the
// commit range. SessionChrome's own contract stays exactly what T1 locked
// (`{ ref: string }`, nothing else) - every real prop (model, capabilities,
// ...) is read from the threads store internally, the same way every other
// pane-level component in this app does.

test("renders nothing for a ref with no tracked model yet (defensive - Session.tsx never mounts this before hydration in practice)", () => {
  const { container } = render(<SessionChrome ref="untracked_ref" />);
  expect(container.firstChild).toBeNull();
});

test("composes the status row, session actions, goal control, and tasks panel once the ref's thread is tracked", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_a"));
  await threadsStore.getState().ensureThread("ref_a");

  render(<SessionChrome ref="ref_a" />);

  // Status row: model chip.
  expect(screen.getByText("anthropic/claude-sonnet-4-5")).toBeTruthy();
  // Session actions menu trigger.
  expect(screen.getByRole("button", { name: /session actions/i })).toBeTruthy();
  // Goal control.
  expect(screen.getByText(/no goal set/i)).toBeTruthy();
  // Tasks panel trigger.
  expect(screen.getByRole("button", { name: "Tasks" })).toBeTruthy();
});

test("every composed piece acts on the SAME ref passed to SessionChrome", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_b", { name: "Session B" }));
  await threadsStore.getState().ensureThread("ref_b");
  let renamedTo: unknown;
  fake.on("serf/thread/name/set", (params) => {
    renamedTo = params;
    return {};
  });

  render(<SessionChrome ref="ref_b" />);

  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Rename" }));
  const dialog = await screen.findByRole("dialog");
  const input = dialog.querySelector("input");
  if (!input) throw new Error("rename dialog missing its input");
  await user.clear(input);
  await user.type(input, "New name");
  await user.click(screen.getByRole("button", { name: /save/i }));

  await waitFor(() => expect(renamedTo).toEqual({ ref: "ref_b", name: "New name" }));
});

test("the tasks panel fetches for the SAME ref passed to SessionChrome", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_c"));
  await threadsStore.getState().ensureThread("ref_c");
  let calledRef: unknown;
  fake.on("serf/tasks/list", (params) => {
    calledRef = params.ref;
    return { data: [] };
  });

  render(<SessionChrome ref="ref_c" />);
  await user.click(screen.getByRole("button", { name: "Tasks" }));

  await waitFor(() => expect(calledRef).toBe("ref_c"));
});
