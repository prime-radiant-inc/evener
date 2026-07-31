// JobsPanel: a trigger + Sheet for the session's shell/delegate job list.
//
// Mirrors TasksPanel's structure onto the jobs wire (agent/jobs_panel.go's
// JobSummary/JobOutputTail via jobData.ts's parsers): fetch-on-open, refetch
// whenever model.jobsUpdatedAt changes while the panel stays open (a live
// serf/job/started or serf/job/finished push while the user is looking - the
// reducer bumps it on both), and the same failure taxonomy. Unlike
// TasksPanel there is no live-pushed aggregate to badge the trigger with:
// the running count comes from the last fetched list itself, so the trigger
// starts bare and gains its ●N only after the first fetch lands.
//
// Failure handling is TasksPanel's, with one simplification: there is no
// model.tasks-style aggregate to disambiguate isThreadNotFound's "never had
// jobs" from "can't ask any more", so that rejection is ALWAYS terminal -
// daemonGone. With no prior rows it renders "This session has ended" (and no
// Try again: sessionErrors.ts's own comment covers why retrying cannot
// succeed); with retained rows it keeps them under a terminal notice instead
// of blanking them. Every other rejection follows the first-fetch/stale-
// refetch split: a first fetch that fails has nothing to keep and gets the
// error state alone; a re-fetch that fails keeps the list the reader is
// looking at and puts the failure above it. Both carry Try again - pushes
// are event-driven, never scheduled, so a quiet session emits nothing more
// and Try again is the only way out.
//
// The output tail is lazy-on-expand: Disclosure mounts its body only when
// open (widgets/disclosure/index.tsx), so JobOutputTailView living inside
// the body fetches serf/jobs/output exactly when a row is expanded. The tail
// does NOT re-fire on jobsUpdatedAt - the next collapse/expand re-mounts and
// refetches; keeping the tail static while open avoids text jumping under
// the reader.
import { forwardRef, type ReactNode, useEffect, useImperativeHandle, useState } from "react";
import { errorText, sessionActionError, sessionActionHeadline } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, type ChipTone, EmptyState, Sheet, useToasts } from "../../../widgets";
import { Disclosure } from "../../../widgets/disclosure";
import { requireClass } from "../../../widgets/internal/requireClass";
import { type JobOutput, type JobRow, type JobStatus, parseJobListData, parseJobOutputData } from "./jobData";
import styles from "./jobspanel.module.css";
import { isActionUnavailable, isThreadNotFound } from "./sessionErrors";
import { formatWorkDuration } from "./statusFormat";

export interface JobsPanelProps {
  sessionRef: string;
  model: ThreadModel;
  // SessionChrome's useNowTick value: the clock a running job's elapsed time
  // ticks against. Passed down rather than owned here so every chrome
  // surface shares one tick (see TasksPanelProps' hideTrigger comment for
  // the same ownership rationale).
  now: number;
  hideTrigger?: boolean;
}

/** Lets SessionChrome open this panel's Sheet from a collapsed menu item -
 * TasksPanelHandle's identical rationale. */
export interface JobsPanelHandle {
  open: () => void;
}

const CLASS = {
  state: requireClass(styles.state, "jobspanel.module.css", "state"),
  list: requireClass(styles.list, "jobspanel.module.css", "list"),
  description: requireClass(styles.description, "jobspanel.module.css", "description"),
  stale: requireClass(styles.stale, "jobspanel.module.css", "stale"),
  staleMessage: requireClass(styles.staleMessage, "jobspanel.module.css", "staleMessage"),
  staleHint: requireClass(styles.staleHint, "jobspanel.module.css", "staleHint"),
  detailList: requireClass(styles.detailList, "jobspanel.module.css", "detailList"),
  detailRow: requireClass(styles.detailRow, "jobspanel.module.css", "detailRow"),
  detailLabel: requireClass(styles.detailLabel, "jobspanel.module.css", "detailLabel"),
  detailValue: requireClass(styles.detailValue, "jobspanel.module.css", "detailValue"),
  detailPrompt: requireClass(styles.detailPrompt, "jobspanel.module.css", "detailPrompt"),
  outputTail: requireClass(styles.outputTail, "jobspanel.module.css", "outputTail"),
  outputCaption: requireClass(styles.outputCaption, "jobspanel.module.css", "outputCaption"),
};

