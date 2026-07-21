import { useState } from "react";
import { Button } from "../../widgets/button";
import { Sheet, type SheetSide } from "../../widgets/sheet";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./sheet.module.css";

// Same open-by-default, contained-frame approach as dialog.tsx (Sheet
// shares Dialog's exact contract) - see this task's report.
function SheetDemo({ side }: { side: SheetSide }) {
  const [open, setOpen] = useState(true);

  return (
    <div className={styles.demoFrame}>
      {!open && (
        <Button variant="quiet" size="sm" onClick={() => setOpen(true)}>
          Reopen
        </Button>
      )}
      <Sheet
        side={side}
        open={open}
        onClose={() => setOpen(false)}
        title={`Side: ${side}`}
        footer={
          <Button variant="primary" onClick={() => setOpen(false)}>
            Done
          </Button>
        }
      >
        <p>Slides in from the {side} edge. Escape, scrim click, and the close button all close it.</p>
      </Sheet>
    </div>
  );
}

export default function SheetGallerySection() {
  return (
    <section>
      <h2>Sheet</h2>
      <ThemeFlip>
        <SheetDemo side="right" />
      </ThemeFlip>
      <ThemeFlip>
        <SheetDemo side="bottom" />
      </ThemeFlip>
    </section>
  );
}
