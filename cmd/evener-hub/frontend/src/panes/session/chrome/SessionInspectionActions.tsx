// SessionInspectionActions: the session pane's header entry to its
// inspectors. Desktop keeps the Find + Overview button pair. The phone folds
// both into ONE entry (docs/web-ui/design-system.md section 12): an Overview
// trigger opening a bottom sheet that holds the session facts (the same
// DetailsPanelBody the Details pane shows), the Find action, the activity
// counts as rows that open the shared ActivitySidebar on their tab, and the
// session links - one entry to every inspector, and nothing in the
// composer.

import { taskSettledCount } from "@evener/appwire-client";
import { fileURLToPath } from "@evener/appwire-client/docContent";
import { type RefObject, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  activitySidebarOpenFor,
  activitySidebarStore,
  useActivitySidebarOpenFor,
} from "../../../shell/activitybar/activitySidebarStore";
import { ACTIVITY_TABS } from "../../../shell/activitybar/activityTabs";
import { useLocalSessionBranch } from "../../../shell/gitLocation";
import { SearchIcon } from "../../../shell/rail/railIcons";
import type { ActivityTab } from "../../../shell/statusbar/statusScope";
import { useActivityScope } from "../../../shell/statusbar/useActivityScope";
import { useIsMobile } from "../../../shell/useIsMobile";
import { workspaceStore } from "../../../shell/workspace";
import { useThreadsStore } from "../../../stores/threads";
import { Button, Eyebrow, IconButton, ListRow, Sheet } from "../../../widgets";
import { isWebHref } from "../../../widgets/contextcard";
import { requireClass } from "../../../widgets/internal/requireClass";
import { usePortalDocument } from "../../../widgets/internal/usePortalDocument";
import { requestFindInSession } from "../find/findRequest";
import { useNowTick, WORK_TICK_MS } from "../liveness";
import { FileOpenBesideButton } from "../transcript/fileOpenBeside";
import { DetailsPanelBody } from "./DetailsPanel";
import styles from "./sessioninspectionsheet.module.css";

const CLASS = {
  body: requireClass(styles.body, "sessioninspectionsheet.module.css", "body"),
  group: requireClass(styles.group, "sessioninspectionsheet.module.css", "group"),
  list: requireClass(styles.list, "sessioninspectionsheet.module.css", "list"),
  glyph: requireClass(styles.glyph, "sessioninspectionsheet.module.css", "glyph"),
};

export function openSessionOverview(sessionRef: string, paneId: string | undefined, isMobile: boolean): void {
  // Desktop toggles only a sidebar already scoped to this session; opening it
  // from another session re-scopes rather than closing it under the reader.
  if (!isMobile && activitySidebarOpenFor(sessionRef)) {
    activitySidebarStore.getState().close();
    return;
  }
  if (paneId) workspaceStore.getState().focusPane(paneId);
  activitySidebarStore.getState().openFor(sessionRef, undefined, undefined, paneId);
}

function OverviewIcon() {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <rect x="2" y="2.5" width="12" height="11" rx="1.5" fill="none" stroke="currentColor" strokeWidth="1.3" />
      <path d="M9.5 2.5v11" stroke="currentColor" strokeWidth="1.3" />
    </svg>
  );
}

/** The sheet's content, mounted only while open: the work-time clock rides
 * here (liveness.ts's ticks are transient by design), so a closed sheet runs
 * no timer. */
