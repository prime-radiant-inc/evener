import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { buildCommands, type PaletteRunContext } from "../../../shell/palette/commands";
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
    ...overrides,
  };
}

async function openPanel(model: ThreadModel) {
  render(<DetailsPanel model={model} now={0} />);
  await userEvent.click(screen.getByRole("button", { name: "Details" }));
}

afterEach(() => {
  cleanup();
});

test("the trigger opens the details sheet", async () => {
  await openPanel(testModel());
  expect(screen.getByRole("dialog", { name: "Session details" })).toBeTruthy();
});

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

test("tokens read as up/down arrows over the session's cumulative usage", async () => {
  await openPanel(testModel());
  const row = screen.getByTestId("session-details-tokens");
  expect(row.textContent).toContain("↑100k");
  expect(row.textContent).toContain("↓20k");
});

test("no tokens row at all when the daemon reports no token data", async () => {
  await openPanel(testModel({ usage: null }));
  expect(screen.queryByTestId("session-details-tokens")).toBeNull();
});

// Cost is a server-formatted string (appwire.EstimateCost) - shown verbatim,
// because the pricing table it came from never crosses the wire.
test("cost is rendered exactly as the server formatted it", async () => {
  await openPanel(testModel({ cost: "~$1.00" }));
  expect(screen.getByTestId("session-details-cost").textContent).toContain("~$1.00");
});

test("no cost row when the daemon omits cost, rather than a bogus zero", async () => {
  await openPanel(testModel({ cost: null }));
  expect(screen.queryByTestId("session-details-cost")).toBeNull();
  expect(screen.getByRole("dialog", { name: "Session details" }).textContent).not.toContain("$");
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

test("a closed session gets the same no-context treatment as an ended one", async () => {
  await openPanel(testModel({ status: { type: "closed" } }));
  expect(screen.queryByTestId("session-details-context")).toBeNull();
});

test("a live session whose daemon reports no context window shows no context row", async () => {
  await openPanel(testModel({ contextWindow: 0, contextUsed: 0, contextPressure: 0 }));
  expect(screen.queryByTestId("session-details-context")).toBeNull();
});

// The command palette's "Toggle session details" (/status) synthesizes a click
// on [data-details-trigger] (shell/palette/commands.ts clickTrigger), so pin
// that the palette's own selector resolves to exactly this trigger.
test("the trigger carries data-details-trigger so the palette's /status command can reach it", () => {
  render(<DetailsPanel model={testModel()} now={0} />);
  expect(document.querySelector("[data-details-trigger]")).toBe(screen.getByRole("button", { name: "Details" }));
});

test("running the palette's 'Toggle session details' command opens the panel", () => {
  render(<DetailsPanel model={testModel()} now={0} />);
  const command = buildCommands().find((c) => c.title === "Toggle session details");
  if (!command) throw new Error("no 'Toggle session details' command in the palette registry");
  const ctx: PaletteRunContext = {
    sessionRef: "ref_a",
    onPage: "session",
    toasts: { push: () => {} },
    ui: { clearToSearch: () => {}, showHelp: () => {} },
  };
  // clickTrigger dispatches a real native click, so the resulting React state
  // update has to be flushed here rather than by userEvent's own act wrapper.
  act(() => {
    command.run?.(ctx);
  });
  expect(screen.getByRole("dialog", { name: "Session details" })).toBeTruthy();
});
