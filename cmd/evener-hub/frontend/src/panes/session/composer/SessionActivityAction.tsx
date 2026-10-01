import { activitySidebarStore, closeSessionActivityPanes } from "../../../shell/activitybar/activitySidebarStore";
import { useSessionActivity } from "../../../stores/sessionActivity";
import { Button } from "../../../widgets";
import { activityActionLabel } from "../chrome/activityFormat";

/** Opens the shared activity sidebar; the binding owns summary demand. */
export function SessionActivityAction({ sessionRef }: { sessionRef: string }) {
  const { snapshot } = useSessionActivity(sessionRef);
  return (
    <Button
      variant="quiet"
      size="sm"
      aria-haspopup="dialog"
      data-testid="composer-activity"
      title="Activity: active jobs and agents in this session"
      onClick={() => {
        closeSessionActivityPanes(sessionRef);
        activitySidebarStore.getState().openWith();
      }}
    >
      {activityActionLabel(snapshot?.summary)}
    </Button>
  );
}
