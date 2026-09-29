// LoadOlderRow: the older-turn paging STATUS row (a quiet loading line, an
// inline error with Retry) plus the geometry-driven trigger that fills a page
// too short to scroll.
//
// Paging is automatic. Each surface owns the near-top scroll rule (the live
// pane's useTranscriptScroll, the read-only pane's own listener); this row
// covers the one case that rule cannot see - a page too short to fill its
// scroll port, where there is nothing to scroll and so no scroll event ever
// fires.
//
// Why it reads the SCROLL PORT's geometry instead of watching a sentinel of
// its own: the row renders into FlowOverlay's non-scrolling top slot, so
// anything of its own sits inside the viewport at every scroll position. An
// IntersectionObserver on such a sentinel reports "intersecting" the instant
// it is observed, which loaded an older page on every session open whether or
// not the reader was anywhere near the top of history. Reading the port's real
// geometry (scrollMetrics.shouldAutoLoadOlder) also lets the check run again
// on every ResizeObserver notification - the mount, a pane resize, and the
// virtualizer settling its estimates all re-decide - where a one-shot check
// would leave a transcript that starts out overflowing and later fits with
// older history it can never scroll to reach.
//
// The row renders a quiet "Loading older turns…" while a page is in flight, an
// error with a retry button when one failed, and nothing when idle: paging is
// automatic, so a standing "Older turns" banner would only narrate work the
// reader never asked about. The only pressable thing here is the retry, which
// exists because Jesse ruled out a fallback "load more" button but silent
// failure is not an option. Retry is also the accessible escape hatch: a
// failed fetch must be recoverable without pixel-precise scrolling.
import { useEffect, useRef } from "react";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./loadolderrow.module.css";
import { shouldAutoLoadOlder } from "./scrollMetrics";

export interface LoadOlderRowProps {
  // Fetches the next older page. Called automatically by the geometry check
  // below, and by the retry button after a failure.
  onLoad: () => void;
  loading: boolean;
  // The last failed fetch's finished sentence, or null when the last attempt
  // succeeded (or none has been made). Rendered verbatim: useTranscript
  // already labelled it, and only useTranscript can tell a failed page fetch
  // from the failed session resume behind it, so a label added here would
  // talk over that.
  error: string | null;
  // The transcript's scroll element, read to decide whether older history
  // should load and re-read whenever its geometry changes.
  scrollElement: () => HTMLElement | null;
}

const CLASS = {
  row: requireClass(styles.row, "loadolderrow.module.css", "row"),
  label: requireClass(styles.label, "loadolderrow.module.css", "label"),
  error: requireClass(styles.error, "loadolderrow.module.css", "error"),
  retry: requireClass(styles.retry, "loadolderrow.module.css", "retry"),
};

// How many frames to keep looking for a scroll element that has not mounted
// yet. The row and the list render in the same commit, so the first check
// normally finds it; this only keeps a late mount from silently disabling
// paging for the pane's whole life.
const PORT_RETRY_FRAMES = 5;

export function LoadOlderRow({ onLoad, loading, error, scrollElement }: LoadOlderRowProps) {
  // Latest-ref so the observer - attached once - never calls a stale
  // onLoad/scrollElement pair. loadOlder's identity changes on every
  // loadingOlder flip (useTranscript.ts), and a stale closure would read a
  // stale guard.
  const onLoadRef = useRef(onLoad);
  onLoadRef.current = onLoad;
  const scrollElementRef = useRef(scrollElement);
  scrollElementRef.current = scrollElement;
  // A failed fetch stops the automatic retry loop: without this the observer
  // would re-fire against a still-too-short port and hammer a failing
  // endpoint. The retry button (and any later successful fetch, which grows
  // the content) is what clears it.
  const blockedRef = useRef(false);
  blockedRef.current = error !== null;

  useEffect(() => {
    const maybeLoad = () => {
      if (blockedRef.current) return;
      const el = scrollElementRef.current();
      if (el === null || !shouldAutoLoadOlder(el)) return;
      onLoadRef.current();
    };
    // jsdom has no ResizeObserver at all; a test that cares stubs it the way
    // DockHost.test.tsx stubs it for dockview. Without one there is still the
    // one geometry check to make; with one, its initial notification for each
    // observed target IS that check, and it runs again on every later change.
    if (typeof ResizeObserver !== "function") {
      maybeLoad();
      return undefined;
    }
    let observer: ResizeObserver | null = null;
    let port: HTMLElement | null = null;
    let content: HTMLElement | null = null;
    // Both halves of the geometry the decision reads are observed: the port's
    // (a pane resize) and its content's (the rows settling, or the transcript
    // shrinking below the port). Re-resolving every time matters because a
    // transcript can hand back a different element (or none, before the list
    // mounts) and watching a node it has moved on from would never fire again.
    const sync = () => {
      if (observer === null) return;
      const el = scrollElementRef.current();
      if (el !== port) {
        if (port !== null) observer.unobserve(port);
        if (content !== null) observer.unobserve(content);
        port = el;
        content = null;
        if (port !== null) {
          observer.observe(port);
          const child = port.firstElementChild;
          if (child instanceof HTMLElement) {
            observer.observe(child);
            content = child;
          }
        }
      }
      maybeLoad();
    };
    observer = new ResizeObserver(sync);
    sync();
    let frame: number | null = null;
    let remaining = PORT_RETRY_FRAMES;
    const retryUntilMounted = () => {
      if (port !== null || remaining <= 0) return;
      remaining -= 1;
      frame = requestAnimationFrame(() => {
        sync();
        retryUntilMounted();
      });
    };
    retryUntilMounted();
    return () => {
      if (frame !== null) cancelAnimationFrame(frame);
      observer?.disconnect();
    };
  }, []);

  return (
    <div className={CLASS.row} data-testid="load-older-row">
      {error !== null ? (
        <>
          {/* role=alert: a failure the reader did not ask for and cannot see
              coming needs announcing, unlike the quiet loading state. */}
          <span role="alert" className={CLASS.error}>
            {error}
          </span>
          <button type="button" data-testid="load-older-retry" className={CLASS.retry} onClick={onLoad}>
            Retry
          </button>
        </>
      ) : (
        loading && <span className={CLASS.label}>Loading older turns…</span>
      )}
    </div>
  );
}