// Type glyph: › for shell jobs (a prompt's continuation mark), ◈ for
// delegates (a distinct agent). JobRow.type is a free string on the wire, so
// anything else falls back to a neutral dot rather than a bare description.
const TYPE_GLYPH: Record<string, string> = {
  shell: "›",
  delegate: "◈",
};

// Color-is-attention (tokens.css: agent working / failure, nothing else):
// only a live job is alive, only a genuinely failed job is danger; every
// settled non-failure state - completed, cancelled, stopped - recedes
// neutral (TasksPanel's cancelled-is-neutral comment applies here whole).
export const STATUS_TONE: Record<JobStatus, ChipTone> = {
  running: "alive",
  completed: "neutral",
  cancelled: "neutral",
  stopped: "neutral",
  failed: "danger",
  exhausted: "danger",
};

// The one name this panel's failure goes by - TasksPanel's LOAD_FAILURE
// convention: toast and inline state are built from this and the same
// discriminator, so the panel never says two different things about one
// failure.
const LOAD_FAILURE = "Couldn't load jobs";

interface LoadFailure {
  headline: string;
  detail?: string;
  sentence: string;
}

function loadFailure(err: unknown): LoadFailure {
  const headline = sessionActionHeadline(LOAD_FAILURE, err);
  const sentence = sessionActionError(LOAD_FAILURE, err);
  const detail = errorText(err).trim();
  return detail ? { headline, detail, sentence } : { headline, sentence };
}

// Scopes a job row's disclosure state to this session - taskDisclosureId's
// identical NUL-separator idiom (TasksPanel.tsx's own comment covers why the
// session must be in the key).
function jobDisclosureId(sessionRef: string, jobId: string): string {
  return `${sessionRef}\0${jobId}`;
}

// Elapsed time for a running job, wall duration for a finished one, both
// through statusFormat.ts's formatWorkDuration (the chrome's own long-range
// bucketing - jobs can run for hours, so transcript/messages/format.ts's
// sub-second convention is the wrong one here). undefined when either
// timestamp is unparseable: show no clock rather than a garbage one.
function jobDuration(row: JobRow, now: number): string | undefined {
  const started = Date.parse(row.startedAt);
  if (!Number.isFinite(started)) return undefined;
  const end = row.status === "running" || !row.endedAt ? now : Date.parse(row.endedAt);
  if (!Number.isFinite(end)) return undefined;
  return formatWorkDuration(end - started);
}

function triggerLabel(rows: JobRow[] | null): string {
  const running = rows?.filter((row) => row.status === "running").length ?? 0;
  return running > 0 ? `Jobs ●${running}` : "Jobs";
}

// One label/value row in a job's detail list - TasksPanel's TaskDetailField
// grammar, omitted entirely when the field has nothing to show.
function JobDetailField({ label, testId, children }: { label: string; testId: string; children: ReactNode }) {
  return (
    <div className={CLASS.detailRow} data-testid={testId}>
      <dt className={CLASS.detailLabel}>{label}</dt>
      <dd className={CLASS.detailValue}>{children}</dd>
    </div>
  );
}

// The retained output tail for one job, fetched lazily when its row's
// disclosure first opens (see the header comment). Try again bumps reloads
// to refetch; a parse miss (uninterpretable data from an old daemon) renders
// as an empty tail rather than an error, matching parseJobListData's
// null-means-capability-gap convention upstream.
function JobOutputTailView({ sessionRef, jobId }: { sessionRef: string; jobId: string }) {
  const toasts = useToasts();
  const [output, setOutput] = useState<JobOutput | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [reloads, setReloads] = useState(0);
  // biome-ignore lint/correctness/useExhaustiveDependencies: toasts wrapper is fresh every render; toasts.push is stable
  useEffect(() => {
    let cancelled = false;
    setError(null);
    threadsStore
      .getState()
      .jobOutput(sessionRef, jobId)
      .then((data) => {
        if (cancelled) return;
        setOutput(parseJobOutputData(data) ?? { tail: "", totalBytes: 0, retainedStart: 0, truncated: false });
      })
      .catch((err) => {
        if (cancelled) return;
        const sentence = sessionActionError("Couldn't load job output", err);
        setError(sentence);
        toasts.push("error", sentence);
      });
    return () => {
      cancelled = true;
    };
  }, [sessionRef, jobId, reloads]);
  if (error) {
    return (
      <div data-testid="job-output-error">
        {/* role=alert: the tail failed under an expanded row the reader is
            already looking at, with nothing on screen leading up to it. */}
        <p role="alert" className={CLASS.staleMessage}>
          {error}
        </p>
        <Button variant="quiet" size="sm" onClick={() => setReloads((n) => n + 1)}>
          Try again
        </Button>
      </div>
    );
  }
  if (!output) return <p data-testid="job-output-loading">Loading output…</p>;
  return (
    <div data-testid="job-output">
      {output.truncated && (
        <p className={CLASS.outputCaption}>
          Showing last {output.tail.length} of {output.totalBytes} bytes
        </p>
      )}
      <pre className={CLASS.outputTail}>{output.tail}</pre>
    </div>
  );
}

