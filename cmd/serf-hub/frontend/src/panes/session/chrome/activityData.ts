// Strict client-side parser and disclosure-state helpers for recursive
// serf/jobs/list activity trees. Wire truth: protocol/types.gen.ts's
// JobActivity* interfaces and docs/appwire-protocol.md's json field catalog.

export interface ActivityCounts {
  active: number;
  failed: number;
  completed: number;
  complete: boolean;
}

export interface ActivityBranchState {
  error?: string;
  truncated?: boolean;
  continuation?: string;
}

export interface ActivityJob {
  jobId: string;
  ownerSessionId: string;
  ownerRef: string;
  type: string;
  status: string;
  outcome?: string;
  terminal: boolean;
  background: boolean;
  hasOutput: boolean;
  description: string;
  command?: string;
  task?: string;
  reason?: string;
  startedAt: string;
  endedAt?: string;
  exitCode?: number;
  outputBytes: number;
}

export interface ActivityShellEntry {
  kind: "shell";
  job: ActivityJob;
}

export interface ActivitySessionNode {
  kind: "session";
  sessionId: string;
  ref: string;
  label: string;
  aggregate: string;
  counts: ActivityCounts;
  entries: ActivityEntry[];
  branch: ActivityBranchState;
}

export interface ActivityDelegate {
  delegateId: string;
  childSessionId: string;
  childRef: string;
  mandate?: string;
  turns: ActivityJob[];
  child?: ActivitySessionNode;
  branch: ActivityBranchState;
}

export interface ActivityDelegateEntry {
  kind: "delegate";
  delegate: ActivityDelegate;
}

export type ActivityEntry = ActivityShellEntry | ActivityDelegateEntry;

export interface ActivityTree {
  revision: number;
  root: ActivitySessionNode;
}

export interface ActivityDisclosureState {
  expandedIDs: string[];
  selectedID?: string;
  selectionPruned: boolean;
  tree?: ActivityTree | null;
}

const MAX_RECURSION_DEPTH = 64;
const INCOMPLETE_ERROR = "incomplete";
const DEPTH_LIMIT_ERROR = "depth limit exceeded";

type ParseResult<T> = {
  value: T | null;
  incomplete: boolean;
};

type ActivityIdentity =
  | { kind: "session"; sessionId: string }
  | { kind: "delegate"; delegateId: string }
  | { kind: "shell"; jobId: string };

type ActivityNodeLike = ActivityIdentity | ActivitySessionNode | ActivityShellEntry | ActivityDelegateEntry;

type TreeIndex = {
  ids: Set<string>;
  parents: Map<string, string>;
};

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function readString(object: Record<string, unknown>, key: string): string | null {
  const value = object[key];
  return typeof value === "string" ? value : null;
}

function readBoolean(object: Record<string, unknown>, key: string): boolean | null {
  const value = object[key];
  return typeof value === "boolean" ? value : null;
}

function readInteger(object: Record<string, unknown>, key: string): number | null {
  const value = object[key];
  return typeof value === "number" && Number.isInteger(value) ? value : null;
}

function readNonNegativeInteger(object: Record<string, unknown>, key: string): number | null {
  const value = readInteger(object, key);
  return value !== null && value >= 0 ? value : null;
}

function readOptionalString(object: Record<string, unknown>, key: string): string | undefined {
  const value = object[key];
  return typeof value === "string" && value !== "" ? value : undefined;
}

function parseBranchState(raw: unknown): ActivityBranchState | null {
  if (!isPlainObject(raw)) return null;
  const branch: ActivityBranchState = {};
  const error = readOptionalString(raw, "error");
  const truncated = raw.truncated;
  const continuation = readOptionalString(raw, "continuation");
  if (typeof truncated !== "undefined" && typeof truncated !== "boolean") return null;
  if (typeof raw.continuation !== "undefined" && typeof raw.continuation !== "string") return null;
  if (typeof raw.error !== "undefined" && typeof raw.error !== "string") return null;
  if (error) branch.error = error;
  if (typeof truncated === "boolean") branch.truncated = truncated;
  if (continuation) branch.continuation = continuation;
  return branch;
}

