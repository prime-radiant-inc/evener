import { useState } from "react";
import { Button, Dialog } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./sessionmenu.module.css";

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
    <Dialog
      open={open}
      onClose={() => {
        if (!busy) onClose();
      }}
      title="Force stop this session?"
      footer={
        <div className={requireClass(styles.footer, "sessionmenu.module.css", "footer")}>
          <Button variant="quiet" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button variant="danger" disabled={busy} onClick={() => void confirm()}>
            Force stop
          </Button>
        </div>
      }
    >
      <p className={requireClass(styles.body, "sessionmenu.module.css", "body")}>
        Active turns and jobs may be interrupted. Saved transcripts are retained. You can resume the session after its
        process stops.
      </p>
    </Dialog>
  );
}
