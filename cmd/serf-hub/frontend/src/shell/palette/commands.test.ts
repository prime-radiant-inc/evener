import { lazy } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import type { ThreadModel } from "../../protocol/model";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities } from "../../protocol/types.gen";
import { connectionStore } from "../../stores/connection";
import { prefsStore, resetPrefsStoreForTests } from "../../stores/prefs";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { registerPane } from "../paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { blockedMessage, isBlocked } from "./blocked";
import {
  buildCommands,
  type Command,
  commandsInScope,
  copyToClipboard,
  filterCommands,
  type PaletteRunContext,
  splitModelId,
} from "./commands";
import { buildPaletteContext } from "./paletteContext";
import { RECENT_COMMANDS_KEY } from "./recentCommands";

// /project calls the rail's imperative reveal seam (PIN-A); T5 produces the
// real body, so this stream tests against a mock of the SEAM only.
vi.mock("../rail/railController", () => ({ revealSessionInRail: vi.fn() }));

import { revealSessionInRail } from "../rail/railController";

// Minimal test-only "session" pane registration so /aside's openPane hop has
// a real registry entry - mirrors SessionActionsMenu.test.tsx's setup.
registerPane({
  id: "session",
  title: () => "test session",
  component: lazy(() => Promise.resolve({ default: () => null })),
});

// See stores/prefs.test.ts: Node 26 shadows jsdom's localStorage with a
// non-functional global, so every localStorage-touching test file needs this.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

const CAPS: ThreadCapabilities = {
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
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "",
    status: { type: "idle" },
    modelProvider: "anthropic",
    model: "claude",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    lastFrameAt: 0,
    capabilities: CAPS,
    goal: null,
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    ...overrides,
  };
}

function wireThread(ref: string): Thread {
  return {
    id: "thr_child",
    sessionId: "sess_child",
    preview: "",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/p",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref, capabilities: CAPS, queue: {} },
  };
}

