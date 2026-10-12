// Tests-first checkpoint for the multiline comment component (Task 5).
//
// `./DocumentCommentBox` is the production module this checkpoint precedes, so
// the static import below deliberately does NOT resolve yet: the file is a
// collection failure until the source grant lands, and `tsc` reports the
// missing module instead of this file hiding it behind an optional call or a
// stub. The props exercised are the approved ones: `work`, `onNote`, `onSend`,
// `onCancel`, `saving`, `status`.
//
// The capture is the fixture's real captured passage. Genuine keyboard
// insertion, focus and geometry on a physical device stay in their own later
// browser/phone gate; the value-behavior assertions here pair the event
// propagation check with the literal multiline value the textbox holds.
import {
  type AnnotationCommentWork,
  type CapturedPassage,
  editCommentText,
  selectCommentPassage,
} from "@evener/appwire-client";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { expect, test, vi } from "vitest";
import { DocumentCommentBox } from "./DocumentCommentBox";
import { cleanupAfterTest, fixture } from "./testing/annotationRepositoryFixtures";

// A controlled host: it feeds the component a `work` object and applies the
// real shared comment helpers on edit, exactly as the reader pane does. It is
// not a mock of the component; the component under test renders for real.
function CommentHost({
  capture,
  initial = "",
  saving = false,
  status = "unsaved",
  onSend,
  onCancel,
  workRef,
}: {
  capture?: CapturedPassage;
  initial?: string;
  saving?: boolean;
  status?: "unsaved" | "saved, pending draft";
  onSend: () => void;
  onCancel: () => void;
  workRef: { current: AnnotationCommentWork };
}) {
  const [work, setWork] = useState<AnnotationCommentWork>({ note: initial, capture, captureFrozen: false });
  workRef.current = work;
  return (
    <DocumentCommentBox
      work={work}
      onNote={(note: string) => setWork((current) => editCommentText(current, note))}
      onSend={onSend}
      onCancel={onCancel}
      saving={saving}
      status={status}
    />
  );
}

test("renders the exact multiline note as literal text with no injected markup", async () => {
  const f = await fixture("comment-note");
  const capture = f.operation.capture;
  const onNote = vi.fn();
  const onSend = vi.fn();
  const onCancel = vi.fn();
  const note = "Keep this\n<script>window.pwned=1</script> /same";

  render(
    <DocumentCommentBox
      work={{ note, capture, captureFrozen: true }}
      onNote={onNote}
      onSend={onSend}
      onCancel={onCancel}
      saving={false}
      status="unsaved"
    />,
  );

  // No jest-dom matchers in this tree (see testSetup.ts); read the value directly.
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe(note);
  expect(document.querySelector("script")).toBeNull();
});

test("a first edit freezes the attached capture through the shared comment helpers", async () => {
  const f = await fixture("comment-freeze");
  const capture = f.operation.capture;
  const onSend = vi.fn();
  const onCancel = vi.fn();
  const workRef: { current: AnnotationCommentWork } = { current: { note: "", captureFrozen: false } };

  render(<CommentHost capture={capture} workRef={workRef} onSend={onSend} onCancel={onCancel} />);
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Keep this scope." } });

  expect(workRef.current).toEqual({ note: "Keep this scope.", capture, captureFrozen: true });
  // A later selection keeps the already-frozen capture (same work object).
  expect(selectCommentPassage(workRef.current, { ...capture, mappingId: "map-B" })).toBe(workRef.current);
  expect(onSend).not.toHaveBeenCalled();
});

test("a whitespace-only first edit freezes the capture (trim is validation only)", async () => {
  const f = await fixture("comment-freeze-whitespace");
  const capture = f.operation.capture;
  const onSend = vi.fn();
  const onCancel = vi.fn();
  const workRef: { current: AnnotationCommentWork } = { current: { note: "", captureFrozen: false } };

  render(<CommentHost capture={capture} workRef={workRef} onSend={onSend} onCancel={onCancel} />);
  fireEvent.change(screen.getByRole("textbox"), { target: { value: " " } });

  expect(workRef.current).toEqual({ note: " ", capture, captureFrozen: true });
});

