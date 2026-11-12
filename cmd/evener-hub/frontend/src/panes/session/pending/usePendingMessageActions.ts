import { errorText, sessionActionError } from "@evener/appwire-client";
import type { PendingMessage } from "@evener/appwire-client/state/mutation";
import {
  canEditQueuedMessage,
  canRemoveQueuedMessage,
  commandMarkers,
  skillMarkers,
} from "@evener/appwire-client/state/mutation";
import { useState } from "react";
import { copyToClipboard } from "../../../shell/palette/commands";
import { pressLocalRecoveryFenced, pressRefusal } from "../../../stores/liveControls";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
import {
  captureRecoveryStopBarrier,
  retryBlockedForTarget,
  settleDisplayCopy,
  threadsStore,
} from "../../../stores/threads";
import { useToasts } from "../../../widgets";
import {
  discardRecoveryPendingTurn,
  editQueuedPendingTurn,
  refreshPendingTurnsProjection,
  removeQueuedPendingTurn,
  resendRecoveryPendingTurn,
  retryBlockedPendingTurn,
  submitWithPendingTracking,
} from "../composer/queue/pendingTurnsStore";
import {
  pushRecoveryRefusal,
  pushSelectionRefusal,
  selectionRefusal,
  submitDraftToStore,
} from "../composer/submitProtocol";
import type { PendingActionContext } from "./actionContext";

const RECOVERY_ACTIONS_UNAVAILABLE_REASON = "Queue actions aren't available until this session is resumed";

export type PendingRowAction = "edit" | "remove" | "retry" | "steerNow" | "dismiss" | "copy";

type PendingRowKind = "recovery" | "queue" | "local";

function rowKind(row: PendingMessage): PendingRowKind {
  // A live queue entry owns the actions even when an unedited recovery copy
  // still accompanies it. Activating that recovery's edit would leave the
  // daemon's queued entry behind; an independently edited recovery is its own
  // composition and keeps recovery ownership.
  const editedRecovery = (row.recovery?.compositionRevision ?? 0) > 0;
  // An unattempted or canceled local promotion inherits its source entry's
  // queueIndex. That index describes the message this promotion would consume,
  // not an entry the daemon owns on its behalf, so this must answer before the
  // queue branch or Remove would cancel the original entry instead of only
  // discarding the local promotion.
  const local = row.outbox;
  if (
    local?.method === "turn/promoteQueuedAsSteer" &&
    (row.state === "canceled" || (local.attempted === false && row.state !== "delivering"))
  )
    return "local";
  if (row.queueEntryId !== undefined && row.queueIndex !== undefined && !editedRecovery) return "queue";
  if (row.recovery) return "recovery";
  if (row.queueEntryId !== undefined && row.queueIndex !== undefined) return "queue";
  return "local";
}

function displayCopyId(row: PendingMessage): string {
  return row.outbox?.clientMutationId ?? row.optimistic?.clientMutationId ?? row.recovery?.clientMutationId ?? row.id;
}