function connectFake(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function focusSession(ref: string): void {
  workspaceStore.setState({ panes: [{ id: "p1", type: "session", params: { ref } }], focusedPaneId: "p1" });
}

function seedModel(ref: string, overrides: Partial<ThreadModel> = {}): void {
  threadsStore.setState({ threads: new Map([[ref, testModel({ ref, ...overrides })]]) });
}

const pushes: Array<{ kind: string; text: string }> = [];
function runContext(overrides: Partial<PaletteRunContext> = {}): PaletteRunContext {
  return {
    ...buildPaletteContext(),
    toasts: { push: (kind, text) => pushes.push({ kind, text }) },
    ui: { clearToSearch: vi.fn(), showHelp: vi.fn() },
    ...overrides,
  };
}

function cmd(id: string): Command {
  const found = buildCommands().find((c) => c.id === id);
  if (!found) throw new Error(`no command "${id}"`);
  return found;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  resetPrefsStoreForTests();
  localStorage.clear();
  window.history.pushState({}, "", "/");
  pushes.length = 0;
  vi.mocked(revealSessionInRail).mockClear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

// --- scope gating (search.js:581-588) ---

test("with no focused session, only the 8 global commands are in scope", () => {
  const inScope = commandsInScope(buildPaletteContext());
  expect(inScope.every((c) => c.scope === "global")).toBe(true);
  expect(inScope).toHaveLength(8);
});

test("a live focused session exposes global + session + ended-ok (all 23 commands)", () => {
  focusSession("ref_a");
  seedModel("ref_a", { status: { type: "active" }, activeTurnId: "t1" });
  const inScope = commandsInScope(buildPaletteContext());
  expect(inScope).toHaveLength(23);
  expect(inScope.some((c) => c.id === "steer")).toBe(true);
  expect(inScope.some((c) => c.id === "project")).toBe(true);
});

test("an ended focused session drops the live-only session commands, keeping global + ended-ok", () => {
  focusSession("ref_a");
  seedModel("ref_a", { status: { type: "ended" } });
  const inScope = commandsInScope(buildPaletteContext());
  expect(inScope.map((c) => c.id)).not.toContain("steer");
  expect(inScope.map((c) => c.id)).toContain("copy-id");
  expect(inScope).toHaveLength(12); // 8 global + 4 ended-ok
});

// --- filterCommands (search.js:637-651) ---

test("filterCommands with an empty filter surfaces recents above, excluded from the main list", () => {
  localStorage.setItem(RECENT_COMMANDS_KEY, JSON.stringify(["settings"]));
  const { recent, commands } = filterCommands(buildPaletteContext(), "/");
  expect(recent.map((c) => c.id)).toEqual(["settings"]);
  expect(commands.map((c) => c.id)).not.toContain("settings");
});

test("filterCommands with a non-empty filter ranks by score and excludes non-matches", () => {
  const { recent, commands } = filterCommands(buildPaletteContext(), "/set");
  expect(recent).toEqual([]);
  expect(commands[0]?.id).toBe("settings"); // exact substring wins
  expect(commands.map((c) => c.id)).not.toContain("new"); // no subsequence match
});

// --- navigation (search.js:330-341) ---

test("/new navigates to the spawn route", () => {
  cmd("new").run?.(runContext());
  expect(window.location.pathname).toBe("/new");
});

test("/spawn navigates to /new with the URL-encoded prompt", () => {
  const c = cmd("spawn");
  if (c.args?.kind !== "free") throw new Error("expected free args");
  c.args.run(runContext(), "fix the bug");
  expect(window.location.pathname).toBe("/new");
  expect(window.location.search).toBe("?prompt=fix%20the%20bug");
});

// --- /theme: the hazard-#1 FIX (§4.4) ---

test("/theme applies the theme immediately via prefsStore.setTheme", () => {
  const c = cmd("theme");
  if (c.args?.kind !== "enum") throw new Error("expected enum args");
  expect(c.args.source(runContext())).toEqual([
    { id: "dark", label: "Dark" },
    { id: "light", label: "Light" },
  ]);
  c.args.run(runContext(), { id: "light", label: "Light" });
  expect(prefsStore.getState().theme).toBe("light");
  expect(document.documentElement.getAttribute("data-theme")).toBe("light");
});

// --- idle guards: blocked sentinel, no wire call (§2.5) ---

test("/steer is blocked with the floor's message and makes no wire call when there is no active turn", () => {
  const fake = connectFake();
  focusSession("ref_a");
  seedModel("ref_a", { status: { type: "idle" }, activeTurnId: undefined });
  const c = cmd("steer");
  if (c.args?.kind !== "free") throw new Error("expected free args");
  const result = c.args.run(runContext(), "go left");
  expect(isBlocked(result)).toBe(true);
  expect(blockedMessage(result)).toBe("steer failed: no active turn");
  expect(fake.calls.some((call) => call.method === "turn/steer")).toBe(false);
});

test("/steer sends turn/steer when a turn is active", async () => {
  const fake = connectFake();
  fake.on("turn/steer", () => ({}));
  focusSession("ref_a");
  seedModel("ref_a", { status: { type: "active" }, activeTurnId: "t1" });
  const c = cmd("steer");
  if (c.args?.kind !== "free") throw new Error("expected free args");
  await c.args.run(runContext(), "go left");
  const call = fake.calls.find((call) => call.method === "turn/steer");
  expect(call?.params).toMatchObject({ ref: "ref_a", input: [{ type: "text", text: "go left" }] });
});

test("/interrupt, /queue, /drain-as-steer each block with their own no-active-turn message", () => {
  focusSession("ref_a");
  seedModel("ref_a", { status: { type: "idle" }, activeTurnId: undefined });
  const q = cmd("queue");
  if (q.args?.kind !== "free") throw new Error("expected free args");
  expect(blockedMessage(cmd("interrupt").run?.(runContext()))).toBe("interrupt failed: no active turn");
  expect(blockedMessage(q.args.run(runContext(), "later"))).toBe("queue failed: no active turn");
  expect(blockedMessage(cmd("drain-as-steer").run?.(runContext()))).toBe("drain failed: no active turn");
});

// --- /model: busy guard + provider/model split + toast ---

test("/model is blocked while a turn is in progress", () => {
  focusSession("ref_a");
  seedModel("ref_a", { status: { type: "active" }, activeTurnId: "t1" });
  const c = cmd("model");
  if (c.args?.kind !== "enum") throw new Error("expected enum args");
  const result = c.args.run(runContext(), { id: "openai/gpt-5.5", label: "gpt-5.5" });
  expect(blockedMessage(result)).toBe("model change failed: turn in progress");
});

test("/model source lists models and run sets the split provider/model with a success toast", async () => {
  const fake = connectFake();
  fake.on("model/list", () => ({ data: [{ provider: "openai", model: "gpt-5.5" }] }));
  fake.on("thread/model/set", () => ({}));
  focusSession("ref_a");
  seedModel("ref_a", { status: { type: "idle" } });
  const c = cmd("model");
  if (c.args?.kind !== "enum") throw new Error("expected enum args");
  expect(await c.args.source(runContext())).toEqual([{ id: "openai/gpt-5.5", label: "gpt-5.5", hint: "openai" }]);
  await c.args.run(runContext(), { id: "openai/gpt-5.5", label: "gpt-5.5" });
  const call = fake.calls.find((call) => call.method === "thread/model/set");
  expect(call?.params).toMatchObject({ ref: "ref_a", modelProvider: "openai", model: "gpt-5.5" });
  expect(pushes).toContainEqual({ kind: "success", text: "Model: openai/gpt-5.5" });
});

test("splitModelId splits on the first slash so a model id with slashes survives", () => {
  expect(splitModelId("openai/gpt-5.5")).toEqual({ provider: "openai", model: "gpt-5.5" });
  expect(splitModelId("anthropic/claude-sonnet-4-5")).toEqual({ provider: "anthropic", model: "claude-sonnet-4-5" });
});

// --- /reasoning-effort: snapshot source + set + toast ---

test("/reasoning-effort source prefixes (default) and omits 'none'; run sets the effort", async () => {
  const fake = connectFake();
  fake.on("thread/reasoning-effort/set", () => ({}));
  focusSession("ref_a");
  seedModel("ref_a", { supportsReasoning: true, reasoningEffortLevels: ["none", "low", "high"] });
  const c = cmd("reasoning-effort");
  if (c.args?.kind !== "enum") throw new Error("expected enum args");
  expect(c.args.source(runContext())).toEqual([
    { id: "", label: "(default)" },
    { id: "low", label: "low" },
    { id: "high", label: "high" },
  ]);
  await c.args.run(runContext(), { id: "high", label: "high" });
  expect(fake.calls.find((call) => call.method === "thread/reasoning-effort/set")?.params).toMatchObject({
    ref: "ref_a",
    reasoningEffort: "high",
  });
  expect(pushes).toContainEqual({ kind: "success", text: "Effort: high" });
});

test("/reasoning-effort offers zero options for a non-reasoning model", () => {
  focusSession("ref_a");
  seedModel("ref_a", { supportsReasoning: false, reasoningEffortLevels: [] });
  const c = cmd("reasoning-effort");
  if (c.args?.kind !== "enum") throw new Error("expected enum args");
  expect(c.args.source(runContext())).toEqual([]);
});

// --- fire-and-report session actions ---

test("/goal sets the trimmed objective", async () => {
  const fake = connectFake();
  fake.on("goal/set", () => ({ started: true }));
  focusSession("ref_a");
  seedModel("ref_a");
  const c = cmd("goal");
  if (c.args?.kind !== "free") throw new Error("expected free args");
  await c.args.run(runContext(), "  ship it  ");
  expect(fake.calls.find((call) => call.method === "goal/set")?.params).toMatchObject({
    ref: "ref_a",
    objective: "ship it",
  });
});

test("/shutdown shuts the session down and toasts success", async () => {
  const fake = connectFake();
  fake.on("thread/shutdown", () => ({}));
  focusSession("ref_a");
  seedModel("ref_a");
  await cmd("shutdown").run?.(runContext());
  expect(fake.calls.some((call) => call.method === "thread/shutdown")).toBe(true);
  expect(pushes).toContainEqual({ kind: "success", text: "Session shut down" });
});

test("/aside forks the session and opens the child as a new pane", async () => {
  const fake = connectFake();
  fake.on("thread/fork", (params) => {
    expect(params).toMatchObject({ ref: "ref_a", aside: true });
    return { thread: wireThread("local:child") };
  });
  focusSession("ref_a");
  seedModel("ref_a");
  await cmd("aside").run?.(runContext());
  expect(
    workspaceStore
      .getState()
      .panes.find((p) => p.type === "session" && JSON.stringify(p.params) === JSON.stringify({ ref: "local:child" })),
  ).toBeTruthy();
});

// --- /upgrade ---

test("/upgrade calls serf/upgrade and toasts success + restart message", async () => {
  const fake = connectFake();
  fake.on("serf/upgrade", () => ({
    release: "1.2.3",
    channel: "stable",
    url: "",
    archive: "",
    prefix: "",
    binDir: "",
    shareBinDir: "",
    installed: [],
    restartMessage: "restart your shell",
  }));
  await cmd("upgrade").run?.(runContext());
  expect(fake.calls.some((call) => call.method === "serf/upgrade")).toBe(true);
  expect(pushes).toContainEqual({ kind: "success", text: "Serf upgraded to stable" });
  expect(pushes).toContainEqual({ kind: "info", text: "restart your shell" });
});

// --- /project, /copy-id, /tasks, /status ---

test("/project reveals the focused session in the rail via the seam", () => {
  focusSession("ref_a");
  seedModel("ref_a");
  cmd("project").run?.(runContext());
  expect(vi.mocked(revealSessionInRail)).toHaveBeenCalledWith("ref_a");
});

test("/copy-id copies the session ref to the clipboard", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
  focusSession("ref_a");
  seedModel("ref_a");
  cmd("copy-id").run?.(runContext());
  await Promise.resolve();
  expect(writeText).toHaveBeenCalledWith("ref_a");
});

test("copyToClipboard prefers the async Clipboard API", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
  await copyToClipboard("hello");
  expect(writeText).toHaveBeenCalledWith("hello");
});

test("/tasks and /status click their chrome triggers when present (no-op-safe when absent)", () => {
  const tasksBtn = document.createElement("button");
  tasksBtn.setAttribute("data-tasks-trigger", "");
  const tasksClick = vi.fn();
  tasksBtn.addEventListener("click", tasksClick);
  const detailsBtn = document.createElement("button");
  detailsBtn.setAttribute("data-details-trigger", "");
  const detailsClick = vi.fn();
  detailsBtn.addEventListener("click", detailsClick);
  document.body.append(tasksBtn, detailsBtn);

  focusSession("ref_a");
  seedModel("ref_a");
  cmd("tasks").run?.(runContext());
  expect(tasksClick).toHaveBeenCalledTimes(1);
  cmd("status").run?.(runContext());
  expect(detailsClick).toHaveBeenCalledTimes(1);
  tasksBtn.remove();
  detailsBtn.remove();

  // Neither command may throw with no trigger in the DOM - a pane type that
  // renders no session chrome (a doc pane, the transcript view) is a real
  // state, and the palette's own scope gating is by ref, not by chrome.
  expect(() => cmd("tasks").run?.(runContext())).not.toThrow();
  expect(() => cmd("status").run?.(runContext())).not.toThrow();
});
