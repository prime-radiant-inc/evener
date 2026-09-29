// How many live sessions each host has (spec 12's "3 live"). No hosts catalog
// exists, so this reads the Live section's pages and counts rows by host
// (ruling 5). The hub keeps an offline host's last rows, so an offline host's
// count is what "3 live, out of reach" reports.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { NavigationPages } from "../navigationPages";

/** The host_id a live session on the hub's own machine carries. */
export const LOCAL_HOST = "local";

export function liveCountsByHost(
	rows: readonly Pick<NavigationSessionSummary, "host_id">[],
): ReadonlyMap<string, number> {
	const counts = new Map<string, number>();
	for (const row of rows) counts.set(row.host_id, (counts.get(row.host_id) ?? 0) + 1);
	return counts;
}

export function liveSessionsText(count: number, offline: boolean): string {
	if (count === 0) return "No live sessions";
	return offline ? `${count} live, out of reach` : `${count} live`;
}

/** At most this many Live pages are read: 500 sessions, far past the fleets
 * the spec sizes for (section 2's peak is 16 top-level sessions). */
export const LIVE_PAGE_LIMIT = 10;

export class LiveSessionsReader {
	private readonly pages: NavigationPages<NavigationSessionSummary>;

	constructor(client: ConversationClientLike) {
		this.pages = new NavigationPages<NavigationSessionSummary>(
			client,
			{ resource: "section", section: "live" },
			"sessions",
			(row) => row.ref,
			50,
		);
	}

	/** Follows the hub's navigation changes until the returned stop runs. A
	 * sheet starts it from an effect, never while rendering. */
	watch(): () => void {
		return this.pages.watch();
	}

	getSnapshot = () => this.pages.getSnapshot();

	subscribe = (listener: () => void) => this.pages.subscribe(listener);

	/** Reads the first page, then the rest, up to LIVE_PAGE_LIMIT pages. */
	async load(): Promise<void> {
		await this.pages.refresh();
		for (let read = 1; read < LIVE_PAGE_LIMIT && this.pages.getSnapshot().remaining > 0; read++)
			await this.pages.more();
	}

	dispose(): void {
		this.pages.cancel();
	}
}
