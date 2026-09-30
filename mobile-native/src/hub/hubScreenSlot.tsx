// One generic Hub screen slot: a list page publishes what the page it pushes
// needs, and the pushed page reads it. Two pages publish today — the Plugins
// page hands a marketplace its writes, and the Providers page hands a provider
// its detail — so the payloads differ, but the publish/read shape is the same
// and lives here once.
//
// The slot is an external store, not React state: the owning page publishes on
// every render, and only the pushed page subscribes, so a publication never
// re-renders the page that made it. A publication lands in a layout effect,
// before the commit paints, so a page pushed in the commit that builds it
// paints with its content, and a controlled field in the pushed page (a pasted
// key) never renders a commit behind the owning page's state, which would drop
// and reorder fast keystrokes.
import {
	createContext,
	type ReactNode,
	useContext,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";
import type { MarketplaceWrites } from "../MarketplaceBrowser";

class SlotStore<T> {
	private value: T | null = null;
	private readonly listeners = new Set<() => void>();

	getSnapshot = (): T | null => this.value;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	set(value: T | null): void {
		if (value === this.value) return;
		this.value = value;
		for (const listener of this.listeners) listener();
	}
}

/** One slot's publish/read surface, over its own store. */
export interface HubScreenSlot<T> {
	Provider: (props: { children: ReactNode }) => ReactNode;
	/** The owning page's current publication, or null when none is mounted. */
	useSlot: () => T | null;
	/** Reads the slot's current publication outside render, where useSlot's
	 * render-time value can't: a test seam for when a publication lands within a
	 * commit. */
	useSlotReader: () => () => T | null;
	/** Publishes `slot` after every render of the calling page, and takes it
	 * back when the page goes, unless a newer page has published since. */
	usePublish: (slot: T) => void;
}

/** Makes one slot with its own store. Call once per payload kind; the returned
 * hooks and Provider serve only that kind. */
export function createHubScreenSlot<T>(): HubScreenSlot<T> {
	// Outside the Hub sheet (a page's own tests) nothing reads the slot, so
	// publishing into the default goes nowhere.
	const SlotContext = createContext<SlotStore<T>>(new SlotStore<T>());

	function Provider({ children }: { children: ReactNode }) {
		const [store] = useState(() => new SlotStore<T>());
		return <SlotContext.Provider value={store}>{children}</SlotContext.Provider>;
	}

	function useSlot(): T | null {
		const store = useContext(SlotContext);
		return useSyncExternalStore(store.subscribe, store.getSnapshot);
	}

	function useSlotReader(): () => T | null {
		return useContext(SlotContext).getSnapshot;
	}

	function usePublish(slot: T): void {
		const store = useContext(SlotContext);
		const published = useRef<T | null>(null);
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

	return { Provider, useSlot, useSlotReader, usePublish };
}

/** What the pushed marketplace page writes through, the hub it was published
 * for, and the Plugins page's removal warning, which the pushed page covers. */
export type PluginsScreenSlot = MarketplaceWrites & { hubId: string; marketplaceWarning: string | null };

// The Plugins page publishes while a marketplace's page is pushed over it, and
// takes it back when it goes.
const pluginsSlot = createHubScreenSlot<PluginsScreenSlot>();

/** Provides the Plugins page's slot to the Hub sheet's stack. */
export const PluginsScreenSlotProvider = pluginsSlot.Provider;
/** The Plugins page's current publication, or null when none is mounted. */
export const usePluginsScreenSlot = pluginsSlot.useSlot;
/** Publishes the Plugins page's publication while it is mounted. */
export const usePublishPluginsScreen = pluginsSlot.usePublish;

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

// The Providers page publishes its selected provider's detail while the detail
// is pushed over it, and takes it back when it goes.
const providersSlot = createHubScreenSlot<ProviderDetailSlot>();

/** Provides the Providers page's slot to the Hub sheet's stack. */
export const ProvidersScreenSlotProvider = providersSlot.Provider;
/** The Providers page's current detail, or null when none is mounted. */
export const useProviderDetailSlot = providersSlot.useSlot;
/** Reads the detail outside render, for a publication that lands within a
 * commit (ProviderDetailPage.test.tsx). */
export const useProviderDetailSlotReader = providersSlot.useSlotReader;
/** Publishes the Providers page's selected provider while it is mounted. */
export const usePublishProviderDetail = providersSlot.usePublish;
