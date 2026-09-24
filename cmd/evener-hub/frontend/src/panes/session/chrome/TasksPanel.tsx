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
//     refires on an unrelated model update). The package's
//     parseTaskListData (taskListData.ts, pinned wire-true against
//     agent/task/task_store.go's real Task shape) owns interpreting the raw
//     `unknown` response.
//
// Failure handling: a source-backed thread may reject the wire call outright
// (appwire.Unavailable, "actionUnavailable" - verified against
// a source that omits the capability) - an expected capability gap, not a bug, so it
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

import type { ThreadModel } from "@evener/appwire-client";
import {
  absoluteTime,
  EMPTY_TASKS_PANEL_ENTRY,
  groupTasks,
  relativeTime,
  type TaskRow,
  taskAggregateLabel,
} from "@evener/appwire-client";
import { forwardRef, useEffect, useImperativeHandle, useState } from "react";
import { tasksPanelStore, useTasksPanelStore } from "../../../stores/tasksPanel";
import { Button, EmptyState, Markdown, Sheet, useToasts } from "../../../widgets";
import { Disclosure } from "../../../widgets/disclosure";
import { isDisclosureOpen, toggleDisclosure } from "../../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../../widgets/internal/requireClass";
import { STATUS_TOUCH, TaskCheck, TOUCH_WORD } from "../transcript/tools/taskCheck";
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
  srOnly: requireClass(styles.srOnly, "taskspanel.module.css", "srOnly"),
};

function triggerLabel(tasks: ThreadModel["tasks"]): string {
  // Bare sentence, no "Tasks" prefix: the aggregate already names the noun
  // ("4 of 7 tasks left"), so a prefix would stutter ("Tasks 4 of 7 tasks
  // left"). Bare "Tasks" remains the pre-aggregate fallback.
  return tasks ? taskAggregateLabel(tasks) : "Tasks";
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
        <span className={CLASS.promptPreview}>
          <Markdown source={firstLine} />
        </span>
        <span className={CLASS.promptChevron} aria-hidden="true" data-open={open ? "true" : "false"}>
          ▸
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
      <TaskCheck touch={STATUS_TOUCH[task.status]} />
      <span className={CLASS.summaryMain}>
        <span className={CLASS.summaryLine}>
          {/* The glyph is aria-hidden by design; its status word rides
              visually-hidden beside the label, the same word the card's
              rows carry. */}
          <span className={CLASS.srOnly}>{TOUCH_WORD[STATUS_TOUCH[task.status]]}</span>
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
      <LiveGroup label="In progress" status="in_progress" tasks={groups.inProgress} sessionRef={sessionRef} />
      <LiveGroup label="Open" status="open" tasks={groups.open} sessionRef={sessionRef} />
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
    // The store classifies the outcome (unsupported / empty / daemon gone /
    // failure - see this file's header and taskPanelState.ts) and keeps the
    // retained rows under a failure or a gone daemon. It resolves the result
    // only to its latest caller, so an obsolete failure never toasts over
    // rows a newer fetch has since put on screen; `live` covers the one case
    // left, this effect being cleaned up (unmount or a new session) while
    // its fetch is the latest.
    let live = true;
    void tasksPanelStore
      .refresh(sessionRef, () => model.tasks !== null)
      .then((result) => {
        if (live && result?.kind === "failure") toasts.push("error", result.failure.sentence);
      })
      // A thrown port rejects after the store has already settled the entry
      // as a failure, which renderBody shows with Try again; nothing to add.
      .catch(() => undefined);
    return () => {
      live = false;
    };
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
            <span className={CLASS.count}>{taskAggregateLabel(model.tasks)}</span>
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
