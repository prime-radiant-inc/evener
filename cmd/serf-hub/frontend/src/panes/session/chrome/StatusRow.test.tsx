import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
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
        reasoningEffortLevels: ["low", "medium", "high"],
        reasoningEffort: "medium",
      })}
      now={1000}
    />,
  );
  const select = screen.getByRole("combobox", { name: /reasoning effort/i }) as HTMLSelectElement;
  expect(select.value).toBe("medium");
  expect(Array.from(select.options).map((o) => o.value)).toEqual(["low", "medium", "high"]);
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

test("shows the current reasoning effort as plain text when the model supports it but offers no switchable ladder", () => {
  render(
    <StatusRow
      sessionRef="ref_a"
      model={testModel({ supportsReasoning: true, reasoningEffortLevels: [], reasoningEffort: "high" })}
      now={1000}
    />,
  );
  expect(screen.queryByRole("combobox", { name: /reasoning effort/i })).toBeNull();
  expect(screen.getByText("high")).toBeTruthy();
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
