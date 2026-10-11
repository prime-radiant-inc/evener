// LoadOlderRow: the older-turn paging STATUS row (a quiet loading line, an
// inline error with Retry) plus the geometry-driven trigger that fills a page
// too short to scroll.
//
// Paging is automatic, and the near-top rule belongs to the scroll coordinator
// every transcript surface runs (useTranscriptScroll, both the live session
// pane and the read-only transcript pane). This row adds the one case that rule
// cannot see - a page too short to fill its scroll port, where there is nothing
// to scroll and so no scroll event ever fires.
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
import { portGeometryTargets, shouldAutoLoadOlder } from "./scrollMetrics";

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
  // Re-points the observation at whatever the transcript hands back now. Kept
  // in a ref so the render effect below can call the mounted effect's closure.
  const syncTargetsRef = useRef<() => void>(() => {});
  // The geometry check itself, for the render effect's no-observer fallback
  // (jsdom): without an observer, a port that appears on a later render would
  // otherwise never be checked.
  const maybeLoadRef = useRef<() => void>(() => {});

  useEffect(() => {
    const maybeLoad = () => {
      if (blockedRef.current) return;
      const el = scrollElementRef.current();
      if (el === null || !shouldAutoLoadOlder(el)) return;
      onLoadRef.current();
    };
    maybeLoadRef.current = maybeLoad;
    let observer: ResizeObserver | null = null;
    let observed: HTMLElement[] = [];
    // Both halves of the geometry the decision reads are observed
    // (portGeometryTargets). Re-resolving on every call matters because a
    // transcript can swap either node, and observing the new one is what makes
    // its initial notification fire the next check.
    const syncTargets = () => {
      if (observer === null) return;
      const el = scrollElementRef.current();
      const next = el === null ? [] : portGeometryTargets(el);
      if (next.length === observed.length && next.every((target, index) => target === observed[index])) return;
      for (const target of observed) observer.unobserve(target);
      observed = next;
      for (const target of next) observer.observe(target);
    };
    syncTargetsRef.current = syncTargets;
    // jsdom has no ResizeObserver at all; a test that cares stubs it the way
    // DockHost.test.tsx stubs it for dockview.
    if (typeof ResizeObserver === "function") {
      observer = new ResizeObserver(() => {
        syncTargets();
        maybeLoad();
      });
    }
    syncTargets();
    // The check itself: with an observer, its initial notification for each
    // observed target delivers it; without one, the render effect below does
    // (this effect cannot call it directly without checking twice on mount).
    return () => {
      syncTargetsRef.current = () => {};
      maybeLoadRef.current = () => {};
      observer?.disconnect();
    };
  }, []);

  // A swapped port, or a port's first content child appearing after the first
  // check, changes no observed border box - so the observer alone could never
  // notice it and the row would sit watching a detached node. Any render that
  // brings either node re-points the observation here (an identity compare
  // only; the new node's own initial notification does the check).
  useEffect(() => {
    syncTargetsRef.current();
    if (typeof ResizeObserver !== "function") maybeLoadRef.current();
  });

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
