import type { PaneProps } from "../../shell/paneRegistry";
import { navigate, paneToURL } from "../../shell/routing";
import { Button, EmptyState, PaneScaffold } from "../../widgets";

export interface WelcomePaneParams {
  // Shown as the empty state's hint - e.g. the note /new renders while the
  // spawn pane doesn't exist yet (Wave 6). Absent on the plain "/" welcome.
  note?: string;
}

function goToNewSession(): void {
  const url = paneToURL("spawn", {});
  if (url) navigate(url);
}

export default function Welcome({ params }: PaneProps<WelcomePaneParams>) {
  return (
    <PaneScaffold title="Welcome">
      <EmptyState
        title="No session open"
        hint={params.note}
        action={
          <Button variant="quiet" onClick={goToNewSession}>
            New session
          </Button>
        }
      />
    </PaneScaffold>
  );
}
