import { IDBFactory } from "fake-indexeddb";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import type { ThreadModel } from "../../protocol/model";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities } from "../../protocol/types.gen";
import "../../panes/sessionPanels";
import { useCommandCatalog } from "../../stores/commandCatalog";
import { connectionStore } from "../../stores/connection";
import { prefsStore, resetPrefsStoreForTests } from "../../stores/prefs";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { resetTreeStoreForTests, treeStore } from "../../stores/tree";
import { registerPaneForTests } from "../paneRegistry";
import * as railController from "../rail/railController";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { blockedMessage, isBlocked } from "./blocked";
import {
  buildCommands,
  type Command,
  commandSurface,
  commandsInScope,
  copyToClipboard,
  filterCommands,
  type PaletteRunContext,
  sessionBuiltinCommands,
  sessionScopedHandoffMatch,
  splitModelId,
  UNAVAILABLE_REASON,
} from "./commands";
import { buildPaletteContext } from "./paletteContext";
import { RECENT_COMMANDS_KEY } from "./recentCommands";

// /project calls the rail's imperative reveal seam (PIN-A); T5 produces the
// real body, so this stream tests against a stub of the SEAM only.
//
// A hoisted vi.mock("../rail/railController", () => ({ revealSessionInRail:
// vi.fn() })) used to sit here, replacing the WHOLE module (dropping
// setRailRevealHandler entirely) in the shared module registry - under
// isolate:false that registry is shared by every file in the worker, so this
// would poison every other file that imports railController.ts (Rail.test.tsx,
// RailHost.test.tsx, railController.test.ts) for the rest of the worker's
// life, not just while this file's own tests run. vi.spyOn mutates only the
// one property this file cares about, on the SAME shared module object every
// other file also reads from, and mockRestore() in afterAll hands the real
// revealSessionInRail back for whatever file runs next.
//
// Re-spied in beforeEach below, not just once here: this file's own afterEach
// (like several sibling rail test files) calls vi.restoreAllMocks(), which is
// a GLOBAL operation - it un-does this spy (handing the real
// revealSessionInRail back onto the shared module object) the moment ANY
// test anywhere in the worker restores mocks, not just this file's own. A
// one-time spy at module scope would silently stop taking effect after that.
let revealSessionInRail = vi.spyOn(railController, "revealSessionInRail").mockImplementation(() => {});
afterAll(() => {
  revealSessionInRail.mockRestore();
});

// Minimal test-only "session" pane registration so /aside's openPane hop has
// a real registry entry - mirrors SessionActionsMenu.test.tsx's setup.
afterAll(
  registerPaneForTests({
    id: "session",
    title: () => "test session",
    component: lazy(() => Promise.resolve({ default: () => null })),
  }),
);

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

const NO_CAPS: ThreadCapabilities = {
  send: false,
  steer: false,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  queue: false,
  goal: false,
  rename: false,
};

function testModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
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
    jobsUpdatedAt: null,
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
    ...rest,
    jobsTreeRevision,
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
    serf: { ref, capabilities: CAPS, queue: { revision: 0 } },
  };
}

function connectFake(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function focusSession(ref: string): void {
  workspaceStore.setState({
    panes: [{ id: "p1", type: "session", params: { ref }, slot: "main" }],
    focusedPaneId: "p1",
  });
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
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  useCommandCatalog.setState({ commands: [], loaded: false });
  resetWorkspaceStoreForTests();
  resetPrefsStoreForTests();
  resetTreeStoreForTests();
  localStorage.clear();
  window.history.pushState({}, "", "/");
  pushes.length = 0;
  // Keep viewport tests isolated from direct matchMedia assignments.
  // @ts-expect-error jsdom baseline has no matchMedia.
  delete window.matchMedia;
  // vi.restoreAllMocks() in this file's own afterEach (or any other test
  // file's, sharing this worker) strips the spy - see this file's own
  // comment on the vi.spyOn call above.
  revealSessionInRail = vi.spyOn(railController, "revealSessionInRail").mockImplementation(() => {});
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  // Direct assignments are not spies, so restoreAllMocks cannot remove the
  // mobile viewport fake from the final test in this shared worker.
  // @ts-expect-error jsdom baseline has no matchMedia.
  delete window.matchMedia;
  // Every test here writes real durable outbox records into this file's own
  // globalThis.indexedDB instance - the beforeEach above only replaces it
  // BEFORE each test, so whatever the LAST test wrote stays installed as the
  // global indexedDB after this file finishes. Under isolate:false that
  // leftover, populated database is what a later file's own default
  // getMutationRuntime() (no setMutationStorageForTests override) discovers
  // and re-pins.
  globalThis.indexedDB = new IDBFactory();
});

