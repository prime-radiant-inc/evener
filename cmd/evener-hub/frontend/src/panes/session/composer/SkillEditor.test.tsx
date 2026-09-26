import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { createRef, useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import "../testing/editorGeometry";
import { SkillEditor, type SkillEditorHandle, type SkillEditorProps, type SkillEditorValue } from "./SkillEditor";

afterEach(cleanup);

function mount(initial: SkillEditorValue, extra: Partial<SkillEditorProps> = {}) {
  const ref = createRef<SkillEditorHandle>();
  const changed = vi.fn();
  let current = initial;
  function Controlled() {
    const [value, setValue] = useState(initial);
    return (
      <SkillEditor
        ref={ref}
        value={value}
        aria-label="Message"
        {...extra}
        onChange={(next, cursor) => {
          current = next;
          changed(next, cursor);
          setValue(next);
        }}
      />
    );
  }
  const result = render(<Controlled />);
  const editor = result.getByRole("textbox");
  return { ...result, editor, ref, changed, value: () => current };
}

function paste(editor: HTMLElement, text: string, html = "") {
  const clipboardData = {
    getData: (type: string) => (type === "text/plain" ? text : type === "text/html" ? html : ""),
    types: ["text/plain", "text/html"],
    files: [],
    items: [],
  };
  fireEvent.paste(editor, { clipboardData });
}

function key(editor: HTMLElement, key: string, extra: Record<string, unknown> = {}) {
  fireEvent.keyDown(editor, { key, ...extra });
}

describe("SkillEditor real ProseMirror view", () => {
  it("replaces a completion range with an inline atom and preserves controlled-echo cursor/focus", () => {
    const result = mount(
      { text: "Use /rev now", skillNames: [] },
      { skillDetails: () => "Review code; source: project" },
    );
    act(() => {
      result.ref.current?.focus();
      result.ref.current?.insertSkill(4, 8, "team:review");
    });
    expect(result.value()).toEqual({ text: "Use /team:review now", skillNames: ["team:review"] });
    expect(result.ref.current?.getCursor()).toBe("Use /team:review".length);
    expect(document.activeElement).toBe(result.editor);
    const atom = result.editor.querySelector('[data-skill-name="team:review"]');
    expect(atom?.textContent).toBe("/team:review");
    expect(atom?.getAttribute("contenteditable")).toBe("false");
    expect(atom?.getAttribute("title")).toBe("Review code; source: project");
    expect(atom?.getAttribute("aria-label")).toContain("Review code; source: project");
  });

  it.each(["Backspace", "Delete"])(
    "%s removes one adjacent atom in one keypress and undo/redo restores metadata",
    (direction) => {
      const result = mount({ text: "a /review b", skillNames: ["review"] });
      act(() => result.ref.current?.setSelection(direction === "Backspace" ? 9 : 2));
      key(result.editor, direction);
      expect(result.value()).toEqual({ text: "a  b", skillNames: [] });
      key(result.editor, "z", { ctrlKey: true });
      expect(result.value()).toEqual({ text: "a /review b", skillNames: ["review"] });
      key(result.editor, "z", { ctrlKey: true, shiftKey: true });
      expect(result.value()).toEqual({ text: "a  b", skillNames: [] });
    },
  );

  it.each(["", "suffix", " suffix", "\nsuffix"])(
    "inserts the completion separator in one undoable transaction before %j",
    (suffix) => {
      const result = mount({ text: `Run /rev${suffix}`, skillNames: [] });
      act(() => result.ref.current?.insertSkill(4, 8, "review"));
      const separator = /^\s/.test(suffix) ? "" : " ";
      expect(result.value()).toEqual({ text: `Run /review${separator}${suffix}`, skillNames: ["review"] });
      expect(result.ref.current?.getCursor()).toBe(`Run /review${separator}`.length);
      key(result.editor, "z", { ctrlKey: true });
      expect(result.value()).toEqual({ text: `Run /rev${suffix}`, skillNames: [] });
      key(result.editor, "z", { ctrlKey: true, shiftKey: true });
      expect(result.value()).toEqual({ text: `Run /review${separator}${suffix}`, skillNames: ["review"] });
    },
  );

  it("replaces a selection including skills with plain text, and undo restores all mentions", () => {
    const result = mount({ text: "/review and /review", skillNames: ["review"] });
    act(() => result.ref.current?.setSelection(0, 7));
    paste(result.editor, "first");
    expect(result.value()).toEqual({ text: "first and /review", skillNames: ["review"] });
    act(() => result.ref.current?.setSelection(0, result.value().text.length));
    paste(result.editor, "replacement");
    expect(result.value()).toEqual({ text: "replacement", skillNames: [] });
    key(result.editor, "z", { ctrlKey: true });
    expect(result.value()).toEqual({ text: "first and /review", skillNames: ["review"] });
  });

  it("pastes literal multiline text, rejects rich formatting, and never activates copied slash text", () => {
    const result = mount({ text: "", skillNames: [] });
    paste(
      result.editor,
      "one\ntwo\n/review",
      '<b>one</b><script>alert(1)</script><span data-skill-name="review">/review</span>',
    );
    expect(result.value()).toEqual({ text: "one\ntwo\n/review", skillNames: [] });
    expect(result.editor.querySelector("b,script,[data-skill-name]")).toBeNull();
    key(result.editor, "Enter", { shiftKey: true });
    expect(result.value().text).toBe("one\ntwo\n/review\n");
  });

  it("preserves newlines when the browser mutates a text node while typing", async () => {
    const result = mount({ text: "first\nsecond", skillNames: [] });
    await act(async () => {
      const text = result.editor.firstChild;
      if (!(text instanceof Text)) throw new Error("Expected a real editor text node");
      text.appendData("!");
    });
    await waitFor(() => expect(result.value()).toEqual({ text: "first\nsecond!", skillNames: [] }));
  });

  it("keeps pasted duplicate slash text plain through controlled echoes and deduplicates only actual atoms", () => {
    const result = mount({ text: "/review and ", skillNames: ["review"] });
    act(() => result.ref.current?.setSelection(result.value().text.length));
    paste(result.editor, "/review");
    expect(result.value()).toEqual({ text: "/review and /review", skillNames: ["review"] });
    expect(result.editor.querySelectorAll("[data-skill-name]")).toHaveLength(1);
    act(() => result.ref.current?.setSelection(0, 7));
    key(result.editor, "Delete");
    expect(result.value()).toEqual({ text: " and /review", skillNames: [] });
  });

  it("maps selection around emoji, newlines and indivisible atoms", () => {
    const result = mount({ text: "😀\n/review\nlast", skillNames: ["review"] });
    act(() => result.ref.current?.setSelection(10));
    expect(result.ref.current?.getCursor()).toBe(10);
    act(() => result.ref.current?.setSelection(5, 8));
    paste(result.editor, "replacement");
    expect(result.value()).toEqual({ text: "😀\nreplacement\nlast", skillNames: [] });
  });

  it("runs React keyboard/paste callbacks before editor defaults, including mixed image/text paste", () => {
    let allow = false;
    const onKeyDown = vi.fn((event) => {
      if (!allow) event.preventDefault();
    });
    const onPaste = vi.fn((event) => {
      if (!allow) event.preventDefault();
    });
    const result = mount({ text: "", skillNames: [] }, { onKeyDown, onPaste });
    key(result.editor, "Enter");
    paste(result.editor, "blocked");
    expect(result.value().text).toBe("");
    allow = true;
    key(result.editor, "Enter");
    fireEvent.paste(result.editor, {
      clipboardData: {
        getData: (type: string) => (type === "text/plain" ? "caption" : ""),
        types: ["text/plain", "Files"],
        files: [new File(["image"], "image.png", { type: "image/png" })],
        items: [],
      },
    });
    expect(onKeyDown).toHaveBeenCalledTimes(2);
    expect(onPaste).toHaveBeenCalledTimes(2);
    expect(onPaste.mock.calls[1]?.[0].clipboardData.files).toHaveLength(1);
    expect(result.value().text).toBe("\ncaption");
  });

  it("does not separate a completed skill from punctuation that already bounds it", () => {
    const result = mount({ text: "/rev, please", skillNames: [] });
    act(() => result.ref.current?.insertSkill(0, 4, "review"));
    // The comma already bounds the reference, so no separator is needed; adding
    // one would put whitespace in the request the user never typed.
    expect(result.value()).toEqual({ text: "/review, please", skillNames: ["review"] });
  });

  it("does not separate a completed skill from sentence punctuation", () => {
    const result = mount({ text: "/rev. more", skillNames: [] });
    act(() => result.ref.current?.insertSkill(0, 4, "review"));
    // A dot followed by a non-token character already bounds the reference, so
    // the parser reads `/review. more` as complete and no space is needed.
    expect(result.value()).toEqual({ text: "/review. more", skillNames: ["review"] });
  });

  it("separates a completed skill from text that would join its reference", () => {
    const result = mount({ text: "/revplease", skillNames: [] });
    act(() => result.ref.current?.insertSkill(0, 4, "review"));
    expect(result.value()).toEqual({ text: "/review please", skillNames: ["review"] });
  });

  it("reports a non-collapsed selection's lower offset, as the textarea's selectionStart did", () => {
    const result = mount({ text: "keep SELECTED tail", skillNames: [] });
    act(() => result.ref.current?.setSelection(5, 13));
    expect(result.ref.current?.getSelection()).toEqual({ start: 5, end: 13 });
    // `getCursor` is the anchor attachment marker stripping uses, and the field
    // it replaced reported the selection's start - not its focus end.
    expect(result.ref.current?.getCursor()).toBe(5);
  });

  it("does not submit Enter or insert a newline while composing", () => {
    const onKeyDown = vi.fn();
    const result = mount({ text: "", skillNames: [] }, { onKeyDown });
    fireEvent.compositionStart(result.editor);
    key(result.editor, "Enter", { isComposing: true, keyCode: 229 });
    expect(onKeyDown).not.toHaveBeenCalled();
    expect(result.value().text).toBe("");
    fireEvent.compositionEnd(result.editor);
  });

  it("reconciles external replacement and resets history so cleared submissions cannot reappear", () => {
    const ref = createRef<SkillEditorHandle>();
    const changed = vi.fn();
    const result = render(
      <SkillEditor ref={ref} value={{ text: "/rev", skillNames: [] }} onChange={changed} aria-label="Message" />,
    );
    const editor = result.getByRole("textbox");
    act(() => ref.current?.insertSkill(0, 4, "review"));
    const echo = { text: "/review ", skillNames: ["review"] };
    result.rerender(<SkillEditor ref={ref} value={echo} onChange={changed} aria-label="Message" />);
    expect(ref.current?.getCursor()).toBe(8);
    key(editor, "z", { ctrlKey: true });
    expect(changed.mock.lastCall?.[0]).toEqual({ text: "/rev", skillNames: [] });
    result.rerender(
      <SkillEditor ref={ref} value={{ text: "", skillNames: [] }} onChange={changed} aria-label="Message" />,
    );
    changed.mockClear();
    key(editor, "z", { ctrlKey: true });
    expect(editor.textContent).toBe("");
    expect(changed).not.toHaveBeenCalled();
    result.rerender(
      <SkillEditor
        ref={ref}
        value={{ text: "/reviewer /review", skillNames: ["review", "invisible"] }}
        onChange={changed}
        aria-label="Message"
      />,
    );
    expect(editor.querySelectorAll("[data-skill-name]")).toHaveLength(1);
  });

  it("serializes native clipboard selection with names and preserves metadata on undo after cut", () => {
    const result = mount({ text: "Use /review\nnow", skillNames: ["review"] });
    act(() => {
      result.ref.current?.focus();
      result.ref.current?.setSelection(0, result.value().text.length);
    });
    const setData = vi.fn();
    const clipboardData = { clearData: vi.fn(), setData };
    fireEvent.copy(result.editor, { clipboardData });
    expect(setData).toHaveBeenCalledWith("text/plain", "Use /review\nnow");
    fireEvent.cut(result.editor, { clipboardData });
    expect(result.value()).toEqual({ text: "", skillNames: [] });
    key(result.editor, "z", { ctrlKey: true });
    expect(result.value()).toEqual({ text: "Use /review\nnow", skillNames: ["review"] });
  });

  it("forwards accessible attributes, placeholder, focus/blur, and updates detail callbacks", () => {
    const ref = createRef<SkillEditorHandle>();
    const onFocus = vi.fn();
    const onBlur = vi.fn();
    const props = {
      value: { text: "/review", skillNames: ["review"] },
      onChange: vi.fn(),
      onFocus,
      onBlur,
      "aria-label": "Message",
      "aria-controls": "skills",
      "aria-activedescendant": "review-option",
      placeholder: "Write",
      minLines: 3,
    };
    const result = render(<SkillEditor ref={ref} {...props} skillDetails={() => "Old"} />);
    const editor = result.getByRole("textbox");
    expect(editor.getAttribute("aria-multiline")).toBe("true");
    expect(editor.getAttribute("aria-controls")).toBe("skills");
    expect(editor.getAttribute("aria-activedescendant")).toBe("review-option");
    expect(editor.getAttribute("data-placeholder")).toBe("Write");
    act(() => ref.current?.focus());
    fireEvent.blur(editor);
    expect(onFocus).toHaveBeenCalled();
    expect(onBlur).toHaveBeenCalled();
    result.rerender(<SkillEditor ref={ref} {...props} skillDetails={() => "New"} />);
    expect(editor.querySelector("[data-skill-name]")?.getAttribute("title")).toBe("New");
  });
});
