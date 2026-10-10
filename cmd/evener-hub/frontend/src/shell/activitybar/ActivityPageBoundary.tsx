import type { SessionActivityCollection } from "@evener/appwire-client";
import { useCallback, useEffect, useRef, useState } from "react";
import { Button } from "../../widgets";
import { useActivityViewCurrent } from "./ActivityViewport";

interface ActivityPageBoundaryProps {
  resource: SessionActivityCollection;
  label: string;
  rows: readonly unknown[];
  hasMore: boolean;
  loading: boolean;
  error: unknown;
  permanent: boolean;
  enabled?: boolean;
  restore?: boolean;
  loadMore(resource: SessionActivityCollection): Promise<void>;
}

// Views supply page demand near their visible boundary. The shared store owns
// admission and recovery; an error must not turn visibility into a retry loop.
export function ActivityPageBoundary({
  resource,
  label,
  rows,
  hasMore,
  loading,
  error,
  permanent,
  enabled = true,
  restore = false,
  loadMore,
}: ActivityPageBoundaryProps) {
  const element = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(false);
  // A background refresh of the collection must present nothing: only a page
  // this control actually asked for shows the loading label and disables it.
  const [pagePending, setPagePending] = useState(false);
  const currentVisibility = useRef(false);
  const observedRows = useRef(rows);
  const isCurrent = useActivityViewCurrent();
  const admit = useCallback(() => {
    setPagePending(true);
    // Thunked so a synchronous throw reaches the chain: evaluating it as an
    // argument would escape the catch and leave the control pending forever.
    return Promise.resolve()
      .then(() => loadMore(resource))
      .catch(() => {})
      .finally(() => setPagePending(false));
  }, [loadMore, resource]);
  useEffect(() => {
    observedRows.current = rows;
    currentVisibility.current = false;
    setVisible(false);
    const target = element.current;
    if (!target || !enabled || !hasMore || loading || typeof IntersectionObserver !== "function") return;
    let active = true;
    const observer = new IntersectionObserver(
      (entries) => {
        if (!active || !isCurrent() || observedRows.current !== rows) return;
        const next = entries.some((entry) => entry.isIntersecting);
        currentVisibility.current = next;
        setVisible(next);
      },
      { rootMargin: "200px" },
    );
    observer.observe(target);
    return () => {
      active = false;
      currentVisibility.current = false;
      observer.disconnect();
    };
    // A successful page changes rows even when React batches the loading states.
    // Re-observe the moved boundary so stale geometry cannot drain unseen pages.
  }, [enabled, hasMore, loading, rows, isCurrent]);
  useEffect(() => {
    if (!isCurrent() || !enabled || !hasMore || loading || error || permanent) return;
    if (!restore && !(visible && currentVisibility.current)) return;
    const admitPage = () => {
      if (isCurrent() && observedRows.current === rows) void admit();
    };
    if (!restore) {
      admitPage();
      return;
    }
    // Restoring a previously inspected extent supplies bounded view demand.
    // Yield between fresh pages; failures remain the shared store's concern.
    const timer = setTimeout(admitPage, 100);
    return () => clearTimeout(timer);
  }, [enabled, visible, hasMore, loading, error, permanent, admit, isCurrent, restore, rows]);
  if (!hasMore) return null;
  return (
    <div ref={element}>
      <Button variant="quiet" size="sm" disabled={pagePending || permanent} onClick={() => void admit()}>
        {pagePending ? `Loading ${label}…` : `Load more ${label}`}
      </Button>
    </div>
  );
}