// --- scope gating (search.js:581-588) ---

test("with no focused session, only the 9 global commands are in scope", () => {
  const inScope = commandsInScope(buildPaletteContext());
  expect(inScope.every((c) => c.scope === "global")).toBe(true);
  expect(inScope).toHaveLength(9);
});

test("a live focused session exposes global + session (all 24 commands)", () => {
  focusSession("ref_a");
  seedModel("ref_a", { status: { type: "active" }, activeTurnId: "t1" });
  const inScope = commandsInScope(buildPaletteContext());
  expect(inScope).toHaveLength(24);
  expect(inScope.some((c) => c.id === "steer")).toBe(true);
  expect(inScope.some((c) => c.id === "project")).toBe(true);
});

// The kata-zshh ruling: an ended session keeps the whole registry. The hub
// advertises ChangeModel/Send/Compact/Clear/Shutdown/Goal/Rename for a cold
// exited thread (app_threadread.go's pastEntryThread) and resumes it behind
// the call, so the palette must not decide for it.
test("an ended focused session still lists every command; only the wire's own false flags disable one", () => {
  focusSession("ref_a");
  seedModel("ref_a", {
    status: { type: "ended" },
    // Exactly pastEntryThread's advertisement for a cold exited thread.
    capabilities: { ...CAPS, steer: false, interrupt: false, queue: false },
  });
  const inScope = commandsInScope(buildPaletteContext());
  expect(inScope).toHaveLength(24);
  const byId = new Map(inScope.map((c) => [c.id, c]));
  for (const id of ["model", "goal", "clear", "compact", "aside", "shutdown", "copy-id"]) {
    expect(byId.get(id)?.unavailableReason).toBeUndefined();
  }
  for (const id of ["steer", "interrupt", "queue", "drain-as-steer"]) {
    expect(byId.get(id)?.unavailableReason).toBe(UNAVAILABLE_REASON);
  }
});

// The mechanism assertion: flip ONLY the capability flag and the verdict
// flips with it, in both directions, with session liveness held constant.
// A test that merely asserted "/model appears on an ended session" would
// pass just as well against a status-based gate.
test("the wire capability flag, not session liveness, decides availability", () => {
  focusSession("ref_a");
  const reasonFor = (id: string) => commandsInScope(buildPaletteContext()).find((c) => c.id === id)?.unavailableReason;

  // Live and idle, but the wire says no: unavailable.
  seedModel("ref_a", { status: { type: "idle" }, capabilities: { ...CAPS, compact: false } });
  expect(reasonFor("compact")).toBe(UNAVAILABLE_REASON);

  // Ended, but the wire says yes: available.
  seedModel("ref_a", { status: { type: "ended" }, capabilities: { ...CAPS, compact: true } });
  expect(reasonFor("compact")).toBeUndefined();
});

test("a session command with no wire capability is never capability-gated", () => {
  focusSession("ref_a");
  // Every flag off. /reasoning-effort has no ThreadCapabilities field (the hub
  // gates it on nothing either - app_rpc.go's "No capability gate" comment),
  // so it must stay enabled while its capability-backed siblings do not.
  seedModel("ref_a", {
    status: { type: "ended" },
    capabilities: NO_CAPS,
  });
  const byId = new Map(commandsInScope(buildPaletteContext()).map((c) => [c.id, c]));
  expect(byId.get("reasoning-effort")?.unavailableReason).toBeUndefined();
  expect(byId.get("clear")?.unavailableReason).toBe(UNAVAILABLE_REASON);
});

