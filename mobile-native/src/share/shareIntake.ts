// Watches for shares the extension left in the app group and hands them to the
// app one at a time (spec: share intent). The sheet marks itself busy while it
// is up, so a share is never offered twice, and releases when it closes so the
// next pending share can be offered.
import type { SharePort, SharedPayload } from "./sharePayload";
import { pendingShares } from "./sharePayload";

export interface ShareIntakeOptions {
	/** Called with the oldest unplaced share, one at a time. The app opens its
	 * sheet; nothing is deleted until the sheet's own placement completes. */
	onShare(share: SharedPayload): void;
	port: SharePort;
}

export interface ShareIntake {
	/** Whether a share is with the sheet. A share already offered is not
	 * offered again. */
	readonly busy: boolean;
	/** Offers the oldest pending share, unless the sheet is up. */
	offer(): Promise<void>;
	/** The sheet closed: another pending share may be offered now. */
	release(): Promise<void>;
	/** A placed share is never offered again, even if its file lingers. */
	forget(id: string): void;
	/** The app unmounted: nothing more is offered. */
	dispose(): void;
}

export function createShareIntake(options: ShareIntakeOptions): ShareIntake {
	const offered = new Set<string>();
	let busy = false;
	let disposed = false;
	const offer = async (): Promise<void> => {
		if (busy || disposed) return;
		let shares: SharedPayload[];
		try {
			shares = await pendingShares(options.port);
		} catch {
			return;
		}
		const next = shares.find((share) => !offered.has(share.id));
		if (!next || busy) return;
		offered.add(next.id);
		busy = true;
		options.onShare(next);
	};
	return {
		get busy() {
			return busy;
		},
		offer,
		release() {
			busy = false;
			return offer();
		},
		forget(id) {
			offered.add(id);
		},
		dispose() {
			disposed = true;
		},
	};
}
