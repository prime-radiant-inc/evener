// Sending to a session from a screen stacked above it: the stop request a
// subagent's screen sends its coordinator (spec 9), and the review the Reader
// sends its session (spec 10.2). Both are admitted through the durable
// runtime, like the composer's messages, so they survive a dropped
// connection. Both read the session's live state from here, because the
// session's own screen isn't reading while another screen is on top.
import {
	type AppwireClientLike,
	acquireThreadSubscription,
	type ThreadSubscriptionLease,
	hydrateThread,
	sessionControls,
	type ThreadCapabilities,
} from "@evener/appwire-client";
import { reconcilePendingEntries } from "@evener/appwire-client/state/mutation";
import { type ConversationClientLike, READ_ITEM_LIMIT } from "../../../mobile/src/services/conversation";
import { type NativeMutationRuntime, nativeMutationTargetKey } from "../nativeMutationRuntime";
import { type SendAction, sendAction } from "./sendAction";

export interface SessionState {
	threadId: string;
	instanceId: string;
	cwd: string;
	status: string;
	capabilities: ThreadCapabilities;
	queueDepth: number;
	/** The session's model (Thread.modelProvider, the wire's model field). */
	model: string;
}

/** A session read from a screen above it. Following holds an additive lease
 * on this same connection; other transcript and activity owners remain subscribed. */
export class SessionLink {
	private state: SessionState | null = null;
	private listeners = new Set<() => void>();
	private stopListening: (() => void) | null = null;
	private generation = 0;
	private lease: ThreadSubscriptionLease | null = null;

	constructor(
		private readonly client: ConversationClientLike,
		readonly ref: string,
	) {}

	getSnapshot = (): SessionState | null => this.state;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	async read({ follow }: { follow: boolean }): Promise<SessionState> {
		const generation = ++this.generation;
		if (follow) this.lease ??= acquireThreadSubscription(this.client, this.ref);
		const response =
			follow && this.lease
				? await this.lease.read({ includeTurns: false })
				: await this.client.request("thread/read", { ref: this.ref, includeTurns: false });
		const thread = response.thread;
		const state: SessionState = {
			threadId: thread.id,
			instanceId: thread.evener.instanceId ?? thread.id,
			cwd: thread.cwd,
			status: thread.status.type,
			capabilities: thread.evener.capabilities,
			queueDepth: thread.evener.queue.depth ?? 0,
			model: thread.modelProvider,
		};
		if (generation === this.generation) {
			if (follow) this.listen();
			this.publish(state);
		}
		return state;
	}

	dispose(): void {
		this.generation += 1;
		this.stopListening?.();
		this.stopListening = null;
		this.lease?.release();
		this.lease = null;
		this.listeners.clear();
	}

	private listen(): void {
		if (this.stopListening) return;
		this.stopListening = this.client.onNotification((notification) => {
			const state = this.state;
			if (!state) return;
			if (notification.method === "thread/status/changed" && notification.params.ref === this.ref)
				this.publish({
					...state,
					status: notification.params.status.type,
					...(notification.params.capabilities ? { capabilities: notification.params.capabilities } : {}),
				});
			else if (notification.method === "thread/queueChanged" && notification.params.ref === this.ref)
				this.publish({ ...state, queueDepth: notification.params.queue.depth ?? 0 });
			else if (
				notification.method === "evener/thread/resync" &&
				notification.params.ref === this.ref &&
				notification.params.threadId === state.threadId
			)
				void this.read({ follow: true }).catch(() => {});
		});
	}

	private publish(state: SessionState): void {
		this.state = state;
		for (const listener of [...this.listeners]) listener();
	}
}

export type SessionMessageKind = "send" | "queue" | "steer";

/** Ask coordinator to stop it (spec 9): the one Send that steers, because the
 * request is about the turn that is running. With no turn running it sends,
 * which starts one and resumes a shut-down coordinator; a harness that can't
 * steer mid-turn gets it queued. A coordinator that needs a restart can't
 * take it. */
export function stopRequestKind(session: SessionState): SessionMessageKind | null {
	if (session.status === "restartRequired") return null;
	const controls = sessionControls(session.status, session.capabilities, session.queueDepth);
	if (controls.steer) return "steer";
	if (controls.queue) return "queue";
	if (controls.send) return "send";
	return null;
}

export interface SessionTarget {
	hubId: string;
	ref: string;
	threadId: string;
	instanceId: string;
}

/** What the composer's one Send (spec 8.5) does with a message written on a
 * screen above the session, such as the Reader's review: phase 3's
 * sendAction, fed what the session's own store feeds it
 * (mobile/src/state/conversation.ts, reconcilePendingMutations). The read has
 * the window the session's own read uses, so a send of this client's that the
 * session already shows stops counting as in flight. Every record in this
 * phone's durable outbox is this client's own
 * (createConversationMutationPendingPort), and the runtime remembers what it
 * submitted, so a send the daemon lists as pending after its record settled
 * still counts as this phone's. */
export async function readSendAction(
	runtime: Pick<NativeMutationRuntime, "read" | "submittedHere">,
	client: ConversationClientLike,
	hubId: string,
	ref: string,
): Promise<{ action: SendAction; target: SessionTarget }> {
	const response = await client.request("thread/read", {
		ref,
		includeTurns: true,
		itemsView: "fragment",
		itemLimit: READ_ITEM_LIMIT,
	});
	const session = hydrateThread(response, ref, Date.now());
	const targetKey = nativeMutationTargetKey(hubId, ref);
	const held = await runtime.read(targetKey);
	// Every durable record here is this phone's own; the runtime's record of
	// what it submitted covers a send the read settled out of the outbox.
	const pending = reconcilePendingEntries(
		targetKey,
		[...held.outbox, ...held.optimistic],
		session,
		runtime.submittedHere(targetKey),
		() => true,
	);
	return {
		// The read just answered, so the connection is up.
		action: sendAction(session, pending, true),
		target: { hubId, ref, threadId: session.threadId, instanceId: session.instanceId ?? session.threadId },
	};
}

/** Admits the message durably, then releases the session's target if a
 * reconnect left it waiting for a read its own screen isn't making (Review
 * Focus 2). A release that can't run leaves the message in the outbox, and
 * the session's next read sends it. */
export async function submitSessionMessage(
	runtime: Pick<NativeMutationRuntime, "submit" | "settleTarget">,
	client: AppwireClientLike | null,
	target: SessionTarget,
	kind: SessionMessageKind,
	text: string,
): Promise<void> {
	await runtime.submit({
		kind,
		hubId: target.hubId,
		targetRef: target.ref,
		threadId: target.threadId,
		instanceId: target.instanceId,
		input: [{ type: "text", text }],
	});
	if (client) await runtime.settleTarget(target.hubId, target.ref, client).catch(() => "blocked" as const);
}
