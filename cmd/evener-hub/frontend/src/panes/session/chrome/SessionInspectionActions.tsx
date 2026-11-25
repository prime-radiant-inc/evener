// SessionInspectionActions: the session pane's header entry to its
// inspectors. Both desktop and mobile render Find in session then Overview
// (labeled buttons on desktop, 44px icon buttons below 900px). Overview opens
// the shared tabbed ActivitySidebar directly for that session.

import {
  activitySidebarOpenFor,
  activitySidebarStore,
  useActivitySidebarOpenFor,
} from "../../../shell/activitybar/activitySidebarStore";
import { SearchIcon } from "../../../shell/rail/railIcons";
import { useIsMobile } from "../../../shell/useIsMobile";
import { workspaceStore } from "../../../shell/workspace";
import { Button, IconButton } from "../../../widgets";
import { requestFindInSession } from "../find/findRequest";

export function openSessionOverview(
  sessionRef: string,
  paneId: string | undefined,
  isMobile: boolean,
  opener?: HTMLElement,
): void {
  // Desktop toggles only a sidebar already scoped to this session; opening it
  // from another session re-scopes rather than closing it under the reader.
  if (!isMobile && activitySidebarOpenFor(sessionRef)) {
    activitySidebarStore.getState().close();
    return;
  }
  if (paneId) workspaceStore.getState().focusPane(paneId);
  activitySidebarStore.getState().openFor(sessionRef, undefined, opener, paneId);
}

function OverviewIcon() {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <rect x="2" y="2.5" width="12" height="11" rx="1.5" fill="none" stroke="currentColor" strokeWidth="1.3" />
      <path d="M9.5 2.5v11" stroke="currentColor" strokeWidth="1.3" />
    </svg>
  );
}

export function SessionInspectionActions({ sessionRef, paneId }: { sessionRef: string; paneId?: string }) {
  const isMobile = useIsMobile();
  const overviewOpen = useActivitySidebarOpenFor(sessionRef);
  if (isMobile) {
    return (
      <>
        <IconButton
          label="Find in session"
          icon={
            <span data-pane-id={paneId}>
              <SearchIcon />
            </span>
          }
          variant="quiet"
          onClick={() => requestFindInSession(sessionRef, { target: paneId })}
        />
        <IconButton
          label="Overview"
          icon={
            <span data-session-overview-ref={sessionRef} data-pane-id={paneId}>
              <OverviewIcon />
            </span>
          }
          variant={overviewOpen ? "secondary" : "quiet"}
          aria-pressed={overviewOpen}
          onClick={(event) => openSessionOverview(sessionRef, paneId, true, event.currentTarget)}
        />
      </>
    );
  }
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
        onClick={(event) => openSessionOverview(sessionRef, paneId, false, event.currentTarget)}
      >
        Overview
      </Button>
    </>
  );
}
