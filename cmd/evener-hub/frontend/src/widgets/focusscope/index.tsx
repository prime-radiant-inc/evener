import { type ReactNode, useEffect, useRef } from "react";
import { tabbable } from "./tabbable";

export interface FocusScopeProps {
  /** When true, Tab/Shift+Tab loop within the scope instead of leaving it
   * (modal behavior - Dialog, Sheet, and Menu all pass true). When false
   * (default), focus still moves in on mount and is restored on unmount,
   * but Tab is left to the browser's normal traversal. */
  trap?: boolean;
  /** When false, the scope neither moves focus in on mount nor restores it
   * on unmount — for combobox-style popovers whose focus must STAY in the
   * anchoring input while the panel is open (Popover autoFocus={false}).
   * Default true (Dialog/Sheet/Menu behavior). */
  autoFocus?: boolean;
  children: ReactNode;
}

/**
 * Focus-management primitive Dialog/Sheet/Menu build on. Ports the
 * semantics of the old UI's cmd/evener-hub/assets/focus-trap.js
 * (EvenerFocusTrap): on mount, remember whatever had focus and move focus to
 * the first tabbable descendant (or the scope itself, if there is none); on
 * unmount, restore focus to what was remembered. When `trap` is true, Tab
 * and Shift+Tab additionally cycle within the scope's tabbable elements
 * instead of leaving it.
 *
 * Renders a plain wrapping <div tabIndex={-1}> (no styling of its own -
 * Dialog/Sheet/Menu own all visible chrome) so there is always a valid
 * fallback focus target even when the scope's content has nothing tabbable.
 */
export function FocusScope({ trap = false, autoFocus = true, children }: FocusScopeProps) {
  const ref = useRef<HTMLDivElement>(null);
  const restoreTargetRef = useRef<Element | null>(null);
  const ownerDocumentRef = useRef<Document | null>(null);

  // Mount: capture the restore target, then move focus in. Unmount:
  // restore, if the target is still focusable and still in the document.
  // Skipped entirely for autoFocus={false} scopes (combobox popovers), whose
  // anchoring input must keep focus for continued typing.
  useEffect(() => {
    if (!autoFocus) return;
    const scope = ref.current;
    // The scope's own document: in a Dockview popout the active element and the
    // focus target both belong to that window, and the opener's document knows
    // neither.
    const doc = scope?.ownerDocument ?? document;
    ownerDocumentRef.current = doc;
    restoreTargetRef.current = doc.activeElement;
    if (scope) {
      const first = tabbable(scope)[0];
      (first ?? scope).focus();
    }
    return () => {
      const target = restoreTargetRef.current;
      // Realm-independent: a node adopted into another window keeps its original
      // prototype, so `instanceof HTMLElement` from either window rejects it.
      const focusable =
        target !== null && typeof target === "object" && (target as Node).nodeType === 1
          ? (target as HTMLElement)
          : null;
      // Its document as it is now: an adopted node belongs to the window it was
      // moved into, not to the one this scope mounted in.
      const home = focusable?.ownerDocument ?? doc;
      if (focusable !== null && home.contains(focusable) && typeof focusable.focus === "function") {
        focusable.focus();
      }
    };
  }, [autoFocus]);

  // Tab-cycling, active only while trapping. Re-binds if `trap` changes.
  useEffect(() => {
    if (!trap) return;
    const scope = ref.current;
    if (!scope) return;

    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== "Tab" || !scope) return;
      const list = tabbable(scope);
      if (list.length === 0) {
        event.preventDefault();
        return;
      }
      const first = list[0];
      const last = list[list.length - 1];
      if (!first || !last) return; // list.length >= 1 was just checked above
      const active = (ownerDocumentRef.current ?? scope.ownerDocument ?? document).activeElement;
      if (event.shiftKey && active === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && active === last) {
        event.preventDefault();
        first.focus();
      }
    }

    scope.addEventListener("keydown", onKeyDown);
    return () => scope.removeEventListener("keydown", onKeyDown);
  }, [trap]);

  return (
    <div ref={ref} tabIndex={-1}>
      {children}
    </div>
  );
}
