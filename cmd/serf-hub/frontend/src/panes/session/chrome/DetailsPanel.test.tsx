import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import type { ThreadModel, TurnModel } from "../../../protocol/model";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { buildCommands, type PaletteRunContext } from "../../../shell/palette/commands";
import { registerPane } from "../../../shell/paneRegistry";
import { isPaneOpen, resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { requireClass } from "../../../widgets/internal/requireClass";
import rawMeterStyles from "../../../widgets/meter/meter.module.css";
import { DetailsPanel } from "./DetailsPanel";

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

function testModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
  return {
    ref: "local:033uaztQj6XPP6eF7pS0OW",
    threadId: "033uaztQj6XPP6eF7pS0OW",
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
    capabilities: CAPABILITIES,
    goal: null,
    contextUsed: 42_000,
    contextWindow: 100_000,
    contextPressure: 0.42,
    usage: { inputTokens: 100_000, outputTokens: 20_000 },
    cost: "~$1.00",
    workMillis: 4200,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    ...rest,
    jobsTreeRevision,
  };
}

// A turn carrying only the fields the panel's token derivation reads.
function usageTurn(id: string, inputTokens: number, outputTokens: number): TurnModel {
  return { id, status: "completed", items: [], usage: { inputTokens, outputTokens } };
}

async function openPanel(model: ThreadModel) {
  render(<DetailsPanel model={model} now={0} />);
  await userEvent.click(screen.getByRole("button", { name: "Details" }));
}

function panelText(): string {
  return screen.getByRole("dialog", { name: "Session details" }).textContent ?? "";
}

function PaneFixture() {
  return <div>pane</div>;
}

