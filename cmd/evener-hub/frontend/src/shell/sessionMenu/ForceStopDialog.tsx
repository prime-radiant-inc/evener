import { useState } from "react";
import { ConfirmDialog } from "../../widgets";

export function ForceStopDialog({
  open,
  onClose,
  onConfirm,
}: {
  open: boolean;
  onClose(): void;
  onConfirm(): Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const confirm = async () => {
    if (busy) return;
    setBusy(true);
    try {
      await onConfirm();
      onClose();
    } catch {
      // The action adapter reports the failure; retain confirmation for retry.
    } finally {
      setBusy(false);
    }
  };
  return (
    <ConfirmDialog
      open={open}
      title="Force shutdown this session?"
      confirmLabel="Force shutdown"
      busy={busy}
      onConfirm={() => void confirm()}
      onCancel={() => {
        if (!busy) onClose();
      }}
    >
      Active turns and jobs may be interrupted. Saved transcripts are retained. You can resume the session after its
      process stops.
    </ConfirmDialog>
  );
}
