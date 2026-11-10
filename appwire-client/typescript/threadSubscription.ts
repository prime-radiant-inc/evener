import type { AppwireClientLike } from "./clientLike";
import type { AnyNotification, ThreadReadParams, ThreadReadResponse, ThreadStatus } from "./types.gen";

export type ThreadSubscriptionClient = Pick<
  AppwireClientLike,
  "request" | "onNotification" | "onStateChange" | "state"
>;
export type ThreadSubscriptionReadParams = Omit<ThreadReadParams, "ref" | "subscribe" | "replaceSubscription">;
export interface ThreadSubscriptionMetadata {
  readonly threadId: string;
  readonly sessionId: string;
  readonly status: ThreadStatus;
  /** A pending question is actionable attention even while the status rests. */
  readonly askPending?: boolean;
  /** A pending sandbox escalation is actionable attention. */
  readonly approvalPending?: boolean;
  /** The escalations still pending, so multiple approvals clear one at a time. */
  readonly pendingEscalationIds?: readonly string[];
}
export interface ThreadSubscriptionLease {
  ensure(): Promise<void>;
  read(params: ThreadSubscriptionReadParams): Promise<ThreadReadResponse>;
  metadata(): ThreadSubscriptionMetadata | null;
  release(): void;
}

function copyStatus(status: ThreadStatus): ThreadStatus {
  return {
    type: status.type,
    ...(status.activeFlags ? { activeFlags: [...status.activeFlags] } : {}),
  };
}

interface Membership {
  ref: string;
  holders: number;
  subscribed: boolean;
  transition: Promise<void> | null;
  // The acquisition whose transition is pending, and the one whose last
  // holder departed while it was still pending. A fresh epoch supersedes a
  // retired one; a held transition (its holders never all departed) keeps
  // joining regardless, preserving the concurrent-mount dedup.
  transitionEpoch: number;
  retiredEpoch: number;
  // Acquisitions still outstanding on the wire, superseded ones included:
  // the membership must not be deleted while any may still land, and a
  // superseded success may be the only thing holding the wire subscription.
  outstandingAcquisitions: number;
  // A superseded acquisition's request succeeded after the current one
  // failed: the wire holds a subscription the membership never recorded,
  // and the next empty release must unsubscribe it.
  orphanedSubscribe: boolean;
  metadata: ThreadSubscriptionMetadata | null;
  // The newest thread/status/changed admitted for this membership and the
  // thread it named. A read issued before that revision carries an older
  // snapshot and must not revert the shared status (issue #4878 PAR).
  statusRevision: number;
  statusThreadId: string;
  // An askPending flag can arrive before the first read admits metadata. Keep
  // the newest one, keyed by its thread, and apply it when that read lands.
  pendingAsk?: { threadId: string; askPending: boolean };
  // True while metadata's askPending is the current answer. A resync or a
  // replacement thread retires it so the recovery snapshot can seed the value;
  // a status frame received during that read sets it again.
  askAuthoritative: boolean;
  // Escalation frames received before or during a read. The snapshot seeds the
  // set and these frame facts overlay it, so a resolved(A) before the response
  // cannot discard a still-pending B from that response.
  requestedEscalations: Set<string>;
  resolvedEscalations: Set<string>;
  pendingEscalations: Set<string>;
}

// All owners of a connection join this membership table. Transcript hydration
// and independent rich reads remain with their callers; only wire membership
// acquisition and release wait for one another.
class ThreadSubscriptions {
  private members = new Map<string, Membership>();
  private generation = 0;
  private stopState: () => void;
  private stopNotifications: () => void;

  constructor(
    private readonly client: ThreadSubscriptionClient,
    // A registry wired by the connection owner lives for that connection: it
    // keeps its listeners while its last member departs, so a later store
    // action does not re-register them (the lazy per-action wiring the
    // threads store's wiring test forbids). A registry created by a bare
    // acquisition retires with its last member.
    private readonly connectionOwned = false,
  ) {
    this.stopState = client.onStateChange((state) => {
      if (state === "ready") return;
      this.generation += 1;
      for (const member of this.members.values()) {
        member.subscribed = false;
        member.transition = null;
        member.metadata = null;
        member.statusRevision = 0;
        member.statusThreadId = "";
        member.pendingAsk = undefined;
        member.askAuthoritative = false;
        member.requestedEscalations.clear();
        member.resolvedEscalations.clear();
        member.pendingEscalations.clear();
        member.orphanedSubscribe = false;
        this.releaseEmpty(member);
      }
    });
    // The shared status is the one view a re-acquired store has before its
    // first read: a released turn's idle frame must land here too, or a
    // reopened viewer falls back to the status the original read admitted
    // (issue #4878).
    this.stopNotifications = client.onNotification((notification) => {
      if (notification.method === "thread/status/changed") this.admitStatus(notification);
      else if (
        notification.method === "evener/sandbox/escalation/requested" ||
        notification.method === "evener/sandbox/escalation/resolved"
      )
        this.admitEscalation(notification);
      else if (notification.method === "evener/thread/resync") this.admitResync(notification);
    });
  }

