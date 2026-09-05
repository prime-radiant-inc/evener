// useTranscriptScrollKeys: the session transcript's keyboard scroll seam
// (webui-keybindings-p3 Task 1). Every mounted Session pane registers its own
// handlers for the transcript.* actions against the shared keybindings
// registry, using the registry's multi-instance support (registry.ts's
// ActionRunner contract): a handler returns false - DECLINES - when its pane
// is not workspaceStore.focusedPaneId, so only the focused pane's transcript
// scrolls. This is the SelectionQuote clobber class from Phase 2a, pinned by
// this hook's own two-pane test.
//
// The scroll mechanisms are the ones the transcript's own flow stack already
// exposes - no new scroll math:
//   - line/page steps write the scroll element's scrollTop directly, the
//     same adjustment useTranscriptScroll's own anchor-restore paths use
//     (el.scrollTop += delta). The native scroll listener that hook attaches
//     keeps wasAtBottom/the new-content pill in step afterward.
//   - scrollTop uses the virtualizer's scrollToIndex(0, { align: "start" }).
//   - scrollBottom is the hook's own jumpToBottom (error-anchor aware, pill
//     clearing) - the exact action NewContentPill's click target runs.
//
// Mobile: nothing registers at all (rail.toggle's no-registration pattern);
// with no handler the bindings are inert there.

import { type RefObject, useEffect, useRef } from "react";
import { ACTIONS } from "../../../../keybindings/actions";
import { keybindingsRegistry } from "../../../../keybindings/registry";
import { installKeybindings } from "../../../../shell/installKeybindings";
import { useIsMobile } from "../../../../shell/useIsMobile";
import { workspaceStore } from "../../../../shell/workspace";
import type { VirtualListHandle } from "../../../../widgets/virtuallist";

/** One line-scroll step in pixels (≈ one transcript row's text line). */
export const TRANSCRIPT_LINE_SCROLL_PX = 40;

/** One page-scroll step as a fraction of the scroll viewport's height. */
export const TRANSCRIPT_PAGE_SCROLL_RATIO = 0.9;

export interface UseTranscriptScrollKeysOptions {
  /** The workspace pane this Session instance is mounted as. */
  paneId: string;
  listRef: RefObject<VirtualListHandle | null>;
  /** useTranscriptScroll's jumpToBottom (error-anchor aware, pill clearing). */
  jumpToBottom: () => void;
}

export function useTranscriptScrollKeys({ paneId, listRef, jumpToBottom }: UseTranscriptScrollKeysOptions): void {
  const isMobile = useIsMobile();
  // jumpToBottom's identity tracks useTranscriptScroll's renders; the ref
  // keeps the registered handler stable across them (SelectionQuote's
  // actionsRef idiom).
  const jumpToBottomRef = useRef(jumpToBottom);
  jumpToBottomRef.current = jumpToBottom;

  useEffect(() => {
    if (isMobile) return undefined;
    installKeybindings();
    const registry = keybindingsRegistry.getState();
    const focused = () => workspaceStore.getState().focusedPaneId === paneId;
    const scrollElement = () => listRef.current?.getScrollElement() ?? null;
    const unregister = [
      registry.registerAction(ACTIONS.transcriptLineUp, () => {
        if (!focused()) return false;
        const el = scrollElement();
        if (!el) return false;
        el.scrollTop -= TRANSCRIPT_LINE_SCROLL_PX;
        return true;
      }),
      registry.registerAction(ACTIONS.transcriptLineDown, () => {
        if (!focused()) return false;
        const el = scrollElement();
        if (!el) return false;
        el.scrollTop += TRANSCRIPT_LINE_SCROLL_PX;
        return true;
      }),
      registry.registerAction(ACTIONS.transcriptPageUp, () => {
        if (!focused()) return false;
        const el = scrollElement();
        if (!el) return false;
        el.scrollTop -= el.clientHeight * TRANSCRIPT_PAGE_SCROLL_RATIO;
        return true;
      }),
      registry.registerAction(ACTIONS.transcriptPageDown, () => {
        if (!focused()) return false;
        const el = scrollElement();
        if (!el) return false;
        el.scrollTop += el.clientHeight * TRANSCRIPT_PAGE_SCROLL_RATIO;
        return true;
      }),
      registry.registerAction(ACTIONS.transcriptScrollTop, () => {
        if (!focused()) return false;
        const list = listRef.current;
        if (!list) return false;
        list.scrollToIndex(0, { align: "start" });
        return true;
      }),
      registry.registerAction(ACTIONS.transcriptScrollBottom, () => {
        if (!focused()) return false;
        jumpToBottomRef.current();
        return true;
      }),
    ];
    return () => {
      for (const dispose of unregister) dispose();
    };
  }, [isMobile, paneId, listRef]);
}
