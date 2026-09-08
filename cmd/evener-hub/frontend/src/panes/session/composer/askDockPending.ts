// Hook-only seam for Composer.tsx: reads the ask-dock pending flag without
// pulling AskDock.tsx (the answering surface + AskQuestionCard) into the
// composer's initial chunk. Session.tsx keeps importing the full barrel -
// it mounts AskDock itself - so this changes only what the composer pays
// for, not what any other surface loads.
import { useAskDockStore } from "./askDock/askDockStore";

export function useAskDockPending(ref: string): boolean {
  return useAskDockStore((s) => (s.byRef.get(ref)?.batches.length ?? 0) > 0);
}
