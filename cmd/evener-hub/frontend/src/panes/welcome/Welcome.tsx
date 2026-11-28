import { useEffect } from "react";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate } from "../../shell/routing";
import { useHasSessionPane } from "../../shell/workspace";
import { EmptyState, PaneScaffold } from "../../widgets";
import { useProviderSetup } from "../spawn/useProviderSetup";
import { WelcomeContent } from "./WelcomeContent";

export type WelcomePaneParams = Record<string, never>;

export default function Welcome({ focused }: PaneProps<WelcomePaneParams>) {
  const { status } = useProviderSetup();
  const hasSession = useHasSessionPane();
  useEffect(() => {
    if (focused && !hasSession && status === "missing" && window.location.pathname === "/")
      navigate(`/new${window.location.search}${window.location.hash}`, { replace: true });
  }, [focused, hasSession, status]);
  return (
    <PaneScaffold title="Welcome">
      <EmptyState
        title="No session open"
        reason="no-session"
        size="display"
        action={<WelcomeContent showNewSession showHints />}
      />
    </PaneScaffold>
  );
}
