import { parseJobLogTail } from "../../cmd/evener-hub/frontend/src/panes/transcript/jobLogData";
import { sessionActionError } from "../../cmd/evener-hub/frontend/src/protocol/errors";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface OutputState {
  content: string | null;
  error: string | null;
  hasEarlier: boolean;
  earliestStart: number;
  totalBytes: number;
  loading: boolean;
}
export class JobOutput {
  private state: OutputState;
  private disposed = false;
  private listeners = new Set<() => void>();
  private inFlight?: Promise<void>;
  constructor(
    private client: ConversationClientLike,
    private ref: string,
    private jobId: string,
    retained?: OutputState,
  ) {
    this.state = retained
      ? { ...retained, loading: false }
      : {
          content: null,
          error: null,
          hasEarlier: false,
          earliestStart: 0,
          totalBytes: 0,
          loading: false,
        };
  }
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<OutputState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener();
  }
  refresh = () => this.run();
  loadEarlier = () =>
    this.state.hasEarlier && this.state.earliestStart > 0
      ? this.run(this.state.earliestStart)
      : Promise.resolve();
  private run(beforeBytes?: number): Promise<void> {
    if (this.disposed) return Promise.resolve();
    if (this.inFlight) return this.inFlight;
    this.inFlight = this.load(beforeBytes).finally(() => {
      this.inFlight = undefined;
    });
    return this.inFlight;
  }
  private async load(beforeBytes?: number) {
    this.publish({ loading: true, error: null });
    try {
      const response = await this.client.request("evener/jobs/output", {
        ref: this.ref,
        jobId: this.jobId,
        ...(beforeBytes === undefined ? {} : { beforeBytes }),
      });
      const page = parseJobLogTail((response as { data: unknown }).data);
      if (!page) throw new Error("Invalid job output response");
      if (beforeBytes !== undefined && page.retainedStart >= beforeBytes) {
        this.publish({ hasEarlier: false });
      } else
        this.publish({
          content:
            page.tail +
            (beforeBytes === undefined ? "" : (this.state.content ?? "")),
          earliestStart: page.retainedStart,
          totalBytes: page.totalBytes,
          hasEarlier: page.hasEarlier && page.retainedStart > 0,
        });
    } catch (error) {
      this.publish({
        error: sessionActionError("Could not load output", error),
      });
    }
    this.publish({ loading: false });
  }
  dispose() {
    this.disposed = true;
    this.listeners.clear();
  }
}
