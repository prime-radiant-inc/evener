import {
  adoptableBootGeneration,
  DAEMONLESS_BOOT_GENERATION,
  liveBootGeneration,
  retiresBootGeneration,
} from "./bootGeneration";
import type { AppwireClientLike } from "./clientLike";
import { mutationErrorData, WireError } from "./errors";
import { sameJsonValue } from "./plainObject";
import { intentWithinBound } from "./sessionActivity";
import { isThreadNotFound } from "./sessionErrors";
import { activityAfter, activityInstant, joinDelegateState, revisionOf } from "./stableDelegate";
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
/** A pushed delegate frame held for a row no read has admitted yet, with the
 * boot generation its publisher stamped on it. An older peer stamps nothing,
 * and an absent stamp is kept absent: it says nothing about identity, so it
 * is never read as a generation and never retired as one. */
interface BufferedDelegateFrame {
  frame: EvenerDelegateInfo;
  bootGeneration: string | undefined;
}
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
  /** A replacement epoch another collection's response announced before this
   * collection's own read saw it: what frames arriving in that window belong
   * to, and the ordering this read is about to replace. */
  announcedEpoch: string | undefined;
  /** The session this collection's rows belong to. `acceptContext` clears it on
   * a replacement, so a continuation page whose response names a different
   * session is recognized as a restart rather than adopted. */
  sessionId: string | undefined;
  incomplete: boolean;
  boundary: string | undefined;
  refresh: RefreshWalk | null;
  pace: { handle: unknown; resume(): void } | null;
  /** Bumped when the last observer leaves; a read begun before it is dropped. */
  releases: number;
}
/** Cap on the buffered latest frame per unknown row identity; the per-key
 * seen-unknown set bounds reads, so this only guards an unbounded store. */
const MAX_BUFFERED_DELEGATE_FRAMES = 128;
/** The producer publishes activity at most once per second, so one read per
 * heartbeat cadence -- coalesced across every streaming id -- keeps a paged
 * roster's rows reading as live without a read per frame. */
