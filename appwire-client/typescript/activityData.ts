// Strict client-side parser and disclosure-state helpers for recursive
// evener/jobs/list activity trees. Wire truth: appwire-client/typescript/types.gen.ts's
// JobActivity* interfaces and docs/appwire-protocol.md's json field catalog.
import { isPlainObject } from "./plainObject";

export interface ActivityCounts {
  active: number;
  failed: number;
  completed: number;
  complete: boolean;
}

// A terminal entry's failure is the outcome the daemon already decided
// (agent/jobs_activity.go's aggregateActivity counts nothing else), and each
// kind states it in its own vocabulary. activityOutcome derives a shell job's
// and a delegate turn's outcome from its jobstore status, so a failure arrives
// as "failure"; a stable delegate carries its delegatestore outcome verbatim,
// so a failure arrives as "failed" or "exhausted". These are the one definition
// per kind, shared by the rows and by the merged summaries.
// The status fallback below serves readers with no outcome (the activity
// tree's status dot): it knows the daemon's failure statuses, including the
// command-outcome statuses a nonzero exit or a signal death ends in.
export function isFailedJobOutcome(outcome: string | undefined): boolean {
  return outcome === "failure";
}

export function isFailedDelegateOutcome(outcome: string | undefined): boolean {
  return outcome === "failed" || outcome === "exhausted";
}

export function isActivityFailure(outcome: string | undefined, status: string | undefined): boolean {
  if (isFailedJobOutcome(outcome) || isFailedDelegateOutcome(outcome)) return true;
  const normalized = status?.trim().toLowerCase();
  return (
    normalized === "failed" ||
    normalized === "exhausted" ||
    normalized === "error" ||
    normalized === "command_exited_nonzero" ||
    normalized === "command_killed"
  );
}

// The display word for a daemon job status. Rows state a job's status by
// design (the searchable, honest machine vocabulary) - EXCEPT the two
// command-outcome statuses, whose 23-char snake_case form ellipsizes
// mid-word in the rail's narrow column and disagrees with the words the
// notification card already ruled for them ("Command failed" /
// "Command killed", steeringClassify's terminalJobTitle). Those two, and
// only those, render under the card's display words. A legacy pre-split
// "failed" record joins them when its reason names the command's own
// outcome, so durable history reads the same across every surface.
export function jobStatusDisplay(status: string, reason?: string): string {
  switch (status) {
    case "command_exited_nonzero":
      return "Command failed";
    case "command_killed":
      return "Command killed";
    case "failed": {
      const trimmedReason = reason?.trim() ?? "";
      if (trimmedReason === "exit_nonzero") return "Command failed";
      if (trimmedReason.startsWith("killed_by_signal")) return "Command killed";
      return status;
    }
    default:
      return status;
  }
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
  transcriptRef?: string;
  parentDelegateId?: string;
  terminal: boolean;
  background: boolean;
  hasOutput: boolean;
  description: string;
  command?: string;
  task?: string;
  reason?: string;
  startedAt: string;
  endedAt?: string;
  lastOutputAt?: string;
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
  // What the daemon could not render of this session and why: the
  // continuation-path bound (agent/jobs_activity.go's
  // activityUnreachableByPathDiagnostic) and the journal conditions a
  // generation cannot express (an unreadable or torn delegates.jsonl). Kept
  // apart from branch, which says only that the list stopped, not why.
  diagnostics?: string[];
  branch: ActivityBranchState;
}

export interface ActivityDelegate {
  delegateId: string;
  ownerSessionId?: string;
  rootSessionId?: string;
  childSessionId: string;
  childRef: string;
  transcriptRef?: string;
  parentDelegateId?: string;
  type?: string;
  lifecycle?: string;
  phase?: string;
  status?: string;
  projectionRevision?: number;
  outcome?: string;
  reason?: string;
  terminal?: boolean;
  resumable?: boolean;
  notResumableReason?: string;
  mandate?: string;
  task?: string;
  description?: string;
  agentType?: string;
  requestedModel?: string;
  resolvedProfileId?: string;
  resolvedModel?: string;
  model?: string;
  reasoningEffort?: string;
  originTurnId?: string;
  originToolCallId?: string;
  originItemId?: string;
  runStartedAt?: string;
  runEndedAt?: string;
  latestActivityAt?: string;
  runningForMs?: number | null;
  quietForMs?: number | null;
  durationMs?: number | null;
  packetKind?: string;
  message?: unknown;
  structuredResult?: unknown;
  structuredResultValid?: boolean;
  structuredResultReason?: string;
  warnings?: string[];
  diagnostics?: string[];
  exhaustionBudget?: string;
  exhaustionLimit?: number;
  exhaustionResumable?: boolean;
  delegationAllowance?: number;
  parentWatchGranted?: boolean;
  worktree?: ActivityWorktree;
  usage?: ActivityUsage;
  turns?: ActivityJob[];
  child?: ActivitySessionNode;
  branch: ActivityBranchState;
}