// The job's full details, revealed by JobRowView's disclosure. status/type/
// started/output size are non-omitempty on the wire so always render; the
// rest are omitted when absent (DetailsPanel's own rule) rather than shown
// empty. The output tail renders only when the wire says there is any.
function JobDetails({ row, sessionRef }: { row: JobRow; sessionRef: string }) {
  return (
    <>
      <dl className={CLASS.detailList}>
        <JobDetailField label="status" testId="job-detail-status">
          {row.status}
        </JobDetailField>
        <JobDetailField label="type" testId="job-detail-type">
          {row.type}
        </JobDetailField>
        <JobDetailField label="started" testId="job-detail-started">
          {row.startedAt}
        </JobDetailField>
        {row.endedAt && (
          <JobDetailField label="ended" testId="job-detail-ended">
            {row.endedAt}
          </JobDetailField>
        )}
        {row.exitCode !== undefined && (
          <JobDetailField label="exit code" testId="job-detail-exit-code">
            {row.exitCode}
          </JobDetailField>
        )}
        <JobDetailField label="output size" testId="job-detail-output-size">
          {row.outputBytes} bytes
        </JobDetailField>
        {row.reason && (
          <JobDetailField label="reason" testId="job-detail-reason">
            {row.reason}
          </JobDetailField>
        )}
        {row.command && (
          <JobDetailField label="command" testId="job-detail-command">
            <pre className={CLASS.detailPrompt}>{row.command}</pre>
          </JobDetailField>
        )}
        {row.task && (
          <JobDetailField label="task" testId="job-detail-task">
            <pre className={CLASS.detailPrompt}>{row.task}</pre>
          </JobDetailField>
        )}
      </dl>
      {row.hasOutput && <JobOutputTailView sessionRef={sessionRef} jobId={row.jobId} />}
    </>
  );
}

function JobRowView({ row, sessionRef, now }: { row: JobRow; sessionRef: string; now: number }) {
  const duration = jobDuration(row, now);
  const summary = (
    <>
      <Chip tone={STATUS_TONE[row.status]}>{row.status}</Chip>
      <span aria-hidden="true">{TYPE_GLYPH[row.type] ?? "•"}</span>
      <span className={CLASS.description}>{row.description}</span>
      {duration && <span className={CLASS.state}>{duration}</span>}
    </>
  );
  return (
    // No className here: Disclosure's own .summary/.body already lay out the
    // full row width - this <li> exists only to keep the <ul>'s children
    // real <li>s, the list semantics screen readers rely on.
    <li data-testid="job-row">
      <Disclosure id={jobDisclosureId(sessionRef, row.jobId)} summary={summary}>
        <JobDetails row={row} sessionRef={sessionRef} />
      </Disclosure>
    </li>
  );
}

