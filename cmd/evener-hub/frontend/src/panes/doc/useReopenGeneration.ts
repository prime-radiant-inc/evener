import { useRef } from "react";
import { documentPaneState, useWorkspaceStore } from "../../shell/workspace";

// The retained pane record's reopen generation: openDoc increments it when the
// same document is opened again into an already-mounted pane, and the reader
// refreshes on the increment. The subscription must NOT re-render a pane whose
// record disappears at close - a dying pane re-rendering re-creates disposed
// work and issues a read that supersedes live peers' tickets - so the snapshot
// keeps the last generation seen while the record existed: a closing pane's
// snapshot is stable, and only a real increment changes it.
export function useReopenGeneration(paneId: string): number {
  const lastGeneration = useRef(0);
  return useWorkspaceStore((state) => {
    const pane = state.panes.find((candidate) => candidate.id === paneId);
    if (pane) {
      const generation = documentPaneState(pane)?.reopen ?? 0;
      if (generation > 0) lastGeneration.current = generation;
    }
    return lastGeneration.current;
  });
}
