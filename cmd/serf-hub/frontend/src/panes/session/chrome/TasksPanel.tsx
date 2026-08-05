// TasksPanel: a trigger + Sheet for the session's task list.
//
// Two independent signals, per the plan's push-driven-plus-fetch-on-open
// model:
//   - The TRIGGER's badge is model.tasks (the {total, done} aggregate,
//     pushed live by serf/task/updated - protocol/reducer.ts's own case) -
//     cheap and already-live without opening anything, so it stays exactly
//     as before.
//   - The SHEET's row list is fetched fresh via threadsStore.listTasks(ref)
//     every time the panel opens (unblocked by the T1 addendum, commit
//     da1b43f85 on w5-interaction, cherry-picked here), and re-fetched
//     automatically if model.tasks changes again while the panel stays
//     open (a live update while the user is looking at the list - the
//     aggregate object reference only changes when the reducer's own
//     serf/task/updated case actually re-assigns it, so this never
//     refires on an unrelated model update). taskData.ts's
//     parseTaskListData (pinned wire-true against agent/task/task_store.go's
//     real Task shape) owns interpreting the raw `unknown` response.
//
// Failure handling: a Codex-source thread rejects the wire call outright
// (appwire.Unavailable, "actionUnavailable" - verified against
// CodexSource.ListTasks) - an expected capability gap, not a bug, so it
// gets an honest inline "not available" state and no toast. A resolved-
// but-uninterpretable response (parseTaskListData returning null - e.g. an
// old daemon with no tasksFn registered, which responds with null data
// rather than rejecting - server/appwire_runtime.go:713-721) is folded into
// the SAME "not available" state, for the same reason: nothing actually
// went wrong at the transport level. A thread with no live local daemon
// behind it (a one-shot CLI session that already exited, or one never
// resumed) rejects ListTasks the same way its ref lookup fails: "thread not
// found" (isThreadNotFound, sessionErrors.ts - shared with ActivityPanel).
// Whether that folds into an empty list or
// a distinct terminal state depends on model.tasks: null means the frontend
// truly has no way to distinguish "never had tasks" from "can't currently
// ask", so it renders the same "No tasks yet" a real `[]` response would.
// But once model.tasks is non-null, the trigger beside this panel is
// ALREADY on screen claiming tasks exist (serf/task/updated only fires when
// the agent has actually edited its list - see the re-fetch paragraph
// below) - "No tasks yet" would contradict it, so that case gets its own
// terminal copy instead (renderBody's daemonGone branch), and never wipes
// whatever rows are already retained. Any other rejection - including the
// SAME sessionUnavailable code for a daemon that's merely unreachable this
// instant - is a genuine failure: toast (the wave's failure-feedback
// convention) AND an inline report.
//
// That inline report takes one of two forms, because a re-fetch has
// something a first fetch does not: the previous page, still in hand. A
// first fetch that fails has nothing to show, and gets the error state
// alone. A re-fetch that fails KEEPS the list the reader is looking at and
// puts the failure above it, the way LoadOlderRow reports a failed page
// without discarding the transcript above it - a list one push out of date
// beats a blank panel. The list is never left to pass as current: the
// notice above it says what it is, so a failed refresh can be mistaken
// neither for a fresh list nor for the "No tasks yet" state.
//
// Both forms carry Try again, because there is no schedule to fall back on.
// The re-fetch trigger is model.tasks changing, i.e. a serf/task/updated
// push, which the projector emits only when the agent actually edits its
// task list (internal/appprojector/appwire_projection.go's EventTaskUpdated
// case) - never on a timer. A session whose agent is still working heals
// itself on the next edit; one that has gone quiet emits nothing more, and
// without Try again its panel stays broken until the reader guesses that
// closing and re-opening refetches.
//
// The daemonGone terminal state (isThreadNotFound with model.tasks non-null)
// is the one exception, and deliberately carries no Try again: see
// isThreadNotFound's own comment for why that specific rejection means
// neither a live daemon nor a past-index record exists for the thread, so
// nothing - not Try again, not closing and re-opening, not reloading the
// whole app - can make the next attempt succeed.
import { forwardRef, type ReactNode, useEffect, useImperativeHandle, useRef, useState } from "react";
import { errorText, sessionActionError, sessionActionHeadline } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { EMPTY_TASKS_PANEL_ENTRY, tasksPanelStore, useTasksPanelStore } from "../../../stores/tasksPanel";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, type ChipTone, EmptyState, Sheet, useToasts } from "../../../widgets";
import { Disclosure } from "../../../widgets/disclosure";
import { requireClass } from "../../../widgets/internal/requireClass";
import { isActionUnavailable, isThreadNotFound } from "./sessionErrors";
import { parseTaskListData, type TaskRow, type TaskStatus } from "./taskData";
import styles from "./taskspanel.module.css";

