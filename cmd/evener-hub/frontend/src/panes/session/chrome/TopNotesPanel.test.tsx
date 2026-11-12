import type { ThreadModel } from "@evener/appwire-client";
import { act, cleanup, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetHumanNoteDrafts, useHumanNoteDraft } from "../../../stores/humanNoteDrafts";
import { putThreadModel, resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { topNotesStore } from "../../../stores/topNotes";
import { readModuleCss, topRuleBlock } from "../../../styles/cssBlock";
import { ALL_CAPABILITIES } from "../../../threadTestUtils";
import { TopNotesPanel } from "./TopNotesPanel";

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
    capabilities: ALL_CAPABILITIES,
    goal: null,
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    contextAssumed: false,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/repo",
    jobsTreeRevision,
    ...rest,
  };
}

// Why the collapsed summary shows a placeholder (null when it previews content).
function summaryPlaceholderReason(): string | null | undefined {
  return screen.getByTestId("top-notes-summary").querySelector("[data-reason]")?.getAttribute("data-reason");
}

beforeEach(() => {
  topNotesStore.getState().resetForTests();
  // openAndFocus judges a request's origin era from the threads store, so
  // tests hydrate real models and must start from none.
  resetThreadsStoreForTests();
  // Draft records persist across renders by design (unsaved edits survive a
  // collapse); each test starts with none so a leftover draft from a
  // previous test cannot answer this file's summary assertions.
  resetHumanNoteDrafts();
});

afterEach(() => {
  cleanup();
  // A dismissal test's outside target is a plain DOM node, not a React
  // container: testing-library's cleanup() leaves it behind, so a failed
  // assertion would otherwise leak an "elsewhere" button into the next test.
  for (const stray of document.querySelectorAll('[data-testid="notes-outside-target"]')) stray.remove();
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

  expect(summaryPlaceholderReason()).toBe("empty");
  expect(screen.queryByTestId("top-notes-icon-person")).toBeNull();
  expect(screen.queryByTestId("top-notes-icon-skill")).toBeNull();
  expect(screen.queryByTestId("top-notes-icon-globe")).toBeNull();
});

