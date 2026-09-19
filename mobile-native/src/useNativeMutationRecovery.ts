import { useEffect, useRef, useState } from "react";
import type {
	NativeMutationPersistenceSnapshot,
	NativeMutationStorageListener,
} from "./nativeMutationRuntime";

export interface NativeMutationRecoveryRuntime {
	readTargetRecords(
		targetKey: string,
	): Promise<NativeMutationPersistenceSnapshot>;
	subscribeStorage(listener: NativeMutationStorageListener): () => void;
}

export interface NativeMutationRecoveryProjection {
	targetKey: string;
	snapshot: NativeMutationPersistenceSnapshot | null;
	loading: boolean;
	error: unknown;
}

function emptyProjection(
	targetKey: string,
	loading: boolean,
): NativeMutationRecoveryProjection {
	return { targetKey, snapshot: null, loading, error: null };
}

export function useNativeMutationRecovery(
	runtime: NativeMutationRecoveryRuntime | null,
	targetKey: string,
): NativeMutationRecoveryProjection {
	const [state, setState] = useState(() =>
		emptyProjection(targetKey, runtime !== null),
	);
	const generation = useRef(0);

	useEffect(() => {
		const currentGeneration = ++generation.current;
		let disposed = false;
		let latestRequest = 0;
		const current = (request: number) =>
			!disposed &&
			generation.current === currentGeneration &&
			request === latestRequest;

		setState(emptyProjection(targetKey, runtime !== null));
		if (runtime === null) {
			return () => {
				disposed = true;
			};
		}

		const read = () => {
			const request = ++latestRequest;
			let pending: Promise<NativeMutationPersistenceSnapshot>;
			try {
				pending = runtime.readTargetRecords(targetKey);
			} catch (error) {
				if (current(request))
					setState({ targetKey, snapshot: null, loading: false, error });
				return;
			}
			void pending.then(
				(snapshot) => {
					if (current(request))
						setState({ targetKey, snapshot, loading: false, error: null });
				},
				(error) => {
					if (current(request))
						setState({ targetKey, snapshot: null, loading: false, error });
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

	if (runtime === null || state.targetKey !== targetKey)
		return emptyProjection(targetKey, runtime !== null);
	return state;
}
