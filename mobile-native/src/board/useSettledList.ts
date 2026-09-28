import { useEffect, useLayoutEffect, useState, useSyncExternalStore } from "react";
import type { HoldableItem } from "./heldOrder";
import { type SettledSnapshot, SettledList } from "./settledList";

/** Binds a SettledList to the Board's render: the items it would show (memoized
 * by the caller, null before the first load) go in; what to show comes out.
 * A layout effect hands them over, so the re-render it causes lands before the
 * frame is shown and the list never flashes the previous items (or none).
 * Unmounting drops the subscription and disposes the list, clearing the settle
 * deadline and any open washes, so a timer a Board left behind mid-settle or
 * mid-wash cannot fire after it is gone. */
export function useSettledList<T extends HoldableItem>(
	items: readonly T[] | null,
): { list: SettledList<T>; snapshot: SettledSnapshot<T> } {
	const [list] = useState(() => new SettledList<T>());
	useLayoutEffect(() => {
		list.setItems(items);
	}, [list, items]);
	useEffect(() => () => list.dispose(), [list]);
	const snapshot = useSyncExternalStore(list.subscribe, list.getSnapshot);
	return { list, snapshot };
}
