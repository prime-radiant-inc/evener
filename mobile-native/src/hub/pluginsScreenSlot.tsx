// What the Plugins page hands a marketplace's page pushed over it. The Plugins
// page owns the stores, the write gate and the applied-removal guard for as
// long as it is mounted (PluginsPage.tsx says why each lives there), and a
// marketplace's page is only ever pushed from it, so it stays mounted
// underneath. It publishes those here, keyed by the hub it was opened for, and
// takes them back when it goes; the pushed page reads them as state, so it
// re-renders whenever the Plugins page publishes anew.
import {
	createContext,
	type Dispatch,
	type ReactNode,
	type SetStateAction,
	useContext,
	useEffect,
	useMemo,
	useState,
} from "react";
import type { MarketplaceWrites } from "../MarketplaceBrowser";

/** What the pushed page writes through, the hub it was published for, and
 * the Plugins page's removal warning, which the pushed page covers. */
export type PluginsScreenSlot = MarketplaceWrites & { hubId: string; marketplaceWarning: string | null };

type SlotState = [PluginsScreenSlot | null, Dispatch<SetStateAction<PluginsScreenSlot | null>>];

// Outside the Hub sheet (a page's own tests) nothing reads the slot, so
// publishing into the default does nothing.
const SlotContext = createContext<SlotState>([null, () => {}]);

export function PluginsScreenSlotProvider({ children }: { children: ReactNode }) {
	const [slot, setSlot] = useState<PluginsScreenSlot | null>(null);
	const value = useMemo<SlotState>(() => [slot, setSlot], [slot]);
	return <SlotContext.Provider value={value}>{children}</SlotContext.Provider>;
}

/** The Plugins page's current publication, or null when none is mounted. */
export function usePluginsScreenSlot(): PluginsScreenSlot | null {
	return useContext(SlotContext)[0];
}

/** Publishes `slot` while the calling page is mounted, and takes it back
 * when the page goes, unless a newer page has published since. */
export function usePublishPluginsScreen(slot: PluginsScreenSlot): void {
	const publish = useContext(SlotContext)[1];
	useEffect(() => {
		publish(slot);
		return () => publish((current) => (current === slot ? null : current));
	}, [publish, slot]);
}
