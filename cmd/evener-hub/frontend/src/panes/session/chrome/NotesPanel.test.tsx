// NotesPanelBody: the ordered display rule plus the blur-save human editor
// and the live remove wiring. Mirrors DetailsPanel.test.tsx's harness
// (testModel with capability overrides); the body renders directly here
// (no Sheet trigger to click through - the desktop pane mounts the body).
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory } from "fake-indexeddb";
import { createRef } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { useStore } from "zustand";
import { WireError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { MutationOutboxIndexedDB } from "../../../stores/mutationOutboxIndexedDB";
import {
  readMutationPersistence,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  threadsStore,
} from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { fileURLToPath, NotesPanel, NotesPanelBody, type NotesPanelHandle } from "./NotesPanel";

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

function noteResponse(params: { clientMutationId: string; note?: string }, note = params.note ?? "") {
  return {
    note,
    receipt: {
      clientMutationId: params.clientMutationId,
      threadId: "thread",
      disposition: "applied",
      projectionState: "notProjected",
    },
  };
}

function openPanel(model: ThreadModel) {
  render(<NotesPanelBody sessionRef={model.ref} model={model} />);
}

function editor(): HTMLTextAreaElement {
  return screen.getByRole("textbox", { name: "Human note" }) as HTMLTextAreaElement;
}

function LivePanel({ sessionRef }: { sessionRef: string }) {
  const model = useStore(threadsStore, (state) => state.threads.get(sessionRef));
  return model ? <NotesPanelBody sessionRef={sessionRef} model={model} /> : null;
}

function clockClient() {
  const indexedDB = new IDBFactory();
  vi.stubGlobal("indexedDB", indexedDB);
  vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  setMutationStorageForTests(new MutationOutboxIndexedDB({ indexedDB }));
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  return { fake: connectFakeClient(), user: userEvent.setup({ advanceTimers: vi.advanceTimersByTime }) };
}

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

test("an actual blur retains the last pane's subscription through the deadline", async () => {
  const { fake, user } = clockClient();
  const model = testModel();
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  await threadsStore.getState().ensureThread(model.ref);
  let resolve!: () => void;
  const submitted = new Promise<void>((done) => {
    resolve = done;
  });
  const seen: unknown[] = [];
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    resolve();
    return noteResponse(params);
  });
  const panel = render(<LivePanel sessionRef={model.ref} />);
  await user.type(editor(), "closed sentinel");
  await user.tab();
  panel.unmount();
  threadsStore.getState().releaseThread(model.ref);
  expect(threadsStore.getState().threads.has(model.ref)).toBe(true);
  await advance(9_999);
  expect(seen).toHaveLength(0);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
    await submitted;
  });
  expect(seen).toHaveLength(1);
});

test("two real panels share text and any same-session focus cancels the one timer", async () => {
  const { fake, user } = clockClient();
  const model = testModel();
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  const seen: unknown[] = [];
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    return noteResponse(params);
  });
  render(
    <>
      <LivePanel sessionRef={model.ref} />
      <LivePanel sessionRef={model.ref} />
    </>,
  );
  const editors = screen.getAllByRole("textbox", { name: "Human note" }) as HTMLTextAreaElement[];
  await user.type(editors[0]!, "shared sentinel");
  expect(editors[1]!.value).toBe("shared sentinel");
  await user.tab(); // transfers focus directly to the second editor
  await advance(10_000);
  expect(seen).toHaveLength(0);
  await user.tab(); // actual last-owner blur
  await advance(9_999);
  await user.click(editors[0]!);
  await advance(10_000);
  expect(seen).toHaveLength(0);
});

test("closing without blur keeps the shared draft without inventing a save", async () => {
  const { fake, user } = clockClient();
  const model = testModel();
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  const seen: unknown[] = [];
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    return noteResponse(params);
  });
  const panel = render(<LivePanel sessionRef={model.ref} />);
  await user.type(editor(), "unsubmitted sentinel");
  panel.unmount();
  await advance(10_000);
  render(<LivePanel sessionRef={model.ref} />);
  expect(editor().value).toBe("unsubmitted sentinel");
  expect(seen).toHaveLength(0);
});

test("clean focused editors accept authoritative store updates without a write", async () => {
  const { fake, user } = clockClient();
  const model = testModel({ humanNote: "old sentinel" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  const seen: unknown[] = [];
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    return noteResponse(params);
  });
  render(<LivePanel sessionRef={model.ref} />);
  await user.click(editor());
  act(() => threadsStore.setState({ threads: new Map([[model.ref, { ...model, humanNote: "remote sentinel" }]]) }));
  expect(editor().value).toBe("remote sentinel");
  await user.tab();
  await advance(10_000);
  expect(seen).toHaveLength(0);
});

