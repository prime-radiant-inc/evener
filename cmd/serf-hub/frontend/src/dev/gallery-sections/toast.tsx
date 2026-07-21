import { useEffect } from "react";
import { Button } from "../../widgets/button";
import { Toast, useToasts, type ToastKind } from "../../widgets/toast";
import { ThemeFlip } from "../ThemeFlip";
import styles from "./toast.module.css";

const SAMPLES: { kind: ToastKind; text: string }[] = [
  { kind: "success", text: "Changes saved" },
  { kind: "error", text: "Failed to reach the hub" },
  { kind: "warning", text: "This session is idle" },
  { kind: "info", text: "New version available" },
];

// Toast has no controlled "open" prop (push()-and-auto-dismiss, per its
// locked API) - shown open here by genuinely calling push() on mount, one
// per kind, through the real public useToasts() hook. Toasts are
// inherently ephemeral (5s auto-dismiss is part of the contract, not a
// demo artifact), so a "Push toasts" button is also provided to re-trigger
// them without a page reload once they've dismissed.
function ToastDemo() {
  const { push } = useToasts();

  function pushAll() {
    for (const sample of SAMPLES) push(sample.kind, sample.text);
  }

  useEffect(pushAll, []);

  return (
    <div className={styles.demoFrame}>
      <p className={styles.hint}>
        <Button variant="quiet" size="sm" onClick={pushAll}>
          Push toasts
        </Button>{" "}
        (they auto-dismiss after 5s, or hover one to pause it)
      </p>
      <Toast />
    </div>
  );
}

export default function ToastGallerySection() {
  return (
    <section>
      <h2>Toast</h2>
      <ThemeFlip>
        <ToastDemo />
      </ThemeFlip>
    </section>
  );
}