  private admitStatus(notification: Extract<AnyNotification, { method: "thread/status/changed" }>): void {
    const { ref, threadId, status, askPending } = notification.params;
    const direct = this.members.get(ref);
    if (direct?.metadata) this.admitMemberStatus(direct, threadId, status, askPending);
    else if (direct && askPending !== undefined) {
      direct.statusRevision += 1;
      direct.statusThreadId = threadId;
      direct.pendingAsk = { threadId, askPending };
      direct.askAuthoritative = true;
    }
    for (const member of this.members.values()) {
      if (member !== direct && member.metadata?.threadId === threadId)
        this.admitMemberStatus(member, threadId, status, askPending);
    }
  }

  private admitMemberStatus(
    member: Membership,
    threadId: string,
    status: ThreadStatus,
    askPending: boolean | undefined,
  ): void {
    if (!member.metadata) return;
    member.statusRevision += 1;
    member.statusThreadId = threadId;
    member.pendingAsk = undefined;
    if (askPending !== undefined) member.askAuthoritative = true;
    member.metadata = {
      ...member.metadata,
      status: copyStatus(status),
      ...(askPending === undefined ? {} : { askPending }),
    };
  }

  private admitEscalation(
    notification: Extract<
      AnyNotification,
      {
        method: "evener/sandbox/escalation/requested" | "evener/sandbox/escalation/resolved";
      }
    >,
  ): void {
    const { ref, threadId, escalationId } = notification.params;
    const direct = this.members.get(ref);
    const member = direct ?? [...this.members.values()].find((candidate) => candidate.metadata?.threadId === threadId);
    if (!member) return;
    if (notification.method === "evener/sandbox/escalation/requested") {
      member.requestedEscalations.add(escalationId);
      member.resolvedEscalations.delete(escalationId);
      member.pendingEscalations.add(escalationId);
    } else {
      member.requestedEscalations.delete(escalationId);
      member.resolvedEscalations.add(escalationId);
      member.pendingEscalations.delete(escalationId);
    }
    if (member.metadata) member.metadata = this.approvalMetadata(member, member.metadata);
  }

  private approvalMetadata(member: Membership, metadata: ThreadSubscriptionMetadata): ThreadSubscriptionMetadata {
    const ids = [...member.pendingEscalations].sort();
    return {
      ...metadata,
      ...(ids.length
        ? { approvalPending: true, pendingEscalationIds: ids }
        : { approvalPending: false, pendingEscalationIds: [] }),
    };
  }

  private admitResync(notification: Extract<AnyNotification, { method: "evener/thread/resync" }>): void {
    const { ref, threadId } = notification.params;
    const member =
      this.members.get(ref) ??
      [...this.members.values()].find((candidate) => candidate.metadata?.threadId === threadId);
    if (!member) return;
    // The source is authoritative again: requests from before the interruption
    // may have resolved while the relay was down. Retire both cached answers
    // and re-read the thread; a request/resolution frame arriving after this
    // resync overlays that read.
    member.requestedEscalations.clear();
    member.resolvedEscalations.clear();
    member.askAuthoritative = false;
  }

