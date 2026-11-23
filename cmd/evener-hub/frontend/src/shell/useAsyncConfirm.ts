import { useState } from "react";

/** Busy-lock a dialog-confirmed action: one submission at a time, close only on success, stay open on rejection. */
export function useAsyncConfirm() {
  const [busy, setBusy] = useState(false);
  async function run(action: () => Promise<void>, close: () => void): Promise<void> {
    if (busy) return;
    setBusy(true);
    try {
      await action();
      close();
    } catch {
      // The action's adapter reports the failure; retain confirmation for retry.
    }
    setBusy(false);
  }
  return { busy, run };
}
