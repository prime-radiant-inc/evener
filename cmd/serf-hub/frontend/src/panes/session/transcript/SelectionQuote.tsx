// Adapted from Beautiful UI's Selection Actions (beautifului.dev, MIT © 2026 Shane Levine) — see LICENSES/beautiful-ui.txt.
//
// A small floating action bar that appears near a text selection made
// inside transcript MESSAGE content (never chrome, never the composer) and
// offers a short list of actions to run against the selected text. Ships
// with exactly one action, wired by Session.tsx ("Quote in reply" -> the
// composer draft via quoteInsert.ts) - `actions` is a list so a future
// action is just another entry, not a structural change here.
//
// Containment (jsdom has no real selection/layout - see this component's
// own test file header) is decided purely off DOM shape, not a live text
// search: selectionQuoteLogic.ts's messageContentElement walks up from the
// selection's own commonAncestorContainer looking for the nearest
// data-view-anchor-message="true" wrapper - the SAME attribute TurnBlock.tsx
// and Session.tsx already stamp on every message item (both view modes), so
// this file needs no new DOM marker convention and never touches
// transcript/messages/ itself. A selection anywhere else under
// `containerRef` - a turn separator, the seen divider, an action-group
// summary - reports "not message content" and the bar never shows.
//
// Listening: `pointerup` inside the container catches the common
// mouse/touch drag-to-select gesture immediately; `selectionchange`
// (debounced) is the fallback that also catches keyboard selection
// (Shift+Arrow) and selection CLEARING (a click elsewhere collapses the
// range, which this same handler reads as "hide the bar" - no separate
// outside-click listener is needed). `mousedown` on the bar itself is
// preventDefault()'d so pressing its own button never collapses the
// selection out from under the click that is about to read it. The bar is
// `position: fixed` and positioned once per selection (see the useLayoutEffect
// below) - it does NOT track scroll, so a capture-phase `scroll` listener on
// document dismisses it on any scroll (the transcript pane, a nested
// scroller, anywhere) rather than leaving it floating over content it no
// longer points at.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { Button } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { clampToBounds, messageContentElement } from "./selectionQuoteLogic";
import styles from "./selectionquote.module.css";

export interface SelectionAction {
  label: string;
  onInvoke: (selectedText: string) => void;
}

export interface SelectionQuoteProps {
  containerRef: React.RefObject<HTMLElement | null>;
  actions: SelectionAction[];
}

const CLASS = {
  bar: requireClass(styles.bar, "selectionquote.module.css", "bar"),
};

// Debounce window for the selectionchange fallback - long enough to coalesce
// a keyboard selection's own rapid-fire events, short enough that the bar
// still feels immediate once the gesture settles.
const SELECTIONCHANGE_DEBOUNCE_MS = 150;

// Used only until the bar's own element has been measured once (the
// useLayoutEffect below); close enough to the real rendered size (one quiet
// button, --space-1 padding) that the first paint doesn't visibly jump once
// the real measurement lands.
const ESTIMATED_BAR_SIZE = { width: 140, height: 32 };

interface CapturedSelection {
  text: string;
  rect: DOMRect;
}

