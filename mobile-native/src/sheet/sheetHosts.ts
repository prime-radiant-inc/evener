// A sheet route renders beside the screen that opened it, not inside it, so it
// can't take that screen's props or callbacks, and route params must stay
// plain data. The screen provides a host instead: the object its sheet reads
// and calls, under a key the sheet rebuilds from its own params. Each kind of
// sheet has its own registry, so PRs that add sheets never share a type.
import { useEffect, useRef, useSyncExternalStore } from "react";

export interface SheetHosts<Host> {
	/** Provide `host` under `key`, replacing what this owner provided before.
	 * The newest owner answers, so a session opened twice answers from the
	 * copy on top. */
	provide(key: string, owner: object, host: Host): void;
	/** Take this owner's host away: its screen closed. */
	release(key: string, owner: object): void;
	get(key: string): Host | undefined;
	subscribe(listener: () => void): () => void;
}

export function sheetHosts<Host>(): SheetHosts<Host> {
	const entries = new Map<string, { owner: object; host: Host }[]>();
	const listeners = new Set<() => void>();
	const changed = () => {
		for (const listener of [...listeners]) listener();
	};
	return {
		provide(key, owner, host) {
			const list = entries.get(key) ?? [];
			const mine = list.find((entry) => entry.owner === owner);
			if (mine?.host === host) return;
			if (mine) mine.host = host;
			else list.push({ owner, host });
			entries.set(key, list);
			changed();
		},
		release(key, owner) {
			const list = entries.get(key);
			if (!list?.some((entry) => entry.owner === owner)) return;
			const rest = list.filter((entry) => entry.owner !== owner);
			if (rest.length > 0) entries.set(key, rest);
			else entries.delete(key);
			changed();
		},
		get(key) {
			return entries.get(key)?.at(-1)?.host;
		},
		subscribe(listener) {
			listeners.add(listener);
			return () => {
				listeners.delete(listener);
			};
		},
	};
}

/** The key a sheet and its screen share: a session's is `sheetKey(hubId,
 * ref)`, the Board's `sheetKey(hubId)`. */
export function sheetKey(...parts: readonly string[]): string {
	return JSON.stringify(parts);
}

/** A screen provides its sheets' host while it's mounted. Memoize `host`:
 * each new object re-renders the open sheet, which is how the sheet sees the
 * screen's live state. */
export function useProvideSheetHost<Host>(hosts: SheetHosts<Host>, key: string, host: Host): void {
	const owner = useRef({}).current;
	useEffect(() => {
		hosts.provide(key, owner, host);
	}, [hosts, key, owner, host]);
	useEffect(() => () => hosts.release(key, owner), [hosts, key, owner]);
}

/** A sheet's host, or undefined once its screen is gone. Then the sheet
 * leaves (`finish`), since nothing is left for it to act on. */
export function useSheetHost<Host>(
	hosts: SheetHosts<Host>,
	key: string,
	sheet: { finish(): void },
): Host | undefined {
	const host = useSyncExternalStore(hosts.subscribe, () => hosts.get(key));
	useEffect(() => {
		if (host === undefined) sheet.finish();
	}, [host, sheet]);
	return host;
}