  acquire(ref: string): ThreadSubscriptionLease {
    let member = this.members.get(ref);
    if (!member) {
      member = {
        ref,
        holders: 0,
        subscribed: false,
        transition: null,
        transitionEpoch: 0,
        retiredEpoch: 0,
        outstandingAcquisitions: 0,
        orphanedSubscribe: false,
        metadata: null,
        statusRevision: 0,
        statusThreadId: "",
        pendingAsk: undefined,
        askAuthoritative: false,
        requestedEscalations: new Set(),
        resolvedEscalations: new Set(),
        pendingEscalations: new Set(),
      };
      this.members.set(ref, member);
    }
    const held = member;
    held.holders += 1;
    let released = false;
    const active = () => {
      if (released) throw new Error("Thread subscription lease has been released");
      if (this.client.state !== "ready") throw new Error("Thread subscription connection is not ready");
    };
    const read = async (params: ThreadSubscriptionReadParams): Promise<ThreadReadResponse> => {
      active();
      // A held transition joins (concurrent first reads share one subscribe);
      // a retired one — whose last holder departed while it was pending — is
      // skipped: this read issues its own subscribe rather than waiting out a
      // request nobody remains to answer for.
      if (held.transition && held.transitionEpoch !== held.retiredEpoch) {
        await held.transition;
        return read(params);
      }
      if (!held.subscribed) return this.acquireWire(held, params);
      const generation = this.generation;
      const statusRevision = held.statusRevision;
      const response = await this.client.request("thread/read", {
        ...params,
        ref,
        subscribe: false,
        replaceSubscription: false,
      });
      if (!released) this.admitMetadata(held, response, generation, statusRevision);
      return response;
    };
    return {
      ensure: async () => {
        active();
        await this.ensure(held);
      },
      read,
      metadata: () => (released || this.client.state !== "ready" ? null : held.metadata),
      release: () => {
        if (released) return;
        released = true;
        held.holders -= 1;
        if (held.holders === 0) {
          held.metadata = null;
          // The last holder departed while the acquisition is still on the
          // wire: no one remains to consume it, and a reopened reader must
          // not queue behind a request whose owner is gone — it supersedes
          // the dead acquisition with its own subscribe instead.
          if (held.transition) held.retiredEpoch = held.transitionEpoch;
        }
        this.releaseEmpty(held);
      },
    };
  }

  private async ensure(member: Membership): Promise<void> {
    // The same retired-acquisition rule as read(): a held transition joins;
    // one whose last holder departed is superseded by a fresh acquisition —
    // a reopened activity must neither wait on the departed request nor
    // inherit its failure.
    if (member.transition && member.transitionEpoch !== member.retiredEpoch) {
      await member.transition;
      if (member.holders > 0) await this.ensure(member);
      return;
    }
    if (member.subscribed || member.holders === 0) return;
    await this.acquireWire(member, { includeTurns: false });
  }

  private acquireWire(member: Membership, params: ThreadSubscriptionReadParams): Promise<ThreadReadResponse> {
    const generation = this.generation;
    const epoch = ++member.transitionEpoch;
    const statusRevision = member.statusRevision;
    member.outstandingAcquisitions += 1;
    const response = this.client.request("thread/read", {
      ...params,
      ref: member.ref,
      subscribe: true,
      replaceSubscription: false,
    });
    const transition = response.then((result) => {
      // Only the CURRENT acquisition may flip membership state: a request
      // superseded by a fresh subscribe (its owner departed, or a newer
      // acquisition replaced it) settles into nothing — except that its
      // success still holds the wire subscription, which nobody else
      // recorded: remember it so the next empty release tears it down.
      // A failure propagates: reads joined on this transition must see it.
      if (generation === this.generation && epoch === member.transitionEpoch) {
        member.subscribed = true;
        this.admitMetadata(member, result, generation, statusRevision);
        return;
      }
      if (generation !== this.generation || member.subscribed) return;
      member.orphanedSubscribe = true;
    });
    // The acquisition's own settle: superseded requests keep teardown
    // ownership until they land, so the membership cannot be deleted out
    // from under a flight that may still subscribe on the wire. The
    // empty-membership cleanup runs after every settle — a stale generation
    // only means the connection changed: releaseEmpty self-gates on live
    // holders and pending transitions, and its wire action is fenced by
    // the ready state, so a stale settle can only complete the table
    // cleanup the disconnect could not (round-21 low).
    const settled = () => {
      member.outstandingAcquisitions -= 1;
      if (member.transition === transition) return;
      this.releaseEmpty(member);
    };
    void transition.then(settled, settled);
    this.track(member, transition, generation);
    return response;
  }

