import {
  isActionUnavailable,
  isThreadNotFound,
} from "../../cmd/evener-hub/frontend/src/panes/session/chrome/sessionErrors";
import {
  parseTaskListData,
  type TaskRow,
} from "../../cmd/evener-hub/frontend/src/panes/session/chrome/taskData";
import { sessionActionError } from "../../cmd/evener-hub/frontend/src/protocol/errors";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface TaskListState {
  rows: TaskRow[] | null;
  loading: boolean;
  unsupported: boolean;
  daemonGone: boolean;
  error: string | null;
}

/** Own a task panel's fetches and notifications for one hub/session lifetime. */
export class TaskList {
  private state: TaskListState = {
    rows: null,
    loading: false,
    unsupported: false,
    daemonGone: false,
    error: null,
  };
  private listeners = new Set<() => void>();
  private unsubscribe?: () => void;
  private disposed = false;
  private dirty = false;
  private inFlight?: Promise<void>;
  constructor(
    private client: ConversationClientLike,
    private ref: string,
    private threadId: string,
    private hasAggregate: () => boolean,
    rows: TaskRow[] | null = null,
  ) {
    this.state.rows = rows;
  }
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<TaskListState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener();
  }
  start() {
    if (this.disposed || this.unsubscribe) return;
    this.unsubscribe = this.client.onNotification((n) => {
      if (
        (n.method === "evener/task/updated" ||
          n.method === "evener/thread/resync") &&
        n.params.ref === this.ref &&
        n.params.threadId === this.threadId
      )
        void this.refresh();
    });
    void this.refresh();
  }
  refresh = (): Promise<void> => {
    if (this.disposed) return Promise.resolve();
    if (this.inFlight) {
      this.dirty = true;
      return this.inFlight;
    }
    this.inFlight = this.load().finally(() => {
      this.inFlight = undefined;
    });
    return this.inFlight;
  };
  private async load() {
    this.publish({
      loading: true,
      error: null,
      unsupported: false,
      daemonGone: false,
    });
    do {
      this.dirty = false;
      try {
        const result = await this.client.request("evener/tasks/list", {
          ref: this.ref,
        });
        if (!this.dirty && !this.disposed) {
          const rows = parseTaskListData((result as { data: unknown }).data);
          this.publish({
            rows,
            unsupported: rows === null,
            error: null,
            daemonGone: false,
          });
        }
      } catch (error) {
        if (!this.dirty && !this.disposed) {
          if (isActionUnavailable(error))
            this.publish({ rows: null, unsupported: true });
          else if (isThreadNotFound(error))
            this.publish(
              this.hasAggregate() ? { daemonGone: true } : { rows: [] },
            );
          else
            this.publish({
              error: sessionActionError("Could not load tasks", error),
            });
        }
      }
    } while (this.dirty && !this.disposed);
    this.publish({ loading: false });
  }
  dispose() {
    this.disposed = true;
    this.unsubscribe?.();
    this.listeners.clear();
  }
}