test("collapsed summary treats a whitespace-only note as empty", () => {
  const model = makeModel({
    humanNote: "   ",
    agentNote: " \n\t ",
    sessionUrls: [],
  });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  expect(summaryPlaceholderReason()).toBe("empty");
  expect(screen.queryByTestId("top-notes-icon-person")).toBeNull();
  expect(screen.queryByTestId("top-notes-icon-skill")).toBeNull();
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

  putThreadModel(model.ref, model);
  act(() => {
    topNotesStore.getState().openAndFocus(model.ref);
  });

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

test("the read-only placeholder does not reuse the writable session's invitation", () => {
  const empty = { humanNote: "", agentNote: "", sessionUrls: [] };
  const writable = makeModel(empty);
  render(<TopNotesPanel sessionRef={writable.ref} model={writable} />);
  const invitation = screen.getByTestId("top-notes-summary").textContent;
  cleanup();
  const readOnly = makeModel({ ...empty, status: { type: "ended" } });
  render(<TopNotesPanel sessionRef={readOnly.ref} model={readOnly} />);
  expect(screen.getByTestId("top-notes-summary").textContent).not.toBe(invitation);
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
  expect(summaryPlaceholderReason()).toBe("read-only");
  // The visible preview IS the accessible name - a generic label would hide
  // the content from screen readers. No hint text rides along in any state,
  // so there is no describedby either.
  expect(summary.getAttribute("aria-label")).toBeNull();
  expect(summary.getAttribute("aria-describedby")).toBeNull();
  expect(screen.queryByText("Click to view")).toBeNull();

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
  putThreadModel(model.ref, model);
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
  putThreadModel(modelA.ref, modelA);
  putThreadModel(modelB.ref, modelB);
  const view = render(<TopNotesPanel sessionRef={modelA.ref} model={modelA} />);

  // A's request is served while A is mounted.
  act(() => {
    topNotesStore.getState().openAndFocus(modelA.ref);
  });
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
  act(() => {
    topNotesStore.getState().openAndFocus(modelB.ref);
  });
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
    putThreadModel(modelA.ref, modelA);
    putThreadModel(modelB.ref, modelB);
    const view = render(<TopNotesPanel sessionRef={modelA.ref} model={modelA} />);

    // A's request is taken and its frame queued, unfired.
    act(() => {
      topNotesStore.getState().openAndFocus(modelA.ref);
    });
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Human note" })).toBeTruthy());
    expect(frames.length).toBe(1);

    // The pane is reused for session B, and B gets its own request, all
    // before A's frame fires.
    act(() => {
      topNotesStore.getState().openAndFocus(modelB.ref);
    });
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
  putThreadModel(model.ref, model);
  const view = render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  // /notes on an ended session: the panel opens, but there is no editor to
  // focus, so the request must be held rather than consumed.
  act(() => {
    topNotesStore.getState().openAndFocus(model.ref);
  });
  await waitFor(() => expect(screen.getByTestId("top-notes-expanded-content")).toBeTruthy());
  expect(screen.queryByRole("textbox", { name: "Human note" })).toBeNull();
  expect(topNotesStore.getState().hasPendingFocus(model.ref)).toBe(true);

  // The session resumes without remounting: the held request focuses the
  // now-mounted editor.
  view.rerender(<TopNotesPanel sessionRef={model.ref} model={{ ...model, status: { type: "idle" } }} />);
  const textarea = screen.getByRole("textbox", { name: "Human note" });
  await waitFor(() => expect(document.activeElement).toBe(textarea));
});

test("a held request never steals focus from an active control", async () => {
  const model = makeModel({ status: { type: "ended" }, humanNote: "Saved" });
  putThreadModel(model.ref, model);
  const view = render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  // /notes on an ended session: the request is held with no editor to serve.
  act(() => {
    topNotesStore.getState().openAndFocus(model.ref);
  });
  await waitFor(() => expect(screen.getByTestId("top-notes-expanded-content")).toBeTruthy());

  // The user moves on - focus lands in an unrelated control (the composer,
  // say) before the session ever hydrates.
  const elsewhere = document.createElement("input");
  document.body.appendChild(elsewhere);
  elsewhere.focus();

  view.rerender(<TopNotesPanel sessionRef={model.ref} model={{ ...model, status: { type: "idle" } }} />);

  // Writability arrives: the stale request is consumed SILENTLY. Yanking
  // focus here would route mid-typing keystrokes into a shared note.
  await waitFor(() => expect(screen.getByRole("textbox", { name: "Human note" })).toBeTruthy());
  expect(document.activeElement).toBe(elsewhere);
  expect(topNotesStore.getState().hasPendingFocus(model.ref)).toBe(false);
  elsewhere.remove();
});

test("a request issued for a writable session focuses through a pane reused from a read-only one", async () => {
  const modelA = makeModel({ status: { type: "ended" }, humanNote: "A" });
  const modelB = makeModel({ ref: "local:sess_b", humanNote: "B" });
  putThreadModel(modelA.ref, modelA);
  putThreadModel(modelB.ref, modelB);
  const view = render(<TopNotesPanel sessionRef={modelA.ref} model={modelA} />);

  // A control elsewhere holds focus (a menu opener that kept focus after its
  // menu closed, say) while the pane is reused for B and B's /notes request
  // lands in the same flow that re-pointed the pane.
  const elsewhere = document.createElement("input");
  document.body.appendChild(elsewhere);
  elsewhere.focus();
  act(() => {
    topNotesStore.getState().openAndFocus(modelB.ref);
  });
  view.rerender(<TopNotesPanel sessionRef={modelB.ref} model={modelB} />);

  // B accepted writes when the request was issued, so the request is fresh:
  // the previous session's read-only era may not swallow it.
  const editorB = await screen.findByRole("textbox", { name: "Human note" });
  await waitFor(() => expect(topNotesStore.getState().hasPendingFocus(modelB.ref)).toBe(false));
  await act(() => new Promise((resolve) => requestAnimationFrame(resolve)));
  expect(document.activeElement).toBe(editorB);
  elsewhere.remove();
});

test("a request held from a read-only era does not steal focus after a remount", async () => {
  const model = makeModel({ status: { type: "ended" }, humanNote: "Saved" });
  putThreadModel(model.ref, model);
  const view = render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  // /notes on the ended session: the request is held with no editor to serve.
  act(() => {
    topNotesStore.getState().openAndFocus(model.ref);
  });
  await waitFor(() => expect(screen.getByTestId("top-notes-expanded-content")).toBeTruthy());
  expect(topNotesStore.getState().hasPendingFocus(model.ref)).toBe(true);

  // The panel unmounts (the pane closes) with the request still held.
  view.unmount();

  // The session resumes writable while the panel is away, and a control the
  // user is in has focus by the time the panel remounts.
  const writable = { ...model, status: { type: "idle" } };
  putThreadModel(model.ref, writable);
  const elsewhere = document.createElement("input");
  document.body.appendChild(elsewhere);
  elsewhere.focus();
  render(<TopNotesPanel sessionRef={model.ref} model={writable} />);

  // The request was born in a read-only era: it is consumed SILENTLY so the
  // user's focus stays where it is - yanking it to the note editor would
  // route mid-typing keystrokes into a shared note.
  await waitFor(() => expect(screen.getByRole("textbox", { name: "Human note" })).toBeTruthy());
  await waitFor(() => expect(topNotesStore.getState().hasPendingFocus(model.ref)).toBe(false));
  // Drain the frame the focus delivery rides on, so a steal is observed
  // rather than outrun.
  await act(() => new Promise((resolve) => requestAnimationFrame(resolve)));
  expect(document.activeElement).toBe(elsewhere);
  elsewhere.remove();
});

test("no state carries an affordance hint", async () => {
  const user = userEvent.setup();
  const model = makeModel({ humanNote: "Saved note" });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);

  // Collapsed: the bar carries no "Click to expand" invite - the disclosure
  // affordance is the chevron and the row itself.
  expect(screen.queryByText("Click to expand")).toBeNull();
  expect(screen.getByTestId("top-notes-summary").getAttribute("aria-describedby")).toBeNull();

  await user.click(screen.getByTestId("top-notes-summary"));

  // Expanded: the header row carries no hint either.
  expect(screen.queryByText("Click to collapse")).toBeNull();
  expect(screen.getByTestId("top-notes-collapse-trigger").getAttribute("aria-describedby")).toBeNull();
});

test("the panel is an inset card with a boundary that cannot clip", () => {
  // In standalone session panes the card attaches to the header: it cancels
  // the body's top padding and reads as a continuation of the header's
  // hairline, so the top is square and the seam is a single line. Cascade
  // columns render the same panel inside a flush body (bodyNoScroll) with
  // no padding to cancel — there the margin stays 0 and is scoped to
  // session bodies only, while a separate geometry guard asserts the
  // trigger remains visible inside each column's scrollport.
  const css = readModuleCss(import.meta.url, "topnotespanel.module.css");
  const root = topRuleBlock(css, ".topNotesPanel");
  expect(root).toContain("border: 1px solid var(--edge)");
  expect(root).toContain("border-top: none");
  expect(root).toContain("border-radius: 0 0 var(--radius-panel)");
  expect(root).not.toContain("--shadow-overlay");
  expect(root).toContain("box-shadow:");
  expect(root).toContain("var(--shadow-color)");
  // The attach-to-header margin must not apply everywhere: only session
  // bodies have padding to cancel, so it is scoped to [data-pane-scaffold^="session:"]
  // and the cascade column's trigger geometry is asserted by the browser guard.
  expect(css).toContain(':global([data-pane-scaffold^="session:"]) .topNotesPanel');
  expect(css).toContain("margin-top: calc(-1 * var(--space-5))");

  // The collapsed trigger must not add a bottom edge of its own: the card
  // already draws that edge, and a second one doubles it.
  expect(topRuleBlock(css, ".summary")).not.toMatch(/border-bottom/);

  // Keyboard focus must survive the clipping containers: the card's
  // overflow:hidden and the body's scrollport cut an outside outline, so
  // both trigger states carry the sanctioned inset ring (the same recipe
  // sessionchrome.module.css documents for controls flush inside a clip
  // boundary).
  const focus = topRuleBlock(css, ".summary:focus-visible,\n.expandedHeader:focus-visible");
  expect(focus).toContain("outline: var(--focus-ring)");
  expect(focus).toContain("outline-offset: -2px");
});

// --- dismissal: a press outside the panel collapses it ---------------------
//
// The panel is a disclosure, not a modal: it floats over the conversation, so
// pressing anywhere else in the pane means the user has moved on. The press
// that decides is where the gesture STARTED, matching the overlay family's
// press-start rule (widgets/dialog/OverlayPanel.tsx): selecting note text and
// releasing the pointer outside the panel must not collapse it mid-drag.

/** A visible target outside the panel, standing in for the transcript. */
function outsideTarget(): HTMLElement {
  const target = document.createElement("button");
  target.type = "button";
  target.textContent = "elsewhere";
  target.setAttribute("data-testid", "notes-outside-target");
  document.body.append(target);
  return target;
}

test("pressing outside the expanded panel collapses it", async () => {
  const model = makeModel({ humanNote: "Saved note" });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);
  const user = userEvent.setup();
  await user.click(screen.getByTestId("top-notes-summary"));
  expect(screen.getByTestId("top-notes-expanded-content")).toBeTruthy();

  const outside = outsideTarget();
  await user.click(outside);

  expect(screen.queryByTestId("top-notes-expanded-content")).toBeNull();
  expect(topNotesStore.getState().isExpanded(model.ref)).toBe(false);
  outside.remove();
});

