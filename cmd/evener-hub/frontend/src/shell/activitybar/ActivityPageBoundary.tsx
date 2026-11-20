import type { SessionActivityCollection } from "@evener/appwire-client";
import { useCallback, useEffect, useRef, useState } from "react";
import { Skeleton } from "../../widgets";
import { useActivityViewCurrent } from "./ActivityViewport";

interface ActivityPageBoundaryProps {
  resource: SessionActivityCollection;
  rows: readonly unknown[];
  hasMore: boolean;
  loading: boolean;
  error: unknown;
  permanent: boolean;
  loadMore(resource: SessionActivityCollection): Promise<void>;
}

// Views supply page demand near their visible boundary. The shared store owns
// admission and recovery; an error must not turn visibility into a retry loop.
export function ActivityPageBoundary({
  resource,
  rows,
  hasMore,
  loading,
  error,
  permanent,
  loadMore,
}: ActivityPageBoundaryProps) {
  const element = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(false);
  const currentVisibility = useRef(false);
  const observedRows = useRef(rows);
  const isCurrent = useActivityViewCurrent();
  const admit = useCallback(() => {
    // Thunked so a synchronous throw reaches the chain: evaluating it as an
    // argument would escape the catch and surface an unhandled rejection.
    return Promise.resolve()
      .then(() => loadMore(resource))
      .catch(() => {});
  }, [loadMore, resource]);
  useEffect(() => {
    observedRows.current = rows;
    currentVisibility.current = false;
    setVisible(false);
    const target = element.current;
    if (!target || !hasMore || loading || typeof IntersectionObserver !== "function") return;
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
  }, [hasMore, loading, rows, isCurrent]);
  useEffect(() => {
    if (!isCurrent() || !hasMore || loading || error || permanent) return;
    if (!(visible && currentVisibility.current)) return;
    const admitPage = () => {
      if (isCurrent() && observedRows.current === rows) void admit();
    };
    admitPage();
  }, [visible, hasMore, loading, error, permanent, admit, isCurrent, rows]);
  if (!hasMore || permanent) return null;
  return (
    <div ref={element}>
      <Skeleton lines={1} />
    </div>
  );
}
