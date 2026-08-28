import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { ModelListResponse, ThreadCapabilities } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { VisionModelSwitch } from "./VisionModelSwitch";

const CAPABILITIES: ThreadCapabilities = {
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

function testModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "",
    status: { type: "idle" },
    modelProvider: "anthropic",
    model: "claude-sonnet-4-5",
    reasoningEffort: undefined,
    visionModel: "",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
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
    ...rest,
    jobsTreeRevision,
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
      { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "Claude Sonnet 4.5", supportsVision: false },
      { provider: "openai", model: "gpt-5.5", displayName: "GPT-5.5", supportsVision: true },
      { provider: "openai", model: "gpt-4o", displayName: "GPT-4o", supportsVision: false },
    ],
  };
}

function trigger(): HTMLButtonElement {
  return screen.getByTestId("vision-model-switch-trigger") as HTMLButtonElement;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  resetToastStoreForTests();
});

test("unset uses the session model label and Current model sends an empty wire ref", async () => {
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());
  let called: unknown;
  fake.on("thread/vision-model/set", (params) => {
    called = params;
    return {};
  });

  const user = userEvent.setup();
  render(<VisionModelSwitch sessionRef="ref_a" model={testModel()} />);
  expect(screen.getByTestId("vision-model-switch-value").textContent).toBe("anthropic/claude-sonnet-4-5");
  await user.click(trigger());
  await screen.findByRole("combobox");
  await waitFor(() => expect(screen.getByRole("option", { name: /Current model/i })).toBeTruthy());
  await user.click(screen.getByRole("option", { name: /Current model/i }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a", visionModel: "" }));
});

test("the vision trigger names its distinct action for assistive technology", () => {
  render(<VisionModelSwitch sessionRef="ref_a" model={testModel()} />);
  expect(screen.getByRole("button", { name: "anthropic/claude-sonnet-4-5 — change vision model" })).toBe(trigger());
});

test("off renders an Off trigger label", () => {
  render(<VisionModelSwitch sessionRef="ref_a" model={testModel({ visionModel: "off" })} />);
  expect(screen.getByTestId("vision-model-switch-value").textContent).toBe("Off");
});

test("case-insensitive off sentinel renders an Off trigger label", () => {
  render(<VisionModelSwitch sessionRef="ref_a" model={testModel({ visionModel: "OfF" })} />);
  expect(screen.getByTestId("vision-model-switch-value").textContent).toBe("Off");
});

test("a pinned vision ref renders unchanged", () => {
  render(<VisionModelSwitch sessionRef="ref_a" model={testModel({ visionModel: "openai/gpt-5.5" })} />);
  expect(screen.getByTestId("vision-model-switch-value").textContent).toBe("openai/gpt-5.5");
});

test("only vision-capable catalog rows appear and picking one sends its canonical ref", async () => {
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());
  let called: unknown;
  fake.on("thread/vision-model/set", (params) => {
    called = params;
    return {};
  });

  const user = userEvent.setup();
  render(<VisionModelSwitch sessionRef="ref_a" model={testModel()} />);
  await user.click(trigger());
  await screen.findByRole("combobox");
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  expect(screen.getByRole("option", { name: /GPT-5\.5/i })).toBeTruthy();
  expect(screen.queryByRole("option", { name: /gpt-4o/i })).toBeNull();
  await user.click(screen.getByRole("option", { name: /GPT-5\.5/i }));

  await waitFor(() => expect(called).toEqual({ ref: "ref_a", visionModel: "openai/gpt-5.5" }));
});

test("changeVisionModel capability disables the trigger", () => {
  render(
    <VisionModelSwitch
      sessionRef="ref_a"
      model={testModel({ capabilities: { ...CAPABILITIES, changeVisionModel: false } })}
    />,
  );
  expect(trigger().disabled).toBe(true);
});

test("a failed vision-model write surfaces an error toast", async () => {
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());
  fake.on("thread/vision-model/set", () => {
    throw new Error("vision switch boom");
  });

  const user = userEvent.setup();
  render(
    <>
      <VisionModelSwitch sessionRef="ref_a" model={testModel()} />
      <Toast />
    </>,
  );
  await user.click(trigger());
  await screen.findByRole("combobox");
  await waitFor(() => expect(screen.getByRole("option", { name: /GPT-5\.5/i })).toBeTruthy());
  await user.click(screen.getByRole("option", { name: /GPT-5\.5/i }));

  expect(await screen.findByText(/vision switch boom/i)).toBeTruthy();
});