test.each(["ended", "capability", "instance"])(
  "deadline rechecks %s without changing the original fence",
  async (loss) => {
    const { fake, user } = clockClient();
    const model = testModel({ instanceId: "original-instance" });
    threadsStore.setState({ threads: new Map([[model.ref, model]]) });
    await threadsStore.getState().ensureThread(model.ref);
    const seen: unknown[] = [];
    fake.on("notes/human/set", (params) => {
      seen.push(params);
      return noteResponse(params);
    });
    render(<LivePanel sessionRef={model.ref} />);
    await user.type(editor(), "retained sentinel");
    await user.tab();
    const changed =
      loss === "ended"
        ? { ...model, status: { type: "ended" as const } }
        : loss === "capability"
          ? { ...model, capabilities: { ...model.capabilities, sharedNotes: false } }
          : { ...model, instanceId: "replacement-instance" };
    act(() => threadsStore.setState({ threads: new Map([[model.ref, changed]]) }));
    await advance(10_000);
    expect(seen).toHaveLength(0);
    act(() => threadsStore.setState({ threads: new Map([[model.ref, model]]) }));
    expect(editor().value).toBe("retained sentinel");
    expect(screen.getByTestId("shared-notes-error")).toBeTruthy();
  },
);

test("a definite refusal stays visible and keeps its draft across close and reopen", async () => {
  const { fake, user } = clockClient();
  const model = testModel();
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  await threadsStore.getState().ensureThread(model.ref);
  fake.on("notes/human/set", (params) => {
    throw new WireError("refusal sentinel", -32013, {
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
    });
  });
  const panel = render(<LivePanel sessionRef={model.ref} />);
  await user.type(editor(), "failed sentinel");
  await user.tab();
  await advance(10_000);
  expect(await screen.findByTestId("shared-notes-error")).toBeTruthy();
  panel.unmount();
  render(<LivePanel sessionRef={model.ref} />);
  expect(editor().value).toBe("failed sentinel");
  expect(screen.getByTestId("shared-notes-error")).toBeTruthy();
});

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

test("dirty blur sends nothing at 9999ms and one raw note at 10000ms", async () => {
  const indexedDB = new IDBFactory();
  vi.stubGlobal("indexedDB", indexedDB);
  // Testing Library's async wrapper detects fake clocks through Jest's API.
  vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  setMutationStorageForTests(new MutationOutboxIndexedDB({ indexedDB }));
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  const fake = connectFakeClient();
  const model = testModel();
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  const seen: unknown[] = [];
  let submittedResolve!: () => void;
  const submitted = new Promise<void>((resolve) => {
    submittedResolve = resolve;
  });
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    submittedResolve();
    return noteResponse(params);
  });
  openPanel(model);
  await user.type(editor(), "draft sentinel");
  await user.tab();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(9_999);
  });
  expect(seen).toHaveLength(0);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
    await submitted;
  });
  expect(seen).toHaveLength(1);
  expect(seen[0]).toMatchObject({ ref: model.ref, note: "draft sentinel" });
});

test("a pending blur save is flushed when the page goes away", async () => {
  const indexedDB = new IDBFactory();
  vi.stubGlobal("indexedDB", indexedDB);
  vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  setMutationStorageForTests(new MutationOutboxIndexedDB({ indexedDB }));
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  const fake = connectFakeClient();
  const model = testModel();
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  const seen: unknown[] = [];
  let submittedResolve!: () => void;
  const submitted = new Promise<void>((resolve) => {
    submittedResolve = resolve;
  });
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    submittedResolve();
    return noteResponse(params);
  });
  openPanel(model);
  await user.type(editor(), "draft sentinel");
  await user.tab(); // schedules the delayed blur save
  expect(seen).toHaveLength(0);

  // The page goes away inside the debounce window: the pending save is
  // flushed, not dropped the way a bare timer clear would drop it. The one
  // fake-timer tick yields so the flushed save's real IndexedDB dispatch can
  // run; far below the 10s debounce, whatever lands came from the flush.
  await act(async () => {
    window.dispatchEvent(new Event("pagehide"));
    await vi.advanceTimersByTimeAsync(1);
    await submitted;
  });
  expect(seen).toHaveLength(1);
  expect(seen[0]).toMatchObject({ ref: model.ref, note: "draft sentinel" });
});

