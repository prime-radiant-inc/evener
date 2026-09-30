import type { AppwireClientLike } from "./clientLike";
import { mutationErrorData, WireError } from "./errors";
import { isThreadNotFound } from "./sessionErrors";
import { acquireThreadSubscription, type ThreadSubscriptionLease } from "./threadSubscription";
import type {
  AnyNotification,
  JobActivityJob,
  SessionActivityContext,
  SessionActivityIssue,
  SessionActivityResource,
  SessionActivityScope,
  SessionActivitySummary,
  SessionDelegate,
  SessionWatch,
} from "./types.gen";

export type SessionActivityCollection = "delegates" | "jobs" | "watches";
export type SessionActivityClient = Pick<
  AppwireClientLike,
  "request" | "onNotification" | "onReady" | "onStateChange" | "state"
>;
export interface SessionActivityClock {
  setTimeout(callback: () => void, delayMs: number): unknown;
  clearTimeout(handle: unknown): void;
}
export interface SessionActivityReadState {
  loading: boolean;
  pending: boolean;
  error: unknown | null;
  unavailable: boolean;
  permanent: boolean;
}
export interface SessionActivityCollectionState<Row> extends SessionActivityReadState {
  rows: readonly Row[];
  context: SessionActivityContext | null;
  complete: boolean;
  hasMore: boolean;
  issues: readonly SessionActivityIssue[];
}
export interface SessionActivitySnapshot {
  ref: string;
  scope: SessionActivityScope;
  context: SessionActivityContext | null;
  summary: SessionActivitySummary | null;
  summaryState: SessionActivityReadState;
  delegates: SessionActivityCollectionState<SessionDelegate>;
  jobs: SessionActivityCollectionState<JobActivityJob>;
  watches: SessionActivityCollectionState<SessionWatch>;
}

type ActivityRow = SessionDelegate | JobActivityJob | SessionWatch;
interface ActivityPage {
  context: SessionActivityContext;
  scope: SessionActivityScope;
  rows: readonly ActivityRow[];
  page: { nextCursor?: string; complete: boolean; issues: SessionActivityIssue[] };
}
interface RefreshWalk {
  advance: boolean;
  rows: Map<string, ActivityRow>;
  issues: readonly SessionActivityIssue[];
}
interface ResourceRead {
  observers: number;
  oneShot: boolean;
  inFlight: Promise<void> | null;
  rootQueued: boolean;
  pageQueued: boolean;
  failures: number;
  timer: unknown | null;
  cursor: string | undefined;
  epoch: string | undefined;
  sessionId: string | undefined;
  incomplete: boolean;
  boundary: string | undefined;
  refresh: RefreshWalk | null;
  pace: { handle: unknown; resume(): void } | null;
}
const resources: readonly SessionActivityResource[] = ["summary", "delegates", "jobs", "watches"];
const defaultClock: SessionActivityClock = {
  setTimeout: (callback, delayMs) => setTimeout(callback, delayMs),
  clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};
const readState = (): SessionActivityReadState => ({
  loading: false,
  pending: false,
  error: null,
  unavailable: false,
  permanent: false,
});
const collectionState = <Row>(): SessionActivityCollectionState<Row> => ({
  ...readState(),
  rows: [],
  context: null,
  complete: false,
  hasMore: false,
  issues: [],
});
const resourceRead = (): ResourceRead => ({
  observers: 0,
  oneShot: false,
  inFlight: null,
  rootQueued: false,
  pageQueued: false,
  failures: 0,
  timer: null,
  cursor: undefined,
  epoch: undefined,
  sessionId: undefined,
  incomplete: false,
  boundary: undefined,
  refresh: null,
  pace: null,
});

/** Owns one session/scope/connection lifetime. Results from a disposed owner
 * never enter its replacement, and collection membership is independent of
 * summary counts and transcript hydration. */
