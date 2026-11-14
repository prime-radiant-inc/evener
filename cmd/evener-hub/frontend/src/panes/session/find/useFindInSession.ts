// Find in the session: the words being looked for, the entries that hold
// them, the one the reader is on, and the search into older history.
//
// Find searches what is loaded and never answers "no matches" for the session
// until history is known to end. Stepping older past the oldest loaded match
// asks the shared paging owner for older pages, as Find's own consumer, until
// a match appears or the start of history is confirmed; a failed or
// disconnected read leaves the search waiting, and it resumes on its own.
// Closing Find, or changing the words, withdraws only that demand.
//
// The transcript's scroll owner moves the reader (jumpToRow); Find only names
// the row and then settles the entry into view inside it.
import { ACTIONS, findNeedle } from "@evener/appwire-client";
import { type RefObject, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { keybindingsRegistry } from "../../../keybindings/appRegistry";
import { installKeybindings } from "../../../shell/installKeybindings";
import type { VirtualListHandle } from "../../../widgets";
import { requestComposerFocus } from "../composer/composerFocus";
import type { TranscriptBodyRow } from "../transcript/TranscriptBody";
import { type FindHit, findHits } from "./findHits";
import { markTranscript, scrollTopRevealing } from "./findMarks";
import { consumeFindRequest, useFindRequest } from "./findRequest";

/** How the search stands, for the bar to say:
 *  - idle: no words yet.
 *  - matches: the reader is among `count` loaded matches.
 *  - unsearched: nothing loaded matches, and older history has not been read.
 *  - searching: older history is being read (including paced retries).
 *  - incomplete: older history cannot be read; `error` explains.
 *  - none: history is fully loaded and holds no match. */
export type FindStatus = "idle" | "matches" | "unsearched" | "searching" | "incomplete" | "none";

export interface FindView {
  query: string;
  status: FindStatus;
  /** 1-based from the newest match, 0 when the reader is on none. */
  position: number;
  count: number;
  /** Older history exists that this search has not read. */
  moreHistory: boolean;
  error: string | null;
  canStepOlder: boolean;
  canStepNewer: boolean;
  /** Bumps whenever the bar should take focus and select its words. */
  focusEpoch: number;
  setQuery(query: string): void;
  step(direction: 1 | -1): void;
  close(): void;
}

interface FindInSessionOptions {
  ref: string;
  /** The seat's pane id: the request address and the composer focus target. */
  paneId: string;
  rows: readonly TranscriptBodyRow[];
  listRef: RefObject<VirtualListHandle | null>;
  jumpToRow(index: number): void;
  holdPosition(): void;
  markGesture(readerIntent?: boolean, beforeOffset?: number): void;
  /** null is the confirmed start of history; undefined is "not known yet". */
  olderPageKey: string | null | undefined;
  /** A page is being read, or a retry is waiting its turn. */
  olderLoading: boolean;
  olderError: string | null;
  olderPermanent: boolean;
  findOlder(): void;
  cancelFindOlder(): void;
  /** Whether this conversation is the one the keyboard is in. */
  ownsKeyboard(): boolean;
  /** Whether the loaded window holds this source item, shown or not. */
  itemLoaded(itemId: string): boolean;
}

interface FindState {
  ref: string;
  query: string;
  /** The current match: its entry's display key, and the source item that
   * says which message it is. Both null with no current match. */
  key: string | null;
  item: string | null;
  /** What older history is being read for, if anything: the next match
   * older than the current one, or the current match itself after a refresh
   * or resync dropped the page that held it. */
  seek: null | "older" | "restore";
}

// How long after a step Find may still settle the entry into view. A fold
// opening and the row's first measurement land within a few frames; past
// this, a remount of the same entry is the reader scrolling, not the step.
const SETTLE_WINDOW_MS = 1500;
// How long a row jump gets to render its row before Find asks again.
const JUMP_RETRY_MS = 150;

const NO_MATCH = { key: null, item: null } as const;

function matchOf(hit: FindHit | undefined): Pick<FindState, "key" | "item"> {
  return hit === undefined ? NO_MATCH : { key: hit.key, item: hit.itemId };
}

// A reply still streaming is an overlay item, keyed "stream:<id>:<type>"
// (the wire's overlay slot key), until its round is recorded.
function isStreamOverlay(itemId: string): boolean {
  return itemId.startsWith("stream:");
}

export function useFindInSession({
  ref,
  paneId,
  rows,
  listRef,
  jumpToRow,
  holdPosition,
  markGesture,
  olderPageKey,
  olderLoading,
  olderError,
  olderPermanent,
  findOlder,
  cancelFindOlder,
  ownsKeyboard,
  itemLoaded,
}: FindInSessionOptions): {
  view: FindView | null;
  open(): void;
  /** The reader left history for the live end: a search into older history
   * stops, so a late match cannot pull them back. Find stays open. */
  leaveHistory(): void;
} {
  const [state, setState] = useState<FindState | null>(null);
  const find = state?.ref === ref ? state : null;
  const isOpen = find !== null;
  const [focusEpoch, setFocusEpoch] = useState(0);
  // Bumps when a caller names a match to land on, so landing on the match
  // that is already current still jumps.
  const [jumpEpoch, setJumpEpoch] = useState(0);
  const returnFocus = useRef<Element | null>(null);

  const query = find?.query ?? "";
  const needle = findNeedle(query);
  // Rows re-project on every streamed token, usually without changing the
  // matches; keeping the same array then keeps the marks and the step from
  // being redone for nothing.
  const lastHits = useRef<FindHit[]>([]);
  const hits = useMemo(() => {
    const next = isOpen ? findHits(rows, query) : [];
    const previous = lastHits.current;
    const same =
      next.length === previous.length &&
      next.every((hit, index) => {
        const before = previous[index];
        return (
          before !== undefined &&
          before.key === hit.key &&
          before.itemId === hit.itemId &&
          before.rowIndex === hit.rowIndex
        );
      });
    if (!same) lastHits.current = next;
    return lastHits.current;
  }, [isOpen, rows, query]);
  // The current match is a message, named by its source item. Its display
  // key is not its identity: a round's first loaded reply takes the round's
  // key, so loading or dropping an earlier reply of that round hands the key
  // to a different message.
  let currentIndex = -1;
  if (find != null && find.key !== null) {
    currentIndex = hits.findIndex((hit) => hit.itemId === find.item);
    if (currentIndex < 0 && find.item !== null && isStreamOverlay(find.item)) {
      // A streamed reply being recorded is the same message under a new
      // item: the overlay item gives way to the recorded one, on one key.
      const recorded = hits.findIndex((hit) => hit.key === find.key);
      if (recorded >= 0 && !isStreamOverlay(hits[recorded]?.itemId ?? "")) currentIndex = recorded;
    }
  }
  const current = currentIndex < 0 ? null : (hits[currentIndex] ?? null);
  const historyEnded = olderPageKey === null;

  const rowsRef = useRef(rows);
  rowsRef.current = rows;

  const openWith = useCallback(
    (request?: { query?: string; itemId?: string }) => {
      const active = document.activeElement;
      if (active instanceof HTMLElement && active.closest("[data-find-bar]") === null) returnFocus.current = active;
      setFocusEpoch((epoch) => epoch + 1);
      if (request?.query !== undefined) setJumpEpoch((epoch) => epoch + 1);
      setState((previous) => {
        const kept = previous?.ref === ref ? previous : null;
        if (request?.query === undefined) return kept ?? { ref, query: "", ...NO_MATCH, seek: null };
        const found = findHits(rowsRef.current, request.query);
        const picked = found.find((hit) => hit.itemId === request.itemId) ?? found.at(-1);
        return { ref, query: request.query, ...matchOf(picked), seek: null };
      });
    },
    [ref],
  );
  const open = useCallback(() => openWith(), [openWith]);

  const request = useFindRequest(ref, paneId);
  useEffect(() => {
    if (!request) return;
    openWith(request);
    consumeFindRequest(ref);
  }, [request, openWith, ref]);

  const close = useCallback(() => {
    setState(null);
    const target = returnFocus.current;
    returnFocus.current = null;
    if (target instanceof HTMLElement && target.isConnected) target.focus();
    else requestComposerFocus(ref, paneId);
  }, [ref, paneId]);

  const setQuery = useCallback(
    (next: string) => {
      // Typing lands on the newest loaded match, as the reader is usually at
      // the end. It never reads older history by itself: each keystroke of a
      // longer word would otherwise page the whole session for a prefix.
      const newest = findHits(rowsRef.current, next).at(-1);
      setState({ ref, query: next, ...matchOf(newest), seek: null });
    },
    [ref],
  );

  const step = useCallback(
    (direction: 1 | -1) => {
      if (find === null || needle === "") return;
      const from = currentIndex < 0 ? (direction === -1 ? hits.length : -1) : currentIndex;
      const next = hits[from + direction];
      if (next) setState({ ...find, ...matchOf(next), seek: null });
      // Nothing older is loaded: look in older history.
      else if (direction === -1 && !historyEnded) setState({ ...find, seek: "older" });
    },
    [find, needle, currentIndex, hits, historyEnded],
  );

  // A refresh or a resync after reconnecting replaces the loaded window with
  // the newest page, which can drop the page the current match was found on.
  // Find then reads back to it rather than losing the reader's place. A match
  // whose message is still loaded but no longer holds the words is simply no
  // longer current.
  const lost = find !== null && find.key !== null && current === null;
  useEffect(() => {
    if (find === null) return;
    if (current !== null && (current.key !== find.key || current.itemId !== find.item)) {
      // Renamed or recorded: follow the message to what it goes by now.
      setState({ ...find, ...matchOf(current), seek: find.seek === "restore" ? null : find.seek });
      return;
    }
    if (!lost || find.seek === "restore") return;
    const evicted = find.item !== null && !itemLoaded(find.item);
    if (evicted && !historyEnded) setState({ ...find, seek: "restore" });
    else setState({ ...find, ...NO_MATCH, seek: null });
  }, [find, current, lost, historyEnded, itemLoaded]);

  // While seeking, take the newest match older than the current one as soon
  // as a page brings one, and otherwise keep Find's demand standing. Asking
  // again is idempotent while a page is in flight or a retry is paced. A page
  // settling resolves every consumer's demand, including one made while it
  // was still merging, so the settle itself (olderLoading) asks anew.
  // biome-ignore lint/correctness/useExhaustiveDependencies: olderPageKey and olderLoading re-arm the demand after each page that settles without a match
  useEffect(() => {
    if (find === null || find.seek === null) return;
    if (find.seek === "restore") {
      if (current !== null) {
        setState({ ...find, seek: null });
        return;
      }
      if (historyEnded) {
        setState({ ...find, ...NO_MATCH, seek: null });
        return;
      }
    } else {
      if (lost) return;
      const older = currentIndex < 0 ? hits.at(-1) : hits[currentIndex - 1];
      if (older) {
        setState({ ...find, ...matchOf(older), seek: null });
        return;
      }
      if (historyEnded) {
        setState({ ...find, seek: null });
        return;
      }
    }
    if (olderPermanent) return;
    findOlder();
  }, [find, hits, current, lost, currentIndex, historyEnded, olderPageKey, olderLoading, olderPermanent, findOlder]);

  const seeking = find !== null && find.seek !== null;
  useEffect(() => {
    if (!seeking) return;
    return cancelFindOlder;
  }, [seeking, cancelFindOlder]);

  // A new current match is a jump the reader asked for. Landing takes a few
  // frames: the row may not be rendered yet, a page may have just prepended
  // above it, and the row has to be measured.
  // So for a short window after the step, each frame either asks the scroll
  // owner for the row again (while the entry is not mounted) or settles the
  // mounted entry into view. The reader's own scroll ends the window.
  const settle = useRef<{ key: string; until: number; jumpedAt: number } | null>(null);
  const settleFrame = useRef<number | null>(null);
  const currentKey = current?.key ?? null;

  const sync = useRef(() => {});
  sync.current = () => {
    const port = listRef.current?.getScrollElement() ?? null;
    const marked = markTranscript(port, hits, currentKey, needle);
    const pending = settle.current;
    if (!port || !pending) return;
    const now = performance.now();
    const hit = pending.key === currentKey ? hits.find((candidate) => candidate.key === currentKey) : undefined;
    if (!hit || now > pending.until) {
      settle.current = null;
      return;
    }
    if (marked.current === null) {
      if (now - pending.jumpedAt < JUMP_RETRY_MS) return;
      pending.jumpedAt = now;
      jumpToRow(hit.rowIndex);
      return;
    }
    const rect = (marked.focus ?? marked.current).getBoundingClientRect();
    const target = scrollTopRevealing(port, rect);
    if (target === null) return;
    const before = port.scrollTop;
    port.scrollTop = target;
    if (port.scrollTop !== before) markGesture(true, before);
  };

  // A landing is owed for a new current match and for new words inside the
  // same entry (the place in it moves). Rows prepending above it are not one.
  // biome-ignore lint/correctness/useExhaustiveDependencies: the dependencies are the landing's causes, listed above
  useEffect(() => {
    if (current === null) {
      settle.current = null;
      return;
    }
    // The reader chose this place, in view or not: output arriving later
    // must not carry them off it.
    holdPosition();
    settle.current = {
      key: current.key,
      until: performance.now() + SETTLE_WINDOW_MS,
      jumpedAt: Number.NEGATIVE_INFINITY,
    };
    // Frames, not this effect, drive the scroll: the virtual list flushes its
    // own state when it moves, which React forbids inside a commit.
    const tick = () => {
      settleFrame.current = null;
      sync.current();
      if (settle.current !== null) settleFrame.current = requestAnimationFrame(tick);
    };
    if (settleFrame.current === null) settleFrame.current = requestAnimationFrame(tick);
  }, [currentKey, jumpEpoch, needle]);
  // Marks are document state, not React output, so they follow what is
  // mounted: the virtual list mounting a row, a fold opening, a streamed
  // reply growing. One pass per frame at most; closing Find, or this
  // conversation unmounting, takes them away. The scroll port exists only
  // once the transcript has rows, which can be after Find opened.
  const hasRows = rows.length > 0;
  // biome-ignore lint/correctness/useExhaustiveDependencies: hasRows stands for the port mounting
  useLayoutEffect(() => {
    if (!isOpen) return;
    const port = listRef.current?.getScrollElement();
    if (!port) return;
    let frame: number | null = null;
    const observer = new MutationObserver(() => {
      if (frame !== null) return;
      frame = requestAnimationFrame(() => {
        frame = null;
        sync.current();
      });
    });
    observer.observe(port, { childList: true, subtree: true, characterData: true });
    const readerMoved = () => {
      settle.current = null;
    };
    port.addEventListener("wheel", readerMoved, { passive: true });
    port.addEventListener("touchmove", readerMoved, { passive: true });
    return () => {
      observer.disconnect();
      port.removeEventListener("wheel", readerMoved);
      port.removeEventListener("touchmove", readerMoved);
      if (frame !== null) cancelAnimationFrame(frame);
      if (settleFrame.current !== null) cancelAnimationFrame(settleFrame.current);
      settleFrame.current = null;
      settle.current = null;
      markTranscript(port, [], null, "");
    };
  }, [isOpen, listRef, hasRows]);
  // Marking is safe inside a commit; only the scroll waits for a frame.
  useLayoutEffect(() => {
    if (!isOpen) return;
    markTranscript(listRef.current?.getScrollElement() ?? null, hits, currentKey, needle);
  }, [isOpen, listRef, hits, currentKey, needle]);

  // The app's find chord. Declining leaves the keydown to the browser, which
  // is how its own find stays available outside a transcript, and inside the
  // bar itself for a reader who wants it.
  const ownsKeyboardRef = useRef(ownsKeyboard);
  ownsKeyboardRef.current = ownsKeyboard;
  useEffect(() => {
    installKeybindings();
    return keybindingsRegistry.getState().registerAction(ACTIONS.transcriptFind, (event) => {
      if (!ownsKeyboardRef.current()) return false;
      const target = event?.target;
      if (target instanceof Element && target.closest("[data-find-bar]") !== null) return false;
      open();
      return true;
    });
  }, [open]);

  const leaveHistory = useCallback(() => {
    settle.current = null;
    setState((previous) => {
      if (previous?.ref !== ref || previous.seek === null) return previous;
      // A match being read back to is not loaded; keeping it would start the
      // read again.
      return previous.seek === "restore" ? { ...previous, ...NO_MATCH, seek: null } : { ...previous, seek: null };
    });
  }, [ref]);

  if (find === null) return { view: null, open, leaveHistory };

  let status: FindStatus;
  if (needle === "") status = "idle";
  else if (seeking) status = olderPermanent ? "incomplete" : "searching";
  else if (hits.length > 0) status = "matches";
  else status = historyEnded ? "none" : "unsearched";

  return {
    open,
    leaveHistory,
    view: {
      query,
      status,
      position: currentIndex < 0 ? 0 : hits.length - currentIndex,
      count: hits.length,
      moreHistory: !historyEnded,
      error: status === "incomplete" ? olderError : null,
      canStepOlder: needle !== "" && !seeking && (historyEnded ? hits.length > 0 && currentIndex !== 0 : true),
      canStepNewer: needle !== "" && hits.length > 0 && (currentIndex < 0 || currentIndex < hits.length - 1),
      focusEpoch,
      setQuery,
      step,
      close,
    },
  };
}
