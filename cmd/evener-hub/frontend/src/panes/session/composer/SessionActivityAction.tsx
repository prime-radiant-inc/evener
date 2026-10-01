import { activityPanelStore } from "../../../stores/activityPanel";
import { useSessionActivity } from "../../../stores/sessionActivity";
import { Button } from "../../../widgets";

/** Opens SessionChrome's existing sheet; the shared binding owns summary demand. */
export function SessionActivityAction({ sessionRef }: { sessionRef: string }) {
  const { snapshot } = useSessionActivity(sessionRef);
  const summary = snapshot?.summary;
  const label =
    summary?.delegates.known && summary.jobs.known
      ? `Activity · ${summary.delegates.active + summary.jobs.active} active`
      : "Activity";
  return (
    <Button
      variant="quiet"
      size="sm"
      aria-haspopup="dialog"
      data-testid="composer-activity"
      title="Activity: active jobs and agents in this session"
      onClick={() => activityPanelStore.getState().setSheetOpen(sessionRef, true)}
    >
      {label}
    </Button>
  );
}
