import type { IDockviewHeaderActionsProps } from "dockview-core";
import { IconButton, OpenIcon } from "../widgets";
import { popOutPane } from "./paneActions";

// PopoutHeaderAction is the desktop workspace's per-group "Pop out" affordance:
// a dockview right-header action that promotes the group's focused pane to a
// native popout window (popOutPane -> dockview addPopoutGroup -> the served
// same-origin /popout.html shell). DockHost is the only place it is wired, so
// it exists only on the desktop dockview host - the mobile StackHost has no
// group headers and never renders it.
//
// It is absent when there is no focused pane to pop out, and for a group that
// is already in its own popout (or a floating) window, where re-popping out is
// meaningless.
export function PopoutHeaderAction({ activePanel, location }: IDockviewHeaderActionsProps) {
  if (!activePanel) return null;
  if (location?.type === "popout" || location?.type === "floating") return null;
  return (
    <IconButton
      label="Pop out"
      icon={<OpenIcon size={16} />}
      variant="quiet"
      size="sm"
      onClick={() => popOutPane(activePanel.id)}
    />
  );
}
