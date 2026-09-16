// The tasks-panel store: a framework-free store holding, per session ref, the
// task list a panel last loaded and the state of the fetch that loaded it.
// createTasksPanelStore is a factory - each app builds the instance it wraps
// in its own view layer, and tests build their own - returning the store
// triple plus the store-bound actions. The store owns the whole fetch: it
// reads through the `TasksListRead` port the app supplies, classifies what
// came back (rows, an empty list, an unsupported source, a daemon that has
// gone, or a failure) and publishes it, dropping any completion a newer fetch
// or an eviction has superseded. Pure logic - no DOM, no React.

import type { AppwireClient } from "./client";
import { errorText, sessionActionError, sessionActionHeadline } from "./errors";
import { isActionUnavailable, isThreadNotFound } from "./sessionErrors";
import { parseTaskListData, type TaskRow } from "./taskListData";

/** One failure in the two shapes a panel renders it: `headline` and `detail`
 * for a title-and-hint empty state, `sentence` for the single-string cases (a
 * toast, a stale notice above a retained list). All three come off the same
 * rejection, so a panel's reports of one failure cannot drift apart. */
export interface PanelLoadFailure {
  headline: string;
  detail?: string;
  sentence: string;
}

export interface TasksPanelEntry {
  /** null until a list has loaded; [] is a confirmed-empty list. */
  rows: TaskRow[] | null;
  /** The source cannot answer evener/tasks/list at all. */
  unsupported: boolean;
  /** The session's daemon has exited while the trigger already claimed tasks
   * exist; the retained rows are the last list that will ever load. */
  daemonGone: boolean;
  failure: PanelLoadFailure | null;
  loading: boolean;
  fetchID: number;
}

export type TasksFetchResult =
  | { kind: "rows"; rows: TaskRow[] }
  | { kind: "unsupported" }
  | { kind: "daemon-gone" }
  | { kind: "empty" }
  | { kind: "failure"; failure: PanelLoadFailure };

/** The evener/tasks/list read, resolving to the response's raw `data`. The
 * web routes it through its threads store so the read waits out a reconnect;
 * native reads straight off its client. */
export type TasksListRead = (ref: string) => Promise<unknown>;

/** The push feed `watch` follows for the two notifications that invalidate a
 * session's task list. */
export type TasksPanelNotifications = Pick<AppwireClient, "onNotification">;

export interface TasksPanelState {
  entries: Map<string, TasksPanelEntry>;
  /** Marks the entry loading and returns the id its completion must carry. */
  beginFetch(ref: string): number;
  /** Returns whether the result was accepted - false when a newer fetch has
   * superseded this one or the entry was evicted, so the caller must not
   * surface it either. */
  publishFetch(ref: string, fetchID: number, result: TasksFetchResult): boolean;
  setRows(ref: string, rows: TaskRow[]): void;
  evict(ref: string): void;
  resetForTests(): void;
}

/** Runs after every state change with the new state and the one it replaced
 * - the listener shape zustand's useStore subscribes with. */
export type TasksPanelListener = (state: TasksPanelState, previous: TasksPanelState) => void;

export interface TasksPanelStore {
  getState(): TasksPanelState;
  /** The state the store was created with: the snapshot a view binding
   * (React's useSyncExternalStore, zustand's useStore) reads before its first
   * subscription. */
  getInitialState(): TasksPanelState;
  /** Shallow-merges the partial (or the updater's result) into the state and
   * notifies every subscriber, even when nothing changed. */
  setState(partial: Partial<TasksPanelState> | ((state: TasksPanelState) => Partial<TasksPanelState>)): void;
  /** Returns the unsubscribe function. */
  subscribe(listener: TasksPanelListener): () => void;
  /**
   * Loads the session's task list through the read port and publishes the
   * outcome. Overlapping calls for one ref coalesce: a call that arrives
   * while a run is in flight marks it dirty, and the run drops the answer
   * to the now-stale read and reads once more after it settles instead of
   * issuing a concurrent request.
   * Resolves to the published result for the caller that started the run,
   * and to null for a caller folded into an in-flight run or whose result
   * was superseded - so exactly one caller can react (toast) to a failure.
   * `hasAggregate` is read when a "thread not found" rejection arrives: true
   * means the trigger beside the panel already claims tasks exist, so the
   * rejection is the daemon having gone rather than an empty list.
   */
  refresh(ref: string, hasAggregate: () => boolean): Promise<TasksFetchResult | null>;
  /** Loads the list now and again on every evener/task/updated or
   * evener/thread/resync for exactly this ref and thread. Returns the stop
   * function. */
  watch(notifications: TasksPanelNotifications, ref: string, threadId: string, hasAggregate: () => boolean): () => void;
}

export const EMPTY_TASKS_PANEL_ENTRY: TasksPanelEntry = {
  rows: null,
  unsupported: false,
  daemonGone: false,
  failure: null,
  loading: false,
  fetchID: 0,
};

// The one name this panel's failure goes by on both apps. Both reports of it
// - a toast and the inline state - are built from this and from the same
// discriminator (errors.ts), so a failed session resume takes over both or
// neither; a panel can never say two different things about one failure.
const LOAD_FAILURE = "Couldn't load tasks";

/** The three renderings of one rejection. A rejection carrying no text of its
 * own leaves the detail out entirely, the same way sessionActionError drops
 * the separator. */
export function tasksLoadFailure(err: unknown): PanelLoadFailure {
  const headline = sessionActionHeadline(LOAD_FAILURE, err);
  const sentence = sessionActionError(LOAD_FAILURE, err);
  const detail = errorText(err).trim();
  return detail ? { headline, detail, sentence } : { headline, sentence };
}

