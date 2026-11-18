import {
  errorText,
  type JobActivityJob,
  jobOutputPrunedBounds,
  jobStatusUnconfirmed,
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
const messageOf = (error: unknown) => errorText(error);

// livePollingStopped reports whether this reader has nothing left to wait for.
//
// A retained row whose nonterminal status is unconfirmed is the case that
// matters: its journal stopped at damage, so the status describes the intact
// prefix and not the job. Polling such a row at the live rate asks the same
// unanswerable question once a second, forever, and shows a reader a finished
// job that looks live. The reader stops scheduling and stays useful: the
// recovered command and whatever output survived remain readable, output
// paging still works, refresh() re-reads on demand, and a reconnect read still
// happens, because those are the ways a person establishes what happened.
//
// A terminal row is confirmed and already stops. A healthy live row carries no
// diagnostics and keeps the normal cadence.
function livePollingStopped(job: JobActivityJob | null): boolean {
  if (job === null) return false;
  return job.terminal || jobStatusUnconfirmed(job);
}

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
  /** A tail read someone asked for explicitly — refresh(), a reconnect — that
   * has not taken the output slot yet. Unconfirmed metadata stops RECURRING
   * polls, but the one-shot read a person asked for still happens; without this
   * flag an older-page request holding the slot would outlive the refresh, and
   * the reconcile that follows the metadata would erase it. */
  private explicitTailOwed = false;
  /** Whether a LIVE tail read has settled at least once. Until it has, the
   * reader's initial load is still owed, so nothing about it counts as
   * recurring. A replacement reader inherits an occupied output slot from the
   * reader it replaced, and its metadata can report an unconfirmed row before it
   * has ever sent an output request; parking the deadline then leaves the pane
   * on its loading indicator, because releasing the inherited slot finds no
   * work due. Recurring reads may only be suppressed after an attempt to read
   * has actually finished. */
  private liveAttempted = false;
  private timeout: ReturnType<typeof setTimeout> | undefined;

  /** Whether a live tail read is owed regardless of the recurring cadence: one a
   * person asked for (refresh, reconnect), or the initial load that has not
   * settled yet. An unconfirmed row must never park owed work — that is what
   * would leave a reader waiting on evidence only that read could produce. */
  private tailOwed(): boolean {
    return this.explicitTailOwed || !this.liveAttempted;
  }

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
        // A reconnect is a fresh chance to establish the row, so a job whose
        // output schedule settled because it was unconfirmed reads again and
        // the next response decides afresh.
        this.nextReadAt = Date.now();
        this.explicitTailOwed = true;
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
    // An explicit refresh is how a person asks again after the schedule settled
    // on an unconfirmed row, so it clears the output deadline the failed or
    // unconfirmed attempt parked, not only the two above.
    this.nextReadAt = Date.now();
    this.explicitTailOwed = true;
    if (this.snapshot.job?.terminal) {
      this.terminalObservation++;
      this.publish({ drainPending: true });
    }
    this.pump();
  }

  /** Settles or restarts the live output deadline from the confidence of the
   * row it now holds. Called wherever that confidence can change — a metadata
   * read landing, a reconnect — so the schedule never depends on which of the
   * two replies happened to arrive last. A pending drain still finishes,
   * because those bytes were already written. */
  private reconcileLiveDeadline(job: JobActivityJob | null, drainPending: boolean) {
    // An owed one-shot read is a demand, not a poll. It keeps the slot due until
    // it is served, so unconfirmed metadata stops the RECURRING cadence without
    // cancelling the read a person asked for.
    if (livePollingStopped(job) && !drainPending && !this.tailOwed()) {
      this.liveDueAt = Number.POSITIVE_INFINITY;
      return;
    }
    if (!Number.isFinite(this.liveDueAt)) {
      // The row is followable now (a confirmed run, or a terminal one whose
      // drain has work left), so the cadence it had stopped starts again.
      this.liveDueAt = Date.now();
      if (!Number.isFinite(this.nextReadAt)) this.nextReadAt = Date.now();
    }
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

  /** Joins the output read in flight and any follow-up the same pump queues,
   * without adding demand: a suite that opens a job log converges on the
   * read's real completion instead of a DOM deadline the machinery can
   * outlast under load. Resolves immediately when no output read is in flight
   * (the reader is not mounted, or its last read already settled); callers
   * still assert on the rendered log. */
  async settleForTests(): Promise<void> {
    let inFlight = this.outputInFlight;
    while (inFlight) {
      await inFlight;
      inFlight = this.outputInFlight;
    }
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
      // An owed tail read is a DEMAND, not a poll: it forces the slot regardless
      // of the cadence, so unconfirmed metadata can stop recurring reads without
      // cancelling the one a person asked for.
      const liveDue = now >= this.liveDueAt || this.snapshot.drainPending || this.tailOwed();
      const pending = this.snapshot.pendingDemand;
      const readHistory = pending !== null && !(liveDue && this.historyServedSinceLive);
      const selection = readHistory && pending !== null ? selectJobLogDemand(this.snapshot.windows, pending) : null;
      if (readHistory && selection === null) this.publish({ pendingDemand: null });
      if (selection !== null || liveDue) this.readOutput(selection, selection !== null ? pending : null);
    }
    if (!this.readable()) return;
    // A pending demand, a pending drain and an owed tail are all work the slot
    // is going to do, so the next wakeup for them is the backoff alone. Folding
    // liveDueAt into this maximum would make an unconfirmed row's infinite live
    // deadline govern the wakeup as well, and an owed tail would then wait for a
    // timer nobody armed — the refresh the person asked for would never be sent.
    const outputDue = this.outputInFlight
      ? Number.POSITIVE_INFINITY
      : Math.max(
          this.nextReadAt,
          this.snapshot.pendingDemand !== null || this.snapshot.drainPending || this.tailOwed() ? now : this.liveDueAt,
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
        // An unconfirmed nonterminal row gets no further metadata reads: the
        // journal cannot establish the job's state, so the next read would ask
        // the same unanswerable question.
        this.metadataDueAt = livePollingStopped(job) ? Number.POSITIVE_INFINITY : Date.now() + READ_INTERVAL_MS;
        // The output deadline follows the METADATA too, not only the page that
        // happened to arrive last. Metadata may settle after a failed output
        // attempt, or a refresh may deliver the page before the metadata that
        // says the row is unconfirmed; reconciling on the read that changed the
        // answer keeps the schedule honest in both orders.
        this.reconcileLiveDeadline(job, drainPending);
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
    // Whatever a person asked for is being served now. A history page satisfies
    // its own demand, so only the asked-for tail clears this flag.
    if (kind === "live") this.explicitTailOwed = false;
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
          // Output stops following only when the row is settled: terminal with
          // nothing left to drain, or an unconfirmed status that no amount of
          // further reading can turn into an answer. A pending drain still
          // finishes, because those bytes were already written.
          const settled = livePollingStopped(this.snapshot.job) && !drainPending;
          this.liveDueAt = settled ? Number.POSITIVE_INFINITY : Date.now() + READ_INTERVAL_MS;
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
        // A failed attempt is a settling observation like any other. For a row
        // whose status is unconfirmed, retrying the tail once a second asks a
        // question the journal cannot answer, and shows the reader an error that
        // keeps re-firing. The error stays visible; refresh(), paging, reconnect
        // reads and every healthy live row keep their normal retry.
        //
        // The stop is expressed in liveDueAt and NOT in nextReadAt. nextReadAt is
        // the slot's own backoff, and pump() consults it before anything else, so
        // parking it at infinity would strand every other user of the slot: an
        // older-page request queued by scrolling, or a terminal drain whose
        // metadata arrived after this failure. "Do not poll again yet" and "do
        // not read again at all" are different statements, and only the first is
        // true here.
        const automatic = selection === null && this.snapshot.pendingDemand === null && !this.snapshot.drainPending;
        const stop = automatic && livePollingStopped(this.snapshot.job);
        this.nextReadAt = Date.now() + READ_INTERVAL_MS;
        if (stop) this.liveDueAt = Number.POSITIVE_INFINITY;
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
        // A settled LIVE tail is what makes the initial load no longer owed; a
        // history page says nothing about whether this reader has read its tail.
        if (kind === "live") this.liveAttempted = true;
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
