// Binds a package scoped-host-read store to a component: the store owns the
// debounce, the kept response and the stale-answer rules; this hook starts it
// when an input changes and renders when an answer arrives.
import { createScopedHostReadStore, type ScopedHostReadInputs, type ScopedReadState } from "@evener/appwire-client";
import { useEffect, useRef, useState, useSyncExternalStore } from "react";

export function useScopedHostRead<T>(inputs: ScopedHostReadInputs<T>): { state: ScopedReadState<T>; retry(): void } {
	const { client, host, cwd, inputKey, enabled, read } = inputs;
	const [store] = useState(createScopedHostReadStore<T>);
	const { revision } = useSyncExternalStore(store.subscribe, store.getState, store.getInitialState);
	// Latest-value sync for the debounced callback: assigned in an effect, not
	// the render body, so a concurrent render never tears the value.
	const readRef = useRef(read);
	useEffect(() => {
		readRef.current = read;
	}, [read]);

	// The revision restarts the read; it isn't read inside.
	// biome-ignore lint/correctness/useExhaustiveDependencies: revision is a restart trigger, not a value the body reads
	useEffect(
		() => store.start({ client, host, cwd, inputKey, enabled, read: () => readRef.current() }),
		[store, client, host, cwd, inputKey, enabled, revision],
	);

	return { state: store.view(inputs), retry: store.retry };
}
