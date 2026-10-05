import {
  type JobActivityJob,
  jobOutputPrunedBounds,
  parseActivityJob,
  parseJobOutputPage,
} from "@evener/appwire-client";
import type { PaneLifetime } from "../../shell/paneLifetime";
import { connectionStore } from "../../stores/connection";
import { threadsStore } from "../../stores/threads";
import { retainedTranscriptReadView, type TranscriptReadView } from "../session/transcript/transcriptReadView";
import {
  applyJobLogPage,
  emptyJobLogWindows,
  JOB_LOG_PAGE_BYTES,
  type JobLogDemand,
  type JobLogWindows,
  reconcileJobLogBounds,
  retainJobLogReadingWindow,
  selectJobLogDemand,
} from "./jobLogWindow";

export type JobLogScrollCapture = { byteOffset: number; pixelOffset: number; following: boolean };
export type JobLogReaderSnapshot = {
  windows: JobLogWindows;
  job: JobActivityJob | null;
  outputError: string | null;
  metadataError: string | null;
  pendingDemand: JobLogDemand | null;
  drainPending: boolean;
  scrollCapture: JobLogScrollCapture | null;
};

const READ_INTERVAL_MS = 1000;
const messageOf = (error: unknown) => (error instanceof Error ? error.message : String(error));

export class JobLogReader {
  private snapshot: JobLogReaderSnapshot = {
    windows: emptyJobLogWindows(),
    job: null,
    outputError: null,
    metadataError: null,
    pendingDemand: null,
    drainPending: false,
    scrollCapture: null,
  };
  private readonly listeners = new Set<() => void>();
  private readonly unsubscribeView: () => void;
  private readonly unsubscribeConnection: () => void;
  private mounted = false;
  private disposed = false;
  private generation = 0;
  private terminalObservation = 0;
  private keep: "start" | "end" = "start";
  private outputInFlight: Promise<void> | null = null;
  private metadataInFlight = false;
  private historyServedSinceLive = false;
  private nextReadAt = 0;
  private liveDueAt = 0;
  private metadataDueAt = 0;
  private timeout: ReturnType<typeof setTimeout> | undefined;

  constructor(
    private readonly lifetime: PaneLifetime,
    private readonly ownerRef: string,
    private readonly jobId: string,
    private readonly view: TranscriptReadView,
  ) {
    // Replacement changes the reader, not the pane's one transport slot.
    for (const previous of lifetime.jobOutputReads.values()) {
      if (previous.outputInFlight === null) continue;
      this.outputInFlight = previous.outputInFlight.then(() => {
        this.outputInFlight = null;
        this.pump();
      });
    }
    this.unsubscribeView = view.subscribe(() => {
      this.generation++;
      if (!view.alive) this.dispose();
      else this.pump();
    });
    this.unsubscribeConnection = connectionStore.subscribe((next, previous) => {
      if (next.client === previous.client && (next.state === "ready") === (previous.state === "ready")) return;
      this.generation++;
      if (next.state === "ready") {
        this.liveDueAt = Date.now();
        this.metadataDueAt = Date.now();
      }
      this.pump();
    });
  }

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  getSnapshot = () => this.snapshot;

  private publish(update: Partial<JobLogReaderSnapshot>) {
    this.snapshot = { ...this.snapshot, ...update };
    for (const listener of this.listeners) listener();
  }

  private readable() {
    const connection = connectionStore.getState();
    return (
      !this.disposed &&
      this.mounted &&
      this.lifetime.alive &&
      this.view.alive &&
      this.view.readable &&
      connection.state === "ready" &&
      connection.client?.state === "ready"
    );
  }

  setMounted(mounted: boolean) {
    if (this.disposed || mounted === this.mounted) return;
    this.mounted = mounted;
    this.generation++;
    this.view.setReadable(mounted);
    this.pump();
  }

