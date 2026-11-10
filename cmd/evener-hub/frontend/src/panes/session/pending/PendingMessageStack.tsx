import { isTurnActive } from "@evener/appwire-client";
import type { PendingMessage } from "@evener/appwire-client/state/mutation";
import { useContext } from "react";
import { controlsFor } from "../../../stores/liveControls";
import { useThreadsStore } from "../../../stores/threads";
import { Button } from "../../../widgets";
import { SessionNowContext } from "../liveness";
import { UserMessageView } from "../transcript/messages/UserMessageItem";
import type { PendingActionContext } from "./actionContext";
import { pendingMessageCaption } from "./pendingMessageCaption";
import { rowMatchesIdentity, withoutActiveRecovery } from "./pendingMessageLiveEdge";
import styles from "./pendingmessagestack.module.css";
import { usePendingMessageActions } from "./usePendingMessageActions";

// The one stack of undelivered user messages: the transcript's trailing
// virtual row, below every server-sourced turn. The row is the message it will
// become, with a status caption and persistent actions. It scrolls with the
// transcript; the new-content pill covers a reader who is scrolled away.
export function PendingMessageStack({
  ref: sessionRef,
  messages,
  context,
}: {
  ref: string;
  messages: readonly PendingMessage[];
  context?: PendingActionContext;
}) {
  const model = useThreadsStore((state) => state.threads.get(sessionRef));
  const now = useContext(SessionNowContext);
  const refusedId = useThreadsStore((state) => state.transientRefusalPaces.get(sessionRef)?.clientMutationId);
  const handlers = usePendingMessageActions(context ?? NO_ACTIONS);
  const turnActive = isTurnActive(model?.status.type ?? "");
  const canDrain = model !== undefined && controlsFor(model).drain;
  // The composer owns the recovery row it has activated; the stack keeps the
  // rest of the tray.
  const shown = withoutActiveRecovery(messages, context?.activeRecoveryId);
  const queued = shown.some((row) => row.method === "queue");

  if (shown.length === 0) return null;

  return (
    <div className={styles.stack} data-testid="pending-message-stack">
      {queued && canDrain && context !== undefined && (
        <div className={styles.header}>
          <Button variant="quiet" size="sm" disabled={context.busy || handlers.draining} onClick={handlers.drain}>
            Steer queue now
          </Button>
        </div>
      )}
      <ul className={styles.list}>
        {shown.map((row) => (
          <li key={row.id} data-testid="pending-message-row" data-state={row.state} data-message-id={row.id}>
            <UserMessageView
              item={{
                id: row.id,
                turnId: "",
                type: "userMessage",
                text: row.displayText,
                images: row.images,
              }}
              status={pendingMessageCaption(
                row,
                turnActive,
                now,
                refusedId !== undefined && rowMatchesIdentity(row, refusedId),
              )}
              footer={
                context === undefined ? undefined : (
                  <PendingMessageRowActions row={row} handlers={handlers} canDrain={canDrain} disabled={context.busy} />
                )
              }
            />
          </li>
        ))}
      </ul>
    </div>
  );
}

type Handlers = ReturnType<typeof usePendingMessageActions>;

function PendingMessageRowActions({
  row,
  handlers,
  canDrain,
  disabled,
}: {
  row: PendingMessage;
  handlers: Handlers;
  canDrain: boolean;
  disabled: boolean;
}) {
  const busy = disabled || handlers.isBusy(row);
  const actions: Array<{ verb: Parameters<Handlers["action"]>[1]; label: string; disabled: boolean }> = [];

  switch (row.state) {
    case "waiting":
      if (handlers.canEdit(row)) actions.push({ verb: "edit", label: "Edit message", disabled: busy });
      if (handlers.canRemove(row)) actions.push({ verb: "remove", label: "Remove from queue", disabled: busy });
      break;
    case "queued":
    case "held":
      if (row.queueEntryId !== undefined && canDrain)
        actions.push({ verb: "steerNow", label: "Steer now", disabled: busy });
      if (handlers.canEdit(row)) actions.push({ verb: "edit", label: "Edit message", disabled: busy });
      if (handlers.canRemove(row)) actions.push({ verb: "remove", label: "Remove from queue", disabled: busy });
      break;
    case "uncertain":
    case "canceled":
      if (handlers.canEdit(row)) actions.push({ verb: "edit", label: "Edit message", disabled: busy });
      if (handlers.canRemove(row)) actions.push({ verb: "remove", label: "Remove from queue", disabled: busy });
      if (handlers.canRetry(row)) actions.push({ verb: "retry", label: "Retry", disabled: busy });
      break;
    case "refused":
      if (row.interrupt === true) {
        actions.push({ verb: "dismiss", label: "Dismiss", disabled: busy });
      } else if (handlers.canEdit(row)) {
        actions.push({ verb: "edit", label: "Edit message", disabled: busy });
      }
      break;
    case "orphaned":
      actions.push({ verb: "copy", label: "Copy", disabled: busy });
      break;
    case "sending":
    case "delivering":
    case "steering":
      break;
  }

  if (actions.length === 0) return null;

  return (
    <>
      {actions.map((action) => (
        <Button
          key={action.verb}
          variant="quiet"
          size="sm"
          disabled={action.disabled}
          onClick={() => handlers.action(row, action.verb)}
        >
          {action.label}
        </Button>
      ))}
    </>
  );
}

// A seat without a composer source still renders the stack read-only. The
// fallback keeps the hook unconditional; its busy flag keeps every action
// closed, and the row only draws actions when a real context exists.
const NO_ACTIONS: PendingActionContext = {
  ref: "",
  getComposerText: () => ({ text: "", hasPending: false }),
  onRestoreToComposer: () => undefined,
  onDrainSuccess: () => undefined,
  busy: true,
  onDrainBusyChange: () => undefined,
};
