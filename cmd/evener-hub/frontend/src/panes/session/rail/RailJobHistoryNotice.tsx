// The rail's degraded-job presentation. A complete job projection stays
// silent; an incomplete one is announced politely, and the two states a retry
// can resolve (degraded, fatal) carry the retry that resolves them.
import type { JSX } from "react";
import { Button, InlineNotice } from "../../../widgets";
import type { RailJobHistory } from "./railSummary";

const MESSAGE: Record<Exclude<RailJobHistory["status"], "complete">, string> = {
  incomplete: "Job history is incomplete. Some job lanes may be missing.",
  degraded: "Job history may be out of date. Job lanes can be missing or still marked running.",
  fatal: "Job history is unavailable.",
};

export interface RailJobHistoryNoticeProps {
  /** null before the first projection exists: nothing is announced yet. */
  jobHistory: RailJobHistory | null;
  onRefresh: () => void;
}

export function RailJobHistoryNotice({ jobHistory, onRefresh }: RailJobHistoryNoticeProps): JSX.Element | null {
  if (!jobHistory || jobHistory.status === "complete") return null;
  return (
    <InlineNotice
      tone="neutral"
      live="status"
      data-job-history={jobHistory.status}
      action={
        jobHistory.status === "incomplete" ? undefined : (
          <Button variant="link" onClick={onRefresh}>
            Retry
          </Button>
        )
      }
    >
      {MESSAGE[jobHistory.status]}
    </InlineNotice>
  );
}
