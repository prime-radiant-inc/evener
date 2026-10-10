import type { AppwireClientLike } from "./clientLike";
import { mutationErrorData, WireError } from "./errors";
import { sameJsonValue } from "./plainObject";
import { isThreadNotFound } from "./sessionErrors";
import {
  acquireThreadSubscription,
  type ThreadSubscriptionLease,
  type ThreadSubscriptionMetadata,
} from "./threadSubscription";
import type {
  AnyNotification,
  EvenerDelegateInfo,
  JobActivityJob,
  SessionActivityContext,
  SessionActivityIssue,
  SessionActivityResource,
  SessionActivityScope,
  SessionActivitySummary,
  SessionDelegate,
  SessionWatch,
  ThreadStatus,
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
  runtime: ThreadSubscriptionMetadata | null;
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
  rows: ActivityRow[];
  /** Identity -> the position of its row in `rows`. A row a *later* page
   * re-serves replaces that position, preserving first-seen order; the set is
   * never consulted for a duplicate within one served page, so two rows an
   * authoritative read returned under the same identity both survive. */
  index: Map<string, number>;
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
  incomplete: boolean;
  boundary: string | undefined;
  refresh: RefreshWalk | null;
  pace: { handle: unknown; resume(): void } | null;
  /** Bumped when the last observer leaves; a read begun before it is dropped. */
  releases: number;
}
/** Cap on the buffered latest frame per unknown delegate id; the per-ID
 * seen-unknown set bounds reads, so this only guards an unbounded store. */
