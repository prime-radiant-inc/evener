import {
  forwardRef,
  type HTMLAttributes,
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
}

export interface VirtualTranscriptProps {
  readonly threadKey: string;
  readonly skinId: ConceptId;
  readonly contentSize: ContentSizeCategory;
  readonly items: readonly ConversationDisplayItem[];
  readonly skin: ConversationSkin;
  readonly savedAnchor: ConversationAnchor | null;
  /** Frame-owned unseen state; the transcript never keeps a second copy. */
  readonly unseen: number;
  readonly onAnchorChange: (anchor: ConversationAnchor) => void;
  readonly onUnseenChange: (count: number) => void;
  readonly onFocusIntentChange: (key: string | null) => void;
}

interface LegacyVirtualTranscriptProps
  extends Omit<HTMLAttributes<HTMLElement>, "children"> {
  readonly children: ReactNode;
}

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

function isLegacyProps(
  props: VirtualTranscriptProps | LegacyVirtualTranscriptProps,
): props is LegacyVirtualTranscriptProps {
  return "children" in props;
}

const ForwardedLiveVirtualTranscriptSession = forwardRef(
  function LiveVirtualTranscriptSession(
    {
      threadKey,
      skinId,
      contentSize,
      items,
      skin,
      savedAnchor,
      unseen,
      onAnchorChange,
      onUnseenChange,
      onFocusIntentChange,
    }: VirtualTranscriptProps,
    forwardedRef: React.ForwardedRef<VirtualTranscriptHandle>,
  ): ReactElement {
    const listRef = useRef<VariableHeightVirtualListHandle>(null);
    const followingRef = useRef(savedAnchor?.following ?? true);
    const initializedRef = useRef(false);
    const measuredAnchorRef = useRef<MeasuredAnchor | null>(null);
    const previousItemsRef = useRef(items);
    const activeRef = useRef(true);
    const onFocusIntentChangeRef = useRef(onFocusIntentChange);
    onFocusIntentChangeRef.current = onFocusIntentChange;
    const [focusedKey, setFocusedKey] = useState<string | null>(null);
    const keys = items.map((item) => item.key);

    useEffect(
      () => () => {
        activeRef.current = false;
        onFocusIntentChangeRef.current(null);
      },
      [],
    );

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
      }),
      [keys, onFocusIntentChange, onUnseenChange, publishAnchor, threadKey],
    );

    return (
      <section
        aria-label="Transcript"
        className="live-conversation-frame__virtual-transcript"
      >
        <VariableHeightVirtualList
          ref={listRef}
          items={items}
          getItemKey={(item) => item.key}
          estimateSize={estimateDisplayRow}
          cacheScope={{ threadKey, skinId, contentSize }}
          overscan={6}
          maxMountedRows={48}
          onScroll={handleScroll}
          renderItem={(item) =>
            renderDisplayItem(item, skin, item.key === focusedKey)
          }
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

const ForwardedVirtualTranscript = forwardRef(
  function VirtualTranscriptBoundary(
    props: VirtualTranscriptProps | LegacyVirtualTranscriptProps,
    ref: React.ForwardedRef<VirtualTranscriptHandle>,
  ): ReactElement {
    if (isLegacyProps(props)) {
      const { children, ...attributes } = props;
      return (
        <section
          {...attributes}
          aria-label="Transcript"
          data-page-scroll-owner="true"
        >
          {children}
        </section>
      );
    }
    return <ForwardedLiveVirtualTranscript {...props} ref={ref} />;
  },
);

export const VirtualTranscript = ForwardedVirtualTranscript as {
  (
    props: VirtualTranscriptProps & {
      readonly ref?: React.Ref<VirtualTranscriptHandle>;
    },
  ): ReactElement;
  (props: LegacyVirtualTranscriptProps): ReactElement;
};
