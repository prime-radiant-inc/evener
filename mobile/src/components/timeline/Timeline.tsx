import { type JSX, useLayoutEffect, useRef } from "react";
import type { MobileTimelineItem } from "../../conversation/model";
import {
  type MeasuredAnchor,
  resolveReconciledAnchor,
} from "../../conversation/paging";
import type { ConceptId } from "../../live-concepts/model";
import type { ContentSizeCategory } from "../../native/contract";
import { NewActivityButton } from "./NewActivityButton";
import { TimelineItem } from "./TimelineItem";
import {
  VariableHeightVirtualList,
  type VariableHeightVirtualListHandle,
} from "./VariableHeightVirtualList";
import "./Timeline.css";

export interface TimelineProps {
  /** The ordered conversation items (oldest first, newest last). */
  readonly items: MobileTimelineItem[];
  /** Whether follow mode is active (auto-scroll to bottom). */
  readonly following: boolean;
  /** Number of unseen new items (drives the new-activity pill). */
  readonly unseen: number;
  /** Invoked when the user taps the new-activity pill. */
  readonly onTapNewActivity?: () => void;
  /** Invoked when the user scrolls near the top to load older items. */
  readonly loadOlder?: () => void;
  /** Invoked when the user taps an external link in an assistant message. */
  readonly onExternalLink?: (url: string) => void;
  /** Height of each timeline item estimate for virtualization (default 64). */
  readonly estimateItemHeight?: number;
  /** Scroll threshold from the top (in px) that triggers loadOlder (default 120). */
  readonly loadOlderThreshold?: number;
  /** Stable measurement scope supplied by coordinators that own thread identity. */
  readonly cacheScope?: {
    readonly threadKey: string;
    readonly skinId: ConceptId;
    readonly contentSize: ContentSizeCategory;
  };
}

interface PendingLoad {
  readonly generation: number;
  readonly anchor: MeasuredAnchor;
  readonly previousKeys: readonly string[];
}

const TIMELINE_CACHE_SCOPE = {
  threadKey: "production-timeline",
  skinId: "stillwater",
  contentSize: "large",
} as const;

/** Production timeline adapter over the shared measured virtualization boundary. */
export function Timeline({
  items,
  following,
  unseen,
  onTapNewActivity,
  loadOlder,
  onExternalLink,
  estimateItemHeight = 64,
  loadOlderThreshold = 120,
  cacheScope = TIMELINE_CACHE_SCOPE,
}: TimelineProps): JSX.Element {
  const listRef = useRef<VariableHeightVirtualListHandle>(null);
  const loadGenerationRef = useRef(0);
  const pendingLoadRef = useRef<PendingLoad | null>(null);
  const itemKeys = items.map((item) => item.id);
  const latestItem = items.at(-1);

  useLayoutEffect(() => {
    const pending = pendingLoadRef.current;
    if (pending === null) return;
    const changed =
      pending.previousKeys.length !== itemKeys.length ||
      pending.previousKeys.some((key, index) => key !== itemKeys[index]);
    if (!changed) return;

    pendingLoadRef.current = null;
    const key = resolveReconciledAnchor(
      pending.previousKeys,
      itemKeys,
      pending.anchor,
    );
    if (key === null) return;
    void listRef.current?.restoreAnchor({
      key,
      offsetPx: pending.anchor.offsetPx,
      priorIndex: Math.max(0, itemKeys.indexOf(key)),
    });
  }, [itemKeys]);

  useLayoutEffect(() => {
    if (following && latestItem !== undefined) {
      listRef.current?.scrollToEnd("auto");
    }
  }, [following, latestItem]);

  const handleScroll = (metrics: {
    offset: number;
    viewport: number;
    total: number;
  }): void => {
    if (
      loadOlder === undefined ||
      metrics.offset > loadOlderThreshold ||
      pendingLoadRef.current !== null
    ) {
      return;
    }
    const anchor = listRef.current?.captureAnchor();
    if (anchor === null || anchor === undefined) return;
    loadGenerationRef.current += 1;
    pendingLoadRef.current = {
      generation: loadGenerationRef.current,
      anchor,
      previousKeys: itemKeys,
    };
    loadOlder();
  };

  return (
    <div className="evener-timeline">
      <VariableHeightVirtualList
        ref={listRef}
        items={items}
        getItemKey={(item) => item.id}
        estimateSize={() => estimateItemHeight}
        cacheScope={cacheScope}
        overscan={6}
        maxMountedRows={48}
        onScroll={handleScroll}
        renderItem={(item) => (
          <div style={{ padding: "var(--space-4) 0" }}>
            <TimelineItem item={item} onExternalLink={onExternalLink} />
          </div>
        )}
      />
      {unseen > 0 && !following ? (
        <NewActivityButton
          unseen={unseen}
          onTap={onTapNewActivity ?? (() => {})}
        />
      ) : null}
    </div>
  );
}
