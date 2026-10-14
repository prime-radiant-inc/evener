import type { AppwireClientLike } from "./clientLike";
import type { ThreadReadParams, ThreadReadResponse, ThreadStatus } from "./types.gen";

export type ThreadSubscriptionClient = Pick<AppwireClientLike, "request" | "onStateChange" | "state">;
export type ThreadSubscriptionReadParams = Omit<ThreadReadParams, "ref" | "subscribe" | "replaceSubscription">;
export interface ThreadSubscriptionMetadata {
  readonly threadId: string;
  readonly sessionId: string;
  readonly status: ThreadStatus;
}
export interface ThreadSubscriptionLease {
  ensure(): Promise<void>;
  read(params: ThreadSubscriptionReadParams): Promise<ThreadReadResponse>;
  metadata(): ThreadSubscriptionMetadata | null;
  release(): void;
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
}

// All owners of a connection join this membership table. Transcript hydration
// and independent rich reads remain with their callers; only wire membership
// acquisition and release wait for one another.
class ThreadSubscriptions {
  private members = new Map<string, Membership>();
  private generation = 0;
  private stopState: () => void;

  constructor(private readonly client: ThreadSubscriptionClient) {
    this.stopState = client.onStateChange((state) => {
      if (state === "ready") return;
      this.generation += 1;
      for (const member of this.members.values()) {
        member.subscribed = false;
        member.transition = null;
        member.metadata = null;
        member.orphanedSubscribe = false;
        this.releaseEmpty(member);
      }
    });
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
      const response = await this.client.request("thread/read", {
        ...params,
        ref,
        subscribe: false,
        replaceSubscription: false,
      });
      if (!released) this.admitMetadata(held, response, generation);
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
        this.admitMetadata(member, result, generation);
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

  private admitMetadata(member: Membership, { thread }: ThreadReadResponse, generation: number): void {
    if (generation !== this.generation || this.client.state !== "ready" || member.holders === 0) return;
    member.metadata = {
      threadId: thread.id,
      sessionId: thread.sessionId ?? thread.id,
      status: {
        type: thread.status.type,
        ...(thread.status.activeFlags ? { activeFlags: [...thread.status.activeFlags] } : {}),
      },
    };
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
    if (this.members.size === 0) {
      this.stopState();
      registries.delete(this.client);
    }
  }
}

const registries = new WeakMap<ThreadSubscriptionClient, ThreadSubscriptions>();

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
