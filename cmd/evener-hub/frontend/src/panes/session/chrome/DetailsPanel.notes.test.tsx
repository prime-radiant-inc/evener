// Shared-notes section of DetailsPanelBody: the ordered display rule plus
// the live edit/remove wiring. Mirrors DetailsPanel.test.tsx's harness
// (testModel with capability overrides, DetailsPanel render + click to
// open the sheet).
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { DetailsPanel } from "./DetailsPanel";

const FULL_CAPABILITIES: ThreadCapabilities = {
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
  sharedNotes: true,
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
    visionModel: "",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    lastFrameAt: 0,
    capabilities: FULL_CAPABILITIES,
    goal: null,
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
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

async function openPanel(model: ThreadModel) {
  render(<DetailsPanel model={model} now={0} />);
  await userEvent.click(screen.getByRole("button", { name: "Details" }));
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
});

// --- rule 1: capability unset hides the section entirely ---------------------

test("shared notes section hides when capability unset", async () => {
  await openPanel(testModel({ capabilities: { ...FULL_CAPABILITIES, sharedNotes: false } }));
  expect(screen.queryByTestId("shared-notes-section")).toBeNull();
});

// --- rule 2: set but not live shows read-only --------------------------------

test("shared notes section is read-only when ended: values shown, no edit trigger, no remove buttons", async () => {
  await openPanel(
    testModel({
      status: { type: "ended" },
      humanNote: "human hello",
      agentNote: "agent hello",
      sessionUrls: [{ id: "u1", url: "https://x.test/y", label: "x" }],
    }),
  );
  expect(screen.getByTestId("shared-notes-section")).toBeTruthy();
  expect(screen.getByTestId("shared-notes-human").textContent).toMatch(/human hello/);
  expect(screen.getByTestId("shared-notes-agent").textContent).toMatch(/agent hello/);
  expect(screen.queryByTestId("shared-notes-edit")).toBeNull();
  expect(screen.queryByTestId("shared-notes-add-note")).toBeNull();
  expect(screen.queryByTestId("shared-notes-url-remove-u1")).toBeNull();
});

test("ended-empty session shows inert text with no trigger", async () => {
  await openPanel(testModel({ status: { type: "notLoaded" } }));
  expect(screen.getByTestId("shared-notes-section")).toBeTruthy();
  expect(screen.getByTestId("shared-notes-empty")).toBeTruthy();
  expect(screen.queryByTestId("shared-notes-add-note")).toBeNull();
  expect(screen.queryByTestId("shared-notes-edit")).toBeNull();
});

// --- rule 3: live shows full editing ------------------------------------------

test("live session with notes shows full section with edit and remove", async () => {
  await openPanel(
    testModel({
      humanNote: "human hello",
      agentNote: "agent hello",
      sessionUrls: [{ id: "u1", url: "https://x.test/y", label: "x" }],
    }),
  );
  expect(screen.getByTestId("shared-notes-edit")).toBeTruthy();
  expect(screen.getByTestId("shared-notes-url-remove-u1")).toBeTruthy();
});

test("live-empty session shows the Add a note trigger", async () => {
  await openPanel(testModel());
  expect(screen.getByTestId("shared-notes-add-note")).toBeTruthy();
});

test("live session with agent content still offers the first-note trigger", async () => {
  await openPanel(
    testModel({
      agentNote: "agent hello",
      sessionUrls: [{ id: "u1", url: "https://x.test/y", label: "x" }],
    }),
  );
  expect(screen.getByTestId("shared-notes-human")).toBeTruthy();
  expect(screen.getByTestId("shared-notes-add-note")).toBeTruthy();
  expect(screen.queryByTestId("shared-notes-edit")).toBeNull();
  // Clicking it opens the mounted editor (the pre-fix bug set popoverOpen
  // with no editor mounted).
  const user = userEvent.setup();
  await user.click(screen.getByTestId("shared-notes-add-note"));
  expect(screen.getByTestId("shared-notes-editor")).toBeTruthy();
});