const MAX_BUFFERED_DELEGATE_FRAMES = 128;
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
  incomplete: false,
  boundary: undefined,
  refresh: null,
  pace: null,
  releases: 0,
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
  private runtimeThreadId: string | null = null;
  private statusRevision = 0;
  private statusUpdate: { revision: number; threadId: string; status: ThreadStatus } | null = null;
  /** The applied merge order per loaded delegate row: a frame is admitted over
   * it only by a strictly greater revision (the row itself carries the
   * independent latestActivityAt maximum). Keyed by the row's identity, because
   * a read can hold two rows under one delegate id. Cleared on a source epoch
   * change, a session replacement and dispose. */
  private readonly appliedDelegates = new Map<string, number>();
  /** Delegate ids that triggered a root read for membership; at most once per
   * id. Cleared with the applied map. */
  private readonly seenUnknownDelegates = new Set<string>();
  /** Latest pushed frame per unknown delegate id, applied once a read admits
   * the row so a settle during a later-page read is not dropped. */
  private readonly bufferedUnknownDelegates = new Map<string, EvenerDelegateInfo>();
  /** The last accepted context's no-read-merge eligibility. */
  private pushEligible = false;
  /** The replacement epoch whose delegate state has already been retired: a
   * sibling resource can report the same new epoch on every response while the
   * delegates read is still in flight, and retiring once per epoch keeps that
   * from clearing a buffered frame that arrived after the first report. */
  private retirementEpoch: string | undefined;

  constructor(
    private readonly client: SessionActivityClient,
    private readonly ref: string,
    options: { scope?: SessionActivityScope; clock?: SessionActivityClock; retained?: SessionActivitySnapshot } = {},
  ) {
    if (!ref.trim()) throw new TypeError("Session activity requires a session ref");
    this.scope = options.scope ?? "session";
    if (this.scope !== "session" && this.scope !== "subtree") throw new TypeError("Unknown session activity scope");
    this.clock = options.clock ?? defaultClock;
    this.state = {
      ref,
      scope: this.scope,
      context: null,
      runtime: null,
      summary: null,
      summaryState: readState(),
      delegates: collectionState(),
      jobs: collectionState(),
      watches: collectionState(),
    };
    const retained = options.retained;
    if (retained?.ref === ref && retained.scope === this.scope) {
      // Membership supplies the displayed boundary for a fresh cursor walk.
      // Runtime, cursors and read outcomes belong to the new connection.
      const retain = <Row>(collection: SessionActivityCollectionState<Row>): SessionActivityCollectionState<Row> => ({
        ...collection,
        ...readState(),
        pending: true,
        complete: false,
        hasMore: false,
      });
      this.state = {
        ...this.state,
        context: retained.context,
        summary: retained.summary,
        summaryState: { ...readState(), pending: true },
        delegates: retain(retained.delegates),
        jobs: retain(retained.jobs),
        watches: retain(retained.watches),
      };
    }
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
        // Whoever asked for the collection's reads has gone, explicit loads
        // included: the read in flight drops its reply, and nothing queued
        // behind it or walking on after it runs, so a store another holder
        // keeps alive takes in no closed view's late page.
        read.releases += 1;
        this.stopReadDemand(read, false);
        read.oneShot = false;
        if (read.refresh) read.refresh.advance = false;
        this.change(resource, { pending: false });
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
        this.runtimeThreadId = null;
        this.statusUpdate = null;
        // The connection may come back to a producer without the push
        // capability, and an invalidation that arrives before the first
        // refreshed response would trust the old one: the merge is re-enabled
        // only by a fresh live capability context.
        this.pushEligible = false;
        this.clearDelegates();
        this.retirementEpoch = undefined;
        this.publish({ runtime: null });
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
    this.clearDelegates();
    // The replacement context has not been seen by noteContext yet; a fresh
    // session resets eligibility rather than reporting a live->retained flip.
    this.pushEligible = false;
    this.retirementEpoch = undefined;
    const metadata = this.lease?.metadata();
    if (metadata?.sessionId !== context.sessionId || this.statusUpdate?.threadId !== metadata?.threadId)
      this.statusUpdate = null;
    for (const resource of resources) {
      const read = this.reads[resource];
      const demanded = read.observers > 0 || read.oneShot || read.rootQueued || read.pageQueued;
      this.stopReadDemand(read, resource === source ? read.rootQueued : demanded);
      read.cursor = undefined;
      read.epoch = undefined;
      read.incomplete = false;
      read.boundary = undefined;
      read.refresh = null;
    }
    const freshCollection = <Row>(resource: SessionActivityCollection): SessionActivityCollectionState<Row> => ({
      ...collectionState<Row>(),
      loading: this.reads[resource].inFlight !== null,
      pending: this.reads[resource].rootQueued,
    });
    this.publish({
      context,
      runtime: null,
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
        read.refresh = read.boundary ? { advance: false, rows: [], index: new Map(), issues: [] } : null;
      }
      const cursor = root ? undefined : read.cursor;
      let generation = this.generation;
      const releases = read.releases;
      // A read is stale once the store is disposed, its context is replaced,
      // or its collection's last observer leaves (even from a listener during
      // one of this read's own publishes): from then on nothing it does lands.
      const stale = () => this.disposed || generation !== this.generation || releases !== read.releases;
      // A read dropped once the hub served it can still have warmed the hub's
      // count, so a dropped read refreshes that count before skipping its page.
      const dropped = () => {
        if (!stale()) return false;
        this.refreshWarmedCount(resource, generation);
        return true;
      };
      const statusRevision = this.statusRevision;
      try {
        this.lease ??= acquireThreadSubscription(this.client, this.ref);
        await this.lease.ensure();
        if (stale() || this.client.state !== "ready") continue;
        const result = await this.fetch(resource, cursor);
        if (dropped()) continue;
        if (result.scope !== this.scope) throw new Error("Session activity response belongs to another scope");
        generation = this.acceptContext(result.context, resource);
        if (stale()) continue;
        // A source replacement is visible only in a response: the invalidation
        // carries no epoch.
        const epoch = result.context.epoch;
        const previous = read.epoch;
        const crossing = previous !== undefined && previous !== epoch;
        // The delegate merge ordering is epoch-scoped, so the delegates
        // collection's own replacement retires it here.
        if (crossing && resource === "delegates") {
          this.clearDelegates();
          this.retirementEpoch = undefined;
        }
        read.epoch = epoch;
        // A page cannot cross epochs: its rows extend a retired walk. A root read
        // is the fresh read for the new epoch.
        if (crossing && !root) {
          read.cursor = undefined;
          read.refresh = null;
          read.rootQueued = true;
          continue;
        }
        if (resource === "summary") {
          const summary = result as SessionActivitySummary;
          const unavailable = (summary.issues?.length ?? 0) > 0;
          if (!unavailable) read.failures = 0;
          this.publish({
            context: summary.context,
            runtime: this.runtimeFor(summary.context, statusRevision),
            summary,
            summaryState: {
              ...this.state.summaryState,
              pending: unavailable || !summary.context.ancestryKnown || summary.refreshPending === true,
              error: null,
              unavailable: false,
              permanent: false,
            },
          });
          if (stale()) continue;
          this.noteContext(summary.context);
          if (unavailable) {
            read.failures += 1;
            this.retry(resource);
          } else if (!summary.context.ancestryKnown || summary.refreshPending) this.schedule(resource, 100);
        } else {
          const page = result as ActivityPage;
          const current = this.state[resource];
          const walk = read.refresh;
          const last = page.rows[page.rows.length - 1];
          // Admitted paging extends displayed coverage even while a partial
          // root needs recovery. Automatic fresh-walk rows remain provisional.
          if (!root && !walk && read.boundary && last) read.boundary = rowIdentity(resource, last);
          let reachedBoundary = false;
          if (walk) {
            reachedBoundary = page.page.complete;
            // A served page is authoritative: keep every row it returned,
            // including two rows that share a delegate id. Only a row a later
            // page re-serves replaces its earlier occurrence, in first-seen
            // order; `pageSeen` keeps an intra-page duplicate from aliasing.
            const pageSeen = new Set<string>();
            for (const row of page.rows) {
              const identity = rowIdentity(resource, row);
              const at = pageSeen.has(identity) ? undefined : walk.index.get(identity);
              if (at !== undefined) {
                walk.rows[at] = row;
              } else {
                if (!walk.index.has(identity)) walk.index.set(identity, walk.rows.length);
                walk.rows.push(row);
              }
              pageSeen.add(identity);
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
            rows = [...walk.rows];
            reconciled = true;
          } else if (!walk && root && freshIssues.length === 0) {
            rows = page.rows;
            reconciled = true;
          } else {
            rows = mergeRows(resource, current.rows, page.rows);
          }
          if (resource === "delegates") {
            // A read joins each row the same way a frame does, so it cannot
            // clobber a newer frame nor skip one applied in the gap, and it
            // admits any buffered frame for a row it serves.
            rows = this.joinDelegateRows(
              rows as readonly SessionDelegate[],
              current.rows as readonly SessionDelegate[],
            );
          }
          // A clean continuation cannot acknowledge an unresolved root scan.
          // Retain its issues and root recovery demand until fresh reconciliation.
          const issues = reconciled ? [] : mergeIssues(current.issues, freshIssues);
          if (reachedBoundary) {
            read.refresh = null;
            if (reconciled) read.boundary = undefined;
          }
          read.cursor = page.page.nextCursor;
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
          if (dropped()) continue;
          this.publish({ context: page.context, runtime: this.runtimeFor(page.context, statusRevision) });
          if (dropped()) continue;
          this.noteContext(page.context);
          // Collection reads can warm retained count indexes without emitting
          // a notification. Refresh an observed unknown count after useful
          // progress, paced and coalesced across pages, without scanning merely
          // because a summary count is unknown.
          const progressed =
            page.rows.length > 0 ||
            (read.cursor !== undefined && read.cursor !== cursor) ||
            (page.page.complete && !current.complete);
          if (progressed) this.refreshWarmedCount(resource, generation);
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
        if (stale()) continue;
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
        const update = { error, permanent, unavailable: unavailableError(error), pending: false };
        if (resource === "summary") {
          this.publish({ summary: null, summaryState: { ...this.state.summaryState, ...update } });
        } else {
          this.change(resource, update);
        }
        if (stale()) continue;
        if (!permanent) this.retry(resource, cursor ? "page" : "root");
      }
    }
  }
  /** A collection read the hub served can have warmed the hub's count for
   * that collection, whether its page landed or was dropped because the view
   * that asked for it left. Refresh a summary count still unknown, so the
   * count does not stay hidden until the next notification. A read dropped
   * for a replaced context or a disposed store refreshes nothing: its count
   * belongs to no current session. */
  private refreshWarmedCount(resource: SessionActivityResource, generation: number): void {
    if (resource === "summary" || this.disposed || generation !== this.generation) return;
    if (this.state.summary && !this.state.summary[resource].known) this.schedule("summary", 100);
  }
  private runtimeFor(context: SessionActivityContext, statusRevision: number): ThreadSubscriptionMetadata | null {
    const metadata = this.lease?.metadata();
    if (
      !metadata ||
      metadata.sessionId !== context.sessionId ||
      (this.runtimeThreadId !== null && metadata.threadId !== this.runtimeThreadId)
    )
      return null;
    const update = this.statusUpdate;
    // The shared metadata can also come from another owner's delayed rich
    // read. A qualified live notification remains authoritative until its
    // connection or session identity retires, including on later summary polls.
    if (update && update.revision >= statusRevision && update.threadId === metadata.threadId)
      return { ...metadata, status: update.status };
    return metadata;
  }
  private publishCollection(
    resource: SessionActivityCollection,
    state: SessionActivityCollectionState<ActivityRow>,
  ): void {
    this.publish({ [resource]: state });
  }
  /** The no-read merge is eligible only when the producing source advertises
   * the bounded preview and is live -- the combination in which delegate
   * frames reliably follow an invalidation. A live context that turns retained
   * (an ancestor released its runtime) can receive post-release invalidations
   * with no frame, so the flip reconciles the observed delegates with a read. */
  private noteContext(context: SessionActivityContext): void {
    const next = context.reportPreview === true && context.availability === "live";
    const wasEligible = this.pushEligible;
    if (wasEligible && !next && (this.reads.delegates.observers > 0 || this.reads.delegates.oneShot)) {
      this.reconcileDelegates();
    }
    this.pushEligible = next;
    // A source replacement is visible only in a response, and the delegate
    // merge ordering is epoch-scoped. When another collection is the first to
    // see the replacement the loaded delegates rows are a retired generation's,
    // so retire the ordering and re-read them: an eligible context suppresses
    // the invalidation-driven delegate read, so nothing else would.
    // Only the no-read merge depends on the loaded ordering: with the gate off
    // the activity invalidation already re-reads the observed delegates, and a
    // gate that turns on re-reads them too. Retiring on every other collection's
    // context would spend a read on state this store is not merging into.
    const loaded = this.reads.delegates.epoch;
    if (
      (wasEligible || next) &&
      loaded !== undefined &&
      loaded !== context.epoch &&
      this.retirementEpoch !== context.epoch
    ) {
      this.retirementEpoch = context.epoch;
      this.retireDelegates();
    }
  }
  /** The delegate merge ordering is invalid across a source replacement: a
   * replacement journal rebuilds projectionRevision from 1, so an older epoch's
   * higher revision would otherwise win forever. */
  private clearDelegates(): void {
    this.appliedDelegates.clear();
    this.seenUnknownDelegates.clear();
    this.bufferedUnknownDelegates.clear();
  }
  private retireDelegates(): void {
    this.clearDelegates();
    this.reconcileDelegates();
  }
  /** Reconcile the observed delegates after a source or gate change. A
   * permanent read refusal stops the ordinary automatic reads, but a changed
   * condition is a new one: clear it so this recovery attempt is really made
   * (and, refused again, it is recorded again instead of retried in a loop). */
  private reconcileDelegates(): void {
    const delegates = this.reads.delegates;
    if (delegates.observers === 0 && !delegates.oneShot) return;
    this.change("delegates", { permanent: false });
    void this.request("delegates", "root");
  }
  /** Record the merge order a loaded delegate row has reached. */
  private recordAppliedDelegate(row: SessionDelegate): void {
    this.appliedDelegates.set(rowIdentity("delegates", row), revisionOf(row.projectionRevision));
  }
  /** The frame's logical owner scopes it: `ownerSessionId` is always the
   * physical root, so a session store keys on `logicalOwnerSessionId` (the
   * nearest ancestor session) instead. A subtree store's ref-matched frames
   * already belong to the observed subtree. */
  private delegateFrameInScope(frame: EvenerDelegateInfo): boolean {
    const context = this.state.context;
    if (!context) return false;
    if (this.scope !== "session") return true;
    return (frame.logicalOwnerSessionId ?? frame.ownerSessionId) === context.sessionId;
  }
  /** A pushed frame patches a loaded row in place and never invents one: an
   * unknown delegate asks for one root read (bounded per id) and its latest
   * frame waits for a read to admit the row. */
  private applyDelegateFrame(frame: EvenerDelegateInfo): void {
    if (!this.delegateFrameInScope(frame)) return;
    const rows = this.state.delegates.rows;
    // The id may name no loaded row or more than one: a read preserves a child
    // it served twice under one delegate id, and a frame carries no child
    // identity to tell those rows apart, so patching the first would write one
    // row's update into another.
    let index = -1;
    let ambiguous = false;
    for (let at = 0; at < rows.length; at += 1) {
      if (rows[at]?.delegateId !== frame.delegateId) continue;
      if (index !== -1) {
        ambiguous = true;
        break;
      }
      index = at;
    }
    if (ambiguous) {
      this.noteAmbiguousDelegate(frame);
      return;
    }
    if (index === -1) {
      this.noteUnknownDelegate(frame);
      return;
    }
    const current = rows[index];
    if (!current) return;
    const merged = this.mergeDelegateFrame(current, frame);
    if (merged === current) return;
    const next = rows.slice();
    next[index] = merged;
    this.recordAppliedDelegate(merged);
    this.publish({ delegates: { ...this.state.delegates, rows: next } });
  }
  private noteUnknownDelegate(frame: EvenerDelegateInfo): void {
    const read = this.reads.delegates;
    if (read.observers === 0 && !read.oneShot) return;
    // Frames can arrive out of order, so a later arrival must not replace a
    // newer settlement: keep the projector's join, exactly as a loaded row does.
    const buffered = this.bufferedUnknownDelegates.get(frame.delegateId);
    this.bufferedUnknownDelegates.set(frame.delegateId, buffered ? mergeDelegateFrames(buffered, frame) : frame);
    if (this.bufferedUnknownDelegates.size > MAX_BUFFERED_DELEGATE_FRAMES) {
      const oldest = this.bufferedUnknownDelegates.keys().next();
      if (!oldest.done) {
        this.bufferedUnknownDelegates.delete(oldest.value);
        // The evicted id must be readable again: leaving it in the seen set
        // would let a delegate whose read never admitted it stay undiscovered
        // forever, with later frames updating a buffer that no read will use.
        this.seenUnknownDelegates.delete(oldest.value);
      }
    }
    if (this.seenUnknownDelegates.has(frame.delegateId)) return;
    this.seenUnknownDelegates.add(frame.delegateId);
    void this.request("delegates", "root");
  }
  /** A frame whose delegate id names more than one loaded row cannot be
   * attributed to one of them, so it patches none: read once for the id and let
   * the read settle every row it serves. */
  private noteAmbiguousDelegate(frame: EvenerDelegateInfo): void {
    const read = this.reads.delegates;
    if (read.observers === 0 && !read.oneShot) return;
    if (this.seenUnknownDelegates.has(frame.delegateId)) return;
    this.seenUnknownDelegates.add(frame.delegateId);
    void this.request("delegates", "root");
  }
  /** The projector's join: the strictly greater revision supplies the snapshot
   * fields, while latestActivityAt is the independent maximum -- a lower
   * revision may advance it, a higher one never moves it backward. */
  private mergeDelegateFrame(current: SessionDelegate, frame: EvenerDelegateInfo): SessionDelegate {
    const incoming = delegateRowFromFrame(current, frame);
    const applied = this.appliedDelegates.get(rowIdentity("delegates", current));
    const merged = joinDelegateState(
      incoming,
      current,
      revisionOf(incoming.projectionRevision) > (applied ?? revisionOf(current.projectionRevision)),
    );
    return delegateRowsEqual(merged, current) ? current : merged;
  }
  private joinDelegateRows(
    incoming: readonly SessionDelegate[],
    previous: readonly SessionDelegate[],
  ): SessionDelegate[] {
    const previousById = new Map(previous.map((row) => [rowIdentity("delegates", row), row]));
    // A page can serve one delegate id twice, and nothing downstream can tell
    // which of those rows an id-only frame belongs to, so a buffered frame is
    // settled by the read unless the id came back as a single row.
    const served = new Map<string, number>();
    for (const row of incoming) served.set(row.delegateId, (served.get(row.delegateId) ?? 0) + 1);
    return incoming.map((row) => {
      const identity = rowIdentity("delegates", row);
      const soleRow = served.get(row.delegateId) === 1;
      const existing = previousById.get(identity);
      const applied = this.appliedDelegates.get(identity);
      let merged: SessionDelegate;
      if (applied === undefined) {
        // First sight in this epoch -- or the first read after a replacement
        // cleared the map: the served row seeds the order and wins, so an old
        // epoch's higher revision cannot regress it.
        merged = row;
      } else {
        // A served row is an authoritative snapshot: it wins at equal revision
        // (parity with a same-revision frame) and only loses to a strictly
        // newer applied frame, which it must not clobber.
        const candidate = joinDelegateState(row, existing ?? row, revisionOf(row.projectionRevision) >= applied);
        merged = existing && delegateRowsEqual(candidate, existing) ? existing : candidate;
      }
      // Record the served row's order before folding a buffered frame in, so the
      // frame is joined against the row it lands in rather than the ordering
      // that row had before this read answered.
      this.recordAppliedDelegate(merged);
      const buffered = soleRow ? this.bufferedUnknownDelegates.get(row.delegateId) : undefined;
      if (buffered) {
        this.bufferedUnknownDelegates.delete(row.delegateId);
        merged = this.mergeDelegateFrame(merged, buffered);
      }
      // An ambiguous id keeps its seen entry, or every later frame would start
      // another root read instead of staying bounded to one per id.
      if (soleRow) this.seenUnknownDelegates.delete(row.delegateId);
      this.recordAppliedDelegate(merged);
      return merged;
    });
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
  /** Stops a read's scheduled and queued requests. `rootQueued` says whether a
   * root read is left queued; failures reset so the next reads start their
   * backoff afresh, not from the failures earlier reads left behind. */
  private stopReadDemand(read: ResourceRead, rootQueued: boolean): void {
    this.cancelTimer(read);
    this.cancelPace(read);
    read.rootQueued = rootQueued;
    read.pageQueued = false;
    read.failures = 0;
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
      case "thread/status/changed": {
        if (this.client.state !== "ready") return;
        if (this.runtimeThreadId !== null && notification.params.threadId !== this.runtimeThreadId) return;
        const metadata = this.lease?.metadata();
        if (
          metadata &&
          notification.params.threadId !== metadata.threadId &&
          notification.params.threadId !== this.runtimeThreadId
        )
          return;
        if (
          metadata &&
          notification.params.threadId === metadata.threadId &&
          this.state.context &&
          this.state.context.sessionId !== metadata.sessionId &&
          notification.params.threadId !== this.runtimeThreadId
        )
          return;
        this.statusRevision += 1;
        this.statusUpdate = {
          revision: this.statusRevision,
          threadId: notification.params.threadId,
          status: notification.params.status,
        };
        if (
          metadata &&
          notification.params.threadId === metadata.threadId &&
          this.state.context?.sessionId === metadata.sessionId
        )
          this.publish({ runtime: { ...metadata, status: this.statusUpdate.status } });
        return;
      }
      case "evener/delegate/updated":
        // Only an eligible context enables the no-read merge. An older or
        // retained source keeps today's behavior: the activity-changed
        // invalidation re-reads the collection, so reports still land.
        if (!this.pushEligible) return;
        this.applyDelegateFrame(notification.params.delegate);
        return;
      case "evener/thread/activity/changed":
        if (
          this.scope === "session" &&
          this.state.context &&
          notification.params.sessionId !== this.state.context.sessionId
        )
          return;
        changed = notification.params.resources;
        break;
      case "evener/thread/resync":
        for (const resource of resources) {
          this.reads[resource].refresh = null;
          this.cancelPace(this.reads[resource]);
        }
        // The server may already have emitted a reply before the alias was
        // cleared. It must not publish after this resync boundary.
        this.generation += 1;
        this.runtimeThreadId = notification.params.threadId;
        if (this.statusUpdate?.threadId !== this.runtimeThreadId) this.statusUpdate = null;
        if (this.state.runtime && this.state.runtime.threadId !== this.runtimeThreadId) this.publish({ runtime: null });
        changed = resources;
        break;
      default:
        return;
    }
    // The no-read merge covers delegate field changes; summary, jobs and
    // watches refresh as today. A resync still refreshes every observed
    // resource, delegates included.
    const skipDelegates = notification.method === "evener/thread/activity/changed" && this.pushEligible;
    for (const resource of changed) {
      if (resource === "delegates" && skipDelegates) continue;
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
    this.runtimeThreadId = null;
    this.statusUpdate = null;
    for (const stop of this.stopListening) stop();
    this.stopListening = [];
    if (this.state.runtime) this.publish({ runtime: null });
  }
  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.generation += 1;
    for (const resource of resources) {
      const read = this.reads[resource];
      this.stopReadDemand(read, false);
      read.boundary = undefined;
      read.refresh = null;
      read.observers = 0;
    }
    this.lease?.release();
    this.lease = null;
    this.runtimeThreadId = null;
    this.statusUpdate = null;
    this.clearDelegates();
    this.pushEligible = false;
    this.state = { ...this.state, runtime: null };
    for (const stop of this.stopListening) stop();
    this.stopListening = [];
    this.listeners.clear();
  }
}

/** A winning frame supplies every mutating field, absent optionals included,
 * so it clears what it omits; only the loaded row's identity survives: a frame
 * carries no refs and `name` is immutable per delegate. */
function delegateRowFromFrame(row: SessionDelegate, frame: EvenerDelegateInfo): SessionDelegate {
  return {
    delegateId: row.delegateId,
    name: row.name,
    ownerRef: row.ownerRef,
    rootRef: row.rootRef,
    childRef: row.childRef,
    runGeneration: frame.runGeneration,
    projectionRevision: frame.projectionRevision,
    reportPreview: frame.reportPreview,
    reportPreviewTruncated: frame.reportPreviewTruncated,
    parentDelegateId: frame.parentDelegateId,
    description: frame.description ?? "",
    task: frame.task ?? "",
    type: frame.type,
    lifecycle: frame.lifecycle,
    phase: frame.phase,
    status: frame.status,
    outcome: frame.outcome,
    reason: frame.reason,
    error: frame.error,
    terminal: frame.terminal ?? false,
    resumable: frame.resumable,
    notResumableReason: frame.notResumableReason,
    model: frame.model,
    reasoningEffort: frame.reasoningEffort,
    runStartedAt: frame.runStartedAt,
    runEndedAt: frame.runEndedAt,
    latestActivityAt: frame.latestActivityAt,
    usage: frame.usage,
    worktree: frame.worktree,
  };
}
/** time.RFC3339Nano, the layout internal/appprojector parses activity
 * timestamps with. Date.parse is looser and keeps only milliseconds, so the
 * comparison tests this shape and reads the fraction itself. */
const activityTimestamp = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:[.,](\d+))?(Z|([+-])(\d{2}):(\d{2}))$/;
/** The whole seconds since the epoch (offset applied) and the fractional
 * nanoseconds of an RFC3339Nano activity timestamp, or null when the value is
 * not one -- so a value only Date.parse accepts never orders against a real
 * one, and two values that differ below a millisecond order exactly as the
 * projector's time.Time does. */
