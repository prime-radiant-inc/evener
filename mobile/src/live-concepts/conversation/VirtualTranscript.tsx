import {
  forwardRef,
  type ReactElement,
  type ReactNode,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import {
  VariableHeightVirtualList,
  type VariableHeightVirtualListHandle,
} from "../../components/timeline/VariableHeightVirtualList";
import {
  type MeasuredAnchor,
  resolveReconciledAnchor,
} from "../../conversation/paging";
import type { ContentSizeCategory } from "../../native/contract";
import type { ConceptId, ConversationDisplayItem } from "../model";
import type { ConversationAnchor, ConversationSkin } from "./contract";

const FOLLOW_BOUNDARY_PX = 48;

export interface VirtualTranscriptHandle {
  captureAnchor(): ConversationAnchor | null;
  restoreAnchor(anchor: ConversationAnchor): Promise<void>;
  scrollToTail(): void;
  focusKey(key: string): void;
  focusFeed(): void;
}

interface VirtualTranscriptBaseProps {
  readonly threadKey: string;
  readonly skinId: ConceptId;
  readonly contentSize: ContentSizeCategory;
  readonly items: readonly ConversationDisplayItem[];
  readonly savedAnchor: ConversationAnchor | null;
  /** Frame-owned unseen state; the transcript never keeps a second copy. */
  readonly unseen: number;
  readonly onAnchorChange: (anchor: ConversationAnchor) => void;
  readonly onUnseenChange: (count: number) => void;
  readonly onFocusIntentChange: (key: string | null) => void;
  readonly locked?: boolean;
}

type VirtualTranscriptRenderer =
  | {
      /** Task 3/default rendering retained for standalone measured tests/users. */
      readonly renderMode?: "skin";
      readonly skin: ConversationSkin;
      readonly renderItem?: never;
    }
  | {
      /** Frame supplies the sole semantic article and every owned control. */
      readonly renderMode: "frame-owned";
      readonly skin?: never;
      readonly renderItem: (
        item: ConversationDisplayItem,
        index: number,
        focused: boolean,
      ) => ReactNode;
    };

export type VirtualTranscriptProps = VirtualTranscriptBaseProps &
  VirtualTranscriptRenderer;

export function estimateDisplayRow(item: ConversationDisplayItem): number {
  if ("semanticKind" in item) return 56;
  if (item.sourceKind === "user") return 96;
  if (item.sourceKind === "assistant") return 128;
  return 160;
}

function renderDisplayItem(
  item: ConversationDisplayItem,
  skin: ConversationSkin,
  focused: boolean,
): ReactNode {
  if ("semanticKind" in item) {
    return skin.renderActivityMarker({ item, focused });
  }
  return skin.renderNarrativeItem({
    item,
    body: <p>{item.body.text}</p>,
    focused,
  });
}

const ForwardedLiveVirtualTranscriptSession = forwardRef(
  function LiveVirtualTranscriptSession(
    props: VirtualTranscriptProps,
    forwardedRef: React.ForwardedRef<VirtualTranscriptHandle>,
  ): ReactElement {
    const {
      threadKey,
      skinId,
      contentSize,
      items,
      savedAnchor,
      unseen,
      onAnchorChange,
      onUnseenChange,
      onFocusIntentChange,
      locked = false,
    } = props;
    const listRef = useRef<VariableHeightVirtualListHandle>(null);
    const transcriptRef = useRef<HTMLElement>(null);
    const followingRef = useRef(savedAnchor?.following ?? true);
    const initializedRef = useRef(false);
    const measuredAnchorRef = useRef<MeasuredAnchor | null>(null);
    const previousItemsRef = useRef(items);
    const activeRef = useRef(true);
    const lifecycleEpochRef = useRef(0);
    const onFocusIntentChangeRef = useRef(onFocusIntentChange);
    onFocusIntentChangeRef.current = onFocusIntentChange;
    const [focusedKey, setFocusedKey] = useState<string | null>(null);
    const keys = items.map((item) => item.key);
    const initialItem = items.at(-1);

    useEffect(() => {
      const epoch = lifecycleEpochRef.current + 1;
      lifecycleEpochRef.current = epoch;
      activeRef.current = true;
      return () => {
        activeRef.current = false;
        void Promise.resolve().then(() => {
          if (lifecycleEpochRef.current === epoch && !activeRef.current) {
            onFocusIntentChangeRef.current(null);
          }
        });
      };
    }, []);

    const publishAnchor = useCallback(
      (following: boolean): void => {
        if (!activeRef.current) return;
        const anchor = listRef.current?.captureAnchor() ?? null;
        measuredAnchorRef.current = anchor;
        if (anchor === null) return;
        onAnchorChange({
          threadKey,
          itemKey: anchor.key,
          offsetPx: anchor.offsetPx,
          following,
        });
      },
      [onAnchorChange, threadKey],
    );

    const handleScroll = (metrics: {
      offset: number;
      viewport: number;
      total: number;
      measured: boolean;
    }): void => {
      const nextFollowing =
        metrics.total - (metrics.offset + metrics.viewport) <=
        FOLLOW_BOUNDARY_PX;
      if (!initializedRef.current) {
        const handle = listRef.current;
        if (!metrics.measured || handle === null) return;
        initializedRef.current = true;
        if (
          savedAnchor !== null &&
          savedAnchor.threadKey === threadKey &&
          keys.includes(savedAnchor.itemKey)
        ) {
          followingRef.current = savedAnchor.following;
          void handle
            .restoreAnchor({
              key: savedAnchor.itemKey,
              offsetPx: savedAnchor.offsetPx,
              priorIndex: keys.indexOf(savedAnchor.itemKey),
            })
            .then(() => publishAnchor(savedAnchor.following));
        } else {
          followingRef.current = true;
          handle.scrollToEnd("auto");
        }
        return;
      }

      if (nextFollowing && !followingRef.current) onUnseenChange(0);
      followingRef.current = nextFollowing;
      publishAnchor(nextFollowing);
    };

    useLayoutEffect(() => {
      const previousItems = previousItemsRef.current;
      if (previousItems === items) return;
      const previousKeys = previousItems.map((item) => item.key);
      const changed =
        previousKeys.length !== keys.length ||
        previousKeys.some((key, index) => key !== keys[index]) ||
        previousItems.some((item, index) => item !== items[index]);
      previousItemsRef.current = items;
      if (!changed || !initializedRef.current) return;

      if (followingRef.current) {
        listRef.current?.scrollToEnd("auto");
        return;
      }

      const anchor =
        measuredAnchorRef.current ?? listRef.current?.captureAnchor();
      if (anchor === null || anchor === undefined) return;
      const key = resolveReconciledAnchor(previousKeys, keys, anchor);
      if (key === null) return;
      void listRef.current?.restoreAnchor({
        key,
        offsetPx: anchor.offsetPx,
        priorIndex: Math.max(0, keys.indexOf(key)),
      });
    }, [items, keys]);

    useImperativeHandle(
      forwardedRef,
      () => ({
        captureAnchor(): ConversationAnchor | null {
          if (!initializedRef.current) return null;
          const anchor = listRef.current?.captureAnchor();
          return anchor === null || anchor === undefined
            ? null
            : {
                threadKey,
                itemKey: anchor.key,
                offsetPx: anchor.offsetPx,
                following: followingRef.current,
              };
        },
        async restoreAnchor(anchor): Promise<void> {
          if (anchor.threadKey !== threadKey) return;
          const priorIndex = keys.indexOf(anchor.itemKey);
          if (priorIndex < 0) return;
          followingRef.current = anchor.following;
          await listRef.current?.restoreAnchor({
            key: anchor.itemKey,
            offsetPx: anchor.offsetPx,
            priorIndex,
          });
          publishAnchor(anchor.following);
        },
        scrollToTail(): void {
          followingRef.current = true;
          onUnseenChange(0);
          listRef.current?.scrollToEnd("auto");
        },
        focusKey(key): void {
          if (!keys.includes(key)) return;
          setFocusedKey(key);
          onFocusIntentChange(key);
          listRef.current?.focusKey(key);
        },
        focusFeed(): void {
          transcriptRef.current
            ?.querySelector<HTMLElement>('[data-virtual-list-scroll="true"]')
            ?.focus({ preventScroll: true });
        },
      }),
      [keys, onFocusIntentChange, onUnseenChange, publishAnchor, threadKey],
    );

    return (
      <section
        ref={transcriptRef}
        aria-label="Transcript"
        className="live-conversation-frame__virtual-transcript"
      >
        <VariableHeightVirtualList
          ref={listRef}
          items={items}
          getItemKey={(item) => item.key}
          estimateSize={estimateDisplayRow}
          cacheScope={{ threadKey, skinId, contentSize }}
          rowSemantics={
            props.renderMode === "frame-owned"
              ? { mode: "caller-owned" }
              : undefined
          }
          scrollSemantics={
            props.renderMode === "frame-owned"
              ? {
                  mode: "custom",
                  role: "feed",
                  ariaLabel: "Conversation transcript",
                  pageScrollOwner: !locked,
                  locked,
                }
              : undefined
          }
          overscan={6}
          maxMountedRows={48}
          initialViewportEstimate={
            props.renderMode === "frame-owned" && initialItem !== undefined
              ? estimateDisplayRow(initialItem)
              : undefined
          }
          onScroll={handleScroll}
          renderItem={(item, index) => {
            const focused = item.key === focusedKey;
            return props.renderMode === "frame-owned"
              ? props.renderItem(item, index, focused)
              : renderDisplayItem(item, props.skin, focused);
          }}
        />
        {unseen > 0 ? (
          <button
            type="button"
            aria-label={`${unseen} new activity`}
            onClick={() => {
              followingRef.current = true;
              onUnseenChange(0);
              listRef.current?.scrollToEnd("smooth");
            }}
          >
            New activity
          </button>
        ) : null}
      </section>
    );
  },
);

const ForwardedLiveVirtualTranscript = forwardRef(
  function LiveVirtualTranscript(
    props: VirtualTranscriptProps,
    ref: React.ForwardedRef<VirtualTranscriptHandle>,
  ): ReactElement {
    return (
      <ForwardedLiveVirtualTranscriptSession
        key={props.threadKey}
        {...props}
        ref={ref}
      />
    );
  },
);

export const VirtualTranscript = ForwardedLiveVirtualTranscript;
