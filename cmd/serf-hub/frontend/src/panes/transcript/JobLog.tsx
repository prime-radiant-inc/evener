// JobLog renders a shell job's transcript - its output log - inside the
// read-only transcript pane. A "job:<id>" ref is not a thread, so instead of
// the thread engine (useTranscript/TurnBlock) it fetches the job's output tail
// through serf/jobs/output against the OWNING session: the pane's parentRef,
// which every producer that opens a job transcript (the activity tree's rows
// and detail strips) already supplies. Refresh re-reads the tail; the read is
// a single bounded tail, so there is no paging.
import { useEffect, useState } from "react";
import { connectionStore } from "../../stores/connection";
import { threadsStore } from "../../stores/threads";
import { Button, EmptyState, PaneScaffold } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { BackToParentAction } from "../backToParentAction";
import styles from "./transcript.module.css";

const CLASS = {
  body: requireClass(styles.body, "transcript.module.css", "body"),
  joblog: requireClass(styles.joblog, "transcript.module.css", "joblog"),
  joblogNote: requireClass(styles.joblogNote, "transcript.module.css", "joblogNote"),
};

interface JobLogTail {
  tail: string;
  totalBytes: number;
  retainedStart: number;
  truncated: boolean;
}

// serf/jobs/output's data field crosses the wire untyped (unknown on the
// generated JobsOutputResponse); validate the appwire.JobOutputTail shape
// before trusting it rather than casting.
function parseJobLogTail(data: unknown): JobLogTail | null {
  if (typeof data !== "object" || data === null) return null;
  const raw = data as Record<string, unknown>;
  if (typeof raw.tail !== "string") return null;
  if (typeof raw.totalBytes !== "number" || typeof raw.retainedStart !== "number") return null;
  return {
    tail: raw.tail,
    totalBytes: raw.totalBytes,
    retainedStart: raw.retainedStart,
    truncated: raw.truncated === true,
  };
}

type JobLogState = { status: "loading" } | { status: "error"; message: string } | { status: "ready"; tail: JobLogTail };

export function JobLog({ jobRef, parentRef }: { jobRef: string; parentRef?: string }) {
  const jobId = jobRef.slice("job:".length);
  const [state, setState] = useState<JobLogState>({ status: "loading" });
  const [refreshIndex, setRefreshIndex] = useState(0);

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshIndex is the Refresh button's re-run signal - the fetch inputs are unchanged by design
  useEffect(() => {
    // The owner session ref is the only route to the job's log; a pane
    // without one (a producer bug, not a user state) says so instead of
    // issuing a request that could only fail less clearly.
    if (parentRef === undefined) {
      setState({ status: "error", message: "the owning session is unknown" });
      return;
    }
    const ownerRef = parentRef;
    let cancelled = false;
    let started = false;
    // Deferred until the one client is actually ready - the same handshake
    // race Transcript's own ensureThread effect defers through.
    const start = () => {
      if (started || connectionStore.getState().state !== "ready") return;
      started = true;
      threadsStore
        .getState()
        .jobOutput(ownerRef, jobId)
        .then(
          (data) => {
            if (cancelled) return;
            const tail = parseJobLogTail(data);
            setState(
              tail === null ? { status: "error", message: "malformed output payload" } : { status: "ready", tail },
            );
          },
          (err) => {
            if (!cancelled) setState({ status: "error", message: err instanceof Error ? err.message : String(err) });
          },
        );
    };
    setState({ status: "loading" });
    start();
    const unsubscribe = connectionStore.subscribe(start);
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [parentRef, jobId, refreshIndex]);

  const actions = (
    <>
      <Button variant="quiet" size="sm" onClick={() => setRefreshIndex((index) => index + 1)}>
        Refresh
      </Button>
      {parentRef !== undefined && <BackToParentAction parentRef={parentRef} />}
    </>
  );

  return (
    <PaneScaffold title={jobId} actions={actions}>
      {state.status === "loading" && <EmptyState title="Loading job output…" />}
      {state.status === "error" && <EmptyState title="Job transcript unavailable" hint={state.message} />}
      {state.status === "ready" && state.tail.tail === "" && (
        <EmptyState title="No output yet" hint="This job hasn't written anything." />
      )}
      {state.status === "ready" && state.tail.tail !== "" && (
        <div className={CLASS.body}>
          {state.tail.truncated && (
            <span className={CLASS.joblogNote}>
              {`Output truncated — showing the last ${state.tail.totalBytes - state.tail.retainedStart} of ${state.tail.totalBytes} bytes`}
            </span>
          )}
          <pre className={CLASS.joblog}>{state.tail.tail}</pre>
        </div>
      )}
    </PaneScaffold>
  );
}
