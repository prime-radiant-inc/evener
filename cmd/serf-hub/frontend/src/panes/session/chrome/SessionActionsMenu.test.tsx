import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterAll, afterEach, beforeEach, expect, test } from "vitest";
import { WireError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities } from "../../../protocol/types.gen";
import { registerPaneForTests } from "../../../shell/paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { SessionActionsMenu } from "./SessionActionsMenu";

// A minimal, test-only "session" pane registration - real registerPane/
// paneFor/openPane machinery, just without pulling in the actual
// panes/session module (a heavier dependency this test doesn't need: it
// only asserts openPane was called correctly for fork/aside's child-pane
// hop, never that a real SessionPane renders) - mirrors transcript/tools/
// subagentModule.test.tsx's identical setup for the exact same reason.
afterAll(
  registerPaneForTests({
    id: "session",
    title: () => "test session",
    component: lazy(() => Promise.resolve({ default: () => null })),
  }),
);

const FULL_CAPABILITIES: ThreadCapabilities = {
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

function testModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "My session",
    status: { type: "idle" },
    modelProvider: "anthropic",
    model: "claude",
    askPending: false,
    pendingEscalations: [],
    turns: [
      {
        id: "turn_1",
        status: "completed",
        items: [
          { id: "item_1", turnId: "turn_1", type: "userMessage", text: "please fix the bug", status: "completed" },
        ],
      },
    ],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    lastFrameAt: 0,
    capabilities: FULL_CAPABILITIES,
    goal: null,
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    ...rest,
    jobsTreeRevision,
  };
}

// wireThread is a fully-formed wire Thread (thread/clear and thread/fork
// both respond with one) - thread/clear's store action (stores/threads.ts)
// folds it straight through hydrateThread, so a malformed one here would
// throw inside that fold rather than exercising this component at all; the
// fork/aside handlers below only ever read `.serf.ref` back out, but the
// fake's return value is still type-checked against the real wire
// response shape, so every required field has to be present regardless.
function wireThread(overrides: Partial<Thread> = {}): Thread {
  return {
    id: "thr_a",
    sessionId: "sess_a",
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref: "ref_a", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

async function openMenu(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: /session actions/i }));
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  // Toasts are module state and outlive cleanup(); without this a toast from an
  // earlier test in this file is still on screen, and an assertion that a
  // message is ABSENT matches the stale one instead.
  resetToastStoreForTests();
  resetWorkspaceStoreForTests();
});

afterEach(() => {
  cleanup();
});

// --- menu contents + capability gating --------------------------------------

test("opens to show every session action", async () => {
  const user = userEvent.setup();
  render(<SessionActionsMenu sessionRef="ref_a" model={testModel()} />);
  await openMenu(user);

  // Fork is no longer here: it moved to a per-user-message affordance
  // (ForkFromHereButton, transcript/messages/UserMessageItem.tsx), where the
  // specific message being forked from IS the context - a session-chrome menu
  // item never had one. "Set goal…" is the entry point that replaced it.
  expect(screen.queryByRole("menuitem", { name: "Fork" })).toBeNull();
  expect(screen.getByRole("menuitem", { name: "Set goal…" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Aside" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Compact" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Clear" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Shut down" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Rename" })).toBeTruthy();
});

test("disables each action the thread's own capabilities say are unavailable", async () => {
  const user = userEvent.setup();
  const noCapabilities: ThreadCapabilities = {
    ...FULL_CAPABILITIES,
    compact: false,
    clear: false,
    shutdown: false,
    rename: false,
    forkFromTurn: false,
    goal: false,
  };
  render(<SessionActionsMenu sessionRef="ref_a" model={testModel({ capabilities: noCapabilities })} />);
  await openMenu(user);

  // Set goal… gates on the goal capability; Aside on forkFromTurn (it forks at
  // the tip). Fork itself is gone from this menu - see the test above.
  expect(screen.getByRole("menuitem", { name: "Set goal…" }).getAttribute("aria-disabled")).toBe("true");
  expect(screen.getByRole("menuitem", { name: "Aside" }).getAttribute("aria-disabled")).toBe("true");
  expect(screen.getByRole("menuitem", { name: "Compact" }).getAttribute("aria-disabled")).toBe("true");
  expect(screen.getByRole("menuitem", { name: "Clear" }).getAttribute("aria-disabled")).toBe("true");
  expect(screen.getByRole("menuitem", { name: "Shut down" }).getAttribute("aria-disabled")).toBe("true");
  expect(screen.getByRole("menuitem", { name: "Rename" }).getAttribute("aria-disabled")).toBe("true");
});

test("Set goal… fires the onSetGoal seam SessionChrome wires to the controlled goal dialog", async () => {
  const user = userEvent.setup();
  let opened = 0;
  render(<SessionActionsMenu sessionRef="ref_a" model={testModel()} onSetGoal={() => opened++} />);
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Set goal…" }));

  expect(opened).toBe(1);
});

// --- Compact (direct call, no confirmation) ---------------------------------

test("Compact calls the compact action directly with no confirmation dialog", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let called: unknown;
  fake.on("thread/compact/start", (params) => {
    called = params;
    return {};
  });

  render(<SessionActionsMenu sessionRef="ref_a" model={testModel()} />);
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Compact" }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a" }));
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("a failed Compact surfaces an error toast", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/compact/start", () => {
    throw new Error("compact boom");
  });

  render(
    <>
      <SessionActionsMenu sessionRef="ref_a" model={testModel()} />
      <Toast />
    </>,
  );
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Compact" }));

  await screen.findByText(/compact boom/i);
});