export class SessionActivityStore {
  private state: SessionActivitySnapshot;
  private readonly clock: SessionActivityClock;
  private readonly scope: SessionActivityScope;
  private readonly reads: Record<SessionActivityResource, ResourceRead> = {
    summary: resourceRead(),
    delegates: resourceRead(),
    jobs: resourceRead(),
    watches: resourceRead(),
  };
  private listeners = new Set<() => void>();
  private stopListening: (() => void)[] = [];
  private lease: ThreadSubscriptionLease | null = null;
  private disposed = false;
  private generation = 0;

  constructor(
    private readonly client: SessionActivityClient,
    private readonly ref: string,
    options: { scope?: SessionActivityScope; clock?: SessionActivityClock } = {},
  ) {
    if (!ref.trim()) throw new TypeError("Session activity requires a session ref");
    this.scope = options.scope ?? "session";
    if (this.scope !== "session" && this.scope !== "subtree") throw new TypeError("Unknown session activity scope");
    this.clock = options.clock ?? defaultClock;
    this.state = {
      ref,
      scope: this.scope,
      context: null,
      summary: null,
      summaryState: readState(),
      delegates: collectionState(),
      jobs: collectionState(),
      watches: collectionState(),
    };
  }
  getSnapshot = (): SessionActivitySnapshot => this.state;
  subscribe = (listener: () => void): (() => void) => {
    if (this.disposed) return () => {};
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  start(): void {
    if (this.disposed || this.reads.summary.observers > 0) return;
    this.reads.summary.observers = 1;
    void this.request("summary", "root");
  }
  observe(resource: SessionActivityCollection): () => void {
    if (this.disposed) return () => {};
    const read = this.reads[resource];
    read.observers += 1;
    if (read.observers === 1) void this.request(resource, "root");
    let released = false;
    return () => {
      if (released) return;
      released = true;
      if (this.disposed) return;
      read.observers -= 1;
      if (read.observers === 0) {
        this.cancelTimer(read);
        if (!read.oneShot) {
          this.cancelPace(read);
          read.rootQueued = false;
          read.pageQueued = false;
          if (!read.inFlight) this.change(resource, { pending: false });
        }
      }
      this.releaseIdle();
    };
  }
  load = (resource: SessionActivityCollection): Promise<void> => this.request(resource, "root", true);
  loadMore = (resource: SessionActivityCollection): Promise<void> => {
    if (!this.reads[resource].cursor) return Promise.resolve();
    return this.request(resource, "page", true);
  };
  refresh = (resource?: SessionActivityResource): Promise<void> => {
    if (resource) return this.request(resource, "root", true);
    return Promise.all(
      resources
        .filter((name) => name === "summary" || this.reads[name].observers > 0)
        .map((name) => this.request(name, "root", true)),
    ).then(() => {});
  };

  private listen(): void {
    if (this.disposed || this.stopListening.length > 0) return;
    this.stopListening = [
      this.client.onNotification((notification) => this.invalidate(notification)),
      this.client.onStateChange((state) => {
        if (state === "ready") return;
        this.generation += 1;
        for (const resource of resources) {
          const read = this.reads[resource];
          this.cancelTimer(read);
          this.cancelPace(read);
          read.refresh = null;
        }
      }),
      this.client.onReady(() => {
        for (const resource of resources) {
          const read = this.reads[resource];
          if (read.observers > 0 || read.rootQueued || read.pageQueued) {
            if (!this.readState(resource).permanent) void this.request(resource, "root");
          }
        }
      }),
    ];
  }
  private readState(resource: SessionActivityResource): SessionActivityReadState {
    return resource === "summary" ? this.state.summaryState : this.state[resource];
  }
  private change(resource: SessionActivityResource, update: Partial<SessionActivityReadState>): void {
    if (resource === "summary") this.publish({ summaryState: { ...this.state.summaryState, ...update } });
    else this.publish({ [resource]: { ...this.state[resource], ...update } });
  }
  private publish(update: Partial<SessionActivitySnapshot>): void {
    if (this.disposed) return;
    this.state = { ...this.state, ...update };
    for (const listener of this.listeners) listener();
  }
  private acceptContext(context: SessionActivityContext, source: SessionActivityResource): number {
    if (!this.state.context || this.state.context.sessionId === context.sessionId) return this.generation;
    // A routing alias can resolve to a replacement session. Its evidence and
    // cursors retire together; opaque cache epochs alone do not imply this.
    this.generation += 1;
    const generation = this.generation;
    for (const resource of resources) {
      const read = this.reads[resource];
      this.cancelTimer(read);
      const demanded = read.observers > 0 || read.oneShot || read.rootQueued || read.pageQueued;
      read.rootQueued = resource === source ? read.rootQueued : demanded;
      read.pageQueued = false;
      read.cursor = undefined;
      read.epoch = undefined;
      read.sessionId = undefined;
      read.incomplete = false;
      read.boundary = undefined;
      read.refresh = null;
      this.cancelPace(read);
      read.failures = 0;
    }
    const freshCollection = <Row>(resource: SessionActivityCollection): SessionActivityCollectionState<Row> => ({
      ...collectionState<Row>(),
      loading: this.reads[resource].inFlight !== null,
      pending: this.reads[resource].rootQueued,
    });
    this.publish({
      context,
      summary: null,
      summaryState: {
        ...readState(),
        loading: this.reads.summary.inFlight !== null,
        pending: this.reads.summary.rootQueued,
      },
      delegates: freshCollection<SessionDelegate>("delegates"),
      jobs: freshCollection<JobActivityJob>("jobs"),
      watches: freshCollection<SessionWatch>("watches"),
    });
    for (const resource of resources) {
      if (resource !== source && this.reads[resource].rootQueued) void this.request(resource, "root");
    }
    return generation;
  }
  private request(resource: SessionActivityResource, mode: "root" | "page", explicit = false): Promise<void> {
    if (this.disposed || (!explicit && this.readState(resource).permanent)) return Promise.resolve();
    this.listen();
    const read = this.reads[resource];
    this.cancelTimer(read);
    if (explicit) read.oneShot = true;
    if (mode === "root") read.rootQueued = true;
    else read.pageQueued = true;
    if (read.inFlight) return read.inFlight;
    if (mode === "root") read.refresh = null;
    if (this.client.state !== "ready") {
      this.change(resource, { pending: true });
      return Promise.resolve();
    }
    if (explicit) read.failures = 0;
    // Install inFlight before publishing or requesting: subscribers can
    // synchronously ask for more data without starting a second read.
    const running = Promise.resolve()
      .then(() => this.drain(resource))
      .finally(() => {
        if (read.inFlight !== running) return;
        read.inFlight = null;
        // A result publish can wake a consumer after drain has finished but
        // before this promise settles. Carry its queued demand into a new
        // read rather than resolving a promise that dropped that request.
        if (!this.disposed && this.client.state === "ready" && (read.rootQueued || read.pageQueued)) {
          return this.request(resource, read.rootQueued ? "root" : "page", true);
        }
        read.oneShot = false;
        this.change(resource, { loading: false });
        this.releaseIdle();
      });
    read.inFlight = running;
    this.change(resource, { loading: true, error: null, unavailable: false, permanent: false });
    return running;
  }
  private async drain(resource: SessionActivityResource): Promise<void> {
    const read = this.reads[resource];
    while (
      !this.disposed &&
      this.client.state === "ready" &&
      (read.rootQueued || read.pageQueued || (read.refresh?.advance && (read.observers > 0 || read.oneShot)))
    ) {
      // Finish the admitted walk before servicing coalesced invalidations;
      // restarting at every changed row can starve later-page current work.
      const advancingRefresh = read.refresh?.advance;
      const root = read.rootQueued && !advancingRefresh;
      if (root) read.rootQueued = false;
      else if (advancingRefresh && read.refresh) read.refresh.advance = false;
      else read.pageQueued = false;
      if (root && resource !== "summary") {
        const rows = this.state[resource].rows;
        const last = rows[rows.length - 1];
        // A stale cursor/reconnect restarts the fresh walk, not its original
        // displayed boundary: provisional rows must not extend that boundary.
        read.boundary ??= last ? rowIdentity(resource, last) : undefined;
        read.refresh = read.boundary ? { advance: false, rows: new Map(), issues: [] } : null;
      }
      const cursor = root ? undefined : read.cursor;
      let generation = this.generation;
      try {
        this.lease ??= acquireThreadSubscription(this.client, this.ref);
        await this.lease.ensure();
        if (this.disposed || generation !== this.generation || this.client.state !== "ready") continue;
        const result = await this.fetch(resource, cursor);
        if (this.disposed || generation !== this.generation) continue;
        if (result.scope !== this.scope) throw new Error("Session activity response belongs to another scope");
        generation = this.acceptContext(result.context, resource);
        if (this.disposed || generation !== this.generation) continue;
        if (resource === "summary") {
          const summary = result as SessionActivitySummary;
          read.failures = 0;
          this.publish({
            context: summary.context,
            summary,
            summaryState: {
              ...this.state.summaryState,
              pending: !summary.context.ancestryKnown,
              error: null,
              unavailable: false,
              permanent: false,
            },
          });
          if (!summary.context.ancestryKnown) this.schedule(resource, 100);
        } else {
          const page = result as ActivityPage;
          if (!root && (page.context.epoch !== read.epoch || page.context.sessionId !== read.sessionId)) {
            read.cursor = undefined;
            read.refresh = null;
            read.rootQueued = true;
            continue;
          }
          const current = this.state[resource];
          const walk = read.refresh;
          let reachedBoundary = false;
          if (walk) {
            reachedBoundary = page.page.complete;
            for (const row of page.rows) {
              const identity = rowIdentity(resource, row);
              walk.rows.set(identity, row);
              if (identity === read.boundary) reachedBoundary = true;
            }
            walk.issues = mergeIssues(walk.issues, page.page.issues);
          }
          const freshIssues = walk?.issues ?? page.page.issues;
          // Only a fresh walk through the displayed boundary (or the whole
          // collection) proves membership absent. Partial source issues never
          // prove removal, and an opaque epoch change is not session replacement.
          let rows: readonly ActivityRow[];
          let reconciled = false;
          if (walk && reachedBoundary && freshIssues.length === 0) {
            rows = [...walk.rows.values()];
            reconciled = true;
          } else if (!walk && root && freshIssues.length === 0) {
            rows = page.rows;
            reconciled = true;
          } else {
            rows = mergeRows(resource, current.rows, page.rows);
          }
          // A clean continuation cannot acknowledge an unresolved root scan.
          // Retain its issues and root recovery demand until fresh reconciliation.
          const issues = reconciled ? [] : mergeIssues(current.issues, freshIssues);
          if (reachedBoundary) {
            read.refresh = null;
            if (reconciled) read.boundary = undefined;
          }
          read.cursor = page.page.nextCursor;
          read.epoch = page.context.epoch;
          read.sessionId = page.context.sessionId;
          read.incomplete = !page.page.complete || issues.length > 0 || read.refresh !== null;
          this.publishCollection(resource, {
            ...current,
            rows,
            context: page.context,
            complete: page.page.complete && issues.length === 0 && read.refresh === null,
            hasMore: !!read.cursor,
            issues,
            pending: !page.context.ancestryKnown || read.incomplete,
            error: null,
            unavailable: false,
            permanent: false,
          });
          if (this.disposed || generation !== this.generation) continue;
          this.publish({ context: page.context });
          // Collection reads can warm retained count indexes without emitting
          // a notification. Refresh an observed unknown count after useful
          // progress, paced and coalesced across pages, without scanning merely
          // because a summary count is unknown.
          const progressed =
            page.rows.length > 0 ||
            (read.cursor !== undefined && read.cursor !== cursor) ||
            (page.page.complete && !current.complete);
          if (progressed && this.state.summary && !this.state.summary[resource].known) this.schedule("summary", 100);
          if (read.refresh && read.cursor && read.cursor !== cursor && (read.observers > 0 || read.oneShot)) {
            read.refresh.advance = true;
            await this.pacePage(read);
          } else if (
            page.rows.length === 0 &&
            read.cursor &&
            read.cursor !== cursor &&
            !page.page.complete &&
            (read.observers > 0 || read.oneShot)
          ) {
            read.pageQueued = true;
          } else if (
            issues.length > 0 ||
            (!page.page.complete && !read.cursor) ||
            (page.rows.length === 0 && read.cursor === cursor && !page.page.complete)
          ) {
            read.failures += 1;
            this.retry(resource);
          } else {
            read.failures = 0;
            if (!page.context.ancestryKnown) this.schedule(resource, 100);
          }
        }
      } catch (error) {
        if (this.disposed || generation !== this.generation) continue;
        if (error instanceof WireError && error.evenerErrorInfo === "sessionActivityCursorStale" && cursor) {
          read.cursor = undefined;
          read.refresh = null;
          read.rootQueued = true;
          continue;
        }
        read.rootQueued = false;
        read.pageQueued = false;
        read.failures += 1;
        const permanent = permanentError(error);
        this.change(resource, { error, permanent, unavailable: unavailableError(error), pending: false });
        if (!permanent) this.retry(resource, cursor ? "page" : "root");
      }
    }
  }
  private publishCollection(
    resource: SessionActivityCollection,
    state: SessionActivityCollectionState<ActivityRow>,
  ): void {
    this.publish({ [resource]: state });
  }
  private async fetch(
    resource: SessionActivityResource,
    cursor?: string,
  ): Promise<SessionActivitySummary | ActivityPage> {
    const params = { ref: this.ref, scope: this.scope, ...(cursor ? { cursor } : {}) };
    switch (resource) {
      case "summary":
        return this.client.request("evener/thread/activity/read", { ref: this.ref, scope: this.scope });
      case "delegates": {
        const response = await this.client.request("evener/thread/delegates/list", params);
        return { ...response, rows: response.delegates };
      }
      case "jobs": {
        const response = await this.client.request("evener/thread/jobs/list", params);
        return { ...response, rows: response.jobs };
      }
      case "watches": {
        const response = await this.client.request("evener/thread/watches/list", params);
        return { ...response, rows: response.watches };
      }
    }
  }
  private retry(resource: SessionActivityResource, mode: "root" | "page" = "root"): void {
    const failures = this.reads[resource].failures;
    this.schedule(resource, Math.min(30000, 1000 * 2 ** Math.min(5, Math.max(0, failures - 1))), mode);
  }
  private schedule(resource: SessionActivityResource, delayMs: number, mode: "root" | "page" = "root"): void {
    const read = this.reads[resource];
    if (this.disposed || read.observers === 0 || this.client.state !== "ready" || this.readState(resource).permanent)
      return;
    this.cancelTimer(read);
    read.timer = this.clock.setTimeout(() => {
      read.timer = null;
      void this.request(resource, mode);
    }, delayMs);
  }
  private pacePage(read: ResourceRead): Promise<void> {
    return new Promise((resolve) => {
      const resume = () => {
        read.pace = null;
        resolve();
      };
      read.pace = { handle: this.clock.setTimeout(resume, 100), resume };
    });
  }
  private cancelPace(read: ResourceRead): void {
    if (!read.pace) return;
    this.clock.clearTimeout(read.pace.handle);
    read.pace.resume();
  }
  private cancelTimer(read: ResourceRead): void {
    if (read.timer !== null) this.clock.clearTimeout(read.timer);
    read.timer = null;
  }
  private invalidate(notification: AnyNotification): void {
    if (this.disposed) return;
    if (
      !("ref" in notification.params) ||
      (notification.params.ref !== this.ref && notification.params.ref !== this.state.context?.ref)
    )
      return;
    let changed: readonly SessionActivityResource[];
    switch (notification.method) {
      case "evener/thread/activity/changed":
        if (
          this.scope === "session" &&
          this.state.context &&
          notification.params.sessionId !== this.state.context.sessionId
        )
          return;
        changed = notification.params.resources;
        break;
      case "evener/delegate/updated":
        changed = ["summary", "delegates"];
        break;
      case "evener/job/started":
      case "evener/job/finished":
        changed = ["summary", "jobs"];
        break;
      case "evener/jobs/treeUpdated":
        changed = resources;
        break;
      case "evener/thread/resync":
        for (const resource of resources) {
          this.reads[resource].refresh = null;
          this.cancelPace(this.reads[resource]);
        }
        // The server may already have emitted a reply before the alias was
        // cleared. It must not publish after this resync boundary.
        this.generation += 1;
        changed = resources;
        break;
      default:
        return;
    }
    for (const resource of changed) {
      if (this.reads[resource].observers > 0 || this.reads[resource].oneShot) {
        void this.request(resource, "root", notification.method === "evener/thread/resync");
      }
    }
  }
  private releaseIdle(): void {
    if (
      resources.some(
        (resource) =>
          this.reads[resource].observers > 0 ||
          this.reads[resource].inFlight ||
          this.reads[resource].rootQueued ||
          this.reads[resource].pageQueued,
      )
    )
      return;
    this.lease?.release();
    this.lease = null;
    for (const stop of this.stopListening) stop();
    this.stopListening = [];
  }
  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.generation += 1;
    for (const resource of resources) {
      const read = this.reads[resource];
      this.cancelTimer(read);
      this.cancelPace(read);
      read.boundary = undefined;
      read.refresh = null;
      read.observers = 0;
      read.rootQueued = false;
      read.pageQueued = false;
    }
    this.lease?.release();
    this.lease = null;
    for (const stop of this.stopListening) stop();
    this.stopListening = [];
    this.listeners.clear();
  }
}

