// The floating Next-needs-you control. One per focused conversation, rendered
// above the composer by FlowOverlay (or by ConversationBody's dormant dock).
// It owns only "the list is open" and "a pointer hold is in progress"; every
// session list, ordering, validity and navigation decision belongs to the
// shared needs-you controller.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import type {
  ReactElement,
  KeyboardEvent as ReactKeyboardEvent,
  MouseEvent as ReactMouseEvent,
  PointerEvent as ReactPointerEvent,
} from "react";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useOptionalNeedsYou } from "../../shell/NeedsYouProvider";
import type { NeedsYouChoice } from "../../shell/needsYouActions";
import { Button, IconButton, ListRow, Popover } from "../../widgets";
import { LONG_PRESS_MS, POINTER_CLICK_WINDOW_MS } from "../../widgets/hovercard";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./nextNeedsYou.module.css";

export interface NextNeedsYouProps {
  sessionRef: string;
  viewId: string;
  hidden: boolean;
}

const HOLD_SLOP_PX = 10;

const CLASS = {
  root: requireClass(styles.root, "nextNeedsYou.module.css", "root"),
  nextSlot: requireClass(styles.nextSlot, "nextNeedsYou.module.css", "nextSlot"),
  nextLead: requireClass(styles.nextLead, "nextNeedsYou.module.css", "nextLead"),
  nextLabel: requireClass(styles.nextLabel, "nextNeedsYou.module.css", "nextLabel"),
  ellipsis: requireClass(styles.ellipsis, "nextNeedsYou.module.css", "ellipsis"),
  list: requireClass(styles.list, "nextNeedsYou.module.css", "list"),
};

function attentionReason(summary: NavigationSessionSummary): string | undefined {
  if (summary.approval_pending) return "approval";
  if (summary.ask_pending) return "question";
  if (summary.failure) return "failure";
  return undefined;
}

function attentionReasonLabel(reason: string | undefined): string | undefined {
  if (reason === "approval") return "Approval pending";
  if (reason === "question") return "Question pending";
  if (reason === "failure") return "Failed";
  return undefined;
}

function rowMeta(choice: NeedsYouChoice, reason: string | undefined): string | undefined {
  if (choice.selectable) return attentionReasonLabel(reason);
  if (choice.refreshState === "failed") return "Couldn't refresh";
  if (choice.refreshState === "refreshing") return "Refreshing";
  return undefined;
}

export function NextNeedsYou(props: NextNeedsYouProps): ReactElement | null {
  const needsYou = useOptionalNeedsYou();
  if (needsYou === null) return null;
  return <OwnedNextNeedsYou {...props} needsYou={needsYou} />;
}

