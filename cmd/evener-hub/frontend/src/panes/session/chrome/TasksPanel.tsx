// TasksPanel: a trigger + Sheet for the session's task list.
//
// Two independent signals, per the plan's push-driven-plus-fetch-on-open
// model:
//   - The TRIGGER's badge is model.tasks (the {total, done} aggregate,
//     pushed live by evener/task/updated - protocol/reducer.ts's own case) -
//     cheap and already-live without opening anything, so it stays exactly
//     as before.
//   - The SHEET's row list is fetched fresh via threadsStore.listTasks(ref)
//     every time the panel opens (unblocked by the T1 addendum, commit
//     da1b43f85 on w5-interaction, cherry-picked here), and re-fetched
//     automatically if model.tasks changes again while the panel stays
//     open (a live update while the user is looking at the list - the
//     aggregate object reference only changes when the reducer's own
//     evener/task/updated case actually re-assigns it, so this never
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
// ALREADY on screen claiming tasks exist (evener/task/updated only fires when
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
// The re-fetch trigger is model.tasks changing, i.e. a evener/task/updated
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
import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from "react";
import { errorText, sessionActionError, sessionActionHeadline } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { EMPTY_TASKS_PANEL_ENTRY, tasksPanelStore, useTasksPanelStore } from "../../../stores/tasksPanel";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, type ChipTone, EmptyState, Markdown, Meter, Sheet, useToasts } from "../../../widgets";
import { Disclosure } from "../../../widgets/disclosure";
import { isDisclosureOpen, toggleDisclosure } from "../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../widgets/internal/requireClass";
import { isActionUnavailable, isThreadNotFound } from "./sessionErrors";
import { parseTaskListData, type TaskRow, type TaskStatus } from "./taskData";
import { groupTasks } from "./taskGroups";
import styles from "./taskspanel.module.css";
import { absoluteTime, relativeTime } from "./taskTime";

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
  bodyHead: requireClass(styles.bodyHead, "taskspanel.module.css", "bodyHead"),
  count: requireClass(styles.count, "taskspanel.module.css", "count"),
  list: requireClass(styles.list, "taskspanel.module.css", "list"),
  description: requireClass(styles.description, "taskspanel.module.css", "description"),
  groupHead: requireClass(styles.groupHead, "taskspanel.module.css", "groupHead"),
  groupCount: requireClass(styles.groupCount, "taskspanel.module.css", "groupCount"),
  settledSummary: requireClass(styles.settledSummary, "taskspanel.module.css", "settledSummary"),
  summaryMain: requireClass(styles.summaryMain, "taskspanel.module.css", "summaryMain"),
  summaryLine: requireClass(styles.summaryLine, "taskspanel.module.css", "summaryLine"),
  descDim: requireClass(styles.descDim, "taskspanel.module.css", "descDim"),
  descStruck: requireClass(styles.descStruck, "taskspanel.module.css", "descStruck"),
  time: requireClass(styles.time, "taskspanel.module.css", "time"),
  latest: requireClass(styles.latest, "taskspanel.module.css", "latest"),
  latestLabel: requireClass(styles.latestLabel, "taskspanel.module.css", "latestLabel"),
  latestText: requireClass(styles.latestText, "taskspanel.module.css", "latestText"),
  stale: requireClass(styles.stale, "taskspanel.module.css", "stale"),
  staleMessage: requireClass(styles.staleMessage, "taskspanel.module.css", "staleMessage"),
  staleHint: requireClass(styles.staleHint, "taskspanel.module.css", "staleHint"),
  expandedBody: requireClass(styles.expandedBody, "taskspanel.module.css", "expandedBody"),
  metaStrip: requireClass(styles.metaStrip, "taskspanel.module.css", "metaStrip"),
  metaKey: requireClass(styles.metaKey, "taskspanel.module.css", "metaKey"),
  metaValue: requireClass(styles.metaValue, "taskspanel.module.css", "metaValue"),
  times: requireClass(styles.times, "taskspanel.module.css", "times"),
  promptDetails: requireClass(styles.promptDetails, "taskspanel.module.css", "promptDetails"),
  promptSummary: requireClass(styles.promptSummary, "taskspanel.module.css", "promptSummary"),
  promptLabel: requireClass(styles.promptLabel, "taskspanel.module.css", "promptLabel"),
  promptChevron: requireClass(styles.promptChevron, "taskspanel.module.css", "promptChevron"),
  promptPreview: requireClass(styles.promptPreview, "taskspanel.module.css", "promptPreview"),
  promptBody: requireClass(styles.promptBody, "taskspanel.module.css", "promptBody"),
  notesHead: requireClass(styles.notesHead, "taskspanel.module.css", "notesHead"),
  noNotes: requireClass(styles.noNotes, "taskspanel.module.css", "noNotes"),
  notesRail: requireClass(styles.notesRail, "taskspanel.module.css", "notesRail"),
  note: requireClass(styles.note, "taskspanel.module.css", "note"),
};

