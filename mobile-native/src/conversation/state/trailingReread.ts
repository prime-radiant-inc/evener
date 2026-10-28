// The deferred reread a rehydrate owes the store when a mutation changed owner
// during its await. The rehydrate must not publish a projection that predates
// the mutation, so it stores one request holding the EXACT binding captured at
// schedule time (never recaptured). The request drains once the mutation has
// settled (pendingSubmission cleared; a failedSubmission is already terminal,
// so it drains exactly as the failed record did). A binding that changed since
// (switch to B) is stale and its request is dropped. Further mutation
// revisions create at most one later need.

export interface TrailingRereadHost<B extends { readonly ref: string }> {
	/** The current binding, or null when none can schedule a read. */
	captureBinding(): B | null;
	isBindingCurrent(binding: B): boolean;
	/** The monotonic mutation-owner revision. */
	mutationRev(): number;
	/** True while a mutation attempt is in flight. */
	mutationPending(): boolean;
	/** Queues the effect on the store-owned drain scheduler. */
	enqueue(ref: string, effect: () => Promise<void>): void;
	/** Reads with the captured binding's own service, never the closure's. */
	reread(binding: B): Promise<void>;
}

export interface TrailingReread {
	/** Stores a request for the current binding and drains it if already settled. */
	schedule(): void;
	/**
	 * Drains the stored request once the mutation has settled: one reread goes
	 * through the scheduler, its binding validated again inside the effect.
	 */
	drain(): void;
	/** Drops the stored request. */
	clear(): void;
	isPending(): boolean;
}

export function createTrailingReread<B extends { readonly ref: string }>(host: TrailingRereadHost<B>): TrailingReread {
	let pending: B | null = null;

	function drain(): void {
		if (pending === null) return;
		// A still-pending attempt keeps the request deferred.
		if (host.mutationPending()) return;
		const binding = pending;
		// The effect compares this against the current revision to detect a
		// newer mutation that started after enqueue.
		const enqueueMutationRev = host.mutationRev();
		pending = null;
		if (!host.isBindingCurrent(binding)) return;
		host.enqueue(binding.ref, async () => {
			if (!host.isBindingCurrent(binding)) return;
			// A newer mutation started after enqueue: do zero read and restore one
			// binding-owned request for that revision, which its settle hook drains
			// exactly once.
			if (host.mutationRev() !== enqueueMutationRev && host.mutationPending()) {
				pending = binding;
				return;
			}
			await host.reread(binding);
		});
	}

	return {
		schedule() {
			const binding = host.captureBinding();
			if (binding === null) return;
			pending = binding;
			// The mutation may have settled before the rehydrate was released.
			drain();
		},
		drain,
		clear() {
			pending = null;
		},
		isPending() {
			return pending !== null;
		},
	};
}
