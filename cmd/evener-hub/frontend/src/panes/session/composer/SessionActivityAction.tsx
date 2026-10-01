import { activityPanelStore } from "../../../stores/activityPanel";
import { useSessionActivity } from "../../../stores/sessionActivity";
import { Button } from "../../../widgets";
import { activityActionLabel } from "../chrome/activityFormat";

/** Opens SessionChrome's existing sheet; the shared binding owns summary demand. */
export function SessionActivityAction({ sessionRef }: { sessionRef: string }) {
  const { snapshot } = useSessionActivity(sessionRef);
  return (
    <Button
      variant="quiet"
      size="sm"
      aria-haspopup="dialog"
      data-testid="composer-activity"
      title="Activity: active jobs and agents in this session"
      onClick={() => activityPanelStore.getState().setSheetOpen(sessionRef, true)}
    >
      {activityActionLabel(snapshot?.summary)}
    </Button>
  );
}
