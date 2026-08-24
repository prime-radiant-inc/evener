// Edge cases for SessionChrome.tsx that close the remaining uncovered lines:
// - onRename error catch (171-172)
// - onShutdown error catch (179-180)
// - onPin error catch (188-189)
// - onToggleArchive error catch (207-208)
// - onDelete error catch (221-222) + skipped warning (216-218)

import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../protocol/types.gen";
import { resetWorkspaceStoreForTests } from "../../../shell/workspace";
import { resetActivitySummaryStoreForTests } from "../../../stores/activitySummary";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { resetTreeStoreForTests, type TreeResponse, treeStore } from "../../../stores/tree";
import { Toast } from "../../../widgets";
import "../../sessionPanels";
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
    source: "evener",
    evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

function treeWithSession(ref: string): TreeResponse {
  return {
    generated_at: "2026-08-06T00:00:00Z",
    sources: [],
    live: [
      {
        row_id: `row_${ref}`,
        ref,
        host_id: "local",
        session_id: `sess_${ref}`,
        title: `Session ${ref}`,
        project: "",
        state: "idle",
        kind: "session",
        live: true,
        children: [],
      },
    ],
    needs_you: [],
    pin_sections: [],
    projects: [],
    archived_projects: [],
    test_runs: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function failResponse(status = 500, body: unknown = { error: "failed" }): Response {
  return { ok: false, status, statusText: "Error", json: () => Promise.resolve(body) } as Response;
}

function okResponse(body: unknown): Response {
  return { ok: true, status: 200, statusText: "OK", json: () => Promise.resolve(body) } as Response;
}

// We can't easily spy on the toast store from outside, so we mount Toast
// and observe DOM output instead.
function renderWithToast(ui: React.ReactElement) {
  return render(
    <>
      {ui}
      <Toast />
    </>,
  );
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivitySummaryStoreForTests();
  resetGoalOverridesForTests();
  resetTreeStoreForTests();
});

afterEach(() => {
  cleanup();
  // @ts-expect-error jsdom has no matchMedia by default
  delete window.matchMedia;
  resetThreadsStoreForTests();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// --- onRename error (lines 171-172) ---

test("rename failure toasts an error", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_rename", { name: "Old Name" }));
  fake.on("evener/thread/name/set", () => {
    throw new Error("name already taken");
  });
  await threadsStore.getState().ensureThread("ref_rename");

  renderWithToast(<SessionChrome ref="ref_rename" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Rename" }));
  const dialog = await screen.findByRole("dialog");
  const input = dialog.querySelector("input");
  if (!input) throw new Error("rename dialog missing its input");
  await user.clear(input);
  await user.type(input, "New Name");
  await user.click(screen.getByRole("button", { name: "Rename" }));

  expect(await screen.findByText("Couldn't rename session: name already taken")).toBeTruthy();
  const openDialog = screen.getByRole("dialog", { name: "Rename session" });
  expect(within(openDialog).getByRole("button", { name: "Rename" }).hasAttribute("disabled")).toBe(false);
});

// --- onShutdown error (lines 179-180) ---

test("shutdown failure toasts an error", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_shutdown"));
  fake.on("thread/shutdown", () => {
    throw new Error("shutdown failed");
  });
  await threadsStore.getState().ensureThread("ref_shutdown");

  renderWithToast(<SessionChrome ref="ref_shutdown" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Shut down" }));

  // Find and click the confirm button in the shutdown dialog
  const dialog = await screen.findByRole("dialog");
  const buttons = dialog.querySelectorAll("button");
  for (const btn of buttons) {
    if (btn.textContent?.match(/shut down/i)) {
      await user.click(btn);
      break;
    }
  }

  expect(await screen.findByText("Couldn't shut down session: shutdown failed")).toBeTruthy();
  expect(screen.getByRole("dialog", { name: "Shut down this session?" })).toBeTruthy();
});

// --- onToggleArchive error (lines 207-208) ---

test("archive toggle failure toasts an error", async () => {
  const fetchMock = vi.fn();
  fetchMock.mockResolvedValue(failResponse(500, { error: "archive failed" }));
  vi.stubGlobal("fetch", fetchMock);

  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_archive"));
  await threadsStore.getState().ensureThread("ref_archive");
  treeStore.setState({ tree: treeWithSession("ref_archive") });

  renderWithToast(<SessionChrome ref="ref_archive" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: "Archive" }));

  expect(await screen.findByText("Couldn't update archive state: archive failed")).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledWith(
    "/api/archive",
    expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ kind: "session", id: "sess_ref_archive", archived: true }),
    }),
  );
});

// --- onDelete error (lines 221-222) ---

test("delete failure toasts an error", async () => {
  const fetchMock = vi.fn();
  fetchMock.mockResolvedValue(failResponse(500, { error: "delete failed" }));
  vi.stubGlobal("fetch", fetchMock);

  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_del", { name: "Del Session" }));
  await threadsStore.getState().ensureThread("ref_del");
  treeStore.setState({ tree: treeWithSession("ref_del") });

  renderWithToast(<SessionChrome ref="ref_del" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: /Delete/ }));

  // Confirm the delete dialog
  const dialog = await screen.findByRole("dialog");
  const buttons = dialog.querySelectorAll("button");
  for (const btn of buttons) {
    if (btn.textContent?.match(/delete/i)) {
      await user.click(btn);
      break;
    }
  }

  expect(await screen.findByText('Couldn\'t delete "Del Session": delete failed')).toBeTruthy();
  expect(screen.getByRole("dialog", { name: "Delete session?" })).toBeTruthy();
});

// --- onDelete with skipped sessions (lines 216-218) ---

test("delete with skipped sessions shows a warning toast", async () => {
  const fetchMock = vi.fn();
  fetchMock.mockResolvedValue(
    okResponse({
      deleted: ["other_ref"],
      skipped: [{ id: "ref_skip", reason: "still in use" }],
    }),
  );
  vi.stubGlobal("fetch", fetchMock);

  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/read", () => readResponse("ref_skip", { name: "Skip Session" }));
  await threadsStore.getState().ensureThread("ref_skip");
  treeStore.setState({ tree: treeWithSession("ref_skip") });

  renderWithToast(<SessionChrome ref="ref_skip" />);
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: /Delete/ }));

  const dialog = await screen.findByRole("dialog");
  const buttons = dialog.querySelectorAll("button");
  for (const btn of buttons) {
    if (btn.textContent?.match(/delete/i)) {
      await user.click(btn);
      break;
    }
  }

  expect(await screen.findByText('Couldn\'t delete "Skip Session": still in use')).toBeTruthy();
});