function parseCounts(raw: unknown): ActivityCounts | null {
  if (!isPlainObject(raw)) return null;
  const active = readNonNegativeInteger(raw, "active");
  const failed = readNonNegativeInteger(raw, "failed");
  const completed = readNonNegativeInteger(raw, "completed");
  const complete = readBoolean(raw, "complete");
  if (active === null || failed === null || completed === null || complete === null) return null;
  return { active, failed, completed, complete };
}

function parseJob(raw: unknown): ActivityJob | null {
  if (!isPlainObject(raw)) return null;
  const jobId = readString(raw, "jobId");
  const ownerSessionId = readString(raw, "ownerSessionId");
  const ownerRef = readString(raw, "ownerRef");
  const type = readString(raw, "type");
  const status = readString(raw, "status");
  const terminal = readBoolean(raw, "terminal");
  const background = readBoolean(raw, "background");
  const hasOutput = readBoolean(raw, "hasOutput");
  const description = readString(raw, "description");
  const startedAt = readString(raw, "startedAt");
  const outputBytes = readNonNegativeInteger(raw, "outputBytes");
  if (
    jobId === null ||
    ownerSessionId === null ||
    ownerRef === null ||
    type === null ||
    status === null ||
    terminal === null ||
    background === null ||
    hasOutput === null ||
    description === null ||
    startedAt === null ||
    outputBytes === null
  ) {
    return null;
  }
  const job: ActivityJob = {
    jobId,
    ownerSessionId,
    ownerRef,
    type,
    status,
    terminal,
    background,
    hasOutput,
    description,
    startedAt,
    outputBytes,
  };
  const outcome = readOptionalString(raw, "outcome");
  const command = readOptionalString(raw, "command");
  const task = readOptionalString(raw, "task");
  const reason = readOptionalString(raw, "reason");
  const endedAt = readOptionalString(raw, "endedAt");
  const exitCode = raw.exitCode;
  if (typeof raw.outcome !== "undefined" && typeof raw.outcome !== "string") return null;
  if (typeof raw.command !== "undefined" && typeof raw.command !== "string") return null;
  if (typeof raw.task !== "undefined" && typeof raw.task !== "string") return null;
  if (typeof raw.reason !== "undefined" && typeof raw.reason !== "string") return null;
  if (typeof raw.endedAt !== "undefined" && typeof raw.endedAt !== "string") return null;
  if (typeof exitCode !== "undefined" && !Number.isInteger(exitCode)) return null;
  if (outcome) job.outcome = outcome;
  if (command) job.command = command;
  if (task) job.task = task;
  if (reason) job.reason = reason;
  if (endedAt) job.endedAt = endedAt;
  if (typeof exitCode === "number") job.exitCode = exitCode;
  return job;
}

function markIncomplete(branch: ActivityBranchState): ActivityBranchState {
  return branch.error ? branch : { ...branch, error: INCOMPLETE_ERROR };
}

function applyDepthLimit(branch: ActivityBranchState): ActivityBranchState {
  return {
    ...branch,
    truncated: true,
    error: branch.error ?? DEPTH_LIMIT_ERROR,
  };
}

function parseEntry(raw: unknown, depth: number): ParseResult<ActivityEntry> {
  if (!isPlainObject(raw)) return { value: null, incomplete: true };
  const kind = readString(raw, "kind");
  if (kind === "job") {
    const job = parseJob(raw.job);
    return { value: job ? { kind: "shell", job } : null, incomplete: job === null };
  }
  if (kind === "delegate") {
    const delegate = parseDelegate(raw.delegate, depth);
    return {
      value: delegate.value ? { kind: "delegate", delegate: delegate.value } : null,
      incomplete: delegate.incomplete,
    };
  }
  return { value: null, incomplete: true };
}

