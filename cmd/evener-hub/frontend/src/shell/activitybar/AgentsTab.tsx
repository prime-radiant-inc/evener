import { ActivityPageBoundary } from "./ActivityPageBoundary";
// The Agents tab: the scope's subagents as drillable rows, read from the
// paged subagents resource (lists and locations carry no children anymore -
// this resource is the tree). Current subagents inline, the loaded inactive
// ones behind a fold paged 20 at a time, and the wire's remainder behind a
// REAL control: the resource pages, so "Load more" fetches the next page
// instead of naming a number nobody can reach. Null resource is a loading
// state, never "no subagents".

import { activityNodeID, type SessionDelegate } from "@evener/appwire-client";
import { useState } from "react";
import { useSessionActivity } from "../../stores/sessionActivity";
import { Button, Chevron } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
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
function drill(sub: SessionDelegate): void {
  workspaceStore
    .getState()
    .openPane("transcript", { ref: sub.childRef, parentRef: sub.ownerRef }, { slot: "secondary" });
}

export function AgentsTab({ scope }: { scope: ActivityScope }) {
  const [foldOpen, setFoldOpen] = useState(false);
  const [shown, setShown] = useState(PAGE);
  const { snapshot, loadMore } = useSessionActivity(scope.leaf.ref, "session", "delegates");
  const collection = snapshot?.delegates;
  if (collection?.permanent && collection.rows.length === 0)
    return <span className={CLASS.emptyNote}>Subagents unavailable for this session.</span>;
  if (!collection || (collection.rows.length === 0 && !collection.complete)) {
    return <span className={CLASS.emptyNote}>Loading subagents…</span>;
  }
  const rows = collection.rows;
  const current = rows.filter((row) => !row.terminal);
  const inactive = rows.filter((row) => row.terminal);
  return (
    <div className={CLASS.stack}>
      {rows.length === 0 && collection.complete ? (
        <span className={CLASS.emptyNote}>No subagents at this level.</span>
      ) : null}
      {current.map((sub) => (
        <AgentRow key={activityNodeID({ ...sub, kind: "delegate" })} sub={sub} onDrill={() => drill(sub)} />
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
                <AgentRow key={activityNodeID({ ...sub, kind: "delegate" })} sub={sub} onDrill={() => drill(sub)} />
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
      <ActivityPageBoundary
        resource="delegates"
        label="subagents"
        hasMore={collection.hasMore}
        loading={collection.loading}
        error={collection.error}
        permanent={collection.permanent}
        enabled={foldOpen || inactive.length === 0}
        loadMore={loadMore}
      />
      {collection.error ? <span className={CLASS.emptyNote}>Subagents are updating…</span> : null}
    </div>
  );
}
