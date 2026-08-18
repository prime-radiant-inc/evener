import type { ReactNode } from "react";
import { Button } from "../button";
import { Dialog } from "../dialog";

export interface ConfirmDialogProps {
  open: boolean;
  title: string;
  /** Body copy explaining the consequence - mirrors Dialog's own `children`
   * naming. */
  children: ReactNode;
  /** The destructive (or otherwise consequential) verb, e.g. "Remove",
   * "Clear", "Install". */
  confirmLabel: string;
  cancelLabel?: string;
  /** Danger-tone confirm button (the common case: Remove/Clear/Delete).
   * Set false for a consequential-but-not-destructive action this wave's
   * binding constraint still requires a confirm step for (e.g. plugin
   * Install - a code-execution surface, not data loss) so its button
   * doesn't read as "red = deleting something". Default true. */
  destructive?: boolean;
  /** Disables both buttons - set while the caller's own async onConfirm is
   * in flight, to block a double-submit. Default false. */
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

/**
 * A Dialog pre-composed into this wave's binding "every destructive action
 * confirms" contract: title + body + a Cancel/confirm-verb footer, danger
 * tone by default. Controlled, like every other overlay widget in this set
 * (Dialog/Menu/Switch/Tree) - open/onConfirm/onCancel props, no imperative
 * confirm()-returns-a-Promise helper - so callers key it off a single
 * "what's pending" bit of state:
 *
 *   const [pending, setPending] = useState<Item | null>(null);
 *   <ConfirmDialog open={pending !== null} title=... confirmLabel="Remove"
 *     onConfirm={() => { doRemove(pending!); setPending(null); }}
 *     onCancel={() => setPending(null)}>
 *     {`Remove "${pending?.name}"? This cannot be undone.`}
 *   </ConfirmDialog>
 *
 * Escape and scrim-click both cancel (wired to onCancel via Dialog's own
 * onClose), matching every other overlay's dismiss convention - nothing
 * here treats a dismiss as a confirm.
 */
export function ConfirmDialog({
  open,
  title,
  children,
  confirmLabel,
  cancelLabel = "Cancel",
  destructive = true,
  busy = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  return (
    <Dialog
      open={open}
      onClose={onCancel}
      title={title}
      footer={
        <>
          <Button variant="quiet" onClick={onCancel} disabled={busy}>
            {cancelLabel}
          </Button>
          <Button variant={destructive ? "danger" : "primary"} onClick={onConfirm} disabled={busy}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      {children}
    </Dialog>
  );
}
