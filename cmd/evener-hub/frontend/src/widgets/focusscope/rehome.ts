// Focus recovery for surfaces whose content changes under the keyboard: a
// listing that no longer carries the focused row, a refresh that swaps the rows
// for a skeleton, an error banner that replaces them. React unmounts the focused
// control, the browser moves focus to <body> (measured in Chrome), and FocusScope
// (./index.tsx) focuses on mount only - so nothing puts it back, and the user's
// next Tab starts from the top of the document.
//
// The two pieces are deliberately separate. useFocusRehome decides WHEN - it is
// the only thing that knows a control it was watching disappeared - and
// rehomeFocus does it, handing focus to the root's first tabbable control.
// Deciding on `document.activeElement === document.body` alone would be wrong:
// <body> is where focus starts on a fresh mount and where it rests after a click
// on non-focusable content, so that test alone moves the keyboard into a dialog
// or pane nobody was using.
import { type RefObject, useEffect, useRef } from "react";
import { tabbable } from "./tabbable";

/**
 * Hands focus to the root's first tabbable control. Called once the caller has
 * established that the control holding focus INSIDE the root was just removed
 * (useFocusRehome owns that judgement); focus that is anywhere else by now - the
 * user moved it, another component took it - is left where it is.
 */
export function rehomeFocus(root: HTMLElement | null): void {
  if (root === null) return;
  if (document.activeElement !== document.body) return;
  const next = tabbable(root)[0];
  next?.focus();
}

/**
 * useFocusRehome keeps the keyboard inside `root` when a commit removes the
 * control that held it.
 *
 * It remembers whatever focus lands inside the root, however it got there - a
 * click between two commits counts as much as one this component caused - and
 * after every commit checks whether that control is still in the document. Only
 * a remembered control that is GONE re-homes focus: a fresh mount, a page whose
 * focus is still on <body>, or a control that survived the commit are all left
 * alone.
 */
export function useFocusRehome(root: RefObject<HTMLElement | null>): void {
  const lastFocused = useRef<HTMLElement | null>(null);

  // Capture phase: focusin does not bubble, but a capture-phase listener on the
  // document sees every focus that lands anywhere, including on controls that
  // stop propagation.
  useEffect(() => {
    const node = root.current;
    if (node === null) return;
    const remember = (event: FocusEvent): void => {
      const target = event.target;
      if (target instanceof HTMLElement && node.contains(target)) lastFocused.current = target;
    };
    document.addEventListener("focusin", remember, true);
    return () => document.removeEventListener("focusin", remember, true);
  }, [root]);

  // No dependency array on purpose: the condition is "the control this hook
  // remembered is no longer in the document", and any commit can be the one that
  // removed it - a listing change, a read starting, a read failing.
  useEffect(() => {
    const remembered = lastFocused.current;
    if (remembered === null || remembered.isConnected) return;
    lastFocused.current = null;
    rehomeFocus(root.current);
  });
}
