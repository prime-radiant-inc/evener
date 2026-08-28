import type { PaneProps } from "../../shell/paneRegistry";
import { EmptyState, PaneScaffold } from "../../widgets";
import { WelcomeContent } from "./WelcomeContent";

export interface WelcomePaneParams {
  // Shown as the empty state's hint - e.g. the note /new renders while the
  // spawn pane doesn't exist yet (Wave 6). Absent on the plain "/" welcome.
  note?: string;
}

export default function Welcome({ params }: PaneProps<WelcomePaneParams>) {
  // The note is rendered by EmptyState's hint (the original rendering path,
  // so the existing "shows params.note as a hint" test stays green). It is
  // NOT also passed to WelcomeContent here: WelcomeContent.note is the
  // reusable slot for consumers that don't wrap it in EmptyState (the mobile
  // welcome panel); passing it here too would render the note twice.
  return (
    <PaneScaffold title="Welcome">
      <EmptyState title="No session open" hint={params.note} action={<WelcomeContent showNewSession showHints />} />
    </PaneScaffold>
  );
}