function OverviewSheetBody({
  sessionRef,
  paneId,
  triggerRef,
  onDismiss,
}: {
  sessionRef: string;
  paneId?: string;
  triggerRef: RefObject<HTMLButtonElement | null>;
  onDismiss: () => void;
}) {
  const model = useThreadsStore((s) => s.threads.get(sessionRef));
  const resolvedBranch = useLocalSessionBranch(sessionRef, model);
  const now = useNowTick(WORK_TICK_MS);
  const scope = useActivityScope(sessionRef);
  // Either source can report a known list, including an authoritative zero.
  const tasks = model?.tasks ?? scope.leaf.tasks;
  const counts =
    tasks == null ? scope.counts : { ...scope.counts, tasksSettled: taskSettledCount(tasks), tasksTotal: tasks.total };
  const tabs = ACTIVITY_TABS.filter((tab) => (tab.id === "tasks" ? tasks != null : tab.chipVisible(counts)));

  const openTab = (tab: ActivityTab) => {
    if (paneId) workspaceStore.getState().focusPane(paneId);
    // The trigger is the top-bar entry, not the row: the row unmounts with
    // this sheet, while the StackHost bar persists across focus changes, so
    // the sidebar's dismissal can keep its promise to return focus there
    // even after a drill moved the pane underneath.
    activitySidebarStore.getState().openFor(sessionRef, tab, triggerRef.current ?? undefined, paneId);
    onDismiss();
  };

  return (
    <div className={CLASS.body}>
      {model && <DetailsPanelBody model={model} now={now} resolvedBranch={resolvedBranch} />}
      <ListRow
        variant="plain"
        label="Find in session"
        onSelect={() => {
          requestFindInSession(sessionRef, { target: paneId });
          onDismiss();
        }}
      />
      <section className={CLASS.group}>
        <Eyebrow as="h3">Activity</Eyebrow>
        <ul className={CLASS.list} aria-label="Session activity">
          {tabs.map((tab) => (
            <li key={tab.id}>
              <ListRow
                variant="plain"
                data-activity-tab={tab.id}
                data-pane-id={paneId}
                data-session-ref={sessionRef}
                label={tab.label}
                leading={
                  <span className={CLASS.glyph} aria-hidden="true">
                    {tab.glyph}
                  </span>
                }
                value={tab.chipCount(counts)}
                aria-label={`${tab.chipLabel(counts)} - open Overview`}
                onSelect={() => openTab(tab.id)}
              />
            </li>
          ))}
        </ul>
      </section>
      {model && model.sessionUrls.length > 0 && (
        <section className={CLASS.group}>
          <Eyebrow as="h3">Links</Eyebrow>
          <ul className={CLASS.list} aria-label="Session links">
            {model.sessionUrls.map((url) => (
              <li key={url.id}>
                {isWebHref(url.url) ? (
                  // The destination stays visible beside an agent-controlled
                  // label (the shared file-link and notes rule: a trusted-
                  // looking label over a phishing URL must never display the
                  // label alone).
                  <ListRow
                    variant="plain"
                    data-testid={`overview-sheet-link-${url.id}`}
                    label={url.label || url.url}
                    meta={url.label ? url.url : undefined}
                    onSelect={() => window.open(url.url, "_blank", "noopener,noreferrer")}
                  />
                ) : /^file:\/\//.test(url.url) ? (
                  // Local file references open through the in-app
                  // document/open-beside flow, the same control the notes
                  // body offers; out-of-scope paths keep the visible text
                  // but no affordance.
                  <ListRow
                    variant="plain"
                    data-testid={`overview-sheet-link-${url.id}`}
                    label={url.label || url.url}
                    meta={url.label ? url.url : undefined}
                    action={
                      <FileOpenBesideButton
                        absPath={fileURLToPath(url.url)}
                        sessionRef={sessionRef}
                        cwd={model.cwd}
                        sourcePaneId={paneId}
                      />
                    }
                  />
                ) : (
                  <ListRow
                    variant="plain"
                    data-testid={`overview-sheet-link-${url.id}`}
                    label={url.label || url.url}
                    meta={url.label ? url.url : undefined}
                  />
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

/** The phone's one inspector entry: the trigger plus the sheet it opens. */
function MobileOverviewEntry({
  sessionRef,
  paneId,
  overviewOpen,
}: {
  sessionRef: string;
  paneId?: string;
  overviewOpen: boolean;
}) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const sheetDocument = usePortalDocument(triggerRef, open);
  return (
    <>
      <IconButton
        ref={triggerRef}
        label="Overview"
        icon={
          <span data-session-overview-ref={sessionRef} data-pane-id={paneId}>
            ⋯
          </span>
        }
        variant={overviewOpen ? "secondary" : "quiet"}
        aria-pressed={overviewOpen}
        onClick={() => setOpen(true)}
      />
      {/* Anchor to the viewport, outside the header's floating blur. */}
      {sheetDocument !== null &&
        createPortal(
          <Sheet side="bottom" open={open} onClose={() => setOpen(false)} title="Overview">
            {open ? (
              <OverviewSheetBody
                sessionRef={sessionRef}
                paneId={paneId}
                triggerRef={triggerRef}
                onDismiss={() => setOpen(false)}
              />
            ) : null}
          </Sheet>,
          sheetDocument.body,
        )}
    </>
  );
}

export function SessionInspectionActions({ sessionRef, paneId }: { sessionRef: string; paneId?: string }) {
  const isMobile = useIsMobile();
  const overviewOpen = useActivitySidebarOpenFor(sessionRef);
  if (isMobile) return <MobileOverviewEntry sessionRef={sessionRef} paneId={paneId} overviewOpen={overviewOpen} />;
  return (
    <>
      <Button
        size="sm"
        variant="quiet"
        icon={<SearchIcon />}
        aria-label="Find in session"
        data-pane-id={paneId}
        onClick={() => requestFindInSession(sessionRef, { target: paneId })}
      >
        Find
      </Button>
      <Button
        size="sm"
        variant={overviewOpen ? "secondary" : "quiet"}
        icon={<OverviewIcon />}
        aria-label="Overview"
        aria-pressed={overviewOpen}
        data-session-overview-ref={sessionRef}
        data-pane-id={paneId}
        onClick={() => openSessionOverview(sessionRef, paneId, isMobile)}
      >
        Overview
      </Button>
    </>
  );
}
