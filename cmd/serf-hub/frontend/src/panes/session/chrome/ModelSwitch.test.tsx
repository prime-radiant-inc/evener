import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { ModelListResponse, ThreadCapabilities } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { ModelSwitch } from "./ModelSwitch";

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
    model: "claude-sonnet-4-5",
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

function modelListResponse(): ModelListResponse {
  return {
    data: [
      { provider: "anthropic", model: "claude-sonnet-4-5" },
      { provider: "anthropic", model: "claude-opus-5" },
      { provider: "openai", model: "gpt-5.5" },
    ],
  };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
});

test("shows the current model as a passive chip alongside a Change-model trigger", () => {
  render(<ModelSwitch sessionRef="ref_a" model={testModel()} />);
  expect(screen.getByText("anthropic/claude-sonnet-4-5")).toBeTruthy();
  expect(screen.getByRole("button", { name: /change model/i })).toBeTruthy();
  expect(screen.queryByRole("combobox")).toBeNull();
});

test("the trigger is disabled when the thread's changeModel capability is unavailable", () => {
  render(
    <ModelSwitch sessionRef="ref_a" model={testModel({ capabilities: { ...CAPABILITIES, changeModel: false } })} />,
  );
  expect((screen.getByRole("button", { name: /change model/i }) as HTMLButtonElement).disabled).toBe(true);
});

// Busy-gate: a model switch mid-turn is refused by the daemon, so the trigger
// follows the LIVE turn state (isTurnActive - the same predicate Composer's
// Stop/Steer gate uses), not only the static changeModel capability.
test("the trigger is disabled while a turn is active, even when changeModel is capable", () => {
  render(<ModelSwitch sessionRef="ref_a" model={testModel({ status: { type: "active" }, activeTurnId: "turn_1" })} />);
  expect((screen.getByRole("button", { name: /change model/i }) as HTMLButtonElement).disabled).toBe(true);
});

test("the trigger is enabled when idle and changeModel-capable", () => {
  render(<ModelSwitch sessionRef="ref_a" model={testModel()} />);
  expect((screen.getByRole("button", { name: /change model/i }) as HTMLButtonElement).disabled).toBe(false);
});

test("opening the picker fetches the catalog via listModels and shows a loading state until it resolves", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  const box: { resolve: ((r: ModelListResponse) => void) | null } = { resolve: null };
  fake.on("model/list", () => new Promise((resolve) => (box.resolve = resolve)));

  render(<ModelSwitch sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: /change model/i }));

  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
  box.resolve?.(modelListResponse());
  await waitFor(() => expect(screen.getByRole("combobox")).toBeTruthy());
});

test("renders every catalog entry as an option immediately once loaded, with no keystroke", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());

  render(<ModelSwitch sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox");

  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
});

test("typing filters the option list by provider/model substring", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());

  render(<ModelSwitch sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: /change model/i }));
  const combobox = await screen.findByRole("combobox");
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  await user.clear(combobox);
  await user.keyboard("opus");

  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
  expect(screen.getByRole("option", { name: /claude-opus-5/i })).toBeTruthy();
});

test("picking an option calls setModel with that option's provider/model and closes the picker", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());
  let called: unknown;
  fake.on("thread/model/set", (params) => {
    called = params;
    return {};
  });

  render(<ModelSwitch sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: /change model/i }));
  const combobox = await screen.findByRole("combobox");
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  await user.clear(combobox);
  await user.keyboard("gpt");
  await waitFor(() => expect(screen.getByRole("option", { name: /gpt-5\.5/i })).toBeTruthy());
  await user.click(screen.getByRole("option", { name: /gpt-5\.5/i }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a", modelProvider: "openai", model: "gpt-5.5" }));
  expect(screen.queryByRole("combobox")).toBeNull();
});

// A failed load surfaces the reason inline, in the open panel, over the search
// field (which stays: the panel is still dismissable and the field still holds
// the current model). No option list, since there is no catalog to list.
test("a failed catalog fetch surfaces the error inline, keeping the field and the trigger", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => {
    throw new Error("catalog boom");
  });

  render(
    <>
      <ModelSwitch sessionRef="ref_a" model={testModel()} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: /change model/i }));

  expect((await screen.findByRole("alert")).textContent).toMatch(/catalog boom/i);
  expect(screen.queryByRole("listbox")).toBeNull();
  expect(screen.getByRole("combobox")).toBeTruthy();
  expect(screen.getByRole("button", { name: /change model/i })).toBeTruthy();
});

// The picker is dismissable by Escape and by an outside click, not only by
// its Cancel button (parity with the legacy live picker's dismiss affordances).
test("pressing Escape closes an open picker", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());

  render(<ModelSwitch sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox");
  await user.keyboard("{Escape}");

  await waitFor(() => expect(screen.queryByRole("combobox")).toBeNull());
  // Reverts to the passive chip + trigger, unchanged.
  expect(screen.getByRole("button", { name: /change model/i })).toBeTruthy();
});

// The picker's own list scrolls, and the transcript behind it can scroll too:
// neither may dismiss it (it used to close on any scroll).
test("a scroll does not close the open picker", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());

  render(<ModelSwitch sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox");
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  window.dispatchEvent(new Event("scroll"));

  expect(screen.getByRole("combobox")).toBeTruthy();
});

// Popover runs with autoFocus={false} so the panel's input can own focus and
// its selection, which makes restoring focus to the trigger on close
// ModelSwitch's own job. Without it, focus falls to <body>.
test("closing returns focus to the trigger", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());

  render(<ModelSwitch sessionRef="ref_a" model={testModel()} />);
  const trigger = screen.getByRole("button", { name: /change model/i });
  await user.click(trigger);
  await screen.findByRole("combobox");
  await user.keyboard("{Escape}");

  await waitFor(() => expect(screen.queryByRole("combobox")).toBeNull());
  expect(document.activeElement).toBe(trigger);
});

test("a click outside the open picker closes it", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());

  render(
    <div>
      <button type="button" data-testid="outside">
        outside
      </button>
      <ModelSwitch sessionRef="ref_a" model={testModel()} />
    </div>,
  );
  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox");
  await user.click(screen.getByTestId("outside"));

  await waitFor(() => expect(screen.queryByRole("combobox")).toBeNull());
});

test("a failed setModel surfaces an error toast - the picker is already closed (optimistic close), no rollback", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());
  fake.on("thread/model/set", () => {
    throw new Error("switch boom");
  });

  render(
    <>
      <ModelSwitch sessionRef="ref_a" model={testModel()} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: /change model/i }));
  const combobox = await screen.findByRole("combobox");
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  await user.clear(combobox);
  await user.keyboard("gpt");
  await waitFor(() => expect(screen.getByRole("option", { name: /gpt-5\.5/i })).toBeTruthy());
  await user.click(screen.getByRole("option", { name: /gpt-5\.5/i }));

  await screen.findByText(/switch boom/i);
  expect(screen.queryByRole("combobox")).toBeNull();
});