function parseDelegate(raw: unknown, depth: number): ParseResult<ActivityDelegate> {
  if (!isPlainObject(raw)) return { value: null, incomplete: true };
  const delegateId = readString(raw, "delegateId");
  const childSessionId = readString(raw, "childSessionId");
  const childRef = readString(raw, "childRef");
  const branch = parseBranchState(raw.branch);
  const turnsRaw = raw.turns;
  if (
    delegateId === null ||
    childSessionId === null ||
    childRef === null ||
    branch === null ||
    !Array.isArray(turnsRaw)
  ) {
    return { value: null, incomplete: true };
  }
  const turns: ActivityJob[] = [];
  for (const turnRaw of turnsRaw) {
    const turn = parseJob(turnRaw);
    if (!turn) return { value: null, incomplete: true };
    turns.push(turn);
  }
  const delegate: ActivityDelegate = {
    delegateId,
    childSessionId,
    childRef,
    turns,
    branch,
  };
  const mandate = readOptionalString(raw, "mandate");
  if (typeof raw.mandate !== "undefined" && typeof raw.mandate !== "string") {
    return { value: null, incomplete: true };
  }
  if (mandate) delegate.mandate = mandate;

  if (typeof raw.child !== "undefined") {
    if (depth >= MAX_RECURSION_DEPTH) {
      delegate.branch = applyDepthLimit(delegate.branch);
    } else {
      const child = parseSession(raw.child, depth + 1);
      if (child.value) {
        delegate.child = child.value;
        if (child.incomplete) delegate.branch = markIncomplete(delegate.branch);
      } else {
        delegate.branch = markIncomplete(delegate.branch);
      }
    }
  }
  return { value: delegate, incomplete: false };
}

function parseSession(raw: unknown, depth: number): ParseResult<ActivitySessionNode> {
  if (!isPlainObject(raw)) return { value: null, incomplete: true };
  const sessionId = readString(raw, "sessionId");
  const ref = readString(raw, "ref");
  const label = readString(raw, "label");
  const aggregate = readString(raw, "aggregate");
  const counts = parseCounts(raw.counts);
  const branch = parseBranchState(raw.branch);
  const entriesRaw = raw.entries;
  if (
    sessionId === null ||
    ref === null ||
    label === null ||
    aggregate === null ||
    counts === null ||
    branch === null ||
    !Array.isArray(entriesRaw)
  ) {
    return { value: null, incomplete: true };
  }
  const entries: ActivityEntry[] = [];
  let incomplete = false;
  for (const entryRaw of entriesRaw) {
    const parsed = parseEntry(entryRaw, depth);
    if (parsed.value) entries.push(parsed.value);
    if (parsed.incomplete) incomplete = true;
  }
  return {
    value: {
      kind: "session",
      sessionId,
      ref,
      label,
      aggregate,
      counts,
      entries,
      branch: incomplete ? markIncomplete(branch) : branch,
    },
    incomplete,
  };
}

export function parseActivityTree(data: unknown): ActivityTree | null {
  if (!isPlainObject(data)) return null;
  const revision = readNonNegativeInteger(data, "revision");
  if (revision === null) return null;
  const root = parseSession(data.root, 1);
  if (!root.value) return null;
  return { revision, root: root.value };
}

export function activityNodeID(node: ActivityNodeLike): string {
  if (node.kind === "session" && "sessionId" in node) return `session:${node.sessionId}`;
  if (node.kind === "delegate") {
    if ("delegate" in node) return `delegate:${node.delegate.delegateId}`;
    return `delegate:${node.delegateId}`;
  }
  if (node.kind === "shell") {
    if ("job" in node) return `job:${node.job.jobId}`;
    return `job:${node.jobId}`;
  }
  throw new Error("unsupported activity node identity");
}

function jobIsActive(job: ActivityJob): boolean {
  return !job.terminal;
}

function delegateHasActiveWork(delegate: ActivityDelegate): boolean {
  return delegate.turns.some(jobIsActive) || (delegate.child ? sessionHasActiveWork(delegate.child) : false);
}