// Mirrors the legacy sidebar/inline task-row grammar (cmd/evener-hub/assets/
// renderer-format.js: planGlyphForStatus/planStateClass) translated onto
// this app's own widget vocabulary (Chip tones) rather than the legacy's
// window.EvenerIcons SVG fragments, which this client has no equivalent of -
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

function hasTaskOutcomes(tasks: NonNullable<ThreadModel["tasks"]>): boolean {
  return tasks.cancelled !== undefined && tasks.remaining !== undefined;
}

function taskMeterValue(tasks: NonNullable<ThreadModel["tasks"]>): number {
  return tasks.cancelled !== undefined && tasks.remaining !== undefined ? tasks.done + tasks.cancelled : tasks.done;
}

export function taskAggregateLabel(tasks: NonNullable<ThreadModel["tasks"]>): string {
  if (hasTaskOutcomes(tasks)) {
    return `${tasks.done} done, ${tasks.cancelled} cancelled, ${tasks.remaining} remaining (${tasks.total} total)`;
  }
  return `${tasks.done}/${tasks.total}`;
}

function triggerLabel(tasks: ThreadModel["tasks"]): string {
  return tasks ? `Tasks ${taskAggregateLabel(tasks)}` : "Tasks";
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

// The expanded body: dense, one line per concern (spec §Expanded row). Each
// part omits itself when its data is absent rather than rendering an empty
// shell - a freshly appended task with no deps, no reasoning override, no
// notes and no prompt shows the meta strip and "No updates yet." only.
function TaskExpandedBody({ task, sessionRef }: { task: TaskRow; sessionRef: string }) {
  return (
    <div className={CLASS.expandedBody} data-testid="task-expanded">
      <TaskMetaStrip task={task} />
      <TaskTimestamps task={task} />
      <TaskPromptDisclosure task={task} sessionRef={sessionRef} />
      <TaskNotesTimeline task={task} />
    </div>
  );
}

function TaskMetaStrip({ task }: { task: TaskRow }) {
  const deps = task.dependsOn ?? [];
  return (
    <div className={CLASS.metaStrip} data-testid="task-meta">
      <span className={CLASS.metaKey}>type</span>
      <span className={CLASS.metaValue}>{task.type}</span>
      {task.reasoningEffort && (
        <>
          <span className={CLASS.metaKey}>reasoning</span>
          <span className={CLASS.metaValue}>{task.reasoningEffort}</span>
        </>
      )}
      {deps.length > 0 && (
        <>
          <span className={CLASS.metaKey}>depends</span>
          <span className={CLASS.metaValue}>{deps.map((id) => `#${id}`).join(" ")}</span>
        </>
      )}
    </div>
  );
}

function TaskTimestamps({ task }: { task: TaskRow }) {
  if (!task.createdAt) return null;
  const updatedAt = task.updatedAt;
  const showUpdated = updatedAt && updatedAt !== task.createdAt;
  return (
    <div className={CLASS.times} data-testid="task-times">
      <span>created {absoluteTime(task.createdAt)}</span>
      {showUpdated && (
        <span>
          updated <span title={absoluteTime(updatedAt)}>{relativeTime(updatedAt)}</span>
        </span>
      )}
      {task.status === "done" && task.completedAt && (
        <span>
          completed <span title={absoluteTime(task.completedAt)}>{relativeTime(task.completedAt)}</span>
        </span>
      )}
    </div>
  );
}

function TaskPromptDisclosure({ task, sessionRef }: { task: TaskRow; sessionRef: string }) {
  const id = `${taskDisclosureId(sessionRef, task.id)}\0prompt`;
  const open = isDisclosureOpen(id, false);
  if (task.prompt.trim() === "") return null;
  const firstLine = task.prompt.split("\n").find((line) => line.trim() !== "") ?? "";
  return (
    <details className={CLASS.promptDetails} data-testid="task-prompt" open={open}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; see SteeringItem.tsx */}
      <summary
        className={CLASS.promptSummary}
        data-testid="task-prompt-summary"
        onClick={(e) => {
          e.preventDefault();
          toggleDisclosure(id, false);
        }}
      >
        <span className={CLASS.promptLabel}>Prompt</span>
        <span className={CLASS.promptChevron} aria-hidden="true" data-open={open ? "true" : "false"}>
          ▸
        </span>
        <span className={CLASS.promptPreview}>
          <Markdown source={firstLine} />
        </span>
      </summary>
      {open && (
        <div className={CLASS.promptBody} data-testid="task-prompt-body">
          <Markdown source={task.prompt} />
        </div>
      )}
    </details>
  );
}

