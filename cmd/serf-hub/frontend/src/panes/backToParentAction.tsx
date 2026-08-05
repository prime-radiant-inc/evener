// BackToParentAction: the shared "Back to <parent>" PaneScaffold header
// action for a contextually-opened drill-down child pane. First built for
// the read-only transcript pane (kata 0pzz - a subagent transcript is a
// child of a specific parent session, and the pane it opens must be able
// to say so and offer a way back, rather than leaving the reader to
// remember it). The doc pane (kata 9br8, opened via openDocBeside from a
// file/image tool card) is the SAME shape of contextual child, opened the
// SAME way, with the SAME parent-session return path (DocParams.session
// already IS the parent session ref, just under a different field name) -
// so this lives as one shared component instead of a second copy of the
// same button+icon+truncation rule.
import { requestPaneFocus, workspaceStore } from "../shell/workspace";
import { useThreadsStore } from "../stores/threads";
import { Button, Chevron } from "../widgets";
import { requireClass } from "../widgets/internal/requireClass";
import styles from "./backtoparentaction.module.css";

const CLASS = {
  backLabel: requireClass(styles.backLabel, "backtoparentaction.module.css", "backLabel"),
};

// The app's chevrons all come from the shared widgets/chevron module.

// A single header action that is BOTH the identity marker (its own visible
// label names the parent, so a reader lands already knowing whose child
// this is - no hover, no memory) and the return path (one click re-focuses
// that session, or reopens it if the reader closed it meanwhile -
// workspaceStore.openPane's own dedup makes "already open" vs. "reopen
// fresh" the same call). The parent's live name is read reactively (a
// rename while this pane is open stays current); a parent whose name
// hasn't hydrated yet (or was evicted after its own pane closed - threads
// are refcounted, see stores/threads.ts) falls back to the raw ref, the
// same fallback every other pane title in this app already uses.
export function BackToParentAction({ parentRef }: { parentRef: string }) {
  const name = useThreadsStore((s) => s.threads.get(parentRef)?.name);
  const label = name || parentRef;
  const focusParent = () => {
    const parentScaffold = Array.from(document.querySelectorAll<HTMLElement>("[data-pane-scaffold]")).find(
      (element) => element.dataset.paneScaffold === `session:${parentRef}`,
    );
    const parentPaneId = workspaceStore.getState().openPane("session", { ref: parentRef });
    if (parentScaffold) {
      parentScaffold.focus();
    } else {
      // An orphan return has no mounted parent body to focus yet. Leave the
      // browser's current focus alone; PaneScaffold consumes this marker and
      // focuses the restored parent scaffold once its lazy tree mounts.
      requestPaneFocus(parentPaneId);
    }
  };

  return (
    <Button variant="quiet" size="sm" icon={<Chevron direction="left" />} onClick={focusParent}>
      <span className={CLASS.backLabel}>Back to {label}</span>
    </Button>
  );
}
