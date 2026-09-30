// JobLog renders a shell job's transcript - its full command plus its output
// log - inside the read-only transcript pane. A "job:<id>" ref is not a thread,
// so instead of the thread engine (useTranscript/TurnBlock) it makes two reads
// against the OWNING session: the job's metadata (evener/jobs/get) for the
// command line, and the job's output (evener/jobs/output) for the log. The
// pane's parentRef is the owning session, which every producer that opens a
// job transcript (the activity tree's rows and detail strips) already supplies.
//
// The output read starts at the bounded tail; while the server reports
// hasEarlier, a "Load earlier output" button pages backwards (beforeBytes = the
// earliest offset on screen) and prepends, so the whole log is reachable.
// Refresh re-reads both and drops the paged prefix. The metadata read is
// best-effort: a job whose command cannot be read (an older daemon, a rejected
// call, a malformed payload) still renders its log without a command line.

import type { ActivityJob, JobLogTail } from "@evener/appwire-client";
import { jobCommandLabel, parseActivityJob, parseJobLogTail } from "@evener/appwire-client";
import { Fragment, useEffect, useMemo, useState } from "react";
import { connectionStore } from "../../stores/connection";
import { threadsStore } from "../../stores/threads";
import { Button, EmptyState, PaneScaffold } from "../../widgets";
import { parseAnsiLines } from "../../widgets/codeblock/ansi";
import { AnsiLineContent } from "../../widgets/codeblock/ansiLine";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./transcript.module.css";

const CLASS = {
  body: requireClass(styles.body, "transcript.module.css", "body"),
  joblogCommand: requireClass(styles.joblogCommand, "transcript.module.css", "joblogCommand"),
  joblog: requireClass(styles.joblog, "transcript.module.css", "joblog"),
  joblogNote: requireClass(styles.joblogNote, "transcript.module.css", "joblogNote"),
};

interface JobLogContent {
  content: string;
  totalBytes: number;
  // Lifetime offset of the first byte on screen: the next page's beforeBytes.
  earliestStart: number;
  hasEarlier: boolean;
}

type JobLogState = { status: "loading" } | { status: "error"; message: string } | ({ status: "ready" } & JobLogContent);

function contentOf(tail: JobLogTail): JobLogContent {
  return {
    content: tail.tail,
    totalBytes: tail.totalBytes,
    earliestStart: tail.retainedStart,
    hasEarlier: tail.hasEarlier,
  };
}

export function JobLog({ jobRef, parentRef }: { jobRef: string; parentRef?: string }) {
  const jobId = jobRef.slice("job:".length);
  const [state, setState] = useState<JobLogState>({ status: "loading" });
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [refreshIndex, setRefreshIndex] = useState(0);
  // The job's own metadata, for the command line above the log. Best-effort:
  // a failed or malformed read leaves it null and the pane renders the log
  // alone, exactly as an older daemon's answer would.
  const [job, setJob] = useState<ActivityJob | null>(null);

  // Both reads share one ready-gate: the owning session ref is the only route
  // to either, and the one client's handshake is the race Transcript's own
  // ensureThread effect defers through. Refresh re-runs both, which also retries
  // a metadata read that failed.
  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshIndex is the Refresh button's re-run signal - the fetch inputs are unchanged by design
  useEffect(() => {
    // A pane without an owner (a producer bug, not a user state) says so
    // instead of issuing a request that could only fail less clearly.
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
        .jobGet(ownerRef, jobId)
        .then(
          (data) => {
            if (!cancelled) setJob(parseActivityJob(data));
          },
          () => {
            if (!cancelled) setJob(null);
          },
        );
      threadsStore
        .getState()
        .jobOutput(ownerRef, jobId)
        .then(
          (data) => {
            if (cancelled) return;
            const tail = parseJobLogTail(data);
            setState(
              tail === null
                ? { status: "error", message: "malformed output payload" }
                : { status: "ready", ...contentOf(tail) },
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

  function loadEarlier(): void {
    if (parentRef === undefined || state.status !== "ready" || loadingEarlier) return;
    const beforeBytes = state.earliestStart;
    setLoadingEarlier(true);
    threadsStore
      .getState()
      .jobOutput(parentRef, jobId, beforeBytes)
      .then(
        (data) => {
          const page = parseJobLogTail(data);
          setLoadingEarlier(false);
          setState((current) => {
            if (current.status !== "ready") return current;
            // A page must start strictly before the bytes on screen. One that
            // doesn't (a daemon that ignored beforeBytes and re-sent the
            // tail) ends paging rather than duplicating content.
            if (page === null || page.retainedStart >= current.earliestStart) {
              return { ...current, hasEarlier: false };
            }
            return {
              ...current,
              content: page.tail + current.content,
              totalBytes: page.totalBytes,
              earliestStart: page.retainedStart,
              hasEarlier: page.hasEarlier,
            };
          });
        },
        () => setLoadingEarlier(false),
      );
  }

  // Job output is terminal text: parse it through the codeblock ANSI
  // pipeline so escape sequences become styled runs instead of literal
  // "[2m" noise - the same treatment ActivityRowDetail's output preview and
  // CodeBlock's ansi mode give every other job-output surface. The whole
  // concatenated log (the tail plus any paged-earlier prefix) is re-parsed
  // from the top on each change, so SGR state spanning a page boundary
  // flows into the newer content exactly as the terminal emitted it.
  const content = state.status === "ready" ? state.content : "";
  const lines = useMemo(() => parseAnsiLines(content), [content]);

  const actions = (
    <Button variant="quiet" size="sm" onClick={() => setRefreshIndex((index) => index + 1)}>
      Refresh
    </Button>
  );

  // The shared label the activity strip's detail uses too, so the pane and the
  // strip word a job identically; undefined when the job carries none (or its
  // payload could not be read).
  const command = job === null ? undefined : jobCommandLabel(job);

  return (
    <PaneScaffold title={jobId} actions={actions}>
      <div className={CLASS.body}>
        {command !== undefined && (
          <code className={CLASS.joblogCommand} data-testid="joblog-command">
            {command}
          </code>
        )}
        {state.status === "loading" && <EmptyState title="Loading job output…" />}
        {state.status === "error" && <EmptyState title="Job transcript unavailable" hint={state.message} />}
        {state.status === "ready" && state.content === "" && (
          <EmptyState title="No output yet" hint="This job hasn't written anything." />
        )}
        {state.status === "ready" && state.content !== "" && (
          <>
            {state.earliestStart > 0 && (
              <span className={CLASS.joblogNote}>
                {`Output truncated — showing the last ${state.totalBytes - state.earliestStart} of ${state.totalBytes} bytes`}
                {state.hasEarlier && (
                  <>
                    {" · "}
                    <Button variant="quiet" size="xs" disabled={loadingEarlier} onClick={loadEarlier}>
                      {loadingEarlier ? "Loading…" : "Load earlier output"}
                    </Button>
                  </>
                )}
              </span>
            )}
            <pre className={CLASS.joblog} data-testid="joblog-content">
              {lines.map((line, index) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: the parsed lines are a static split of the fetched log, never reordered
                <Fragment key={index}>
                  {index > 0 ? "\n" : null}
                  <AnsiLineContent line={line} />
                </Fragment>
              ))}
            </pre>
          </>
        )}
      </div>
    </PaneScaffold>
  );
}
