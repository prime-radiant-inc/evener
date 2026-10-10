// Job output uses its owning session's byte pages, the workspace pane's
// retained reader and the same virtual list as session transcripts.
import { jobCommandLabel, jobStatusDisplay } from "@evener/appwire-client";
import type { Virtualizer } from "@tanstack/react-virtual";
import { useEffect, useLayoutEffect, useMemo, useRef, useSyncExternalStore } from "react";
import { useStore } from "zustand";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { workspaceStore } from "../../shell/workspace";
import { Button, EmptyState, PaneScaffold, VirtualList, type VirtualListHandle } from "../../widgets";
import { AnsiLineContent } from "../../widgets/codeblock/ansiLine";
import { requireClass } from "../../widgets/internal/requireClass";
import { isAtBottom } from "../session/transcript/flow/scrollMetrics";
import { type JobLogReader, type JobLogScrollCapture, retainedJobLogReader } from "./jobLogReader";
import { type JobLogRow, type JobLogWindows, jobLogRows, jobLogSourceBytes } from "./jobLogWindow";
import styles from "./transcript.module.css";

const CLASS = {
  body: requireClass(styles.body, "transcript.module.css", "body"),
  joblogCommand: requireClass(styles.joblogCommand, "transcript.module.css", "joblogCommand"),
  joblog: requireClass(styles.joblog, "transcript.module.css", "joblog"),
  joblogNote: requireClass(styles.joblogNote, "transcript.module.css", "joblogNote"),
};

type JobLogDisplayRow =
  | { kind: "output"; key: string; row: JobLogRow }
  | { kind: "unloaded" | "pruned"; key: string; startBytes: number; endBytes: number };

function outputRowContainsByte(item: JobLogDisplayRow, byteOffset: number): boolean {
  return item.kind === "output" && item.row.offsetBytes <= byteOffset && item.row.endBytes > byteOffset;
}

function glyphRect(element: HTMLElement, textOffset: number): DOMRect {
  const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
  let node = walker.nextNode();
  while (node) {
    const length = node.textContent?.length ?? 0;
    if (textOffset < length) {
      const range = document.createRange();
      range.setStart(node, textOffset);
      range.setEnd(node, textOffset + 1);
      return range.getBoundingClientRect();
    }
    textOffset -= length;
    node = walker.nextNode();
  }
  return element.getBoundingClientRect();
}

function sourceGlyphRect(element: HTMLElement, row: JobLogRow, byteOffset: number): DOMRect {
  let index = 0;
  for (let next = 0; next < row.textByteOffsets.length; next++) {
    const offset = row.textByteOffsets[next];
    if (offset === undefined || row.offsetBytes + offset > byteOffset) break;
    if (next === 0 || offset !== row.textByteOffsets[next - 1]) index = next;
  }
  return glyphRect(element, index);
}

function visibleGlyphCapture(
  element: HTMLElement,
  row: JobLogRow,
  windows: JobLogWindows,
  viewport: DOMRect,
  originTop: number,
  following: boolean,
) {
  let start = 0;
  let end = row.textByteOffsets.length;
  while (start < end) {
    const middle = Math.floor((start + end) / 2);
    if (glyphRect(element, middle).bottom <= viewport.top) start = middle + 1;
    else end = middle;
  }
  let index = Math.min(start, Math.max(0, row.textByteOffsets.length - 1));
  const source = [windows.older, windows.live].find((range) => range?.offsetBytes === row.offsetBytes);
  if (source && source.offsetBytes > 0) {
    // At most three leading continuation bytes can change when an earlier page repairs UTF8.
    let partialBytes = 0;
    while (partialBytes < 3) {
      const byte = source.bytes[partialBytes];
      if (byte === undefined || byte < 0x80 || byte > 0xbf) break;
      partialBytes++;
    }
    while (index + 1 < row.textByteOffsets.length && (row.textByteOffsets[index] ?? 0) < partialBytes) index++;
  }
  return {
    byteOffset: row.offsetBytes + (row.textByteOffsets[index] ?? 0),
    pixelOffset: glyphRect(element, index).top - originTop,
    following,
  };
}