// --- Aside (direct call, opens the new pane on success) ---------------------

test("Aside forks at the tip and opens the new session as a pane", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let called: unknown;
  fake.on("thread/fork", (params) => {
    called = params;
    return {
      thread: wireThread({
        id: "child_1",
        sessionId: "child_1",
        source: "local",
        serf: { ref: "local/child_1", capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
      }),
    };
  });

  render(<SessionActionsMenu sessionRef="ref_a" model={testModel()} />);
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Aside" }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a", sourceTurnId: "", aside: true }));
  await waitFor(() => expect(workspaceStore.getState().panes.some((p) => p.type === "session")).toBe(true));
  expect(workspaceStore.getState().panes.find((p) => p.type === "session")?.params).toEqual({ ref: "local/child_1" });
});

test("a failed Aside surfaces an error toast and opens no pane", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/fork", () => {
    throw new Error("aside boom");
  });

  render(
    <>
      <SessionActionsMenu sessionRef="ref_a" model={testModel()} />
      <Toast />
    </>,
  );
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Aside" }));

  await screen.findByText(/aside boom/i);
  expect(workspaceStore.getState().panes).toHaveLength(0);
});

// --- Clear (destructive, confirms via Dialog) -------------------------------

test("Clear opens a confirmation dialog rather than acting immediately", async () => {
  const user = userEvent.setup();
  render(<SessionActionsMenu sessionRef="ref_a" model={testModel()} />);
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Clear" }));

  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).getByRole("heading", { name: /clear conversation/i })).toBeTruthy();
});

test("confirming Clear calls clearThread and closes the dialog on success", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let called: unknown;
  fake.on("thread/clear", (params) => {
    called = params;
    return { thread: wireThread(), ref: "ref_a" };
  });

  render(<SessionActionsMenu sessionRef="ref_a" model={testModel()} />);
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Clear" }));
  const dialog = await screen.findByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: /^clear$/i }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("cancelling Clear closes the dialog without calling the action", async () => {
  const user = userEvent.setup();
  render(<SessionActionsMenu sessionRef="ref_a" model={testModel()} />);
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Clear" }));
  const dialog = await screen.findByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: /cancel/i }));

  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("a failed Clear surfaces an error toast and leaves the dialog open", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/clear", () => {
    throw new Error("clear boom");
  });

  render(
    <>
      <SessionActionsMenu sessionRef="ref_a" model={testModel()} />
      <Toast />
    </>,
  );
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Clear" }));
  const dialog = await screen.findByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: /^clear$/i }));

  await screen.findByText(/clear boom/i);
  expect(screen.getByRole("dialog")).toBeTruthy();
});

// --- Shutdown (destructive, confirms via Dialog) ----------------------------