test("an outside press collapses only once its click completes", async () => {
  // The panel is IN FLOW. Collapsing on mousedown would reflow the pane before
  // the mouseup, so a control below it moves out from under the pointer and
  // loses its click. The dismissal therefore waits for the completed click.
  const model = makeModel({ humanNote: "Saved note" });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);
  const user = userEvent.setup();
  await user.click(screen.getByTestId("top-notes-summary"));

  const outside = outsideTarget();
  await user.pointer({ target: outside, keys: "[MouseLeft>]" });
  // Still expanded between press and release: the layout has not shifted yet,
  // so the control being clicked keeps its position and receives its click.
  expect(topNotesStore.getState().isExpanded(model.ref)).toBe(true);
  await user.pointer({ keys: "[/MouseLeft]" });
  expect(topNotesStore.getState().isExpanded(model.ref)).toBe(false);
  outside.remove();
});

test("pressing inside the expanded panel leaves it open", async () => {
  const model = makeModel({ humanNote: "Saved note" });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);
  const user = userEvent.setup();
  await user.click(screen.getByTestId("top-notes-summary"));

  await user.click(screen.getByRole("textbox", { name: "Human note" }));

  expect(screen.getByTestId("top-notes-expanded-content")).toBeTruthy();
  expect(topNotesStore.getState().isExpanded(model.ref)).toBe(true);
});

