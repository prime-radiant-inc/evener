// What the Providers page hands the provider detail pushed over it (spec 12:
// a detail pushes, as a host's does). The Providers page owns the credential
// store, the sign-in flow and every write the detail makes (ProvidersPage.tsx
// says why each lives there), and the detail is only ever pushed from it, so
// it stays mounted underneath. It publishes the detail here on every render;
// the pushed page reads it, as pluginsScreenSlot's Marketplace page reads the
// Plugins page's.
//
// Unlike that slot, this one is an external store, not React state: the page
// publishes on every render, and only the pushed page subscribes, so a
// publication never re-renders the page that made it.
import { createContext, type ReactNode, useContext, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";

/** The detail the Providers page shows for its selected provider. */
export interface ProviderDetailSlot {
	/** The hub the page was opened for. */
	hubId: string;
	/** The provider whose detail this is, or null when none is selected. */
	name: string | null;
	/** The detail's content, for the selected provider. */
	detail: ReactNode;
	/** Leaving would lose a pasted key or strand its save: the page asks first. */
	guarded: boolean;
	/** Asks before leaving (spec 6), then calls `then`. */
	leave(then: () => void): void;
	/** The pushed page has gone: the Providers page drops the selection. */
	onGone(): void;
}

class SlotStore {
	private value: ProviderDetailSlot | null = null;
	private readonly listeners = new Set<() => void>();

	getSnapshot = (): ProviderDetailSlot | null => this.value;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	set(value: ProviderDetailSlot | null): void {
		if (value === this.value) return;
		this.value = value;
		for (const listener of this.listeners) listener();
	}
}

// Outside the Hub sheet (a page's own tests) nothing reads the slot, so
// publishing into the default goes nowhere.
const SlotContext = createContext<SlotStore>(new SlotStore());

export function ProvidersScreenSlotProvider({ children }: { children: ReactNode }) {
	const [store] = useState(() => new SlotStore());
	return <SlotContext.Provider value={store}>{children}</SlotContext.Provider>;
}

/** Reads the slot's current publication outside render, where
 * useProviderDetailSlot's render-time value can't: a test seam for when a
 * publication lands within a commit (providersScreenSlot.test.tsx). */
export function useProviderDetailSlotReader(): () => ProviderDetailSlot | null {
	return useContext(SlotContext).getSnapshot;
}

/** The Providers page's current detail, or null when none is mounted. */
export function useProviderDetailSlot(): ProviderDetailSlot | null {
	const store = useContext(SlotContext);
	return useSyncExternalStore(store.subscribe, store.getSnapshot);
}

/** Publishes `slot` after every render of the calling page, and takes it back
 * when the page goes, unless a newer page has published since. It publishes
 * in a layout effect, before the commit paints, so a detail pushed in the
 * commit that builds it paints with its content, and a controlled field in
 * the detail (the pasted key) never renders a commit behind the page's
 * state, which drops and reorders fast keystrokes. */
export function usePublishProviderDetail(slot: ProviderDetailSlot): void {
	const store = useContext(SlotContext);
	const published = useRef<ProviderDetailSlot | null>(null);
	useLayoutEffect(() => {
		published.current = slot;
		store.set(slot);
	});
	// Only the page's unmount takes the slot back.
	useEffect(
		() => () => {
			if (store.getSnapshot() === published.current) store.set(null);
		},
		[store],
	);
}
