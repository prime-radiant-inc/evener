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
import { requireClass } from "../../../widgets/internal/requireClass";
import rawMeterStyles from "../../../widgets/meter/meter.module.css";
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

/** A running session: the clock only reports an IN-FLIGHT turn, so it needs a
 * live turn anchor, not just an "active" status. */
function runningModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  return testModel({
    status: { type: "active" },
    activeTurnId: "turn_1",
    activeTurnStartedAt: new Date(1_000_000).toISOString(),
    ...overrides,
  });
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

// --- what the strip does NOT carry any more --------------------------------
//
// The strip's job is "what makes me act in the next minute", not "every fact
// about this session". Each removal below has a specific home instead, and each
// of these tests exists to keep the fact from creeping back onto this altitude.

// The pane header already renders Cadence for this session (Session.tsx passes
// it to PaneScaffold), so a second dot two rows down restated it.
test("no state dot: the pane header already carries this session's cadence", () => {
  render(<StatusRow sessionRef="ref_a" model={runningModel()} now={1_000_000} />);
  expect(screen.queryByRole("img", { name: "Working" })).toBeNull();
});

// cwd / branch / project can't change mid-session, so they are reference
// material rather than status - DetailsPanel is where they live now (and
// DetailsPanel.test.tsx owns their behavior).
test("no location cluster: cwd, branch and project belong to the details sheet", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={runningModel({ gitBranch: "feature/x", projectPath: "/proj", cwd: "/proj/wt" })}
      now={1_000_000}
    />,
  );
  expect(screen.queryByTestId("status-row-location")).toBeNull();
  expect(screen.getByTestId("status-row").textContent).not.toContain("/proj/wt");
  expect(screen.getByTestId("status-row").textContent).not.toContain("feature/x");
});

// Cost is the glanceable form of the same fact, and the details sheet carries
// the exact figures - two representations of token spend on one 12px line was
// the same fact at two altitudes.
test("no raw token counts: cost subsumes them for glancing", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={runningModel({ usage: { inputTokens: 1500, outputTokens: 320 }, cost: "~$1.23" })}
      now={1_000_000}
    />,
  );
  expect(screen.queryByTestId("status-row-usage")).toBeNull();
  expect(screen.getByTestId("status-row").textContent).not.toContain("↑");
  expect(screen.getByTestId("status-row-cost").textContent).toBe("~$1.23");
});

// --- model switcher ---------------------------------------------------------

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
  // Named "Model" (widgets/modelCatalog's own aria-label); the effort control's
  // own combobox is named "Reasoning effort", so this is unambiguous even
  // though both roles now live on the same row.
  const combobox = await screen.findByRole("combobox", { name: "Model" });
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
  await user.clear(combobox);
  await user.keyboard("gpt");
  await waitFor(() => expect(screen.getByRole("option", { name: /gpt-5\.5/i })).toBeTruthy());
  await user.click(screen.getByRole("option", { name: /gpt-5\.5/i }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a", modelProvider: "openai", model: "gpt-5.5" }));
});

// --- reasoning effort switcher ----------------------------------------------
//
// The effort control lost its bordered <select> box and became the same kind of
// quiet value-as-trigger the model switcher is - but it is STILL a real native
// <select> underneath, so every one of these behavior tests holds unchanged.

test("renders no reasoning-effort control when the model doesn't support reasoning and has no current value", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ supportsReasoning: false, reasoningEffortLevels: [] })}
      now={1000}
    />,
  );
  expect(screen.queryByRole("combobox", { name: /reasoning effort/i })).toBeNull();
  expect(screen.queryByTestId("status-row-effort")).toBeNull();
});

test("renders an interactive select from reasoningEffortLevels, valued at the current reasoningEffort", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({
        supportsReasoning: true,
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

// The visible readout is a separate, aria-hidden element laid UNDER the
// transparent select, so it has to track the same value or the row would show
// one thing and speak another.
test("the visible readout shows the same value the select carries", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ supportsReasoning: true, reasoningEffortLevels: ["low", "high"], reasoningEffort: "high" })}
      now={1000}
    />,
  );
  expect(screen.getByTestId("status-row-effort-value").textContent).toBe("high");
});

