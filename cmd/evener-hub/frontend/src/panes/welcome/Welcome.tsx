import { useEffect } from "react";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate } from "../../shell/routing";
import { useWorkspaceStore } from "../../shell/workspace";
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
  const hasSession = useWorkspaceStore((state) => state.panes.some((pane) => pane.type === "session"));
  useEffect(() => {
    if (focused && !hasSession && status === "missing" && window.location.pathname === "/")
      navigate(`/new${window.location.search}${window.location.hash}`, { replace: true });
  }, [focused, hasSession, status]);
  // The note is rendered by EmptyState's hint (the original rendering path,
  // so the existing "shows params.note as a hint" test stays green). It is
  // NOT also passed to WelcomeContent here: WelcomeContent.note is the
  // reusable slot for consumers that don't wrap it in EmptyState (the mobile
  // welcome panel); passing it here too would render the note twice.
  return (
    <PaneScaffold title="Welcome">
      <EmptyState
        title="No session open"
        hint={params.note}
        size="display"
        action={<WelcomeContent showNewSession showHints />}
      />
    </PaneScaffold>
  );
}