test("a flushed blur save does not resubmit when its original deadline passes", async () => {
  const indexedDB = new IDBFactory();
  vi.stubGlobal("indexedDB", indexedDB);
  vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  setMutationStorageForTests(new MutationOutboxIndexedDB({ indexedDB }));
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  const fake = connectFakeClient();
  const model = testModel();
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  const setHumanNote = vi.spyOn(threadsStore.getState(), "setHumanNote");
  const seen: unknown[] = [];
  let submittedResolve!: () => void;
  const submitted = new Promise<void>((resolve) => {
    submittedResolve = resolve;
  });
  // Hold the first write's acknowledgement open: the teardown flush submits the
  // note, but the draft stays dirty and `submitting` while the whole debounce
  // window elapses. That is the surviving-pagehide (bfcache) window in which the
  // original 10s deadline must not submit the same note a second time.
  let releaseAck!: () => void;
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    submittedResolve();
    return new Promise<ReturnType<typeof noteResponse>>((resolve) => {
      releaseAck = () => resolve(noteResponse(params));
    });
  });
  openPanel(model);
  await user.type(editor(), "draft sentinel");
  await user.tab(); // schedules the delayed blur save
  await act(async () => {
    window.dispatchEvent(new Event("pagehide"));
    await vi.advanceTimersByTimeAsync(1);
    await submitted;
  });
  expect(seen).toHaveLength(1);

  // The page stayed alive past the original deadline while the acknowledgement
  // was still outstanding.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10_000);
  });
  expect(seen).toHaveLength(1);

  // Releasing the acknowledgement must not release a second, queued copy of the
  // same note: one edit is one write, however the teardown interleaves.
  await act(async () => {
    releaseAck();
    await vi.advanceTimersByTimeAsync(10_000);
  });
  expect(seen).toHaveLength(1);
  expect(setHumanNote).toHaveBeenCalledTimes(1);
  setHumanNote.mockRestore();
});

test("a malformed file URL never becomes an open-beside target", () => {
  expect(fileURLToPath("file:///tmp/with%20space.md")).toBe("/tmp/with space.md");
  // A malformed escape keeps the undecoded path instead of the whole URL.
  expect(fileURLToPath("file:///tmp/bad%zz.md")).toBe("/tmp/bad%zz.md");
  // A string that is not a URL yields no path at all.
  expect(fileURLToPath("not a url")).toBe("");
});

// --- rule 1: capability unset hides the panel body entirely -------------------

test("notes panel body renders nothing when capability unset", () => {
  openPanel(testModel({ capabilities: { ...FULL_CAPABILITIES, sharedNotes: false } }));
  expect(screen.queryByTestId("shared-notes-section")).toBeNull();
});

test("standalone Notes navigation hides when capability is unset", () => {
  const model = testModel({ capabilities: { ...FULL_CAPABILITIES, sharedNotes: false } });
  render(<NotesPanel sessionRef={model.ref} model={model} />);

  expect(screen.queryByRole("button", { name: "Notes" })).toBeNull();
});

test.each(["idle", "ended", "notLoaded", "restartRequired"] as const)(
  "imperative Notes opening rechecks capability for %s sessions",
  async (status) => {
    const user = userEvent.setup();
    const handle = createRef<NotesPanelHandle>();
    const supported = testModel({ status: { type: status }, humanNote: "imperative note sentinel" });
    const unsupported = { ...supported, capabilities: { ...FULL_CAPABILITIES, sharedNotes: false } };
    const panel = render(<NotesPanel ref={handle} sessionRef={supported.ref} model={supported} hideTrigger />);
    panel.rerender(<NotesPanel ref={handle} sessionRef={supported.ref} model={unsupported} hideTrigger />);

    act(() => handle.current?.open());
    expect.soft(screen.queryByRole("dialog", { name: "Session notes" })).toBeNull();
    // Keep the positive control runnable on the unguarded implementation too.
    const close = screen.queryByRole("button", { name: "Close" });
    if (close) await user.click(close);

    panel.rerender(<NotesPanel ref={handle} sessionRef={supported.ref} model={supported} hideTrigger />);
    act(() => handle.current?.open());
    expect(screen.getByRole("dialog", { name: "Session notes" })).toBeTruthy();
    if (status === "idle") expect(editor().value).toBe("imperative note sentinel");
    else expect(screen.getByTestId("shared-notes-human").textContent).toBe("imperative note sentinel");
  },
);

