import { useEffect, useMemo, useState } from "react";
import { sessionActionError } from "../../../protocol/errors";
import { threadsStore } from "../../../stores/threads";
import { Button, Chip, CodeBlock, EmptyState } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { openTranscript } from "../transcript/openTranscript";
import type { ActivitySelectionNode } from "./ActivityTree";
import type { ActivityDelegate, ActivityJob, ActivitySessionNode } from "./activityData";
import styles from "./activitypanel.module.css";

const CLASS = {
  inspector: requireClass(styles.inspector, "activitypanel.module.css", "inspector"),
  inspectorHeader: requireClass(styles.inspectorHeader, "activitypanel.module.css", "inspectorHeader"),
  inspectorTitle: requireClass(styles.inspectorTitle, "activitypanel.module.css", "inspectorTitle"),
  inspectorSection: requireClass(styles.inspectorSection, "activitypanel.module.css", "inspectorSection"),
  inspectorMeta: requireClass(styles.inspectorMeta, "activitypanel.module.css", "inspectorMeta"),
  detailList: requireClass(styles.detailList, "activitypanel.module.css", "detailList"),
  detailRow: requireClass(styles.detailRow, "activitypanel.module.css", "detailRow"),
  detailLabel: requireClass(styles.detailLabel, "activitypanel.module.css", "detailLabel"),
  detailValue: requireClass(styles.detailValue, "activitypanel.module.css", "detailValue"),
  prompt: requireClass(styles.prompt, "activitypanel.module.css", "prompt"),
  turnsList: requireClass(styles.turnsList, "activitypanel.module.css", "turnsList"),
  turnRow: requireClass(styles.turnRow, "activitypanel.module.css", "turnRow"),
  muted: requireClass(styles.muted, "activitypanel.module.css", "muted"),
  branchError: requireClass(styles.branchError, "activitypanel.module.css", "branchError"),
  toolbar: requireClass(styles.toolbar, "activitypanel.module.css", "toolbar"),
};

function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className={CLASS.detailRow}>
      <dt className={CLASS.detailLabel}>{label}</dt>
      <dd className={CLASS.detailValue}>{children}</dd>
    </div>
  );
}

function outputOwnerForDelegate(delegate: ActivityDelegate): ActivityJob | undefined {
  for (let index = delegate.turns.length - 1; index >= 0; index -= 1) {
    const turn = delegate.turns[index];
    if (turn?.hasOutput) return turn;
  }
  return undefined;
}

function outputCaption(totalBytes: number, retainedStart: number, truncated: boolean): string | null {
  if (!truncated) return null;
  return `Showing last ${Math.max(0, totalBytes - retainedStart)} of ${totalBytes} bytes`;
}

