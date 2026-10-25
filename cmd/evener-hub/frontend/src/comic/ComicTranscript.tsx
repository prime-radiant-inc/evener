import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { isAtBottom } from "../panes/session/transcript/flow/scrollMetrics";
import type { UseTranscriptResult } from "../panes/session/transcript/useTranscript";
import { olderTranscriptPageKey } from "../stores/threads";
import { Button, type CommittedVirtualListLayout, Select, VirtualList, type VirtualListHandle } from "../widgets";
import { type ComicExpression, ComicPanel, type ComicScene } from "./ComicPanel";
import { ComicPlainTranscript } from "./ComicPlainTranscript";
import styles from "./comic.module.css";
import { type ComicRow, composeComicRows } from "./layout";
import { createComicMeasurer, loadComicFonts } from "./measure";
import type { ComicEntry } from "./projection";
import { type ComicReadingPosition, findComicAnchorRow } from "./readerPosition";
import type { useComicReader } from "./useComicReader";

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
  if (isAtBottom(port)) return { mode: "end", sessionRef, path: [] };
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
        sessionRef,
        path: [],
        entryId: source.dataset.comicEntry ?? "",
        offset: low,
        viewportOffset: point.top - top,
      };
  }
}

export function ComicTranscript({
  entries,
  history,
  reader,
  mode,
}: {
  entries: readonly ComicEntry[];
  history: UseTranscriptResult;
  reader: ReturnType<typeof useComicReader>;
  mode: "comic" | "plain";
}): React.ReactElement {
  const host = useRef<HTMLDivElement>(null);
  const controlsId = useId();
  const list = useRef<VirtualListHandle>(null);
  const committed = useRef<CommittedVirtualListLayout | undefined>(undefined);
  const sessionRef = history.model?.ref ?? reader.position?.sessionRef ?? "";
  const anchor = useRef<ComicReadingPosition>(reader.position ?? { mode: "end", sessionRef, path: [] });
  const restoring = useRef(!!reader.position);
  const commanded = useRef<number | undefined>(undefined);
  const moving = useRef(false);
  const movementStart = useRef<number | undefined>(undefined);
  const [scene, setScene] = useState<ComicScene>("room");
  const [expression, setExpression] = useState<ComicExpression>("neutral");
  const [font, setFont] = useState<"loading" | "ready" | "unavailable">("loading");
  const [geometry, setGeometry] = useState({ width: 0, epoch: 0 });
  const [tailSpace, setTailSpace] = useState(0);
  const [rows, setRows] = useState<readonly ComicRow[]>([]);
  const probe = useRef<ReturnType<typeof createComicMeasurer> | undefined>(undefined);
  const fitsCache = useRef(new Map<string, boolean>());
  const layoutCache = useRef(new Map<string, { rows: readonly ComicRow[]; fitsKeys: readonly string[] }>());
  const lastPage = useRef<string | undefined>(undefined);
  const byId = useMemo(() => new Map(entries.map((entry) => [entry.id, entry])), [entries]);
  const revision = useRef(0);
  const priorEntries = useRef(entries);
  const nearest = useRef<string | undefined>(undefined);
  const latest = useRef({ entries, history, reader, sessionRef, rows });
  latest.current = { entries, history, reader, sessionRef, rows };

  const save = useCallback((position: ComicReadingPosition) => {
    anchor.current = position;
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
      layoutCache.current.clear();
    };
  }, []);

  useLayoutEffect(() => {
    const measurer = probe.current;
    if (!measurer) return;
    const used = new Set<string>();
    const usedLayouts = new Set<string>();
    const fits: Parameters<typeof composeComicRows>[2] = (text, composition, kind) => {
      const key = JSON.stringify([kind, composition, text]);
      used.add(key);
      const cached = fitsCache.current.get(key);
      if (cached !== undefined) return cached;
      const result = measurer.fits(text, composition, kind);
      fitsCache.current.set(key, result);
      return result;
    };
    // Itemless errors remain a separate group, never assigned a fabricated sourceIndex.
    const items = entries.filter((entry) => entry.sourceTurnId === undefined);
    const errors = entries.filter((entry) => entry.sourceTurnId !== undefined);
    const compose = (group: readonly ComicEntry[]): ComicRow[] => {
      const result: ComicRow[] = [];
      for (let index = 0; index < group.length; ) {
        const candidates = group.slice(index, index + 2);
        const key = JSON.stringify([
          geometry,
          candidates.map((entry) => [entry.id, entry.text, entry.kind, entry.streaming, entry.images?.length ?? 0]),
        ]);
        usedLayouts.add(key);
        let cached = layoutCache.current.get(key);
        if (!cached) {
          const fitsKeys = new Set<string>();
          const composed = composeComicRows(candidates, geometry.width, (text, composition, kind) => {
            fitsKeys.add(JSON.stringify([kind, composition, text]));
            return fits(text, composition, kind);
          });
          // Let the accepted composer decide whether this entry takes a partner.
          const selected = composed.filter((row) =>
            row.panels.some((panel) => panel.slice.entryId === candidates[0]?.id),
          );
          cached = { rows: selected, fitsKeys: [...fitsKeys] };
          layoutCache.current.set(key, cached);
        }
        for (const fitKey of cached.fitsKeys) used.add(fitKey);
        result.push(...cached.rows);
        index += cached.rows.some((row) => row.panels.length === 2) ? 2 : 1;
      }
      return result;
    };
    const next = [...compose(items), ...compose(errors)];
    const position = anchor.current;
    if (!moving.current && position.mode === "away" && !entries.some((entry) => entry.id === position.entryId)) {
      const previous = priorEntries.current;
      const index = previous.findIndex((entry) => entry.id === position.entryId);
      if (index >= 0) {
        restoring.current = true;
        const available = new Set(entries.map((entry) => entry.id));
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
    priorEntries.current = entries;
    for (const key of fitsCache.current.keys()) if (!used.has(key)) fitsCache.current.delete(key);
    for (const key of layoutCache.current.keys()) if (!usedLayouts.has(key)) layoutCache.current.delete(key);
    setRows(next);
  }, [entries, geometry]);

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
      const rowIndex = findComicAnchorRow(current.rows, position);
      if (rowIndex === undefined) return;
      if (position.mode === "end") {
        const bottom = Math.max(0, port.scrollHeight - port.clientHeight);
        if (Math.abs(port.scrollTop - bottom) > 1) layout.scrollToOffset(bottom);
        commanded.current = port.scrollTop;
        return;
      }
      const slice = current.rows[rowIndex]?.panels.find((panel) => panel.slice.entryId === position.entryId)?.slice;
      const source = [...port.querySelectorAll<HTMLElement>("[data-comic-entry]")].find(
        (node) => node.dataset.comicEntry === position.entryId && Number(node.dataset.comicStart) === slice?.start,
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
    [reader, rows, captureMovement],
  );

  useLayoutEffect(() => {
    const port = list.current?.getScrollElement();
    if (!port) return;
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
      latest.current.history.cancelOlder();
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        if (revision.current !== requestedRevision || latest.current.reader !== reader) return;
        if (list.current?.isLayoutCurrent()) captureMovement(port);
      });
    };
    const complete = () => {
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
      cancel(event);
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
      if (!pointer.cancelled && moving.current) complete();
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
      if (port.contains(document.getSelection()?.anchorNode ?? null)) cancel();
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
    document.addEventListener("pointerup", pointerEnd);
    document.addEventListener("pointercancel", pointerEnd);
    document.addEventListener("selectionchange", selection);
    return () => {
      cancelAnimationFrame(frame);
      port.removeEventListener("wheel", wheel);
      port.removeEventListener("touchstart", touchStart);
      port.removeEventListener("pointerdown", pointerDown);
      port.removeEventListener("keydown", key);
      port.removeEventListener("scroll", scroll);
      document.removeEventListener("pointerup", pointerEnd);
      document.removeEventListener("pointercancel", pointerEnd);
      document.removeEventListener("selectionchange", selection);
    };
  }, [captureMovement, reader]);

  useEffect(() => {
    if (
      !restoring.current ||
      anchor.current.mode !== "away" ||
      !rows.length ||
      findComicAnchorRow(rows, anchor.current) !== undefined
    )
      return;
    if (history.hasOlder) {
      const page = olderTranscriptPageKey(sessionRef) ?? entries.map((entry) => entry.id).join("\n");
      if (!history.loadingOlder && !history.olderError && lastPage.current !== page) {
        lastPage.current = page;
        history.loadOlderReportingError();
      }
    } else {
      const first =
        rows.flatMap((row) => row.panels).find((panel) => panel.slice.entryId === nearest.current)?.slice ??
        rows[0]?.panels[0]?.slice;
      if (first) save({ ...anchor.current, entryId: first.entryId, offset: first.start });
      restoring.current = false;
      if (committed.current) restore(committed.current);
    }
  }, [entries, rows, history, save, restore, sessionRef]);

  const jump = () => {
    revision.current += 1;
    restoring.current = false;
    moving.current = false;
    movementStart.current = undefined;
    history.cancelOlder();
    setTailSpace(0);
    save({ mode: "end", sessionRef, path: [] });
    if (committed.current) restore(committed.current);
  };
  return (
    <div className={styles.reader} data-comic-mode={mode} data-comic-font={font}>
      <div className={styles.controls}>
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
        {history.hasOlder && (
          <Button variant="link" disabled={history.loadingOlder} onClick={history.loadOlderReportingError}>
            Older messages
          </Button>
        )}
        <Button variant="link" onClick={jump}>
          Jump to latest
        </Button>
      </div>
      {history.olderError && (
        <div role="alert">
          {history.olderError}{" "}
          <Button variant="link" onClick={history.loadOlderReportingError}>
            Retry
          </Button>
        </div>
      )}
      <div ref={host} className={styles.scroll} data-comic-scroll="">
        <VirtualList
          ref={list}
          count={rows.length}
          dynamic
          getItemKey={(index) => rows[index]?.id ?? index}
          anchorToEnd={anchor.current.mode === "end"}
          estimateSize={() => (mode === "comic" ? 380 : 180)}
          onLayout={restore}
          renderRow={(index) => {
            const row = rows[index];
            if (!row) return null;
            const error = row.panels.some(({ slice }) => byId.get(slice.entryId)?.sourceTurnId !== undefined);
            return (
              <section
                className={styles.row}
                style={index === rows.length - 1 ? { paddingBottom: tailSpace } : undefined}
                aria-label={error ? "Turn errors" : undefined}
                data-comic-row={row.id}
              >
                {row.panels.map(({ slice, composition }) => {
                  const entry = byId.get(slice.entryId);
                  if (!entry) return null;
                  return (
                    <div
                      className={styles.sourcePanel}
                      key={`${slice.entryId}:${slice.start}`}
                      data-comic-entry={slice.entryId}
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
