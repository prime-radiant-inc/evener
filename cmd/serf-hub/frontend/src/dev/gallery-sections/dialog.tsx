import { useState } from "react";
import { Button } from "../../widgets/button";
import { Dialog } from "../../widgets/dialog";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./dialog.module.css";

interface DialogDemoProps {
  title: string;
  danger?: boolean;
}

// Rendered open by default (open-by-default state, not a portal - see this
// task's report) inside a containing-block frame (dialog.module.css) so
// the demo stays inside its own box rather than covering the gallery. It
// stays genuinely interactive: closing it (Escape, scrim click, or the
// close button) reveals a "Reopen" affordance rather than leaving a dead
// gap, so every close path is still explorable.
function DialogDemo({ title, danger }: DialogDemoProps) {
  const [open, setOpen] = useState(true);

  return (
    <div className={styles.demoFrame}>
      {!open && (
        <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
          Reopen
        </Button>
      )}
      <Dialog
        open={open}
        onClose={() => setOpen(false)}
        title={title}
        footer={
          danger ? (
            <>
              <Button variant="quiet" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button variant="danger" onClick={() => setOpen(false)}>
                Delete session
              </Button>
            </>
          ) : (
            <Button variant="primary" onClick={() => setOpen(false)}>
              Done
            </Button>
          )
        }
      >
        <p>
          {danger
            ? "This permanently deletes the session and its transcript. This cannot be undone."
            : "This is the dialog body. It can hold any content - text, forms, lists."}
        </p>
      </Dialog>
    </div>
  );
}

export default function DialogGallerySection() {
  return (
    <section>
      <h2>Dialog</h2>
      <ThemeFlip>
        <DialogDemo title="Session settings" />
      </ThemeFlip>
      <ThemeFlip>
        <DialogDemo title="Delete session" danger />
      </ThemeFlip>
    </section>
  );
}