// The sheet's Notes button is read navigation, not a forbidden edit trigger.
// Hiding it on all non-live sessions would remove this supported read path.
test.each(["ended", "closed", "notLoaded", "restartRequired"] as const)(
  "Notes sheet opens saved %s content without editor or removal controls",
  async (status) => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    const model = testModel({
      status: { type: status },
      humanNote: "human sheet sentinel",
      agentNote: "agent sheet sentinel",
      sessionUrls: [{ id: "u1", url: "https://notes.test/sheet", label: "sheet reference" }],
    });
    threadsStore.setState({ threads: new Map([[model.ref, model]]) });
    render(<NotesPanel sessionRef={model.ref} model={model} />);

    await user.click(screen.getByRole("button", { name: "Notes" }));

    expect(screen.getByRole("dialog", { name: "Session notes" })).toBeTruthy();
    expect(screen.getByTestId("shared-notes-human").textContent).toBe("human sheet sentinel");
    expect(screen.getByTestId("shared-notes-agent").textContent).toBe("agent sheet sentinel");
    expect(screen.getByRole("link", { name: "sheet reference" }).getAttribute("href")).toBe("https://notes.test/sheet");
    expect(screen.queryByRole("textbox", { name: "Human note" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Remove sheet reference" })).toBeNull();
    expect(fake.calls.filter((call) => call.method === "notes/human/set" || call.method === "urls/remove")).toEqual([]);
  },
);

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

test("notes panel is read-only when restartRequired: values shown, no editor, no remove buttons", () => {
  openPanel(
    testModel({
      status: { type: "restartRequired" },
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

test("web links show the destination URL beside an agent-controlled label", () => {
  openPanel(
    testModel({
      sessionUrls: [{ id: "u1", url: "https://evil.test/phish", label: "Trusted docs" }],
    }),
  );
  const row = screen.getByTestId("shared-notes-url-u1");
  // The label links onward, but the destination URL stays visible beside it:
  // a trusted-looking label must never display alone over a phishing URL.
  expect(row.querySelector("a")?.getAttribute("href")).toBe("https://evil.test/phish");
  expect(row.textContent).toMatch(/Trusted docs/);
  expect(row.textContent).toMatch(/https:\/\/evil\.test\/phish/);
});

test("labelless web links render the URL once, not twice", () => {
  openPanel(
    testModel({
      sessionUrls: [{ id: "u2", url: "https://x.test/plain" }],
    }),
  );
  const row = screen.getByTestId("shared-notes-url-u2");
  // No label: the anchor carries the URL and no trailing URL span duplicates
  // it (the row's Remove button is separate affordance, not URL text).
  expect(row.querySelector("a")?.getAttribute("href")).toBe("https://x.test/plain");
  expect(row.querySelector("a")?.textContent).toBe("https://x.test/plain");
  expect(row.textContent).toBe("https://x.test/plain Remove");
});

test("file links keep visible text and offer open-beside in scope", () => {
  const model = testModel({
    cwd: "/home/proj",
    sessionUrls: [{ id: "u3", url: "file:///home/proj/docs/a.md", label: "spec" }],
  });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  openPanel(model);
  const row = screen.getByTestId("shared-notes-url-u3");
  // Visible text stays (label + URL), and the in-scope path offers the
  // document/open-beside affordance instead of dead text.
  expect(row.textContent).toMatch(/spec/);
  expect(row.textContent).toMatch(/file:\/\/\/home\/proj\/docs\/a\.md/);
  expect(screen.getByRole("button", { name: "Open beside: docs/a.md" })).toBeTruthy();
});

test("file links out of scope keep text but offer no affordance", () => {
  const model = testModel({
    cwd: "/home/proj",
    sessionUrls: [{ id: "u4", url: "file:///etc/passwd" }],
  });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  openPanel(model);
  const row = screen.getByTestId("shared-notes-url-u4");
  expect(row.textContent).toMatch(/file:\/\/\/etc\/passwd/);
  expect(screen.queryByRole("button", { name: /Open beside/ })).toBeNull();
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
  const { user, fake } = clockClient();
  let called: unknown;
  fake.on("notes/human/set", (params) => {
    called = params;
    return noteResponse(params, "saved note");
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  void threadsStore.getState().ensureThread(model.ref);
  openPanel(model);
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "saved note");
  await user.tab();
  await advance(10_000);

  await waitFor(() => expect(called).toMatchObject({ ref: model.ref, note: "saved note" }));
  expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe("saved note");
  expect(await screen.findByTestId("shared-notes-saved")).toBeTruthy();
});

test("blurring with an unchanged draft saves nothing", async () => {
  const { user, fake } = clockClient();
  let calls = 0;
  fake.on("notes/human/set", (params) => {
    calls += 1;
    return noteResponse(params, "old note");
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  void threadsStore.getState().ensureThread(model.ref);
  openPanel(model);
  await user.click(editor());
  await user.tab();
  await advance(10_000);

  await waitFor(() => expect(screen.queryByTestId("shared-notes-saving")).toBeNull());
  expect(calls).toBe(0);
});

test("a push while the editor is focused never clobbers typing", async () => {
  const user = userEvent.setup();
  connectFakeClient();
  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  void threadsStore.getState().ensureThread(model.ref);
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
  void threadsStore.getState().ensureThread(model.ref);
  const { rerender } = render(<NotesPanelBody sessionRef={model.ref} model={model} />);
  expect(editor().value).toBe("old note");

  rerender(<NotesPanelBody sessionRef={model.ref} model={{ ...model, humanNote: "pushed note" }} />);
  expect(editor().value).toBe("pushed note");
});

test("remove dispatches urls/remove", async () => {
  const { user, fake } = clockClient();
  let called: unknown;
  fake.on("urls/remove", (params) => {
    called = params;
    return {};
  });

  const model = testModel({ sessionUrls: [{ id: "u1", url: "https://x.test/y", label: "x" }] });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  void threadsStore.getState().ensureThread(model.ref);
  openPanel(model);
  await user.click(screen.getByTestId("shared-notes-url-remove-u1"));

  await waitFor(() => expect(called).toMatchObject({ ref: model.ref, id: "u1" }));
});

test("a second Remove click while the first is in flight is ignored and stays silent", async () => {
  const { user, fake } = clockClient();
  let calls = 0;
  let release!: () => void;
  const firstInFlight = new Promise<void>((resolve) => {
    release = resolve;
  });
  fake.on("urls/remove", (params) => {
    calls += 1;
    // The reported defect: the first request succeeds and the second reports the
    // entry as already gone, which must not reach the user as an error toast.
    if (calls === 1) return firstInFlight.then(() => ({}));
    throw new WireError("link not found", -32004, { id: (params as { id: string }).id });
  });

  const model = testModel({ sessionUrls: [{ id: "u1", url: "https://x.test/y", label: "x" }] });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  void threadsStore.getState().ensureThread(model.ref);
  render(
    <>
      <NotesPanelBody sessionRef={model.ref} model={model} />
      <Toast />
    </>,
  );
  const button = screen.getByTestId("shared-notes-url-remove-u1");
  void user.click(button);
  await waitFor(() => expect(calls).toBe(1));
  // The double-click's second click lands while the first request is pending.
  await user.click(button);
  expect(calls).toBe(1);

  release();
  await act(async () => {
    await firstInFlight;
  });
  expect(calls).toBe(1);
  expect(screen.queryByText(/Couldn't remove link/i)).toBeNull();
});

// --- save coalescing -------------------------------------------------------------

test("reverting to the stored text during an in-flight save still persists the revert", async () => {
  const { user, fake } = clockClient();
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
      return gate.then(() => noteResponse(params));
    }
    return noteResponse(params);
  });

  const model = testModel({ humanNote: "stored A" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  void threadsStore.getState().ensureThread(model.ref);
  openPanel(model);
  // A save of B is in flight...
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "draft B");
  await user.tab();
  await advance(10_000);
  await waitFor(() => expect(seen).toHaveLength(1));
  // ...and the user reverts to the currently-stored A before B lands. The
  // revert equals the store mid-flight, but dropping it would leave B
  // persisted instead: the queue must hold it and the drain must persist it.
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "stored A");
  await user.tab();
  release();
  await waitFor(() => expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe("draft B"));
  expect(editor().value).toBe("stored A");
  expect(screen.queryByTestId("shared-notes-saved")).toBeNull();
  await advance(9_999);
  expect(seen).toHaveLength(1);
  await advance(1);
  await waitFor(() => expect(seen).toHaveLength(2));
  expect(seen[1]).toMatchObject({ ref: model.ref, note: "stored A" });
  expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe("stored A");
});

test("older B success followed by C rejection keeps C visible and recoverable", async () => {
  const { user, fake } = clockClient();
  const model = testModel({ humanNote: "A" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  await threadsStore.getState().ensureThread(model.ref);
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  fake.on("notes/human/set", (params) => {
    if (params.note === "B") return gate.then(() => noteResponse(params));
    throw new WireError("C refusal sentinel", -32013, {
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
    });
  });
  render(<LivePanel sessionRef={model.ref} />);
  await user.clear(editor());
  await user.type(editor(), "B");
  await user.tab();
  await advance(10_000);
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "notes/human/set")).toHaveLength(1));
  await user.clear(editor());
  await user.type(editor(), "C");
  await user.tab();
  release();
  await waitFor(() => expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe("B"));
  expect(editor().value).toBe("C");
  await advance(10_000);
  await screen.findByTestId("shared-notes-error");
  expect(editor().value).toBe("C");
  expect(screen.queryByTestId("shared-notes-saved")).toBeNull();
});

test("server whitespace and Unicode are retained and an unchanged blur does not resubmit", async () => {
  const { user, fake } = clockClient();
  const raw = "  line one\n\tline two\u00a0e\u0301🙂  ";
  fake.on("notes/human/set", (params) => noteResponse(params, raw));
  const model = testModel();
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  void threadsStore.getState().ensureThread(model.ref);
  openPanel(model);
  await user.type(editor(), "request sentinel");
  await user.tab();
  await advance(10_000);
  await screen.findByTestId("shared-notes-saved");
  expect(editor().value).toBe(raw);
  expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe(raw);
  await user.click(editor());
  await user.tab();
  await advance(10_000);
  expect(fake.calls.filter((call) => call.method === "notes/human/set")).toHaveLength(1);
});

test("a second blur while a save is in flight replays the latest draft instead of dropping it", async () => {
  const { user, fake } = clockClient();
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
      return gate.then(() => noteResponse(params));
    }
    return noteResponse(params);
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  void threadsStore.getState().ensureThread(model.ref);
  openPanel(model);
  // First blur starts the gated save...
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "first draft");
  await user.tab();
  await advance(10_000);
  await waitFor(() => expect(seen).toHaveLength(1));
  // ...while it is in flight, a second edit + blur parks (not drops) the
  // newer draft; releasing the gate lets the loop replay it.
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "second draft");
  await user.tab();
  await advance(10_000);
  release();
  await waitFor(() => expect(seen).toHaveLength(2));
  expect(seen[1]).toMatchObject({ ref: model.ref, note: "second draft" });
  expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe("second draft");
  await waitFor(async () => expect((await readMutationPersistence(model.ref)).recovery).toEqual([]));
});

test("a B-save parking behind an in-flight A-save persists instead of overwriting A", async () => {
  const { user, fake } = clockClient();
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
      return gate.then(() => noteResponse(params));
    }
    return noteResponse(params);
  });

  const modelA = testModel({ ref: "local:aaaa", threadId: "aaaa", humanNote: "note A" });
  const modelB = testModel({ ref: "local:bbbb", threadId: "bbbb", humanNote: "note B" });
  threadsStore.setState({ threads: new Map([[modelA.ref, modelA]]) });
  void threadsStore.getState().ensureThread(modelA.ref);
  void threadsStore.getState().ensureThread(modelA.ref);
  const { rerender } = render(<NotesPanelBody sessionRef={modelA.ref} model={modelA} />);
  // A's blur starts the gated save...
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "draft A2");
  await user.tab();
  await advance(10_000);
  await waitFor(() => expect(seen).toHaveLength(1));
  // ...then the panel switches to B. Its independent session draft and
  // deadline must not overwrite A's submitted note.
  threadsStore.setState({
    threads: new Map([
      [modelA.ref, modelA],
      [modelB.ref, modelB],
    ]),
  });
  void threadsStore.getState().ensureThread(modelB.ref);
  rerender(<NotesPanelBody sessionRef={modelB.ref} model={modelB} />);
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "draft B2");
  await user.tab();
  await advance(10_000);
  release();
  await waitFor(() => expect(seen).toHaveLength(2));
  expect(seen[0]).toMatchObject({ ref: modelA.ref, note: "draft A2" });
  expect(seen[1]).toMatchObject({ ref: modelB.ref, note: "draft B2" });
  expect(threadsStore.getState().threads.get(modelA.ref)?.humanNote).toBe("draft A2");
  expect(threadsStore.getState().threads.get(modelB.ref)?.humanNote).toBe("draft B2");
});