  private admitMetadata(
    member: Membership,
    { thread }: ThreadReadResponse,
    generation: number,
    readStatusRevision: number,
  ): void {
    if (generation !== this.generation || this.client.state !== "ready" || member.holders === 0) return;
    // A status frame that landed after this read was issued is newer than the
    // read's snapshot: keep it rather than letting the late read revert the
    // status a re-acquired store would see.
    const newer = member.statusRevision > readStatusRevision && member.statusThreadId === thread.id;
    // The same rule protects the pending-question flag: a status frame that
    // carried askPending (true or false) is newer than this read's snapshot.
    // Once metadata has an explicit value, later snapshots cannot revert it;
    // a fresh status frame is what changes the question state.
    const earlyAsk = member.pendingAsk?.threadId === thread.id ? member.pendingAsk.askPending : undefined;
    member.pendingAsk = undefined;
    if (member.metadata?.threadId !== thread.id) member.askAuthoritative = false;
    const askPending =
      earlyAsk !== undefined
        ? earlyAsk
        : member.askAuthoritative && member.metadata?.askPending !== undefined
          ? member.metadata.askPending
          : thread.evener?.askPending === true;
    // Seed from the snapshot, then overlay every escalation frame received
    // before this response. Keeping the frame facts also fences later stale
    // reads: a resolved id never comes back, and a requested id a snapshot
    // predates is added back.
    member.pendingEscalations.clear();
    for (const escalation of thread.evener?.pendingEscalations ?? [])
      member.pendingEscalations.add(escalation.escalationId);
    for (const escalationId of member.requestedEscalations) member.pendingEscalations.add(escalationId);
    for (const escalationId of member.resolvedEscalations) member.pendingEscalations.delete(escalationId);
    const approvalIDs = [...member.pendingEscalations].sort();
    const approvalFields = approvalIDs.length ? { approvalPending: true, pendingEscalationIds: approvalIDs } : {};
    member.metadata = {
      threadId: thread.id,
      sessionId: thread.sessionId ?? thread.id,
      status: copyStatus((newer ? member.metadata?.status : undefined) ?? thread.status),
      askPending,
      ...approvalFields,
    };
    member.askAuthoritative = true;
  }

  private track(member: Membership, transition: Promise<void>, generation: number): void {
    member.transition = transition;
    const settled = () => {
      if (generation !== this.generation || member.transition !== transition) return;
      member.transition = null;
      this.releaseEmpty(member);
    };
    void transition.then(settled, settled);
  }

  private releaseEmpty(member: Membership): void {
    if (member.holders > 0 || member.transition || member.outstandingAcquisitions > 0) return;
    if ((member.subscribed || member.orphanedSubscribe) && this.client.state === "ready") {
      member.subscribed = false;
      member.orphanedSubscribe = false;
      // Local release is final. Failed wire cleanup is contained by the
      // server's connection-close cleanup; a new owner always re-subscribes.
      const transition = this.client.request("thread/unsubscribe", { ref: member.ref }).then(
        () => {},
        () => {},
      );
      this.track(member, transition, this.generation);
      return;
    }
    if (this.members.get(member.ref) === member) this.members.delete(member.ref);
    if (!this.connectionOwned && this.members.size === 0) this.retire();
  }

  /** The connection owner is letting go of the client: every member goes with
   * it, and its handlers stop. The status feed goes first and the connection
   * listener last, so a caller observing the connection listener's release
   * sees the retired registry's final handler leave with it. */
  retire(): void {
    this.members.clear();
    this.stopNotifications();
    this.stopState();
    registries.delete(this.client);
  }
}

const registries = new WeakMap<ThreadSubscriptionClient, ThreadSubscriptions>();

/** Wire a connection's shared subscription registry at connect time, before
 * any thread is acquired: the registry's state/notification handlers belong
 * to the connection, not to whichever store action happens to subscribe
 * first. Idempotent for one client. */
export function wireThreadSubscriptions(client: ThreadSubscriptionClient): void {
  if (registries.has(client)) return;
  registries.set(client, new ThreadSubscriptions(client, true));
}

/** Retire a connection's registry when its owner lets go of the client. */
export function retireThreadSubscriptions(client: ThreadSubscriptionClient): void {
  registries.get(client)?.retire();
}

/** Acquire one additive thread subscription lifetime. Every transcript and
 * activity owner of this connection must join, rather than unsubscribe or
 * replace another owner's membership directly. */
export function acquireThreadSubscription(client: ThreadSubscriptionClient, ref: string): ThreadSubscriptionLease {
  if (!ref.trim()) throw new TypeError("Thread subscription requires a session ref");
  let registry = registries.get(client);
  if (!registry) {
    registry = new ThreadSubscriptions(client);
    registries.set(client, registry);
  }
  return registry.acquire(ref);
}
