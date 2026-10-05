// Job output uses its owning session's byte pages, the workspace pane's
// retained reader and the same virtual list as session transcripts.
import { jobCommandLabel, jobStatusDisplay } from "@evener/appwire-client";
import { useEffect, useLayoutEffect, useMemo, useRef, useSyncExternalStore } from "react";
import { useStore } from "zustand";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { workspaceStore } from "../../shell/workspace";
import { Button, EmptyState, PaneScaffold, VirtualList, type VirtualListHandle } from "../../widgets";
import { AnsiLineContent } from "../../widgets/codeblock/ansiLine";
import { requireClass } from "../../widgets/internal/requireClass";
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
  const list = useRef<VirtualListHandle>(null);
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
      : rows.findIndex(
          (item) =>
            item.kind === "output" &&
            item.row.offsetBytes <= capture.byteOffset &&
            item.row.endBytes > capture.byteOffset,
        );
    if (index < 0)
      index = rows.findIndex((item) => item.kind === "output" && item.row.offsetBytes >= capture.byteOffset);
    return index < 0 ? rows.length - 1 : index;
  };

  const inspect = () => {
    const scroller = list.current?.getScrollElement();
    if (!scroller || !mounted.current) return;
    const pending = restore.current;
    if (pending !== null) {
      const element = scroller.querySelector<HTMLElement>(
        `[data-index="${restorationIndex(pending)}"] [data-joblog-kind]`,
      );
      if (!element) return;
      if (!pending.following)
        scroller.scrollTop +=
          element.getBoundingClientRect().top - scroller.getBoundingClientRect().top - pending.pixelOffset;
      restore.current = null;
    }
    const viewport = scroller.getBoundingClientRect();
    const elements = Array.from(scroller.querySelectorAll<HTMLElement>("[data-joblog-kind]"));
    const visible = elements.filter((element) => {
      const rect = element.getBoundingClientRect();
      return rect.bottom > viewport.top && rect.top < viewport.bottom;
    });
    const first = visible.find((element) => element.dataset.joblogKind === "output");
    const following = scroller.scrollHeight - scroller.clientHeight - scroller.scrollTop <= 4;
    const previous = reader.getSnapshot().scrollCapture;
    const byte = first ? Number(first.dataset.sourceStart) : undefined;
    const marker = elements.find((element) => {
      const rect = element.getBoundingClientRect();
      return element.dataset.joblogKind === "unloaded" && rect.bottom >= viewport.top && rect.top <= viewport.bottom;
    });
    const gapIndex = rows.findIndex(
      (item) =>
        item.kind === "unloaded" &&
        (item.startBytes === Number(marker?.dataset.sourceStart) ||
          (previous !== null &&
            !(previous.following && following) &&
            byte !== undefined &&
            ((previous.byteOffset < item.startBytes && byte >= item.endBytes) ||
              (previous.byteOffset >= item.endBytes && byte < item.startBytes)))),
    );
    const gap = rows[gapIndex];
    if (gap && gap.kind === "unloaded") {
      const direction = (previous?.byteOffset ?? byte ?? 0) >= gap.endBytes ? "backward" : "forward";
      if (first && byte !== undefined && (direction === "backward" ? byte >= gap.endBytes : byte < gap.startBytes))
        reader.capture({
          byteOffset: byte,
          pixelOffset: first.getBoundingClientRect().top - viewport.top,
          following: false,
        });
      reader.demand(
        {
          direction,
          boundaryBytes: direction === "backward" ? gap.endBytes : gap.startBytes,
          limitBytes: gap.endBytes,
        },
        direction === "backward" ? "start" : "end",
      );
      // A missing interval is a paging boundary, not a shortcut to its far side.
      list.current?.scrollToIndex(gapIndex, { align: direction === "forward" ? "end" : "start" });
    } else if (first) {
      reader.capture({
        byteOffset: Number(first.dataset.sourceStart),
        pixelOffset: first.getBoundingClientRect().top - viewport.top,
        following,
      });
    }
  };
  update.current = inspect;
  const onListChange = () => {
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
      const old = previousRows.current.find(
        (item) =>
          item.kind === "output" &&
          item.row.offsetBytes <= capture.byteOffset &&
          item.row.endBytes > capture.byteOffset,
      );
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
