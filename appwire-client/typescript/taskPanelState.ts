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
import { createFrameworkFreeStore, type FrameworkFreeStore, type StoreListener } from "./frameworkFreeStore";
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
   * superseded this one or the entry was evicted. */
  publishFetch(ref: string, fetchID: number, result: TasksFetchResult): boolean;
  setRows(ref: string, rows: TaskRow[]): void;
  evict(ref: string): void;
  resetForTests(): void;
}

export type TasksPanelListener = StoreListener<TasksPanelState>;

export interface TasksPanelStore extends FrameworkFreeStore<TasksPanelState> {
  /**
   * Loads the session's task list through the read port and publishes the
   * outcome. Overlapping calls for one ref coalesce: a call that arrives
   * while a run is in flight marks it dirty, and the run drops the answer
   * to the now-stale read and reads once more after it settles instead of
   * issuing a concurrent request.
   * Resolves to the published result for the run's latest caller - the one
   * whose `hasAggregate` the run classified with - and to null for every
   * earlier caller once the run settles, so exactly one caller reacts
   * (toasts) to a failure and it is the one still current. `hasAggregate` is
   * read when a "thread not found" rejection arrives: true means the trigger
   * beside the panel already claims tasks exist, so the rejection is the
   * daemon having gone rather than an empty list.
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

/** The three renderings of one rejection under the panel's one failure name
 * (`failure`, e.g. "Couldn't load tasks"). A rejection carrying no text of its
 * own leaves the detail out entirely, the same way sessionActionError drops
 * the separator. */
export function panelLoadFailure(failure: string, err: unknown): PanelLoadFailure {
  const headline = sessionActionHeadline(failure, err);
  const sentence = sessionActionError(failure, err);
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
  return { kind: "failure", failure: panelLoadFailure(LOAD_FAILURE, err) };
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

interface RefreshWaiter {
  resolve: (result: TasksFetchResult | null) => void;
  reject: (error: unknown) => void;
}

interface RefreshRun {
  dirty: boolean;
  hasAggregate: () => boolean;
  /** One per caller, in call order; the last is the run's owner. */
  waiters: RefreshWaiter[];
}

/** Hands the run's outcome to its callers: the owner (latest caller) gets
 * the published result, every earlier caller null; a thrown port (a host's
 * hasAggregate) rejects all of them. */
function settleRun(run: RefreshRun, outcome: { published: TasksFetchResult | null } | { error: unknown }): void {
  const waiters = run.waiters.splice(0);
  const owner = waiters.pop();
  if ("error" in outcome) {
    for (const waiter of waiters) waiter.reject(outcome.error);
    owner?.reject(outcome.error);
    return;
  }
  for (const waiter of waiters) waiter.resolve(null);
  owner?.resolve(outcome.published);
}

/** Builds an empty store that reads task lists through `listTasks`. */
export function createTasksPanelStore(listTasks: TasksListRead): TasksPanelStore {
  // Monotonic across every entry and NOT stored per-entry: an entry recreated
  // after eviction must never hand out an id that a request begun in its
  // previous life could still complete with (publishFetch matches on fetchID).
  let nextFetchID = 0;
  const runs = new Map<string, RefreshRun>();

  const store = createFrameworkFreeStore<TasksPanelState>((set, get) => {
    const updateEntry = (ref: string, update: (entry: TasksPanelEntry) => TasksPanelEntry): void => {
      set((s) => {
        const next = new Map(s.entries);
        next.set(ref, update(s.entries.get(ref) ?? EMPTY_TASKS_PANEL_ENTRY));
        return { entries: next };
      });
    };

    return {
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
        if (!get().entries.has(ref)) return;
        set((s) => {
          const entries = new Map(s.entries);
          entries.delete(ref);
          return { entries };
        });
      },

      resetForTests() {
        nextFetchID = 0;
        // A run still reading is abandoned: its callers settle now (null)
        // and, when the read returns, it finds itself no longer registered
        // and publishes nothing into the reset store.
        for (const run of runs.values()) settleRun(run, { published: null });
        runs.clear();
        set({ entries: new Map() });
      },
    };
  });
  const { getState: get } = store;

  const runLoop = async (ref: string, run: RefreshRun): Promise<void> => {
    let published: TasksFetchResult | null = null;
    let fetchID = 0;
    try {
      do {
        run.dirty = false;
        fetchID = get().beginFetch(ref);
        let result: TasksFetchResult;
        try {
          result = classifyTasksResponse(await listTasks(ref));
        } catch (err) {
          result = classifyTasksRejection(err, run.hasAggregate());
        }
        // resetForTests abandoned this run while it was reading.
        if (runs.get(ref) !== run) return;
        // A read a newer trigger has already made stale is not shown even
        // briefly; the loop reads again and publishes that answer instead.
        if (!run.dirty) published = get().publishFetch(ref, fetchID, result) ? result : null;
      } while (run.dirty);
    } catch (error) {
      // A host port threw (hasAggregate, most likely): the callers reject,
      // and the entry beginFetch marked loading settles as a failure so the
      // panel shows the error with its retry instead of spinning.
      if (runs.get(ref) === run) {
        runs.delete(ref);
        get().publishFetch(ref, fetchID, { kind: "failure", failure: panelLoadFailure(LOAD_FAILURE, error) });
      }
      settleRun(run, { error });
      return;
    }
    runs.delete(ref);
    settleRun(run, { published });
  };

  const refresh: TasksPanelStore["refresh"] = (ref, hasAggregate) => {
    const inFlight = runs.get(ref);
    if (inFlight) {
      inFlight.dirty = true;
      inFlight.hasAggregate = hasAggregate;
      return new Promise((resolve, reject) => inFlight.waiters.push({ resolve, reject }));
    }
    const run: RefreshRun = { dirty: false, hasAggregate, waiters: [] };
    runs.set(ref, run);
    // The caller is registered before the loop starts: a read that throws
    // synchronously completes the whole run before runLoop returns.
    const settled = new Promise<TasksFetchResult | null>((resolve, reject) => run.waiters.push({ resolve, reject }));
    void runLoop(ref, run);
    return settled;
  };

  return {
    ...store,
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
