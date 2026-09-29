// The Agents tab: the scope's current subagents as drillable rows, the
// inactive ones behind a fold labelled with the TRUE total (loaded rows plus
// the wire's more_subagents), paged locally 20 at a time. The wire has no
// subagent page fetch (the rail's overflow precedent), so the unloaded
// remainder is a passive "+N more" note, never a control.

import type { NavigationSessionSummary } from "@evener/appwire-client";
import { useState } from "react";
import { Button, Chevron } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { subagentIsCurrent } from "../rail/railNodes";
import type { ActivityScope } from "../statusbar/statusScope";
import { workspaceStore } from "../workspace";
import styles from "./activitybar.module.css";
import { AgentRow } from "./activityRows";

const CLASS = {
  stack: requireClass(styles.stack, "activitybar.module.css", "stack"),
  foldButton: requireClass(styles.foldButton, "activitybar.module.css", "foldButton"),
  passiveMore: requireClass(styles.passiveMore, "activitybar.module.css", "passiveMore"),
  emptyNote: requireClass(styles.emptyNote, "activitybar.module.css", "emptyNote"),
};

const PAGE = 20;

function drill(leaf: NavigationSessionSummary, sub: NavigationSessionSummary): void {
  // Drilling opens the subagent's transcript beside the parent's: a secondary
  // pane, which re-scopes the bar and this sidebar when focused
  // (focusedSession.ts's transcript rule).
  workspaceStore.getState().openPane("transcript", { ref: sub.ref, parentRef: leaf.ref }, { slot: "secondary" });
}

export function AgentsTab({ scope }: { scope: ActivityScope }) {
  const children = scope.leaf.children ?? [];
  // One pass: subagentIsCurrent walks the child's subtree, so two filter()
  // passes would pay that walk twice (railNodes' splitChildren precedent).
  const current: NavigationSessionSummary[] = [];
  const inactive: NavigationSessionSummary[] = [];
  for (const child of children) {
    (subagentIsCurrent(child) ? current : inactive).push(child);
  }
  const more = scope.leaf.more_subagents ?? 0;
  const [foldOpen, setFoldOpen] = useState(false);
  const [shown, setShown] = useState(PAGE);
  const foldTotal = inactive.length + more;
  return (
    <div className={CLASS.stack}>
      {children.length === 0 && foldTotal === 0 ? (
        <span className={CLASS.emptyNote}>No subagents at this level.</span>
      ) : null}
      {current.map((sub) => (
        <AgentRow key={sub.ref} sub={sub} onDrill={() => drill(scope.leaf, sub)} />
      ))}
      {inactive.length === 0 && more > 0 ? (
        // Nothing loaded to fold: the wire's remainder shows directly
        // (WatchesTab's pattern), never behind a click that reveals a lone note.
        <span className={CLASS.passiveMore}>+{more} more</span>
      ) : null}
      {inactive.length > 0 ? (
        <>
          <div>
            <Button variant="quiet" size="sm" onClick={() => setFoldOpen((value) => !value)}>
              <Chevron direction={foldOpen ? "down" : "right"} /> Inactive subagents ({foldTotal})
            </Button>
          </div>
          {foldOpen ? (
            <>
              {inactive.slice(0, shown).map((sub) => (
                <AgentRow key={sub.ref} sub={sub} onDrill={() => drill(scope.leaf, sub)} />
              ))}
              {shown < inactive.length ? (
                <Button variant="quiet" size="sm" onClick={() => setShown((n) => n + PAGE)}>
                  Show {Math.min(PAGE, inactive.length - shown)} more · {inactive.length - shown} remaining
                </Button>
              ) : null}
              {more > 0 ? <span className={CLASS.passiveMore}>+{more} more</span> : null}
            </>
          ) : null}
        </>
      ) : null}
    </div>
  );
}
