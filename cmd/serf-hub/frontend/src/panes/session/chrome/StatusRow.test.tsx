import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { hydrateThread } from "../../../protocol/reducer";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { StatusRow } from "./StatusRow";

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
    modelProvider: "anthropic/claude-sonnet-4-5",
    model: "anthropic/claude-sonnet-4-5",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    lastFrameAt: 0,
    capabilities: CAPABILITIES,
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

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
});

// --- state dot ------------------------------------------------------------

test("the state dot reflects the thread's status via the shared cadenceStateForStatus mapping", () => {
  render(<StatusRow sessionRef="ref_a" model={testModel({ status: { type: "active" } })} now={1000} />);
  // StatusDot's own accessible name for the "working" family (see
  // widgets/statusdot's STATE_LABEL) - reused rather than a brittle class
  // check, matching Session.tsx's own Cadence assertions elsewhere.
  expect(screen.getByRole("img", { name: "Working" })).toBeTruthy();
});

// --- model chip -------------------------------------------------------------

test("shows a single model label when provider and model are still the same string (cold-hydrate shape)", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ modelProvider: "anthropic/claude-sonnet-4-5", model: "anthropic/claude-sonnet-4-5" })}
      now={1000}
    />,
  );
  expect(screen.getByText("anthropic/claude-sonnet-4-5")).toBeTruthy();
});

test("shows provider/model once a live thread/model/changed has actually split them apart", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ modelProvider: "anthropic", model: "claude-opus-5" })}
      now={1000}
    />,
  );
  expect(screen.getByText("anthropic/claude-opus-5")).toBeTruthy();
});

// composition proof only - ModelSwitch.test.tsx owns the full picker
// behavior (loading/filter/pick/failure); this just confirms StatusRow
// actually wires the ModelSwitch trigger in, for the SAME sessionRef.
test("wires the model-switch trigger in, acting on the SAME sessionRef passed to StatusRow", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => ({ data: [{ provider: "openai", model: "gpt-5.5" }] }));
  let called: unknown;
  fake.on("thread/model/set", (params) => {
    called = params;
    return {};
  });

  render(<StatusRow sessionRef="ref_a" model={testModel()} now={1000} />);
  await user.click(screen.getByRole("button", { name: /change model/i }));
  const combobox = await screen.findByRole("combobox");
  await user.type(combobox, "gpt");
  await waitFor(() => expect(screen.getByRole("option", { name: /gpt-5\.5/i })).toBeTruthy());
  await user.click(screen.getByRole("option", { name: /gpt-5\.5/i }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a", modelProvider: "openai", model: "gpt-5.5" }));
});

// --- reasoning effort switcher ----------------------------------------------

test("renders no reasoning-effort control when the model doesn't support reasoning and has no current value", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ supportsReasoning: false, reasoningEffortLevels: [] })}
      now={1000}
    />,
  );
  expect(screen.queryByRole("combobox", { name: /reasoning effort/i })).toBeNull();
});

test("renders an interactive select from reasoningEffortLevels, valued at the current reasoningEffort", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({
        supportsReasoning: true,
        cwd: "/tmp/project",
        reasoningEffortLevels: ["low", "medium", "high"],
        reasoningEffort: "medium",
      })}
      now={1000}
    />,
  );
  const select = screen.getByRole("combobox", { name: /reasoning effort/i }) as HTMLSelectElement;
  expect(select.value).toBe("medium");
  // A leading "(default)" option (value "") heads the ladder so an unset
  // effort has an honest home to sit at - see the none-vs-default tests below.
  expect(Array.from(select.options).map((o) => o.value)).toEqual(["", "low", "medium", "high"]);
});