test("pressing inside a second panel for the same conversation leaves both open", async () => {
  // One conversation can host two panels at once (an ordinary session pane and
  // a cascade column), and they share one expanded flag per ref. A press inside
  // either one is inside the surface the user is working in, so neither may
  // collapse the shared state.
  const model = makeModel({ humanNote: "Saved note" });
  topNotesStore.getState().setExpanded(model.ref, true);
  render(
    <>
      <TopNotesPanel sessionRef={model.ref} model={model} />
      <TopNotesPanel sessionRef={model.ref} model={model} />
    </>,
  );
  const editors = screen.getAllByRole("textbox", { name: "Human note" });
  expect(editors).toHaveLength(2);
  const user = userEvent.setup();

  await user.click(editors[1]!);

  expect(topNotesStore.getState().isExpanded(model.ref)).toBe(true);
  expect(screen.getAllByTestId("top-notes-expanded-content")).toHaveLength(2);
});

test("a drag that starts inside the panel and releases outside does not collapse it", async () => {
  const model = makeModel({ humanNote: "Saved note" });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);
  const user = userEvent.setup();
  await user.click(screen.getByTestId("top-notes-summary"));

  const outside = outsideTarget();
  await user.pointer([
    { target: screen.getByRole("textbox", { name: "Human note" }), keys: "[MouseLeft>]" },
    { target: outside },
    { keys: "[/MouseLeft]" },
  ]);

  expect(topNotesStore.getState().isExpanded(model.ref)).toBe(true);
  outside.remove();
});

