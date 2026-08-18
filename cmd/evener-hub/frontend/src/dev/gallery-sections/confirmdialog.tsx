import { useState } from "react";
import { Button } from "../../widgets/button";
import { ConfirmDialog } from "../../widgets/confirmdialog";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./confirmdialog.module.css";

interface ConfirmDialogDemoProps {
  title: string;
  confirmLabel: string;
  destructive?: boolean;
  body: string;
}

// Rendered open by default, same "explorable, not a dead gap" shape as the
// Dialog gallery section's own DialogDemo (see dialog.tsx): closing it
// (Cancel, Escape, confirm) reveals a "Reopen" affordance.
function ConfirmDialogDemo({ title, confirmLabel, destructive, body }: ConfirmDialogDemoProps) {
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
    <section>
      <h2>ConfirmDialog</h2>
      <ThemeFlip>
        <ConfirmDialogDemo
          title='Remove instance "openai-work"?'
          confirmLabel="Remove"
          body="This will also clear its stored credentials."
        />
      </ThemeFlip>
      <ThemeFlip>
        <ConfirmDialogDemo
          title="Install serf-lint?"
          confirmLabel="Install"
          destructive={false}
          body="Plugins can run arbitrary code on the hub. Only install ones you trust."
        />
      </ThemeFlip>
    </section>
  );
}