test("Return does not Send or reach a surrounding composer shortcut, and multiline input stays literal", async () => {
  const f = await fixture("comment-return");
  const capture = f.operation.capture;
  const onSend = vi.fn();
  const onCancel = vi.fn();
  const workRef: { current: AnnotationCommentWork } = { current: { note: "", captureFrozen: false } };

  // The surrounding shortcut is a REAL native keydown listener on an ancestor
  // OUTSIDE the React root. React delegates bubble events to its root, so an
  // earlier listener on that same root could not be blocked by the textarea's
  // stopPropagation; from a true ancestor, normal synthetic stopPropagation
  // keeps the event from reaching it.
  const outer = document.createElement("div");
  document.body.appendChild(outer);
  cleanupAfterTest(() => outer.remove());
  const composerKeyDown = vi.fn();
  outer.addEventListener("keydown", composerKeyDown);
  cleanupAfterTest(() => outer.removeEventListener("keydown", composerKeyDown));
  const root = document.createElement("div");
  outer.appendChild(root);

  render(<CommentHost capture={capture} workRef={workRef} onSend={onSend} onCancel={onCancel} />, { container: root });
  const textbox = screen.getByRole("textbox");

  // The propagation assertion: Return on the textbox must not Send and must not
  // bubble to a composer-style shortcut on a surrounding element.
  fireEvent.keyDown(textbox, { key: "Enter", code: "Enter" });
  expect(onSend).not.toHaveBeenCalled();
  expect(composerKeyDown).not.toHaveBeenCalled();

  // Paired exact value behavior: normal multiline input keeps a literal newline
  // and never invokes Send. Real OS-keyboard insertion is a later phone gate.
  const user = userEvent.setup();
  await user.type(textbox, "line one{Enter}line two");
  expect(workRef.current.note).toBe("line one\nline two");
  expect((textbox as HTMLTextAreaElement).value).toBe("line one\nline two");
  expect(onSend).not.toHaveBeenCalled();
});

