import { WireError } from "../../cmd/evener-hub/frontend/src/protocol/errors";
import type { MobileConversation } from "../../mobile/src/conversation/model";
import type { ConversationService } from "../../mobile/src/services/conversation";

type Operation = "rename" | "compact" | "shutdown" | "setReasoningEffort";
interface ControlsState {
  pending: Operation | null;
  error: string | null;
  notice: string | null;
}

/** Own actions for one conversation binding, independently of its sheet. */
export class SessionControls {
  private state: ControlsState = { pending: null, error: null, notice: null };
  private listeners = new Set<() => void>();
  private disposed = false;
  constructor(
    private service: Pick<ConversationService, Operation>,
    private refresh: () => Promise<void>,
    private stopped: () => void,
    private isCurrent: () => boolean,
    private getReasoning: () => Pick<
      MobileConversation,
      "supportsReasoning" | "reasoningEffort" | "reasoningEffortLevels"
    > | null,
  ) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(state: ControlsState) {
    this.state = state;
    for (const listener of this.listeners) listener();
  }
  dispose() {
    this.disposed = true;
    this.listeners.clear();
  }
  rename(name: string) {
    const trimmed = name.trim();
    if (!trimmed) return Promise.resolve();
    return this.run("rename", () => this.service.rename(trimmed));
  }
  compact() {
    return this.run("compact", () => this.service.compact());
  }
  shutdown() {
    return this.run("shutdown", () => this.service.shutdown());
  }
  setReasoningEffort(effort: string) {
    const current = this.getReasoning();
    if (
      !current?.supportsReasoning ||
      !current.reasoningEffortLevels?.includes(effort) ||
      current.reasoningEffort === effort
    )
      return Promise.resolve();
    return this.run("setReasoningEffort", () =>
      this.service.setReasoningEffort(effort),
    );
  }
  private async run(kind: Operation, operation: () => Promise<void>) {
    if (this.disposed || !this.isCurrent() || this.state.pending) return;
    this.publish({ pending: kind, error: null, notice: null });
    try {
      await operation();
      if (this.disposed || !this.isCurrent()) return;
      if (kind === "shutdown") {
        this.publish({
          pending: null,
          error: null,
          notice: "Runtime stop requested.",
        });
        this.stopped();
        return;
      }
      await this.refresh();
      if (this.disposed || !this.isCurrent()) return;
      this.publish({
        pending: null,
        error: null,
        notice:
          kind === "compact"
            ? "Compaction requested. Progress appears in the conversation."
            : kind === "setReasoningEffort"
              ? "Reasoning effort updated."
              : "Session renamed.",
      });
    } catch (cause) {
      if (this.disposed || !this.isCurrent()) return;
      this.publish({
        pending: null,
        notice: null,
        error:
          cause instanceof WireError
            ? `Could not confirm the action: ${cause.message}`
            : "Could not confirm the action. Check the session before trying again; it may have been applied.",
      });
    }
  }
}