export const JobsPanel = forwardRef<JobsPanelHandle, JobsPanelProps>(function JobsPanel(
  { sessionRef, model, now, hideTrigger = false },
  ref,
) {
  const toasts = useToasts();
  const [open, setOpen] = useState(false);
  useImperativeHandle(ref, () => ({ open: () => setOpen(true) }), []);
  const [rows, setRows] = useState<JobRow[] | null>(null);
  const [unsupported, setUnsupported] = useState(false);
  const [error, setError] = useState<LoadFailure | null>(null);
  // Set instead of setRows([]) when isThreadNotFound fires - unlike
  // TasksPanel there is no live aggregate to soften this, so it is always
  // terminal (sessionErrors.ts's own comment covers why no retry is
  // offered). rows is left exactly as it was, so this never wipes a retained
  // list.
  const [daemonGone, setDaemonGone] = useState(false);
  // Bumped by Try again. The only fetch trigger a reader controls.
  const [reloads, setReloads] = useState(0);

  // Re-fetches on every open, on every Try again, and again whenever
  // model.jobsUpdatedAt changes while still open (a live job lifecycle push
  // while the user is looking) - see this file's own header comment.
  // `toasts` is deliberately not a dependency: useToasts() returns a fresh
  // wrapper object every render (see widgets/toast/index.tsx), so depending
  // on it would refire this effect on every unrelated re-render;
  // toasts.push itself is a stable, module-level function underneath.
  // biome-ignore lint/correctness/useExhaustiveDependencies: toasts is a fresh wrapper object every render (see above) - toasts.push itself is stable
  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setError(null);
    setUnsupported(false);
    setDaemonGone(false);
    threadsStore
      .getState()
      .listJobs(sessionRef)
      .then((data) => {
        if (cancelled) return;
        const parsed = parseJobListData(data);
        if (parsed === null) {
          setUnsupported(true);
          setRows(null);
        } else {
          setRows(parsed);
        }
      })
      .catch((err) => {
        if (cancelled) return;
        if (isActionUnavailable(err)) {
          setUnsupported(true);
          setRows(null);
          return;
        }
        if (isThreadNotFound(err)) {
          // Terminal: rows is left untouched so a retained list survives
          // this rejection instead of being replaced by "No jobs yet".
          setDaemonGone(true);
          return;
        }
        // `rows` is deliberately left alone: whatever the last fetch that
        // did resolve put there stays on screen under the stale notice.
        const failure = loadFailure(err);
        setError(failure);
        toasts.push("error", failure.sentence);
      });
    return () => {
      cancelled = true;
    };
  }, [open, model.jobsUpdatedAt, sessionRef, reloads]);

  function reload() {
    setReloads((n) => n + 1);
  }

  function openPanel() {
    setOpen(true);
  }

  function closePanel() {
    setOpen(false);
  }

  const retry = (
    <Button variant="quiet" size="sm" onClick={reload}>
      Try again
    </Button>
  );

  function renderBody() {
    if (unsupported) {
      // No Try again here: the source cannot answer this call at all, so
      // asking again would only fail again.
      return <EmptyState title="Job list isn't available" hint="This session's source doesn't support the job list." />;
    }
    if (daemonGone && (rows === null || rows.length === 0)) {
      // Nothing to show either way - no Try again: sessionErrors.ts's
      // comment covers why asking again cannot succeed.
      return (
        <EmptyState
          title="This session has ended"
          hint="Its daemon has exited, and there's no record of its job list to fall back on."
        />
      );
    }
    if (rows === null) {
      if (error) return <EmptyState title={error.headline} hint={error.detail} action={retry} />;
      return <p className={CLASS.state}>Loading jobs…</p>;
    }
    return (
      <>
        {daemonGone && (
          <div className={CLASS.stale} data-testid="jobs-daemon-gone">
            <p className={CLASS.staleMessage}>This session's daemon has exited.</p>
            <p className={CLASS.staleHint}>Showing the last list that loaded. It won't update again.</p>
          </div>
        )}
        {error && (
          <div className={CLASS.stale} data-testid="jobs-stale">
            {/* role=alert: the refresh failed on its own, with the reader
                mid-list and nothing on screen leading up to it. */}
            <p role="alert" className={CLASS.staleMessage}>
              {error.sentence}
            </p>
            <p className={CLASS.staleHint}>Showing the last list that loaded.</p>
            {retry}
          </div>
        )}
        {rows.length === 0 ? (
          <EmptyState title="No jobs yet" hint="No shell or delegate jobs have run in this session." />
        ) : (
          <ul className={CLASS.list}>
            {rows.map((row) => (
              <JobRowView key={row.jobId} row={row} sessionRef={sessionRef} now={now} />
            ))}
          </ul>
        )}
      </>
    );
  }

  return (
    <>
      {/* No data-* trigger attribute: unlike /tasks there is no palette
          command for jobs, so nothing synthesizes a click here. */}
      {!hideTrigger && (
        <Button variant="quiet" size="sm" onClick={openPanel}>
          {triggerLabel(rows)}
        </Button>
      )}
      <Sheet open={open} onClose={closePanel} title="Jobs">
        {renderBody()}
      </Sheet>
    </>
  );
});