export function SelectionQuote({ containerRef, actions }: SelectionQuoteProps) {
  const [selection, setSelection] = useState<CapturedSelection | null>(null);
  const [position, setPosition] = useState<{ x: number; y: number } | null>(null);
  const barRef = useRef<HTMLDivElement>(null);
  // Read by the Mod+' handler inside the effect below without making
  // `actions` an effect dependency - every caller (Session.tsx included)
  // passes a fresh array literal each render, and re-subscribing the whole
  // listener set on every render for that reason alone would be wasteful
  // (same idiom as Composer.tsx's own textRef, kept live without being a
  // dependency).
  const actionsRef = useRef(actions);
  actionsRef.current = actions;

  const evaluate = useCallback(() => {
    const container = containerRef.current;
    if (!container) {
      setSelection(null);
      return;
    }
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) {
      setSelection(null);
      return;
    }
    const text = sel.toString();
    if (text.trim() === "") {
      setSelection(null);
      return;
    }
    const range = sel.getRangeAt(0);
    if (!messageContentElement(range.commonAncestorContainer, container)) {
      setSelection(null);
      return;
    }
    setSelection({ text, rect: range.getBoundingClientRect() });
  }, [containerRef]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return undefined;

    let debounceTimer: ReturnType<typeof setTimeout> | undefined;
    const debouncedEvaluate = () => {
      if (debounceTimer !== undefined) clearTimeout(debounceTimer);
      debounceTimer = setTimeout(evaluate, SELECTIONCHANGE_DEBOUNCE_MS);
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setSelection(null);
        return;
      }
      // Mod+' invokes the bar's first (and today, only) action directly
      // against the currently captured selection - the same code path a
      // click on the bar's own button takes (actions[0].onInvoke below),
      // just reachable without a pointer. A no-op with nothing captured.
      if (event.key === "'" && (event.metaKey || event.ctrlKey)) {
        setSelection((current) => {
          if (!current) return current;
          actionsRef.current[0]?.onInvoke(current.text);
          return null;
        });
      }
    };
    const handleScroll = () => setSelection(null);

    container.addEventListener("pointerup", evaluate);
    document.addEventListener("selectionchange", debouncedEvaluate);
    document.addEventListener("keydown", handleKeyDown);
    // Capture phase: a scroll inside any nested scroller (not just document)
    // still fires here on the way down, so the bar dismisses regardless of
    // which element actually scrolled.
    document.addEventListener("scroll", handleScroll, true);
    return () => {
      if (debounceTimer !== undefined) clearTimeout(debounceTimer);
      container.removeEventListener("pointerup", evaluate);
      document.removeEventListener("selectionchange", debouncedEvaluate);
      document.removeEventListener("keydown", handleKeyDown);
      document.removeEventListener("scroll", handleScroll, true);
    };
  }, [containerRef, evaluate]);

  // Positions the bar above the selection, centered on it, clamped so it
  // never spills outside the pane's own container rect. Runs after the
  // bar's own DOM exists so its REAL measured size (not ESTIMATED_BAR_SIZE)
  // drives the clamp - a two-pass layout, same idiom a positioned tooltip
  // uses, since the bar's rendered width depends on its label text.
  useLayoutEffect(() => {
    const container = containerRef.current;
    if (!selection || !container) {
      setPosition(null);
      return;
    }
    const containerRect = container.getBoundingClientRect();
    const bar = barRef.current;
    const barSize = bar
      ? { width: bar.offsetWidth || ESTIMATED_BAR_SIZE.width, height: bar.offsetHeight || ESTIMATED_BAR_SIZE.height }
      : ESTIMATED_BAR_SIZE;
    const gapAboveSelection = 8;
    const raw = {
      x: selection.rect.left + selection.rect.width / 2 - barSize.width / 2 - containerRect.left,
      y: selection.rect.top - barSize.height - gapAboveSelection - containerRect.top,
    };
    const clamped = clampToBounds(raw, barSize, { width: containerRect.width, height: containerRect.height });
    setPosition({ x: clamped.x + containerRect.left, y: clamped.y + containerRect.top });
  }, [selection, containerRef]);

  if (!selection || !position) return null;

  return (
    <div
      ref={barRef}
      className={CLASS.bar}
      style={{ left: `${position.x}px`, top: `${position.y}px` }}
      role="toolbar"
      aria-label="Selection actions"
      // See this component's own header comment: without this, the native
      // mousedown-collapses-selection behavior fires before the button's
      // click handler ever reads `selection.text`.
      onMouseDown={(event) => event.preventDefault()}
    >
      {actions.map((action) => (
        <Button
          key={action.label}
          variant="quiet"
          size="xs"
          type="button"
          onClick={() => {
            action.onInvoke(selection.text);
            setSelection(null);
          }}
        >
          {action.label}
        </Button>
      ))}
    </div>
  );
}