test("a failed save retries on the next blur with the latest draft", async () => {
  const { user, fake } = clockClient();
  const seen: unknown[] = [];
  let calls = 0;
  fake.on("notes/human/set", (params) => {
    calls += 1;
    seen.push(params);
    if (calls === 1)
      throw new WireError("first save boom", -32013, {
        clientMutationId: params.clientMutationId,
        mutationOutcome: "notAccepted",
      });
    return noteResponse(params);
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  void threadsStore.getState().ensureThread(model.ref);
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
  await advance(10_000);
  await screen.findAllByText(/first save boom/i);
  // A newer draft typed after the failure retries on the next explicit save
  // (the failure itself never requeues into the same drain): the next blur
  // lands the newest text.
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "second draft");
  await user.tab();
  await advance(10_000);
  await waitFor(() => expect(seen).toHaveLength(2));
  expect(seen[1]).toMatchObject({ ref: model.ref, note: "second draft" });
  expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe("second draft");
  await waitFor(async () => expect((await readMutationPersistence(model.ref)).recovery).toEqual([]));
});

test("a persistently failing save does not hot-loop the same drain", async () => {
  const { user, fake } = clockClient();
  let calls = 0;
  fake.on("notes/human/set", (params) => {
    calls += 1;
    throw new WireError("always boom", -32013, {
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
    });
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  void threadsStore.getState().ensureThread(model.ref);
  render(
    <>
      <NotesPanelBody sessionRef={model.ref} model={model} />
      <Toast />
    </>,
  );
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "doomed draft");
  await user.tab();
  await advance(10_000);
  await screen.findAllByText(/always boom/i);
  // The failed drain settles after exactly one attempt: no requeue means no
  // request/toast/saving storm, and the loop is free for the next explicit
  // save. The draft stays put for the user to retry.
  await waitFor(() => expect(screen.queryByTestId("shared-notes-saving")).toBeNull());
  expect(calls).toBe(1);
  expect(editor().value).toBe("doomed draft");
});

// --- failure keeps the draft ---------------------------------------------------

test("a B-save queued behind a failing A-save still persists and reports", async () => {
  const { user, fake } = clockClient();
  const seen: unknown[] = [];
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    if ((params as { ref: string }).ref === "local:aaaa")
      throw new WireError("A save boom", -32013, {
        clientMutationId: params.clientMutationId,
        mutationOutcome: "notAccepted",
      });
    return noteResponse(params);
  });

  const modelA = testModel({ ref: "local:aaaa", threadId: "aaaa", humanNote: "note A" });
  const modelB = testModel({ ref: "local:bbbb", threadId: "bbbb", humanNote: "note B" });
  threadsStore.setState({
    threads: new Map([
      [modelA.ref, modelA],
      [modelB.ref, modelB],
    ]),
  });
  void threadsStore.getState().ensureThread(modelA.ref);
  void threadsStore.getState().ensureThread(modelB.ref);
  const { rerender } = render(
    <>
      <NotesPanelBody sessionRef={modelA.ref} model={modelA} />
      <Toast />
    </>,
  );
  // A's blur starts the loop and fails (exactly once — failures never
  // requeue into the same drain)...
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "draft A2");
  await user.tab();
  await advance(10_000);
  await screen.findAllByText(/A save boom/i);
  // ...then the panel switches to B, whose blur starts a fresh loop. B
  // persists and reports Saved; A's draft stays in A's textarea for an
  // explicit retry — nothing stranded silent.
  rerender(
    <>
      <NotesPanelBody sessionRef={modelB.ref} model={modelB} />
      <Toast />
    </>,
  );
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "draft B2");
  await user.tab();
  await advance(10_000);
  await screen.findByTestId("shared-notes-saved");
  // A attempted once (no hot-loop retry); B attempted once and landed.
  expect(seen.map((p) => (p as { ref: string }).ref)).toEqual(["local:aaaa", "local:bbbb"]);
  expect(threadsStore.getState().threads.get(modelB.ref)?.humanNote).toBe("draft B2");
});