const DELEGATE_HEARTBEAT_READ_PACE_MS = 1000;
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
  announcedEpoch: undefined,
  sessionId: undefined,
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
  private readonly appliedDelegates = new Map<string, { epoch: string | undefined; revision: number }>();
  /** Epochs the delegate ordering has moved past. A response from a retired
   * epoch can still arrive late, and its context names that epoch: announcing it
   * again would point the merge ordering at records this store already cleared. */
  private readonly retiredDelegateEpochs = new Set<string>();
  /** What a frame that named no loaded row identity asked for a root read
   * with: at most one read per identity. A legacy frame's reads are bounded
   * per delegate id by its own revision ladder. Cleared with the applied map. */
  private readonly seenUnknownDelegates = new Set<string>();
  /** The revision of the last frame an older, child-ref-less producer asked
   * for a read with, per delegate id: a legacy frame can name no row, so a read
   * is the only place its change can land, and only a strictly newer revision
   * is newer news worth one. Cleared with the applied map. */
  private readonly legacyDelegateReadRevisions = new Map<string, number>();
  /** Latest pushed frame per unknown row identity, applied once a read admits
   * the row so a settle during a later-page read is not dropped. */
  private readonly bufferedUnknownDelegates = new Map<string, BufferedDelegateFrame>();
  /** The boot generation the delegate ordering is under, adopted from the
   * frames' own stamps and from a caller that holds a source identity
   * (`adoptBootGeneration`, for a store beside a transcript read model).
   * Undefined until a source names itself: a peer too old to stamp its frames
   * sends nothing about identity, and nothing is inferred on its behalf. */
  private delegateBootGeneration: string | undefined;
  /** The generation each loaded row's hold sample was published under, keyed
   * by row identity. `holdSequence` is the publishing source's own counter and
   * restarts at 1 when another daemon boots, so two samples order only when
   * both name the same generation. A row with no entry wears a sample from a
   * source that named no generation (an older peer), and such a sample orders
   * only against another of its own kind. */
  private readonly delegateHoldBootGenerations = new Map<string, string>();
  /** The activity page each loaded row's hold sample was served by, keyed by
   * row identity. A read carries no boot generation -- the collection's own
   * source epoch is the only identity it names, and the epoch alone cannot tell
   * a page the replacement served from one the daemon it replaced did, because
   * any response refreshes it and the two sides learn a replacement
   * independently. So a page's sample is attributed to the reading source: the
   * ordering's boot transition count when it was served. A sample served
   * before the latest transition predates the adopted generation without
   * anything being guessed about it. At most one of this map and
   * `delegateHoldBootGenerations` holds an identity: each records the sample
   * the row wears now. */
  private readonly delegatePageHolds = new Map<string, number>();
  /** How many boot generations this store's delegate ordering has moved
   * through. A page's hold sample cannot name its publisher, so this is what
   * distinguishes a page the replacement served from one the daemon it
   * replaced did. */
  private bootTransitions = 0;
  /** Boot generations the delegate ordering has moved past. A hold or buffered
   * frame stamped with one of these was numbered by a daemon that no longer
   * serves the thread, so its sequence can never order a sample the
   * replacement takes. */
  private readonly retiredDelegateBootGenerations = new Set<string>();
  /* The hold is ordered by `holdSequence`, the source's own monotonic counter,
   * and by nothing else. Entering and leaving the drain move no durable
   * delegate field, so the projection revision cannot order the hold: both
   * edges arrive at the revision the row already holds. Arrival order cannot
   * order it either, because a publisher that sampled the hold and then paused
   * can send after a later sample, and a read can be delivered after a frame it
   * predates. A store-local stamp would only move that mistake into the client.
   * `holdSequence` rides every row and frame, so the row itself says where its
   * hold sits and every merge compares the same number the source assigned. */
  /** Whether a buffered frame was evicted before a read could place it. A walk
   * in flight keeps the frames for the rows it will settle, so the recovery read
   * waits for that walk to finish rather than restarting it. */
  private evictedDelegateFrame = false;
  /** A frame naming an identity arrived while a walk was in flight, and that
   * walk froze its membership before the identity's row was created, so its
   * pages cannot serve the row: one coalesced root read is owed once the walk
   * finishes. Cleared with the applied map. */
  private followUpDelegateRead = false;
  /** The last accepted context's no-read-merge eligibility. */
  private pushEligible = false;
  /** Escalations still pending for this scope, so resolving one of several
   * keeps the approval state until the last one leaves. Seeded from the
   * subscription metadata and updated by the requested/resolved frames. */
  private readonly pendingEscalations = new Set<string>();
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
  /** The snapshot a binding that keeps these rows alive over a fresh store
   * should retain. A hold sample a page served before the latest boot
   * transition cannot be attributed to either side of that transition, so the
   * row is reported as carrying no wait -- its durable fields intact -- until a
   * page of the adopted source settles it. Without such a row the state itself
   * is returned. */
  retainedSnapshot(): SessionActivitySnapshot {
    const rows = this.state.delegates.rows;
    let next: SessionDelegate[] | null = null;
    for (let at = 0; at < rows.length; at += 1) {
      const row = rows[at];
      if (!row || !this.unresolvedPageHold(row)) continue;
      if (!next) next = rows.slice();
      next[at] = withoutDelegateHold(row);
    }
    return next ? { ...this.state, delegates: { ...this.state.delegates, rows: next } } : this.state;
  }
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

  /** Joins the resource's registered read span — the read in flight now and
   * any follow-up read its completion queues — without adding demand. A paced
   * refresh walk registers as one span, so a suite that drives an
   * invalidation-driven walk can await its actual completion instead of
   * watching a wall-clock condition ceiling the walk's real-clock pacing can
   * outlast under load. Call it from the same synchronous window that emitted
   * the invalidation, so the walk is already registered when the join starts. */
  async settleForTests(resource: SessionActivityResource): Promise<void> {
    let inFlight = this.reads[resource].inFlight;
    while (inFlight) {
      await inFlight;
      inFlight = this.reads[resource].inFlight;
    }
  }

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
          read.announcedEpoch = undefined;
        }
        this.runtimeThreadId = null;
        this.statusUpdate = null;
        // The connection may come back to a producer without the push
        // capability, and an invalidation that arrives before the first
        // refreshed response would trust the old one: the merge is re-enabled
        // only by a fresh live capability context.
        this.pushEligible = false;
        this.clearDelegates();
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
    this.resetDelegateBootGenerations();
    // The replacement context has not been seen by noteContext yet; a fresh
    // session resets eligibility rather than reporting a live->retained flip.
    this.pushEligible = false;
    const metadata = this.lease?.metadata();
    if (metadata?.sessionId !== context.sessionId || this.statusUpdate?.threadId !== metadata?.threadId)
      this.statusUpdate = null;
    for (const resource of resources) {
      const read = this.reads[resource];
      const demanded = read.observers > 0 || read.oneShot || read.rootQueued || read.pageQueued;
      this.stopReadDemand(read, resource === source ? read.rootQueued : demanded);
      read.cursor = undefined;
      read.epoch = undefined;
      read.announcedEpoch = undefined;
      read.sessionId = undefined;
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
        const session = result.context.sessionId;
        const replacedSession = read.sessionId !== session;
        const previous = read.epoch;
        const crossing = previous !== undefined && previous !== epoch;
        // The delegate ordering never returns to an epoch it has left, so keep
        // the epoch itself: a late sibling response still names it, and that
        // announcement must not reactivate records this store has cleared.
        if (crossing && resource === "delegates" && previous !== undefined) this.retiredDelegateEpochs.add(previous);
        // The delegate merge ordering is epoch-scoped, so the delegates
        // collection's own replacement retires it here.
        // A summary-triggered retire already cleared this epoch's state and
        // forced the read, so clearing again here would wipe a frame that
        // arrived between the two and leave it undiscovered until another.
        if (crossing && resource === "delegates" && read.announcedEpoch !== epoch) {
          this.clearDelegates();
          read.announcedEpoch = undefined;
          // Nothing announced this replacement, so no forced read is already on
          // its way, and the clear just retired every frame merged under the
          // retired epoch. Only a fresh read can recount them.
          read.rootQueued = true;
        }
        // A fresh delegates read is the authority on the epoch: an announcement
        // naming an epoch the ordering has already left would judge this read's
        // own records retired, so retire the announcement instead.
        if (
          resource === "delegates" &&
          read.announcedEpoch !== undefined &&
          this.retiredDelegateEpochs.has(read.announcedEpoch)
        ) {
          read.announcedEpoch = undefined;
        }
        read.epoch = epoch;
        // A page cannot cross epochs: its rows extend a retired walk. A root read
        // is the fresh read for the new epoch. A page that first observes a
        // replacement session restarts too, so its rows are never adopted as the
        // new session's own.
        if (!root && (crossing || replacedSession)) {
          read.sessionId = session;
          read.cursor = undefined;
          read.refresh = null;
          read.rootQueued = true;
          continue;
        }
        read.sessionId = session;
        if (resource === "summary") {
          const summary = result as SessionActivitySummary;
          const unavailable = (summary.issues?.length ?? 0) > 0;
          if (!unavailable) read.failures = 0;
          const runtime = this.seedEscalations(this.runtimeFor(summary.context, statusRevision));
          this.publish({
            context: summary.context,
            runtime,
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
          // A clean continuation cannot acknowledge an unresolved root scan.
          // Retain its issues and root recovery demand until fresh reconciliation.
          const issues = reconciled ? [] : mergeIssues(current.issues, freshIssues);
          // A page that admits a buffered frame's identity is the one place
          // the frame can be placed: the frame names its row exactly, and
          // the applied record protects the merge against every later page
          // of the same read, so an unsettled page loses nothing by placing
          // it. An identity no loaded row carries keeps waiting.
          if (resource === "delegates") {
            // A read joins each row the same way a frame does, so it cannot
            // clobber a newer frame nor skip one applied in the gap; a buffered
            // frame is placed by the page that admits its identity.
            rows = this.joinDelegateRows(
              rows as readonly SessionDelegate[],
              current.rows as readonly SessionDelegate[],
              new Set(page.rows.map((row) => rowIdentity("delegates", row))),
            );
          }
          if (reachedBoundary) {
            read.refresh = null;
            if (reconciled) read.boundary = undefined;
            // A frame evicted while this walk held it was never placed: recover
            // it with one read now that the walk is over. The flag belongs to the
            // delegate buffer, so another collection's walk does not consume it.
            if (resource === "delegates" && this.evictedDelegateFrame) {
              this.evictedDelegateFrame = false;
              read.rootQueued = true;
            }
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
          if (resource === "delegates") {
            this.settleBufferedFrames();
            // A read whose collection lost its last observer -- a listener
            // can release it during the publish above -- is dropped from here
            // on: it may not schedule the follow-up read either, or the
            // loop's own condition would fetch and publish a page nobody
            // observes.
            if (dropped()) continue;
            // A frame buffered during a walk named a row that walk's frozen
            // membership could not serve. The walk is over and no page it
            // served could place it, so the frames still buffered are owed
            // the one coalesced read that can admit their rows -- but only
            // while something still asks for the collection: a released
            // collection leaves its frames buffered, not reading.
            if (
              this.followUpDelegateRead &&
              read.refresh === null &&
              this.bufferedUnknownDelegates.size > 0 &&
              (read.observers > 0 || read.oneShot)
            ) {
              this.followUpDelegateRead = false;
              read.rootQueued = true;
            }
          }
          if (dropped()) continue;
          this.publish({
            context: page.context,
            runtime: this.seedEscalations(this.runtimeFor(page.context, statusRevision)),
          });
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
        // A read that was standing in for the delegates failed for good: the
        // invalidation suppressed the delegate read because this one would carry
        // the capability back, and nothing else would now reveal that the
        // ancestor released its runtime.
        if (permanent && resource !== "delegates") this.reconcileDelegates();
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
    // A gate that turns on recovers a read a refusal parked while the source was
    // retained: without this the resource stays refused and later unknown-id
    // frames are buffered for a read that will never run.
    if (next && !wasEligible && this.readState("delegates").permanent) this.reconcileDelegates();
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
    const delegates = this.reads.delegates;
    if (
      (wasEligible || next) &&
      delegates.epoch !== undefined &&
      delegates.epoch !== context.epoch &&
      // A response from an epoch the ordering has left is late, not late news:
      // announcing it would retire the current ordering for an older one.
      !this.retiredDelegateEpochs.has(context.epoch) &&
      delegates.announcedEpoch !== context.epoch
    ) {
      // An announcement this one replaces is retired: a response that still
      // names it is behind the ordering, and retiring keeps it from reclaiming
      // the announcement and clearing the current epoch's records.
      if (delegates.announcedEpoch !== undefined) this.retiredDelegateEpochs.add(delegates.announcedEpoch);
      // Announced before the retire: a frame that arrives while the forced read
      // is in flight is the replacement's own, and the retire's publish can
      // reach a listener that handles one before this method returns.
      delegates.announcedEpoch = context.epoch;
      this.retireDelegates();
    }
  }
  /** The delegate merge ordering is invalid across a source replacement: a
   * replacement journal rebuilds projectionRevision from 1, so an older epoch's
   * higher revision would otherwise win forever. The buffered frames and their
   * read bounds belong to the retired generation too, so they are cleared rather
   * than placed on the replacement's rows. */
  private clearDelegates(): void {
    this.appliedDelegates.clear();
    this.seenUnknownDelegates.clear();
    this.legacyDelegateReadRevisions.clear();
    this.bufferedUnknownDelegates.clear();
    this.followUpDelegateRead = false;
  }
  /** Drop everything the delegate ordering knows about the source it was
   * reading: the adopted generation, the samples each loaded row was
   * attributed to it under, and the generations it has moved past. The rows
   * being replaced were a different session's, so their samples are not the
   * replacement's to attribute, and a retired set from the replaced session
   * names generations the replacement's frames can never be judged against. */
  private resetDelegateBootGenerations(): void {
    this.delegateBootGeneration = undefined;
    this.delegateHoldBootGenerations.clear();
    this.delegatePageHolds.clear();
    this.bootTransitions = 0;
    this.retiredDelegateBootGenerations.clear();
  }
  private retireDelegates(): void {
    this.clearDelegates();
    this.reconcileDelegates();
  }
  /** Adopt the boot generation a caller holds for this thread's daemon, or for
   * a generation a frame stamped.
   *
   * Adopting a different generation retires the one it replaced: the holds and
   * buffered frames that daemon numbered are dropped with it, because its
   * `holdSequence` counts from its own boot and can never order a sample its
   * replacement takes. A token that names no owner (`unknown-owner`, an empty
   * string, or an older peer's omission) adopts nothing and retires nothing:
   * the store keeps whatever it already held.
   *
   * A token the adopted generation *ignores* is a departed boot of the same
   * owner: a read issued before the owner's newer boot can answer after a frame
   * already adopted it, and adopting that read's token back would restore holds
   * the newer boot retired and judge its own later frames retired. A different
   * owner, a higher counter, and attested absence all compare as replacements,
   * so only a provably older counter of the same owner is refused. */
  adoptBootGeneration(bootGeneration: string | undefined): void {
    const adopted = adoptableBootGeneration(bootGeneration);
    if (adopted === undefined || adopted === this.delegateBootGeneration) return;
    if (retiresBootGeneration(this.delegateBootGeneration, adopted)) return;
    const previous = this.delegateBootGeneration;
    this.delegateBootGeneration = adopted;
    this.bootTransitions += 1;
    // A generation the ordering has returned to is current again: a late frame
    // that named it must not be judged retired.
    this.retiredDelegateBootGenerations.delete(adopted);
    if (previous === undefined) {
      // The first adoption names the source the rows already displayed were
      // read from: the store reads one source at a time, so the samples that
      // named no source are that source's. Attributing them here is what lets a
      // later replacement retire exactly those samples and no others.
      //
      // A row a page served is not one of them. Its sample is the activity
      // collection's, and the transcript's token names the history this read
      // model found -- the two can already disagree when relay recovery
      // switched daemons, so labelling a page with this token is exactly the
      // mislabel that wiped a replacement's own hold.
      for (const row of this.state.delegates.rows) {
        const identity = rowIdentity("delegates", row);
        if (this.delegateHoldBootGenerations.has(identity) || this.delegatePageHolds.has(identity)) continue;
        if (hasDelegateHoldSample(row)) this.delegateHoldBootGenerations.set(identity, adopted);
      }
      // A page served before this first adoption was served by the source this
      // adoption names: no replacement has been reported yet, so nothing says
      // otherwise. Recording it under the transition this adoption opened is
      // what lets the source's own frames order against it.
      for (const [identity, transitions] of this.delegatePageHolds) {
        if (transitions < this.bootTransitions) this.delegatePageHolds.set(identity, this.bootTransitions);
      }
    }
    // The source being replaced takes the samples it numbered with it.
    // Rows the store never attributed are left alone: nothing proved whose
    // samples they wear, so nothing may be cleared on a guess.
    if (previous !== undefined) this.retireDelegateBootGeneration(previous);
    // A buffered frame from the retired generation names durable state the
    // replacement never published, so it is dropped rather than settled. Its
    // identity is released so a later frame can ask again.
    for (const [identity, buffered] of this.bufferedUnknownDelegates) {
      if (!this.bootGenerationRetired(buffered.bootGeneration)) continue;
      this.bufferedUnknownDelegates.delete(identity);
      this.seenUnknownDelegates.delete(identity);
    }
    if (this.bufferedUnknownDelegates.size === 0) this.followUpDelegateRead = false;
    if (adopted === DAEMONLESS_BOOT_GENERATION) {
      // Attested absence: the hub stamps this token on a *successful* read as
      // saved-history provenance, which every caller gates out (`live`,
      // docs/product/history-content.md), so reaching this method with it means
      // a failed read established that nobody owns the thread. Nothing can be
      // holding it open, so no wait may be displayed -- including a page's
      // sample, which a live replacement's transition must not wipe.
      this.retireAllHolds();
      return;
    }
    // A page's sample cannot be attributed to the replaced daemon or to its
    // replacement: reads carry no boot generation, and no frame re-publishes an
    // unchanged wait to restore one that was cleared. So the samples the
    // transition leaves unresolved are not wiped but re-read: the
    // reconciliation walk re-serves the collection from the daemon serving the
    // thread now, and that page settles every one of them.
    if (previous !== undefined && this.unresolvedPageHolds()) this.reconcileDelegates();
  }
  /** The boot generation the delegate ordering is under, or undefined while no
   * source has named itself. A binding that reuses rows a retired store
   * retained reads this to tell a store that must be told whose samples those
   * rows wear from one that has already learned a source of its own. */
  get bootGeneration(): string | undefined {
    return this.delegateBootGeneration;
  }
  /** Retire one generation's holds: every loaded row whose sample it numbered
   * stops wearing that sample, and the generation joins the set a late frame
   * or buffered frame is judged against. The wait belonged to a daemon that no
   * longer serves the thread, so keeping it displayed would show a hold
   * nothing can now release. */
  private retireDelegateBootGeneration(previous: string): void {
    this.retiredDelegateBootGenerations.add(previous);
    this.clearHoldSamples((identity) => this.delegateHoldBootGenerations.get(identity) === previous);
  }
  /** Whether a generation is one the ordering has left: a sample it stamped
   * cannot order against the adopted source's. An absent stamp names no
   * generation and is never retired -- an older peer's frame says nothing
   * about identity, so nothing about identity may be read into it. */
  private bootGenerationRetired(bootGeneration: string | undefined): boolean {
    if (bootGeneration === undefined) return false;
    if (this.retiredDelegateBootGenerations.has(bootGeneration)) return true;
    return retiresBootGeneration(this.delegateBootGeneration, bootGeneration);
  }
  /** Whether a loaded row still wears a hold sample a page served before the
   * latest boot transition: the store cannot tell whether that page came from
   * the daemon the transition replaced or from the one that replaced it. */
  private unresolvedPageHold(row: SessionDelegate): boolean {
    if (!hasDelegateHoldSample(row)) return false;
    const transitions = this.delegatePageHolds.get(rowIdentity("delegates", row));
    return transitions !== undefined && transitions < this.bootTransitions;
  }
  /** Whether any loaded row wears an unresolved page hold. */
  private unresolvedPageHolds(): boolean {
    if (this.delegatePageHolds.size === 0) return false;
    return this.state.delegates.rows.some((row) => this.unresolvedPageHold(row));
  }
  /** Clear every loaded row's hold sample. Only a token that attests nobody
   * serves the thread any more earns this: no daemon is left whose wait the
   * row could be reporting, so nothing may keep displaying one. */
  private retireAllHolds(): void {
    this.clearHoldSamples(() => true);
  }
  /** Clear the hold sample of every loaded row `shouldClear` selects, and the
   * attribution each of those rows wears. Rows the predicate does not admit are
   * left alone: nothing proved whose samples they wear, so nothing may be
   * cleared on a guess. At most one of the two attribution maps holds an
   * identity -- each records the sample the row wears now -- so clearing both
   * here is what keeps that invariant without either caller repeating it. */
  private clearHoldSamples(shouldClear: (identity: string) => boolean): void {
    const rows = this.state.delegates.rows;
    let next: SessionDelegate[] | null = null;
    for (let at = 0; at < rows.length; at += 1) {
      const row = rows[at];
      if (!row) continue;
      const identity = rowIdentity("delegates", row);
      if (!shouldClear(identity)) continue;
      this.delegateHoldBootGenerations.delete(identity);
      this.delegatePageHolds.delete(identity);
      const cleared = withoutDelegateHold(row);
      if (cleared === row) continue;
      if (!next) next = rows.slice();
      next[at] = cleared;
    }
    if (next) this.publish({ delegates: { ...this.state.delegates, rows: next } });
  }
  /** Whether a loaded row's held sample orders against an incoming sample
   * stamped `sampleBootGeneration`: both must name the same generation. A
   * sample the row cannot attribute to the incoming one is not comparable -- a
   * replaced daemon's sequence would otherwise outrank the replacement's first
   * sample -- so the incoming sample stands instead, exactly as a first-sight
   * one does.
   *
   * An incoming sample that names no generation says nothing about identity,
   * so it compares with whatever the row holds, with one exception: a page's
   * sample belongs to the activity source epoch it was served in, and a page
   * from an older epoch is not this source's to order against. The sender's
   * omission is no evidence it is another source's -- a previous-build daemon
   * stamps its reads with its boot and its delegate frames not at all, and that
   * source's page and frame must still order against each other. */
  private holdsComparable(identity: string, sampleBootGeneration: string | undefined): boolean {
    if (sampleBootGeneration === undefined) {
      const page = this.delegatePageHolds.get(identity);
      return page === undefined || page === this.bootTransitions;
    }
    const page = this.delegatePageHolds.get(identity);
    if (page !== undefined) {
      // A page names no boot generation of its own. It is the adopted source's
      // only when it was served after the latest transition, and then only the
      // generation the store holds for that source orders against it.
      return page === this.bootTransitions && this.delegateBootGeneration === sampleBootGeneration;
    }
    return this.delegateHoldBootGenerations.get(identity) === sampleBootGeneration;
  }
  /** Attribute a row's hold sample to the generation that published it. A frame
   * that omitted its stamp names no generation, so it is attributed the way a
   * page is: to the activity source epoch the ordering is in now. Its sample
   * orders against a page that same source served in this epoch, while the next
   * transition makes it unresolved and re-reads it, exactly as it does a page's
   * -- a replacement's samples are never ordered against it. */
  private recordHoldBootGeneration(identity: string, bootGeneration: string | undefined): void {
    if (bootGeneration === undefined) {
      this.recordPageHold(identity);
      return;
    }
    if (this.delegateHoldBootGenerations.get(identity) === bootGeneration) return;
    this.delegatePageHolds.delete(identity);
    this.delegateHoldBootGenerations.set(identity, bootGeneration);
  }
  /** Attribute a row's hold sample to the activity page that served it, under
   * the boot transition the ordering is in now. */
  private recordPageHold(identity: string): void {
    this.delegateHoldBootGenerations.delete(identity);
    this.delegatePageHolds.set(identity, this.bootTransitions);
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
  /** Record the merge order a loaded delegate row has reached. A row a read
   * served carries the epoch of that response, not the announced one: a read
   * still in flight for the epoch being replaced is stamped with the journal its
   * revisions came from, so it cannot seed the replacement's ordering with an
   * inflated old-epoch revision and leave retired rows displayed. A frame is
   * stamped with the announced epoch -- see `delegateEpoch`. */
  private recordAppliedDelegate(row: SessionDelegate, epoch: string | undefined): void {
    this.appliedDelegates.set(rowIdentity("delegates", row), {
      epoch,
      revision: revisionOf(row.projectionRevision),
    });
  }
  /** The recorded revision for a row, or undefined when the record belongs to a
   * retired epoch: a source replacement rebuilds revisions from 1, so an
   * old-epoch record must not judge a new-epoch row or frame. */
  private appliedRevision(identity: string): number | undefined {
    const applied = this.appliedDelegates.get(identity);
    return applied && applied.epoch === this.delegateEpoch() ? applied.revision : undefined;
  }
  /** The epoch the delegate merge ordering belongs to. A sibling response can
   * announce a replacement while the forced delegates read is still in flight;
   * a frame that arrives in that window already carries the replacement's
   * state, so recording it under the epoch being replaced would let the
   * arriving read judge the record retired and overwrite the frame with its
   * own, older row. */
  private delegateEpoch(): string | undefined {
    const delegates = this.reads.delegates;
    return delegates.announcedEpoch ?? delegates.epoch;
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
  /** A pushed frame patches a loaded row in place and never invents one: the
   * frame carries the same child ref the read row's childRef keys on, so it
   * names its row identity directly. A frame whose identity no loaded row
   * matches asks for one root read (bounded per identity) and its latest frame
   * waits for a read to admit the row. A frame from an older producer carries
   * no child ref and can name no row: it asks for one read per delegate id
   * per strictly newer revision -- or one paced, coalesced read while its
   * activity keeps advancing at the same revision -- and patches nothing
   * itself. A frame that
   * matches a loaded row before any read has established the delegates
   * ordering's epoch -- a replacement's retained
   * rows with the first response still pending -- is buffered like an unknown
   * one: a merge recorded under an undefined epoch is invisible to the ordering
   * that response is about to establish, and its older snapshot would overwrite
   * the frame as first sight. The response's settlement reconciles the buffered
   * frame with the row it served.
   *
   * `bootGeneration` is the identity the frame's own publisher stamped on it
   * -- not the store's: a frame from a daemon the store has moved past
   * is dropped, and a frame that names a generation the store has not adopted
   * is what adopts it. One such frame may move an ordering the store already
   * holds: a provably newer generation of the SAME owner, on a frame
   * addressed to this thread, names the daemon serving the thread now. */
  private applyDelegateFrame(frame: EvenerDelegateInfo, bootGeneration: string | undefined): void {
    if (!this.delegateFrameInScope(frame)) return;
    if (this.bootGenerationRetired(bootGeneration)) return;
    // Adopt a live frame stamp on first sight or when it proves the same owner
    // advanced (retiresBootGeneration in reverse), before merging — otherwise a
    // replacement daemon's older page can wipe a wait its first frame published.
    const frameBoot = liveBootGeneration(bootGeneration);
    if (
      frameBoot !== undefined &&
      (this.delegateBootGeneration === undefined || retiresBootGeneration(frameBoot, this.delegateBootGeneration))
    ) {
      this.adoptBootGeneration(frameBoot);
    }
    const rows = this.state.delegates.rows;
    if (frame.childRef === undefined) {
      // A legacy frame carries no identity, so everything but its heartbeat
      // needs the read the revision ladder asks for.
      this.mergeLegacyDelegateActivity(frame);
      this.noteUnknownDelegate(frame, undefined, bootGeneration);
      return;
    }
    let index = -1;
    if (frame.childRef !== undefined) {
      // The two fields are the identity the read row keys on; comparing them
      // directly is the same match the string identity encodes, without
      // building a string per row scanned.
      for (let at = 0; at < rows.length; at += 1) {
        const row = rows[at];
        if (row && row.delegateId === frame.delegateId && row.childRef === frame.childRef) {
          index = at;
          break;
        }
      }
      if (index !== -1 && this.delegateEpoch() === undefined) {
        // The frame matched a retained row before any read established the
        // ordering's epoch: recording the merge under an undefined epoch would
        // let the pending first response treat the row as first sight and
        // overwrite the frame with a snapshot that predates it. The frame is
        // buffered instead, and the response's settlement reconciles it with
        // the row it served.
        this.bufferDelegateFrame(frame, delegateIdentityKey(frame.delegateId, frame.childRef), bootGeneration);
        return;
      }
    }
    if (index === -1) {
      this.noteUnknownDelegate(frame, delegateFrameIdentity(frame), bootGeneration);
      return;
    }
    const current = rows[index];
    if (!current) return;
    const { row: merged, holdFromFrame } = this.mergeDelegateFrame(current, frame, bootGeneration);
    // The attribution names the sample the joined row wears now, not the frame:
    // a row sample that outranked this frame's keeps the attribution it was
    // loaded under -- the frame's generation for a stamped frame, which is the
    // one the row's own sample named for it to have been comparable at all, and
    // for an unstamped one the activity epoch both sides belong to.
    //
    // Recorded before the unchanged-row return below: a frame whose sample is
    // already displayed still names the generation that published it, which is
    // what the next adoption retires. An unchanged wait emits no update of its
    // own, so skipping the attribution here would leave a page's attribution on
    // a frame's sample forever.
    const identity = delegateIdentityKey(current.delegateId, current.childRef);
    if (holdFromFrame) this.recordHoldBootGeneration(identity, bootGeneration);
    if (merged === current) return;
    const next = rows.slice();
    next[index] = merged;
    this.recordAppliedDelegate(merged, this.delegateEpoch());
    this.publish({ delegates: { ...this.state.delegates, rows: next } });
  }
  /** A legacy frame cannot name its row, so its snapshot waits for the read
   * its id's revision ladder asks for. Its delegate id, when the loaded set
   * is the whole collection and holds exactly one row under it, still names
   * one child -- and latestActivityAt is the independent maximum every join
   * takes, so the heartbeat can land directly: the producer's
   * ReportActivityPhase advances activity without touching the durable
   * projection revision, so the equal-revision ladder would otherwise drop a
   * streaming legacy delegate's heartbeat and leave its row reading as quiet.
   * A paged set cannot prove which child under the id the heartbeat belongs
   * to, so anything short of a complete set, an ambiguous or absent id, or an
   * ordering with no established epoch -- or a heartbeat timestamp the
   * projector cannot parse -- is left to the read. */
  private mergeLegacyDelegateActivity(frame: EvenerDelegateInfo): void {
    if (this.delegateEpoch() === undefined) return;
    if (!this.state.delegates.complete) return;
    const rows = this.state.delegates.rows;
    let at = -1;
    for (let index = 0; index < rows.length; index += 1) {
      const row = rows[index];
      if (!row || row.delegateId !== frame.delegateId) continue;
      // Two rows under the id leave the heartbeat unattributable, exactly as
      // the frame's other fields: neither takes it.
      if (at !== -1) return;
      at = index;
    }
    if (at === -1) return;
    const current = rows[at];
    // The modern frame's ingress (delegateRowFromFrame) admits a timestamp
    // only when the projector can parse it; the legacy heartbeat must carry
    // the same guarantee before it takes a row. The blank-current rule of
    // activityAfter would otherwise accept any nonblank candidate on a row
    // with no activity, and once such a value stuck, no later heartbeat could
    // compare as later against a timestamp that cannot parse, while the
    // equal-revision ladder asks no read. Refuse only the direct merge: the
    // frame's ladder read still runs, and the row keeps the activity it had.
    if (frame.latestActivityAt === undefined || !isValidActivityTimestamp(frame.latestActivityAt)) {
      return;
    }
    if (!current || !activityAfter(frame.latestActivityAt, current.latestActivityAt)) return;
    const next = rows.slice();
    next[at] = { ...current, latestActivityAt: frame.latestActivityAt };
    this.publish({ delegates: { ...this.state.delegates, rows: next } });
  }
  /** Hold the latest frame for an identity no loaded row can take, until a
   * settled read can place it. Frames can arrive out of order, so a later
   * arrival must not replace a newer settlement: keep the projector's join,
   * exactly as a loaded row does. Re-inserting moves the identity to the end,
   * so eviction drops the coldest identity rather than one a frame just
   * refreshed. */
  private bufferDelegateFrame(frame: EvenerDelegateInfo, identity: string, bootGeneration: string | undefined): void {
    const buffered = this.bufferedUnknownDelegates.get(identity);
    if (buffered) this.bufferedUnknownDelegates.delete(identity);
    // Two samples order against each other only when the same generation
    // published both: a buffer entry from another daemon's boot numbers its
    // holds from its own start, so the older entry is dropped rather than
    // joined. An omission names no generation, and adds nothing either way.
    const samePublisher = buffered !== undefined && buffered.bootGeneration === bootGeneration;
    this.bufferedUnknownDelegates.set(identity, {
      frame: boundedDelegateFrame(samePublisher ? mergeDelegateFrames(buffered.frame, frame) : frame),
      bootGeneration: bootGeneration ?? buffered?.bootGeneration,
    });
    if (this.bufferedUnknownDelegates.size > MAX_BUFFERED_DELEGATE_FRAMES) {
      const oldest = this.bufferedUnknownDelegates.keys().next();
      if (!oldest.done) {
        this.bufferedUnknownDelegates.delete(oldest.value);
        // The evicted identity must be readable again: leaving it in the seen
        // set would let a delegate whose read never admitted it stay
        // undiscovered forever, with later frames updating a buffer that no
        // read will use.
        this.seenUnknownDelegates.delete(oldest.value);
        // The frame never reached a row, so the identity must be able to ask
        // again, and a walk in flight holds the recovery read until it
        // finishes.
        this.evictedDelegateFrame = true;
      }
    }
  }
  /** The frame named a row identity the loaded set does not carry -- or, from
   * an older producer, no identity at all: the row cannot be invented, so a
   * read is the only place the frame's change can land. A frame with an
   * identity is buffered for a read to admit; a walk in flight settles the
   * frames for the rows it can serve, so it is left to finish, and a frame for
   * a row the walk's frozen membership cannot reach is owed one coalesced read
   * once the walk ends. A legacy frame carries nothing a walk could settle, so
   * its read is queued even mid-walk: the drain finishes the walk first, then
   * the queued read re-serves the collection with the frame's change. */
  private noteUnknownDelegate(
    frame: EvenerDelegateInfo,
    identity: string | undefined,
    bootGeneration: string | undefined,
  ): void {
    const read = this.reads.delegates;
    if (read.observers === 0 && !read.oneShot) return;
    if (identity !== undefined) {
      this.bufferDelegateFrame(frame, identity, bootGeneration);
      // A fresh walk in flight reaches the displayed boundary and settles the
      // buffered frames it can serve, so it is left to finish: asking again per
      // frame would restart that walk forever and never settle the promise it
      // carries. A walk also freezes its membership before it starts (the hub
      // holds one highwater per walk), so a frame for a row created after that
      // is served by no page the walk returns. Its identity's one read is
      // marked asked here -- the per-identity bound holds across walks, or an
      // identity no walk can admit would arm a fresh follow-up on every walk
      // it frames during -- and the read itself is coalesced across
      // identities and issued once the walk is over.
      if (read.inFlight !== null && read.refresh !== null) {
        if (!this.seenUnknownDelegates.has(identity)) {
          this.seenUnknownDelegates.add(identity);
          this.followUpDelegateRead = true;
        }
        return;
      }
      if (this.seenUnknownDelegates.has(identity)) return;
      this.seenUnknownDelegates.add(identity);
      void this.request("delegates", "root");
      return;
    }
    // A legacy frame names no row, so only a read can land its change: the
    // first frame for an id asks once, a strictly newer revision asks again --
    // a terminal frame after a running one is newer news, and the row it
    // would leave stale is the one the first read served -- and an advancing
    // repeat at the same revision asks for one paced, coalesced read: the
    // producer's activity phase moves no revision a ladder could ask with,
    // and a paged roster cannot take the heartbeat directly.
    const revision = revisionOf(frame.projectionRevision);
    const asked = this.legacyDelegateReadRevisions.get(frame.delegateId);
    if (asked !== undefined && asked >= revision) {
      if (read.timer === null && this.legacyHeartbeatAdvances(frame)) {
        this.schedule("delegates", DELEGATE_HEARTBEAT_READ_PACE_MS);
      }
      return;
    }
    this.legacyDelegateReadRevisions.set(frame.delegateId, revision);
    void this.request("delegates", "root");
  }
  /** Whether a legacy heartbeat's activity moves past every loaded row under
   * its id. Demand only, never a landing: a paged set cannot prove which row
   * a heartbeat belongs to, so this decides whether the collection is owed a
   * read, and the read's join owns where the activity lands. */
  private legacyHeartbeatAdvances(frame: EvenerDelegateInfo): boolean {
    let current: string | undefined;
    for (const row of this.state.delegates.rows) {
      if (row.delegateId !== frame.delegateId) continue;
      if (current === undefined || activityAfter(row.latestActivityAt, current)) current = row.latestActivityAt;
    }
    return activityAfter(frame.latestActivityAt, current);
  }
  /** The projector's join: the strictly greater revision supplies the snapshot
   * fields, while latestActivityAt is the independent maximum -- a lower
   * revision may advance it, a higher one never moves it backward.
   *
   * `holdFromFrame` reports whose sample the joined row wears, so the caller
   * attributes exactly the sample the row now carries: a sample that beat the
   * frame's keeps the attribution it was loaded under. */
  private mergeDelegateFrame(
    current: SessionDelegate,
    frame: EvenerDelegateInfo,
    bootGeneration: string | undefined,
  ): { row: SessionDelegate; holdFromFrame: boolean } {
    const incoming = delegateRowFromFrame(current, frame);
    const identity = rowIdentity("delegates", current);
    const applied = this.appliedRevision(identity);
    // No record for the current epoch means the map was retired (or this row was
    // never recorded): the loaded row's revision belongs to a retired journal, so
    // comparing against it would reject the replacement's frames, which rebuild
    // from 1. The frame is first sight for this epoch, as a read's row is.
    const incomingWins = applied === undefined || revisionOf(incoming.projectionRevision) > applied;
    const merged = joinDelegateState(incoming, current, incomingWins);
    // The drain hold is its own facet, outside the revision ladder: entering or
    // leaving the drain moves no durable delegate field, so both edges arrive
    // at the revision the row already carries. A frame is the only ordered
    // carrier of that fact, so it always supplies it -- while every other field
    // keeps the ordering above.
    // Its `holdSequence` is the ordering, and it is compared against the row we
    // are merging into — never against the durable winner, which may be this
    // very frame: a higher revision does not mean a newer hold sample.
    //
    // A retired record means the ordering has left the epoch this row's
    // sequence was assigned in: a replacement daemon numbers its samples from
    // its own start, so a larger retired sequence would suppress a real wait,
    // or keep a finished one visible. The frame is first sight for this epoch,
    // exactly as it is for the durable fields above. The same holds for the
    // boot generation: only a sample the row and the frame both attribute to
    // one generation can order against the other.
    const holdComparable =
      applied === undefined || !this.holdsComparable(identity, bootGeneration) ? undefined : current;
    const held = joinDelegateHold(merged, holdComparable, frame);
    return {
      row: delegateRowsEqual(held.row, current) ? current : held.row,
      holdFromFrame: held.holdFromFrame,
    };
  }
  private joinDelegateRows(
    incoming: readonly SessionDelegate[],
    previous: readonly SessionDelegate[],
    served: ReadonlySet<string>,
  ): SessionDelegate[] {
    const previousById = new Map(previous.map((row) => [rowIdentity("delegates", row), row]));
    // A response naming an epoch the ordering has left is late, not news: its
    // rows carry a retired journal's revisions, so they cannot judge the
    // current epoch's applied records -- a late row must not out-rank a frame
    // the announcement window applied -- nor overwrite those records, or the
    // replacement's own read would treat the frame as first sight and clobber
    // it with a snapshot that predates it.
    const currentOrdering = this.reads.delegates.epoch === this.delegateEpoch();
    return incoming.map((servedRow) => {
      // The served row rides the same bound as a frame.
      const row = inadmissibleIntent(servedRow.latestIntent) ? { ...servedRow, latestIntent: undefined } : servedRow;
      const identity = rowIdentity("delegates", row);
      const existing = previousById.get(identity);
      const applied = this.appliedRevision(identity);
      let merged: SessionDelegate;
      // Whether the row ends up wearing the sample it already carried. A sample
      // the row already wore can win the ordering below, and then the row keeps
      // that sample's own attribution.
      let keptHold = false;
      let comparable = false;
      const existingSequence = existing?.holdSequence ?? 0;
      const servedSequence = row.holdSequence ?? 0;
      if (applied === undefined) {
        // First sight in this epoch -- or the first read after a replacement
        // cleared the map: the served row seeds the order and wins, so an old
        // epoch's higher revision cannot regress it.
        merged = row;
      } else {
        // A served row is an authoritative snapshot within the ordering's own
        // epoch: it wins at equal revision (parity with a same-revision frame)
        // and only loses to a strictly newer applied frame, which it must not
        // clobber. A late response's row is not authoritative here.
        const candidate = joinDelegateState(
          row,
          existing ?? row,
          currentOrdering && revisionOf(row.projectionRevision) >= applied,
        );
        // A snapshot inside a read carries the hold as of the read's issue, and
        // a frame that touched this identity since then carries a later one. The
        // source's sequence decides between them, not the order they arrived
        // in: the row keeps whichever sample the producer took later.
        //
        // Only within one source epoch, though. A retained row's sequence was
        // assigned by the daemon that has since been replaced, and its numbers
        // restart at 1, so comparing across the boundary would carry a finished
        // wait forward — or suppress a real one. A row the ordering no longer
        // covers contributes no sample.
        comparable = this.holdsComparable(identity, this.delegateBootGeneration);
        keptHold =
          existing !== undefined &&
          comparable &&
          currentOrdering &&
          (existing.holdSequence ?? 0) > (candidate.holdSequence ?? 0);
        const resolved = keptHold && existing ? keepDelegateHold(candidate, existing) : candidate;
        merged = existing && delegateRowsEqual(resolved, existing) ? existing : resolved;
      }
      // Only a row this response served carries this response's journal: a row
      // it did not serve was retained from an earlier epoch, and recording it
      // here would let its old-epoch revision reject the replacement's row for
      // the same id. An unrecorded row is first sight for the new epoch, as a
      // replacement's own rows are. A late response records nothing either: its
      // records would belong to an epoch the ordering has already left.
      if (served.has(identity) && currentOrdering) {
        this.recordAppliedDelegate(merged, this.reads.delegates.epoch);
        // A row this response served wears this page's hold sample unless the
        // one it already wore won, and a page is attributed to the reading
        // source -- the ordering's transition count -- rather than to the
        // transcript's boot generation: only one of the two sides knows a
        // replacement happened, and it need not be the transcript's. A late
        // response is not the source's news and attributes nothing.
        //
        // One page re-attributes nothing, though: a page repeating the very
        // sample the row already wears changes no fact about whose sample it
        // is. When a frame stamped that sample, the stamp survives the repeat,
        // because re-attributing it to the page would turn it into an
        // unresolved page sample -- kept through the next generation adoption
        // and re-read instead of retired with the daemon that published it.
        const repeatedStampedHold =
          existing !== undefined &&
          comparable &&
          existingSequence === servedSequence &&
          this.delegateHoldBootGenerations.has(identity);
        if (!keptHold && !repeatedStampedHold && hasDelegateHoldSample(merged)) this.recordPageHold(identity);
      }
      return merged;
    });
  }
  /** Place the frames buffered for identities no loaded row could take -- or
   * for a row a frame matched before any read established the ordering's
   * epoch. A page that admits a row under the frame's identity places it on
   * exactly that row, settled or not: the frame names its row directly, and
   * the applied record protects the merge against every later page of the
   * same read. An identity no loaded row carries keeps waiting: no other row
   * may take its place. */
  private settleBufferedFrames(): void {
    if (this.bufferedUnknownDelegates.size === 0) {
      // No buffered frame is owed a deferred read: the walk that held them
      // placed every one it carried.
      this.followUpDelegateRead = false;
      return;
    }
    const rows = this.state.delegates.rows as readonly SessionDelegate[];
    const loadedByIdentity = new Map<string, number>();
    for (let at = 0; at < rows.length; at += 1) {
      const row = rows[at];
      if (row) loadedByIdentity.set(rowIdentity("delegates", row), at);
    }
    let settledRows: SessionDelegate[] | null = null;
    for (const [identity, buffered] of this.bufferedUnknownDelegates) {
      const index = loadedByIdentity.get(identity);
      if (index === undefined) continue;
      this.bufferedUnknownDelegates.delete(identity);
      this.seenUnknownDelegates.delete(identity);
      const current = rows[index];
      if (!current) continue;
      const { row: merged, holdFromFrame } = this.mergeDelegateFrame(current, buffered.frame, buffered.bootGeneration);
      // Same rule as the pushed-frame merge: the winning sample's attribution
      // is recorded even when the displayed row is unchanged. A buffered frame
      // repeats what a page already showed often -- it waited for a read to
      // admit its row -- and that is exactly the case where the frame's stamp
      // is the only evidence of which daemon published what is displayed.
      if (holdFromFrame) this.recordHoldBootGeneration(identity, buffered.bootGeneration);
      if (merged === current) continue;
      if (!settledRows) settledRows = rows.slice();
      settledRows[index] = merged;
      this.recordAppliedDelegate(merged, this.delegateEpoch());
    }
    if (!settledRows) return;
    if (this.bufferedUnknownDelegates.size === 0) this.followUpDelegateRead = false;
    this.publish({ delegates: { ...this.state.delegates, rows: settledRows } });
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
          this.publish({
            runtime: this.seedEscalations({
              ...metadata,
              status: this.statusUpdate.status,
              ...(notification.params.askPending === undefined ? {} : { askPending: notification.params.askPending }),
            }),
          });
        return;
      }
      case "evener/sandbox/escalation/requested":
      case "evener/sandbox/escalation/resolved": {
        const runtime = this.state.runtime;
        if (!runtime || notification.params.threadId !== runtime.threadId) return;
        if (notification.method === "evener/sandbox/escalation/requested")
          this.pendingEscalations.add(notification.params.escalationId);
        else this.pendingEscalations.delete(notification.params.escalationId);
        const ids = [...this.pendingEscalations].sort();
        this.publish({
          runtime: { ...runtime, approvalPending: ids.length > 0, pendingEscalationIds: ids },
        });
        return;
      }
      case "evener/delegate/updated":
        // Only an eligible context enables the no-read merge. An older or
        // retained source keeps today's behavior: the activity-changed
        // invalidation re-reads the collection, so reports still land.
        if (!this.pushEligible) return;
        // The frame carries the generation its own publisher booted under
        // -- a peer too old to stamp it sends nothing about identity.
        this.applyDelegateFrame(notification.params.delegate, notification.params.bootGeneration);
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
      case "evener/thread/resync": {
        for (const resource of resources) {
          this.reads[resource].refresh = null;
          this.cancelPace(this.reads[resource]);
        }
        // The server may already have emitted a reply before the alias was
        // cleared. It must not publish after this resync boundary.
        this.generation += 1;
        this.runtimeThreadId = notification.params.threadId;
        // The recovery thread read is newer than any status cached before the
        // interruption, even when it names the same thread.
        this.statusUpdate = null;
        // The previous source's summary failure cannot describe the recovered
        // connection. Clearing it here also keeps a recovered runtime publish
        // from carrying a stale error across the resync boundary.
        if (this.state.summaryState.error !== null)
          this.change("summary", { error: null, unavailable: false, permanent: false });
        const knownThreadId = this.state.runtime?.threadId ?? this.lease?.metadata()?.threadId ?? null;
        const replaced = knownThreadId !== null && knownThreadId !== this.runtimeThreadId;
        if (replaced) this.publish({ runtime: null });
        // A relay interruption can hide a resolution or a new question. For
        // the same thread, the recovery thread read is authoritative even when
        // activity reads are still failing; retry it rather than leaving
        // metadata stale. An activity-only scope retries activity via
        // lease.ensure() without refreshing metadata, so this read cannot
        // depend on summary success. A resync naming a replacement thread
        // retires the old runtime instead: that subscription describes the
        // thread being left behind.
        if (!replaced && this.lease) {
          const lease = this.lease;
          const generation = this.generation;
          // Let every notification listener for this resync run first: the
          // subscription registry advances its resync revision there, so the
          // refresh read is issued against the new boundary.
          void Promise.resolve().then(() => this.refreshThreadMetadata(lease, generation));
        }
        // A resync re-establishes the source identity, so the pushed-frame merge
        // waits for the refreshed context as it does across a reconnect. Without
        // this a frame racing the resync's delegate read merges against the epoch
        // that read is about to replace, and the read's own rows then clear it,
        // leaving the row at the snapshot the settle predates.
        this.pushEligible = false;
        this.clearDelegates();
        for (const resource of resources) this.reads[resource].announcedEpoch = undefined;
        changed = resources;
        break;
      }
      default:
        return;
    }
    // The no-read merge covers delegate field changes; summary, jobs and
    // watches refresh as today, and the merge holds only while this invalidation
    // refreshes one of them. A resync still refreshes every observed resource,
    // delegates included.
    const skipDelegates =
      notification.method === "evener/thread/activity/changed" &&
      this.pushEligible &&
      this.refreshesAnotherResource(changed);
    for (const resource of changed) {
      if (resource === "delegates" && skipDelegates) continue;
      if (this.reads[resource].observers > 0 || this.reads[resource].oneShot) {
        void this.request(resource, "root", notification.method === "evener/thread/resync");
      }
    }
  }

  /** Keep the watcher's local escalation set aligned with the metadata a read
   * or status frame supplies. A source that predates pendingEscalationIds
   * cannot name them, so the frame-updated set is left alone. */
  private seedEscalations(metadata: ThreadSubscriptionMetadata | null): ThreadSubscriptionMetadata | null {
    if (!metadata) {
      this.pendingEscalations.clear();
      return null;
    }
    if (metadata.pendingEscalationIds === undefined) return metadata;
    this.pendingEscalations.clear();
    for (const id of metadata.pendingEscalationIds) this.pendingEscalations.add(id);
    return metadata;
  }

  private refreshThreadMetadata(lease: ThreadSubscriptionLease, generation: number, attempt = 0): void {
    if (this.disposed || this.lease !== lease) return;
    if (generation !== this.generation) {
      const next = this.generation;
      void Promise.resolve().then(() => this.refreshThreadMetadata(lease, next));
      return;
    }
    void lease
      .read({ includeTurns: false })
      .then((response) => {
        if (this.disposed || this.lease !== lease) return;
        if (generation !== this.generation) {
          const next = this.generation;
          void Promise.resolve().then(() => this.refreshThreadMetadata(lease, next));
          return;
        }
        const metadata = lease.metadata();
        if (!metadata) return;
        if (this.state.context && this.state.context.sessionId !== metadata.sessionId) return;
        if (this.runtimeThreadId !== null && metadata.threadId !== this.runtimeThreadId) return;
        // Relay recovery can hand this same connected client a replacement
        // daemon, and this lean read is the first response that names it. The
        // adopted generation is what lets a page the replacement served and a
        // frame it stamped order against each other: without it the page is
        // not comparable to the frame, and an older page sample overwrites a
        // newer wait the replacement published -- which nothing restores,
        // since an unchanged hold emits no update of its own. `liveBootGeneration`
        // keeps a saved-history `daemonless` success from displacing the live
        // source this store already holds.
        this.adoptBootGeneration(liveBootGeneration(response.bootGeneration));
        this.publish({ runtime: this.seedEscalations(metadata) });
      })
      .catch((error: unknown) => {
        if (this.disposed || this.lease !== lease || permanentError(error)) return;
        if (generation !== this.generation) {
          const next = this.generation;
          void Promise.resolve().then(() => this.refreshThreadMetadata(lease, next));
          return;
        }
        const delay = Math.min(1000 * 2 ** Math.min(attempt, 5), 30000);
        this.clock.setTimeout(() => this.refreshThreadMetadata(lease, generation, attempt + 1), delay);
      });
  }
  /** Whether this invalidation refreshes a resource other than delegates, so an
   * accepted response can carry a capability or availability change back to this
   * store. Suppressing the invalidation-driven delegate read assumes one can: a
   * store that observes no other collection, one whose other resource is absent
   * from this invalidation, one whose only other demand is an explicit read
   * nobody retries, and one whose other read has permanently failed all still
   * have no way to learn that their ancestor released the runtime, which sends
   * invalidations and no frames. */
  private refreshesAnotherResource(changed: readonly SessionActivityResource[]): boolean {
    return changed.some(
      (resource) =>
        resource !== "delegates" && this.reads[resource].observers > 0 && !this.readState(resource).permanent,
    );
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

/** An intent the wire's bound admits -- a string of at most
 * MAX_INTENT_CODE_POINTS code points, astral characters counted once, the
 * same walk the S5 decoder applies -- or undefined. Absent, wrong-typed, and
 * over-bound values all read as omitted: the S5 decoder drops a session entry
 * that carries one, but a row or frame keeps every other field, so a bad
 * value costs only the line itself. */
function boundedIntent(value: unknown): string | undefined {
  return typeof value === "string" && intentWithinBound(value) ? value : undefined;
}
/** Whether value is present but not admissible — the one case both strip sites
 * read the same way: an over-bound or invalid line applies as omitted, and
 * the row or frame keeps every other field. */
function inadmissibleIntent(value: string | undefined): boolean {
  return value !== undefined && boundedIntent(value) === undefined;
}
/** A winning frame supplies every mutating field, absent optionals included,
 * so it clears what it omits; only the loaded row's identity survives: `name`
 * is immutable per delegate, the row's refs are the read's own, and the frame's
 * child ref has already proven it names this row. */
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
    needsAttention: frame.needsAttention === true ? true : undefined,
    notResumableReason: frame.notResumableReason,
    model: frame.model,
    reasoningEffort: frame.reasoningEffort,
    runStartedAt: frame.runStartedAt,
    runEndedAt: frame.runEndedAt,
    // The projector parses RFC3339Nano, so a value its reader could never return
    // must not enter the row: unorderable, it would stick there and drift from
    // what the read path can produce.
    latestActivityAt:
      frame.latestActivityAt !== undefined && activityInstant(frame.latestActivityAt)
        ? frame.latestActivityAt
        : undefined,
    latestIntent: boundedIntent(frame.latestIntent),
    // The frame carries the same live wait the read row does, so a frame that
    // wins the merge replaces it with the truth rather than erasing it.
    drainHeldByJobs: frame.drainHeldByJobs,
    runningJobCount: frame.runningJobCount,
    oldestRunningJobStartedAt: frame.oldestRunningJobStartedAt,
    drainHeldBySubagents: frame.drainHeldBySubagents,
    runningSubagentCount: frame.runningSubagentCount,
    holdSequence: frame.holdSequence,
    usage: frame.usage,
    worktree: frame.worktree,
  };
}
export function isValidActivityTimestamp(value: string): boolean {
  return activityInstant(value) !== null;
}

/** The drain-hold facet of a row and a frame, ordered by `holdSequence` from
 * the two original samples. Durable fields order by projection revision; the
 * hold orders by sequence within the source epoch. Read delegateFacetOrder in
 * internal/appprojector/appwire_projection.go for why they are separate.
 *
 * A newer sequence is a change even when the displayed fields match: a client
 * that saw an intervening hold needs this sample's sequence to clear it. An
 * exact duplicate stays suppressed. A producer that predates the field sends
 * none, and both sides read 0, so its frames keep the old behaviour.
 *
 * `previous` is undefined when the row's sample belongs to a retired epoch and
 * cannot be compared: the frame then supplies its own hold, as it does for
 * every durable field.
 *
 * The sample that wins supplies every field the hold owns -- both hold flags,
 * both counts, and the oldest job start -- taken from itself alone. A cleared
 * sample omits the counts and the oldest start (`omitempty`), so reading a
 * missing field from the other side would restore exactly the wait the
 * sequence just rejected -- and `row` is not a second source here, because the
 * durable merge may already have made it the losing frame. */
function joinDelegateHold(
  row: SessionDelegate,
  previous: SessionDelegate | undefined,
  frame: EvenerDelegateInfo,
): { row: SessionDelegate; holdFromFrame: boolean } {
  const currentSequence = previous?.holdSequence ?? 0;
  const frameSequence = frame.holdSequence ?? 0;
  if (previous !== undefined && currentSequence > frameSequence) {
    return { row: keepDelegateHold(row, previous), holdFromFrame: false };
  }
  if (
    (row.drainHeldByJobs ?? false) === (frame.drainHeldByJobs ?? false) &&
    row.runningJobCount === frame.runningJobCount &&
    row.oldestRunningJobStartedAt === frame.oldestRunningJobStartedAt &&
    (row.drainHeldBySubagents ?? false) === (frame.drainHeldBySubagents ?? false) &&
    row.runningSubagentCount === frame.runningSubagentCount &&
    row.holdSequence === frame.holdSequence
  ) {
    return { row, holdFromFrame: true };
  }
  return {
    row: {
      ...row,
      drainHeldByJobs: frame.drainHeldByJobs,
      runningJobCount: frame.runningJobCount,
      oldestRunningJobStartedAt: frame.oldestRunningJobStartedAt,
      drainHeldBySubagents: frame.drainHeldBySubagents,
      runningSubagentCount: frame.runningSubagentCount,
      holdSequence: frame.holdSequence,
    },
    holdFromFrame: true,
  };
}

/** A row wearing the hold another row already carries, for the fields the
 * hold owns. Used where the hold is newer than the snapshot being applied. */
function keepDelegateHold(row: SessionDelegate, holds: SessionDelegate): SessionDelegate {
  if (
    (row.drainHeldByJobs ?? false) === (holds.drainHeldByJobs ?? false) &&
    row.runningJobCount === holds.runningJobCount &&
    row.oldestRunningJobStartedAt === holds.oldestRunningJobStartedAt &&
    (row.drainHeldBySubagents ?? false) === (holds.drainHeldBySubagents ?? false) &&
    row.runningSubagentCount === holds.runningSubagentCount &&
    row.holdSequence === holds.holdSequence
  ) {
    return row;
  }
  return {
    ...row,
    drainHeldByJobs: holds.drainHeldByJobs,
    runningJobCount: holds.runningJobCount,
    oldestRunningJobStartedAt: holds.oldestRunningJobStartedAt,
    drainHeldBySubagents: holds.drainHeldBySubagents,
    runningSubagentCount: holds.runningSubagentCount,
    holdSequence: holds.holdSequence,
  };
}

/** Whether a row wears a hold sample at all: the sequence, or the fact it
 * phrases. A row that never held carries none, so it is not a source's sample
 * to attribute. */
function hasDelegateHoldSample(row: SessionDelegate): boolean {
  return (
    row.holdSequence !== undefined ||
    row.drainHeldByJobs !== undefined ||
    row.runningJobCount !== undefined ||
    row.oldestRunningJobStartedAt !== undefined ||
    row.drainHeldBySubagents !== undefined ||
    row.runningSubagentCount !== undefined
  );
}

/** A row with its hold sample removed. A hold belongs to the daemon that
 * sampled it, so once that daemon is replaced the sample is not the
 * replacement's to display: the row keeps its durable fields and reports no
 * wait until the new source says otherwise. */
function withoutDelegateHold(row: SessionDelegate): SessionDelegate {
  if (!hasDelegateHoldSample(row)) return row;
  return {
    ...row,
    drainHeldByJobs: undefined,
    runningJobCount: undefined,
    oldestRunningJobStartedAt: undefined,
    drainHeldBySubagents: undefined,
    runningSubagentCount: undefined,
    holdSequence: undefined,
  };
}

/** The frame fields a row merge can apply. The raw report payload and the model
 * preferences a row does not carry are not part of the row, and a buffered frame
 * waits for a read to admit the row, so keeping them would let 128 buffered
 * entries hold unbounded bytes -- the entry cap bounds identities, not memory. */
function boundedDelegateFrame(frame: EvenerDelegateInfo): EvenerDelegateInfo {
  const {
    message: _message,
    structuredResult: _structuredResult,
    structuredResultReason: _structuredResultReason,
    resolvedModel: _resolvedModel,
    requestedModel: _requestedModel,
    warnings: _warnings,
    diagnostics: _diagnostics,
    ...bounded
  } = frame;
  // The buffer's entry cap bounds identities, not memory, so an intent past the
  // wire's bound is dropped here rather than held by 128 buffered frames.
  return inadmissibleIntent(bounded.latestIntent) ? { ...bounded, latestIntent: undefined } : bounded;
}
/** The projector's join for two frame snapshots of one delegate: the strictly
 * greater revision supplies the fields, latestActivityAt is the independent
 * maximum. Mirrors `mergeDelegateFrame` for a row not yet loaded. */
function mergeDelegateFrames(current: EvenerDelegateInfo, frame: EvenerDelegateInfo): EvenerDelegateInfo {
  const merged = joinDelegateState(
    frame,
    current,
    revisionOf(frame.projectionRevision) > revisionOf(current.projectionRevision),
  );
  // Both edges of the drain hold arrive at the revision the row already
  // carries, so the ladder above cannot order them. The source's sequence does:
  // each buffered frame keeps the sequence it was received with, so a frame
  // sampled before the buffered one and delivered after it loses rather than
  // re-stamping itself at settle. The later sequence is carried even when the
  // displayed fields match, so a client holding an intervening sample can
  // clear it.
  const newer = (frame.holdSequence ?? 0) < (current.holdSequence ?? 0) ? current : frame;
  return {
    ...merged,
    drainHeldByJobs: newer.drainHeldByJobs,
    runningJobCount: newer.runningJobCount,
    oldestRunningJobStartedAt: newer.oldestRunningJobStartedAt,
    drainHeldBySubagents: newer.drainHeldBySubagents,
    runningSubagentCount: newer.runningSubagentCount,
    holdSequence: newer.holdSequence,
  };
}
/** Every field a delegate row carries, as a keyed set: the `Record` type fails
 * to compile while one is missing, so a field added to the wire row cannot be
 * compared by omission -- which reads as "unchanged" and drops a frame's change
 * on the floor. */
const delegateRowFields: Record<keyof SessionDelegate, true> = {
  delegateId: true,
  name: true,
  ownerRef: true,
  rootRef: true,
  childRef: true,
  runGeneration: true,
  projectionRevision: true,
  reportPreview: true,
  reportPreviewTruncated: true,
  parentDelegateId: true,
  description: true,
  task: true,
  type: true,
  lifecycle: true,
  phase: true,
  status: true,
  outcome: true,
  reason: true,
  error: true,
  terminal: true,
  resumable: true,
  needsAttention: true,
  notResumableReason: true,
  model: true,
  reasoningEffort: true,
  runStartedAt: true,
  runEndedAt: true,
  latestActivityAt: true,
  latestIntent: true,
  drainHeldByJobs: true,
  runningJobCount: true,
  oldestRunningJobStartedAt: true,
  drainHeldBySubagents: true,
  runningSubagentCount: true,
  holdSequence: true,
  usage: true,
  worktree: true,
};
const delegateRowFieldNames = Object.keys(delegateRowFields) as (keyof SessionDelegate)[];
function delegateRowsEqual(a: SessionDelegate, b: SessionDelegate): boolean {
  for (const field of delegateRowFieldNames) {
    if (!sameJsonValue(a[field], b[field])) return false;
  }
  return true;
}
/** The identity a delegate row and its frame share: the (delegateId, childRef)
 * pair, encoded once for both. A frame buffered for a row no read has admitted
 * is looked up by this key when a settled read places it, so the row and frame
 * sides must build the same key by construction, never by copy. */
function delegateIdentityKey(delegateId: string, childRef: string): string {
  return `${delegateId}\u0000${childRef}`;
}
function rowIdentity(resource: SessionActivityCollection, row: ActivityRow): string {
  if (resource === "delegates") {
    // A read can serve one delegate id twice (a child it served again), and the
    // walk, the boundary and the merge order must key on the same pair the read
    // preserves or one served row replaces the other.
    const delegate = row as SessionDelegate;
    return delegateIdentityKey(delegate.delegateId, delegate.childRef);
  }
  if (resource === "jobs") {
    const job = row as JobActivityJob;
    return JSON.stringify([job.ownerRef, job.jobId]);
  }
  const watch = row as SessionWatch;
  return JSON.stringify([watch.receiverRef, watch.watch.id]);
}
/** The row identity a frame names: the same (delegateId, childRef) pair the
 * read keys on, when the frame carries its child ref. A frame from an older
 * producer carries none and names no row (#4258). */
function delegateFrameIdentity(frame: EvenerDelegateInfo): string | undefined {
  return frame.childRef === undefined ? undefined : delegateIdentityKey(frame.delegateId, frame.childRef);
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
