import type { SandboxEscalationRequested } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

// The dock re-reads the session on its own after this, so it names nothing
// to press. A new decision may still be tried while it stands: the hub refuses
// a resolve for an escalation it already settled (agent/session_escalation.go's
// ResolveSandboxEscalation), so trying again never decides twice.
const UNCONFIRMED =
  "Couldn't confirm your decision. It may already have been applied.";

/** Decisions belong to the displayed approval and one live session binding. */
export class ApprovalControls {
  private state = {
    pending: null as string | null,
    refreshing: false,
    error: null as string | null,
  };
  private disposed = false;
  private listeners = new Set<() => void>();
  constructor(
    private client: ConversationClientLike,
    private ref: string,
    private approvals: () => SandboxEscalationRequested[],
    private current: () => boolean,
    private refreshSession: () => Promise<void>,
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
  private async refreshAuthoritative() {
    if (this.disposed || !this.current()) return;
    this.publish({ ...this.state, refreshing: true });
    try {
      await this.refreshSession();
      if (this.disposed) return;
      if (!this.current()) throw new Error("Session changed");
      this.publish({ pending: null, refreshing: false, error: null });
    } catch {
      if (!this.disposed)
        this.publish({
          pending: null,
          refreshing: false,
          error: UNCONFIRMED,
        });
    }
  }
  async refresh() {
    if (
      this.disposed ||
      !this.current() ||
      this.state.pending !== null ||
      this.state.refreshing
    )
      return;
    await this.refreshAuthoritative();
  }
  async resolve(displayed: SandboxEscalationRequested, approve: boolean) {
    if (
      this.disposed ||
      !this.current() ||
      this.state.pending ||
      this.state.refreshing
    )
      return;
    const pending = this.approvals().find((value) => value.escalationId === displayed.escalationId);
    if (!pending || JSON.stringify(pending) !== JSON.stringify(displayed))
      return;
    this.publish({ pending: displayed.escalationId, refreshing: false, error: null });
    try {
      const receipt = await this.client.request(
        "evener/sandbox/escalation/resolve",
        {
          ref: this.ref,
          escalationId: displayed.escalationId,
          approve,
        },
      );
      if (!receipt || typeof receipt !== "object" || Array.isArray(receipt))
        throw new Error("Malformed resolution receipt");
      if (this.disposed) return;
      if (!this.current()) throw new Error("Session changed");
      await this.refreshAuthoritative();
    } catch {
      if (this.disposed) return;
      this.publish({
        pending: null,
        refreshing: false,
        error: UNCONFIRMED,
      });
    }
  }
}