function rowIdentity(resource: SessionActivityCollection, row: ActivityRow): string {
  if (resource === "delegates") return (row as SessionDelegate).delegateId;
  if (resource === "jobs") {
    const job = row as JobActivityJob;
    return JSON.stringify([job.ownerRef, job.jobId]);
  }
  const watch = row as SessionWatch;
  return JSON.stringify([watch.receiverRef, watch.watch.id]);
}
function mergeRows(
  resource: SessionActivityCollection,
  existing: readonly ActivityRow[],
  incoming: readonly ActivityRow[],
): ActivityRow[] {
  const rows = new Map(existing.map((row) => [rowIdentity(resource, row), row]));
  for (const row of incoming) rows.set(rowIdentity(resource, row), row);
  return [...rows.values()];
}
function mergeIssues(
  existing: readonly SessionActivityIssue[],
  incoming: readonly SessionActivityIssue[],
): SessionActivityIssue[] {
  const issues = new Map(existing.map((issue) => [JSON.stringify([issue.ref, issue.code]), issue]));
  for (const issue of incoming) issues.set(JSON.stringify([issue.ref, issue.code]), issue);
  return [...issues.values()];
}
function unavailableError(error: unknown): boolean {
  return (
    isThreadNotFound(error) ||
    (error instanceof WireError &&
      ["methodNotFound", "resourceNotFound", "upgradeRequired"].includes(error.evenerErrorInfo ?? "")) ||
    mutationErrorData(error)?.mutationOutcome === "targetDeleted" ||
    (error instanceof WireError &&
      error.evenerErrorInfo === "actionUnavailable" &&
      mutationErrorData(error)?.retryDisposition !== "automatic")
  );
}
function permanentError(error: unknown): boolean {
  return (
    unavailableError(error) ||
    (error instanceof WireError &&
      (error.code === -32600 || error.code === -32601 || error.code === -32602) &&
      error.evenerErrorInfo !== "sessionActivityCursorStale")
  );
}
