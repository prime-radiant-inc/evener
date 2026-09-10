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

// --- save coalescing -------------------------------------------------------------

test("a second blur while a save is in flight replays the latest draft instead of dropping it", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  const seen: unknown[] = [];
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let first = true;
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    if (first) {
      first = false;
      return gate.then(() => ({ note: (params as { note: string }).note }));
    }
    return { note: (params as { note: string }).note };
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  openPanel(model);
  // First blur starts the gated save...
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "first draft");
  await user.tab();
  await waitFor(() => expect(seen).toHaveLength(1));
  // ...while it is in flight, a second edit + blur parks (not drops) the
  // newer draft; releasing the gate lets the loop replay it.
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "second draft");
  await user.tab();
  release();
  await waitFor(() => expect(seen).toHaveLength(2));
  expect(seen[1]).toMatchObject({ ref: model.ref, note: "second draft" });
  expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe("second draft");
});

test("a B-save parking behind an in-flight A-save persists instead of overwriting A", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  const seen: unknown[] = [];
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let first = true;
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    if (first) {
      first = false;
      return gate.then(() => ({ note: (params as { note: string }).note }));
    }
    return { note: (params as { note: string }).note };
  });

  const modelA = testModel({ ref: "local:aaaa", threadId: "aaaa", humanNote: "note A" });
  const modelB = testModel({ ref: "local:bbbb", threadId: "bbbb", humanNote: "note B" });
  threadsStore.setState({ threads: new Map([[modelA.ref, modelA]]) });
  const { rerender } = render(<NotesPanelBody sessionRef={modelA.ref} model={modelA} />);
  // A's blur starts the gated save...
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "draft A2");
  await user.tab();
  await waitFor(() => expect(seen).toHaveLength(1));
  // ...then the panel switches to B, whose blur parks behind A's loop. B
  // must not overwrite A's parked draft, and the loop must drain both.
  threadsStore.setState({
    threads: new Map([
      [modelA.ref, modelA],
      [modelB.ref, modelB],
    ]),
  });
  rerender(<NotesPanelBody sessionRef={modelB.ref} model={modelB} />);
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "draft B2");
  await user.tab();
  release();
  await waitFor(() => expect(seen).toHaveLength(2));
  expect(seen[0]).toMatchObject({ ref: modelA.ref, note: "draft A2" });
  expect(seen[1]).toMatchObject({ ref: modelB.ref, note: "draft B2" });
  expect(threadsStore.getState().threads.get(modelA.ref)?.humanNote).toBe("draft A2");
  expect(threadsStore.getState().threads.get(modelB.ref)?.humanNote).toBe("draft B2");
});

test("a draft parked behind a failed save is kept and retried on the next blur", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  const seen: unknown[] = [];
  let calls = 0;
  fake.on("notes/human/set", (params) => {
    calls += 1;
    seen.push(params);
    if (calls === 1) throw new Error("first save boom");
    return { note: (params as { note: string }).note };
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
  await user.type(editor(), "first draft");
  await user.tab();
  await screen.findAllByText(/first save boom/i);
  // A newer draft typed after the failure must survive it: the next blur
  // retries the queue and lands on the newest text.
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "second draft");
  await user.tab();
  await waitFor(() => expect(seen).toHaveLength(2));
  expect(seen[1]).toMatchObject({ ref: model.ref, note: "second draft" });
  expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe("second draft");
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

// --- flush paths ---------------------------------------------------------------

test("switching sessions flushes the outgoing dirty draft against the old thread", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  const seen: unknown[] = [];
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    return { note: (params as { note: string }).note };
  });

  const modelA = testModel({ ref: "local:aaaa", threadId: "aaaa", humanNote: "note A" });
  // Session B's stored note deliberately EQUALS A's dirty draft: the
  // pre-fix cleanup compared the old draft against the new stored note and
  // skipped the save on the coincidence.
  const modelB = testModel({ ref: "local:bbbb", threadId: "bbbb", humanNote: "dirty A draft" });
  threadsStore.setState({ threads: new Map([[modelA.ref, modelA]]) });
  const { rerender } = render(<NotesPanelBody sessionRef={modelA.ref} model={modelA} />);
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "dirty A draft");
  // Switch without blurring: the sessionRef-change cleanup is the flush
  // under test (blur never fires on a prop-driven panel swap).
  threadsStore.setState({ threads: new Map([[modelB.ref, modelB]]) });
  rerender(<NotesPanelBody sessionRef={modelB.ref} model={modelB} />);

  await waitFor(() => expect(seen).toHaveLength(1));
  expect(seen[0]).toMatchObject({ ref: modelA.ref, note: "dirty A draft" });
});

test("a live-to-ended transition flushes the dirty draft before the editor unmounts", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  const seen: unknown[] = [];
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    return { note: (params as { note: string }).note };
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  const { rerender } = render(<NotesPanelBody sessionRef={model.ref} model={model} />);
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "final words");
  // The session ends with the textarea focused: the editor unmounts for the
  // read-only view without blur firing, and the flush must still save.
  rerender(<NotesPanelBody sessionRef={model.ref} model={{ ...model, status: { type: "ended" } }} />);

  await waitFor(() => expect(seen).toHaveLength(1));
  expect(seen[0]).toMatchObject({ ref: model.ref, note: "final words" });
  expect(screen.queryByRole("textbox", { name: "Human note" })).toBeNull();
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
