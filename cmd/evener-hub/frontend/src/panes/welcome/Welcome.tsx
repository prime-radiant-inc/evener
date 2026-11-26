import { useEffect } from "react";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate } from "../../shell/routing";
import { useHasSessionPane } from "../../shell/workspace";
import { EmptyState, PaneScaffold } from "../../widgets";
import { useProviderSetup } from "../spawn/useProviderSetup";
import { WelcomeContent } from "./WelcomeContent";

export interface WelcomePaneParams {
  // Shown as the empty state's hint - e.g. the note /new renders while the
  // spawn pane doesn't exist yet (Wave 6). Absent on the plain "/" welcome.
  note?: string;
}

export default function Welcome({ params, focused }: PaneProps<WelcomePaneParams>) {
  const { status } = useProviderSetup();
  const hasSession = useHasSessionPane();
  useEffect(() => {
    if (focused && !hasSession && status === "missing" && window.location.pathname === "/")
      navigate(`/new${window.location.search}${window.location.hash}`, { replace: true });
  }, [focused, hasSession, status]);
  // EmptyState renders params.note via its hint slot.
  return (
    <PaneScaffold title="Welcome">
      <EmptyState
        title="No session open"
        hint={params.note}
        reason="no-session"
        size="display"
        action={<WelcomeContent showNewSession showHints />}
      />
    </PaneScaffold>
  );
}
