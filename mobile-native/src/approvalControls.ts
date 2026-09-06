import type { MobileApproval } from "../../mobile/src/conversation/model";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

/** Decisions belong to the displayed approval and one live session binding. */
export class ApprovalControls {
  private state = {
    pending: null as string | null,
    error: null as string | null,
  };
  private disposed = false;
  private listeners = new Set<() => void>();
  constructor(
    private client: ConversationClientLike,
    private ref: string,
    private approvals: () => MobileApproval[],
    private current: () => boolean,
    private refresh: () => Promise<void>,
  ) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  dispose() {
    this.disposed = true;
    this.listeners.clear();
  }
  private publish(state: typeof this.state) {
    this.state = state;
    for (const listener of this.listeners) listener();
  }
  async resolve(displayed: MobileApproval, approve: boolean) {
    if (this.disposed || !this.current() || this.state.pending) return;
    const pending = this.approvals().find((value) => value.id === displayed.id);
    if (!pending || JSON.stringify(pending) !== JSON.stringify(displayed))
      return;
    this.publish({ pending: displayed.id, error: null });
    try {
      await this.client.request("evener/sandbox/escalation/resolve", {
        ref: this.ref,
        escalationId: displayed.id,
        approve,
      });
      if (this.disposed) return;
      if (!this.current()) throw new Error("Session changed");
      await this.refresh();
      if (this.disposed) return;
      this.publish({ pending: null, error: null });
    } catch {
      if (this.disposed) return;
      this.publish({
        pending: null,
        error:
          "Could not confirm this decision. Refresh the session before deciding again; it may already have been applied.",
      });
    }
  }
}
