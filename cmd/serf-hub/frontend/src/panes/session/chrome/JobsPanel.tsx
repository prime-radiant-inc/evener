// JobsPanel: a trigger + Sheet for the session's shell/delegate job list.
//
// Mirrors TasksPanel's structure onto the jobs wire (agent/jobs_panel.go's
// JobSummary/JobOutputTail via jobData.ts's parsers): fetch-on-open, refetch
// whenever model.jobsUpdatedAt changes while the panel stays open (a live
// serf/job/started or serf/job/finished push while the user is looking - the
// reducer bumps it on both), and the same failure taxonomy. Unlike
// TasksPanel there is no live-pushed aggregate to badge the trigger with:
// the count of unsettled jobs comes from the last fetched list itself, so the
// trigger starts bare and gains its ●N only after the first fetch lands.
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
import { forwardRef, type ReactNode, useEffect, useImperativeHandle, useRef, useState } from "react";
import { errorText, sessionActionError, sessionActionHeadline } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, type ChipTone, EmptyState, Sheet, useToasts } from "../../../widgets";
import { Disclosure } from "../../../widgets/disclosure";
import { requireClass } from "../../../widgets/internal/requireClass";
import {
  isSettledStatus,
  type JobOutput,
  type JobRow,
  type JobStatus,
  parseJobListData,
  parseJobOutputData,
} from "./jobData";
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

// A Map, not an object lookup: a wire status of "constructor" would answer
// off Object.prototype and hand Chip something that is not a tone at all.
const TONE_BY_STATUS = new Map<string, ChipTone>(Object.entries(STATUS_TONE));

// The tone for whatever status the wire actually sent (jobData.ts: JobRow.
// status is the wire's string, never narrowed to JobStatus). A status this
// bundle doesn't know comes from a newer daemon: it is not known to be alive
// and not known to have failed, so it recedes neutral rather than borrowing
// either signal. The row still renders, labelled with the wire's own word.
export function statusTone(status: string): ChipTone {
  return TONE_BY_STATUS.get(status) ?? "neutral";
}

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
  // A real job start is a positive Unix-epoch-ms wall-clock time. Go's zero
  // time reaches the wire as "0001-01-01T00:00:00Z" (a record folded without
  // a start timestamp), which parses fine and would clock two millennia onto
  // the row - statusFormat.ts's totalWorkMillis rejects its own anchor on
  // exactly this test, for exactly this reason.
  const started = Date.parse(row.startedAt);
  if (!Number.isFinite(started) || started <= 0) return undefined;
  // Only a running job's clock ticks against `now`. A settled status is the
  // wire's own word that the job is over, so it beats a missing endedAt: the
  // duration is then unknowable, and an unknowable duration shows no clock
  // rather than an elapsed time that climbs forever on a finished job.
  //
  // Deliberately NOT triggerLabel's isSettledStatus question, and the two
  // differ on exactly one row: an unrecognised status. A ticking clock asserts
  // this job is working RIGHT NOW and has been for N - a claim an unknown
  // status does not support - so it gets no clock, the same silence a
  // garbage timestamp gets. The badge defaults the other way because its
  // failure modes are not symmetric: an uncounted job is invisible, while an
  // over-counted one only puts a dot on a trigger the reader can open and
  // check.
  const end = row.status === "running" ? now : Date.parse(row.endedAt ?? "");
  if (!Number.isFinite(end)) return undefined;
  return formatWorkDuration(end - started);
}

// The badge counts every job the wire has NOT declared over - isSettledStatus,
// jobstore's own terminal vocabulary - rather than the jobs it calls
// "running". The two are the same list today, and a daemon that grows a second
// non-terminal status must not drop those jobs out of the count: while the
// panel is closed this badge is the only thing on screen saying anything is
// happening, so a job missing from it is a job the reader cannot see at all.
// Deliberately NOT the question jobDuration asks - see its own comment.
function triggerLabel(rows: JobRow[] | null): string {
  const unsettled = rows?.filter((row) => !isSettledStatus(row.status)).length ?? 0;
  return unsettled > 0 ? `Jobs ●${unsettled}` : "Jobs";
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
  // The caption is in BYTES. tail.length is UTF-16 code units, which
  // under-counts every non-BMP character and mis-counts every non-ASCII one.
  // totalBytes and retainedStart are both byte offsets the daemon measured
  // over the file itself (agent/jobs_panel.go's jobOutputTailFrom sets
  // retainedStart = totalBytes - len(tail) in Go bytes), so their difference
  // is exactly how many bytes this tail carries. Clamped at 0 against a
  // payload whose offsets disagree.
  const retainedBytes = Math.max(0, output.totalBytes - output.retainedStart);
  return (
    <div data-testid="job-output">
      {output.truncated && (
        <p className={CLASS.outputCaption}>
          Showing last {retainedBytes} of {output.totalBytes} bytes
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
      <Chip tone={statusTone(row.status)}>{row.status}</Chip>
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
  // model.jobsUpdatedAt as of the last fetch this panel issued. undefined
  // until the first one, which is how a closed panel tells a real push apart
  // from any other re-render that re-runs this effect (closing it, most of
  // all).
  const fetchedBump = useRef<number | null | undefined>(undefined);

  // Re-fetches on every open, on every Try again, and again whenever
  // model.jobsUpdatedAt changes (a live job lifecycle push) - see this
  // file's own header comment.
  // `toasts` is deliberately not a dependency: useToasts() returns a fresh
  // wrapper object every render (see widgets/toast/index.tsx), so depending
  // on it would refire this effect on every unrelated re-render;
  // toasts.push itself is a stable, module-level function underneath.
  // `hideTrigger` is deliberately not one either: it is read only by the
  // guard below, which every later run re-reads from that run's own props,
  // so collapsing the chrome cannot go unnoticed - it just doesn't cost a
  // fetch of its own.
  // biome-ignore lint/correctness/useExhaustiveDependencies: toasts is a fresh wrapper object every render, hideTrigger only gates the guard (see above)
  useEffect(() => {
    // A closed panel still chases a push, because the trigger's running
    // count is then the only thing on screen and it must not keep claiming a
    // job is running after the push that says it finished. Only a push,
    // though - a bump this panel has not fetched for yet - and only once a
    // first fetch has given the trigger a count worth keeping honest. With
    // the trigger hidden there is no count on screen at all.
    if (!open && (hideTrigger || fetchedBump.current === undefined || fetchedBump.current === model.jobsUpdatedAt)) {
      return;
    }
    fetchedBump.current = model.jobsUpdatedAt;
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
        // Only a fetch the reader can see is worth interrupting them over.
        // A background refresh keeping the trigger's badge honest (see the
        // effect's guard above) fails silently; the badge simply holds its
        // last known count until a later fetch lands.
        if (open) toasts.push("error", failure.sentence);
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
            {/* role=alert: same case as the stale notice below - this lands
                under a reader already looking at the list, with nothing on
                screen leading up to it, and it is terminal. */}
            <p role="alert" className={CLASS.staleMessage}>
              This session's daemon has exited.
            </p>
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
