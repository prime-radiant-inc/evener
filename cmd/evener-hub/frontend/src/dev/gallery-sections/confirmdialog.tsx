import { useState } from "react";
import { Button } from "../../widgets/button";
import { ConfirmDialog } from "../../widgets/confirmdialog";
import { GallerySection } from "../GallerySection";
import styles from "./confirmdialog.module.css";

interface ConfirmDialogDemoProps {
  title: string;
  confirmLabel: string;
  destructive?: boolean;
  pendingLabel?: string;
  busy?: boolean;
  body: string;
}

// Rendered open by default, same "explorable, not a dead gap" shape as the
// Dialog gallery section's own DialogDemo (see dialog.tsx): closing it
// (Cancel, Escape, confirm) reveals a "Reopen" affordance.
function ConfirmDialogDemo({ title, confirmLabel, destructive, pendingLabel, busy, body }: ConfirmDialogDemoProps) {
  const [open, setOpen] = useState(true);

  return (
    <div className={styles.demoFrame}>
      {!open && (
        <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
          Reopen
        </Button>
      )}
      <ConfirmDialog
        open={open}
        title={title}
        confirmLabel={confirmLabel}
        destructive={destructive}
        pendingLabel={pendingLabel}
        busy={busy}
        onConfirm={() => setOpen(false)}
        onCancel={() => setOpen(false)}
      >
        {body}
      </ConfirmDialog>
    </div>
  );
}

export default function ConfirmDialogGallerySection() {
  return (
    <GallerySection
      title="ConfirmDialog"
      flips={[
        <ConfirmDialogDemo
          title='Remove instance "openai-work"?'
          confirmLabel="Remove"
          body="This will also clear its stored credentials."
        />,
        <ConfirmDialogDemo
          title="Install evener-lint?"
          confirmLabel="Install"
          destructive={false}
          body="Plugins can run arbitrary code on the hub. Only install ones you trust."
        />,
        <ConfirmDialogDemo
          title="Delete pin section?"
          confirmLabel="Delete section"
          pendingLabel="Deleting…"
          busy
          body="In flight: the confirm reads its pending label and both buttons are disabled."
        />,
      ]}
    />
  );
}
