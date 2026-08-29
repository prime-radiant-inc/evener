import {
  forwardRef,
  type HTMLAttributes,
  type ReactElement,
  type ReactNode,
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

const ForwardedLiveVirtualTranscript = forwardRef(
  function LiveVirtualTranscript(
    {
      threadKey,
      skinId,
      contentSize,
      items,
      skin,
      savedAnchor,
    }: VirtualTranscriptProps,
    forwardedRef: React.ForwardedRef<VirtualTranscriptHandle>,
  ): ReactElement {
    const listRef = useRef<VariableHeightVirtualListHandle>(null);
    const followingRef = useRef(true);
    const initializedRef = useRef(false);
    const measuredAnchorRef = useRef<MeasuredAnchor | null>(null);
    const previousItemsRef = useRef(items);
    const [unseen, setUnseen] = useState(0);
    const [focusedKey, setFocusedKey] = useState<string | null>(null);
    const keys = items.map((item) => item.key);

    const handleScroll = (metrics: {
      offset: number;
      viewport: number;
      total: number;
    }): void => {
      followingRef.current =
        metrics.total - (metrics.offset + metrics.viewport) <=
        FOLLOW_BOUNDARY_PX;
      measuredAnchorRef.current = listRef.current?.captureAnchor() ?? null;
      if (initializedRef.current) return;

      initializedRef.current = true;
      if (
        savedAnchor !== null &&
        savedAnchor.threadKey === threadKey &&
        keys.includes(savedAnchor.itemKey)
      ) {
        followingRef.current = savedAnchor.following;
        void listRef.current?.restoreAnchor({
          key: savedAnchor.itemKey,
          offsetPx: savedAnchor.offsetPx,
          priorIndex: keys.indexOf(savedAnchor.itemKey),
        });
      } else {
        followingRef.current = true;
        listRef.current?.scrollToEnd("auto");
      }
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
      if (!changed) return;

      if (followingRef.current) {
        listRef.current?.scrollToEnd("auto");
        setUnseen(0);
        return;
      }

      const anchor =
        measuredAnchorRef.current ?? listRef.current?.captureAnchor();
      if (anchor !== null && anchor !== undefined) {
        const key = resolveReconciledAnchor(previousKeys, keys, anchor);
        if (key !== null) {
          void listRef.current?.restoreAnchor({
            key,
            offsetPx: anchor.offsetPx,
            priorIndex: Math.max(0, keys.indexOf(key)),
          });
        }
      }
      const added = keys.filter((key) => !previousKeys.includes(key)).length;
      if (added > 0) setUnseen((count) => count + added);
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
        },
        scrollToTail(): void {
          followingRef.current = true;
          setUnseen(0);
          listRef.current?.scrollToEnd("auto");
        },
        focusKey(key): void {
          if (!keys.includes(key)) return;
          setFocusedKey(key);
          listRef.current?.focusKey(key);
        },
      }),
      [keys, threadKey],
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
              setUnseen(0);
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
