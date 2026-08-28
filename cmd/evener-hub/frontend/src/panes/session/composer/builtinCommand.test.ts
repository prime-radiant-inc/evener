import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { buildCommands, type PaletteRunContext, sessionBuiltinCommands } from "../../../shell/palette/commands";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { matchBuiltinInvocation, runBuiltinCommand } from "./builtinCommand";

const CAPS: ThreadCapabilities = {
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

function connectFake(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

async function seedThread(ref: string, overrides: Record<string, unknown> = {}): Promise<FakeClient> {
  const fake = connectFake();
  fake.on("thread/read", () => ({
    thread: {
      id: `thr_${ref}`,
      sessionId: `sess_${ref}`,
      preview: "",
      ephemeral: false,
      modelProvider: "anthropic",
      createdAt: 1000,
      updatedAt: 1000,
      status: { type: "idle" },
      cwd: "/tmp/p",
      cliVersion: "1.0.0",
      source: "evener",
      evener: { ref, capabilities: CAPS, queue: { revision: 0 } },
      ...overrides,
    },
  }));
  await threadsStore.getState().ensureThread(ref);
  return fake;
}

const pushes: Array<{ kind: string; text: string }> = [];
function runCtx(ref: string): PaletteRunContext {
  return {
    sessionRef: ref,
    onPage: "session",
    toasts: { push: (kind, text) => pushes.push({ kind, text }) },
    ui: { clearToSearch: vi.fn(), showHelp: vi.fn() },
  };
}

function allBuiltins() {
  return buildCommands().filter((c) => c.scope === "session");
}

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  pushes.length = 0;
});

afterEach(() => {
  vi.restoreAllMocks();
});

// --- matchBuiltinInvocation ------------------------------------------------

test("matches a bare argless built-in with nothing after it", () => {
  const match = matchBuiltinInvocation("/compact", allBuiltins());
  expect(match?.command.id).toBe("compact");
  expect(match?.argsText).toBe("");
});

test("an argless built-in followed by extra text is NOT a match - falls through as a plain message", () => {
  expect(matchBuiltinInvocation("/compact right now", allBuiltins())).toBeNull();
});

test("matches a free-arg built-in, capturing everything after the first space as argsText", () => {
  const match = matchBuiltinInvocation("/goal fix the login bug", allBuiltins());
  expect(match?.command.id).toBe("goal");
  expect(match?.argsText).toBe("fix the login bug");
});

test("a free-arg built-in with no trailing text still matches, with an empty argsText", () => {
  const match = matchBuiltinInvocation("/goal", allBuiltins());
  expect(match?.command.id).toBe("goal");
  expect(match?.argsText).toBe("");
});

test("an unknown /name is not a match", () => {
  expect(matchBuiltinInvocation("/frobnicate", allBuiltins())).toBeNull();
});

test("text with no leading slash is not a match", () => {
  expect(matchBuiltinInvocation("hello /goal", allBuiltins())).toBeNull();
});

test("a plugin command name is never matched here - this module only knows built-ins", () => {
  expect(matchBuiltinInvocation("/review fix this", allBuiltins())).toBeNull();
});

test("args text can span multiple lines", () => {
  const match = matchBuiltinInvocation("/steer line one\nline two", allBuiltins());
  expect(match?.argsText).toBe("line one\nline two");
});

// --- runBuiltinCommand -------------------------------------------------

test("a successful argless command (no self-toast) resolves ok with no toast pushed", async () => {
  const fake = await seedThread("ref_a");
  fake.on("thread/compact/start", () => ({}));
  const match = matchBuiltinInvocation("/compact", sessionBuiltinCommands({ sessionRef: "ref_a", onPage: "session" }))!;
  const outcome = await runBuiltinCommand(match, runCtx("ref_a"));
  expect(outcome).toEqual({ ok: true });
  expect(pushes).toEqual([]);
});

test("a successful free-arg command (goal) resolves ok, no toast, live chrome is threadsStore's own concern", async () => {
  const fake = await seedThread("ref_a");
  fake.on("goal/set", () => ({ started: false }));
  const match = matchBuiltinInvocation("/goal fix it", allBuiltins())!;
  const outcome = await runBuiltinCommand(match, runCtx("ref_a"));
  expect(outcome).toEqual({ ok: true });
  expect(pushes).toEqual([]);
});

