/**
 * A real second document and window for popout-realm widget tests.
 *
 * jsdom gives an iframe its own constructors; a document from
 * `document.implementation.createHTMLDocument` shares the opener's, so it
 * cannot catch an `instanceof` realm defect. Render through
 * `createPortal(children, popout.doc.body)` to mount a widget there, and call
 * `frame.remove()` when the test is done.
 */
export function appendPopout(): { frame: HTMLIFrameElement; doc: Document; win: Window & typeof globalThis } {
  const frame = document.createElement("iframe");
  document.body.append(frame);
  const doc = frame.contentDocument;
  if (doc === null) throw new Error("iframe has no contentDocument");
  const win = doc.defaultView;
  if (win === null) throw new Error("iframe has no window");
  return { frame, doc, win: win as Window & typeof globalThis };
}
