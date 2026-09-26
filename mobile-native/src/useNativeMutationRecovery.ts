// The native recovery surface's window onto one target's durable mutation
// rows: it enumerates the recovery projection for the composite hub/
// conversation target key (the runtime's scoped persistence read), follows
// the storage changes the runtime publishes, and exposes the one recovery
// write (discardRecovery) - so a screen can list what a target's failed or
// orphaned mutations still hold and let the user retire a row.
//
// Audited against the landed slice-3 runtime (nativeMutationRuntime.ts): the
// read is the runtime's own NativeMutationPersistenceRead.read(targetRef)
// over storage listRecovery/listOutbox/listOptimistic, the subscription is
// subscribeStorage's storage-change notification, and the discard is
// runtime.discardRecovery, which publishes the change that refreshes this
// projection. The runtime arrives as a parameter - the hook never constructs
// one, never registers a target, and does no subscribe, read or discard work
// during render: every runtime call is owned by an effect, fenced to the
// effect's own runtime/target generation so a client replacement can never
// repopulate the new generation's state with the old one's completions.

import type {
	MutationAttachmentRef,
	MutationPersistenceSnapshot,
} from "@evener/appwire-client/state/mutation";
import { useCallback, useEffect, useRef, useState } from "react";
import type {
	NativeMutationPersistenceRead,
	NativeMutationStorageListener,
} from "./nativeMutationRuntime";

// The runtime surface the recovery projection needs: the landed scoped
// persistence read plus the storage-change subscription and the one recovery
// write. NativeMutationRuntime satisfies this structurally; the narrower
// interface is what lets tests prove the contract without a full runtime.
export interface NativeMutationRecoveryRuntime
	extends NativeMutationPersistenceRead {
	subscribeStorage(listener: NativeMutationStorageListener): () => void;
	discardRecovery(
		clientMutationId: string,
		targetRef: string,
	): Promise<boolean>;
}

export interface NativeMutationRecoveryProjection {
	targetKey: string;
	snapshot: MutationPersistenceSnapshot<MutationAttachmentRef> | null;
	loading: boolean;
	error: unknown;
	/** Retires one durable recovery row for THIS target: the runtime's
	 * discardRecovery scoped to the composite target key, so a row another
	 * target owns is not reachable through this projection. The runtime
	 * publishes the storage change itself, which is what refreshes the
	 * projection - a null runtime makes this a no-op resolving false. */
	discard(clientMutationId: string): Promise<boolean>;
}

interface RecoveryState {
	// The runtime whose effect produced this state. The render-path fence
	// requires BOTH the owner and the target key to match before state is
	// exposed: a runtime replacement re-renders before its effect has
	// cleared the old runtime's snapshot, and that first render must not
	// show the old rows either.
	owner: NativeMutationRecoveryRuntime | null;
	targetKey: string;
	snapshot: MutationPersistenceSnapshot<MutationAttachmentRef> | null;
	loading: boolean;
	error: unknown;
}

function emptyState(
	targetKey: string,
	loading: boolean,
	owner: NativeMutationRecoveryRuntime | null,
): RecoveryState {
	return { owner, targetKey, snapshot: null, loading, error: null };
}

export function useNativeMutationRecovery(
	runtime: NativeMutationRecoveryRuntime | null,
	targetKey: string,
): NativeMutationRecoveryProjection {
	const [state, setState] = useState(() =>
		emptyState(targetKey, runtime !== null, runtime),
	);
	const generation = useRef(0);

	useEffect(() => {
		// Each runtime/target pairing is its own generation: a completion is
		// only ever applied by the generation that issued it, so a replaced
		// runtime's in-flight read or a later storage change on the old
		// runtime can never repopulate this projection.
		const currentGeneration = ++generation.current;
		let disposed = false;
		let latestRequest = 0;
		const current = (request: number) =>
			!disposed &&
			generation.current === currentGeneration &&
			request === latestRequest;

		setState(emptyState(targetKey, runtime !== null, runtime));
		if (runtime === null) {
			return () => {
				disposed = true;
			};
		}

		const read = () => {
			const request = ++latestRequest;
			let pending: Promise<MutationPersistenceSnapshot<MutationAttachmentRef>>;
			try {
				pending = runtime.read(targetKey);
			} catch (error) {
				if (current(request))
					setState({
						owner: runtime,
						targetKey,
						snapshot: null,
						loading: false,
						error,
					});
				return;
			}
			void pending.then(
				(snapshot) => {
					if (current(request))
						setState({
							owner: runtime,
							targetKey,
							snapshot,
							loading: false,
							error: null,
						});
				},
				(error) => {
					if (current(request))
						setState({
							owner: runtime,
							targetKey,
							snapshot: null,
							loading: false,
							error,
						});
				},
			);
		};

		const unsubscribe = runtime.subscribeStorage((targetRefs) => {
			if (targetRefs.includes(targetKey)) read();
		});
		read();
		return () => {
			disposed = true;
			unsubscribe();
		};
	}, [runtime, targetKey]);

	const discard = useCallback(
		(clientMutationId: string): Promise<boolean> => {
			if (runtime === null) return Promise.resolve(false);
			return runtime.discardRecovery(clientMutationId, targetKey);
		},
		[runtime, targetKey],
	);

	// The render path's own fence: until the effect for the current runtime
	// and key has produced state, the previously rendered state belongs to
	// the old pairing and must never leak into the new one's first render -
	// neither across a target-key change nor across a runtime replacement
	// that keeps the key.
	if (
		runtime === null ||
		state.targetKey !== targetKey ||
		state.owner !== runtime
	)
		return { ...emptyState(targetKey, runtime !== null, runtime), discard };
	return { ...state, discard };
}