function OwnedNextNeedsYou({
  sessionRef,
  viewId,
  hidden,
  needsYou,
}: NextNeedsYouProps & { needsYou: NonNullable<ReturnType<typeof useOptionalNeedsYou>> }): ReactElement | null {
  const { controller, snapshot } = needsYou;
  const [listOpen, setListOpen] = useState(false);
  const listOpenRef = useRef(listOpen);
  listOpenRef.current = listOpen;

  const nextChoice = snapshot.choices.find((choice) => choice.summary.ref === snapshot.nextRef);
  const nextTitle = nextChoice?.summary.title?.trim() ?? "";
  const otherChoices = snapshot.choices.filter((choice) => choice.summary.ref !== sessionRef);
  // A loaded page holding only the current session is not the whole queue.
  // Keep demand visible while another page may exist, a read failed, or a page
  // is loading. On a cold shell, the first manifest read is also enough to
  // offer page zero; after a manifest was known, reconnects stay quiet until
  // the queue returns or a read fails.
  const visible =
    !hidden &&
    !snapshot.blocked &&
    (snapshot.nextRef !== null ||
      snapshot.readFailed ||
      (snapshot.canLoadMore && (snapshot.choices.length > 0 || snapshot.loading || snapshot.canLoadInitialPage)));

  const moreRef = useRef<HTMLButtonElement>(null);
  const listRef = useRef<HTMLElement>(null);
  const holdTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const suppressBackstopRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const pressRef = useRef<{ x: number; y: number; pointerId: number } | null>(null);
  const captureRef = useRef<{ element: Element; pointerId: number } | null>(null);
  const pointerDownRef = useRef(false);
  const holdCompletedRef = useRef(false);
  const suppressClickRef = useRef(false);
  const restoreFocusRef = useRef(false);
  const gestureCleanupRef = useRef<(() => void) | null>(null);
  const visibilityCleanupRef = useRef<(() => void) | null>(null);

  const clearHoldTimer = useCallback(() => {
    clearTimeout(holdTimerRef.current);
    holdTimerRef.current = undefined;
    // The visibility watch exists only while a hold is pending. Completing,
    // cancelling or unmounting a hold all pass through here.
    const detachVisibility = visibilityCleanupRef.current;
    visibilityCleanupRef.current = null;
    detachVisibility?.();
  }, []);

  const clearSuppressBackstop = useCallback(() => {
    clearTimeout(suppressBackstopRef.current);
    suppressBackstopRef.current = undefined;
  }, []);

  // The one click a completed hold still emits arrives with the release, so
  // its suppression window starts there, not when the list opened: a hold
  // longer than the window would otherwise navigate on release.
  const armSuppressBackstop = useCallback(() => {
    clearSuppressBackstop();
    suppressBackstopRef.current = setTimeout(() => {
      suppressBackstopRef.current = undefined;
      suppressClickRef.current = false;
    }, POINTER_CLICK_WINDOW_MS);
  }, [clearSuppressBackstop]);

  const detachGesture = useCallback(() => {
    const cleanup = gestureCleanupRef.current;
    if (cleanup === null) return;
    gestureCleanupRef.current = null;
    cleanup();
  }, []);

  // Capture keeps the release delivered while the pointer is away from the
  // button, which is what lets a hold survive a small wobble - but leaving it
  // armed after the gesture is cancelled hands the eventual release back to
  // this button, and the click that follows it would navigate about a gesture
  // the reader already cancelled. Drop it the moment the gesture ends.
  const releasePointerCapture = useCallback(() => {
    const captured = captureRef.current;
    captureRef.current = null;
    if (captured === null) return;
    try {
      if (captured.element.hasPointerCapture(captured.pointerId)) {
        captured.element.releasePointerCapture(captured.pointerId);
      }
    } catch {
      // The element can be gone, and the capture already revoked.
    }
  }, []);

  // A pending hold ends without opening anything. A hold that already
  // completed keeps its one-click suppression until the release click or the
  // backstop consumes it.
  const cancelHold = useCallback(() => {
    clearHoldTimer();
    pressRef.current = null;
    detachGesture();
    releasePointerCapture();
    if (!holdCompletedRef.current) {
      suppressClickRef.current = false;
      clearSuppressBackstop();
    }
  }, [clearHoldTimer, clearSuppressBackstop, detachGesture, releasePointerCapture]);

  // The gesture belongs to the document the button lives in. A conversation in
  // a Dockview popout publishes its pointer events in that window, so listeners
  // on the opener's window would never see the movement that cancels a hold.
  const armGesture = useCallback(
    (doc: Document) => {
      if (gestureCleanupRef.current !== null) return;
      const owns = (event: PointerEvent) => {
        const press = pressRef.current;
        return press !== null && press.pointerId === event.pointerId;
      };
      const onMove = (event: PointerEvent) => {
        if (!owns(event)) return;
        const press = pressRef.current;
        if (!press) return;
        // A hold that already completed opened the list; the press stays alive
        // until it is released so the one click it emits is still consumed. A
        // drift must not end that gesture.
        if (holdCompletedRef.current) return;
        if (Math.hypot(event.clientX - press.x, event.clientY - press.y) > HOLD_SLOP_PX) cancelHold();
      };
      const onUp = (event: PointerEvent) => {
        if (!owns(event)) return;
        pointerDownRef.current = false;
        cancelHold();
        if (suppressClickRef.current) armSuppressBackstop();
      };
      const onCancel = (event: PointerEvent) => {
        if (!owns(event)) return;
        pointerDownRef.current = false;
        holdCompletedRef.current = false;
        cancelHold();
      };
      doc.addEventListener("pointermove", onMove);
      doc.addEventListener("pointerup", onUp);
      doc.addEventListener("pointercancel", onCancel);
      gestureCleanupRef.current = () => {
        doc.removeEventListener("pointermove", onMove);
        doc.removeEventListener("pointerup", onUp);
        doc.removeEventListener("pointercancel", onCancel);
      };
    },
    [armSuppressBackstop, cancelHold],
  );

  const closeList = useCallback((restoreFocus: boolean) => {
    restoreFocusRef.current = restoreFocus;
    setListOpen(false);
  }, []);

  const openList = useCallback(() => {
    restoreFocusRef.current = false;
    setListOpen(true);
  }, []);

  function handleNextPointerDown(event: ReactPointerEvent<HTMLButtonElement>) {
    if (event.button !== 0 || listOpenRef.current) return;
    // A second pointer never takes over an active gesture.
    if (pressRef.current !== null) return;
    holdCompletedRef.current = false;
    suppressClickRef.current = false;
    clearSuppressBackstop();
    pointerDownRef.current = true;
    pressRef.current = { x: event.clientX, y: event.clientY, pointerId: event.pointerId };
    const doc = event.currentTarget.ownerDocument ?? document;
    armGesture(doc);
    try {
      event.currentTarget.setPointerCapture(event.pointerId);
      captureRef.current = { element: event.currentTarget, pointerId: event.pointerId };
    } catch {
      // jsdom has no pointer capture; the document listeners own the gesture.
    }
    holdTimerRef.current = setTimeout(() => {
      clearHoldTimer();
      holdCompletedRef.current = true;
      suppressClickRef.current = true;
      openList();
    }, LONG_PRESS_MS);
    // A page that goes hidden loses the pointer, so the pending hold ends like
    // an early release: the timer never fires and the next click is not
    // consumed as the completed hold's.
    const onVisibilityChange = () => {
      if (doc.visibilityState !== "hidden") return;
      holdCompletedRef.current = false;
      cancelHold();
    };
    doc.addEventListener("visibilitychange", onVisibilityChange);
    visibilityCleanupRef.current = () => {
      doc.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }

  function handleNextClick(event: ReactMouseEvent<HTMLButtonElement>) {
    if (suppressClickRef.current) {
      // The one click a completed hold still emits belongs to the hold.
      suppressClickRef.current = false;
      clearSuppressBackstop();
      event.preventDefault();
      event.stopPropagation();
      return;
    }
    if (listOpenRef.current) closeList(false);
    void controller.next();
  }

  function handleLostPointerCapture() {
    if (holdTimerRef.current === undefined) {
      // A completed hold whose pointer never releases still needs its click
      // window to end - but only once the press itself is over. While the
      // pointer is still down the release that ends the hold has not arrived,
      // and starting the window here would let a slow release navigate.
      if (suppressClickRef.current && !pointerDownRef.current) armSuppressBackstop();
      return;
    }
    holdCompletedRef.current = false;
    cancelHold();
  }

  function handleListKeyDown(event: ReactKeyboardEvent<HTMLElement>) {
    if (event.key !== "Escape") return;
    event.preventDefault();
    event.stopPropagation();
    closeList(true);
  }

  // A visible-to-hidden transition cancels the pending UI intent. A later
  // keyboard action is still governed by the shell's editable-target policy;
  // this component installs no global shortcut handler.
  const wasVisibleRef = useRef(visible);
  useLayoutEffect(() => {
    const wasVisible = wasVisibleRef.current;
    wasVisibleRef.current = visible;
    if (!wasVisible || visible) return;
    // The control's own pending intent (a hold, an open list) ends here. The
    // shared controller additionally drops any deferred navigation it owns so
    // a late reply cannot act for a control the reader can no longer see.
    controller.cancel();
    holdCompletedRef.current = false;
    suppressClickRef.current = false;
    clearSuppressBackstop();
    cancelHold();
    restoreFocusRef.current = false;
    if (listOpenRef.current) setListOpen(false);
  }, [visible, controller, cancelHold, clearSuppressBackstop]);

  // Focus moves into the independent list when it opens.
  useEffect(() => {
    if (!listOpen) return;
    const list = listRef.current;
    if (!list) return;
    const first = list.querySelector<HTMLElement>('button:not([disabled]):not([aria-disabled="true"])');
    (first ?? list).focus();
  }, [listOpen]);

  // Escape restores the trigger once the panel has unmounted. Outside and
  // scroll dismissal leave focus wherever the click or scroll put it.
  useEffect(() => {
    if (listOpen) return;
    if (!restoreFocusRef.current) return;
    restoreFocusRef.current = false;
    moreRef.current?.focus();
  }, [listOpen]);

  // closeOnScroll=false preserves the list's own scrolling, so outer scroll
  // and resize dismissal is this component's. The anchor disappearing ends
  // the panel with the component.
  useEffect(() => {
    if (!listOpen) return;
    // The trigger's own window: a conversation in a Dockview popout scrolls and
    // resizes there, and the list lives there too.
    const view = moreRef.current?.ownerDocument?.defaultView ?? window;
    // contains() reads across realms, where `instanceof Node` does not, and it
    // throws on a target that is not a Node at all (a window, say).
    const insideList = (target: EventTarget | null): boolean => {
      const list = listRef.current;
      if (!list || target === null) return false;
      try {
        return list.contains(target as Node);
      } catch {
        return false;
      }
    };
    const onScroll = (event: Event) => {
      if (insideList(event.target)) return;
      closeList(false);
    };
    const onResize = () => closeList(false);
    view.addEventListener("scroll", onScroll, true);
    view.addEventListener("resize", onResize);
    // An iOS keyboard resizes the visual viewport without a window resize, and
    // the panel's fixed coordinates would sit stale behind it. Only the resize:
    // a visual-viewport scroll is the page's own scroll, which the capture-phase
    // window listener above already judges.
    const visualViewport = view.visualViewport;
    visualViewport?.addEventListener("resize", onResize);
    return () => {
      view.removeEventListener("scroll", onScroll, true);
      view.removeEventListener("resize", onResize);
      visualViewport?.removeEventListener("resize", onResize);
    };
  }, [listOpen, closeList]);

  useEffect(
    () => () => {
      holdCompletedRef.current = false;
      clearHoldTimer();
      clearSuppressBackstop();
      detachGesture();
    },
    [clearHoldTimer, clearSuppressBackstop, detachGesture],
  );

  if (!visible) return null;

  const nextAccessibleName = nextTitle ? `Next needs you: ${nextTitle}` : "Next needs you";

  return (
    <div className={CLASS.root} data-testid="next-needs-you" data-view-id={viewId} data-session-ref={sessionRef}>
      <div className={CLASS.nextSlot}>
        <Button
          variant="secondary"
          size="md"
          data-testid="next-needs-you-next"
          aria-label={nextAccessibleName}
          onPointerDown={handleNextPointerDown}
          onPointerCancel={() => {
            holdCompletedRef.current = false;
            cancelHold();
          }}
          onLostPointerCapture={handleLostPointerCapture}
          onClick={handleNextClick}
        >
          <span className={CLASS.nextLead}>Next</span>
          {nextTitle !== "" && <span className={CLASS.nextLabel}>{nextTitle}</span>}
        </Button>
      </div>
      <Popover
        open={listOpen}
        onClose={() => closeList(false)}
        autoFocus={false}
        closeOnScroll={false}
        data-testid="next-needs-you-list"
        trigger={
          <IconButton
            ref={moreRef}
            data-testid="next-needs-you-more"
            label="More needs-you sessions"
            aria-haspopup="true"
            aria-expanded={listOpen}
            icon={
              <span className={CLASS.ellipsis} aria-hidden="true">
                …
              </span>
            }
            variant="secondary"
            size="md"
            onClick={() => {
              if (listOpenRef.current) closeList(false);
              else openList();
            }}
          />
        }
      >
        <section
          ref={listRef}
          className={CLASS.list}
          aria-label="Needs you sessions"
          tabIndex={-1}
          onKeyDown={handleListKeyDown}
        >
          {otherChoices.map((choice) => {
            const reason = attentionReason(choice.summary);
            return (
              <ListRow
                key={choice.summary.ref}
                data-testid="next-needs-you-row"
                data-ref={choice.summary.ref}
                data-reason={reason}
                variant="plain"
                label={choice.summary.title || choice.summary.ref}
                meta={rowMeta(choice, reason)}
                data-refresh-state={choice.refreshState ?? undefined}
                aria-disabled={choice.selectable ? undefined : "true"}
                onSelect={() => {
                  closeList(false);
                  controller.select(choice.summary.ref);
                }}
              />
            );
          })}
          {snapshot.canLoadMore && (
            <Button
              variant="quiet"
              size="sm"
              data-testid="next-needs-you-load-more"
              disabled={snapshot.loading}
              onClick={() => void controller.loadMore()}
            >
              Load more
            </Button>
          )}
        </section>
      </Popover>
    </div>
  );
}
