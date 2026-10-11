import { createContext, type ReactNode, useCallback, useContext, useLayoutEffect, useMemo, useRef } from "react";
import type { ActivityTab } from "../statusbar/statusScope";
import { type ActivityScrollAnchor, activitySidebarStore } from "./activitySidebarStore";

interface ActivityViewportState {
  anchorID(): string | undefined;
  isCurrent(): boolean;
  contentChanged(ids: readonly string[], complete: boolean, hasMore: boolean): void;
}
const ViewportContext = createContext<ActivityViewportState | null>(null);
const alwaysCurrent = () => true;
const SCROLL_KEYS = new Set([
  "ArrowUp",
  "ArrowDown",
  "ArrowLeft",
  "ArrowRight",
  "Home",
  "End",
  "PageUp",
  "PageDown",
  " ",
]);

/** Tabs report the read they already own; this acquires no data or demand. */
export function useActivityScrollProgress(ids: readonly string[], complete: boolean, hasMore = false): void {
  const view = useContext(ViewportContext);
  useLayoutEffect(() => view?.contentChanged(ids, complete, hasMore), [view, ids, complete, hasMore]);
}

export function useActivityScrollAnchor(): string | undefined {
  return useContext(ViewportContext)?.anchorID();
}

/** Exiting animation children can still receive old observer callbacks. */
export function useActivityViewCurrent(): () => boolean {
  return useContext(ViewportContext)?.isCurrent ?? alwaysCurrent;
}

export function ActivityViewport({
  sessionRef,
  tab,
  className,
  children,
}: {
  sessionRef: string;
  tab: ActivityTab;
  className: string;
  children: ReactNode;
}) {
  const element = useRef<HTMLDivElement>(null);
  const pending = useRef(activitySidebarStore.getState().views.get(sessionRef)?.categories[tab]?.anchor);
  const programmaticTop = useRef<number | null>(null);
  const mutations = useRef<MutationObserver | null>(null);
  const progress = useRef<readonly [readonly string[], boolean, boolean] | null>(null);
  const isCurrent = useCallback(() => {
    const state = activitySidebarStore.getState();
    return state.open && state.ref === sessionRef && state.tab === tab;
  }, [sessionRef, tab]);

  const capture = useCallback(() => {
    const body = element.current;
    if (!body || body.clientHeight <= 0 || !isCurrent()) return;
    const top = body.getBoundingClientRect().top;
    let anchor: ActivityScrollAnchor | undefined;
    for (const row of body.querySelectorAll<HTMLElement>("[data-activity-anchor]")) {
      const rect = row.getBoundingClientRect();
      if (rect.bottom <= top) continue;
      const id = row.dataset.activityAnchor;
      if (id) anchor = { id, offset: rect.top - top };
      break;
    }
    activitySidebarStore.getState().setCategoryView(sessionRef, tab, { anchor });
  }, [isCurrent, sessionRef, tab]);

  const contentChanged = useCallback(
    (ids: readonly string[], complete: boolean, hasMore: boolean) => {
      progress.current = [ids, complete, hasMore];
      const body = element.current;
      const anchor = pending.current;
      if (!body || !anchor || body.clientHeight <= 0 || !isCurrent()) return;
      let row: HTMLElement | undefined;
      for (const candidate of body.querySelectorAll<HTMLElement>("[data-activity-anchor]")) {
        if (candidate.dataset.activityAnchor !== anchor.id) continue;
        row = candidate;
        break;
      }
      let nextTop: number;
      if (row) {
        nextTop = body.scrollTop + row.getBoundingClientRect().top - body.getBoundingClientRect().top - anchor.offset;
      } else if (ids.includes(anchor.id)) {
        // A retained fold can hydrate after its collection has arrived.
        // Wait for its DOM; never infer removal or open the fold ourselves.
        return;
      } else if (complete) {
        pending.current = undefined;
        mutations.current?.disconnect();
        activitySidebarStore.getState().setCategoryView(sessionRef, tab, { anchor: undefined });
        return;
      } else if (hasMore) {
        // Put the existing page boundary in view. Its fresh visibility signal
        // admits each page; this helper never loads or retries a collection.
        nextTop = body.scrollHeight - body.clientHeight;
      } else return;
      if (body.scrollTop !== nextTop) {
        body.scrollTop = nextTop;
        programmaticTop.current = body.scrollTop;
      }
      // A short page can contain the row but clamp its requested offset.
      // Keep its intent until trailing extent arrives through existing demand.
      if (row && (Math.abs(body.scrollTop - nextTop) < 1 || complete)) {
        pending.current = undefined;
        mutations.current?.disconnect();
      }
    },
    [isCurrent, sessionRef, tab],
  );

  useLayoutEffect(() => {
    const body = element.current;
    if (!body || !pending.current) return;
    const observer = new MutationObserver(() => {
      if (progress.current) contentChanged(...progress.current);
    });
    mutations.current = observer;
    observer.observe(body, { childList: true, subtree: true });
    return () => observer.disconnect();
  }, [contentChanged]);

  const cancel = () => {
    if (!isCurrent()) return;
    pending.current = undefined;
    mutations.current?.disconnect();
    programmaticTop.current = null;
    capture();
  };
  const view = useMemo(
    () => ({ anchorID: () => pending.current?.id, isCurrent, contentChanged }),
    [isCurrent, contentChanged],
  );
  return (
    <ViewportContext.Provider value={view}>
      <div
        ref={element}
        className={className}
        onWheelCapture={cancel}
        onTouchStartCapture={cancel}
        onPointerDownCapture={cancel}
        onKeyDownCapture={(event) => {
          if (SCROLL_KEYS.has(event.key)) cancel();
        }}
        onClickCapture={(event) => {
          // Keyboard and assistive activation have no preceding pointer-down.
          if (event.detail === 0) cancel();
        }}
        onScroll={() => {
          if (!isCurrent()) return;
          if (programmaticTop.current === element.current?.scrollTop) {
            programmaticTop.current = null;
            return;
          }
          cancel();
        }}
      >
        {children}
      </div>
    </ViewportContext.Provider>
  );
}
