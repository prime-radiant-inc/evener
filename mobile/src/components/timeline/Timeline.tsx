import { type JSX, useLayoutEffect, useRef, useState } from "react";
import type { MobileTimelineItem } from "../../conversation/model";
import {
  type MeasuredAnchor,
  resolveReconciledAnchor,
} from "../../conversation/paging";
import type { ContentSizeCategory } from "../../native/contract";
import type { LoadOlderResult } from "../../state/conversation";
import { NewActivityButton } from "./NewActivityButton";
import { TimelineItem } from "./TimelineItem";
import {
  VariableHeightVirtualList,
  type VariableHeightVirtualListHandle,
} from "./VariableHeightVirtualList";
import "./Timeline.css";

const FOLLOW_BOUNDARY_PX = 48;

export interface TimelineProps {
  /** Stable identity of the conversation owning this timeline. */
  readonly threadKey: string;
  /** Exact native Dynamic Type category for this measurement scope. */
  readonly contentSize: ContentSizeCategory;
  /** The ordered conversation items (oldest first, newest last). */
  readonly items: MobileTimelineItem[];
  /** Whether follow mode is active (auto-scroll to bottom). */
  readonly following: boolean;
  /** Reports bidirectional 48px follow-boundary transitions to the owner. */
  readonly onFollowingChange?: (following: boolean) => void;
  /** Number of unseen new items (drives the new-activity pill). */
  readonly unseen: number;
  /** Invoked when the user taps the new-activity pill. */
  readonly onTapNewActivity?: () => void;
  /** Resolves with the exact completed page-request outcome. */
  readonly loadOlder?: () => Promise<LoadOlderResult>;
  /** Invoked when the user taps an external link in an assistant message. */
  readonly onExternalLink?: (url: string) => void;
  /** Height of each timeline item estimate for virtualization (default 64). */
  readonly estimateItemHeight?: number;
  /** Scroll threshold from the top (in px) that triggers loadOlder (default 120). */
  readonly loadOlderThreshold?: number;
}

interface PendingLoad {
  readonly generation: number;
  readonly anchor: MeasuredAnchor;
  readonly previousKeys: readonly string[];
}

/** Production timeline adapter over the shared measured virtualization boundary. */
export function Timeline({
  threadKey,
  contentSize,
  items,
  following,
  onFollowingChange,
  unseen,
  onTapNewActivity,
  loadOlder,
  onExternalLink,
  estimateItemHeight = 64,
  loadOlderThreshold = 120,
}: TimelineProps): JSX.Element {
  const listRef = useRef<VariableHeightVirtualListHandle>(null);
  const loadGenerationRef = useRef(0);
  const pendingLoadRef = useRef<PendingLoad | null>(null);
  const followingRef = useRef(following);
  const previousThreadRef = useRef(threadKey);
  const itemKeys = items.map((item) => item.id);
  const currentKeysRef = useRef(itemKeys);
  currentKeysRef.current = itemKeys;
  const latestItem = items.at(-1);
  const [completedLoad, setCompletedLoad] = useState<{
    readonly generation: number;
    readonly itemKeys: readonly string[];
  } | null>(null);

  useLayoutEffect(() => {
    followingRef.current = following;
  }, [following]);

  useLayoutEffect(() => {
    if (previousThreadRef.current === threadKey) return;
    previousThreadRef.current = threadKey;
    loadGenerationRef.current += 1;
    pendingLoadRef.current = null;
    setCompletedLoad(null);
  }, [threadKey]);

  useLayoutEffect(() => {
    const pending = pendingLoadRef.current;
    if (pending === null || completedLoad?.generation !== pending.generation) {
      return;
    }
    const completedKeys = completedLoad.itemKeys;
    if (completedKeys.length === 0) {
      pendingLoadRef.current = null;
      setCompletedLoad(null);
      return;
    }
    if (!completedKeys.every((key) => itemKeys.includes(key))) return;

    pendingLoadRef.current = null;
    setCompletedLoad(null);
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
  }, [completedLoad, itemKeys]);

  useLayoutEffect(() => {
    if (following && latestItem !== undefined) {
      listRef.current?.scrollToEnd("auto");
    }
  }, [following, latestItem]);

  const finishLoad = (generation: number, result: LoadOlderResult): void => {
    const pending = pendingLoadRef.current;
    if (pending?.generation !== generation) return;
    if (result.status !== "loaded") {
      pendingLoadRef.current = null;
      setCompletedLoad(null);
    } else {
      setCompletedLoad({ generation, itemKeys: result.itemKeys });
    }
  };

  const handleScroll = (metrics: {
    offset: number;
    viewport: number;
    total: number;
  }): void => {
    const nextFollowing =
      metrics.total - (metrics.offset + metrics.viewport) <= FOLLOW_BOUNDARY_PX;
    if (nextFollowing !== followingRef.current) {
      followingRef.current = nextFollowing;
      onFollowingChange?.(nextFollowing);
    }

    if (
      loadOlder === undefined ||
      metrics.offset > loadOlderThreshold ||
      pendingLoadRef.current !== null
    ) {
      return;
    }
    const anchor = listRef.current?.captureAnchor();
    if (anchor === null || anchor === undefined) return;
    const generation = loadGenerationRef.current + 1;
    loadGenerationRef.current = generation;
    pendingLoadRef.current = {
      generation,
      anchor,
      previousKeys: currentKeysRef.current,
    };
    try {
      void loadOlder().then(
        (result) => finishLoad(generation, result),
        () => finishLoad(generation, { status: "failed" }),
      );
    } catch {
      finishLoad(generation, { status: "failed" });
    }
  };

  return (
    <div className="evener-timeline">
      <VariableHeightVirtualList
        ref={listRef}
        items={items}
        getItemKey={(item) => item.id}
        estimateSize={() => estimateItemHeight}
        cacheScope={{ threadKey, skinId: "stillwater", contentSize }}
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
