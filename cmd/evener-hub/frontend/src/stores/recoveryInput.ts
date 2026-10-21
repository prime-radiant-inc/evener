import type { ComposerMention, InputItem } from "@evener/appwire-client";
import type { DraftSessionToken } from "@evener/appwire-client/state/mutation";
import type { SkillEditorValue } from "../panes/session/composer/skillDocument";
import type { BrowserDraftKey } from "./browserDraftRepository";
import type { MutationAttachment, MutationRecoveryRecord } from "./mutationOutbox";

// A destructive or editing recovery write's optional fence: which browser
// session key it targets, the durable session token the caller captured, and a
// live callback evaluated once every read prerequisite has run and immediately
// before any write. The key must agree with the record's own ref, so a guard
// can never authorize a write against an unrelated row.
export interface RecoveryWriteGuard {
  key: BrowserDraftKey;
  token: DraftSessionToken;
  stillAuthorized: () => boolean;
}

// The guarded recovery update's optional ordinary consumption: the session
// token, the complete ordinary value and a live ownership callback the
// transaction checks after all reads. All three must match for the ordinary
// row to be cleared; otherwise the ordinary value is preserved while the
// recovery edit still commits.
export interface RecoveryOrdinaryConsumption {
  token: DraftSessionToken;
  value: SkillEditorValue;
  stillOwnsValue: () => boolean;
}

// The completed guarded recovery update: the record the write produced, and
// the session token the SAME transaction's session write left on the ref's
// durable row — the storage's own answer for the revision the completed write
// produced, so a source records that fact instead of deriving it from its
// guard. sessionToken is undefined only when an unguarded update consumed an
// absent ordinary row and initialized no session row.
export interface RecoveryInputUpdate {
  record: MutationRecoveryRecord;
  sessionToken: DraftSessionToken | undefined;
}

// The one mapping an update writes: input, the display it renders, attachments
// and the composer text/mentions, preserving absent-versus-empty mentions and
// the schema `version`. PURE, so updateRecoveryInput and Task 3 can share it
// inside their own transactions without opening one from another.
export function applyRecoveryInput(
  record: MutationRecoveryRecord,
  input: InputItem[],
  attachments?: MutationAttachment[],
  composerText?: string,
  composerMentions?: readonly ComposerMention[],
): MutationRecoveryRecord {
  return {
    ...record,
    payload: { ...record.payload, input },
    optimisticDisplay:
      record.optimisticDisplay && typeof record.optimisticDisplay === "object"
        ? { ...record.optimisticDisplay, input }
        : { method: record.method, input },
    attachments: attachments ?? record.attachments,
    composerText: composerText ?? record.composerText,
    composerMentions: composerText === undefined ? (composerMentions ?? record.composerMentions) : composerMentions,
  };
}