test("a focused session whose model has not hydrated yet leaves every command enabled", () => {
  focusSession("ref_a");
  const inScope = commandsInScope(buildPaletteContext());
  expect(inScope).toHaveLength(24);
  expect(inScope.every((c) => c.unavailableReason === undefined)).toBe(true);
});

// 2026-08-14: filterCommands is the palette's OWN browsable list, and it is
// app-global only now (commandSurface's own doc comment) - a plugin catalog
// entry is session-scoped (catalogCommands sets scope: "session"), so it
// never appears there, focused session or not. commandsInScope (the fuller
// resolver, still blending everything) is what sessionBuiltinCommands and
// this file's OTHER catalog-shaped tests below exercise; slashCommandInvocation
// itself is covered directly by slashCompletion.test.ts's mergeSlashCommands
// cases and Composer.test.tsx's own qualified-invocation test.
test("filterCommands never lists a plugin catalog entry, focused session or not", () => {
  useCommandCatalog.setState({
    commands: [{ name: "review", pluginName: "p", description: "plugin cmd", source: "plugin" }],
    loaded: true,
  });
  expect(filterCommands(buildPaletteContext(), "/rev").commands.some((c) => c.id === "review")).toBe(false);

  focusSession("ref_a");
  expect(filterCommands(buildPaletteContext(), "/rev").commands.some((c) => c.id === "review")).toBe(false);
});

test("catalog commands are absent from commandsInScope without a focused session", () => {
  useCommandCatalog.setState({
    commands: [{ name: "review", pluginName: "p", description: "plugin cmd", source: "plugin" }],
    loaded: true,
  });

  expect(commandsInScope(buildPaletteContext()).some((command) => command.id === "review")).toBe(false);
});

// --- commandSurface / sessionBuiltinCommands / sessionScopedHandoffMatch
// (2026-08-14, "the palette is where you go; the composer is where you act
// on this session" - decisions.md) ---

test("commandSurface maps every registry command honestly: session scope -> session surface, global -> app-global", () => {
  for (const command of buildCommands()) {
    expect(commandSurface(command)).toBe(command.scope === "session" ? "session" : "app-global");
  }
});

test("sessionBuiltinCommands is every session-scoped BUILT-IN, unavailableReason-resolved, never a plugin-catalog entry", () => {
  focusSession("ref_a");
  useCommandCatalog.setState({
    commands: [{ name: "review", pluginName: "p", description: "plugin cmd", source: "plugin" }],
    loaded: true,
  });
  seedModel("ref_a", { capabilities: { ...CAPS, compact: false } });

  const builtins = sessionBuiltinCommands(buildPaletteContext());
  expect(builtins.every((c) => c.scope === "session")).toBe(true);
  expect(builtins.some((c) => c.id === "review")).toBe(false); // the catalog entry never leaks in
  const ids = builtins.map((c) => c.id);
  expect(ids).toEqual(
    expect.arrayContaining([
      "compact",
      "interrupt",
      "clear",
      "aside",
      "shutdown",
      "model",
      "reasoning-effort",
      "steer",
      "queue",
      "goal",
      "drain-as-steer",
      "copy-id",
      "tasks",
      "status",
      "project",
    ]),
  );
  expect(builtins.find((c) => c.id === "compact")?.unavailableReason).toBe(UNAVAILABLE_REASON);
});

test("sessionScopedHandoffMatch matches a built-in id prefix, with or without a focused session", () => {
  expect(sessionScopedHandoffMatch("/go", [])).toBe(true); // prefixes "goal"
  expect(sessionScopedHandoffMatch("/goal fix the bug", [])).toBe(true); // args after the name still match on the first token
  expect(sessionScopedHandoffMatch("/zzz", [])).toBe(false);
  expect(sessionScopedHandoffMatch("/", [])).toBe(false); // empty first token: nothing to hand off yet
  expect(sessionScopedHandoffMatch("", [])).toBe(false);
});