function activityInstant(value: string): { seconds: number; nanos: number } | null {
  const match = activityTimestamp.exec(value);
  if (!match) return null;
  const [, year, month, day, hour, minute, second, fraction, , sign, offsetHour, offsetMinute] = match;
  const monthValue = Number(month);
  const dayValue = Number(day);
  if (monthValue < 1 || monthValue > 12 || dayValue < 1 || dayValue > 31) return null;
  // Go's time.RFC3339Nano rejects a leap second ("second out of range"), so
  // accepting one would order a value the projector refuses.
  if (Number(hour) > 23 || Number(minute) > 59 || Number(second) > 59) return null;
  if (sign !== undefined && (Number(offsetHour) > 23 || Number(offsetMinute) > 59)) return null;
  const yearValue = Number(year);
  const utc = Date.UTC(yearValue, monthValue - 1, dayValue, Number(hour), Number(minute), Number(second));
  const at = new Date(utc);
  // Date.UTC rolls a calendar-invalid day (31 February) into the next month,
  // which the projector's parser rejects outright.
  if (at.getUTCFullYear() !== yearValue || at.getUTCMonth() !== monthValue - 1 || at.getUTCDate() !== dayValue) {
    return null;
  }
  const offsetSeconds =
    sign === undefined ? 0 : (sign === "-" ? -1 : 1) * (Number(offsetHour) * 3600 + Number(offsetMinute) * 60);
  return { seconds: utc / 1000 - offsetSeconds, nanos: Number((fraction ?? "").padEnd(9, "0").slice(0, 9)) };
}
/** Mirrors internal/appprojector.delegateActivityAfter: a blank candidate never
 * wins, a blank current loses to any non-blank candidate, and otherwise the
 * candidate wins only when both values parse and it is strictly later. */