export interface TasksPanelProps {
  sessionRef: string;
  model: ThreadModel;
  // True once SessionChrome's own row has measured too narrow to show this
  // panel's own inline trigger beside Details and the "..." menu without
  // wrapping to a second row (kata vybn) - SessionChrome renders a "Tasks"
  // item in that menu instead, opening this SAME Sheet through the
  // imperative handle below. Omitted (the default) is every existing
  // caller/test, which never suppresses this trigger.
  hideTrigger?: boolean;
}

/** Lets SessionChrome open this panel's Sheet from a collapsed menu item,
 * without lifting `open` out of this component (which would touch every
 * existing render site in TasksPanel.test.tsx for no behavioral gain - see
 * DetailsPanelHandle's identical rationale). */
export interface TasksPanelHandle {
  open: () => void;
}

const CLASS = {
  state: requireClass(styles.state, "taskspanel.module.css", "state"),
  list: requireClass(styles.list, "taskspanel.module.css", "list"),
  description: requireClass(styles.description, "taskspanel.module.css", "description"),
  stale: requireClass(styles.stale, "taskspanel.module.css", "stale"),
  staleMessage: requireClass(styles.staleMessage, "taskspanel.module.css", "staleMessage"),
  staleHint: requireClass(styles.staleHint, "taskspanel.module.css", "staleHint"),
  detailList: requireClass(styles.detailList, "taskspanel.module.css", "detailList"),
  detailRow: requireClass(styles.detailRow, "taskspanel.module.css", "detailRow"),
  detailLabel: requireClass(styles.detailLabel, "taskspanel.module.css", "detailLabel"),
  detailValue: requireClass(styles.detailValue, "taskspanel.module.css", "detailValue"),
  detailPrompt: requireClass(styles.detailPrompt, "taskspanel.module.css", "detailPrompt"),
  notesList: requireClass(styles.notesList, "taskspanel.module.css", "notesList"),
};

// Mirrors the legacy sidebar/inline task-row grammar (cmd/serf-hub/assets/
// renderer-format.js: planGlyphForStatus/planStateClass) translated onto
// this app's own widget vocabulary (Chip tones) rather than the legacy's
// window.SerfIcons SVG fragments, which this client has no equivalent of -
// same semantic mapping for the GLYPH: done gets a checkmark, in_progress
// a filled dot, cancelled an X (distinct shape from pending's hollow
// circle) so "won't happen" reads differently from "hasn't started yet".
//
// The TONE, however, does NOT mirror planGlyphForStatus's own comment
// ("a plan item that will not happen reads the same as a failure") into
// danger - that comment governs only the glyph SHAPE choice. The legacy's
// actual rendering chain colors it neutral: planStateClass (this same
// file, lines 496-506) maps cancelled into the SAME CSS class family as
// pending/done, and style.css:3324-3329 confirms it explicitly - "cancelled
// — recedes like done, struck to read as dropped" - `.plan-item.cancelled
// .plan-glyph` is `--ink-3` (the identical dim neutral pending's glyph
// uses), never a danger red. In this design system's color-is-attention
// rule (tokens.css: "a human is needed / agent working / failure - nothing
// else may be amber/danger"), tinting a routine cancellation as danger
// would make reprioritized work indistinguishable from a genuine failure.
// The ✕ glyph alone carries the "won't happen" distinction; the tone stays
// neutral like every other settled, non-attention-needing state.
const STATUS_GLYPH: Record<TaskStatus, string> = {
  open: "○",
  in_progress: "●",
  done: "✓",
  cancelled: "✕",
};