test("sessionScopedHandoffMatch also matches a plugin catalog entry's name", () => {
  const catalog = [{ name: "review", description: "plugin cmd", source: "plugin" as const }];
  expect(sessionScopedHandoffMatch("/rev", catalog)).toBe(true);
  expect(sessionScopedHandoffMatch("/nomatch", catalog)).toBe(false);
});

test("sessionScopedHandoffMatch never matches an app-global command (those stay palette-native)", () => {
  expect(sessionScopedHandoffMatch("/settings", [])).toBe(false);
  expect(sessionScopedHandoffMatch("/theme", [])).toBe(false);
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
  fake.on("turn/steer", (params) => ({
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: "thread_a",
      projectionState: "reflected",
    },
  }));
  focusSession("ref_a");
  seedModel("ref_a", { status: { type: "active" }, activeTurnId: "t1" });
  const c = cmd("steer");
  if (c.args?.kind !== "free") throw new Error("expected free args");
  await c.args.run(runContext(), "go left");
  await vi.waitFor(() => expect(fake.calls.some((call) => call.method === "turn/steer")).toBe(true));
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

// --- /model: no client-side turn guess + provider/model split + toast ---

// The palette used to answer "is a turn in flight" itself and refuse. Only the
// daemon knows: it answers Conflict for a genuine mid-turn switch
// (server/appwire_runtime.go's handleAppThreadModelSet) and resumes a cold
// session for everyone else (app_model.go's setThreadModelWithResume).
test("/model sends thread/model/set even while a turn is in progress, letting the daemon rule", async () => {
  const fake = connectFake();
  fake.on("thread/model/set", () => ({}));
  focusSession("ref_a");
  seedModel("ref_a", { status: { type: "active" }, activeTurnId: "t1" });
  const c = cmd("model");
  if (c.args?.kind !== "enum") throw new Error("expected enum args");
  const result = c.args.run(runContext(), { id: "openai/gpt-5.5", label: "gpt-5.5" });
  expect(isBlocked(result)).toBe(false);
  await result;
  expect(fake.calls.some((call) => call.method === "thread/model/set")).toBe(true);
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

// UX fix: /model's enum source used to build its list from the bare
// model/list result alone (id + provider hint, no display name). It now
// merges in the same rich /api/models catalog ModelSwitch.tsx's own
// mid-session picker already uses (mergeScopedCatalog), so a model with a
// human display name shows it here too.
test("/model source enriches the scoped list with the rich catalog's display names (mergeScopedCatalog)", async () => {
  const fake = connectFake();
  fake.on("model/list", () => ({ data: [{ provider: "openai", model: "gpt-5.5" }] }));
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({
      ok: true,
      status: 200,
      statusText: "OK",
      json: async () => ({
        models: [{ provider: "openai", model: "gpt-5.5", display_name: "GPT-5.5" }],
        recent: [],
        diagnostics: [],
      }),
    })),
  );
  focusSession("ref_a");
  seedModel("ref_a", { status: { type: "idle" } });
  const c = cmd("model");
  if (c.args?.kind !== "enum") throw new Error("expected enum args");

  expect(await c.args.source(runContext())).toEqual([{ id: "openai/gpt-5.5", label: "GPT-5.5", hint: "openai" }]);
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

// --- Go to next session needing you (UX fix) ------------------------------

test("next-needs-you is a global command", () => {
  expect(cmd("next-needs-you").scope).toBe("global");
});

test("next-needs-you opens the first needs-you session when nothing is focused", () => {
  treeStore.setState({
    tree: {
      generated_at: "2026-01-01T00:00:00Z",
      sources: [],
      live: [],
      needs_you: [
        {
          row_id: "r1",
          ref: "local:ny1",
          host_id: "local",
          session_id: "ny1",
          title: "A",
          project: "P",
          state: "awaiting",
          kind: "session",
          live: true,
          children: [],
        },
        {
          row_id: "r2",
          ref: "local:ny2",
          host_id: "local",
          session_id: "ny2",
          title: "B",
          project: "P",
          state: "awaiting",
          kind: "session",
          live: true,
          children: [],
        },
      ],
      pin_sections: [],
      projects: [],
      archived_projects: [],
      test_runs: [],
      attentionSummary: { needsYou: 2, error: 0, working: 0 },
    },
  });

  cmd("next-needs-you").run?.(runContext());

  expect(window.location.pathname).toBe("/s/local%3Any1");
});

test("next-needs-you cycles from the focused session to the next needs-you session, wrapping", () => {
  treeStore.setState({
    tree: {
      generated_at: "2026-01-01T00:00:00Z",
      sources: [],
      live: [],
      needs_you: [
        {
          row_id: "r1",
          ref: "local:ny1",
          host_id: "local",
          session_id: "ny1",
          title: "A",
          project: "P",
          state: "awaiting",
          kind: "session",
          live: true,
          children: [],
        },
        {
          row_id: "r2",
          ref: "local:ny2",
          host_id: "local",
          session_id: "ny2",
          title: "B",
          project: "P",
          state: "awaiting",
          kind: "session",
          live: true,
          children: [],
        },
      ],
      pin_sections: [],
      projects: [],
      archived_projects: [],
      test_runs: [],
      attentionSummary: { needsYou: 2, error: 0, working: 0 },
    },
  });
  focusSession("local:ny2");

  cmd("next-needs-you").run?.(runContext());

  expect(window.location.pathname).toBe("/s/local%3Any1");
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

test("/tasks and /status toggle the focused session panes at every viewport", () => {
  focusSession("ref_a");
  seedModel("ref_a");

  cmd("tasks").run?.(runContext());
  cmd("status").run?.(runContext());
  expect(workspaceStore.getState().panes).toEqual(
    expect.arrayContaining([
      expect.objectContaining({ type: "sessionTasks", params: { ref: "ref_a" } }),
      expect.objectContaining({ type: "sessionDetails", params: { ref: "ref_a" } }),
    ]),
  );
});

test("/tasks and /status toggle-close already-open panes", () => {
  focusSession("ref_a");
  seedModel("ref_a");
  workspaceStore.getState().openPane("sessionTasks", { ref: "ref_a" });
  workspaceStore.getState().openPane("sessionDetails", { ref: "ref_a" });

  cmd("tasks").run?.(runContext());
  cmd("status").run?.(runContext());
  expect(workspaceStore.getState().panes.some((p) => p.type === "sessionTasks")).toBe(false);
  expect(workspaceStore.getState().panes.some((p) => p.type === "sessionDetails")).toBe(false);
});

// FIX 2 (real-user report): a user hunting for the keyboard shortcut legend
// tried "?" and searched "shortcut" in the palette and never found it - the
// "help" command's own keywords didn't cover "hotkey", one of the terms a
// user reasonably reaches for.
test('the keyboard-shortcuts command is findable by keyword "hotkey"', () => {
  expect(cmd("help").keywords).toContain("hotkey");
});

test("/tasks and /status still toggle the workspace panes on a mobile viewport", () => {
  window.matchMedia = vi.fn(() => ({
    matches: true,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  })) as unknown as typeof window.matchMedia;
  focusSession("ref_a");
  seedModel("ref_a");

  cmd("tasks").run?.(runContext());
  cmd("status").run?.(runContext());
  // The unified SessionMenu owns Details/Tasks at every width, so the chrome
  // trigger buttons the legacy mobile path clicked no longer render - the
  // commands open the workspace panes directly, exactly as on desktop.
  expect(workspaceStore.getState().panes).toEqual(
    expect.arrayContaining([
      expect.objectContaining({ type: "sessionTasks", params: { ref: "ref_a" } }),
      expect.objectContaining({ type: "sessionDetails", params: { ref: "ref_a" } }),
    ]),
  );
});