function activityAfter(candidate: string | undefined, current: string | undefined): boolean {
  if (!candidate || candidate.trim() === "") return false;
  if (!current || current.trim() === "") return true;
  const candidateAt = activityInstant(candidate);
  const currentAt = activityInstant(current);
  if (!candidateAt || !currentAt) return false;
  if (candidateAt.seconds !== currentAt.seconds) return candidateAt.seconds > currentAt.seconds;
  return candidateAt.nanos > currentAt.nanos;
}
/** The projector's join for one delegate: `incomingWins` picks the snapshot
 * fields, while latestActivityAt is the independent maximum of the two -- a
 * lower revision may advance it, a higher one never moves it backward. The
 * other side's activity is taken only when it is strictly later, exactly as
 * internal/appprojector.mergeAppwireDelegateInfo moves it, so a pushed row and
 * the same row read back agree. */
function joinDelegateState<T extends { latestActivityAt?: string | undefined }>(
  incoming: T,
  current: T,
  incomingWins: boolean,
): T {
  const winner = incomingWins ? incoming : current;
  const other = incomingWins ? current : incoming;
  const activity = activityAfter(other.latestActivityAt, winner.latestActivityAt)
    ? other.latestActivityAt
    : winner.latestActivityAt;
  return activity === winner.latestActivityAt ? winner : { ...winner, latestActivityAt: activity };
}
/** A missing/NaN revision is the zero value; an unset read row must not beat a
 * real frame, and two unset rows compare equal. */