// The wire CAN emit supportsReasoning:true with an empty ladder: the daemon's
// Profile sets p.reasoning and p.effortLevels from independent conditions
// (agent/provider/profile.go:454 vs :442), and the reducer coerces the absent
// ladder to [] (reducer.ts:263). Restore the legacy 4-level fallback
// (model-switch.js:30 DEFAULT_EFFORT_LEVELS) so a reasoning model still gets a
// switcher; the daemon clamps to what it actually accepts.
test("falls back to the default effort ladder when the model reasons but names no levels", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ supportsReasoning: true, reasoningEffortLevels: [], reasoningEffort: "medium" })}
      now={1000}
    />,
  );
  const select = screen.getByRole("combobox", { name: /reasoning effort/i }) as HTMLSelectElement;
  expect(select.value).toBe("medium");
  expect(Array.from(select.options).map((o) => o.value)).toEqual(["", "minimal", "low", "medium", "high"]);
});

// none-vs-(default): an unset reasoning effort is the model running on its own
// default, NOT the user having picked a level - it must read "(default)", never
// the first ladder level (which a value-"" select would otherwise display) and
// never a literal "none".
test("an unset reasoning effort shows as (default), never the first ladder level or an explicit 'none'", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ supportsReasoning: true, reasoningEffortLevels: ["low", "medium", "high"] })}
      now={1000}
    />,
  );
  const select = screen.getByRole("combobox", { name: /reasoning effort/i }) as HTMLSelectElement;
  expect(select.value).toBe("");
  expect(select.options[select.selectedIndex]?.textContent).toBe("(default)");
  expect(Array.from(select.options).map((o) => o.textContent)).not.toContain("none");
});

// serf's "none" clears the effort to the provider default (llm/types.go:670,
// providercfg/load.go:76) - the same meaning as unset. A ladder that lists
// "none" collapses it into the single "(default)" entry (never a duplicate
// option), and a current "none" effort selects that entry, not a literal
// "none" the user appears to have chosen (mirrors legacy search.js:409-415).
test("collapses a ladder-listed 'none' into (default) rather than a duplicate option, and a current 'none' effort selects it", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({
        supportsReasoning: true,
        cwd: "/tmp/project",
        reasoningEffortLevels: ["none", "low", "medium", "high"],
        reasoningEffort: "none",
      })}
      now={1000}
    />,
  );
  const select = screen.getByRole("combobox", { name: /reasoning effort/i }) as HTMLSelectElement;
  expect(select.value).toBe("");
  expect(select.options[select.selectedIndex]?.textContent).toBe("(default)");
  expect(Array.from(select.options).map((o) => o.value)).toEqual(["", "low", "medium", "high"]);
});

test("changing the reasoning-effort select calls setReasoningEffort with the new level", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let called: unknown;
  fake.on("thread/reasoning-effort/set", (params) => {
    called = params;
    return {};
  });

  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({
        supportsReasoning: true,
        cwd: "/tmp/project",
        reasoningEffortLevels: ["low", "medium", "high"],
        reasoningEffort: "medium",
      })}
      now={1000}
    />,
  );
  const select = screen.getByRole("combobox", { name: /reasoning effort/i });
  await user.selectOptions(select, "high");

  await waitFor(() => expect(called).toEqual({ ref: "ref_a", reasoningEffort: "high" }));
});

test("a failed setReasoningEffort call surfaces an error toast", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("thread/reasoning-effort/set", () => {
    throw new Error("boom");
  });

  render(
    <>
      <StatusRow
        sessionRef="ref_a"
        model={testModel({ supportsReasoning: true, reasoningEffortLevels: ["low", "high"], reasoningEffort: "low" })}
        now={1000}
      />
      <Toast />
    </>,
  );
  await user.selectOptions(screen.getByRole("combobox", { name: /reasoning effort/i }), "high");

  await screen.findByText(/boom/i);
});

test("a reasoning model with a current effort but no named ladder gets the default-ladder switcher, valued at that effort", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ supportsReasoning: true, reasoningEffortLevels: [], reasoningEffort: "high" })}
      now={1000}
    />,
  );
  const select = screen.getByRole("combobox", { name: /reasoning effort/i }) as HTMLSelectElement;
  expect(select.value).toBe("high");
  expect(Array.from(select.options).map((o) => o.value)).toEqual(["", "minimal", "low", "medium", "high"]);
});

// --- work-time clock --------------------------------------------------------

