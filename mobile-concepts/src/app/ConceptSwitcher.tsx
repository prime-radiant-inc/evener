import {
  type KeyboardEvent,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import type { ConceptId } from "../core/model";
import type { PlatformPrimitives } from "../core/platform";
import type { PrototypeAction } from "../core/state";
import { usePrototypeState } from "../core/store";
import { ConceptCards } from "./ConceptGallery";

const focusableSelector =
  "button:not([disabled]):not([tabindex='-1']), input:not([disabled]):not([tabindex='-1']), select:not([disabled]):not([tabindex='-1']), textarea:not([disabled]):not([tabindex='-1']), [href]:not([tabindex='-1']), [tabindex]:not([tabindex='-1'])";

function currentConceptSwitcherOpener(): HTMLElement | null {
  return document.querySelector<HTMLElement>(
    "[data-concept-switch-trigger='true']",
  );
}

export interface ConceptSwitcherProps {
  open: boolean;
  primitives: PlatformPrimitives;
  dispatch(action: PrototypeAction): void;
  onClose(): void;
}

export function ConceptSwitcher({
  open,
  primitives,
  dispatch,
  onClose,
}: ConceptSwitcherProps) {
  const selectedConcept = usePrototypeState((state) => state.concept);
  const dialogRef = useRef<HTMLElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const previousFocusRef = useRef<HTMLElement | null>(null);
  const wasOpenRef = useRef(false);
  const closingRef = useRef(false);
  const [closing, setClosing] = useState(false);

  const restoreFocus = useCallback(() => {
    const previous = previousFocusRef.current;
    const target = previous?.isConnected
      ? previous
      : currentConceptSwitcherOpener();
    target?.focus();
    previousFocusRef.current = null;
  }, []);

  const resetClosing = useCallback(() => {
    closingRef.current = false;
    setClosing(false);
  }, []);

  const beginClosing = useCallback(() => {
    if (closingRef.current) return false;
    closingRef.current = true;
    setClosing(true);
    return true;
  }, []);

  useEffect(() => {
    if (open) {
      if (!wasOpenRef.current) {
        resetClosing();
        const activeElement =
          document.activeElement instanceof HTMLElement
            ? document.activeElement
            : null;
        previousFocusRef.current =
          activeElement === null || activeElement === document.body
            ? currentConceptSwitcherOpener()
            : activeElement;
      }
      wasOpenRef.current = true;
      closeRef.current?.focus();
      return;
    }

    if (!wasOpenRef.current) return;
    wasOpenRef.current = false;
    restoreFocus();
    resetClosing();
  }, [open, resetClosing, restoreFocus]);

  useEffect(
    () => () => {
      if (wasOpenRef.current) restoreFocus();
      closingRef.current = false;
    },
    [restoreFocus],
  );

  useEffect(() => {
    if (!open) return;
    const containFocus = (event: FocusEvent) => {
      if (
        event.target instanceof Node &&
        !dialogRef.current?.contains(event.target)
      ) {
        closeRef.current?.focus();
      }
    };
    document.addEventListener("focusin", containFocus);
    return () => document.removeEventListener("focusin", containFocus);
  }, [open]);

  if (!open) return null;

  const onSelect = (concept: ConceptId) => {
    if (!beginClosing()) return;
    dispatch({ type: "selectConcept", concept });
    onClose();
  };

  const requestClose = () => {
    if (beginClosing()) onClose();
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      requestClose();
      return;
    }
    if (event.key !== "Tab") return;

    const focusable = Array.from(
      event.currentTarget.querySelectorAll<HTMLElement>(focusableSelector),
    );
    const first = focusable.at(0);
    const last = focusable.at(-1);
    if (!first || !last) return;
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  };

  return (
    <div className="concept-switcher-backdrop">
      <section
        ref={dialogRef}
        className="concept-switcher"
        role="dialog"
        aria-modal="true"
        aria-labelledby="concept-switcher-title"
        data-sheet={primitives.sheet}
        data-dialog={primitives.dialog}
        data-elevation={primitives.elevation}
        data-safe-area={primitives.safeArea}
        data-minimum-target={primitives.minimumTarget}
        data-closing={closing}
        aria-busy={closing}
        onKeyDown={handleKeyDown}
      >
        <header className="concept-switcher__header">
          <div>
            <p className="concept-switcher__eyebrow">Design direction</p>
            <h2 id="concept-switcher-title">Switch concept</h2>
            <p>Keep this interaction state and change only its presentation.</p>
          </div>
          <button
            ref={closeRef}
            type="button"
            aria-label="Close concept switcher"
            disabled={closing}
            onClick={requestClose}
          >
            Close
          </button>
        </header>
        <div className="concept-switcher__grid">
          <ConceptCards
            selectedConcept={selectedConcept}
            disabled={closing}
            onSelect={onSelect}
          />
        </div>
      </section>
    </div>
  );
}
