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
        this.releaseEmpty(member);
      }
    });
  }

  acquire(ref: string): ThreadSubscriptionLease {
    let member = this.members.get(ref);
    if (!member) {
      member = { ref, holders: 0, subscribed: false, transition: null, metadata: null };
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
      if (held.transition) {
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
        if (held.holders === 0) held.metadata = null;
        this.releaseEmpty(held);
      },
    };
  }

  private async ensure(member: Membership): Promise<void> {
    if (member.transition) {
      await member.transition;
      if (member.holders > 0) await this.ensure(member);
      return;
    }
    if (member.subscribed || member.holders === 0) return;
    await this.acquireWire(member, { includeTurns: false });
  }

  private acquireWire(member: Membership, params: ThreadSubscriptionReadParams): Promise<ThreadReadResponse> {
    const generation = this.generation;
    const response = this.client.request("thread/read", {
      ...params,
      ref: member.ref,
      subscribe: true,
      replaceSubscription: false,
    });
    const transition = response.then((result) => {
      if (generation === this.generation) {
        member.subscribed = true;
        this.admitMetadata(member, result, generation);
      }
    });
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
    if (member.holders > 0 || member.transition) return;
    if (member.subscribed && this.client.state === "ready") {
      member.subscribed = false;
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