function jobLogDisplayRows(windows: JobLogWindows, jobId: string): JobLogDisplayRow[] {
  const output = jobLogRows(windows);
  const rows: JobLogDisplayRow[] = [];
  const missing = (startBytes: number, endBytes: number) => {
    const split = Math.min(endBytes, Math.max(startBytes, windows.retainedStartBytes));
    if (startBytes < split)
      rows.push({ kind: "pruned", key: `${jobId}:pruned:${startBytes}`, startBytes, endBytes: split });
    if (split < endBytes)
      rows.push({ kind: "unloaded", key: `${jobId}:unloaded:${split}`, startBytes: split, endBytes });
  };
  const ranges = [windows.older, windows.live]
    .filter((window) => window !== null)
    .sort((a, b) => a.offsetBytes - b.offsetBytes);
  let frontier = 0;
  for (const range of ranges) {
    if (range.offsetBytes > frontier) missing(frontier, range.offsetBytes);
    const end = range.offsetBytes + range.bytes.length;
    for (const row of output) {
      if (row.offsetBytes < Math.max(frontier, range.offsetBytes) || row.offsetBytes >= end) continue;
      rows.push({ kind: "output", key: `${jobId}:output:${row.offsetBytes}`, row });
    }
    frontier = Math.max(frontier, end);
  }
  if (frontier < windows.totalBytes) missing(frontier, windows.totalBytes);
  return rows;
}

export function JobLog({ jobRef, parentRef, paneId }: { jobRef: string; parentRef?: string; paneId?: string }) {
  const pane = useStore(workspaceStore, (state) =>
    state.panes.find(
      (record) =>
        record.id === paneId && record.type === "transcript" && (record.params as { ref?: string }).ref === jobRef,
    ),
  );
  const jobId = jobRef.slice("job:".length);
  if (parentRef === undefined)
    return (
      <PaneScaffold title={jobId} paneId={paneId}>
        <EmptyState title="Job transcript unavailable" hint="the owning session is unknown" />
      </PaneScaffold>
    );
  if (!pane)
    return (
      <PaneScaffold title={jobId} paneId={paneId}>
        <EmptyState title="Loading job output…" />
      </PaneScaffold>
    );
  const lifetime = conversationPaneLifetime(pane);
  const reader = retainedJobLogReader(lifetime, parentRef, jobId);
  return (
    <JobLogBody
      key={JSON.stringify([lifetime.serial, parentRef, jobId])}
      reader={reader}
      jobId={jobId}
      paneId={paneId}
    />
  );
}

