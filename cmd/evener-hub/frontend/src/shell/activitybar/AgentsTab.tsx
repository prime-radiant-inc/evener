// The Agents tab: the scope's subagents as drillable rows, read from the
// paged subagents resource (lists and locations carry no children anymore -
// this resource is the tree). Current subagents inline, the loaded inactive
// ones behind a fold paged 20 at a time, and the wire's remainder behind a
// REAL control: the resource pages, so "Load more" fetches the next page
// instead of naming a number nobody can reach. Null resource is a loading
// state, never "no subagents".

import type { NavigationSessionSummary } from "@evener/appwire-client";
import { useState } from "react";
import { navigationStore } from "../../stores/navigation/store";
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
  emptyNote: requireClass(styles.emptyNote, "activitybar.module.css", "emptyNote"),
};

const PAGE = 20;
// The resource's page size (the store's default for subagents reads).
const WIRE_PAGE = 50;

function drill(leaf: NavigationSessionSummary, sub: NavigationSessionSummary): void {
  // Drilling opens the subagent's transcript beside the parent's: a secondary
  // pane, which re-scopes the bar and this sidebar when focused
  // (focusedSession.ts's transcript rule).
  workspaceStore.getState().openPane("transcript", { ref: sub.ref, parentRef: leaf.ref }, { slot: "secondary" });
}

export function AgentsTab({ scope }: { scope: ActivityScope }) {
  const [foldOpen, setFoldOpen] = useState(false);
  const [shown, setShown] = useState(PAGE);
  if (scope.subagents === null) {
    return <span className={CLASS.emptyNote}>Loading subagents…</span>;
  }
  // Fork originals share the page's rows; they are not agents.
  const rows = scope.subagents.rows.filter((row) => row.kind === "subagent");
  // One pass: subagentIsCurrent walks the row's subtree, so two filter()
  // passes would pay that walk twice (railNodes' splitChildren precedent).
  const current: NavigationSessionSummary[] = [];
  const inactive: NavigationSessionSummary[] = [];
  for (const row of rows) {
    (subagentIsCurrent(row) ? current : inactive).push(row);
  }
  const remaining = scope.subagents.remaining;
  return (
    <div className={CLASS.stack}>
      {rows.length === 0 && remaining === 0 ? (
        <span className={CLASS.emptyNote}>No subagents at this level.</span>
      ) : null}
      {current.map((sub) => (
        <AgentRow key={sub.ref} sub={sub} onDrill={() => drill(scope.leaf, sub)} />
      ))}
      {inactive.length > 0 ? (
        <>
          <div className={CLASS.foldButton}>
            <Button variant="quiet" size="sm" onClick={() => setFoldOpen((value) => !value)}>
              <Chevron direction={foldOpen ? "down" : "right"} /> Inactive subagents ({inactive.length})
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
            </>
          ) : null}
        </>
      ) : null}
      {remaining > 0 ? (
        // The wire's remainder is a real page fetch, never a passive note:
        // every row is reachable. Offset is the loaded row count (pages are
        // contiguous 50-row slices of the same direct-children list).
        <Button
          variant="quiet"
          size="sm"
          onClick={() =>
            void navigationStore.getState().loadSubagents(scope.leaf.ref, scope.subagents?.rows.length ?? 0)
          }
        >
          Load {Math.min(WIRE_PAGE, remaining)} more · {remaining} not shown
        </Button>
      ) : null}
    </div>
  );
}
