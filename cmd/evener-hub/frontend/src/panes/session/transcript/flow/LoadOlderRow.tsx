// LoadOlderRow: the older-turn paging STATUS row, plus the sentinel whose
// visibility triggers the fetch.
//
// Paging is automatic. An IntersectionObserver watches this row: as it comes
// within a page's reach of the viewport, older turns load, and the reader keeps
// scrolling into history without ever pressing anything. useTranscriptScroll's
// own near-top scroll trigger stays as it is - both call the same loadOlder(),
// whose re-entrancy guard (useTranscript.ts) collapses an overlap into one
// fetch, so the two mechanisms cannot double-fetch. The observer is the one
// that fires without a scroll event at all, which is what makes a short first
// page fill itself in.
//
// The row itself renders one of three things, and never nothing: a quiet
// "Loading older turns…" while a page is in flight, an error with a retry
// button when one failed, or - idle, with more history to fetch - a quiet
// "Older turns" label. The idle label is deliberately not a button: it is a
// place-marker for automatic work, and the only pressable thing here is the
// retry, which exists because Jesse ruled out a fallback "load more" button but
// silent failure is not an option. Retry is also the accessible escape hatch: a
// failed fetch must be recoverable without pixel-precise scrolling.
import { useEffect, useRef } from "react";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./loadolderrow.module.css";

export interface LoadOlderRowProps {
  // Fetches the next older page. Called by the sentinel automatically, and by
  // the retry button after a failure.
  onLoad: () => void;
  loading: boolean;
  // The last failed fetch's finished sentence, or null when the last attempt
  // succeeded (or none has been made). Rendered verbatim: useTranscript
  // already labelled it, and only useTranscript can tell a failed page fetch
  // from the failed session resume behind it, so a label added here would
  // talk over that.
  error: string | null;
}

const CLASS = {
  row: requireClass(styles.row, "loadolderrow.module.css", "row"),
  label: requireClass(styles.label, "loadolderrow.module.css", "label"),
  error: requireClass(styles.error, "loadolderrow.module.css", "error"),
  retry: requireClass(styles.retry, "loadolderrow.module.css", "retry"),
};

// How far outside the viewport the sentinel starts counting as "approaching".
// A page-sized margin so the fetch is already in flight by the time the reader
// reaches the top, rather than starting when they hit it.
const PREFETCH_MARGIN = "400px";

export function LoadOlderRow({ onLoad, loading, error }: LoadOlderRowProps) {
  const sentinelRef = useRef<HTMLDivElement>(null);
  // Latest-ref so the observer - attached once - never calls a stale
  // onLoad/loading pair. loadOlder's identity changes on every loadingOlder
  // flip (useTranscript.ts), and a stale closure would read a stale guard.
  const onLoadRef = useRef(onLoad);
  onLoadRef.current = onLoad;
  const blockedRef = useRef(false);
  // A failed fetch stops the automatic retry loop: without this the observer
  // would re-fire against a still-visible sentinel and hammer a failing
  // endpoint. The retry button (and any later successful fetch, which moves
  // the sentinel) is what clears it.
  blockedRef.current = error !== null;

  useEffect(() => {
    const el = sentinelRef.current;
    // jsdom has no IntersectionObserver at all; a test that cares stubs it the
    // way DockHost.test.tsx stubs ResizeObserver. Guarding here keeps every
    // other test that merely renders this row working untouched.
    if (!el || typeof IntersectionObserver !== "function") return undefined;
    const observer = new IntersectionObserver(
      (entries) => {
        if (blockedRef.current) return;
        if (entries.some((e) => e.isIntersecting)) onLoadRef.current();
      },
      { rootMargin: PREFETCH_MARGIN },
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  return (
    <div className={CLASS.row} data-testid="load-older-row">
      <div ref={sentinelRef} data-testid="load-older-sentinel" />
      {error !== null ? (
        <>
          {/* role=alert: a failure the reader did not ask for and cannot see
              coming needs announcing, unlike the two quiet states. */}
          <span role="alert" className={CLASS.error}>
            {error}
          </span>
          <button type="button" data-testid="load-older-retry" className={CLASS.retry} onClick={onLoad}>
            Retry
          </button>
        </>
      ) : (
        <span className={CLASS.label}>{loading ? "Loading older turns…" : "Older turns"}</span>
      )}
    </div>
  );
}
