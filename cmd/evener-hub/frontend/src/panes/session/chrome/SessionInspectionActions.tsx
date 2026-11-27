// SessionInspectionActions: the session pane's header entry to its
// inspectors. Desktop renders Find in session alone, as the same icon-only
// secondary square as the closed Overview's Show chip. Below 900px the header
// renders Find in session then Overview as 44px icon buttons; Overview opens
// the shared tabbed ActivitySidebar directly for that session.

import {
  activitySidebarOpenFor,
  activitySidebarStore,
  useActivitySidebarOpenFor,
} from "../../../shell/activitybar/activitySidebarStore";
import { OverviewIcon, SearchIcon } from "../../../shell/rail/railIcons";
import { useIsMobile } from "../../../shell/useIsMobile";
import { workspaceStore } from "../../../shell/workspace";
import { IconButton } from "../../../widgets";
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

function FindInSessionButton({
  sessionRef,
  paneId,
  variant,
}: {
  sessionRef: string;
  paneId?: string;
  variant: "quiet" | "secondary";
}) {
  return (
    <IconButton
      label="Find in session"
      icon={<SearchIcon />}
      variant={variant}
      data-pane-id={paneId}
      onClick={() => requestFindInSession(sessionRef, { target: paneId })}
    />
  );
}

function MobileOverviewButton({ sessionRef, paneId }: { sessionRef: string; paneId?: string }) {
  const overviewOpen = useActivitySidebarOpenFor(sessionRef);
  return (
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
  );
}

export function SessionInspectionActions({ sessionRef, paneId }: { sessionRef: string; paneId?: string }) {
  const isMobile = useIsMobile();
  if (isMobile) {
    return (
      <>
        <FindInSessionButton sessionRef={sessionRef} paneId={paneId} variant="quiet" />
        <MobileOverviewButton sessionRef={sessionRef} paneId={paneId} />
      </>
    );
  }
  return <FindInSessionButton sessionRef={sessionRef} paneId={paneId} variant="secondary" />;
}