function TaskNotesTimeline({ task }: { task: TaskRow }) {
  const notes = task.notes ?? [];
  if (notes.length === 0) {
    return (
      <div className={CLASS.noNotes} data-testid="task-notes-empty">
        No updates yet.
      </div>
    );
  }
  return (
    <div data-testid="task-notes">
      <div className={CLASS.notesHead}>Updates · {notes.length}</div>
      <ol className={CLASS.notesRail}>
        {notes.map((note, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: notes only ever append over a task's life (agent/task/task_store.go's update handling) - position is stable identity
          <li key={i} className={CLASS.note} data-latest={i === notes.length - 1 ? "true" : undefined}>
            {note}
          </li>
        ))}
      </ol>
    </div>
  );
}

// Settled rows (the collapsed history group) render one line, dimmed, with
// no latest-update excerpt: history costs one line per task. Live rows earn
// their second line with the most recent note.
function TaskRowView({ task, sessionRef, settled = false }: { task: TaskRow; sessionRef: string; settled?: boolean }) {
  const notes = task.notes ?? [];
  const latest = !settled && notes.length > 0 ? notes[notes.length - 1] : null;
  const descClass = task.status === "cancelled" ? CLASS.descStruck : settled ? CLASS.descDim : CLASS.description;
  const summary = (
    <>
      <Chip tone={STATUS_TONE[task.status]}>{STATUS_GLYPH[task.status]}</Chip>
      <span className={CLASS.summaryMain}>
        <span className={CLASS.summaryLine}>
          <span className={descClass} data-struck={task.status === "cancelled" ? "true" : undefined}>
            {task.description}
          </span>
          {task.updatedAt && (
            <span className={CLASS.time} data-testid="task-row-time" title={absoluteTime(task.updatedAt)}>
              {relativeTime(task.updatedAt)}
            </span>
          )}
        </span>
        {latest && (
          <span className={CLASS.latest} data-testid="task-latest">
            <span className={CLASS.latestLabel}>latest</span>
            <span className={CLASS.latestText} title={latest}>
              {latest}
            </span>
          </span>
        )}
      </span>
    </>
  );
  return (
    // No className here: Disclosure's own .summary/.body already lay out
    // the full row width - this <li> exists only to keep the <ul>'s
    // children real <li>s, the list semantics screen readers rely on.
    <li data-testid="task-row">
      <Disclosure id={taskDisclosureId(sessionRef, task.id)} summary={summary}>
        <TaskExpandedBody task={task} sessionRef={sessionRef} />
      </Disclosure>
    </li>
  );
}

function LiveGroup({
  label,
  status,
  tasks,
  sessionRef,
}: {
  label: string;
  status: string;
  tasks: TaskRow[];
  sessionRef: string;
}) {
  if (tasks.length === 0) return null;
  return (
    <section data-testid="task-group-live" data-status={status}>
      <h4 className={CLASS.groupHead}>
        {label} <span className={CLASS.groupCount}>{tasks.length}</span>
      </h4>
      <ul className={CLASS.list}>
        {tasks.map((row) => (
          <TaskRowView key={row.id} task={row} sessionRef={sessionRef} />
        ))}
      </ul>
    </section>
  );
}

