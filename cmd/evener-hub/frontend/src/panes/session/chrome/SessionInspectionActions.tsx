import {
  activitySidebarOpenFor,
  activitySidebarStore,
  useActivitySidebarOpenFor,
} from "../../../shell/activitybar/activitySidebarStore";
import { SearchIcon } from "../../../shell/rail/railIcons";
import { useIsMobile } from "../../../shell/useIsMobile";
import { workspaceStore } from "../../../shell/workspace";
import { Button, IconButton, Tooltip } from "../../../widgets";
import { requestFindInSession } from "../find/findRequest";

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

export function SessionInspectionActions({ sessionRef, paneId }: { sessionRef: string; paneId?: string }) {
  const isMobile = useIsMobile();
  const overviewOpen = useActivitySidebarOpenFor(sessionRef);
  const actions = [
    {
      label: "Find in session",
      text: "Find",
      icon: <SearchIcon />,
      onSelect: () => requestFindInSession(sessionRef, { target: paneId }),
      pressed: undefined,
      overviewRef: undefined,
    },
    {
      label: "Overview",
      text: "Overview",
      icon: <OverviewIcon />,
      onSelect: () => openSessionOverview(sessionRef, paneId, isMobile),
      pressed: overviewOpen,
      overviewRef: sessionRef,
    },
  ];

  return actions.map((action) =>
    isMobile ? (
      <Tooltip key={action.label} label={action.label}>
        <IconButton
          label={action.label}
          icon={action.icon}
          variant={action.pressed ? "secondary" : "quiet"}
          aria-pressed={action.pressed}
          data-session-overview-ref={action.overviewRef}
          data-pane-id={paneId}
          onClick={action.onSelect}
        />
      </Tooltip>
    ) : (
      <Button
        key={action.label}
        size="sm"
        variant={action.pressed ? "secondary" : "quiet"}
        icon={action.icon}
        aria-label={action.label}
        aria-pressed={action.pressed}
        data-session-overview-ref={action.overviewRef}
        data-pane-id={paneId}
        onClick={action.onSelect}
      >
        {action.text}
      </Button>
    ),
  );
}