  demand(demand: JobLogDemand, keep: "start" | "end") {
    if (this.disposed) return;
    this.keep = keep;
    const previous = this.snapshot.pendingDemand;
    if (
      previous?.direction === demand.direction &&
      previous.boundaryBytes === demand.boundaryBytes &&
      previous.limitBytes === demand.limitBytes
    )
      return;
    this.publish({ pendingDemand: demand });
    this.pump();
  }

  refresh() {
    if (this.disposed) return;
    this.liveDueAt = Date.now();
    this.metadataDueAt = Date.now();
    if (this.snapshot.job?.terminal) {
      this.terminalObservation++;
      this.publish({ drainPending: true });
    }
    this.pump();
  }

  capture(value: JobLogScrollCapture) {
    if (this.disposed) return;
    const previous = this.snapshot.scrollCapture;
    const windows = value.following
      ? this.snapshot.windows
      : retainJobLogReadingWindow(this.snapshot.windows, value.byteOffset);
    if (
      windows === this.snapshot.windows &&
      previous?.byteOffset === value.byteOffset &&
      previous.pixelOffset === value.pixelOffset &&
      previous.following === value.following
    )
      return;
    this.publish({ windows, scrollCapture: value });
  }

  dispose() {
    if (this.disposed) return;
    this.disposed = true;
    this.generation++;
    this.stopTimeout();
    this.unsubscribeView();
    this.unsubscribeConnection();
    this.view.dispose();
    this.listeners.clear();
  }

  private stopTimeout() {
    if (this.timeout === undefined) return;
    clearTimeout(this.timeout);
    this.timeout = undefined;
  }

  private pump() {
    this.stopTimeout();
    if (!this.readable()) return;
    const now = Date.now();
    if (!this.metadataInFlight && now >= this.metadataDueAt) this.readMetadata();
    if (!this.outputInFlight && now >= this.nextReadAt) {
      const liveDue = now >= this.liveDueAt || this.snapshot.drainPending;
      const pending = this.snapshot.pendingDemand;
      const readHistory = pending !== null && !(liveDue && this.historyServedSinceLive);
      const selection = readHistory && pending !== null ? selectJobLogDemand(this.snapshot.windows, pending) : null;
      if (readHistory && selection === null) this.publish({ pendingDemand: null });
      if (selection !== null || liveDue) this.readOutput(selection, selection !== null ? pending : null);
    }
    if (!this.readable()) return;
    const outputDue = this.outputInFlight
      ? Number.POSITIVE_INFINITY
      : Math.max(
          this.nextReadAt,
          this.snapshot.pendingDemand !== null || this.snapshot.drainPending ? now : this.liveDueAt,
        );
    const metadataDue = this.metadataInFlight ? Number.POSITIVE_INFINITY : this.metadataDueAt;
    const next = Math.min(outputDue, metadataDue);
    if (Number.isFinite(next))
      this.timeout = setTimeout(
        () => {
          this.timeout = undefined;
          this.pump();
        },
        Math.max(0, next - Date.now()),
      );
  }

  private readMetadata() {
    this.metadataInFlight = true;
    const generation = this.generation;
    const current = () => this.readable() && this.generation === generation;
    void threadsStore
      .getState()
      .jobGet(this.ownerRef, this.jobId, current)
      .then((data) => {
        if (!current()) return;
        const job = parseActivityJob(data);
        if (job === null || job.jobId !== this.jobId || job.ownerRef !== this.ownerRef)
          throw new Error("malformed job metadata");
        let drainPending = this.snapshot.drainPending;
        if (job.terminal && !this.snapshot.job?.terminal) {
          this.terminalObservation++;
          drainPending = true;
          this.liveDueAt = Date.now();
        }
        this.metadataDueAt = job.terminal ? Number.POSITIVE_INFINITY : Date.now() + READ_INTERVAL_MS;
        this.publish({ job, metadataError: null, drainPending });
      })
      .catch((error: unknown) => {
        if (!current()) return;
        this.metadataDueAt = Date.now() + READ_INTERVAL_MS;
        this.publish({ metadataError: messageOf(error) });
      })
      .finally(() => {
        this.metadataInFlight = false;
        this.pump();
      });
  }