function entryHasActiveWork(entry: ActivityEntry): boolean {
  return entry.kind === "shell" ? jobIsActive(entry.job) : delegateHasActiveWork(entry.delegate);
}

function sessionHasActiveWork(session: ActivitySessionNode): boolean {
  return session.counts.active > 0 || session.entries.some(entryHasActiveWork);
}

function pushUnique(ids: string[], id: string): void {
  if (!ids.includes(id)) ids.push(id);
}

function collectDefaultExpanded(session: ActivitySessionNode, ids: string[], includeSelf: boolean): void {
  if (includeSelf) pushUnique(ids, activityNodeID(session));
  for (const entry of session.entries) {
    if (entry.kind !== "delegate") continue;
    if (!delegateHasActiveWork(entry.delegate)) continue;
    pushUnique(ids, activityNodeID(entry));
    if (entry.delegate.child && sessionHasActiveWork(entry.delegate.child)) {
      collectDefaultExpanded(entry.delegate.child, ids, true);
    }
  }
}

export function defaultExpandedIDs(tree: ActivityTree): string[] {
  const ids: string[] = [];
  collectDefaultExpanded(tree.root, ids, true);
  return ids;
}

function indexTree(tree: ActivityTree): TreeIndex {
  const ids = new Set<string>();
  const parents = new Map<string, string>();
  const visitSession = (session: ActivitySessionNode, parentID?: string) => {
    const sessionID = activityNodeID(session);
    ids.add(sessionID);
    if (parentID) parents.set(sessionID, parentID);
    for (const entry of session.entries) {
      if (entry.kind === "shell") {
        const jobID = activityNodeID(entry);
        ids.add(jobID);
        parents.set(jobID, sessionID);
        continue;
      }
      const delegateID = activityNodeID(entry);
      ids.add(delegateID);
      parents.set(delegateID, sessionID);
      for (const turn of entry.delegate.turns) {
        const turnID = activityNodeID({ kind: "shell", jobId: turn.jobId });
        ids.add(turnID);
        parents.set(turnID, delegateID);
      }
      if (entry.delegate.child) visitSession(entry.delegate.child, delegateID);
    }
  };
  visitSession(tree.root);
  return { ids, parents };
}

function nearestSurvivingOwner(
  selectedID: string | undefined,
  previous: ActivityDisclosureState,
  nextIndex: TreeIndex,
): string {
  if (!selectedID) return "";
  const fallbackRoot = previous.tree ? activityNodeID(previous.tree.root) : null;
  if (!previous.tree) return fallbackRoot ?? "";
  const previousIndex = indexTree(previous.tree);
  let cursor: string | undefined = selectedID;
  while (cursor) {
    const parent = previousIndex.parents.get(cursor);
    if (!parent) break;
    if (nextIndex.ids.has(parent)) return parent;
    cursor = parent;
  }
  return nextIndex.ids.has(fallbackRoot ?? "") ? (fallbackRoot as string) : "";
}

export function reconcileActivityState(previous: ActivityDisclosureState, next: ActivityTree): ActivityDisclosureState {
  const nextIndex = indexTree(next);
  const explicit = previous.expandedIDs.filter((id) => nextIndex.ids.has(id));
  const nextActive = defaultExpandedIDs(next);
  const previousActive = previous.tree ? new Set(defaultExpandedIDs(previous.tree)) : null;
  const expandedIDs = [...explicit];
  for (const id of nextActive) {
    if (previousActive?.has(id)) continue;
    pushUnique(expandedIDs, id);
  }

  let selectedID = previous.selectedID;
  let selectionPruned = false;
  if (selectedID && !nextIndex.ids.has(selectedID)) {
    const fallback = nearestSurvivingOwner(selectedID, previous, nextIndex) || activityNodeID(next.root);
    selectedID = fallback;
    selectionPruned = true;
  } else if (!selectedID) {
    selectedID = undefined;
  }

  return { expandedIDs, selectedID, selectionPruned };
}