test("confirming Shut down calls shutdown and closes the dialog on success", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let called: unknown;
  fake.on("thread/shutdown", (params) => {
    called = params;
    return {};
  });

  render(<SessionActionsMenu sessionRef="ref_a" model={testModel()} />);
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Shut down" }));
  const dialog = await screen.findByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: /shut down/i }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

// --- Rename ------------------------------------------------------------------

test("Rename opens a dialog pre-filled with the current session name", async () => {
  const user = userEvent.setup();
  render(<SessionActionsMenu sessionRef="ref_a" model={testModel({ name: "Old name" })} />);
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Rename" }));

  const dialog = await screen.findByRole("dialog");
  expect((within(dialog).getByRole("textbox") as HTMLInputElement).value).toBe("Old name");
});

test("saving Rename calls rename with the trimmed value and closes on success", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let called: unknown;
  fake.on("serf/thread/name/set", (params) => {
    called = params;
    return {};
  });

  render(<SessionActionsMenu sessionRef="ref_a" model={testModel({ name: "Old name" })} />);
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Rename" }));
  const dialog = await screen.findByRole("dialog");
  const input = within(dialog).getByRole("textbox");
  await user.clear(input);
  await user.type(input, "  New name  ");
  await user.click(within(dialog).getByRole("button", { name: /save/i }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a", name: "New name" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("Save is disabled while the rename field is blank", async () => {
  const user = userEvent.setup();
  render(<SessionActionsMenu sessionRef="ref_a" model={testModel({ name: "Old name" })} />);
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Rename" }));
  const dialog = await screen.findByRole("dialog");
  const input = within(dialog).getByRole("textbox");
  await user.clear(input);
  await user.type(input, "   ");

  expect((within(dialog).getByRole("button", { name: /save/i }) as HTMLButtonElement).disabled).toBe(true);
});

test("a failed rename surfaces an error toast and leaves the dialog open", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/thread/name/set", () => {
    throw new Error("rename boom");
  });

  render(
    <>
      <SessionActionsMenu sessionRef="ref_a" model={testModel({ name: "Old name" })} />
      <Toast />
    </>,
  );
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Rename" }));
  const dialog = await screen.findByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: /save/i }));

  await screen.findByText(/rename boom/i);
  expect(screen.getByRole("dialog")).toBeTruthy();
});

// --- extraItems (kata vybn: SessionChrome's own collapsed Details/Tasks) ---

test("extraItems lead the menu, ahead of every built-in action", async () => {
  const user = userEvent.setup();
  render(
    <SessionActionsMenu
      sessionRef="ref_a"
      model={testModel()}
      extraItems={[
        { id: "details", label: "Details", onSelect: () => {} },
        { id: "tasks", label: "Tasks", onSelect: () => {} },
      ]}
    />,
  );
  await openMenu(user);

  const labels = screen.getAllByRole("menuitem").map((el) => el.textContent);
  expect(labels.slice(0, 2)).toEqual(["Details", "Tasks"]);
  expect(labels).toContain("Set goal…");
});

test("omitting extraItems leaves the menu exactly as every other caller sees it", async () => {
  const user = userEvent.setup();
  render(<SessionActionsMenu sessionRef="ref_a" model={testModel()} />);
  await openMenu(user);

  expect(screen.queryByRole("menuitem", { name: "Details" })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: "Tasks" })).toBeNull();
});

// Fork is intentionally NOT in this menu (see "opens to show every session
// action" above): it moved to the per-user-message ForkFromHereButton
// affordance, whose behavior is covered by UserMessageItem.test.tsx's own
// "per-message fork affordance" block - a materially different flow
// (deferInput + composer draft-seed, no in-menu dialog).

// Every action in this menu resumes a cold session first (cmd/serf-hub/
// app_session_resume.go). A failed resume is not a failed compact.
test("a Compact that fails because the session would not start names the start, not the compact", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/compact/start", () => {
    throw new WireError("serf launch-check failed: exit status 1", -32014, { serfErrorInfo: "hubLaunch" });
  });

  render(
    <>
      <SessionActionsMenu sessionRef="ref_a" model={testModel()} />
      <Toast />
    </>,
  );
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Compact" }));

  await screen.findByText("Couldn't start this session: serf launch-check failed: exit status 1");
  expect(screen.queryByText(/couldn't compact/i)).toBeNull();
});