  private readOutput(selection: { beforeBytes: number; maxBytes: number } | null, demand: JobLogDemand | null) {
    const generation = this.generation;
    const observation = this.terminalObservation;
    const kind = selection === null ? "live" : "older";
    const keep = kind === "live" ? "end" : this.keep;
    const current = () => this.readable() && this.generation === generation;
    this.outputInFlight = threadsStore
      .getState()
      .jobOutput(this.ownerRef, this.jobId, selection?.beforeBytes, selection?.maxBytes ?? JOB_LOG_PAGE_BYTES, current)
      .then((data) => {
        if (!current()) return;
        const page = parseJobOutputPage(data);
        if (page === null) throw new Error("malformed output payload");
        if (
          selection !== null &&
          (page.offsetBytes + page.bytesReturned !== selection.beforeBytes ||
            page.bytesReturned > selection.maxBytes ||
            (page.bytesReturned === 0 && selection.beforeBytes > page.retainedStartBytes))
        )
          throw new Error("output page does not match the requested boundary");
        const windows = applyJobLogPage(this.snapshot.windows, page, kind, keep);
        let pendingDemand = this.snapshot.pendingDemand;
        let drainPending = this.snapshot.drainPending;
        if (kind === "live") {
          this.historyServedSinceLive = false;
          if (
            drainPending &&
            observation === this.terminalObservation &&
            page.offsetBytes + page.bytesReturned === page.totalBytes
          )
            drainPending = false;
          this.liveDueAt =
            this.snapshot.job?.terminal && !drainPending ? Number.POSITIVE_INFINITY : Date.now() + READ_INTERVAL_MS;
        } else {
          this.historyServedSinceLive = Date.now() >= this.liveDueAt || drainPending;
          if (pendingDemand === demand && demand !== null) {
            const end = page.offsetBytes + page.bytesReturned;
            const visible = windows.older?.rows.some((row) => row.endBytes > page.offsetBytes && row.offsetBytes < end);
            // A control-only page advances the byte frontier until the same
            // visible demand can be fulfilled. Ordinary pages finish one demand.
            pendingDemand =
              visible || page.bytesReturned === 0
                ? null
                : {
                    ...demand,
                    boundaryBytes: demand.direction === "backward" ? page.offsetBytes : end,
                  };
          }
        }
        this.publish({ windows, pendingDemand, drainPending, outputError: null });
      })
      .catch((error: unknown) => {
        if (!current()) return;
        this.nextReadAt = Date.now() + READ_INTERVAL_MS;
        const bounds = jobOutputPrunedBounds(error);
        let windows = this.snapshot.windows;
        let outputError = messageOf(error);
        if (bounds !== null) {
          try {
            windows = reconcileJobLogBounds(windows, bounds.retainedStartBytes, bounds.totalBytes);
            outputError = "";
          } catch (inconsistent) {
            outputError = messageOf(inconsistent);
          }
        }
        this.publish({ windows, outputError: outputError || null });
      })
      .finally(() => {
        // Obsolete requests keep this slot until settlement, since the transport
        // has no cancellation and a remount must not start a parallel output read.
        this.outputInFlight = null;
        this.pump();
      });
  }
}

export function retainedJobLogReader(lifetime: PaneLifetime, ownerRef: string, jobId: string): JobLogReader {
  const key = JSON.stringify([ownerRef, jobId]);
  const existing = lifetime.jobOutputReads.get(key);
  if (existing) return existing;
  for (const reader of lifetime.jobOutputReads.values()) reader.dispose();
  const reader = new JobLogReader(
    lifetime,
    ownerRef,
    jobId,
    retainedTranscriptReadView(lifetime, `job:${jobId}`, "transcript"),
  );
  lifetime.jobOutputReads.clear();
  if (lifetime.alive) lifetime.jobOutputReads.set(key, reader);
  else reader.dispose();
  return reader;
}
