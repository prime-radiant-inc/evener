import { act, cleanup, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { resetHumanNoteDrafts, useHumanNoteDraft } from "../../../stores/humanNoteDrafts";
import { threadsStore } from "../../../stores/threads";
import { topNotesStore } from "../../../stores/topNotes";
import { TopNotesPanel } from "./TopNotesPanel";

const FULL_CAPABILITIES = {
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

function makeModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
  return {
    ref: "local:sess_test",
    threadId: "sess_test",
    name: "Test Session",
    status: { type: "idle" },
    modelProvider: "anthropic",
    model: "claude-3-7-sonnet",
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
    cwd: "/repo",
    jobsTreeRevision,
    ...rest,
  };
}

beforeEach(() => {
  topNotesStore.getState().resetForTests();
  // Draft records persist across renders by design (unsaved edits survive a
  // collapse); each test starts with none so a leftover draft from a
  // previous test cannot answer this file's summary assertions.
  resetHumanNoteDrafts();
});

afterEach(() => {
  cleanup();
  // Typing leaves a dirty draft holding a 10s save timer; clear it so no
  // stale save can fire after its test ends.
  resetHumanNoteDrafts();
});

test("returns null if canReadSharedNotes is false", () => {
  const model = makeModel({
    capabilities: { ...makeModel().capabilities, sharedNotes: false },
  });
  const { result } = renderHook(() => useHumanNoteDraft(model.ref));
  const { container } = render(<TopNotesPanel sessionRef={model.ref} model={model} />);
  expect(container.firstChild).toBeNull();
  // Nothing visible mounts, so nothing may feed the drafts store either: a
  // session without the notes capability must not grow it.
  expect(result.current).toBeUndefined();
});

test("collapsed default shows first priority: human note with person icon", () => {
  const model = makeModel({
    humanNote: "First line of human note\nSecond line",
    agentNote: "Agent note",
    sessionUrls: [{ id: "u1", url: "https://example.com", label: "Example" }],
  });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  expect(screen.getByTestId("top-notes-summary")).toBeTruthy();
  expect(screen.getByTestId("top-notes-icon-person")).toBeTruthy();
  expect(screen.queryByTestId("top-notes-icon-skill")).toBeNull();
  expect(screen.getByText(/First line of human note/)).toBeTruthy();
});

test("collapsed default shows second priority: agent note with skill icon when human note is empty", () => {
  const model = makeModel({
    humanNote: "",
    agentNote: "First line of agent note\nSecond line",
    sessionUrls: [{ id: "u1", url: "https://example.com", label: "Example" }],
  });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  expect(screen.getByTestId("top-notes-icon-skill")).toBeTruthy();
  expect(screen.queryByTestId("top-notes-icon-person")).toBeNull();
  expect(screen.getByText(/First line of agent note/)).toBeTruthy();
});

test("collapsed default shows third priority: links with globe icon when notes are empty", () => {
  const model = makeModel({
    humanNote: "",
    agentNote: "",
    sessionUrls: [
      { id: "u1", url: "https://example.com", label: "Example" },
      { id: "u2", url: "https://foo.bar", label: "Foo" },
    ],
  });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  expect(screen.getByTestId("top-notes-icon-globe")).toBeTruthy();
  expect(screen.getByText(/2 links/)).toBeTruthy();
});

test("collapsed default shows empty placeholder when no notes or links exist", () => {
  const model = makeModel({
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
  });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  expect(screen.getByText("Add a note…")).toBeTruthy();
  expect(screen.queryByTestId("top-notes-icon-person")).toBeNull();
  expect(screen.queryByTestId("top-notes-icon-skill")).toBeNull();
  expect(screen.queryByTestId("top-notes-icon-globe")).toBeNull();
});

test("clicking the collapsed summary expands the panel, clicking header collapses it", async () => {
  const user = userEvent.setup();
  const model = makeModel({
    humanNote: "Human note content",
  });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  expect(screen.queryByTestId("top-notes-expanded-content")).toBeNull();

  // Click to expand
  await user.click(screen.getByTestId("top-notes-summary"));
  expect(screen.getByTestId("top-notes-expanded-content")).toBeTruthy();
  expect(topNotesStore.getState().isExpanded(model.ref)).toBe(true);

  // Click header to collapse
  await user.click(screen.getByTestId("top-notes-collapse-trigger"));
  expect(screen.queryByTestId("top-notes-expanded-content")).toBeNull();
  expect(topNotesStore.getState().isExpanded(model.ref)).toBe(false);
});

test("openAndFocus from topNotesStore expands the panel and focuses textarea", async () => {
  const model = makeModel({
    humanNote: "Initial draft",
  });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  expect(screen.queryByTestId("top-notes-expanded-content")).toBeNull();

  topNotesStore.getState().openAndFocus(model.ref);

  await waitFor(() => {
    expect(screen.getByTestId("top-notes-expanded-content")).toBeTruthy();
  });

  const textarea = screen.getByRole("textbox", { name: "Human note" });
  await waitFor(() => {
    expect(document.activeElement).toBe(textarea);
  });
});

test("expanded view displays human note editor, agent note, and links with remove functionality", async () => {
  const user = userEvent.setup();
  const removeURLSpy = vi.fn().mockResolvedValue(undefined);
  vi.spyOn(threadsStore.getState(), "removeURL").mockImplementation(removeURLSpy);

  const model = makeModel({
    humanNote: "My note",
    agentNote: "Agent analysis",
    sessionUrls: [
      { id: "u1", url: "https://example.com", label: "Example Docs" },
      { id: "u2", url: "file:///repo/file.txt", label: "Local file" },
    ],
  });

  topNotesStore.getState().setExpanded(model.ref, true);
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  expect(screen.getByTestId("top-notes-expanded-content")).toBeTruthy();
  const textarea = screen.getByRole("textbox", { name: "Human note" }) as HTMLTextAreaElement;
  expect(textarea.value).toBe("My note");
  expect(screen.getByText("Agent analysis")).toBeTruthy();
  expect(screen.getByText("Example Docs")).toBeTruthy();
  expect(screen.getByText("Local file")).toBeTruthy();

  // Test removing URL
  const removeBtn = screen.getByRole("button", { name: "Remove Example Docs" });
  await user.click(removeBtn);
  expect(removeURLSpy).toHaveBeenCalledWith(model.ref, "u1");
});

test("read-only sessions show a view-only empty state instead of a write invitation", async () => {
  const user = userEvent.setup();
  const model = makeModel({
    status: { type: "ended" },
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
  });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  const summary = screen.getByTestId("top-notes-summary");
  expect(screen.getByText("No notes yet")).toBeTruthy();
  expect(screen.queryByText("Add a note…")).toBeNull();
  // The visible preview IS the accessible name - a generic label would hide
  // the content from screen readers - and the hint rides along as a
  // description rather than hidden text.
  expect(summary.getAttribute("aria-label")).toBeNull();
  expect(summary.getAttribute("aria-describedby")).toBeTruthy();
  expect(summary.textContent).toContain("Click to view");

  // Reading still works: the body expands, but a read-only session mounts
  // no editor to type into.
  await user.click(summary);
  expect(screen.getByTestId("top-notes-expanded-content")).toBeTruthy();
  expect(screen.queryByRole("textbox", { name: "Human note" })).toBeNull();
});

test("collapsed summary shows unsaved draft edits immediately, in phrasing content", async () => {
  const user = userEvent.setup();
  const model = makeModel({ humanNote: "Saved note" });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  // Type without committing: model.humanNote stays "Saved note" until the
  // 10s blur-save lands, but the summary must mirror the editor's draft the
  // moment the panel collapses.
  await user.click(screen.getByTestId("top-notes-summary"));
  const textarea = screen.getByRole("textbox", { name: "Human note" }) as HTMLTextAreaElement;
  await user.type(textarea, " plus edits");

  await user.click(screen.getByTestId("top-notes-collapse-trigger"));
  const summaryText = screen.getByText("Saved note plus edits");
  // A button only admits phrasing content, so the clamped summary text is a
  // span, not a div.
  expect(summaryText.tagName).toBe("SPAN");
});

test("a focus request made before the panel mounts is served on mount", async () => {
  const model = makeModel({ humanNote: "Draft" });
  // /notes with only a session-details pane focused: the request lands
  // before the session pane (and this panel) mounts.
  topNotesStore.getState().openAndFocus(model.ref);
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  const textarea = screen.getByRole("textbox", { name: "Human note" });
  await waitFor(() => {
    expect(document.activeElement).toBe(textarea);
  });
});

test("focus requests stay scoped when a pane is reused for another session", async () => {
  const modelA = makeModel({ humanNote: "A" });
  const modelB = makeModel({ ref: "local:sess_b", humanNote: "B" });
  const view = render(<TopNotesPanel sessionRef={modelA.ref} model={modelA} />);

  // A's request is served while A is mounted.
  topNotesStore.getState().openAndFocus(modelA.ref);
  const editorA = await screen.findByRole("textbox", { name: "Human note" });
  await waitFor(() => {
    expect(document.activeElement).toBe(editorA);
  });

  // Pane reuse: the same component instance rerenders for session B
  // without unmounting. B starts collapsed.
  view.rerender(<TopNotesPanel sessionRef={modelB.ref} model={modelB} />);
  expect(screen.getByTestId("top-notes-summary")).toBeTruthy();

  // /notes on B must focus B's editor: the request belongs to B, not to a
  // numeric epoch compared against A's already-served baseline.
  topNotesStore.getState().openAndFocus(modelB.ref);
  const editorB = await screen.findByRole("textbox", { name: "Human note" });
  await waitFor(() => {
    expect(document.activeElement).toBe(editorB);
  });
});

test("collapsed summary follows model updates after the draft record goes clean", async () => {
  const user = userEvent.setup();
  const model = makeModel({ humanNote: "First note" });
  const view = render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  // Expand once (the body mounts and creates the draft record), then
  // collapse: the record stays behind with the text it had at collapse.
  await user.click(screen.getByTestId("top-notes-summary"));
  await user.click(screen.getByTestId("top-notes-collapse-trigger"));

  // Another client saves a new note while the panel is collapsed. The
  // summary must show it, not the stale clean record.
  view.rerender(<TopNotesPanel sessionRef={model.ref} model={{ ...model, humanNote: "Updated by another tab" }} />);
  expect(screen.getByText("Updated by another tab")).toBeTruthy();
});

test("a session that turns read-only shows the saved note, not the stranded draft", async () => {
  const user = userEvent.setup();
  const model = makeModel({ humanNote: "Saved note" });
  const view = render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  await user.click(screen.getByTestId("top-notes-summary"));
  await user.type(screen.getByRole("textbox", { name: "Human note" }), " never saved");
  await user.click(screen.getByTestId("top-notes-collapse-trigger"));

  // The session ends with the edit unsaved: the read-only body shows the
  // saved note, so the summary must agree with what expanding will show.
  view.rerender(<TopNotesPanel sessionRef={model.ref} model={{ ...model, status: { type: "ended" } }} />);
  expect(screen.queryByText("Saved note never saved")).toBeNull();
  expect(screen.getByText("Saved note")).toBeTruthy();
});

test("a focus request never lands on another session's editor after pane reuse", async () => {
  // Drive animation frames by hand so the take-to-frame window is
  // deterministic: the request is taken now, the frame fires later.
  const frames: FrameRequestCallback[] = [];
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    frames.push(cb);
    return frames.length;
  });
  try {
    const modelA = makeModel({ humanNote: "A" });
    const modelB = makeModel({ ref: "local:sess_b", humanNote: "B" });
    const view = render(<TopNotesPanel sessionRef={modelA.ref} model={modelA} />);

    // A's request is taken and its frame queued, unfired.
    topNotesStore.getState().openAndFocus(modelA.ref);
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Human note" })).toBeTruthy());
    expect(frames.length).toBe(1);

    // The pane is reused for session B, and B gets its own request, all
    // before A's frame fires.
    topNotesStore.getState().openAndFocus(modelB.ref);
    view.rerender(<TopNotesPanel sessionRef={modelB.ref} model={modelB} />);
    const editorB = await screen.findByRole("textbox", { name: "Human note" });
    expect(frames.length).toBeGreaterThanOrEqual(2);

    // A's stale frame must not focus B's editor.
    act(() => {
      frames.splice(0, 1)[0]?.(0);
    });
    expect(document.activeElement).not.toBe(editorB);

    // B's own frame focuses B's editor.
    act(() => {
      for (const frame of frames.splice(0)) frame(0);
    });
    expect(document.activeElement).toBe(editorB);
  } finally {
    vi.unstubAllGlobals();
  }
});

test("a focus request on a read-only session waits for the session to accept writes", async () => {
  const model = makeModel({ status: { type: "ended" }, humanNote: "Saved note" });
  const view = render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  // /notes on an ended session: the panel opens, but there is no editor to
  // focus, so the request must be held rather than consumed.
  topNotesStore.getState().openAndFocus(model.ref);
  await waitFor(() => expect(screen.getByTestId("top-notes-expanded-content")).toBeTruthy());
  expect(screen.queryByRole("textbox", { name: "Human note" })).toBeNull();
  expect(topNotesStore.getState().hasPendingFocus(model.ref)).toBe(true);

  // The session resumes without remounting: the held request focuses the
  // now-mounted editor.
  view.rerender(<TopNotesPanel sessionRef={model.ref} model={{ ...model, status: { type: "idle" } }} />);
  const textarea = screen.getByRole("textbox", { name: "Human note" });
  await waitFor(() => expect(document.activeElement).toBe(textarea));
});
