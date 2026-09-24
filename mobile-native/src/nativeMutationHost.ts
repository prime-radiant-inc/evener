import type { AppwireClientLike, ThreadReadResponse } from "@evener/appwire-client";
import type { ConversationMutationSubmitter } from "../../mobile/src/state/conversationMutation";
import {
	NativeMutationRuntime,
	type NativeMutationReadLease,
} from "./nativeMutationRuntime";

// One conversation screen's durable-mutation binding to the process-lifetime
// runtime: it registers exactly this screen's client for this hub/conversation
// target, starts the runtime once, and fences the same raw authoritative read
// that produces the conversation projection - so the runtime's dispatch gate
// opens on the snapshot the screen is actually showing, and a replacement or
// unmount retires the registration and any in-flight read lease with it.
//
// The registration and the read fence are deliberately one object: a read
// lease is only ever handed out while this host still owns a live
// registration, so a lease from a disposed screen can never reconcile a
// replacement client's target (#1955's ownership recheck, applied to the
// durable dispatch gate).
export interface NativeMutationHost extends ConversationMutationSubmitter {
	start(): Promise<void>;
	beginRead(
		targetRef: string,
		expectedThreadId?: string,
	): NativeMutationReadLease | undefined;
	reconcileRead(
		lease: NativeMutationReadLease | undefined,
		response: ThreadReadResponse,
	): Promise<"reconciled" | "blocked" | "stale">;
	dispose(): void;
}

/** The refusal a mutation gets while no host is live: durable submission is
 * unavailable, so the store must surface a failure rather than durably accept
 * a message that no registered client can dispatch. */
export const NATIVE_MUTATION_HOST_UNAVAILABLE =
	"Durable sending is unavailable right now. Reconnect and try again.";

/** A submitter bound to a host that may not be live yet (or any more): while
 * the host lookup is null it refuses, so a submission is never admitted
 * without a registered client, and once a host exists it delegates to the
 * runtime. A screen keeps one stable submitter and hands the store this
 * ref-backed lookup. */
export function createDurableSubmitter(
	host: () => NativeMutationHost | null,
): ConversationMutationSubmitter {
	return {
		submit: (request) => {
			const live = host();
			return live === null
				? Promise.reject(new Error(NATIVE_MUTATION_HOST_UNAVAILABLE))
				: live.submit(request);
		},
	};
}

export function createNativeMutationHost(
	runtime: NativeMutationRuntime,
	hubId: string,
	targetRef: string,
	client: AppwireClientLike,
): NativeMutationHost {
	let unregister: (() => void) | undefined;
	let startPromise: Promise<void> | undefined;
	return {
		start: () => {
			if (startPromise !== undefined) return startPromise;
			startPromise = (async () => {
				unregister = runtime.registerTarget(hubId, targetRef, client);
				await runtime.start();
			})().catch((error) => {
				// Every startup failure - a client the runtime cannot bind, or
				// a runtime start that rejects - reaches the caller as one
				// rejected promise. The registration is kept: the runtime
				// retries its own start on the next submission, and a mutation
				// admitted after that retry succeeds must still have this
				// screen's client bound or it would be durably enqueued and
				// never dispatched. dispose() retires it with the mount, so a
				// replaced client never inherits it.
				startPromise = undefined;
				throw error;
			});
			return startPromise;
		},
		submit: (request) =>
			unregister === undefined
				? Promise.reject(new Error(NATIVE_MUTATION_HOST_UNAVAILABLE))
				: runtime.submit(request),
		beginRead: (readTargetRef, expectedThreadId) =>
			unregister === undefined
				? undefined
				: runtime.beginAuthoritativeRead(
						hubId,
						readTargetRef,
						client,
						expectedThreadId,
					),
		reconcileRead: (lease, response) =>
			lease === undefined || unregister === undefined
				? Promise.resolve("stale")
				: runtime.reconcileAuthoritativeRead(lease, response),
		dispose: () => {
			unregister?.();
			unregister = undefined;
			startPromise = undefined;
		},
	};
}
