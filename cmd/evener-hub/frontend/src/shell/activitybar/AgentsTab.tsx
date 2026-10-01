import { ActivityPageBoundary } from "./ActivityPageBoundary";
// The selected session's typed delegates: current rows stay visible, while
// inactive history opens through disclosure. Visible page boundaries add demand
// to the shared owner without acquiring another reader or retry loop.

import { activityNodeID, type SessionDelegate } from "@evener/appwire-client";
import { useLayoutEffect } from "react";
import { openTranscript } from "../../panes/session/transcript/openTranscript";
import { useSessionActivity } from "../../stores/sessionActivity";
import { Button, Chevron } from "../../widgets";
import { isDisclosureOpen, toggleDisclosure } from "../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../widgets/internal/requireClass";
import type { ActivityScope } from "../statusbar/statusScope";
import { useActivityScrollAnchor, useActivityScrollProgress, useActivityViewCurrent } from "./ActivityViewport";
import styles from "./activitybar.module.css";
import { AgentRow } from "./activityRows";
import { activitySidebarStore, useActivitySidebarStore } from "./activitySidebarStore";

const CLASS = {
  stack: requireClass(styles.stack, "activitybar.module.css", "stack"),
  foldButton: requireClass(styles.foldButton, "activitybar.module.css", "foldButton"),
  emptyNote: requireClass(styles.emptyNote, "activitybar.module.css", "emptyNote"),
};

const PAGE = 20;
function drill(sub: SessionDelegate): void {
  openTranscript(sub.childRef, sub.ownerRef);
}

export function AgentsTab({ scope }: { scope: ActivityScope }) {
  const foldID = `${scope.leaf.ref}\0inactive-delegates`;
  const foldOpen = isDisclosureOpen(foldID, false);
  const shown = useActivitySidebarStore((state) => state.views.get(scope.leaf.ref)?.categories.agents?.shown ?? PAGE);
  const { snapshot, loadMore } = useSessionActivity(scope.leaf.ref, "session", "delegates");
  const collection = snapshot?.delegates;
  const anchorID = useActivityScrollAnchor();
  const isCurrent = useActivityViewCurrent();
  const rows = collection?.rows ?? [];
  const current = rows.filter((row) => !row.terminal);
  const inactive = rows.filter((row) => row.terminal);
  const anchorIndex = inactive.findIndex((sub) => activityNodeID({ ...sub, kind: "delegate" }) === anchorID);
  const visible = Math.max(shown, Math.ceil((anchorIndex + 1) / PAGE) * PAGE);
  useLayoutEffect(() => {
    if (anchorID && foldOpen && visible > shown && isCurrent())
      activitySidebarStore.getState().setCategoryView(scope.leaf.ref, "agents", { shown: visible });
  }, [anchorID, foldOpen, visible, shown, isCurrent, scope.leaf.ref]);
  useActivityScrollProgress(
    collection?.rows.map((sub) => activityNodeID({ ...sub, kind: "delegate" })) ?? [],
    collection?.complete ?? false,
    collection?.hasMore ?? false,
  );
  if (collection?.permanent && collection.rows.length === 0)
    return <span className={CLASS.emptyNote}>Subagents unavailable for this session.</span>;
  if (!collection || (collection.rows.length === 0 && !collection.complete)) {
    return <span className={CLASS.emptyNote}>Loading subagents…</span>;
  }
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
            <Button variant="quiet" size="sm" onClick={() => toggleDisclosure(foldID, false)}>
              <Chevron direction={foldOpen ? "down" : "right"} /> Inactive subagents ({inactive.length})
            </Button>
          </div>
          {foldOpen ? (
            <>
              {inactive.slice(0, visible).map((sub) => (
                <AgentRow key={activityNodeID({ ...sub, kind: "delegate" })} sub={sub} onDrill={() => drill(sub)} />
              ))}
              {visible < inactive.length ? (
                <Button
                  variant="quiet"
                  size="sm"
                  onClick={() =>
                    activitySidebarStore.getState().setCategoryView(scope.leaf.ref, "agents", { shown: visible + PAGE })
                  }
                >
                  Show {Math.min(PAGE, inactive.length - visible)} more · {inactive.length - visible} remaining
                </Button>
              ) : null}
            </>
          ) : null}
        </>
      ) : null}
      <ActivityPageBoundary
        resource="delegates"
        label="subagents"
        rows={collection.rows}
        hasMore={collection.hasMore}
        loading={collection.loading}
        error={collection.error}
        permanent={collection.permanent}
        enabled={foldOpen || inactive.length === 0}
        loadMore={loadMore}
      />
      {collection.error && !collection.permanent ? (
        <span className={CLASS.emptyNote}>Subagents are updating…</span>
      ) : null}
    </div>
  );
}