/** A resolved evener/tasks/list: rows, or unsupported when the data cannot be
 * read as a task list (an old daemon with no task store answers null rather
 * than rejecting - nothing went wrong at the transport level). */
export function classifyTasksResponse(data: unknown): TasksFetchResult {
  const rows = parseTaskListData(data);
  return rows === null ? { kind: "unsupported" } : { kind: "rows", rows };
}

/**
 * A rejected evener/tasks/list. A source that omits the capability rejects
 * with actionUnavailable: unsupported, not a failure. A thread with no live
 * daemon behind it rejects with "thread not found": an empty list when no
 * aggregate was ever pushed (the panel truly cannot tell "never had tasks"
 * from "can't currently ask"), the daemon having gone once one was (the
 * trigger is already on screen claiming tasks exist, so "No tasks yet" would
 * contradict it). Anything else is a genuine failure.
 */
export function classifyTasksRejection(err: unknown, hasAggregate: boolean): TasksFetchResult {
  if (isActionUnavailable(err)) return { kind: "unsupported" };
  if (isThreadNotFound(err)) return hasAggregate ? { kind: "daemon-gone" } : { kind: "empty" };
  return { kind: "failure", failure: tasksLoadFailure(err) };
}

/** The entry after a completed fetch. Rows survive a failure and a gone
 * daemon - a list one push out of date beats a blank panel - and are cleared
 * only by an unsupported answer, which says the source has no list at all. */
export function applyTasksFetchResult(entry: TasksPanelEntry, result: TasksFetchResult): TasksPanelEntry {
  const settled = { ...entry, unsupported: false, daemonGone: false, failure: null, loading: false };
  switch (result.kind) {
    case "rows":
      return { ...settled, rows: result.rows };
    case "empty":
      return { ...settled, rows: [] };
    case "unsupported":
      return { ...settled, rows: null, unsupported: true };
    case "daemon-gone":
      return { ...settled, daemonGone: true };
    case "failure":
      return { ...settled, failure: result.failure };
  }
}

interface RefreshRun {
  dirty: boolean;
  hasAggregate: () => boolean;
  settled: Promise<TasksFetchResult | null>;
}

/** Builds an empty store that reads task lists through `listTasks`. */
export function createTasksPanelStore(listTasks: TasksListRead): TasksPanelStore {
  const listeners = new Set<TasksPanelListener>();
  let state: TasksPanelState;
  const get = (): TasksPanelState => state;
  const set: TasksPanelStore["setState"] = (partial) => {
    const previous = state;
    state = { ...state, ...(typeof partial === "function" ? partial(state) : partial) };
    for (const listener of listeners) listener(state, previous);
  };

  // Monotonic across every entry and NOT stored per-entry: an entry recreated
  // after eviction must never hand out an id that a request begun in its
  // previous life could still complete with (publishFetch matches on fetchID).
  let nextFetchID = 0;
  const runs = new Map<string, RefreshRun>();

  const updateEntry = (ref: string, update: (entry: TasksPanelEntry) => TasksPanelEntry): void => {
    set((s) => {
      const next = new Map(s.entries);
      next.set(ref, update(s.entries.get(ref) ?? { ...EMPTY_TASKS_PANEL_ENTRY }));
      return { entries: next };
    });
  };

  state = {
    entries: new Map(),

    beginFetch(ref) {
      const fetchID = ++nextFetchID;
      updateEntry(ref, (entry) => ({
        ...entry,
        loading: true,
        unsupported: false,
        daemonGone: false,
        failure: null,
        fetchID,
      }));
      return fetchID;
    },

    publishFetch(ref, fetchID, result) {
      const current = get().entries.get(ref);
      if (!current || current.fetchID !== fetchID) return false;
      updateEntry(ref, (entry) => applyTasksFetchResult(entry, result));
      return true;
    },

    setRows(ref, rows) {
      updateEntry(ref, (entry) => applyTasksFetchResult(entry, { kind: "rows", rows }));
    },

    evict(ref) {
      set((s) => {
        if (!s.entries.has(ref)) return s;
        const entries = new Map(s.entries);
        entries.delete(ref);
        return { entries };
      });
    },

    resetForTests() {
      nextFetchID = 0;
      set({ entries: new Map() });
    },
  };
  const initialState = state;

  const refresh: TasksPanelStore["refresh"] = (ref, hasAggregate) => {
    const inFlight = runs.get(ref);
    if (inFlight) {
      inFlight.dirty = true;
      inFlight.hasAggregate = hasAggregate;
      return inFlight.settled.then(() => null);
    }
    const run: RefreshRun = { dirty: false, hasAggregate, settled: Promise.resolve(null) };
    runs.set(ref, run);
    run.settled = (async () => {
      let published: TasksFetchResult | null = null;
      do {
        run.dirty = false;
        const fetchID = get().beginFetch(ref);
        let result: TasksFetchResult;
        try {
          result = classifyTasksResponse(await listTasks(ref));
        } catch (err) {
          result = classifyTasksRejection(err, run.hasAggregate());
        }
        // A read a newer trigger has already made stale is not shown even
        // briefly; the loop reads again and publishes that answer instead.
        if (!run.dirty) published = get().publishFetch(ref, fetchID, result) ? result : null;
      } while (run.dirty);
      runs.delete(ref);
      return published;
    })();
    return run.settled;
  };

  return {
    getState: get,
    getInitialState: () => initialState,
    setState: set,
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    refresh,
    watch(notifications, ref, threadId, hasAggregate) {
      const stop = notifications.onNotification((n) => {
        if (
          (n.method === "evener/task/updated" || n.method === "evener/thread/resync") &&
          n.params.ref === ref &&
          n.params.threadId === threadId
        ) {
          void refresh(ref, hasAggregate);
        }
      });
      void refresh(ref, hasAggregate);
      return stop;
    },
  };
}