test("a blank or whitespace-only note cannot Send", async () => {
  const f = await fixture("comment-blank");
  const capture = f.operation.capture;
  const onNote = vi.fn();
  const onSend = vi.fn();
  const onCancel = vi.fn();

  render(
    <DocumentCommentBox
      work={{ note: " \n\t ", capture, captureFrozen: true }}
      onNote={onNote}
      onSend={onSend}
      onCancel={onCancel}
      saving={false}
      status="unsaved"
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  expect(onSend).not.toHaveBeenCalled();
});

test("a valid Send fires the observer exactly once", async () => {
  const f = await fixture("comment-send");
  const capture = f.operation.capture;
  const onSend = vi.fn();
  const onCancel = vi.fn();
  const workRef: { current: AnnotationCommentWork } = { current: { note: "", captureFrozen: false } };

  render(
    <CommentHost capture={capture} initial="Keep this scope." workRef={workRef} onSend={onSend} onCancel={onCancel} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  expect(onSend).toHaveBeenCalledTimes(1);
});

test("explicit Cancel invokes only the cancel observer", async () => {
  const f = await fixture("comment-cancel");
  const capture = f.operation.capture;
  const onNote = vi.fn();
  const onSend = vi.fn();
  const onCancel = vi.fn();

  // LIMIT: this only proves the component invokes its Cancel observer and never
  // Sends. Real Cancel-clears-only-unsaved cleanup and durable-note preservation
  // belong to the later DocPane.test.tsx checkpoint.
  render(
    <DocumentCommentBox
      work={{ note: "unsent draft", capture, captureFrozen: true }}
      onNote={onNote}
      onSend={onSend}
      onCancel={onCancel}
      saving={false}
      status="unsaved"
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(onCancel).toHaveBeenCalledTimes(1);
  expect(onSend).not.toHaveBeenCalled();
  expect(onNote).not.toHaveBeenCalled();
});

test("a saving box preserves the note and suppresses a second Send, and status renders literally", async () => {
  const f = await fixture("comment-saving");
  const capture = f.operation.capture;
  const onNote = vi.fn();
  const onSend = vi.fn();
  const onCancel = vi.fn();

  const props = { work: { note: "Keep this scope.", capture, captureFrozen: true }, onNote, onSend, onCancel };
  const { rerender } = render(<DocumentCommentBox {...props} saving={true} status="unsaved" />);
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Keep this scope.");
  expect(screen.getByRole("status").textContent).toBe("unsaved");
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
  expect(onSend).not.toHaveBeenCalled();

  rerender(<DocumentCommentBox {...props} saving={false} status="saved, pending draft" />);
  expect(screen.getByRole("status").textContent).toBe("saved, pending draft");
  expect((screen.getByRole("textbox") as HTMLTextAreaElement).value).toBe("Keep this scope.");
});

test("opening the component does not steal focus or the surrounding selection", async () => {
  const onNote = vi.fn();
  const onSend = vi.fn();
  const onCancel = vi.fn();

  const outside = document.createElement("button");
  outside.textContent = "outside";
  document.body.appendChild(outside);
  cleanupAfterTest(() => outside.remove());
  const prose = document.createElement("p");
  prose.textContent = "selectable prose";
  document.body.appendChild(prose);
  cleanupAfterTest(() => prose.remove());

  outside.focus();
  const range = document.createRange();
  range.selectNodeContents(prose);
  const selection = document.getSelection();
  if (!selection) throw new Error("missing document selection");
  selection.removeAllRanges();
  selection.addRange(range);
  cleanupAfterTest(() => selection.removeAllRanges());

  render(
    <DocumentCommentBox
      work={{ note: "", captureFrozen: false }}
      onNote={onNote}
      onSend={onSend}
      onCancel={onCancel}
      saving={false}
      status="unsaved"
    />,
  );

  expect(document.activeElement).toBe(outside);
  expect(document.activeElement).not.toBe(screen.getByRole("textbox"));
  expect(selection.rangeCount).toBe(1);
  expect(selection.getRangeAt(0).toString()).toBe("selectable prose");
});

// ---------------------------------------------------------------------------
// Visible captured passage (whole-UI finding 4). The box opens "with the
// selected passage and Send", so the user keeps a visible confirmation of what
// the pending Send will annotate. The passage renders as inert literal text,
// never as parsed markup. The reader-level freeze through an edit and remount
// runs against the real reader in DocPane.test.tsx; here the component only has
// to render whatever capture its `work` carries.
// ---------------------------------------------------------------------------
function passageNode(): HTMLElement | null {
  return document.querySelector<HTMLElement>("[data-annotation-passage]");
}

test("the comment surface renders the captured selected passage as inert literal text", async () => {
  const f = await fixture("comment-passage");
  const onNote = vi.fn();
  const onSend = vi.fn();
  const onCancel = vi.fn();
  const selectedRenderedText = 'a <b>bold</b> <script>window.pwned=1</script> & "quoted"';
  const capture: CapturedPassage = { ...f.operation.capture, selectedRenderedText };

  const { rerender } = render(
    <DocumentCommentBox
      work={{ note: "", capture, captureFrozen: false }}
      onNote={onNote}
      onSend={onSend}
      onCancel={onCancel}
      saving={false}
      status="unsaved"
    />,
  );

  // The literal selected text is shown verbatim; no element from it survives.
  expect(passageNode()?.textContent).toBe(selectedRenderedText);
  expect(passageNode()?.querySelector("b, script, a")).toBeNull();
  expect(document.querySelector("script")).toBeNull();

  // Before the first edit the reader keeps its capture current, so a newly
  // completed selection updates the displayed passage.
  const updated: CapturedPassage = { ...capture, selectedRenderedText: "Other context" };
  rerender(
    <DocumentCommentBox
      work={{ note: "", capture: updated, captureFrozen: false }}
      onNote={onNote}
      onSend={onSend}
      onCancel={onCancel}
      saving={false}
      status="unsaved"
    />,
  );
  expect(passageNode()?.textContent).toBe("Other context");

  // After the first edit the reader hands the component the frozen capture, and
  // a remount renders that same captured passage unchanged.
  rerender(
    <DocumentCommentBox
      work={{ note: "Keep this scope.", capture: updated, captureFrozen: true }}
      onNote={onNote}
      onSend={onSend}
      onCancel={onCancel}
      saving={false}
      status="unsaved"
    />,
  );
  expect(passageNode()?.textContent).toBe("Other context");
});