beforeAll(() => {
  // Test-only pane registration (RailRow.test.tsx's pattern): the workspace
  // store's togglePane refuses an unregistered type, and the palette's
  // "Toggle session details" command opens a sessionDetails pane.
  registerPane<{ ref: string }>({
    id: "sessionDetails",
    title: () => "Session details",
    component: lazy(() => Promise.resolve({ default: PaneFixture })),
  });
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

test("the trigger opens the details sheet", async () => {
  await openPanel(testModel());
  expect(screen.getByRole("dialog", { name: "Session details" })).toBeTruthy();
});

// --- context ----------------------------------------------------------------

// The exact figures are the panel's whole reason to exist: the status row
// shows the context meter, this shows what the meter is made of.
test("a live session's context row shows percent used, used / window, and how much is left", async () => {
  await openPanel(testModel());
  const row = screen.getByTestId("session-details-context");
  expect(row.textContent).toContain("42% used");
  expect(row.textContent).toContain("42k / 100k");
  expect(row.textContent).toContain("58k left");
});

test("the context row carries a meter at the session's pressure", async () => {
  await openPanel(testModel());
  const meter = screen.getByRole("meter", { name: /Context/ });
  expect(meter.getAttribute("aria-valuenow")).toBe("42000");
  expect(meter.getAttribute("aria-valuemax")).toBe("100000");
});

// The gauge's severity comes from the shared contextTone ladder (see
// statusFormat.ts), which the status row reads too - the same session must
// never look calm on one surface and alarming on the other. These pin the
// rendered tone class at each tier, so a drifted threshold shows up here as a
// wrong class rather than only as a helper-level unit failure.
const meterToneClass = {
  neutral: requireClass(rawMeterStyles.neutral, "meter.module.css", "neutral"),
  attention: requireClass(rawMeterStyles.attention, "meter.module.css", "attention"),
  danger: requireClass(rawMeterStyles.danger, "meter.module.css", "danger"),
};

test.each([
  { pressure: 0.42, tone: "neutral" as const },
  { pressure: 0.8, tone: "attention" as const },
  { pressure: 0.95, tone: "danger" as const },
])("the context meter renders the $tone tone at pressure $pressure", async ({ pressure, tone }) => {
  await openPanel(testModel({ contextUsed: pressure * 100_000, contextPressure: pressure }));
  expect(screen.getByTestId("meter-fill").classList.contains(meterToneClass[tone])).toBe(true);
});

test("a closed session gets the same no-context treatment as an ended one", async () => {
  await openPanel(testModel({ status: { type: "closed" } }));
  expect(screen.queryByTestId("session-details-context")).toBeNull();
});

test("a live session whose daemon reports no context window shows no context row", async () => {
  await openPanel(testModel({ contextWindow: 0, contextUsed: 0, contextPressure: 0 }));
  expect(screen.queryByTestId("session-details-context")).toBeNull();
});

// --- work time --------------------------------------------------------------

test("work time is formatted with the daemon's own duration convention", async () => {
  await openPanel(testModel({ workMillis: 4200 }));
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("4s");
});

test("the live in-flight turn's elapsed time is added to the banked work time", async () => {
  render(
    <DetailsPanel
      model={testModel({ workMillis: 60_000, activeTurnStartedAt: "1970-01-01T00:01:00.000Z" })}
      now={180_000}
    />,
  );
  await userEvent.click(screen.getByRole("button", { name: "Details" }));
  // 60s banked + (180s now - 60s start) = 180s.
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("3m");
});

// The bug Jesse saw: a session whose meta carries no work_millis was the ONE
// row that still rendered, reading a fabricated "1s" (formatWorkDuration
// clamps its minimum up so a real sub-second duration never prints "0s" - but
// an unmeasured session has no duration to round at all).
test("no work-time row at all when the session has no measured work time", async () => {
  await openPanel(testModel({ workMillis: 0 }));
  expect(screen.queryByTestId("session-details-work-time")).toBeNull();
  expect(panelText()).not.toContain("1s");
});

test("an active turn's elapsed time alone is enough to show work time on a session with nothing banked", async () => {
  render(
    <DetailsPanel model={testModel({ workMillis: 0, activeTurnStartedAt: "1970-01-01T00:00:00.500Z" })} now={9_500} />,
  );
  await userEvent.click(screen.getByRole("button", { name: "Details" }));
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("9s");
});

// --- tokens -----------------------------------------------------------------

test("tokens read as up/down arrows over the session's cumulative usage", async () => {
  await openPanel(testModel());
  const row = screen.getByTestId("session-details-tokens");
  expect(row.textContent).toContain("↑100k");
  expect(row.textContent).toContain("↓20k");
});

test("no tokens row at all when neither the thread nor any loaded turn has token data", async () => {
  await openPanel(testModel({ usage: null, turns: [] }));
  expect(screen.queryByTestId("session-details-tokens")).toBeNull();
});

// The root cause of Jesse's empty panel: a fork child's persisted meta carries
// no CumulativeUsage (agent/fork.go's writeForkChild never stamps one), so the
// thread-level total is absent even though every loaded turn has real usage -
// which is why the transcript's per-turn stamps rendered right beside a panel
// that showed nothing.
test("a session with no thread-level total falls back to summing the loaded turns", async () => {
  await openPanel(
    testModel({ usage: null, turns: [usageTurn("t1", 6961, 73), usageTurn("t2", 1276, 47)], olderCursor: undefined }),
  );
  const row = screen.getByTestId("session-details-tokens");
  expect(row.textContent).toContain("↑8k");
  expect(row.textContent).toContain("↓120");
});

// thread/read windows turns via turnLimit and reports the truncation through
// olderCursor. A sum over that window is NOT the session total, so the label
// must say what it actually counts instead of overstating its scope.
test("a derived total over a truncated turn window is labelled as covering only the loaded turns", async () => {
  await openPanel(testModel({ usage: null, turns: [usageTurn("t1", 500, 20)], olderCursor: "cursor_1" }));
  expect(screen.getByTestId("session-details-tokens").textContent).toContain("↑500");
  expect(panelText()).toContain("tokens (loaded turns)");
});

test("the thread's own cumulative total is labelled plainly as the session's tokens", async () => {
  await openPanel(testModel({ olderCursor: "cursor_1" }));
  expect(panelText()).toContain("tokens");
  expect(panelText()).not.toContain("tokens (loaded turns)");
});

// --- cost -------------------------------------------------------------------

// Cost is a server-formatted string (appwire.EstimateCost) - shown verbatim,
// because the pricing table it came from never crosses the wire.
test("cost is rendered exactly as the server formatted it", async () => {
  await openPanel(testModel({ cost: "~$1.00" }));
  expect(screen.getByTestId("session-details-cost").textContent).toContain("~$1.00");
});

test("no cost row when the daemon omits cost, rather than a bogus zero", async () => {
  await openPanel(testModel({ cost: null }));
  expect(screen.queryByTestId("session-details-cost")).toBeNull();
  expect(panelText()).not.toContain("$");
});

// The client cannot derive a cost even when it CAN derive tokens: EstimateCost
// deliberately returns empty for an uncataloged model, and the pricing table
// never crosses the wire, so there is nothing to multiply by. A derived token
// sum must not grow a cost row out of thin air.
test("a derived token total never invents a cost the server did not send", async () => {
  await openPanel(testModel({ usage: null, cost: null, turns: [usageTurn("t1", 6961, 73)] }));
  expect(screen.getByTestId("session-details-tokens")).toBeTruthy();
  expect(screen.queryByTestId("session-details-cost")).toBeNull();
  expect(panelText()).not.toContain("$");
});

// --- identity and location --------------------------------------------------

test("the model row shows the provider/model label the status row's switch shows", async () => {
  await openPanel(testModel({ modelProvider: "openai", model: "gpt-5.6-luna" }));
  expect(screen.getByTestId("session-details-model").textContent).toContain("openai/gpt-5.6-luna");
});

test("the status row names the session's current state", async () => {
  await openPanel(testModel({ status: { type: "active" } }));
  expect(screen.getByTestId("session-details-status").textContent).toContain("active");
});

test("session id, cwd, and branch each render when the wire carried them", async () => {
  await openPanel(
    testModel({ threadId: "033uaztQj6XPP6eF7pS0OW", cwd: "/Users/jesse/serf", gitBranch: "details-empty" }),
  );
  expect(screen.getByTestId("session-details-session-id").textContent).toContain("033uaztQj6XPP6eF7pS0OW");
  expect(screen.getByTestId("session-details-cwd").textContent).toContain("/Users/jesse/serf");
  expect(screen.getByTestId("session-details-branch").textContent).toContain("details-empty");
});

test("a project path distinct from the cwd gets its own row (a linked worktree)", async () => {
  await openPanel(testModel({ cwd: "/Users/jesse/serf/.claude/worktrees/x", projectPath: "/Users/jesse/serf" }));
  expect(screen.getByTestId("session-details-project").textContent).toContain("/Users/jesse/serf");
});

// Repeating the cwd verbatim under a second label teaches a reader nothing.
test("no project row when the project path is just the cwd again", async () => {
  await openPanel(testModel({ cwd: "/Users/jesse/serf", projectPath: "/Users/jesse/serf" }));
  expect(screen.queryByTestId("session-details-project")).toBeNull();
});

test("location rows the wire did not carry are omitted rather than shown blank", async () => {
  await openPanel(testModel({ gitBranch: undefined, projectPath: undefined }));
  expect(screen.queryByTestId("session-details-branch")).toBeNull();
  expect(screen.queryByTestId("session-details-project")).toBeNull();
});

test("created and updated instants render when the wire carried them", async () => {
  const created = "2026-07-23T18:02:46.000Z";
  await openPanel(testModel({ createdAt: created, updatedAt: "2026-07-24T06:01:17.000Z" }));
  expect(screen.getByTestId("session-details-created").textContent).toContain(new Date(created).toLocaleString());
  expect(screen.getByTestId("session-details-updated")).toBeTruthy();
});

test("no created/updated rows when the source did not know the instants", async () => {
  await openPanel(testModel({ createdAt: undefined, updatedAt: undefined }));
  expect(screen.queryByTestId("session-details-created")).toBeNull();
  expect(screen.queryByTestId("session-details-updated")).toBeNull();
});

// --- grouping ---------------------------------------------------------------

// The legacy panel grouped its rows under titled sections (Session / Usage /
// Runtime), which is what keeps a dozen rows scannable.
test("rows are grouped under titled sections", async () => {
  await openPanel(testModel());
  expect(screen.getByRole("heading", { name: "Session" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Usage" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Location" })).toBeTruthy();
});

// A section whose every row was omitted for want of data must not leave a bare
// heading behind.
test("a section with no rows to show is dropped, heading included", async () => {
  await openPanel(
    testModel({
      status: { type: "ended" },
      contextWindow: 0,
      usage: null,
      cost: null,
      workMillis: 0,
      turns: [],
    }),
  );
  expect(screen.queryByRole("heading", { name: "Usage" })).toBeNull();
});

// The whole failure this panel is being fixed for: a session with no accounting
// to report must drop that section rather than present one fabricated number as
// if it were the session's whole accounting.
test("a session with no accounting data at all keeps its identity rows and drops the usage section", async () => {
  await openPanel(
    testModel({
      status: { type: "ended" },
      contextWindow: 0,
      usage: null,
      cost: null,
      workMillis: 0,
      turns: [],
      gitBranch: undefined,
      projectPath: undefined,
      createdAt: undefined,
      updatedAt: undefined,
    }),
  );
  expect(screen.getByTestId("session-details-model")).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "Usage" })).toBeNull();
});

