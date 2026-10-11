// The job-log pane. A "job:<id>" ref is a shell job's output log, not a
// thread: it renders through the job-log surface, which never touches the
// thread engine (no thread/read, no ensureThread). The read-only session
// transcript this pane once rendered is gone — the session pane is the
// conversation view and the zoom cascade embeds it — so a legacy session-ref
// transcript pane (a stale saved layout or a URL the workspace migration
// could not rewrite) recovers as the live conversation rather than losing
// the view.
import type { PaneProps } from "../../shell/paneRegistry";
import Session from "../session/Session";
import { JobLog } from "./JobLog";

export interface TranscriptParams {
  ref: string;
  // The enclosing session's ref, when this pane was opened from a job row.
  // Carried in the pane's own params - not passed around as a one-off
  // argument - so it survives a layout restore/reload. A "job:<id>" ref
  // resolves its owning session from it (JobLog fetches output through the
  // owner, never through thread/read).
  parentRef?: string;
}

export default function Transcript({ params, paneId }: PaneProps<TranscriptParams>) {
  if (params.ref.startsWith("job:")) {
    return <JobLog jobRef={params.ref} parentRef={params.parentRef} paneId={paneId} />;
  }
  return <Session params={{ ref: params.ref }} paneId={paneId} />;
}
