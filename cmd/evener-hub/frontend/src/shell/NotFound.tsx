import { Button, EmptyState } from "../widgets";
import { navigate, paneToURL } from "./routing";

// AppShell renders this in place of DockHost when urlToPane() can't resolve
// the current path at all - a routing-level fallback, not a pane, so it
// skips PaneScaffold (that chrome is for content hosted inside a dockview
// panel; this replaces the whole workspace area instead).
function goHome(): void {
  const url = paneToURL("welcome", {});
  if (url) navigate(url);
}

export function NotFound() {
  return (
    <EmptyState
      title="Page not found"
      hint="This link doesn't match anything in serf."
      action={
        <Button variant="quiet" onClick={goHome}>
          Go home
        </Button>
      }
    />
  );
}
