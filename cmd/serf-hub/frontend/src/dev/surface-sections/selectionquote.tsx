// The selection-quote surface: the REAL SelectionQuote component
// (panes/session/transcript/SelectionQuote.tsx). It has no props for
// forcing itself open - the bar only appears behind a genuine
// window.getSelection() inside a message-content wrapper (a `pointerup` or
// `selectionchange` on the container, see that file's own header) - so
// nothing here fakes props or weakens production code. Instead this
// fabricates a REAL browser selection with the DOM Selection/Range APIs
// against sample message text, then dispatches the exact `pointerup` event
// SelectionQuote listens for - the same trigger a mouse drag-select
// produces, just synthesized instead of dragged. (SelectionQuote's own
// header notes jsdom's selection APIs are too thin to drive honestly, so
// under the render test this may show no bar at all - it still must mount
// without throwing, which is all that test asserts. In a real browser this
// reliably shows the bar.)
import { useEffect, useRef } from "react";
import { SelectionQuote } from "../../panes/session/transcript/SelectionQuote";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

function SelectionQuoteDemo() {
  const containerRef = useRef<HTMLDivElement>(null);
  const messageRef = useRef<HTMLParagraphElement>(null);

  useEffect(() => {
    const messageEl = messageRef.current;
    const selectionApi = window.getSelection?.();
    if (!messageEl || !selectionApi || typeof document.createRange !== "function") return;
    const range = document.createRange();
    range.selectNodeContents(messageEl);
    // jsdom's Range implements no layout (getBoundingClientRect is simply
    // absent - see SelectionQuote.tsx's own header on why this file avoids
    // driving jsdom's selection APIs), so under the render test this range
    // would make SelectionQuote's evaluate() throw the moment it measures
    // the selection. Feature-detect and skip there rather than fight jsdom's
    // gap: a real browser always has this method, so the demo is unaffected.
    if (typeof range.getBoundingClientRect !== "function") return;
    selectionApi.removeAllRanges();
    selectionApi.addRange(range);
    messageEl.dispatchEvent(new Event("pointerup", { bubbles: true }));
  }, []);

  return (
    <div ref={containerRef} style={{ position: "relative", padding: "var(--space-4)" }}>
      <p ref={messageRef} data-view-anchor-message="true">
        Swap the tail write for a rename so a concurrent reader always sees either the old or the new file, never a
        partial one.
      </p>
      <SelectionQuote containerRef={containerRef} actions={[{ label: "Quote in reply", onInvoke: () => {} }]} />
    </div>
  );
}

export default function SelectionQuoteSurfaceSection() {
  return (
    <section>
      <h2>Selection quote</h2>
      <p className={styles.note}>
        Real SelectionQuote bar, triggered by a genuine DOM selection made against sample message text on mount (a
        synthesized pointerup, the same event a mouse drag-select fires) - no props or production code changed to force
        it open.
      </p>
      <ThemeFlip>
        <SelectionQuoteDemo />
      </ThemeFlip>
    </section>
  );
}
