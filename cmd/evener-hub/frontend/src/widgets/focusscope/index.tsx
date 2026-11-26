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
  /** Extra class for the scope's root <div>. Defaults to none, so existing
   * callers render exactly as before. */
  className?: string;
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
 * A caller that owns the root's layout passes className (ActivitySidebar's
 * flex body).
 */
export function FocusScope(props: FocusScopeProps) {
  return <LayerFocusScope {...props} released={false} />;
}

/**
 * FocusScope for a layer that stays mounted while it leaves (see
 * widgets/internal/usePresence.ts). `released` is the layer's closing phase:
 * releasing the scope stops the trap and restores focus at once, exactly as
 * an unmount would, so a leaving layer neither holds nor takes focus; the
 * later unmount restores nothing a second time. Un-releasing it (the layer
 * reopened during its exit) captures the restore target and moves focus in
 * again, as a fresh mount does. Internal to the overlay widgets, not barrelled.
 */
export function LayerFocusScope({
  trap = false,
  autoFocus = true,
  released,
  restoreWithoutScroll,
  className,
  children,
}: FocusScopeProps & {
  released: boolean;
  /** Read at the moment the scope is released, not when it mounted: a caller
   * whose close is LAYOUT's doing (Menu, when layout hid the trigger) must put
   * focus back where it came from without moving the page, and the decision
   * belongs to the close. A ref rather than a boolean because a prop would
   * have to sit in the effect's dependencies, and re-running that effect to
   * read a new value would capture the target and move focus a second time.
   * Escape and outside presses leave it false: those are the reader's own
   * navigation, and the ordinary restore - which may bring the control back
   * into view - is what they asked for. */
  restoreWithoutScroll?: { current: boolean };
}) {
  const ref = useRef<HTMLDivElement>(null);
  const restoreTargetRef = useRef<Element | null>(null);
  const ownerDocumentRef = useRef<Document | null>(null);
  const preCommitRestoreRef = useRef<{ doc: Document; target: Element | null } | null>(null);
  const activeRef = useRef(false);
  const childFocusedOnMountRef = useRef<Element | null>(null);

  if (!activeRef.current && autoFocus && !released) {
    const preDoc = ref.current?.ownerDocument ?? (typeof document !== "undefined" ? document : null);
    preCommitRestoreRef.current = preDoc ? { doc: preDoc, target: preDoc.activeElement } : null;
    childFocusedOnMountRef.current = null;
  }

  // Mount (or un-release): capture the restore target, then move focus in.
  // Unmount (or release): restore, if the target is still focusable and still
  // in the document.
  // Skipped entirely for autoFocus={false} scopes (combobox popovers), whose
  // anchoring input must keep focus for continued typing.
  // biome-ignore lint/correctness/useExhaustiveDependencies: restoreWithoutScroll is read at the CLOSE, by design - putting a ref's .current in the dependency list would re-run this effect whenever the value changed, and re-running it re-captures the restore target and moves focus a second time. The close that sets it is the only thing that should decide it.
  useEffect(() => {
    if (!autoFocus || released) return;
    activeRef.current = true;
    const scope = ref.current;
    // The scope's own document: in a Dockview popout the active element and the
    // focus target both belong to that window, and the opener's document knows
    // neither.
    const doc = scope?.ownerDocument ?? document;
    ownerDocumentRef.current = doc;
    const liveActive = doc.activeElement;
    const preCommit = preCommitRestoreRef.current;
    const preTarget =
      preCommit !== null && preCommit.doc === doc && preCommit.target !== null && !scope?.contains(preCommit.target)
        ? preCommit.target
        : null;
    // Child autoFocus or mount effects run before this parent effect and move
    // activeElement inside the scope; keep the pre-commit opener so closing
    // never tries to restore to an unmounted or inert child.
    restoreTargetRef.current = scope?.contains(liveActive) ? preTarget : liveActive;
    if (scope) {
      if (scope.contains(liveActive) && liveActive !== scope) {
        childFocusedOnMountRef.current = liveActive;
      } else {
        const list = tabbable(scope);
        const retained = childFocusedOnMountRef.current;
        const initial =
          retained !== null && list.includes(retained as HTMLElement) ? (retained as HTMLElement) : list[0];
        // preventScroll: the layer is already placed where the user is looking
        // (a fixed or anchored overlay); a plain focus() of the unstyled scope
        // wrapper at the end of <body> scrolls the page to it, and the scroll
        // closes a Popover or Menu that was just opened.
        (initial ?? scope).focus({ preventScroll: true });
      }
    }
    return () => {
      activeRef.current = false;
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
        // A layout-driven close hands focus back without scrolling: the
        // control it goes to can be exactly the one layout has just pushed out
        // of sight. Focusing the trigger with preventScroll upstream is not
        // enough on its own - a pointer click that opened the menu never
        // focused the trigger, so the recorded target is some OTHER control,
        // and this restore is the path that would scroll to it.
        const withoutScroll = restoreWithoutScroll?.current === true;
        focusable.focus(withoutScroll ? { preventScroll: true } : undefined);
      }
    };
  }, [autoFocus, released]);

  // Tab-cycling, active only while trapping an unreleased scope.
  useEffect(() => {
    if (!trap || released) return;
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
  }, [trap, released]);

  return (
    <div ref={ref} tabIndex={-1} className={className} data-focus-layer={trap ? "" : undefined}>
      {children}
    </div>
  );
}