// An ended session has no live context pressure - work time, tokens, and cost
// are all still real (persisted), so the panel opens and shows them.
test("an ended session shows work time, tokens, and cost but no context row", async () => {
  await openPanel(testModel({ status: { type: "ended" }, contextUsed: 42_000, contextWindow: 100_000 }));
  expect(screen.queryByTestId("session-details-context")).toBeNull();
  expect(screen.getByTestId("session-details-work-time")).toBeTruthy();
  expect(screen.getByTestId("session-details-tokens")).toBeTruthy();
  expect(screen.getByTestId("session-details-cost")).toBeTruthy();
});

// --- palette wiring ---------------------------------------------------------

// The command palette's "Toggle session details" (/status) no longer reaches
// into this panel's DOM: it toggles the sessionDetails workspace pane on every
// viewport (shell/palette/commands.ts toggleSessionPane). Pin that contract
// here instead of the retired [data-details-trigger] click path.
test("running the palette's 'Toggle session details' command toggles the sessionDetails workspace pane", () => {
  const command = buildCommands().find((c) => c.title === "Toggle session details");
  if (!command) throw new Error("no 'Toggle session details' command in the palette registry");
  const ctx: PaletteRunContext = {
    sessionRef: "ref_a",
    onPage: "session",
    toasts: { push: () => {} },
    ui: { clearToSearch: () => {}, showHelp: () => {} },
  };
  const params = { ref: "ref_a" };
  expect(isPaneOpen(workspaceStore.getState(), "sessionDetails", params)).toBe(false);
  act(() => {
    command.run?.(ctx);
  });
  expect(isPaneOpen(workspaceStore.getState(), "sessionDetails", params)).toBe(true);
  // A second run must close it again - the command is a toggle, not an open.
  act(() => {
    command.run?.(ctx);
  });
  expect(isPaneOpen(workspaceStore.getState(), "sessionDetails", params)).toBe(false);
});
