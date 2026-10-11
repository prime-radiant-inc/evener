// The wall clock the transcript's time markers read ("Today 2:14 PM" becomes
// "Yesterday 2:14 PM" at midnight). One ticker, shared by every mounted
// reader and stopped when the last one goes, so a marker re-renders itself as
// the day turns without the list having to re-render every row.
import { useSyncExternalStore } from "react";

const TICK_MS = 60_000;

let now: number | null = null;
let ticker: ReturnType<typeof setInterval> | undefined;
const readers = new Set<() => void>();

function subscribe(reader: () => void) {
	readers.add(reader);
	if (ticker === undefined) {
		ticker = setInterval(() => {
			now = Date.now();
			for (const notify of readers) notify();
		}, TICK_MS);
	}
	return () => {
		readers.delete(reader);
		if (readers.size === 0) {
			clearInterval(ticker);
			ticker = undefined;
			now = null;
		}
	};
}

// The first read with nobody subscribed takes the time; later reads return the
// same instant until the next tick, as useSyncExternalStore requires.
function getSnapshot(): number {
	if (now === null) now = Date.now();
	return now;
}

/** Now, to the minute, re-rendering the caller each minute. */
export function useMinuteClock(): number {
	return useSyncExternalStore(subscribe, getSnapshot);
}
