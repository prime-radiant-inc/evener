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
// found" (isThreadNotFound, below) - that folds into the SAME empty list a
// real `[]` response renders, since the frontend has no way to distinguish
// "never had tasks" from "can't currently ask", and the former is the
// common case. Any other rejection - including the SAME sessionUnavailable
// code for a daemon that's merely unreachable this instant - is a genuine
// failure: toast (the wave's failure-feedback convention) AND an inline
// report.
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
import { forwardRef, useEffect, useImperativeHandle, useState } from "react";
import { errorText, sessionActionError, sessionActionHeadline, WireError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, type ChipTone, EmptyState, Sheet, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
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
  row: requireClass(styles.row, "taskspanel.module.css", "row"),
  description: requireClass(styles.description, "taskspanel.module.css", "description"),
  stale: requireClass(styles.stale, "taskspanel.module.css", "stale"),
  staleMessage: requireClass(styles.staleMessage, "taskspanel.module.css", "staleMessage"),
  staleHint: requireClass(styles.staleHint, "taskspanel.module.css", "staleHint"),
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

function isActionUnavailable(err: unknown): boolean {
  return err instanceof WireError && err.serfErrorInfo === "actionUnavailable";
}

// A local-source thread with no live daemon behind it (a one-shot CLI
// session that already exited, or one never resumed) rejects ListTasks with
// appwire.SessionUnavailable("thread not found: " + threadID) - the ONLY
// call site that prefixes a sessionUnavailable message this way
// (cmd/serf-hub/internal/appsource/local_daemon.go:551, entryForRef finding
// no matching rendezvous entry). A live daemon that's merely unreachable
// for a moment (connection reset, broken pipe, i/o timeout) is ALSO
// sessionUnavailable, but as "local/codex daemon unavailable: ..."
// (local_daemon.go:438-501, codex_source.go:522-591) - that must still
// surface as a real error, so this checks the message prefix too, not just
// the serfErrorInfo code.
function isThreadNotFound(err: unknown): boolean {
  return (
    err instanceof WireError &&
    err.serfErrorInfo === "sessionUnavailable" &&
    err.message.startsWith("thread not found: ")
  );
}

function TaskRowView({ task }: { task: TaskRow }) {
  return (
    <li className={CLASS.row} data-testid="task-row">
      <Chip tone={STATUS_TONE[task.status]}>{STATUS_GLYPH[task.status]}</Chip>
      <span className={CLASS.description}>{task.description}</span>
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
  const [rows, setRows] = useState<TaskRow[] | null>(null);
  const [unsupported, setUnsupported] = useState(false);
  const [error, setError] = useState<LoadFailure | null>(null);
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
    let cancelled = false;
    setError(null);
    setUnsupported(false);
    threadsStore
      .getState()
      .listTasks(sessionRef)
      .then((data) => {
        if (cancelled) return;
        const parsed = parseTaskListData(data);
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
          setRows([]);
          return;
        }
        // `rows` is deliberately left alone: whatever the last fetch that
        // did resolve put there stays on screen under the stale notice.
        // Only a panel that has never had a list renders the error state on
        // its own, and that is exactly the `rows === null` case below.
        const failure = loadFailure(err);
        setError(failure);
        toasts.push("error", failure.sentence);
      });
    return () => {
      cancelled = true;
    };
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
    if (unsupported) {
      // No Try again here: the source cannot answer this call at all, so
      // asking again would only fail again.
      return (
        <EmptyState title="Task list isn't available" hint="This session's source doesn't support the task list." />
      );
    }
    if (rows === null) {
      if (error) return <EmptyState title={error.headline} hint={error.detail} action={retry} />;
      return <p className={CLASS.state}>Loading tasks…</p>;
    }
    return (
      <>
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
              <TaskRowView key={row.id} task={row} />
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