function TaskListGroups({ rows, sessionRef }: { rows: TaskRow[]; sessionRef: string }) {
  const groups = groupTasks(rows);
  return (
    <>
      <LiveGroup label="In progress" status="in_progress" tasks={groups.inProgress} sessionRef={sessionRef} />
      <LiveGroup label="Open" status="open" tasks={groups.open} sessionRef={sessionRef} />
      {groups.settled.length > 0 && (
        <Disclosure
          id={`${sessionRef}\0settled-group`}
          summary={
            <span className={CLASS.settledSummary} data-testid="task-settled-group-summary">
              Done · settled <span className={CLASS.groupCount}>{groups.settled.length}</span>
            </span>
          }
          data-testid="task-settled-group"
        >
          <ul className={CLASS.list}>
            {groups.settled.map((row) => (
              <TaskRowView key={row.id} task={row} sessionRef={sessionRef} settled />
            ))}
          </ul>
        </Disclosure>
      )}
    </>
  );
}

export interface TasksPanelBodyProps {
  sessionRef: string;
  model: ThreadModel;
}

/** Shared task-list body used by both the mobile Sheet and desktop pane. */
export function TasksPanelBody({ sessionRef, model }: TasksPanelBodyProps) {
  const toasts = useToasts();
  const entry = useTasksPanelStore((state) => state.entries.get(sessionRef)) ?? EMPTY_TASKS_PANEL_ENTRY;
  const mountedRef = useRef(true);
  const currentSessionRef = useRef(sessionRef);
  currentSessionRef.current = sessionRef;

  useEffect(
    () => () => {
      mountedRef.current = false;
    },
    [],
  );
  // Bumped by Try again. The only fetch trigger a reader controls: the other
  // two are opening the panel and a push arriving, and neither is available
  // to someone looking at a failed fetch in an open panel on a quiet session.
  const [reloads, setReloads] = useState(0);

  // Fetches on mount, on every Try again, and again whenever model.tasks
  // changes while the body is mounted (a live evener/task/updated push while
  // the user is looking) - see this file's own header comment. `toasts` is
  // deliberately not a dependency: useToasts() returns a fresh wrapper object
  // every render (see widgets/toast/index.tsx), so depending on it would
  // refire this effect on every unrelated re-render; toasts.push itself is a
  // stable, module-level function underneath.
  // biome-ignore lint/correctness/useExhaustiveDependencies: toasts is a fresh wrapper object every render (see above) - toasts.push itself is stable
  useEffect(() => {
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
        const accepted = tasksPanelStore.getState().publishFetch(sessionRef, fetchID, { kind: "failure", failure });
        // A rejected (obsolete) failure must not toast either: a newer fetch
        // already superseded this one, possibly with rows now on screen.
        if (accepted && mountedRef.current && currentSessionRef.current === sessionRef) {
          toasts.push("error", failure.sentence);
        }
      });
  }, [model.tasks, sessionRef, reloads]);

  function reload() {
    setReloads((n) => n + 1);
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
        {model.tasks && (
          <div className={CLASS.bodyHead} data-testid="tasks-body-head">
            <Meter
              label={
                hasTaskOutcomes(model.tasks)
                  ? `Task progress: ${taskAggregateLabel(model.tasks)}`
                  : `Task progress: ${model.tasks.done} of ${model.tasks.total} complete`
              }
              value={taskMeterValue(model.tasks)}
              max={model.tasks.total}
              tone="neutral"
            />
            <span className={CLASS.count}>
              {hasTaskOutcomes(model.tasks)
                ? taskAggregateLabel(model.tasks)
                : `${model.tasks.done}/${model.tasks.total} done`}
            </span>
          </div>
        )}
        {rows.length === 0 ? (
          <EmptyState title="No tasks yet" hint="The agent's task list is empty for this session." />
        ) : (
          <TaskListGroups rows={rows} sessionRef={sessionRef} />
        )}
      </>
    );
  }

  return renderBody();
}

export const TasksPanel = forwardRef<TasksPanelHandle, TasksPanelProps>(function TasksPanel(
  { sessionRef, model, hideTrigger = false },
  ref,
) {
  const [open, setOpen] = useState(false);
  useImperativeHandle(ref, () => ({ open: () => setOpen(true) }), []);

  return (
    <>
      {/* Omitted while hideTrigger is set (the row collapsed this into the
          "..." menu instead - see SessionChrome). The palette's /tasks no
          longer clicks this trigger; it toggles the sessionTasks workspace
          pane (shell/palette/commands.ts toggleSessionPane). */}
      {!hideTrigger && (
        <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
          {triggerLabel(model.tasks)}
        </Button>
      )}
      <Sheet open={open} onClose={() => setOpen(false)} title="Tasks">
        {open ? <TasksPanelBody sessionRef={sessionRef} model={model} /> : null}
      </Sheet>
    </>
  );
});
