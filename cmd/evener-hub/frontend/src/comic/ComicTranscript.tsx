import {
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
} from "react";
import { isAtBottom } from "../panes/session/transcript/flow/scrollMetrics";
import { useTranscriptScrollKeys } from "../panes/session/transcript/flow/useTranscriptScrollKeys";
import { useChromeStore } from "../shell/chromeStore";
import { useIsMobile } from "../shell/useIsMobile";
import { isEntryTranscriptLocalOpening, threadsStore } from "../stores/threads";
import {
  Button,
  type CommittedVirtualListLayout,
  Popover,
  Select,
  VirtualList,
  type VirtualListHandle,
} from "../widgets";
import { type ComicExpression, ComicPanel, type ComicScene } from "./ComicPanel";
import { ComicPlainTranscript } from "./ComicPlainTranscript";
import { ComicSubchatHeader } from "./ComicSubchatHeader";
import styles from "./comic.module.css";
import { type ComicFlowRow, composeComicFlow, findComicFlowAnchorRow } from "./comicRows";
import { createComicMeasurer, loadComicFonts } from "./measure";
import type { ComicReadingPosition } from "./readerPosition";
import type { useComicReader } from "./useComicReader";
import { type ComicReaderSnapshot, type ComicSubchatState, comicPathContains, comicPathKey } from "./useComicSubchats";

// Source ranges belong to this bespoke transcript surface, not the virtual-list widget.
function textPoint(source: HTMLElement, offset: number): DOMRect | undefined {
  const text = source.querySelector("[data-comic-text]")?.firstChild;
  if (!text || text.nodeType !== Node.TEXT_NODE) return source.getBoundingClientRect();
  const local = Math.min(
    Math.max(0, offset - Number(source.dataset.comicStart)),
    Math.max(0, (text.textContent?.length ?? 0) - 1),
  );
  const range = document.createRange();
  range.setStart(text, local);
  range.setEnd(text, local + 1);
  return range.getBoundingClientRect();
}

function capturePoint(port: HTMLElement, sessionRef: string): ComicReadingPosition | undefined {
  if (isAtBottom(port)) {
    const sources = port.querySelectorAll<HTMLElement>("[data-comic-entry]");
    const last = sources.item(sources.length - 1);
    return {
      mode: "end",
      sessionRef: last?.dataset.comicSession ?? sessionRef,
      path: JSON.parse(last?.dataset.comicPath ?? "[]"),
    };
  }
  const top = port.getBoundingClientRect().top;
  for (const source of port.querySelectorAll<HTMLElement>("[data-comic-entry]")) {
    if (source.getBoundingClientRect().bottom <= top) continue;
    let low = Number(source.dataset.comicStart);
    let high = Math.max(low, Number(source.dataset.comicEnd) - 1);
    // First visible source code unit, including the middle of a former continuation.
    while (low < high) {
      const middle = Math.floor((low + high) / 2);
      const rect = textPoint(source, middle);
      if (rect && rect.top < top) low = middle + 1;
      else high = middle;
    }
    const point = textPoint(source, low);
    if (point && point.bottom > top)
      return {
        mode: "away",
        sessionRef: source.dataset.comicSession ?? sessionRef,
        path: JSON.parse(source.dataset.comicPath ?? "[]"),
        entryId: source.dataset.comicEntry ?? "",
        offset: low,
        viewportOffset: point.top - top,
      };
  }
}

