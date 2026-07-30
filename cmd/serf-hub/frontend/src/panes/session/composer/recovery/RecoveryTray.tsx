import { type ChangeEvent, type JSX, useEffect, useRef, useState } from "react";
import type { InputItem } from "../../../../protocol/types.gen";
import { copyToClipboard } from "../../../../shell/palette/commands";
import type { MutationOutboxRecord, MutationRecoveryRecord } from "../../../../stores/mutationOutbox";
import type { InputAttachment } from "../../../../stores/threads";
import { Button, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import {
  resendRecoveryPendingTurn,
  retryBlockedPendingTurn,
  updateRecoveryPendingTurn,
  useBlockedMutationEntries,
  useRecoveryEntries,
} from "../queue/pendingTurnsStore";
import { recoveryComposerDraft } from "./recoveryDraft";
import styles from "./recoverytray.module.css";

export interface RecoveryTrayProps {
  sessionRef: string;
  threadId?: string;
}

const CLASS = {
  tray: requireClass(styles.tray, "recoverytray.module.css", "tray"),
  heading: requireClass(styles.heading, "recoverytray.module.css", "heading"),
  list: requireClass(styles.list, "recoverytray.module.css", "list"),
  entry: requireClass(styles.entry, "recoverytray.module.css", "entry"),
  status: requireClass(styles.status, "recoverytray.module.css", "status"),
  textarea: requireClass(styles.textarea, "recoverytray.module.css", "textarea"),
  attachments: requireClass(styles.attachments, "recoverytray.module.css", "attachments"),
  actions: requireClass(styles.actions, "recoverytray.module.css", "actions"),
  explanation: requireClass(styles.explanation, "recoverytray.module.css", "explanation"),
};

function recordInput(record: MutationOutboxRecord): InputItem[] {
  return Array.isArray(record.payload.input) ? (record.payload.input as InputItem[]) : [];
}

function recordText(record: MutationOutboxRecord): string {
  return recordInput(record)
    .filter((item): item is InputItem & { text: string } => item.type === "text" && typeof item.text === "string")
    .map((item) => item.text)
    .join("\n");
}

function recordAttachments(record: MutationRecoveryRecord): InputAttachment[] {
  return recoveryComposerDraft(record).attachments.flatMap((attachment) =>
    attachment.data === undefined
      ? []
      : [{ name: attachment.name, mediaType: attachment.mediaType, data: attachment.data }],
  );
}

function exportRecord(record: MutationOutboxRecord): void {
  const payload = JSON.stringify(
    {
      clientMutationId: record.clientMutationId,
      method: record.method,
      targetRef: record.targetRef,
      threadId: record.threadId,
      payload: record.payload,
      text: recordText(record),
      attachments: record.attachments.map(({ name, mediaType, blob }) => ({
        name,
        mediaType,
        size: blob.size,
      })),
    },
    null,
    2,
  );
  const url = URL.createObjectURL(new Blob([payload], { type: "application/json" }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `serf-recovery-${record.clientMutationId}.json`;
  anchor.click();
  URL.revokeObjectURL(url);
}

function PayloadActions({ record }: { record: MutationOutboxRecord }): JSX.Element {
  const toasts = useToasts();

  async function copy(): Promise<void> {
    try {
      await copyToClipboard(recordText(record));
      toasts.push("success", "Copied recovery text");
    } catch (error) {
      toasts.push("error", `Couldn't copy recovery text: ${error instanceof Error ? error.message : String(error)}`);
    }
  }

  function exportPayload(): void {
    try {
      exportRecord(record);
    } catch (error) {
      toasts.push(
        "error",
        `Couldn't export recovery payload: ${error instanceof Error ? error.message : String(error)}`,
      );
    }
  }

  return (
    <>
      <Button size="sm" variant="quiet" onClick={() => void copy()}>
        Copy
      </Button>
      <Button size="sm" variant="quiet" onClick={exportPayload}>
        Export
      </Button>
    </>
  );
}

function RecoveryDraft({ record }: { record: MutationRecoveryRecord }): JSX.Element {
  const [text, setText] = useState(() => recordText(record));
  const [sending, setSending] = useState(false);
  const writes = useRef(Promise.resolve());
  const toasts = useToasts();

  useEffect(() => {
    setText(recordText(record));
  }, [record]);

  function handleChange(event: ChangeEvent<HTMLTextAreaElement>): void {
    const nextText = event.target.value;
    setText(nextText);
    writes.current = writes.current.then(async () => {
      await updateRecoveryPendingTurn(record.clientMutationId, record.targetRef, nextText, recordAttachments(record));
    });
  }

  async function handleSend(): Promise<void> {
    setSending(true);
    try {
      await writes.current;
      const won = await resendRecoveryPendingTurn(
        record.clientMutationId,
        record.targetRef,
        "send",
        text,
        recordAttachments(record),
      );
      if (!won) toasts.push("info", "This recovery draft was already sent in another tab.");
    } catch (error) {
      toasts.push("error", `Couldn't send recovery draft: ${error instanceof Error ? error.message : String(error)}`);
    } finally {
      setSending(false);
    }
  }

  return (
    <li className={CLASS.entry}>
      <div className={CLASS.status}>
        {record.recoveryKind === "orphaned"
          ? "The original session was deleted. Choose another session before sending this draft."
          : "The server rejected this submission. Edit it here before sending again."}
      </div>
      <label>
        <span className={CLASS.heading}>Recovered message</span>
        <textarea
          className={CLASS.textarea}
          aria-label="Recovered message"
          value={text}
          onChange={handleChange}
          rows={3}
        />
      </label>
      {record.attachments.length > 0 && (
        <ul className={CLASS.attachments} aria-label="Recovered attachments">
          {record.attachments.map((attachment) => (
            <li key={attachment.presentationId}>{attachment.name}</li>
          ))}
        </ul>
      )}
      <div className={CLASS.actions}>
        <Button size="sm" onClick={() => void handleSend()} disabled={sending || record.recoveryKind === "orphaned"}>
          Send recovered draft
        </Button>
        <PayloadActions record={record} />
      </div>
    </li>
  );
}

function BlockedMutation({ record }: { record: MutationOutboxRecord }): JSX.Element {
  const [retrying, setRetrying] = useState(false);
  const toasts = useToasts();

  async function retry(): Promise<void> {
    setRetrying(true);
    try {
      await retryBlockedPendingTurn(record.clientMutationId, record.targetRef);
    } catch (error) {
      toasts.push("error", `Retry failed: ${error instanceof Error ? error.message : String(error)}`);
    } finally {
      setRetrying(false);
    }
  }

  return (
    <li className={CLASS.entry}>
      <div className={CLASS.status}>Delivery outcome unknown</div>
      <p className={CLASS.explanation}>
        The server may already have accepted this submission. Later actions for this session remain blocked until an
        authoritative outcome is known.
      </p>
      <div>{recordText(record)}</div>
      <div className={CLASS.actions}>
        <Button size="sm" onClick={() => void retry()} disabled={retrying}>
          Retry
        </Button>
        <PayloadActions record={record} />
      </div>
    </li>
  );
}

export function RecoveryTray({ sessionRef }: RecoveryTrayProps): JSX.Element | null {
  const recovery = useRecoveryEntries(sessionRef);
  const blocked = useBlockedMutationEntries(sessionRef);
  if (recovery.length === 0 && blocked.length === 0) return null;

  return (
    <section className={CLASS.tray} aria-labelledby={`recovery-heading-${sessionRef}`}>
      <h3 className={CLASS.heading} id={`recovery-heading-${sessionRef}`}>
        Recovery drafts
      </h3>
      <ul className={CLASS.list}>
        {blocked.map((record) => (
          <BlockedMutation key={record.clientMutationId} record={record} />
        ))}
        {recovery.map((record) => (
          <RecoveryDraft key={record.clientMutationId} record={record} />
        ))}
      </ul>
    </section>
  );
}
