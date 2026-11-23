import { ConfirmDialog } from "../../widgets";
import { useAsyncConfirm } from "../useAsyncConfirm";

export function ForceStopDialog({
  open,
  onClose,
  onConfirm,
}: {
  open: boolean;
  onClose(): void;
  onConfirm(): Promise<void>;
}) {
  const { busy, run } = useAsyncConfirm();
  return (
    <ConfirmDialog
      open={open}
      title="Force shutdown this session?"
      confirmLabel="Force shutdown"
      busy={busy}
      onConfirm={() => void run(onConfirm, onClose)}
      onCancel={() => {
        if (!busy) onClose();
      }}
    >
      Active turns and jobs may be interrupted. Saved transcripts are retained. You can resume the session after its
      process stops.
    </ConfirmDialog>
  );
}