test("shows accumulated work time alone when no turn is currently active", () => {
  render(<StatusRow sessionRef="ref_a" model={testModel({ workMillis: 90_000 })} now={1_000_000} />);
  expect(screen.getByText("1m")).toBeTruthy();
});

test("adds the live elapsed time of the in-flight turn to workMillis while one is active", () => {
  const startedAt = new Date(1_000_000).toISOString();
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ workMillis: 60_000, activeTurnStartedAt: startedAt })}
      now={1_000_000 + 30_000}
    />,
  );
  // 60s accumulated + 30s of the still-running turn = 90s = "1m".
  expect(screen.getByText("1m")).toBeTruthy();
});

// Wire-true reproduction of the W6-close punch item: a present-but-zero
// SerfThread.ActiveTurnStartedAt (the daemon's zero value) runs through the
// REAL reducer, whose epochMsToISO turns it into "1970-01-01T00:00:00.000Z"
// (proven here first), and the status row must NOT render the resulting
// now-minus-epoch span as an absurd "500000h" clock.
test("renders the banked work time, not a now-minus-epoch clock, for a zero-valued activeTurnStartedAt off the wire", () => {
  const now = 1_800_000_000_000;
  const model = hydrateThread(
    {
      thread: {
        id: "thr_a",
        sessionId: "sess_a",
        preview: "",
        ephemeral: false,
        modelProvider: "anthropic/claude-sonnet-4-5",
        createdAt: 1000,
        updatedAt: 1000,
        status: { type: "active" },
        cwd: "/tmp/project",
        cliVersion: "1.0.0",
        source: "serf",
        serf: { ref: "ref_a", capabilities: CAPABILITIES, queue: {}, workMillis: 45_000, activeTurnStartedAt: 0 },
      },
    },
    "ref_a",
    now,
  );
  // The reducer's source guard maps the wire's zero sentinel to absent, so
  // the render takes the no-active-turn path.
  expect(model.activeTurnStartedAt).toBeUndefined();

  render(<StatusRow sessionRef="ref_a" model={model} now={now} />);
  const workTime = screen.getByTestId("status-row-work-time");
  // Honest banked total (45s), never the ~500000h now-minus-epoch clock.
  expect(workTime.textContent).toBe("45s");
});

test("renders the banked work time even if an epoch anchor reaches the model by another path", () => {
  // Defense-in-depth: statusFormat rejects at-or-before-epoch anchors on its
  // own, independent of the reducer's source guard.
  const now = 1_800_000_000_000;
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ workMillis: 45_000, activeTurnStartedAt: new Date(0).toISOString() })}
      now={now}
    />,
  );
  expect(screen.getByTestId("status-row-work-time").textContent).toBe("45s");
});

// --- context gauge -----------------------------------------------------------

test("renders no context gauge when the thread has no context window data", () => {
  render(<StatusRow sessionRef="ref_a" model={testModel({ contextWindow: 0 })} now={1000} />);
  expect(screen.queryByRole("meter")).toBeNull();
});

test("renders the context gauge with an accessible label and the compact used/window numbers", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ contextUsed: 12_000, contextWindow: 128_000, contextPressure: 0.09 })}
      now={1000}
    />,
  );
  const meter = screen.getByRole("meter");
  expect(meter.getAttribute("aria-valuenow")).toBe("12000");
  expect(meter.getAttribute("aria-valuemax")).toBe("128000");
  expect(meter.getAttribute("aria-label")).toBeTruthy();
  expect(screen.getByText("12k / 128k")).toBeTruthy();
});

// --- usage -------------------------------------------------------------------

test("shows nothing for usage when the model has no token data at all", () => {
  render(<StatusRow sessionRef="ref_a" model={testModel({ usage: null })} now={1000} />);
  expect(screen.queryByTestId("status-row-usage")).toBeNull();
});

test("shows input/output token counts when usage data is present", () => {
  render(
    <StatusRow sessionRef="ref_a" model={testModel({ usage: { inputTokens: 1500, outputTokens: 320 } })} now={1000} />,
  );
  expect(screen.getByTestId("status-row-usage").textContent).toBe("↑2k ↓320");
});