test("a failure superseded by a newer save in the same drain never retries stale text", async () => {
  const { user, fake } = clockClient();
  const seen: unknown[] = [];
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let calls = 0;
  fake.on("notes/human/set", (params) => {
    calls += 1;
    seen.push(params);
    // The first (stale) attempt gates; everything after succeeds. If the
    // stale failure were requeued unconditionally, the retry would restore
    // "stale draft" over the newer stored text.
    if (calls === 1)
      return gate.then(() =>
        Promise.reject(
          new WireError("stale save boom", -32013, {
            clientMutationId: params.clientMutationId,
            mutationOutcome: "notAccepted",
          }),
        ),
      );
    return noteResponse(params);
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  void threadsStore.getState().ensureThread(model.ref);
  openPanel(model);
  // First blur starts the gated save of the stale draft...
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "stale draft");
  await user.tab();
  await advance(10_000);
  await waitFor(() => expect(seen).toHaveLength(1));
  // ...while it is in flight, a newer draft parks behind it; releasing the
  // gate fails the stale attempt, and the loop must drain the newer draft
  // instead of stopping at the failure.
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "newer draft");
  await user.tab();
  await advance(10_000);
  release();
  await waitFor(() => expect(seen).toHaveLength(2));
  expect(seen[1]).toMatchObject({ ref: model.ref, note: "newer draft" });
  expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe("newer draft");
  // The stale failure must not resurrect: no third attempt restores it.
  await waitFor(() => expect(screen.queryByTestId("shared-notes-saving")).toBeNull());
  expect(seen).toHaveLength(2);
  expect(threadsStore.getState().threads.get(model.ref)?.humanNote).toBe("newer draft");
});