export const STATUS_TONE: Record<TaskStatus, ChipTone> = {
  open: "neutral",
  in_progress: "alive",
  done: "neutral",
  cancelled: "neutral",
};

function triggerLabel(tasks: ThreadModel["tasks"]): string {
  return tasks ? `Tasks ${tasks.done}/${tasks.total}` : "Tasks";
}

// The one name this panel's failure goes by. Both reports of it - the toast
// and the inline state - are built from this and from the same discriminator
// (protocol/errors.ts), so a failed session resume takes over both or
// neither; the panel can never say two different things about one failure.
const LOAD_FAILURE = "Couldn't load tasks";

// One failure, in the two shapes this panel needs to render it: `headline`
// and `detail` for the EmptyState, which lays a title and a hint out in
// separate slots, and `sentence` for the single-string cases - the toast and
// the stale notice. A rejection carrying no text of its own leaves the
// detail out entirely, the same way sessionActionError drops the separator.
//
// All three come off the same rejection through protocol/errors.ts, and the
// toast renders `sentence` itself rather than recomputing it, so the panel's
// reports of one failure cannot drift apart.
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

// Scopes a task row's disclosure state to this session: task ids restart at
// 1 in every session (agent/task/task_store.go mints them per session), but
// widgets/disclosure's store is one page-lifetime singleton shared by every
// open TasksPanel - without the session in the key, expanding task #1 in one
// session would show task #1 already expanded the next time a DIFFERENT
// session's panel opens. Mirrors subagentModuleStore.ts's itemScopeKey (same
// NUL-separator idiom); no "" fallback is needed here since TasksPanelProps'
// sessionRef is never optional.
function taskDisclosureId(sessionRef: string, taskId: number): string {
  return `${sessionRef}\0${taskId}`;
}

// One label/value row in a task's detail list - the same grammar
// DetailsPanel.tsx's own DetailRow uses (the sibling Sheet's convention for
// "a labeled list of one entity's facts"): caption label above a mono
// value, omitted by the caller entirely when the field has nothing to show
// rather than rendered empty.
function TaskDetailField({ label, testId, children }: { label: string; testId: string; children: ReactNode }) {
  return (
    <div className={CLASS.detailRow} data-testid={testId}>
      <dt className={CLASS.detailLabel}>{label}</dt>
      <dd className={CLASS.detailValue}>{children}</dd>
    </div>
  );
}

// The task's full details, revealed by TaskRowView's disclosure. status and
// type are always present on the wire so always render; the rest are
// omitted when absent (DetailsPanel's own rule) rather than shown empty - a
// freshly appended task with no dependents, no reasoning override and no
// notes yet is common, not malformed.
//
// insert/created_at/updated_at/completed_at are real wire fields
// (agent/task/task_store.go) that never reach TaskRow at all (taskData.ts's
// own comment) - "full details" here means every field TaskRow carries, not
// literally everything the daemon knows about the task. Recorded as a gap
// on kata rb74 rather than invented here.
function TaskDetails({ task }: { task: TaskRow }) {
  const dependsOn = task.dependsOn ?? [];
  const notes = task.notes ?? [];
  const hasPrompt = task.prompt.trim() !== "";
  return (
    <dl className={CLASS.detailList}>
      <TaskDetailField label="status" testId="task-detail-status">
        {task.status}
      </TaskDetailField>
      <TaskDetailField label="type" testId="task-detail-type">
        {task.type}
      </TaskDetailField>
      {dependsOn.length > 0 && (
        <TaskDetailField label="depends on" testId="task-detail-depends-on">
          {dependsOn.map((id) => `#${id}`).join(", ")}
        </TaskDetailField>
      )}
      {task.reasoningEffort && (
        <TaskDetailField label="reasoning" testId="task-detail-reasoning">
          {task.reasoningEffort}
        </TaskDetailField>
      )}
      {hasPrompt && (
        <TaskDetailField label="prompt" testId="task-detail-prompt">
          <pre className={CLASS.detailPrompt}>{task.prompt}</pre>
        </TaskDetailField>
      )}
      {notes.length > 0 && (
        <TaskDetailField label="notes" testId="task-detail-notes">
          <ol className={CLASS.notesList}>
            {notes.map((note, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: notes only ever append over a task's life (agent/task/task_store.go's update handling) - position is stable identity, same reasoning as ThinkBlock.tsx's summaryIndex
              <li key={i}>{note}</li>
            ))}
          </ol>
        </TaskDetailField>
      )}
    </dl>
  );
}

