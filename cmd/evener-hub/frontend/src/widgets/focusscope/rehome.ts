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
 * established that the control holding focus INSIDE the root lost the ability to
 * hold it (useFocusRehome owns that judgement), passed here as `lost`.
 *
 * Focus is handed on when the browser left it nowhere (`<body>` is where an
 * unmounted control's focus goes) or when it is still sitting on `lost` - which
 * is how a natively disabled control reads in engines that do not drop focus
 * themselves (jsdom), while Chrome has already moved it to `<body>`. Focus that
 * is anywhere else by now is the user's or another component's and is left where
 * it is.
 */
export function rehomeFocus(root: HTMLElement | null, lost: HTMLElement | null = null): void {
  if (root === null) return;
  const active = document.activeElement;
  if (active !== document.body && active !== lost) return;
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
    const remember = (event: FocusEvent): void => {
      const target = event.target;
      // root.current is resolved at EVENT time, not captured when the listener is
      // bound: a surface can replace the node the ref points at - the connect
      // dialog's body is unmounted when an editor opens and remounted when it
      // closes - and a listener holding the old node would stop remembering
      // anything for the rest of the session.
      if (target instanceof HTMLElement && root.current?.contains(target)) lastFocused.current = target;
    };
    document.addEventListener("focusin", remember, true);
    return () => document.removeEventListener("focusin", remember, true);
  }, [root]);

  // No dependency array on purpose: the condition is "the control this hook
  // remembered can no longer hold focus", and any commit can be the one that made
  // it so - a listing change, a read starting, a read failing.
  useEffect(() => {
    const remembered = lastFocused.current;
    if (remembered === null) return;
    // Gone, or natively disabled: the browser drops focus to <body> the moment a
    // focused control is disabled (the instance sheet's pending "Test
    // credentials" is one), which is the same lost focus this hook exists for.
    // aria-disabled is deliberately NOT this case - a control refused that way
    // keeps the keyboard where it is, which is why it is not the native
    // attribute.
    if (remembered.isConnected && !remembered.matches(":disabled")) return;
    lastFocused.current = null;
    rehomeFocus(root.current, remembered);
  });
}