test("a command that already toasts on success (shutdown) is not double-toasted", async () => {
  const fake = await seedThread("ref_a");
  fake.on("thread/shutdown", () => ({}));
  const match = matchBuiltinInvocation("/shutdown", allBuiltins())!;
  const outcome = await runBuiltinCommand(match, runCtx("ref_a"));
  expect(outcome).toEqual({ ok: true });
  expect(pushes).toEqual([{ kind: "success", text: "Session shut down" }]);
});

test("a command that already toasts on failure (shutdown) is not double-toasted with a friendlyErrorMessage", async () => {
  const fake = await seedThread("ref_a");
  fake.on("thread/shutdown", () => {
    throw new Error("boom");
  });
  const match = matchBuiltinInvocation("/shutdown", allBuiltins())!;
  const outcome = await runBuiltinCommand(match, runCtx("ref_a"));
  expect(outcome.ok).toBe(false);
  expect(pushes).toEqual([{ kind: "error", text: "Shutdown failed" }]);
});

test("a command with no self-toast that rejects gets a friendlyErrorMessage fallback toast, exactly one", async () => {
  const fake = await seedThread("ref_a");
  fake.on("goal/set", () => {
    throw new Error("boom");
  });
  const match = matchBuiltinInvocation("/goal fix it", allBuiltins())!;
  const outcome = await runBuiltinCommand(match, runCtx("ref_a"));
  expect(outcome.ok).toBe(false);
  expect(pushes).toHaveLength(1);
  expect(pushes[0]?.kind).toBe("error");
});

test("a blocked sentinel (no active turn) surfaces its own message as a toast and preserves the draft (ok: false)", async () => {
  await seedThread("ref_a", { status: { type: "idle" } });
  const match = matchBuiltinInvocation("/steer go left", allBuiltins())!;
  const outcome = await runBuiltinCommand(match, runCtx("ref_a"));
  expect(outcome).toEqual({ ok: false, message: "steer failed: no active turn" });
  expect(pushes).toEqual([{ kind: "error", text: "steer failed: no active turn" }]);
});

test("an unavailableReason command is refused WITHOUT attempting the RPC", async () => {
  await seedThread("ref_a", {
    evener: { ref: "ref_a", capabilities: { ...CAPS, compact: false }, queue: { revision: 0 } },
  });
  const builtins = sessionBuiltinCommands({ sessionRef: "ref_a", onPage: "session" });
  const match = matchBuiltinInvocation("/compact", builtins)!;
  expect(match.command.unavailableReason).toBeDefined();
  const outcome = await runBuiltinCommand(match, runCtx("ref_a"));
  expect(outcome).toEqual({ ok: false, message: "/compact is not available right now" });
  expect(pushes).toEqual([{ kind: "error", text: "/compact is not available right now" }]);
});

test("an enum command (/model) resolves its value from the SAME merged catalog its args.source uses", async () => {
  const fake = await seedThread("ref_a");
  fake.on("model/list", () => ({ data: [{ provider: "anthropic", model: "claude-x" }] }));
  fake.on("thread/model/set", () => ({}));
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ models: [], recent: [], diagnostics: [] }),
    }),
  );
  const match = matchBuiltinInvocation("/model anthropic/claude-x", allBuiltins())!;
  const outcome = await runBuiltinCommand(match, runCtx("ref_a"));
  expect(outcome).toEqual({ ok: true });
  expect(pushes).toEqual([{ kind: "success", text: "Model: anthropic/claude-x" }]);
  vi.unstubAllGlobals();
});

test("an enum command given an unknown value is blocked with an honest message, no RPC attempted", async () => {
  const fake = await seedThread("ref_a");
  fake.on("model/list", () => ({ data: [{ provider: "anthropic", model: "claude-x" }] }));
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({ ok: true, json: async () => ({ models: [], recent: [], diagnostics: [] }) }),
  );
  const match = matchBuiltinInvocation("/model not-a-real-model", allBuiltins())!;
  const outcome = await runBuiltinCommand(match, runCtx("ref_a"));
  expect(outcome).toEqual({ ok: false, message: '/model: unknown value "not-a-real-model"' });
  expect(fake.calls.some((c) => c.method === "thread/model/set")).toBe(false);
  vi.unstubAllGlobals();
});
