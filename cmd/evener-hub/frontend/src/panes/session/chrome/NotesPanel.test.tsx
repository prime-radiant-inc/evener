// NotesPanelBody: the ordered display rule plus the blur-save human editor
// and the live remove wiring. Mirrors DetailsPanel.test.tsx's harness
// (testModel with capability overrides); the body renders directly here
// (no Sheet trigger to click through - the desktop pane mounts the body).
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { NotesPanel, NotesPanelBody } from "./NotesPanel";

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

function openPanel(model: ThreadModel) {
  render(<NotesPanelBody sessionRef={model.ref} model={model} />);
}

function editor(): HTMLTextAreaElement {
  return screen.getByRole("textbox", { name: "Human note" }) as HTMLTextAreaElement;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
});

// --- rule 1: capability unset hides the panel body entirely -------------------

test("notes panel body renders nothing when capability unset", () => {
  openPanel(testModel({ capabilities: { ...FULL_CAPABILITIES, sharedNotes: false } }));
  expect(screen.queryByTestId("shared-notes-section")).toBeNull();
});

// --- rule 2: set but not live shows read-only --------------------------------

test("notes panel is read-only when ended: values shown, no editor, no remove buttons", () => {
  openPanel(
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
  expect(screen.queryByRole("textbox", { name: "Human note" })).toBeNull();
  expect(screen.queryByTestId("shared-notes-url-remove-u1")).toBeNull();
});

test("ended-empty session shows inert text with no editor", () => {
  openPanel(testModel({ status: { type: "notLoaded" } }));
  expect(screen.getByTestId("shared-notes-section")).toBeTruthy();
  expect(screen.getByTestId("shared-notes-empty")).toBeTruthy();
  expect(screen.queryByRole("textbox", { name: "Human note" })).toBeNull();
});

// --- rule 3: live shows the always-visible editor -----------------------------

test("live session with notes shows editor, agent note and remove", () => {
  openPanel(
    testModel({
      humanNote: "human hello",
      agentNote: "agent hello",
      sessionUrls: [{ id: "u1", url: "https://x.test/y", label: "x" }],
    }),
  );
  expect(editor().value).toBe("human hello");
  expect(screen.getByTestId("shared-notes-agent")).toBeTruthy();
  expect(screen.getByTestId("shared-notes-url-remove-u1")).toBeTruthy();
});

test("live-empty session shows an empty editor with placeholder", () => {
  openPanel(testModel());
  expect(editor().value).toBe("");
  expect(editor().placeholder).toMatch(/Add context/);
  expect(screen.getByTestId("shared-notes-agent-empty")).toBeTruthy();
  expect(screen.getByTestId("shared-notes-urls-empty")).toBeTruthy();
});

// --- blur saves through the threads store -------------------------------------

test("blurring the editor saves through the threads store", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let called: unknown;
  fake.on("notes/human/set", (params) => {
    called = params;
    return { note: "saved note" };
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  openPanel(model);
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "saved note");
  await user.tab();

  await waitFor(() => expect(called).toMatchObject({ ref: model.ref, note: "saved note" }));
  expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe("saved note");
  expect(await screen.findByTestId("shared-notes-saved")).toBeTruthy();
});

test("blurring with an unchanged draft saves nothing", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let calls = 0;
  fake.on("notes/human/set", () => {
    calls += 1;
    return { note: "old note" };
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  openPanel(model);
  await user.click(editor());
  await user.tab();

  await waitFor(() => expect(screen.queryByTestId("shared-notes-saving")).toBeNull());
  expect(calls).toBe(0);
});

test("a push while the editor is focused never clobbers typing", async () => {
  const user = userEvent.setup();
  connectFakeClient();
  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  const { rerender } = render(<NotesPanelBody sessionRef={model.ref} model={model} />);
  await user.click(editor());
  await user.type(editor(), " + typing");

  rerender(<NotesPanelBody sessionRef={model.ref} model={{ ...model, humanNote: "pushed note" }} />);
  expect(editor().value).toBe("old note + typing");
});

test("a push while unfocused reseeds the draft", async () => {
  connectFakeClient();
  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  const { rerender } = render(<NotesPanelBody sessionRef={model.ref} model={model} />);
  expect(editor().value).toBe("old note");

  rerender(<NotesPanelBody sessionRef={model.ref} model={{ ...model, humanNote: "pushed note" }} />);
  expect(editor().value).toBe("pushed note");
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
  openPanel(model);
  await user.click(screen.getByTestId("shared-notes-url-remove-u1"));

  await waitFor(() => expect(called).toMatchObject({ ref: model.ref, id: "u1" }));
});

// --- failure keeps the draft ---------------------------------------------------

test("a failed blur-save surfaces an error and keeps the draft", async () => {
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
      <NotesPanelBody sessionRef={model.ref} model={model} />
      <Toast />
    </>,
  );
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "draft note");
  await user.tab();

  await screen.findAllByText(/save note boom/i);
  expect(editor().value).toBe("draft note");
  expect(screen.getByTestId("shared-notes-error")).toBeTruthy();
});

// --- idle-wake warning -----------------------------------------------------------

test("idle live session shows the wake warning under the editor", () => {
  openPanel(testModel({ status: { type: "idle" }, humanNote: "old note" }));
  expect(screen.getByTestId("shared-notes-idle-wake").textContent).toMatch(/Saving will wake the agent/);
});

test("busy live session shows no idle-wake warning", () => {
  openPanel(testModel({ status: { type: "active" }, humanNote: "old note" }));
  expect(screen.queryByTestId("shared-notes-idle-wake")).toBeNull();
});

// --- mobile Sheet trigger ---------------------------------------------------------

test("NotesPanel trigger opens a sheet holding the notes body", async () => {
  const user = userEvent.setup();
  const model = testModel({ humanNote: "old note" });
  render(<NotesPanel sessionRef={model.ref} model={model} />);
  await user.click(screen.getByRole("button", { name: "Notes" }));
  expect(screen.getByRole("textbox", { name: "Human note" })).toBeTruthy();
});
