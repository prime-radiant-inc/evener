// Watches for shares the extension left in the app group and hands them to the
// app one at a time (spec: share intent). A share is offered once; a sheet
// dismissed without choosing leaves it pending and the next external trigger
// (URL, foreground, hub ready) offers it again, so nothing is lost. A placed
// share is forgotten and never offered again.
import type { SharePort, SharedPayload } from "./sharePayload";
import { pendingShares, shareFileName } from "./sharePayload";

export interface ShareIntakeOptions {
	/** Called with the oldest pending share, one at a time. The app opens its
	 * sheet; nothing is deleted until the sheet's own placement completes. */
	onShare(share: SharedPayload): void;
	port: SharePort;
}

export interface ShareIntake {
	/** Whether a share is with the sheet. */
	readonly busy: boolean;
	/** Offers the oldest pending share, unless the sheet is up. */
	offer(): Promise<void>;
	/** The sheet closed without placing its share: it stays pending, and the
	 * next wake offers it again. Does not offer immediately. */
	release(): void;
	/** A placed or explicitly discarded share is never offered again, even if
	 * its file lingers. */
	forget(id: string): void;
	/** A placed or discarded share is done: its file is deleted, and the next
	 * pending one may be offered now, without waiting for a foreground or URL.
	 * A delete that fails leaves the share pending, so a restart offers it
	 * again instead of losing the record silently. */
	advance(id: string): Promise<void>;
	/** The app unmounted: nothing more is offered. */
	dispose(): void;
}

export function createShareIntake(options: ShareIntakeOptions): ShareIntake {
	const done = new Set<string>();
	let active: string | null = null;
	let disposed = false;
	const offer = async (): Promise<void> => {
		if (active !== null || disposed) return;
		let shares: SharedPayload[];
		try {
			shares = await pendingShares(options.port);
		} catch {
			return;
		}
		// The sheet may have opened while the directory was read.
		if (active !== null || disposed) return;
		const next = shares.find((share) => !done.has(share.id));
		if (!next) return;
		active = next.id;
		options.onShare(next);
	};
	return {
		get busy() {
			return active !== null;
		},
		offer,
		release() {
			active = null;
		},
		forget(id) {
			done.add(id);
			if (active === id) active = null;
		},
		async advance(id) {
			try {
				await options.port.remove(shareFileName(id));
			} catch {
				// The file is still there: offering it again is the honest
				// outcome, since the app cannot say it was placed.
				return;
			}
			await offer();
		},
		dispose() {
			disposed = true;
		},
	};
}