test("an earlier success still reports Saved when a sibling fails later in the drain", async () => {
  const { user, fake } = clockClient();
  const seen: unknown[] = [];
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let first = true;
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    // Gate A's save while B has its own deadline and request. A succeeds
    // while B fails; A's Saved must survive B's failure.
    if (first && (params as { ref: string }).ref === "local:aaaa") {
      first = false;
      return gate.then(() => noteResponse(params));
    }
    if ((params as { ref: string }).ref === "local:bbbb")
      throw new WireError("B save boom", -32013, {
        clientMutationId: params.clientMutationId,
        mutationOutcome: "notAccepted",
      });
    return noteResponse(params);
  });

  const modelA = testModel({ ref: "local:aaaa", threadId: "aaaa", humanNote: "note A" });
  const modelB = testModel({ ref: "local:bbbb", threadId: "bbbb", humanNote: "note B" });
  threadsStore.setState({
    threads: new Map([
      [modelA.ref, modelA],
      [modelB.ref, modelB],
    ]),
  });
  void threadsStore.getState().ensureThread(modelA.ref);
  const { rerender } = render(<NotesPanelBody sessionRef={modelA.ref} model={modelA} />);
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "draft A2");
  await user.tab();
  await advance(10_000);
  await waitFor(() => expect(seen).toHaveLength(1));
  void threadsStore.getState().ensureThread(modelB.ref);
  rerender(<NotesPanelBody sessionRef={modelB.ref} model={modelB} />);
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "draft B2");
  await user.tab();
  await advance(10_000);
  release();
  // B's failure surfaces while the panel shows B, and A still landed...
  await waitFor(() => expect(seen).toHaveLength(2));
  await screen.findAllByText(/B save boom/i);
  expect(threadsStore.getState().threads.get(modelA.ref)?.humanNote).toBe("draft A2");
  // ...then switching back to A paints Saved: the outcome recorded for A is
  // success, and B's failure belongs to B's session, not A's status line.
  // B's failed shared draft remains available for a later retry.
  rerender(<NotesPanelBody sessionRef={modelA.ref} model={{ ...modelA, humanNote: "draft A2" }} />);
  await user.click(editor());
  await user.tab();
  await advance(10_000);
  await screen.findByTestId("shared-notes-saved");
});

