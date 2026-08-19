import type { IDockviewHeaderActionsProps } from "dockview-core";
import { IconButton } from "../widgets";
import { popOutPane } from "./paneActions";

// An "out of the box" glyph in the app's 16x16 stroke grammar (matches
// mobile/StackHost's BackIcon): a box with its top-right corner open and an
// arrow leaving through it.
function PopoutIcon() {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <path
        d="M12.5 8.5V12.5H3.5V3.5H7.5"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
      <path
        d="M8 8L13 3M9.5 3H13V6.5"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}

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
      icon={<PopoutIcon />}
      variant="quiet"
      size="sm"
      onClick={() => popOutPane(activePanel.id)}
    />
  );
}