// Losing the box must not cost the control its keyboard operability or its
// accessible name - the whole point of laying a real <select> over the readout
// rather than drawing a fake trigger.
test("the effort control is still keyboard-focusable and accessibly named without its box", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ supportsReasoning: true, reasoningEffortLevels: ["low", "high"], reasoningEffort: "low" })}
      now={1000}
    />,
  );
  const select = screen.getByRole("combobox", { name: /reasoning effort/i }) as HTMLSelectElement;
  select.focus();
  expect(document.activeElement).toBe(select);
  expect(select.disabled).toBe(false);
});

// The readout is aria-hidden precisely so the value isn't announced twice (the
// select already speaks it).
test("the visible readout is hidden from assistive tech, so the value is spoken once", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ supportsReasoning: true, reasoningEffortLevels: ["low", "high"], reasoningEffort: "low" })}
      now={1000}
    />,
  );
  expect(screen.getByTestId("status-row-effort-value").getAttribute("aria-hidden")).toBe("true");
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
  // The VISIBLE readout must say the same thing - a quiet trigger whose face
  // showed "low" while its select sat at "" would be the exact lie this rule
  // exists to prevent.
  expect(screen.getByTestId("status-row-effort-value").textContent).toBe("(default)");
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
  expect(screen.getByTestId("status-row-effort-value").textContent).toBe("(default)");
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
//
// The clock reports an IN-FLIGHT turn's elapsed time, so it renders only while
// one is running: a strip that keeps showing a frozen number implies otherwise.
// The banked total is still one click away in Session details.

test("adds the live elapsed time of the in-flight turn to workMillis while one is active", () => {
  const startedAt = new Date(1_000_000).toISOString();
  render(
    <StatusRow
      sessionRef="ref_a"
      model={runningModel({ workMillis: 60_000, activeTurnStartedAt: startedAt })}
      now={1_000_000 + 30_000}
    />,
  );
  // 60s accumulated + 30s of the still-running turn = 90s = "1m".
  expect(screen.getByTestId("status-row-work-time").textContent).toBe("1m");
});

test("shows no clock at all when no turn is running, even with banked work time", () => {
  render(<StatusRow sessionRef="ref_a" model={testModel({ workMillis: 90_000 })} now={1_000_000} />);
  expect(screen.queryByTestId("status-row-work-time")).toBeNull();
});

// THE FABRICATED "1s" BUG. formatWorkDuration clamps up to a 1s minimum so a
// real sub-second duration never reads "0s" - correct for a measurement, a lie
// for the absence of one. An unmeasured session (workMillis 0, which is Go's
// unset zero value on the wire, not a measurement of zero) must show NOTHING.
// Same gate DetailsPanel's own work-time row uses; formatWorkDuration itself is
// deliberately left alone.
test("an unmeasured work time renders no clock, never a fabricated '1s'", () => {
  const startedAt = new Date(1_000_000).toISOString();
  render(
    <StatusRow
      sessionRef="ref_a"
      model={runningModel({ workMillis: 0, activeTurnStartedAt: startedAt })}
      now={1_000_000}
    />,
  );
  expect(screen.queryByTestId("status-row-work-time")).toBeNull();
  expect(screen.getByTestId("status-row").textContent).not.toContain("1s");
});

// Wire-true reproduction of the W6-close punch item: a present-but-zero
// SerfThread.ActiveTurnStartedAt (the daemon's zero value) runs through the
// REAL reducer, whose epochMsToISO turns it into "1970-01-01T00:00:00.000Z"
// (proven here first), and the status row must NOT render the resulting
// now-minus-epoch span as an absurd "500000h" clock.
test("renders no now-minus-epoch clock for a zero-valued activeTurnStartedAt off the wire", () => {
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
  // the render takes the no-active-turn path - and therefore shows no clock.
  expect(model.activeTurnStartedAt).toBeUndefined();

  render(<StatusRow sessionRef="ref_a" model={model} now={now} />);
  expect(screen.queryByTestId("status-row-work-time")).toBeNull();
  expect(screen.getByTestId("status-row").textContent).not.toMatch(/\d+h/);
});