function JobLogBody({ reader, jobId, paneId }: { reader: JobLogReader; jobId: string; paneId?: string }) {
  const snapshot = useSyncExternalStore(reader.subscribe, reader.getSnapshot);
  const rows = useMemo(() => jobLogDisplayRows(snapshot.windows, jobId), [snapshot.windows, jobId]);
  const gaps = useMemo(() => rows.flatMap((row, index) => (row.kind === "unloaded" ? [{ row, index }] : [])), [rows]);
  const list = useRef<VirtualListHandle>(null);
  const virtualizer = useRef<Virtualizer<HTMLDivElement, HTMLDivElement> | null>(null);
  const previousRows = useRef<JobLogDisplayRow[] | null>(null);
  const restore = useRef<JobLogScrollCapture | null>(
    snapshot.scrollCapture ?? { byteOffset: 0, pixelOffset: 0, following: true },
  );
  const queued = useRef(false);
  const update = useRef<() => void>(() => {});
  const mounted = useRef(false);

  const restorationIndex = (capture: JobLogScrollCapture) => {
    let index = capture.following
      ? rows.length - 1
      : rows.findIndex((item) => outputRowContainsByte(item, capture.byteOffset));
    if (index < 0)
      index = rows.findIndex((item) => item.kind === "output" && item.row.offsetBytes >= capture.byteOffset);
    return index < 0 ? rows.length - 1 : index;
  };

  const inspect = () => {
    const scroller = list.current?.getScrollElement();
    if (!scroller || !mounted.current) return;
    // Byte-count and status notes can move the scroll port inside the pane.
    const originTop = scroller.closest(`.${CLASS.body}`)?.getBoundingClientRect().top ?? 0;
    const pending = restore.current;
    if (pending !== null) {
      const item = rows[restorationIndex(pending)];
      const element = scroller.querySelector<HTMLElement>(
        `[data-index="${restorationIndex(pending)}"] [data-joblog-kind]`,
      );
      const instance = virtualizer.current;
      if (!instance) return;
      if (!element) {
        list.current?.scrollToIndex(restorationIndex(pending), { align: pending.following ? "end" : "start" });
        return;
      }
      const wrapper = element.parentElement;
      const measured = instance.getVirtualItems().find((row) => row.index === restorationIndex(pending));
      if (!pending.following && wrapper && measured && measured.size !== wrapper.offsetHeight) {
        // Moving within an estimated row can unmount it before ResizeObserver measures it.
        instance.resizeItem(measured.index, wrapper.offsetHeight);
        return;
      }
      const rect =
        item?.kind === "output"
          ? sourceGlyphRect(element, item.row, pending.byteOffset)
          : element.getBoundingClientRect();
      if (!pending.following) instance.scrollToOffset(scroller.scrollTop + rect.top - originTop - pending.pixelOffset);
      restore.current = null;
    }
    const viewport = scroller.getBoundingClientRect();
    const elements = Array.from(scroller.querySelectorAll<HTMLElement>("[data-joblog-kind]"));
    const visible = elements.filter((element) => {
      const rect = element.getBoundingClientRect();
      return rect.bottom > viewport.top && rect.top < viewport.bottom;
    });
    const first = visible.find((element) => element.dataset.joblogKind === "output");
    const following = isAtBottom(scroller, 4);
    const previous = reader.getSnapshot().scrollCapture;
    const byte = first ? Number(first.dataset.sourceStart) : undefined;
    const firstRow = rows.find((item) => item.kind === "output" && item.row.offsetBytes === byte);
    const capture =
      first && firstRow?.kind === "output"
        ? visibleGlyphCapture(first, firstRow.row, snapshot.windows, viewport, originTop, following)
        : null;
    const marker = elements.find((element) => {
      const rect = element.getBoundingClientRect();
      return element.dataset.joblogKind === "unloaded" && rect.bottom >= viewport.top && rect.top <= viewport.bottom;
    });
    const gapIndex =
      gaps.find(
        ({ row }) =>
          row.startBytes === Number(marker?.dataset.sourceStart) ||
          (previous !== null &&
            !(previous.following && following) &&
            byte !== undefined &&
            ((previous.byteOffset < row.startBytes && byte >= row.endBytes) ||
              (previous.byteOffset >= row.endBytes && byte < row.startBytes))),
      )?.index ?? -1;
    const gap = rows[gapIndex];
    if (gap && gap.kind === "unloaded") {
      const direction = (previous?.byteOffset ?? byte ?? 0) >= gap.endBytes ? "backward" : "forward";
      if (capture && byte !== undefined && (direction === "backward" ? byte >= gap.endBytes : byte < gap.startBytes))
        reader.capture({ ...capture, following: false });
      else {
        const adjacent = rows[gapIndex + (direction === "backward" ? 1 : -1)];
        if (adjacent?.kind === "output") {
          const capture = {
            byteOffset: adjacent.row.offsetBytes,
            pixelOffset: viewport.top - originTop,
            following: false,
          };
          restore.current = capture;
          reader.capture(capture);
          list.current?.scrollToIndex(restorationIndex(capture), { align: "start" });
        }
      }
      reader.demand(
        {
          direction,
          boundaryBytes: direction === "backward" ? gap.endBytes : gap.startBytes,
          limitBytes: gap.endBytes,
        },
        direction === "backward" ? "start" : "end",
      );
      // A missing interval is a paging boundary, not a shortcut to its far side.
      if (restore.current === null)
        list.current?.scrollToIndex(gapIndex, { align: direction === "forward" ? "end" : "start" });
    } else if (capture) {
      reader.capture(capture);
    }
  };
  update.current = inspect;
  const onListChange = (instance: Virtualizer<HTMLDivElement, HTMLDivElement>) => {
    virtualizer.current = instance;
    if (queued.current) return;
    queued.current = true;
    queueMicrotask(() => {
      queued.current = false;
      update.current();
    });
  };

  useEffect(() => {
    mounted.current = true;
    const visibility = () => reader.setMounted(document.visibilityState !== "hidden");
    visibility();
    document.addEventListener("visibilitychange", visibility);
    return () => {
      mounted.current = false;
      document.removeEventListener("visibilitychange", visibility);
      reader.setMounted(false);
    };
  }, [reader]);

  useLayoutEffect(() => {
    const scroller = list.current?.getScrollElement();
    if (!scroller || rows.length === 0) return;
    const capture = reader.getSnapshot().scrollCapture;
    if (previousRows.current !== null && capture && !capture.following) {
      const old = previousRows.current.find((item) => outputRowContainsByte(item, capture.byteOffset));
      if (old && !rows.some((item) => item.key === old.key)) restore.current = capture;
    }
    previousRows.current = rows;
    const pending = restore.current;
    if (pending) {
      list.current?.scrollToIndex(restorationIndex(pending), { align: pending.following ? "end" : "start" });
    }
    scroller.addEventListener("scroll", inspect);
    inspect();
    return () => scroller.removeEventListener("scroll", inspect);
  });

  const job = snapshot.job;
  const command = job ? jobCommandLabel(job) : undefined;
  const title = job?.description.trim() || command?.split("\n")[0] || jobId;
  const { older, live } = snapshot.windows;
  const sourceBytes = jobLogSourceBytes(snapshot.windows);
  const newline = (end: number) =>
    [older, live].some(
      (window) =>
        window !== null &&
        end > window.offsetBytes &&
        end <= window.offsetBytes + window.bytes.length &&
        window.bytes[end - window.offsetBytes - 1] === 10,
    );
  return (
    <PaneScaffold
      title={title}
      paneId={paneId}
      actions={
        <Button variant="quiet" size="sm" onClick={() => reader.refresh()}>
          Refresh
        </Button>
      }
    >
      <div className={CLASS.body}>
        {job && (
          <span className={CLASS.joblogNote} data-testid="joblog-status">
            {jobStatusDisplay(job.status, job.reason)}
            {job.exitCode !== undefined ? ` · Exit code ${job.exitCode}` : null}
          </span>
        )}
        {command !== undefined && (
          <code className={CLASS.joblogCommand} data-testid="joblog-command">
            {command}
          </code>
        )}
        {sourceBytes > 0 && sourceBytes < snapshot.windows.totalBytes && (
          <span className={CLASS.joblogNote}>
            Loaded {sourceBytes.toLocaleString()} of {snapshot.windows.totalBytes.toLocaleString()} output bytes
          </span>
        )}
        {snapshot.outputError && (
          <span className={CLASS.joblogNote} role="status">
            {snapshot.outputError}
          </span>
        )}
        {live === null && older === null && !snapshot.outputError && <EmptyState title="Loading job output…" />}
        {live !== null && snapshot.windows.totalBytes === 0 && (
          <EmptyState title="No output yet" hint="This job hasn't written anything." />
        )}
        <div
          className={CLASS.joblog}
          data-testid="joblog-content"
          data-joblog-older-bytes={older?.bytes.length ?? 0}
          data-joblog-live-bytes={live?.bytes.length ?? 0}
          data-joblog-source-bytes={sourceBytes}
        >
          <VirtualList
            count={rows.length}
            dynamic
            estimateSize={() => 20}
            anchorToEnd
            ref={list}
            onChange={onListChange}
            getItemKey={(index) => {
              const item = rows[index];
              if (!item) throw new RangeError("job output row is outside the rendered range");
              return item.key;
            }}
            renderRow={(index) => {
              const item = rows[index];
              if (!item) return null;
              return item.kind === "output" ? (
                <div
                  data-joblog-kind="output"
                  data-source-start={item.row.offsetBytes}
                  data-source-end={item.row.endBytes}
                >
                  <AnsiLineContent line={item.row.line} />
                  {newline(item.row.endBytes) ? "\n" : null}
                </div>
              ) : (
                <div
                  className={CLASS.joblogNote}
                  data-joblog-kind={item.kind}
                  data-source-start={item.startBytes}
                  data-source-end={item.endBytes}
                >
                  {item.kind === "unloaded" ? "Output not loaded" : "Output no longer retained"}
                </div>
              );
            }}
          />
        </div>
      </div>
    </PaneScaffold>
  );
}