test("first-note trigger saves through the threads store", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let called: unknown;
  fake.on("notes/human/set", (params) => {
    called = params;
    return { note: "first note" };
  });

  const model = testModel({ agentNote: "agent hello" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  await openPanel(model);
  await user.click(screen.getByTestId("shared-notes-add-note"));
  const editor = screen.getByTestId("shared-notes-editor");
  await user.type(within(editor).getByRole("textbox", { name: "Human note" }), "first note");
  await user.click(within(editor).getByRole("button", { name: /save/i }));

  await waitFor(() => expect(called).toMatchObject({ ref: model.ref, note: "first note" }));
  expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe("first note");
});

// --- edit dispatches RPC -------------------------------------------------------

test("edit dispatches notes/human/set through the threads store", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let called: unknown;
  fake.on("notes/human/set", (params) => {
    called = params;
    return { note: "saved note" };
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  await openPanel(model);
  await user.click(screen.getByTestId("shared-notes-edit"));
  const editor = screen.getByTestId("shared-notes-editor");
  await user.clear(within(editor).getByRole("textbox", { name: "Human note" }));
  await user.type(within(editor).getByRole("textbox", { name: "Human note" }), "saved note");
  await user.click(within(editor).getByRole("button", { name: /save/i }));

  await waitFor(() => expect(called).toMatchObject({ ref: model.ref, note: "saved note" }));
  expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe("saved note");
});

test("remove dispatches urls/remove", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let called: unknown;
  fake.on("urls/remove", (params) => {
    called = params;
    return {};
  });

  const model = testModel({ sessionUrls: [{ id: "u1", url: "https://x.test/y", label: "x" }] });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  await openPanel(model);
  await user.click(screen.getByTestId("shared-notes-url-remove-u1"));

  await waitFor(() => expect(called).toMatchObject({ ref: model.ref, id: "u1" }));
});

// --- push rerenders --------------------------------------------------------------

test("an evener/notes/updated push rerenders the section", async () => {
  const fake = connectFakeClient();
  const model = testModel();
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  const { rerender } = render(<DetailsPanel model={model} now={0} />);
  await userEvent.click(screen.getByRole("button", { name: "Details" }));
  // Live-but-empty renders the human row carrying only the Add-a-note
  // trigger (the explicit affordance); no note text yet.
  expect(screen.getByTestId("shared-notes-human").textContent).toMatch(/Add a note/);

  fake.emitNotification({
    method: "evener/notes/updated",
    params: { threadId: model.threadId, ref: model.ref, humanNote: "pushed note", agentNote: "" },
  });
  const committed = threadsStore.getState().threads.get(model.ref);
  if (!committed) throw new Error("tracked model disappeared");
  rerender(<DetailsPanel model={committed} now={0} />);
  expect(screen.getByTestId("shared-notes-human").textContent).toMatch(/pushed note/);
});

// --- failure toast keeps draft ------------------------------------------------------

test("a failed save surfaces an error toast and keeps the draft in the popover", async () => {
  const user = userEvent.setup();
  connectFakeClient();
  const fake = connectionStore.getState().client as FakeClient;
  fake.on("notes/human/set", () => {
    throw new Error("save note boom");
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  render(
    <>
      <DetailsPanel model={model} now={0} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "Details" }));
  await user.click(screen.getByTestId("shared-notes-edit"));
  const editor = screen.getByTestId("shared-notes-editor");
  await user.clear(within(editor).getByRole("textbox", { name: "Human note" }));
  await user.type(within(editor).getByRole("textbox", { name: "Human note" }), "draft note");
  await user.click(within(editor).getByRole("button", { name: /save/i }));

  await screen.findByText(/save note boom/i);
  // The popover stays open showing the kept draft.
  expect(
    (
      within(screen.getByTestId("shared-notes-editor")).getByRole("textbox", {
        name: "Human note",
      }) as HTMLTextAreaElement
    ).value,
  ).toBe("draft note");
});

// --- idle-wake warning -----------------------------------------------------------

test("idle live session editor warns that saving will wake the agent", async () => {
  const user = userEvent.setup();
  connectFakeClient();
  const model = testModel({ status: { type: "idle" }, humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  await openPanel(model);
  await user.click(screen.getByTestId("shared-notes-edit"));
  expect(screen.getByTestId("shared-notes-editor")).toBeTruthy();
  expect(screen.getByTestId("shared-notes-idle-wake").textContent).toMatch(/Saving will wake the agent/);
});

test("busy live session editor shows no idle-wake warning", async () => {
  const user = userEvent.setup();
  connectFakeClient();
  const model = testModel({ status: { type: "active" }, humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  await openPanel(model);
  await user.click(screen.getByTestId("shared-notes-edit"));
  expect(screen.getByTestId("shared-notes-editor")).toBeTruthy();
  expect(screen.queryByTestId("shared-notes-idle-wake")).toBeNull();
});