test("renders the banked work time, not an absurd span, if an epoch anchor reaches the model by another path", () => {
  // Defense-in-depth: statusFormat rejects at-or-before-epoch anchors on its
  // own, independent of the reducer's source guard.
  const now = 1_800_000_000_000;
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({
        status: { type: "active" },
        activeTurnId: "turn_1",
        workMillis: 45_000,
        activeTurnStartedAt: new Date(0).toISOString(),
      })}
      now={now}
    />,
  );
  expect(screen.getByTestId("status-row-work-time").textContent).toBe("45s");
});

// --- context gauge -----------------------------------------------------------

test("renders no context gauge when the thread has no context window data", () => {
  render(<StatusRow sessionRef="ref_a" model={runningModel({ contextWindow: 0 })} now={1_000_000} />);
  expect(screen.queryByRole("meter")).toBeNull();
});

test("renders the context gauge with the used/window counts in its accessible label", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={runningModel({ contextUsed: 12_000, contextWindow: 128_000, contextPressure: 0.09 })}
      now={1_000_000}
    />,
  );
  const meter = screen.getByRole("meter");
  expect(meter.getAttribute("aria-valuenow")).toBe("12000");
  expect(meter.getAttribute("aria-valuemax")).toBe("128000");
  expect(meter.getAttribute("aria-label")).toContain("12k of 128k");
});

// The gauge's severity comes from the shared contextTone ladder (see
// statusFormat.ts), which the details panel reads too - the same session must
// never look calm on one surface and alarming on the other. DetailsPanel.test
// pins the SAME three tiers against the same class table, so a drifted
// threshold breaks one surface's expectations without the other's.
const meterToneClass = {
  neutral: requireClass(rawMeterStyles.neutral, "meter.module.css", "neutral"),
  attention: requireClass(rawMeterStyles.attention, "meter.module.css", "attention"),
  danger: requireClass(rawMeterStyles.danger, "meter.module.css", "danger"),
};

test.each([
  { pressure: 0.42, tone: "neutral" as const },
  { pressure: 0.8, tone: "attention" as const },
  { pressure: 0.95, tone: "danger" as const },
])("the context gauge renders the $tone tone at pressure $pressure", ({ pressure, tone }) => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={runningModel({ contextUsed: pressure * 128_000, contextWindow: 128_000, contextPressure: pressure })}
      now={1_000_000}
    />,
  );
  expect(screen.getByTestId("meter-fill").classList.contains(meterToneClass[tone])).toBe(true);
});

// The gauge alone shows the pressure; a "12k / 128k" readout beside it said
// the same thing twice in a row that has to stay one line. The exact numbers
// are still reachable - spoken from the meter's label, and on hover from the
// title, following the row's "key value" tooltip convention.
test("shows no duplicate numeric readout beside the gauge, and puts the numbers in a hover tooltip", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={runningModel({ contextUsed: 12_000, contextWindow: 128_000, contextPressure: 0.09 })}
      now={1_000_000}
    />,
  );
  expect(screen.queryByText("12k / 128k")).toBeNull();
  expect(screen.getByTestId("status-row-context").getAttribute("title")).toBe("context 12k / 128k");
});

// --- cost --------------------------------------------------------------------

test("shows the session cost verbatim from the wire string when present", () => {
  render(<StatusRow sessionRef="ref_a" model={testModel({ cost: "~$1.23" })} now={1000} />);
  const cost = screen.getByTestId("status-row-cost");
  // The wire string is already formatted server-side (EstimateCost) — the row
  // displays it verbatim, never re-formatting a number client-side.
  expect(cost.textContent).toBe("~$1.23");
  expect(cost.getAttribute("title")).toContain("~$1.23");
});

test("shows no cost when the wire omits cost as null (honest unknown)", () => {
  render(<StatusRow sessionRef="ref_a" model={testModel({ cost: null })} now={1000} />);
  expect(screen.queryByTestId("status-row-cost")).toBeNull();
});

test("shows no cost when cost is absent (undefined) from the model", () => {
  render(<StatusRow sessionRef="ref_a" model={testModel({})} now={1000} />);
  expect(screen.queryByTestId("status-row-cost")).toBeNull();
});