export function ComicTranscript({
  root,
  subchats,
  reader,
  keyboardScrollPaneId = null,
  mode,
  appearanceView,
  onOpen,
}: {
  root: ComicReaderSnapshot;
  subchats: ComicSubchatState;
  onOpen(ref: string): void;
  reader: ReturnType<typeof useComicReader>;
  keyboardScrollPaneId?: string | null;
  mode: "comic" | "plain";
  appearanceView?: ReactNode;
}): React.ReactElement {
  const { entries, history } = root;
  const isMobile = useIsMobile();
  const host = useRef<HTMLDivElement>(null);
  const controlsId = useId();
  const list = useRef<VirtualListHandle>(null);
  const committed = useRef<CommittedVirtualListLayout | undefined>(undefined);
  const sessionRef = root.ref;
  const anchor = useRef<ComicReadingPosition>(reader.position ?? { mode: "end", sessionRef, path: [] });
  const restoring = useRef(!!reader.position);
  const commanded = useRef<number | undefined>(undefined);
  const savedOffset = useRef<number | undefined>(undefined);
  const moving = useRef(false);
  const movementStart = useRef<number | undefined>(undefined);
  const [scene, setScene] = useState<ComicScene>("room");
  const [expression, setExpression] = useState<ComicExpression>("neutral");
  const [appearanceOpen, setAppearanceOpen] = useState(false);
  const [font, setFont] = useState<"loading" | "ready" | "unavailable">("loading");
  const [geometry, setGeometry] = useState({ width: 0, epoch: 0 });
  const [tailSpace, setTailSpace] = useState(0);
  const [rows, setRows] = useState<readonly ComicFlowRow[]>([]);
  const probe = useRef<ReturnType<typeof createComicMeasurer> | undefined>(undefined);
  // Dockview moves the existing pane DOM between windows without remounting it.
  useChromeStore((state) => state.paneDocuments);
  const ownerDocument = host.current?.ownerDocument;
  const appearanceWindow = ownerDocument?.defaultView;
  useEffect(() => {
    const view = appearanceWindow;
    if (!appearanceOpen || !view) return;
    const close = () => setAppearanceOpen(false);
    const onScroll = (event: Event) => {
      // Reader restoration moves the transcript, not the Appearance trigger.
      if (event.target !== list.current?.getScrollElement()) close();
    };
    view.addEventListener("scroll", onScroll, true);
    view.addEventListener("resize", close);
    return () => {
      view.removeEventListener("scroll", onScroll, true);
      view.removeEventListener("resize", close);
    };
  }, [appearanceOpen, appearanceWindow]);
  const fitsCache = useRef(new Map<string, boolean>());
  const lastPage = useRef<string | undefined>(undefined);
  const snapshots = useMemo(() => new Map([[comicPathKey([]), root], ...subchats.readers]), [root, subchats.readers]);
  const entriesByPath = useMemo(
    () =>
      new Map(
        [...snapshots].map(([path, snapshot]) => [path, new Map(snapshot.entries.map((entry) => [entry.id, entry]))]),
      ),
    [snapshots],
  );
  const retainedAnchors = useRef(new Map<string, ComicReadingPosition>());
  const revision = useRef(0);
  const priorSnapshots = useRef(snapshots);
  const nearest = useRef<string | undefined>(undefined);
  const latest = useRef({ entries, history, reader, sessionRef, rows, snapshots, subchats });
  latest.current = { entries, history, reader, sessionRef, rows, snapshots, subchats };

  const [, retryOpeningPlacement] = useReducer((revision: number) => revision + 1, 0);
  const openingRefs = useCallback(() => {
    const position = anchor.current;
    return new Set([
      position.sessionRef,
      ...[...latest.current.snapshots.values()]
        .filter((snapshot) => comicPathContains(snapshot.path, position.path))
        .map((snapshot) => snapshot.ref),
    ]);
  }, []);
  const isOpening = useCallback(() => [...openingRefs()].some(isEntryTranscriptLocalOpening), [openingRefs]);
  useLayoutEffect(
    () =>
      threadsStore.subscribe((state, previous) => {
        if ([...openingRefs()].some((ref) => state.localOpenings.has(ref) !== previous.localOpenings.has(ref))) {
          // Retry through React's committed rows, never the previous DOM in this publication callback.
          retryOpeningPlacement();
        }
      }),
    [openingRefs],
  );

  const save = useCallback((position: ComicReadingPosition) => {
    anchor.current = position;
    savedOffset.current = list.current?.getScrollElement()?.scrollTop;
    latest.current.reader.save(position);
  }, []);

  const captureMovement = useCallback(
    (port: HTMLElement) => {
      // Native scrolling may start after the gesture's first animation frame.
      if (movementStart.current !== undefined && port.scrollTop === movementStart.current) return;
      const point = capturePoint(port, latest.current.sessionRef);
      if (!point) return;
      save(point);
      moving.current = false;
      movementStart.current = undefined;
      restoring.current = false;
      commanded.current = undefined;
    },
    [save],
  );

  useLayoutEffect(() => {
    anchor.current = reader.position ?? { mode: "end", sessionRef, path: [] };
    restoring.current = !!reader.position;
    moving.current = false;
    movementStart.current = undefined;
    savedOffset.current = undefined;
    lastPage.current = undefined;
    revision.current += 1;
    return () => {
      revision.current += 1;
    };
  }, [reader, sessionRef]);

  useLayoutEffect(() => {
    const container = host.current;
    const port = list.current?.getScrollElement();
    if (!container || !port) return;
    // The probe must inherit .comicRoot and use the scroll port's content width.
    const measuring = document.createElement("div");
    measuring.style.position = "absolute";
    measuring.style.top = "0";
    measuring.style.width = `${port.clientWidth}px`;
    container.append(measuring);
    probe.current = createComicMeasurer(measuring);
    let frame = 0;
    let alive = true;
    const resize = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        if (!alive) return;
        measuring.style.width = `${port.clientWidth}px`;
        fitsCache.current.clear();
        setGeometry((before) => ({ width: port.clientWidth, epoch: before.epoch + 1 }));
      });
    };
    const observer = new ResizeObserver(resize);
    observer.observe(port);
    const fontsChanged = () => {
      resize();
    };
    document.fonts?.addEventListener("loadingdone", fontsChanged);
    void loadComicFonts().then((state) => {
      if (alive) {
        setFont(state);
        resize();
      }
    });
    resize();
    return () => {
      alive = false;
      cancelAnimationFrame(frame);
      observer.disconnect();
      document.fonts?.removeEventListener("loadingdone", fontsChanged);
      probe.current?.dispose();
      probe.current = undefined;
      measuring.remove();
      fitsCache.current.clear();
    };
  }, []);

  useLayoutEffect(() => {
    const measurer = probe.current;
    if (!measurer) return;
    const used = new Set<string>();
    const fits: Parameters<typeof composeComicFlow>[3] = (text, composition, kind) => {
      const key = JSON.stringify([kind, composition, text]);
      used.add(key);
      const cached = fitsCache.current.get(key);
      if (cached !== undefined) return cached;
      const result = measurer.fits(text, composition, kind);
      fitsCache.current.set(key, result);
      return result;
    };
    const next = composeComicFlow(root, subchats, geometry.width, fits);
    const position = anchor.current;
    const target = snapshots.get(comicPathKey(position.path));
    if (
      !moving.current &&
      position.mode === "away" &&
      target?.ref === position.sessionRef &&
      !target.entries.some((entry) => entry.id === position.entryId)
    ) {
      const previous = priorSnapshots.current.get(comicPathKey(position.path))?.entries ?? [];
      const index = previous.findIndex((entry) => entry.id === position.entryId);
      if (index >= 0) {
        restoring.current = true;
        lastPage.current = undefined;
        const available = new Set(target.entries.map((entry) => entry.id));
        nearest.current = undefined;
        for (let distance = 1; distance < previous.length; distance += 1) {
          const candidate = [previous[index - distance], previous[index + distance]].find(
            (entry) => entry && available.has(entry.id),
          );
          if (candidate) {
            nearest.current = candidate.id;
            break;
          }
        }
      }
    }
    priorSnapshots.current = snapshots;
    for (const key of fitsCache.current.keys()) if (!used.has(key)) fitsCache.current.delete(key);
    setRows(next);
  }, [geometry, root, subchats, snapshots]);

  const restore = useCallback(
    (layout: CommittedVirtualListLayout) => {
      const current = latest.current;
      const port = list.current?.getScrollElement();
      if (
        !port ||
        current.reader !== reader ||
        current.rows !== rows ||
        !current.reader.reader.alive ||
        !layout.isCurrent() ||
        layout.virtualizer.scrollElement !== port
      )
        return;
      committed.current = layout;
      if (moving.current) {
        // A valid commit replaces the cancelled anchor before any placement.
        captureMovement(port);
        return;
      }
      const position = anchor.current;
      if (restoring.current && isOpening()) return;
      const rowIndex = findComicFlowAnchorRow(current.rows, position);
      if (rowIndex === undefined) return;
      if (position.mode === "end") {
        const lastPanel = current.rows.reduce((last, row, index) => (row.kind === "panels" ? index : last), -1);
        if (rowIndex < lastPanel) {
          list.current?.scrollToIndex(rowIndex, { align: "end" });
          commanded.current = port.scrollTop;
          return;
        }
        const bottom = Math.max(0, port.scrollHeight - port.clientHeight);
        if (Math.abs(port.scrollTop - bottom) > 1) layout.scrollToOffset(bottom);
        commanded.current = port.scrollTop;
        return;
      }
      const flow = current.rows[rowIndex];
      const slice =
        flow?.kind === "panels"
          ? flow.row.panels.find((panel) => panel.slice.entryId === position.entryId)?.slice
          : undefined;
      const source = [...port.querySelectorAll<HTMLElement>("[data-comic-entry]")].find(
        (node) =>
          node.dataset.comicEntry === position.entryId &&
          node.dataset.comicSession === position.sessionRef &&
          node.dataset.comicPath === comicPathKey(position.path) &&
          (flow?.kind === "strip" || Number(node.dataset.comicStart) === slice?.start),
      );
      if (!source) {
        list.current?.scrollToIndex(rowIndex, { align: "start" });
        commanded.current = port.scrollTop;
        return;
      }
      const point = textPoint(source, position.offset);
      if (!point) return;
      const delta = point.top - port.getBoundingClientRect().top - position.viewportOffset;
      const shortage = port.scrollTop + delta - (port.scrollHeight - port.clientHeight);
      if (shortage > 1) {
        // A shorter plain view may otherwise clamp the same source point to the
        // bottom. Keep only the needed tail space until the reader moves again.
        setTailSpace((space) => space + shortage);
        return;
      }
      if (Math.abs(delta) > 1) layout.scrollToOffset(port.scrollTop + delta);
      commanded.current = port.scrollTop;
      restoring.current = false;
    },
    [reader, rows, captureMovement, isOpening],
  );

  useLayoutEffect(() => {
    if (restoring.current && committed.current) restore(committed.current);
  });

  // biome-ignore lint/correctness/useExhaustiveDependencies: DOM adoption changes the listener owner without remounting the port.
  useLayoutEffect(() => {
    const port = list.current?.getScrollElement();
    if (!port) return;
    const doc = port.ownerDocument;
    let frame = 0;
    let pointer: { id: number; type: string; revision: number; cancelled: boolean } | undefined;
    const cancel = (event?: Event) => {
      pointer = undefined;
      revision.current += 1;
      const requestedRevision = revision.current;
      restoring.current = false;
      moving.current = true;
      movementStart.current = event ? port.scrollTop : undefined;
      setTailSpace(0);
      commanded.current = undefined;
      committed.current?.cancelPendingScroll();
      for (const snapshot of latest.current.snapshots.values()) snapshot.history.cancelOlder();
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        if (revision.current !== requestedRevision || latest.current.reader !== reader) return;
        if (list.current?.isLayoutCurrent()) captureMovement(port);
      });
    };
    const complete = (preserveStationary = false) => {
      if (preserveStationary && port.scrollTop === movementStart.current && list.current?.isLayoutCurrent()) {
        // A restored semantic offset need not be the first visible code unit
        // after reflow. Check its current position, not a layout object's identity:
        // cancelling history demand can publish a new layout without movement.
        const position = anchor.current;
        const source =
          position.mode === "away" &&
          [...port.querySelectorAll<HTMLElement>("[data-comic-entry]")].find(
            (node) =>
              node.dataset.comicEntry === position.entryId &&
              node.dataset.comicSession === position.sessionRef &&
              node.dataset.comicPath === comicPathKey(position.path) &&
              Number(node.dataset.comicStart) <= position.offset &&
              Number(node.dataset.comicEnd) > position.offset,
          );
        const point = source && position.mode === "away" ? textPoint(source, position.offset) : undefined;
        if (
          position.mode === "end"
            ? isAtBottom(port)
            : point && Math.abs(point.top - port.getBoundingClientRect().top - position.viewportOffset) <= 1
        ) {
          cancelAnimationFrame(frame);
          moving.current = false;
          movementStart.current = undefined;
          // A delayed restore scroll event still belongs to this preserved point.
          commanded.current = port.scrollTop;
          return;
        }
      }
      // Completion permits capture without movement. If geometry is invalid,
      // keep moving set until a valid commit captures, never place the old anchor.
      movementStart.current = undefined;
      if (list.current?.isLayoutCurrent()) captureMovement(port);
    };
    const blocked = (direction: number) => {
      if (!list.current?.isLayoutCurrent()) return;
      if (
        direction === 0 ||
        (direction < 0 && port.scrollTop <= 0) ||
        (direction > 0 && port.scrollTop >= port.scrollHeight - port.clientHeight)
      )
        complete();
    };
    const wheel = (event: WheelEvent) => {
      // Passive wheel delivery can follow compositor movement. Compare against
      // our saved/restored offset, not the already-scrolled DOM at delivery.
      const start = commanded.current ?? savedOffset.current ?? port.scrollTop;
      cancel(event);
      movementStart.current = start;
      blocked(event.deltaY);
    };
    const pointerDown = (event: PointerEvent) => {
      cancel(event);
      pointer = { id: event.pointerId, type: event.pointerType, revision: revision.current, cancelled: false };
    };
    const touchStart = (event: TouchEvent) => {
      // A touch pointerdown and its following touchstart are one intent.
      if (pointer?.type === "touch" && pointer.revision === revision.current) return;
      cancel(event);
    };
    const pointerEnd = (event: PointerEvent) => {
      if (
        !pointer ||
        pointer.id !== event.pointerId ||
        pointer.revision !== revision.current ||
        latest.current.reader !== reader ||
        !reader.reader.alive
      )
        return;
      if (event.type === "pointercancel") {
        // Native touch panning takes over here, potentially before any scroll.
        // Finger lift is not evidence that the ensuing fling has finished.
        pointer.cancelled = true;
        return;
      }
      if (!pointer.cancelled && moving.current) complete(true);
      pointer = undefined;
    };
    const scroll = () => {
      // Reflow can clamp the DOM offset before the new layout commits. It does
      // not end tail-following, only a reader gesture may clear this command.
      if (anchor.current.mode === "end" && commanded.current !== undefined) return;
      if (commanded.current !== undefined && Math.abs(port.scrollTop - commanded.current) <= 1) return;
      if (!list.current?.isLayoutCurrent()) return;
      if (restoring.current && !moving.current) return;
      captureMovement(port);
    };
    const selection = () => {
      const selected = doc.getSelection();
      // Focusing a disclosure can emit a collapsed selectionchange after its
      // click. That is not a text selection and must not cancel its destination.
      if (selected && !selected.isCollapsed && port.contains(selected.anchorNode)) cancel();
    };
    const key = (event: KeyboardEvent) => {
      if (["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " "].includes(event.key)) {
        cancel(event);
        const backward = ["ArrowUp", "PageUp", "Home"].includes(event.key) || (event.key === " " && event.shiftKey);
        if (backward) list.current?.noteBackwardMovement?.();
        blocked(backward ? -1 : 1);
      }
    };
    port.addEventListener("wheel", wheel, { passive: true });
    port.addEventListener("touchstart", touchStart, { passive: true });
    port.addEventListener("pointerdown", pointerDown);
    port.addEventListener("keydown", key);
    port.addEventListener("scroll", scroll);
    doc.addEventListener("pointerup", pointerEnd);
    doc.addEventListener("pointercancel", pointerEnd);
    doc.addEventListener("selectionchange", selection);
    return () => {
      cancelAnimationFrame(frame);
      port.removeEventListener("wheel", wheel);
      port.removeEventListener("touchstart", touchStart);
      port.removeEventListener("pointerdown", pointerDown);
      port.removeEventListener("keydown", key);
      port.removeEventListener("scroll", scroll);
      doc.removeEventListener("pointerup", pointerEnd);
      doc.removeEventListener("pointercancel", pointerEnd);
      doc.removeEventListener("selectionchange", selection);
    };
  }, [captureMovement, reader, ownerDocument]);

  useEffect(() => {
    if (!restoring.current || moving.current || !rows.length || isOpening()) return;
    const position = anchor.current;
    let target = root;
    for (let depth = 0; depth < position.path.length; depth += 1) {
      const key = position.path[depth];
      const path = position.path.slice(0, depth + 1);
      const strip = target.activity.strips.find((s) => s.key === key);
      if (!strip) {
        const activity = target.activity.snapshot?.delegates;
        if (!activity || activity.loading || activity.error || activity.unavailable) return;
        if (activity.hasMore) {
          void target.activity.loadMore();
          return;
        }
        if (target.history.hasOlder) {
          if (target.history.loadingOlder) lastPage.current = undefined;
          const page = target.history.olderPageKey ?? undefined;
          if (!target.history.loadingOlder && !target.history.olderError && lastPage.current !== page) {
            lastPage.current = page;
            target.history.loadOlderReportingError();
          }
          return;
        }
        if (target.history.loadingOlder) return;
        save({ mode: "end", sessionRef: target.ref, path: target.path });
        restoring.current = false;
        return;
      }
      if (!subchats.expandedPaths.some((p) => comicPathKey(p) === comicPathKey(path))) {
        subchats.expand(target.path, strip);
        return;
      }
      const child = subchats.readers.get(comicPathKey(path));
      if (subchats.availability?.get(comicPathKey(path)) === "unavailable" && !child) {
        save({
          mode: "away",
          sessionRef: target.ref,
          path: target.path,
          entryId: strip.key,
          offset: 0,
          viewportOffset: position.mode === "away" ? position.viewportOffset : 0,
        });
        restoring.current = false;
        return;
      }
      if (!child) return;
      target = child;
    }
    // Child history can publish before the composed rows commit. Loaded source
    // is not missing merely because its new layout has not reached this effect.
    if (
      position.mode !== "away" ||
      target.entries.some((entry) => entry.id === position.entryId) ||
      findComicFlowAnchorRow(rows, position) !== undefined
    )
      return;
    const history = target.history;
    if (history.hasOlder) {
      // Suppress repeated idle publications, not a cursor reused after shared
      // demand: hydration can rewind it before the owner's retry completes.
      if (history.loadingOlder) lastPage.current = undefined;
      const page = history.olderPageKey ?? target.entries.map((entry) => entry.id).join("\n");
      if (!history.loadingOlder && !history.olderError && lastPage.current !== page) {
        lastPage.current = page;
        history.loadOlderReportingError();
      }
    } else if (!history.loadingOlder) {
      const first = target.entries.find((entry) => entry.id === nearest.current) ?? target.entries[0];
      if (first) save({ ...position, sessionRef: target.ref, path: target.path, entryId: first.id, offset: 0 });
      restoring.current = false;
      if (committed.current) restore(committed.current);
    }
  });

  const toggle = (flow: Extract<ComicFlowRow, { kind: "strip" }>) => {
    const path = [...flow.path, flow.strip.key];
    const key = comicPathKey(path);
    const expanded = subchats.expandedPaths.some((p) => comicPathKey(p) === key);
    const selectPosition = (position: ComicReadingPosition) => {
      // The click can finish while layout is invalid. Its explicit destination
      // supersedes the pointer's deferred capture, including its queued frame.
      revision.current += 1;
      moving.current = false;
      movementStart.current = undefined;
      commanded.current = undefined;
      restoring.current = true;
      save(position);
    };
    if (expanded) {
      if (comicPathContains(path, anchor.current.path)) {
        retainedAnchors.current.set(key, anchor.current);
        selectPosition({
          mode: "away",
          sessionRef: flow.sessionRef,
          path: flow.path,
          entryId: flow.strip.key,
          offset: 0,
          viewportOffset: 0,
        });
      }
      subchats.collapse(path);
    } else {
      subchats.expand(flow.path, flow.strip);
      const retained = retainedAnchors.current.get(key);
      if (retained) {
        selectPosition(retained);
      }
    }
  };

  const jump = () => {
    revision.current += 1;
    restoring.current = false;
    moving.current = false;
    movementStart.current = undefined;
    for (const snapshot of snapshots.values()) snapshot.history.cancelOlder();
    setTailSpace(0);
    save({ mode: "end", sessionRef, path: [] });
    if (committed.current) restore(committed.current);
  };
  useTranscriptScrollKeys({
    paneId: keyboardScrollPaneId,
    listRef: list,
    jumpToBottom: jump,
    markGesture: () => keyboardIntent(),
    onPositioningCommand: () => keyboardIntent(),
  });
  function keyboardIntent() {
    revision.current += 1;
    restoring.current = false;
    moving.current = true;
    movementStart.current = undefined;
    commanded.current = undefined;
    setTailSpace(0);
    committed.current?.cancelPendingScroll();
    for (const snapshot of latest.current.snapshots.values()) snapshot.history.cancelOlder();
  }
  const endAnchorRow = anchor.current.mode === "end" ? (findComicFlowAnchorRow(rows, anchor.current) ?? -1) : -1;
  return (
    <div className={styles.reader} data-comic-mode={mode} data-comic-font={font}>
      <div className={styles.controls}>
        <Popover
          open={appearanceOpen}
          onClose={() => setAppearanceOpen(false)}
          closeOnScroll={false}
          trigger={
            <Button
              variant="link"
              aria-haspopup="dialog"
              aria-expanded={appearanceOpen}
              aria-controls={`${controlsId}-appearance`}
              onClick={() => setAppearanceOpen((open) => !open)}
            >
              Appearance
            </Button>
          }
        >
          <div className={styles.appearance} id={`${controlsId}-appearance`} role="dialog" aria-label="Appearance">
            {appearanceView}
            {mode === "comic" && (
              <>
                <label htmlFor={`${controlsId}-scene`}>
                  Scene
                  <Select
                    id={`${controlsId}-scene`}
                    value={scene}
                    options={[
                      { value: "room", label: "Room" },
                      { value: "field", label: "Field" },
                      { value: "white", label: "White" },
                    ]}
                    onChange={(event) => setScene(event.target.value as ComicScene)}
                  />
                </label>
                <label htmlFor={`${controlsId}-expression`}>
                  Human expression
                  <Select
                    id={`${controlsId}-expression`}
                    value={expression}
                    options={["neutral", "happy-wave", "angry", "scared", "point", "celebrate"].map((value) => ({
                      value,
                      label: value,
                    }))}
                    onChange={(event) => setExpression(event.target.value as ComicExpression)}
                  />
                </label>
              </>
            )}
          </div>
        </Popover>
        <Button variant="link" aria-label="Jump to latest" onClick={jump}>
          {isMobile ? "Latest" : "Jump to latest"}
        </Button>
        {history.hasOlder && (
          <Button variant="link" disabled={history.loadingOlder} onClick={history.loadOlderReportingError}>
            Older messages
          </Button>
        )}
        {history.olderError && (
          <div role="alert">
            {history.olderError}{" "}
            <Button variant="link" onClick={history.loadOlderReportingError}>
              Retry
            </Button>
          </div>
        )}
      </div>
      <div ref={host} className={styles.scroll} data-comic-scroll="">
        <VirtualList
          ref={list}
          count={rows.length}
          dynamic
          getItemKey={(index) => rows[index]?.id ?? index}
          anchorToEnd={
            anchor.current.mode === "end" && !rows.some((row, index) => row.kind === "panels" && index > endAnchorRow)
          }
          estimateSize={() => (mode === "comic" ? 380 : 180)}
          onLayout={restore}
          renderRow={(index) => {
            const flow = rows[index];
            if (!flow) return null;
            if (flow.kind !== "panels")
              return (
                <div
                  data-comic-flow={flow.kind}
                  data-comic-session={flow.sessionRef}
                  data-comic-path={comicPathKey(flow.path)}
                  style={index === rows.length - 1 ? { paddingBottom: tailSpace } : undefined}
                >
                  {flow.kind === "strip" && (
                    <div
                      data-comic-entry={flow.strip.key}
                      data-comic-start={0}
                      data-comic-end={0}
                      data-comic-session={flow.sessionRef}
                      data-comic-path={comicPathKey(flow.path)}
                    >
                      <ComicSubchatHeader
                        strip={flow.strip}
                        expanded={subchats.expandedPaths.some(
                          (p) => comicPathKey(p) === comicPathKey([...flow.path, flow.strip.key]),
                        )}
                        onToggle={() => toggle(flow)}
                        onOpen={onOpen}
                      />
                    </div>
                  )}
                  {flow.kind === "turn-errors-group" && <h2>Turn errors</h2>}
                  {flow.kind === "unplaced-group" && <h2>Meanwhile… outside loaded history</h2>}
                  {flow.kind === "availability" && (
                    <div role={flow.availability === "unavailable" ? "alert" : "status"}>
                      {flow.availability === "unavailable"
                        ? "This conversation is no longer available"
                        : flow.availability === "repairing"
                          ? "Repairing conversation history"
                          : flow.availability === "disconnected"
                            ? "Waiting for connection"
                            : "Loading conversation"}
                    </div>
                  )}
                  {flow.kind === "history" && flow.path.length > 0 && (
                    <>
                      {flow.history.hasOlder && (
                        <Button
                          variant="link"
                          disabled={flow.history.loadingOlder}
                          onClick={flow.history.loadOlderReportingError}
                        >
                          Older messages
                        </Button>
                      )}
                      {flow.history.olderError && (
                        <div role="alert">
                          {flow.history.olderError}{" "}
                          <Button variant="link" onClick={flow.history.loadOlderReportingError}>
                            Retry
                          </Button>
                        </div>
                      )}
                    </>
                  )}
                  {flow.kind === "activity" && (
                    <>
                      {(flow.activity.snapshot?.delegates.error || flow.activity.snapshot?.delegates.unavailable) && (
                        <div role="alert">Delegate activity unavailable</div>
                      )}
                      {flow.activity.snapshot?.delegates.issues.length ? (
                        <div role="status">Some delegate activity is unavailable</div>
                      ) : null}
                      {flow.activity.snapshot?.delegates.hasMore && (
                        <Button
                          variant="link"
                          disabled={flow.activity.snapshot.delegates.loading}
                          onClick={() => void flow.activity.loadMore()}
                        >
                          More delegates
                        </Button>
                      )}
                    </>
                  )}
                </div>
              );
            const row = flow.row;
            const byId = entriesByPath.get(comicPathKey(flow.path));
            const error = row.panels.some(({ slice }) => byId?.get(slice.entryId)?.sourceTurnId !== undefined);
            return (
              <section
                className={styles.row}
                style={index === rows.length - 1 ? { paddingBottom: tailSpace } : undefined}
                aria-label={error ? "Turn errors" : undefined}
                data-comic-row={flow.id}
                data-comic-session={flow.sessionRef}
                data-comic-path={comicPathKey(flow.path)}
              >
                {row.panels.map(({ slice, composition }) => {
                  const entry = byId?.get(slice.entryId);
                  if (!entry) return null;
                  return (
                    <div
                      className={styles.sourcePanel}
                      key={`${slice.entryId}:${slice.start}`}
                      data-comic-entry={slice.entryId}
                      data-comic-session={flow.sessionRef}
                      data-comic-path={comicPathKey(flow.path)}
                      data-comic-start={slice.start}
                      data-comic-end={slice.end}
                    >
                      {mode === "comic" ? (
                        <ComicPanel
                          entry={entry}
                          slice={slice}
                          composition={composition}
                          scene={scene}
                          expression={expression}
                        />
                      ) : (
                        <ComicPlainTranscript
                          entries={[
                            {
                              ...entry,
                              text: entry.text.slice(slice.start, slice.end),
                              images: slice.end === entry.text.length ? entry.images : undefined,
                            },
                          ]}
                        />
                      )}
                    </div>
                  );
                })}
              </section>
            );
          }}
        />
      </div>
    </div>
  );
}
