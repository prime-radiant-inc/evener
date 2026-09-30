import type { SessionActivityCollection } from "@evener/appwire-client";
import { useEffect, useRef, useState } from "react";
import { Button } from "../../widgets";

interface ActivityPageBoundaryProps {
  resource: SessionActivityCollection;
  label: string;
  hasMore: boolean;
  loading: boolean;
  error: unknown;
  permanent: boolean;
  enabled?: boolean;
  loadMore(resource: SessionActivityCollection): Promise<void>;
}

// Views supply page demand near their visible boundary. The shared store owns
// admission and recovery; an error must not turn visibility into a retry loop.
export function ActivityPageBoundary({
  resource,
  label,
  hasMore,
  loading,
  error,
  permanent,
  enabled = true,
  loadMore,
}: ActivityPageBoundaryProps) {
  const element = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    const target = element.current;
    if (!target || !enabled || !hasMore || typeof IntersectionObserver !== "function") return;
    const observer = new IntersectionObserver((entries) => setVisible(entries.some((entry) => entry.isIntersecting)), {
      rootMargin: "200px",
    });
    observer.observe(target);
    return () => observer.disconnect();
  }, [enabled, hasMore]);
  useEffect(() => {
    if (enabled && visible && hasMore && !loading && !error && !permanent) void loadMore(resource);
  }, [enabled, visible, hasMore, loading, error, permanent, loadMore, resource]);
  if (!hasMore) return null;
  return (
    <div ref={element}>
      <Button variant="quiet" size="sm" disabled={loading || permanent} onClick={() => void loadMore(resource)}>
        {loading ? `Loading ${label}…` : `Load more ${label}`}
      </Button>
    </div>
  );
}