test("a failed blur-save surfaces an error and keeps the draft", async () => {
  const { user, fake } = clockClient();
  fake.on("notes/human/set", (params) => {
    throw new WireError("save note boom", -32013, {
      clientMutationId: params.clientMutationId,
      mutationOutcome: "notAccepted",
    });
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  void threadsStore.getState().ensureThread(model.ref);
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
  await advance(10_000);

  await screen.findAllByText(/save note boom/i);
  expect(editor().value).toBe("draft note");
  expect(screen.getByTestId("shared-notes-error")).toBeTruthy();
});

// --- closing is not an actual blur ---------------------------------------------

test("switching sessions without blur retains the outgoing draft without saving", async () => {
  const { user, fake } = clockClient();
  const seen: unknown[] = [];
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    return noteResponse(params);
  });

  const modelA = testModel({ ref: "local:aaaa", threadId: "aaaa", humanNote: "note A" });
  // Session B's stored note deliberately EQUALS A's dirty draft: the
  // pre-fix cleanup compared the old draft against the new stored note and
  // skipped the save on the coincidence.
  const modelB = testModel({ ref: "local:bbbb", threadId: "bbbb", humanNote: "dirty A draft" });
  threadsStore.setState({ threads: new Map([[modelA.ref, modelA]]) });
  void threadsStore.getState().ensureThread(modelA.ref);
  void threadsStore.getState().ensureThread(modelA.ref);
  const { rerender } = render(<NotesPanelBody sessionRef={modelA.ref} model={modelA} />);
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "dirty A draft");
  // A prop-driven session swap does not dispatch a blur or invent a save.
  threadsStore.setState({ threads: new Map([[modelB.ref, modelB]]) });
  void threadsStore.getState().ensureThread(modelB.ref);
  rerender(<NotesPanelBody sessionRef={modelB.ref} model={modelB} />);

  await advance(10_000);
  expect(seen).toHaveLength(0);
  rerender(<NotesPanelBody sessionRef={modelA.ref} model={modelA} />);
  expect(editor().value).toBe("dirty A draft");
});

test("a live-to-ended transition without blur does not invent a save", async () => {
  const { user, fake } = clockClient();
  const seen: unknown[] = [];
  fake.on("notes/human/set", (params) => {
    seen.push(params);
    return noteResponse(params);
  });

  const model = testModel({ humanNote: "old note" });
  threadsStore.setState({ threads: new Map([[model.ref, model]]) });
  void threadsStore.getState().ensureThread(model.ref);
  const { rerender } = render(<NotesPanelBody sessionRef={model.ref} model={model} />);
  await user.click(editor());
  await user.clear(editor());
  await user.type(editor(), "final words");
  // The session ends with the textarea focused: the editor unmounts for the
  // read-only view without blur firing, so no save is scheduled.
  rerender(<NotesPanelBody sessionRef={model.ref} model={{ ...model, status: { type: "ended" } }} />);

  await advance(10_000);
  expect(seen).toHaveLength(0);
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