function revisionOf(value: number | undefined): number {
  return typeof value === "number" && !Number.isNaN(value) ? value : 0;
}
/** The projector's join for two frame snapshots of one delegate: the strictly
 * greater revision supplies the fields, latestActivityAt is the independent
 * maximum. Mirrors `mergeDelegateFrame` for a row not yet loaded. */
function mergeDelegateFrames(current: EvenerDelegateInfo, frame: EvenerDelegateInfo): EvenerDelegateInfo {
  return joinDelegateState(
    frame,
    current,
    revisionOf(frame.projectionRevision) > revisionOf(current.projectionRevision),
  );
}
function delegateRowsEqual(a: SessionDelegate, b: SessionDelegate): boolean {
  return (
    a.delegateId === b.delegateId &&
    a.name === b.name &&
    a.ownerRef === b.ownerRef &&
    a.rootRef === b.rootRef &&
    a.childRef === b.childRef &&
    a.runGeneration === b.runGeneration &&
    a.projectionRevision === b.projectionRevision &&
    a.reportPreview === b.reportPreview &&
    a.reportPreviewTruncated === b.reportPreviewTruncated &&
    a.parentDelegateId === b.parentDelegateId &&
    a.description === b.description &&
    a.task === b.task &&
    a.type === b.type &&
    a.lifecycle === b.lifecycle &&
    a.phase === b.phase &&
    a.status === b.status &&
    a.outcome === b.outcome &&
    a.reason === b.reason &&
    a.error === b.error &&
    a.terminal === b.terminal &&
    a.resumable === b.resumable &&
    a.notResumableReason === b.notResumableReason &&
    a.model === b.model &&
    a.reasoningEffort === b.reasoningEffort &&
    a.runStartedAt === b.runStartedAt &&
    a.runEndedAt === b.runEndedAt &&
    a.latestActivityAt === b.latestActivityAt &&
    sameJsonValue(a.usage, b.usage) &&
    sameJsonValue(a.worktree, b.worktree)
  );
}
function rowIdentity(resource: SessionActivityCollection, row: ActivityRow): string {
  if (resource === "delegates") {
    // A read can serve one delegate id twice (a child it served again), and the
    // walk, the boundary and the merge order must key on the same pair the read
    // preserves or one served row replaces the other.
    const delegate = row as SessionDelegate;
    return `${delegate.delegateId}\u0000${delegate.childRef}`;
  }
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