// The one place that decides which shape a delegate is. `type` is optional on
// the wire (appwire/types.go gives it `json:"type,omitempty"`, and the
// generated types.gen.ts declares `type?: string`), so an empty value arrives
// as no field at all - and the daemon's only delegate construction site sets
// "delegate" (agent/jobs_activity.go:988). A turn container is therefore a
// delegate that says it is something else; silence means the stable form, the
// only shape the daemon actually emits.
//
// No turn-container type exists yet, so this knowingly sends an unrecognized
// one down the container path - no count of its own, no projection fence -
// rather than the milder stable default. Listing recognized values instead
// would mean writing today's fixture string into the protocol, and an empty
// list would leave the container path unreachable. When a real
// turn-container type is defined, narrow this to that value.
export function isTurnContainer(delegate: Pick<ActivityDelegate, "type">): boolean {
  return !!delegate.type && delegate.type !== "delegate";
}

export interface ActivityWorktree {
  path: string;
  branch: string;
  headSha: string;
  ahead: number;
  dirty: boolean;
}

export interface ActivityUsage {
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens?: number;
  totalTokens?: number;
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

export type ActivityNodeLike = ActivityIdentity | ActivitySessionNode | ActivityShellEntry | ActivityDelegateEntry;

type TreeIndex = {
  ids: Set<string>;
  parents: Map<string, string>;
};

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
  return typeof value === "number" && Number.isSafeInteger(value) ? value : null;
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

function parseUsage(raw: unknown): ActivityUsage | null | undefined {
  if (typeof raw === "undefined") return undefined;
  if (!isPlainObject(raw)) return null;
  const hasUsageValue = ["inputTokens", "outputTokens", "cacheReadTokens", "totalTokens"].some(
    (key) => typeof raw[key] !== "undefined",
  );
  if (!hasUsageValue) return undefined;
  // The wire fields use omitempty, so an omitted counter is an explicit zero
  // from a sparse nonempty usage snapshot, rather than an incomplete record.
  const inputTokens = typeof raw.inputTokens === "undefined" ? 0 : readNonNegativeInteger(raw, "inputTokens");
  const outputTokens = typeof raw.outputTokens === "undefined" ? 0 : readNonNegativeInteger(raw, "outputTokens");
  if (inputTokens === null || outputTokens === null) return null;
  const usage: ActivityUsage = { inputTokens, outputTokens };
  const cacheReadTokens = readNonNegativeInteger(raw, "cacheReadTokens");
  const totalTokens = readNonNegativeInteger(raw, "totalTokens");
  if (typeof raw.cacheReadTokens !== "undefined" && cacheReadTokens === null) return null;
  if (typeof raw.totalTokens !== "undefined" && totalTokens === null) return null;
  if (cacheReadTokens !== null) usage.cacheReadTokens = cacheReadTokens;
  if (totalTokens !== null) usage.totalTokens = totalTokens;
  return usage;
}

function parseStringArray(raw: unknown): string[] | null | undefined {
  if (typeof raw === "undefined") return undefined;
  if (!Array.isArray(raw) || raw.some((value) => typeof value !== "string")) return null;
  return [...raw];
}

function parseWorktree(raw: unknown): ActivityWorktree | null | undefined {
  if (typeof raw === "undefined") return undefined;
  if (!isPlainObject(raw)) return null;
  const path = readString(raw, "path");
  const branch = readString(raw, "branch");
  const headSha = readString(raw, "headSha");
  const ahead = readInteger(raw, "ahead");
  const dirty = readBoolean(raw, "dirty");
  if (path === null || branch === null || headSha === null || ahead === null || dirty === null) return null;
  return { path, branch, headSha, ahead, dirty };
}

function copyOptionalString(raw: Record<string, unknown>, target: Record<string, unknown>, key: string): boolean {
  if (typeof raw[key] === "undefined") return true;
  if (typeof raw[key] !== "string") return false;
  target[key] = raw[key];
  return true;
}

function copyOptionalBoolean(raw: Record<string, unknown>, target: Record<string, unknown>, key: string): boolean {
  if (typeof raw[key] === "undefined") return true;
  if (typeof raw[key] !== "boolean") return false;
  target[key] = raw[key];
  return true;
}

function copyOptionalInteger(
  raw: Record<string, unknown>,
  target: Record<string, unknown>,
  key: string,
  nullable = false,
): boolean {
  const value = raw[key];
  if (typeof value === "undefined") return true;
  if (nullable && value === null) {
    target[key] = null;
    return true;
  }
  if (typeof value !== "number" || !Number.isSafeInteger(value)) return false;
  target[key] = value;
  return true;
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
  const transcriptRef = readOptionalString(raw, "transcriptRef");
  const parentDelegateId = readOptionalString(raw, "parentDelegateId");
  const command = readOptionalString(raw, "command");
  const task = readOptionalString(raw, "task");
  const reason = readOptionalString(raw, "reason");
  const endedAt = readOptionalString(raw, "endedAt");
  const lastOutputAt = readOptionalString(raw, "lastOutputAt");
  const exitCode = raw.exitCode;
  if (typeof raw.outcome !== "undefined" && typeof raw.outcome !== "string") return null;
  if (typeof raw.transcriptRef !== "undefined" && typeof raw.transcriptRef !== "string") return null;
  if (typeof raw.parentDelegateId !== "undefined" && typeof raw.parentDelegateId !== "string") return null;
  if (typeof raw.command !== "undefined" && typeof raw.command !== "string") return null;
  if (typeof raw.task !== "undefined" && typeof raw.task !== "string") return null;
  if (typeof raw.reason !== "undefined" && typeof raw.reason !== "string") return null;
  if (typeof raw.endedAt !== "undefined" && typeof raw.endedAt !== "string") return null;
  if (typeof raw.lastOutputAt !== "undefined" && typeof raw.lastOutputAt !== "string") return null;
  if (typeof exitCode !== "undefined" && !Number.isSafeInteger(exitCode)) return null;
  if (outcome) job.outcome = outcome;
  if (transcriptRef) job.transcriptRef = transcriptRef;
  if (parentDelegateId) job.parentDelegateId = parentDelegateId;
  if (command) job.command = command;
  if (task) job.task = task;
  if (reason) job.reason = reason;
  if (endedAt) job.endedAt = endedAt;
  if (lastOutputAt) job.lastOutputAt = lastOutputAt;
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
  if (kind === "shell") {
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
  if (delegateId === null || childSessionId === null || childRef === null || branch === null) {
    return { value: null, incomplete: true };
  }
  const delegate: ActivityDelegate = {
    delegateId,
    childSessionId,
    childRef,
    branch,
  };
  const target = delegate as unknown as Record<string, unknown>;
  const stringFields = [
    "ownerSessionId",
    "rootSessionId",
    "transcriptRef",
    "parentDelegateId",
    "type",
    "lifecycle",
    "phase",
    "status",
    "outcome",
    "reason",
    "notResumableReason",
    "task",
    "description",
    "agentType",
    "requestedModel",
    "resolvedProfileId",
    "resolvedModel",
    "model",
    "reasoningEffort",
    "originTurnId",
    "originToolCallId",
    "originItemId",
    "runStartedAt",
    "runEndedAt",
    "latestActivityAt",
    "packetKind",
    "structuredResultReason",
    "exhaustionBudget",
  ];
  for (const field of stringFields) {
    if (!copyOptionalString(raw, target, field)) return { value: null, incomplete: true };
  }
  for (const field of ["terminal", "resumable", "structuredResultValid", "exhaustionResumable", "parentWatchGranted"]) {
    if (!copyOptionalBoolean(raw, target, field)) return { value: null, incomplete: true };
  }
  for (const field of ["projectionRevision", "exhaustionLimit", "delegationAllowance"]) {
    if (!copyOptionalInteger(raw, target, field)) return { value: null, incomplete: true };
  }
  for (const field of ["runningForMs", "quietForMs", "durationMs"]) {
    if (!copyOptionalInteger(raw, target, field, true)) return { value: null, incomplete: true };
  }
  if (Array.isArray(raw.turns)) {
    const turns = raw.turns.map(parseJob);
    if (turns.some((turn) => turn === null)) return { value: null, incomplete: true };
    delegate.turns = turns as ActivityJob[];
  } else if (typeof raw.turns !== "undefined" && raw.turns !== null) {
    return { value: null, incomplete: true };
  }
  if (Object.hasOwn(raw, "message")) delegate.message = raw.message;
  if (Object.hasOwn(raw, "structuredResult")) delegate.structuredResult = raw.structuredResult;
  const warnings = parseStringArray(raw.warnings);
  const diagnostics = parseStringArray(raw.diagnostics);
  const worktree = parseWorktree(raw.worktree);
  if (warnings === null || diagnostics === null || worktree === null) return { value: null, incomplete: true };
  if (warnings) delegate.warnings = warnings;
  if (diagnostics) delegate.diagnostics = diagnostics;
  if (worktree) delegate.worktree = worktree;
  const mandate = readOptionalString(raw, "mandate");
  if (typeof raw.mandate !== "undefined" && typeof raw.mandate !== "string") {
    return { value: null, incomplete: true };
  }
  if (mandate) delegate.mandate = mandate;

  const usage = parseUsage(raw.usage);
  if (usage === null) return { value: null, incomplete: true };
  if (usage) delegate.usage = usage;

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
  const diagnostics = parseStringArray(raw.diagnostics);
  const branch = parseBranchState(raw.branch);
  const entriesRaw = raw.entries;
  if (
    sessionId === null ||
    ref === null ||
    label === null ||
    aggregate === null ||
    counts === null ||
    diagnostics === null ||
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
  const session: ActivitySessionNode = {
    kind: "session",
    sessionId,
    ref,
    label,
    aggregate,
    counts,
    entries,
    branch: incomplete ? markIncomplete(branch) : branch,
  };
  if (diagnostics) session.diagnostics = diagnostics;
  return { value: session, incomplete };
}

export function parseActivityTree(data: unknown): ActivityTree | null {
  if (!isPlainObject(data)) return null;
  const revision = readNonNegativeInteger(data, "revision");
  if (revision === null) return null;
  const root = parseSession(data.root, 1);
  if (!root.value) return null;
  return { revision, root: root.value };
}

/** What a delegate row's branch says, once both of its branches are read. */
export interface ActivityDelegateBranch extends ActivityBranchState {
  // openSessionRef is set when the branch stopped with nothing to page: the
  // depth or continuation-path bound was reached, so the daemon truncated it
  // and deliberately minted no token, because a token there would name this
  // child as a fresh root at position 0 -- the page a direct request already
  // returns. The child is still addressable as its own session, and this is
  // the ref that reaches it.
  openSessionRef?: string;
}

// activityDelegateBranch reads the two branch states a delegate row has. Its
// own is truncated by the depth bound, which stops before the child is
// loaded at all; the child's is where a continuation-path truncation and any
// size trim inside that child land. A reader that looks at one of them
// answers correctly for half the cases and silently wrongly for the rest,
// which is why this is one function rather than a rule each surface
// remembers: the child's token wins where it has one, and either branch
// being truncated truncates the row.
export function activityDelegateBranch(delegate: ActivityDelegate): ActivityDelegateBranch {
  const own = delegate.branch;
  const child = delegate.child?.branch;
  const continuation = child?.continuation ?? own.continuation;
  const truncated = Boolean(child?.truncated || own.truncated);
  const error = child?.error ?? own.error;
  const childRef = delegate.childRef.trim();
  const merged: ActivityDelegateBranch = {};
  if (continuation !== undefined) merged.continuation = continuation;
  if (truncated) merged.truncated = true;
  if (error !== undefined) merged.error = error;
  if (!continuation && truncated && childRef) merged.openSessionRef = childRef;
  return merged;
}

// activityDelegateDiagnostics reads the two places a delegate row's
// diagnostics land, for the same reason activityDelegateBranch reads two
// branches: the depth bound is stamped on the delegate before its child is
// loaded, while the continuation-path bound and the journal conditions are
// stamped on the child session itself, and that session has no row of its
// own to say so. The delegate's own sentences come first, then the child's,
// each said once.
export function activityDelegateDiagnostics(delegate: ActivityDelegate): string[] {
  return [...new Set([...(delegate.diagnostics ?? []), ...(delegate.child?.diagnostics ?? [])])];
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

export function delegateHasActiveWork(delegate: ActivityDelegate): boolean {
  const childActive = delegate.child ? sessionHasActiveWork(delegate.child) : false;
  if (!isTurnContainer(delegate)) return delegate.terminal !== true || childActive;
  return (delegate.turns ?? []).some((turn) => !turn.terminal) || childActive;
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