// TaskRowView wraps the row's existing glyph+description line as a
// widgets/disclosure summary - the same store-backed disclosure primitive
// the design spec named as "the natural home" for every disclosure in this
// app (docs/superpowers/specs/2026-07-23-webui-ux-round2-design.md §6), and
// the transcript's ThinkBlock/SteeringItem/SystemNoticeItem already use the
// same idiom by hand. Reusing the component directly - rather than a fourth
// hand-rolled copy - is deliberate: no other call site does yet, so this is
// its first real consumer.
function TaskRowView({ task, sessionRef }: { task: TaskRow; sessionRef: string }) {
  const summary = (
    <>
      <Chip tone={STATUS_TONE[task.status]}>{STATUS_GLYPH[task.status]}</Chip>
      <span className={CLASS.description}>{task.description}</span>
    </>
  );
  return (
    // No className here: Disclosure's own .summary/.body already lay out
    // the full row width - this <li> exists only to keep the <ul>'s
    // children real <li>s, the list semantics screen readers rely on.
    <li data-testid="task-row">
      <Disclosure id={taskDisclosureId(sessionRef, task.id)} summary={summary}>
        <TaskDetails task={task} />
      </Disclosure>
    </li>
  );
}

export const TasksPanel = forwardRef<TasksPanelHandle, TasksPanelProps>(function TasksPanel(
  { sessionRef, model, hideTrigger = false },
  ref,
) {
  const toasts = useToasts();
  const [open, setOpen] = useState(false);
  useImperativeHandle(ref, () => ({ open: () => setOpen(true) }), []);
  const entry = useTasksPanelStore((state) => state.entries.get(sessionRef)) ?? EMPTY_TASKS_PANEL_ENTRY;
  const openRef = useRef(open);
  useEffect(() => {
    openRef.current = open;
  }, [open]);
  // Bumped by Try again. The only fetch trigger a reader controls: the other
  // two are opening the panel and a push arriving, and neither is available
  // to someone looking at a failed fetch in an open panel on a quiet session.
  const [reloads, setReloads] = useState(0);

  // Re-fetches on every open, on every Try again, and again whenever
  // model.tasks changes while still open (a live serf/task/updated push while
  // the user is looking) - see this file's own header comment. `toasts` is
  // deliberately not a dependency: useToasts() returns a fresh wrapper object
  // every render (see widgets/toast/index.tsx), so depending on it would
  // refire this effect on every unrelated re-render; toasts.push itself is a
  // stable, module-level function underneath.
  // biome-ignore lint/correctness/useExhaustiveDependencies: toasts is a fresh wrapper object every render (see above) - toasts.push itself is stable
  useEffect(() => {
    if (!open) return;
    const fetchID = tasksPanelStore.getState().beginFetch(sessionRef);
    threadsStore
      .getState()
      .listTasks(sessionRef)
      .then((data) => {
        const parsed = parseTaskListData(data);
        tasksPanelStore
          .getState()
          .publishFetch(
            sessionRef,
            fetchID,
            parsed === null ? { kind: "unsupported" } : { kind: "rows", rows: parsed },
          );
      })
      .catch((err) => {
        if (isActionUnavailable(err)) {
          tasksPanelStore.getState().publishFetch(sessionRef, fetchID, { kind: "unsupported" });
          return;
        }
        if (isThreadNotFound(err)) {
          if (model.tasks === null) {
            // Never had a live aggregate pushed - genuinely "no tasks", not
            // merely "can't ask any more" (see the header comment above).
            tasksPanelStore.getState().publishFetch(sessionRef, fetchID, { kind: "empty" });
          } else {
            // The trigger is already on screen claiming tasks exist; rows
            // is left untouched so a retained list survives this rejection
            // instead of being replaced by "No tasks yet".
            tasksPanelStore.getState().publishFetch(sessionRef, fetchID, { kind: "daemon-gone" });
          }
          return;
        }
        // `rows` is deliberately left alone: whatever the last fetch that
        // did resolve put there stays on screen under the stale notice.
        // Only a panel that has never had a list renders the error state on
        // its own, and that is exactly the `rows === null` case below.
        const failure = loadFailure(err);
        tasksPanelStore.getState().publishFetch(sessionRef, fetchID, { kind: "failure", failure });
        if (openRef.current) toasts.push("error", failure.sentence);
      });
  }, [open, model.tasks, sessionRef, reloads]);

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
    const { rows, unsupported, daemonGone, failure: error } = entry;
    if (unsupported) {
      // No Try again here: the source cannot answer this call at all, so
      // asking again would only fail again.
      return (
        <EmptyState title="Task list isn't available" hint="This session's source doesn't support the task list." />
      );
    }
    if (daemonGone && (rows === null || rows.length === 0)) {
      // Nothing to show either way - a panel that never fetched
      // successfully in this session and a confirmed-empty retained list
      // both have zero rows, so the same terminal message covers both
      // rather than inventing a distinction the reader can't observe. No
      // Try again: see isThreadNotFound's comment for why asking again
      // cannot succeed.
      return (
        <EmptyState
          title="This session has ended"
          hint="Its daemon has exited, and there's no record of its task list to fall back on."
        />
      );
    }
    if (rows === null) {
      if (error) return <EmptyState title={error.headline} hint={error.detail} action={retry} />;
      return <p className={CLASS.state}>Loading tasks…</p>;
    }
    return (
      <>
        {daemonGone && (
          <div className={CLASS.stale} data-testid="tasks-daemon-gone">
            <p className={CLASS.staleMessage}>This session's daemon has exited.</p>
            <p className={CLASS.staleHint}>Showing the last list that loaded. It won't update again.</p>
          </div>
        )}
        {error && (
          <div className={CLASS.stale} data-testid="tasks-stale">
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
          <EmptyState title="No tasks yet" hint="The agent's task list is empty for this session." />
        ) : (
          <ul className={CLASS.list}>
            {rows.map((row) => (
              <TaskRowView key={row.id} task={row} sessionRef={sessionRef} />
            ))}
          </ul>
        )}
      </>
    );
  }

  return (
    <>
      {/* data-tasks-trigger lets the command palette's "Toggle tasks panel"
          (/tasks) synthesize a click here (shell/palette/commands.ts) - without
          it that command is inert. Button forwards data-* to the real <button>.
          Omitted while hideTrigger is set - see DetailsPanel's identical
          hideTrigger comment for why /tasks going quiet while collapsed is
          the accepted trade-off here (kata vybn's report). */}
      {!hideTrigger && (
        <Button variant="quiet" size="sm" onClick={openPanel} data-tasks-trigger="">
          {triggerLabel(model.tasks)}
        </Button>
      )}
      <Sheet open={open} onClose={closePanel} title="Tasks">
        {renderBody()}
      </Sheet>
    </>
  );
});
