import { act, fireEvent } from "@testing-library/react";
import { flushSync } from "react-dom";
import "./editorGeometry";

/** Replace a composer's contents through its real selection and paste handlers. */
export function replaceEditorText(editor: HTMLElement, text: string): void {
  selectEditorText(editor, 0, editor.textContent?.length ?? 0);
  // The native editor transaction finishes after React's capture handler.
  // Commit its state before a following gesture, even inside an outer act().
  flushSync(() => {
    if (text === "") fireEvent.keyDown(editor, { key: "Backspace" });
    else {
      fireEvent.paste(editor, {
        clipboardData: {
          getData: (type: string) => (type === "text/plain" ? text : ""),
          types: ["text/plain"],
          files: [],
          items: [],
        },
      });
    }
  });
}

/** Read the caret using the browser selection, independently of the editor model. */
export function editorCursor(editor: HTMLElement): number {
  const selection = editor.ownerDocument.getSelection();
  if (!selection?.focusNode || !editor.contains(selection.focusNode)) return 0;
  const range = editor.ownerDocument.createRange();
  range.selectNodeContents(editor);
  range.setEnd(selection.focusNode, selection.focusOffset);
  return range.toString().length;
}

/** Place a caret in text using a real DOM range and deliver selectionchange. */
export function selectEditorText(editor: HTMLElement, start: number, end = start): void {
  const document = editor.ownerDocument;
  const locate = (offset: number): [Node, number] => {
    const walker = document.createTreeWalker(editor, NodeFilter.SHOW_TEXT);
    let remaining = offset;
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const length = node.textContent?.length ?? 0;
      if (remaining <= length) {
        const atom = node.parentElement?.closest('[contenteditable="false"]');
        if (atom?.parentNode) {
          const index = Array.from(atom.parentNode.childNodes).indexOf(atom);
          return [atom.parentNode, index + (remaining === 0 ? 0 : 1)];
        }
        return [node, remaining];
      }
      remaining -= length;
    }
    return [editor, editor.childNodes.length];
  };
  act(() => {
    editor.focus();
    const range = document.createRange();
    range.setStart(...locate(start));
    range.setEnd(...locate(end));
    const selection = document.getSelection();
    selection?.removeAllRanges();
    selection?.addRange(range);
    document.dispatchEvent(new Event("selectionchange"));
  });
}