// The action owner for one pending-message row. It preserves the rules the
// durable and recovery rows already enforce, but reads them from the
// identity-keyed row instead of from a surface-specific record.
export function usePendingMessageActions(context: PendingActionContext) {
  const toasts = useToasts();
  const [busyIds, setBusyIds] = useState<ReadonlySet<string>>(new Set());
  const [draining, setDraining] = useState(false);

  function setBusy(id: string, busy: boolean): void {
    setBusyIds((current) => {
      const next = new Set(current);
      if (busy) next.add(id);
      else next.delete(id);
      return next;
    });
  }

  function localRecord(row: PendingMessage) {
    return row.recovery ?? row.outbox;
  }

  async function handleLocalAction(row: PendingMessage, action: "edit" | "remove"): Promise<void> {
    const record = localRecord(row);
    if (!record) return;
    const id = record.clientMutationId;
    const release = action === "edit" ? context.tryAcquireRecoveryOperation?.() : undefined;
    if (action === "edit" && context.tryAcquireRecoveryOperation && !release) return;
    if (action === "edit" && context.getComposerText().hasPending) {
      toasts.push("error", "Image attachment is still processing");
      release?.();
      return;
    }
    setBusy(row.id, true);
    try {
      const target = record.targetRef;
      if (action === "edit") {
        const recovery = await editQueuedPendingTurn(id, target);
        if (recovery) {
          await context.onEditRecovery?.(recovery);
          context.focusComposer?.();
        } else {
          pushRecoveryRefusal("changed");
        }
      } else if (!(await removeQueuedPendingTurn(id, target))) {
        pushRecoveryRefusal("changed");
      }
      if (target !== context.ref) await refreshPendingTurnsProjection(context.ref);
    } catch (error) {
      toasts.push("error", sessionActionError(action === "edit" ? "Edit failed" : "Remove failed", error));
    } finally {
      setBusy(row.id, false);
      release?.();
    }
  }

  async function handleRecoveryEdit(row: PendingMessage): Promise<void> {
    const record = row.recovery;
    if (!record) return;
    const release = context.tryAcquireRecoveryOperation?.();
    if (context.tryAcquireRecoveryOperation && !release) return;
    setBusy(row.id, true);
    try {
      await context.onEditRecovery?.(record as MutationRecoveryRecord);
      context.focusComposer?.();
    } catch (error) {
      toasts.push("error", sessionActionError("Edit failed", error));
    } finally {
      setBusy(row.id, false);
      release?.();
    }
  }

  async function handleSteerNow(row: PendingMessage): Promise<void> {
    if (pressLocalRecoveryFenced(context.ref)) {
      toasts.push("error", RECOVERY_ACTIONS_UNAVAILABLE_REASON);
      return;
    }
    const refusal = pressRefusal(context.ref, "drain");
    if (refusal !== undefined) {
      toasts.push("error", refusal);
      return;
    }
    if (row.queueIndex === undefined || row.queueEntryId === undefined) return;
    setBusy(row.id, true);
    try {
      await threadsStore.getState().promoteQueuedAsSteer(context.ref, row.queueIndex, row.queueEntryId, {
        text: row.text === "" && row.imageCount > 0 ? row.displayText : row.text,
        skillNames: row.skillNames,
        commandNames: row.commandNames,
      });
      await settleDisplayCopy(displayCopyId(row), context.ref).catch(() => undefined);
    } catch (error) {
      toasts.push("error", sessionActionError("Couldn't steer with this message now", error));
    } finally {
      setBusy(row.id, false);
    }
  }

  async function handleCancel(row: PendingMessage): Promise<void> {
    if (pressLocalRecoveryFenced(context.ref)) {
      toasts.push("error", RECOVERY_ACTIONS_UNAVAILABLE_REASON);
      return;
    }
    if (row.queueIndex === undefined || row.queueEntryId === undefined) return;
    setBusy(row.id, true);
    try {
      await threadsStore.getState().cancelQueued(context.ref, row.queueIndex, row.queueEntryId);
      await settleDisplayCopy(displayCopyId(row), context.ref).catch(() => undefined);
    } catch (error) {
      toasts.push("error", sessionActionError("Couldn't remove this message from the queue", error));
    } finally {
      setBusy(row.id, false);
    }
  }

  async function handleEdit(row: PendingMessage): Promise<void> {
    if (pressLocalRecoveryFenced(context.ref)) {
      toasts.push("error", RECOVERY_ACTIONS_UNAVAILABLE_REASON);
      return;
    }
    if (row.queueIndex === undefined || row.queueEntryId === undefined) return;
    setBusy(row.id, true);
    try {
      try {
        context.onRestoreToComposer(row.text, undefined, row.skillNames, row.commandNames, row.mentions);
        context.focusComposer?.();
      } catch (error) {
        toasts.push("error", `Couldn't move this message to the composer: ${errorText(error)}`);
        return;
      }
      await threadsStore.getState().cancelQueued(context.ref, row.queueIndex, row.queueEntryId);
      await settleDisplayCopy(displayCopyId(row), context.ref).catch(() => undefined);
    } catch (error) {
      toasts.push("error", `Moved to the composer, but couldn't remove it from the queue: ${errorText(error)}`);
    } finally {
      setBusy(row.id, false);
    }
  }

  async function handleRetry(row: PendingMessage): Promise<void> {
    const record = localRecord(row);
    if (!record) return;
    setBusy(row.id, true);
    try {
      const outcome = await retryBlockedPendingTurn(record.clientMutationId, record.targetRef);
      if (!outcome.released) {
        if (outcome.current?.state === "blockedUnknown") {
          toasts.push(
            "error",
            "Delivery still cannot be checked. The original message is kept; you can send a new message.",
          );
        } else if (outcome.current?.state === "canceled") {
          toasts.push("error", "Still canceled by Stop. The original message is kept; press Retry again.");
        }
      }
    } catch (error) {
      toasts.push("error", sessionActionError("Retry failed", error));
    } finally {
      setBusy(row.id, false);
    }
  }

  async function handleDismiss(row: PendingMessage): Promise<void> {
    const record = row.recovery;
    if (!record) return;
    setBusy(row.id, true);
    try {
      const discarded = await discardRecoveryPendingTurn(
        record.clientMutationId,
        context.ref,
        undefined,
        record.compositionRevision ?? 0,
      );
      if (!discarded) pushRecoveryRefusal("changed");
    } catch (error) {
      toasts.push("error", `Couldn't dismiss this row: ${errorText(error)}`);
    } finally {
      setBusy(row.id, false);
    }
  }

  async function handleCopy(row: PendingMessage): Promise<void> {
    setBusy(row.id, true);
    try {
      const parts = [row.text, skillMarkers(row.skillNames), commandMarkers(row.commandNames)];
      await copyToClipboard(parts.filter((part) => part !== "").join("\n"));
      toasts.push("success", "Copied message");
    } catch (error) {
      toasts.push("error", `Couldn't copy message: ${errorText(error)}`);
    } finally {
      setBusy(row.id, false);
    }
  }

  async function handleDrain(): Promise<void> {
    const release = context.tryAcquireRecoveryOperation?.();
    if (context.tryAcquireRecoveryOperation && !release) return;
    function abandon(message?: string): void {
      if (message !== undefined) toasts.push("error", message);
      release?.();
    }
    if (pressLocalRecoveryFenced(context.ref)) {
      abandon(RECOVERY_ACTIONS_UNAVAILABLE_REASON);
      return;
    }
    const refusal = pressRefusal(context.ref, "drain");
    if (refusal !== undefined) {
      abandon(refusal);
      return;
    }
    const { text, attachments, hasPending, skillNames, commandNames, mentions } = context.getComposerText();
    if (hasPending) {
      abandon("Image attachment is still processing");
      return;
    }
    const model = threadsStore.getState().threads.get(context.ref);
    const selectionGate = selectionRefusal({ skillNames, commandNames }, model?.capabilities);
    if (selectionGate) {
      pushSelectionRefusal(selectionGate);
      abandon();
      return;
    }
    let recoveryRefusal: "changed" | "consumed" | undefined;
    const recoveryResendRefusal = new Error("Recovery resend was refused");
    const recoveryAlreadySent = new Error("Recovery source was already consumed");
    // Capture the Stop fence before any await: a Stop during the autosave wait
    // must still land between this submission and its mutation.
    const recoveryBarrier = context.activeRecoveryId ? captureRecoveryStopBarrier(context.ref) : undefined;
    context.onDrainBusyChange(true);
    setDraining(true);
    try {
      let expectedRecoveryRevision = context.activeRecoveryRevision;
      await submitWithPendingTracking(
        {
          ref: context.ref,
          recoveryId: context.activeRecoveryId,
          text,
          attachments,
          skillNames,
          commandNames,
          onFailure: (error) => {
            if (error === recoveryResendRefusal || error === recoveryAlreadySent) return;
            toasts.push("error", sessionActionError("Drain failed", error));
          },
        },
        async () => {
          if (context.activeRecoveryId && context.getRecoveryRevision) {
            expectedRecoveryRevision = await context.getRecoveryRevision();
            if (expectedRecoveryRevision === undefined) {
              recoveryRefusal = "changed";
              throw recoveryResendRefusal;
            }
          }
          if (context.activeRecoveryId) {
            const outcome = await resendRecoveryPendingTurn(
              context.activeRecoveryId,
              context.ref,
              "drain",
              text,
              attachments ?? [],
              skillNames,
              commandNames,
              mentions,
              recoveryBarrier,
              context.activeRecoveryId && expectedRecoveryRevision !== undefined
                ? { [context.activeRecoveryId]: expectedRecoveryRevision }
                : undefined,
              context.compositionOwnerId,
            );
            if (outcome.status !== "sent") {
              if (outcome.status === "refused") {
                recoveryRefusal = "changed";
                throw recoveryResendRefusal;
              }
              recoveryRefusal = "consumed";
              throw recoveryAlreadySent;
            }
            return;
          }
          return submitDraftToStore(
            "drain",
            context.ref,
            { text, attachments, skillNames, commandNames, mentions },
            context.compositionOwnerId,
          );
        },
      );
      context.onDrainSuccess();
    } catch {
      // onFailure above reported every non-recovery failure. A recovery
      // refusal is the one outcome it deliberately leaves to this catch.
      if (recoveryRefusal) pushRecoveryRefusal(recoveryRefusal);
    } finally {
      context.onDrainBusyChange(false);
      setDraining(false);
      release?.();
    }
  }

  function action(row: PendingMessage, verb: PendingRowAction): void {
    const kind = rowKind(row);
    switch (verb) {
      case "edit":
        if (kind === "recovery") void handleRecoveryEdit(row);
        else if (kind === "queue") void handleEdit(row);
        else void handleLocalAction(row, "edit");
        break;
      case "remove":
        if (kind === "queue") void handleCancel(row);
        else void handleLocalAction(row, "remove");
        break;
      case "retry":
        void handleRetry(row);
        break;
      case "steerNow":
        void handleSteerNow(row);
        break;
      case "dismiss":
        void handleDismiss(row);
        break;
      case "copy":
        void handleCopy(row);
        break;
    }
  }

  return {
    action,
    drain: () => void handleDrain(),
    draining,
    isBusy: (row: PendingMessage) => busyIds.has(row.id),
    canEdit: (row: PendingMessage) => {
      switch (rowKind(row)) {
        case "recovery":
          return row.interrupt !== true && row.recovery?.recoveryKind !== "orphaned";
        case "queue":
          return (
            row.textComplete && (row.text.trim() !== "" || row.skillNames.length > 0 || row.commandNames.length > 0)
          );
        case "local":
          return row.outbox !== undefined && canEditQueuedMessage(row.outbox) && context.onEditRecovery !== undefined;
      }
    },
    canRemove: (row: PendingMessage) => {
      switch (rowKind(row)) {
        case "recovery":
          return false;
        case "queue":
          return true;
        case "local":
          return row.outbox !== undefined && canRemoveQueuedMessage(row.outbox);
      }
    },
    canRetry: (row: PendingMessage) => {
      const record = row.outbox;
      return (
        record !== undefined &&
        (record.state === "blockedUnknown" || record.state === "canceled") &&
        !retryBlockedForTarget(record.targetRef) &&
        !busyIds.has(row.id)
      );
    },
  };
}
