import type { ComposerMention } from "@evener/appwire-client";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
import type { InputAttachment } from "../../../stores/threads";

// The composer seam a pending-message row needs: the stack and the composer
// share one type for drain, restore and the recovery-operation lease.
export interface PendingActionContext {
  ref: string;
  // The composer pane's durable seat. Edits and drains submit as this pane, so
  // a refusal's bounced row restores here.
  compositionOwnerId?: string;
  getComposerText(): {
    text: string;
    attachments?: InputAttachment[];
    hasPending: boolean;
    skillNames?: readonly string[];
    commandNames?: readonly string[];
    mentions?: readonly ComposerMention[];
  };
  onRestoreToComposer(
    text: string,
    attachments?: InputAttachment[],
    skillNames?: readonly string[],
    commandNames?: readonly string[],
    mentions?: readonly ComposerMention[],
  ): void;
  activeRecoveryId?: string;
  activeRecoveryRevision?: number;
  getRecoveryRevision?(): Promise<number | undefined>;
  onEditRecovery?(record: MutationRecoveryRecord): void | Promise<void>;
  tryAcquireRecoveryOperation?(): (() => void) | null;
  onDrainSuccess(): void;
  focusComposer?(): void;
  busy: boolean;
  onDrainBusyChange(busy: boolean): void;
}