test("Escape inside the expanded panel collapses it and returns focus to the trigger", async () => {
  const model = makeModel({ humanNote: "Saved note" });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);
  const user = userEvent.setup();
  await user.click(screen.getByTestId("top-notes-summary"));
  await user.click(screen.getByRole("textbox", { name: "Human note" }));

  await user.keyboard("{Escape}");

  expect(topNotesStore.getState().isExpanded(model.ref)).toBe(false);
  // A keyboard dismissal must not drop focus onto the document body. The
  // trigger takes it on the frame after the body unmounts.
  await act(() => new Promise((resolve) => requestAnimationFrame(resolve)));
  expect(document.activeElement).toBe(screen.getByTestId("top-notes-summary"));
});

test("a collapsed panel leaves Escape for an enclosing overlay", async () => {
  const model = makeModel({ humanNote: "Saved note" });
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);
  const user = userEvent.setup();
  const summary = screen.getByTestId("top-notes-summary");
  summary.focus();
  const escaped = vi.fn();
  document.addEventListener("keydown", escaped);
  try {
    await user.keyboard("{Escape}");
  } finally {
    document.removeEventListener("keydown", escaped);
  }
  // Collapsed, the panel must not preventDefault a key it has no use for.
  expect(escaped).toHaveBeenCalledTimes(1);
  expect(escaped.mock.calls[0]?.[0].defaultPrevented).toBe(false);
});

test("Escape does not save a dirty draft it collapses", async () => {
  // Closing a panel without leaving the field does not save
  // (docs/web-ui/shared-notes.md). Escape must therefore not blur the editor on
  // the way out: a blur ARMS the 10s save, and that armed timer survives the
  // unmount and can wake the agent. The armed timer is the defect, so assert on
  // it directly rather than waiting it out.
  const user = userEvent.setup();
  const model = makeModel({ humanNote: "Saved note" });
  const { result } = renderHook(() => useHumanNoteDraft(model.ref));
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);
  await user.click(screen.getByTestId("top-notes-summary"));
  const textarea = screen.getByRole("textbox", { name: "Human note" });
  await user.type(textarea, " plus edits");
  expect(result.current?.dirty).toBe(true);

  await user.keyboard("{Escape}");

  expect(topNotesStore.getState().isExpanded(model.ref)).toBe(false);
  expect(result.current?.dirty).toBe(true);
  expect(result.current?.timer).toBeUndefined();
});

test("an outside press keeps the draft and schedules the save it implies", async () => {
  // Pressing outside the panel is LEAVING the field, and shared-notes.md's rule
  // is "If you already left the field, its scheduled save survives closing the
  // panel." So the draft stays, no write happens inside the debounce, and one
  // is armed for after it. (Escape is the other half of that rule: closing
  // WITHOUT leaving the field saves nothing, hence its own test above.)
  const model = makeModel({ humanNote: "Saved note" });
  const setNote = vi.spyOn(threadsStore.getState(), "setHumanNote");
  const { result } = renderHook(() => useHumanNoteDraft(model.ref));
  render(<TopNotesPanel sessionRef={model.ref} model={model} />);
  const user = userEvent.setup();
  await user.click(screen.getByTestId("top-notes-summary"));
  const textarea = screen.getByRole("textbox", { name: "Human note" });
  await user.type(textarea, " plus edits");

  const outside = outsideTarget();
  await user.click(outside);

  // Collapsing is not a save (docs/web-ui/shared-notes.md), and the edit is
  // still there to reopen.
  expect(topNotesStore.getState().isExpanded(model.ref)).toBe(false);
  expect(screen.getByText("Saved note plus edits")).toBeTruthy();
  // Nothing has been written yet; the debounce is still running.
  expect(setNote).not.toHaveBeenCalled();
  expect(result.current?.dirty).toBe(true);
  expect(result.current?.timer).toBeDefined();
  outside.remove();
});