// --- queue depth -------------------------------------------------------------
//
// Send's effect on a running session has to be visible somewhere, and the far
// right of the strip is cheaper than a second row of chrome.

test("shows the queue depth at the far right when messages are waiting", () => {
  render(<StatusRow sessionRef="ref_a" model={runningModel({ queue: { depth: 2 } })} now={1_000_000} />);
  expect(screen.getByTestId("status-row-queue").textContent).toBe("2 queued");
});

test("shows no queue readout when the queue is empty - the normal case is not news", () => {
  render(<StatusRow sessionRef="ref_a" model={runningModel({ queue: { depth: 0 } })} now={1_000_000} />);
  expect(screen.queryByTestId("status-row-queue")).toBeNull();
});

test("shows no queue readout when the wire carries no queue at all", () => {
  render(<StatusRow sessionRef="ref_a" model={runningModel({ queue: null })} now={1_000_000} />);
  expect(screen.queryByTestId("status-row-queue")).toBeNull();
});

// --- the ended state: an epitaph, not a cockpit ------------------------------
//
// A finished session's work and cost are settled, so the live row of instruments
// is replaced by one summary line. "notLoaded" is the shape a cold exited serf
// session actually arrives in (cmd/serf-hub/app_threadread.go's
// pastEntryThread), which is why it counts as ended here.

const ENDED_STATUSES = ["ended", "closed", "notLoaded"] as const;

test.each(ENDED_STATUSES)("a %s session's strip is one summary line of model, work and cost", (type) => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({
        status: { type },
        modelProvider: "anthropic",
        model: "claude-opus-5",
        workMillis: 840_000,
        cost: "~$1.83",
      })}
      now={1_000_000}
    />,
  );
  expect(screen.getByTestId("model-switch-value").textContent).toBe("anthropic/claude-opus-5");
  expect(screen.getByTestId("status-row-work-time").textContent).toBe("14m worked");
  expect(screen.getByTestId("status-row-cost").textContent).toBe("~$1.83");
});

// The model is the ONE live thing left on a finished session's strip: it can be
// sent to again (the hub advertises Send and ChangeModel for a cold exited
// thread and resumes it behind either), so the model its next turn runs on is
// still a choice a user must be able to make - without first knowing that
// "running" is a state a session has. The gauge and the effort control are not:
// context occupancy was never measured for an exited session (the hub builds its
// thread from persisted SessionMeta, which carries no context figures).
test("an ended session keeps a WORKING model switch - its next turn's model is still a choice", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ status: { type: "notLoaded" }, modelProvider: "anthropic", model: "claude-opus-5" })}
      now={1_000_000}
    />,
  );
  const trigger = screen.getByTestId("model-switch-trigger") as HTMLButtonElement;
  expect(trigger.disabled).toBe(false);
});

test("an ended session's strip still drops the dead instruments - no gauge, no effort control", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({
        status: { type: "notLoaded" },
        supportsReasoning: true,
        reasoningEffortLevels: ["low", "high"],
        contextUsed: 12_000,
        contextWindow: 128_000,
      })}
      now={1_000_000}
    />,
  );
  expect(screen.queryByRole("meter")).toBeNull();
  expect(screen.queryByRole("combobox", { name: /reasoning effort/i })).toBeNull();
});

// Same honesty rule as the live row's clock: an unmeasured work time is Go's
// unset zero, not a measurement, so it gets no row rather than a "1s".
test("an ended session with no measured work time shows model and cost only, no fabricated '1s'", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ status: { type: "notLoaded" }, workMillis: 0, cost: "~$0.40" })}
      now={1_000_000}
    />,
  );
  expect(screen.queryByTestId("status-row-work-time")).toBeNull();
  expect(screen.getByTestId("status-row").textContent).not.toContain("1s");
  expect(screen.getByTestId("status-row-cost").textContent).toBe("~$0.40");
});

test("an ended session with no cost shows the facts it has and nothing it doesn't", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ status: { type: "closed" }, workMillis: 90_000, cost: null })}
      now={1_000_000}
    />,
  );
  expect(screen.queryByTestId("status-row-cost")).toBeNull();
  expect(screen.getByTestId("status-row-work-time").textContent).toBe("1m worked");
});
