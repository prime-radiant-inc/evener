import type {
	AppwireClientLike,
	MutationReceipt,
	ThreadReadResponse,
} from "@evener/appwire-client";
import type {
	ConversationMutationRequest,
	ConversationMutationSubmitter,
} from "../../mobile/src/state/conversationMutation";
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
export interface NativeMutationHost {
	start(): Promise<void>;
	submit(
		request: ConversationMutationRequest,
	): Promise<MutationReceipt | undefined>;
	beginRead(
		targetRef: string,
		expectedThreadId?: string,
	): NativeMutationReadLease | undefined;
	reconcileRead(
		lease: NativeMutationReadLease | undefined,
		response: ThreadReadResponse,
	): Promise<"reconciled" | "blocked" | "stale">;
	dispose(): void;
	stop(): Promise<void>;
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
			try {
				unregister = runtime.registerTarget(hubId, targetRef, client);
			} catch (error) {
				// A client the runtime cannot bind is a startup failure like any
				// other: report it as a rejected promise so a caller's catch owns
				// it, rather than throwing out of the caller's synchronous flow.
				return Promise.reject(error);
			}
			startPromise = runtime.start().catch((error) => {
				// Keep the registration: the runtime retries its own start on
				// the next submission, and a mutation admitted after that
				// retry succeeds must still have this screen's client bound or
				// it would be durably enqueued and never dispatched. The
				// registration is retired by dispose() with the mount, so a
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
			lease === undefined
				? Promise.resolve("stale")
				: runtime.reconcileAuthoritativeRead(lease, response),
		dispose: () => {
			unregister?.();
			unregister = undefined;
			startPromise = undefined;
		},
		stop: () => runtime.stop(),
	};
}