function OutputPanel({ ownerRef, jobId }: { ownerRef: string; jobId: string }) {
  const [reloads, setReloads] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [output, setOutput] = useState<{
    tail: string;
    totalBytes: number;
    retainedStart: number;
    truncated: boolean;
  } | null>(null);

  useEffect(() => {
    const reloadToken = reloads;
    let cancelled = false;
    setLoading(true);
    setError(null);
    threadsStore
      .getState()
      .jobOutput(ownerRef, jobId)
      .then((data) => {
        if (cancelled) return;
        const value = data as { tail?: unknown; totalBytes?: unknown; retainedStart?: unknown; truncated?: unknown };
        if (
          typeof value?.tail !== "string" ||
          typeof value?.totalBytes !== "number" ||
          typeof value?.retainedStart !== "number" ||
          typeof value?.truncated !== "boolean"
        ) {
          setOutput({ tail: "", totalBytes: 0, retainedStart: 0, truncated: false });
        } else {
          setOutput({
            tail: value.tail,
            totalBytes: value.totalBytes,
            retainedStart: value.retainedStart,
            truncated: value.truncated,
          });
        }
        if (reloadToken < 0) return;
        setLoading(false);
      })
      .catch((err) => {
        if (cancelled) return;
        if (reloadToken < 0) return;
        setError(sessionActionError("Couldn't load job output", err));
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [ownerRef, jobId, reloads]);

  if (loading) return <p>Loading output…</p>;
  if (error) {
    return (
      <div>
        <p role="alert" className={CLASS.branchError}>
          {error}
        </p>
        <Button variant="quiet" size="sm" onClick={() => setReloads((value) => value + 1)}>
          Try again
        </Button>
      </div>
    );
  }
  const caption = output ? outputCaption(output.totalBytes, output.retainedStart, output.truncated) : null;
  return (
    <div>
      <div className={CLASS.toolbar}>
        <Button variant="quiet" size="sm" onClick={() => setReloads((value) => value + 1)}>
          Refresh output
        </Button>
      </div>
      {caption && <p className={CLASS.muted}>{caption}</p>}
      <CodeBlock text={output?.tail ?? ""} copyLabel="Copy output" />
    </div>
  );
}

function SessionInspector({ session }: { session: ActivitySessionNode }) {
  return (
    <div className={CLASS.inspectorSection}>
      <dl className={CLASS.detailList}>
        <DetailRow label="aggregate">{session.aggregate}</DetailRow>
        <DetailRow label="active">{session.counts.active}</DetailRow>
        <DetailRow label="failed">{session.counts.failed}</DetailRow>
        <DetailRow label="completed">{session.counts.completed}</DetailRow>
      </dl>
      {session.branch.error && <p className={CLASS.branchError}>{session.branch.error}</p>}
    </div>
  );
}

function ShellInspector({ job }: { job: ActivityJob }) {
  return (
    <div className={CLASS.inspectorSection}>
      <dl className={CLASS.detailList}>
        <DetailRow label="status">{job.status}</DetailRow>
        <DetailRow label="type">{job.type}</DetailRow>
        <DetailRow label="started">{job.startedAt}</DetailRow>
        {job.endedAt && <DetailRow label="ended">{job.endedAt}</DetailRow>}
        {job.exitCode !== undefined && <DetailRow label="exit code">{job.exitCode}</DetailRow>}
        <DetailRow label="output size">{job.outputBytes} bytes</DetailRow>
        {job.command && (
          <DetailRow label="command">
            <pre className={CLASS.prompt}>{job.command}</pre>
          </DetailRow>
        )}
        {job.task && (
          <DetailRow label="task">
            <pre className={CLASS.prompt}>{job.task}</pre>
          </DetailRow>
        )}
        {job.reason && <DetailRow label="reason">{job.reason}</DetailRow>}
      </dl>
      {job.hasOutput && <OutputPanel ownerRef={job.ownerRef} jobId={job.jobId} />}
    </div>
  );
}

function DelegateInspector({ delegate, sessionRef }: { delegate: ActivityDelegate; sessionRef: string }) {
  const latestOutputTurn = useMemo(() => outputOwnerForDelegate(delegate), [delegate]);
  return (
    <div className={CLASS.inspectorSection}>
      {delegate.mandate && (
        <div className={CLASS.inspectorSection}>
          <h3>Mandate</h3>
          <p>{delegate.mandate}</p>
        </div>
      )}
      <div className={CLASS.toolbar}>
        <Chip tone="neutral">{latestOutputTurn ? "Latest output available" : "No retained output"}</Chip>
        <Button variant="quiet" size="sm" onClick={() => openTranscript(delegate.childRef, sessionRef)}>
          Open transcript
        </Button>
      </div>
      {delegate.child && <p>Child aggregate: {delegate.child.aggregate}</p>}
      {delegate.branch.error && <p>{delegate.branch.error}</p>}
      <div className={CLASS.inspectorSection}>
        <h3>Turns</h3>
        <ol className={CLASS.turnsList}>
          {delegate.turns.map((turn) => (
            <li key={turn.jobId} className={CLASS.turnRow}>
              <span>{turn.description}</span>
              <span className={CLASS.inspectorMeta}>{turn.status}</span>
            </li>
          ))}
        </ol>
      </div>
      {latestOutputTurn && <OutputPanel ownerRef={latestOutputTurn.ownerRef} jobId={latestOutputTurn.jobId} />}
    </div>
  );
}

export interface ActivityInspectorProps {
  selection?: ActivitySelectionNode;
  removedSelectionNotice?: boolean;
  sessionRef: string;
}

export function ActivityInspector({ selection, removedSelectionNotice = false, sessionRef }: ActivityInspectorProps) {
  if (!selection) {
    return <EmptyState title="Select activity" hint="Choose a row to inspect retained activity details." />;
  }
  return (
    <div className={CLASS.inspector} data-testid="activity-inspector">
      <div className={CLASS.inspectorHeader}>
        <div>
          <h2 className={CLASS.inspectorTitle}>
            {selection.kind === "session"
              ? selection.session.label
              : selection.kind === "delegate"
                ? (selection.delegate.child?.label ?? selection.delegate.childSessionId)
                : selection.job.description}
          </h2>
          <p className={CLASS.inspectorMeta}>{selection.kind}</p>
        </div>
      </div>
      {removedSelectionNotice && <p className={CLASS.branchError}>This activity is no longer retained.</p>}
      {selection.kind === "session" && <SessionInspector session={selection.session} />}
      {selection.kind === "delegate" && <DelegateInspector delegate={selection.delegate} sessionRef={sessionRef} />}
      {selection.kind === "job" && <ShellInspector job={selection.job} />}
    </div>
  );
}
