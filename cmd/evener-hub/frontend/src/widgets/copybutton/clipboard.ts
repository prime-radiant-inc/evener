// clipboard.ts: the copy implementation behind CopyButton. Tries the async
// Clipboard API first; falls back to a hidden-textarea + execCommand("copy")
// when that API is unavailable or throwing (e.g. a non-secure-context remote
// hub, where navigator.clipboard is undefined entirely). Every step runs on
// the document that owns the button: Dockview pops a pane into its own
// document, where the opener's document and clipboard are the wrong ones.
function copyViaExecCommand(text: string, doc: Document): boolean {
  // Focusing the textarea moves focus out of a dialog; an Escape afterwards
  // would no longer reach the dialog's handler. Remember what had focus and
  // give it back once the textarea is gone.
  const previousFocus = doc.activeElement;
  const textarea = doc.createElement("textarea");
  textarea.value = text;
  // Off-screen but still focusable/selectable - execCommand("copy") only
  // acts on the current selection.
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  doc.body.appendChild(textarea);
  textarea.focus();
  textarea.select();
  let ok = false;
  try {
    ok = doc.execCommand("copy");
  } catch {
    ok = false;
  } finally {
    doc.body.removeChild(textarea);
    // Dockview-adopted nodes keep their opener realm's prototype, so an
    // instanceof check here would reject a perfectly focusable element.
    if (previousFocus?.isConnected && "focus" in previousFocus) {
      (previousFocus as HTMLElement).focus({ preventScroll: true });
    }
  }
  return ok;
}

/** Writes text to the clipboard, reporting whether either path succeeded.
 * doc defaults to the opener document; a control rendered in another
 * document (a popout) passes its own. */
export async function copyText(text: string, doc: Document = document): Promise<boolean> {
  const clipboard = doc.defaultView?.navigator.clipboard;
  if (clipboard?.writeText) {
    try {
      await clipboard.writeText(text);
      return true;
    } catch {
      // fall through to the execCommand fallback below
    }
  }
  return copyViaExecCommand(text, doc);
}
