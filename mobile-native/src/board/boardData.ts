import type {
	NavigationInvalidatedPayload,
	NavigationManifest,
	NavigationPinSectionDescriptor,
	NavigationSessionSummary,
} from "@evener/appwire-client";
import {
	decodeNavigationResponse,
	isSequenceGap,
	matchingTargets,
	materializeSnapshot,
	navigationParamsToResourceKey,
	requiredRevision,
} from "@evener/appwire-client/state/navigation";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { NavigationPages } from "../navigationPages";

type Page<T> = ReturnType<NavigationPages<T>["getSnapshot"]>;
export interface BoardSnapshot {
	/** Sources (hosts, online or not) and section and catalog counts. */
	manifest: NavigationManifest | null;
	live: Page<NavigationSessionSummary>;
	needsYou: Page<NavigationSessionSummary>;
	pins: Page<NavigationPinSectionDescriptor>;
	/** True from the first time Live loaded for this hub, and never false again. */
	loaded: boolean;
	/** True while the rows shown were read over an earlier connection and the
	 * current one hasn't replaced them yet. */
	retained: boolean;
	error: string | null;
}
export interface BoardController {
	getSnapshot(): BoardSnapshot;
	subscribe(listener: () => void): () => void;
	/** Bind to the hub's current client, or null while disconnected or in the
	 * background. Rows already shown stay until the new client's reads land. */
	setClient(client: ConversationClientLike | null): void;
	loadMoreLive(): Promise<void>;
	pause(): void;
	resume(): void;
	dispose(): void;
}

/** The hub caps a section page at 50 rows. */
const PAGE_LIMIT = 50;
const PIN_CATALOG_LIMIT = 100;
const MANIFEST_PARAMS = { resource: "manifest", representationVersion: 2 };
const MANIFEST_KEY = navigationParamsToResourceKey(MANIFEST_PARAMS);

interface ManifestState {
	manifest: NavigationManifest | null;
	loaded: boolean;
	error: string | null;
}

/** Reads the hub's manifest, and re-reads it whenever an invalidation names
 * it. Invalidations that arrive during a read coalesce into one follow-up
 * read, which is dropped when the read already returned every revision they
 * announced. */
class ManifestReader {
	state: ManifestState = { manifest: null, loaded: false, error: null };
	/** Bumped by every read and by pause, so an abandoned read's late answer
	 * is dropped. */
	private request = 0;
	private loading = false;
	/** A read is owed: the first one, or one an invalidation asked for that no
	 * landed read has covered yet. */
	private owed = false;
	private paused = false;
	private disposed = false;
	private generation = "";
	private required = 0;
	/** The last notification sequence accepted in this generation. */
	private sequence = 0;
	/** Invalidations seen, and the count as of the last one without a
	 * revision: only a read started after that one can cover it. */
	private invalidations = 0;
	private unversionedAt = 0;
	constructor(
		private client: ConversationClientLike,
		private onChange: () => void,
	) {}
	watch() {
		return this.client.onNotification((event) => {
			if (event.method === "evener/navigation/invalidated")
				this.invalidate(event.params);
		});
	}
	read() {
		this.owed = true;
		this.drain();
	}
	/** Abandon the read in flight: a connection held through the background
	 * may never answer it. */
	pause() {
		this.paused = true;
		if (!this.loading) return;
		this.request++;
		this.loading = false;
		this.owed = true;
	}
	/** Catch up on invalidations held while paused, and retry a first read
	 * that never landed. */
	resume() {
		this.paused = false;
		if (!this.state.loaded) this.owed = true;
		this.drain();
	}
	dispose() {
		this.disposed = true;
		this.request++;
	}
	private invalidate(payload: NavigationInvalidatedPayload) {
		if (payload.generationId !== this.generation) {
			this.generation = payload.generationId;
			this.required = 0;
			this.sequence = 0;
		}
		if (payload.sequence <= this.sequence) return;
		// A skipped sequence means an invalidation was missed, and it may have
		// named the manifest, so it earns a read like one with no revision.
		const gap = isSequenceGap(this.sequence, payload.sequence);
		this.sequence = payload.sequence;
		const targets = matchingTargets(MANIFEST_KEY, payload.targets);
		if (!targets.length && !gap) return;
		this.invalidations++;
		if (gap || targets.some((target) => target.revision === undefined))
			this.unversionedAt = this.invalidations;
		this.required = Math.max(
			this.required,
			requiredRevision(MANIFEST_KEY, payload.targets),
		);
		this.owed = true;
		this.drain();
	}
	private drain() {
		if (!this.owed || this.loading || this.paused || this.disposed) return;
		this.owed = false;
		void this.fetch();
	}
	private async fetch() {
		const request = ++this.request;
		const seen = this.invalidations;
		this.loading = true;
		try {
			const wire = await this.client.request(
				"evener/navigation/read",
				MANIFEST_PARAMS,
			);
			if (request !== this.request) return;
			const decoded = decodeNavigationResponse(MANIFEST_KEY, undefined, wire);
			if (decoded.status !== "snapshot")
				throw new Error("Hub navigation is unavailable.");
			if (
				seen >= this.unversionedAt &&
				(this.generation === "" ||
					(decoded.version.generationId === this.generation &&
						decoded.version.revision >= this.required))
			)
				this.owed = false;
			this.publish({
				manifest: materializeSnapshot(
					MANIFEST_KEY,
					decoded,
				) as unknown as NavigationManifest,
				loaded: true,
				error: null,
			});
		} catch (cause) {
			if (request !== this.request) return;
			this.publish({
				error:
					cause instanceof Error
						? cause.message
						: "Could not read the hub's navigation.",
			});
		} finally {
			if (request === this.request) {
				this.loading = false;
				this.drain();
			}
		}
	}
	private publish(change: Partial<ManifestState>) {
		this.state = { ...this.state, ...change };
		this.onChange();
	}
}

interface Readers {
	live: NavigationPages<NavigationSessionSummary>;
	needsYou: NavigationPages<NavigationSessionSummary>;
	pins: NavigationPages<NavigationPinSectionDescriptor>;
	manifest: ManifestReader;
	stop: Array<() => void>;
}
/** What the Board showed when its last connection went away, per reader,
 * until the current connection's read of that reader lands. */
interface Retained {
	live: Page<NavigationSessionSummary> | null;
	needsYou: Page<NavigationSessionSummary> | null;
	pins: Page<NavigationPinSectionDescriptor> | null;
	manifest: NavigationManifest | null;
}
const nothingRetained: Retained = {
	live: null,
	needsYou: null,
	pins: null,
	manifest: null,
};
const emptyPage = {
	loaded: false,
	truncated: false,
	rows: [],
	remaining: 0,
	loading: false,
	error: null,
	stale: false,
};
const sessionKey = (row: NavigationSessionSummary) => row.ref;
const pinSectionKey = (row: NavigationPinSectionDescriptor) => row.id;

/** A page worth keeping across a reconnect: one that loaded. Its read died
 * with the connection, so it no longer says it is loading. */
function keep<T>(page: Page<T>): Page<T> | null {
	return page.loaded ? { ...page, loading: false } : null;
}
/** The fresh reader's page once it has loaded, the retained copy until then.
 * The retained rows carry the fresh reader's loading and error, so a screen
 * reading them sees what the current connection's read is doing. */
function shown<T>(
	reader: NavigationPages<T> | undefined,
	kept: Page<T> | null,
): Page<T> {
	const fresh = reader?.getSnapshot();
	if (fresh?.loaded) return fresh;
	if (kept && fresh)
		return { ...kept, loading: fresh.loading, error: fresh.error };
	return kept ?? fresh ?? emptyPage;
}

export function createBoardController(): BoardController {
	const listeners = new Set<() => void>();
	let readers: Readers | null = null;
	let retained: Retained = nothingRetained;
	let loaded = false;
	let paused = false;
	let disposed = false;

	const build = (): BoardSnapshot => ({
		manifest: readers?.manifest.state.loaded
			? readers.manifest.state.manifest
			: retained.manifest,
		live: shown(readers?.live, retained.live),
		needsYou: shown(readers?.needsYou, retained.needsYou),
		pins: shown(readers?.pins, retained.pins),
		loaded,
		retained:
			retained.live !== null ||
			retained.needsYou !== null ||
			retained.pins !== null ||
			retained.manifest !== null,
		error: readers
			? (readers.live.getSnapshot().error ??
				readers.needsYou.getSnapshot().error ??
				readers.pins.getSnapshot().error ??
				readers.manifest.state.error)
			: null,
	});
	let snapshot = build();
	// A reader republishes on cancel() with nothing changed, and every fresh
	// reader starts with its own empty rows, so pages compare by content.
	const samePage = (a: Page<unknown>, b: Page<unknown>) =>
		(Object.keys(a) as Array<keyof Page<unknown>>).every((field) =>
			field === "rows"
				? a.rows === b.rows || (!a.rows.length && !b.rows.length)
				: a[field] === b[field],
		);
	const unchanged = (next: BoardSnapshot) =>
		(Object.keys(next) as Array<keyof BoardSnapshot>).every((field) =>
			field === "live" || field === "needsYou" || field === "pins"
				? samePage(next[field], snapshot[field])
				: next[field] === snapshot[field],
		);

	const publish = () => {
		if (disposed) return;
		if (readers) {
			// A fresh read that landed replaces what the last connection showed.
			if (readers.live.getSnapshot().loaded) {
				retained = { ...retained, live: null };
				loaded = true;
			}
			if (readers.needsYou.getSnapshot().loaded)
				retained = { ...retained, needsYou: null };
			if (readers.pins.getSnapshot().loaded)
				retained = { ...retained, pins: null };
			if (readers.manifest.state.loaded)
				retained = { ...retained, manifest: null };
		}
		const next = build();
		if (unchanged(next)) return;
		snapshot = next;
		for (const listener of listeners) listener();
	};

	/** Needs you must be complete, so keep paging until the hub has no more
	 * rows. */
	const fillNeedsYou = () => {
		if (!readers || paused) return;
		const page = readers.needsYou.getSnapshot();
		if (
			page.loaded &&
			!page.loading &&
			!page.error &&
			!page.stale &&
			page.remaining > 0
		)
			void readers.needsYou.more();
	};

	const pages = (bound: Readers) => [bound.live, bound.needsYou, bound.pins];

	const connect = (client: ConversationClientLike): Readers => {
		const section = (name: "live" | "needs_you") =>
			new NavigationPages<NavigationSessionSummary>(
				client,
				{ resource: "section", section: name },
				"sessions",
				sessionKey,
				PAGE_LIMIT,
			);
		const bound: Readers = {
			live: section("live"),
			needsYou: section("needs_you"),
			pins: new NavigationPages<NavigationPinSectionDescriptor>(
				client,
				{ resource: "pin_catalog" },
				"pin_sections",
				pinSectionKey,
				PIN_CATALOG_LIMIT,
			),
			manifest: new ManifestReader(client, publish),
			stop: [],
		};
		bound.stop.push(
			bound.live.subscribe(publish),
			bound.needsYou.subscribe(() => {
				publish();
				fillNeedsYou();
			}),
			bound.pins.subscribe(publish),
		);
		for (const page of pages(bound)) bound.stop.push(page.watch());
		bound.stop.push(bound.manifest.watch());
		if (paused) {
			for (const page of pages(bound)) page.cancel();
			bound.manifest.pause();
		} else {
			for (const page of pages(bound)) void page.refresh();
			bound.manifest.read();
		}
		return bound;
	};

	const disconnect = () => {
		if (!readers) return;
		const bound = readers;
		readers = null;
		for (const stop of bound.stop) stop();
		for (const page of pages(bound)) page.cancel();
		bound.manifest.dispose();
	};

	return {
		getSnapshot: () => snapshot,
		subscribe(listener) {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		setClient(client) {
			if (disposed) return;
			if (readers)
				retained = {
					live: keep(snapshot.live),
					needsYou: keep(snapshot.needsYou),
					pins: keep(snapshot.pins),
					manifest: snapshot.manifest,
				};
			disconnect();
			if (client) readers = connect(client);
			publish();
		},
		async loadMoreLive() {
			await readers?.live.more();
		},
		pause() {
			paused = true;
			if (!readers) return;
			for (const page of pages(readers)) page.cancel();
			readers.manifest.pause();
		},
		resume() {
			paused = false;
			if (!readers) return;
			for (const page of pages(readers)) {
				const state = page.getSnapshot();
				// A first read the pause dropped (or that failed) starts over;
				// a loaded page catches up on the re-reads it owes.
				if (!state.loaded && !state.loading) void page.refresh();
				else page.resume();
			}
			readers.manifest.resume();
			fillNeedsYou();
		},
		dispose() {
			if (disposed) return;
			disconnect();
			disposed = true;
			listeners.clear();
		},
	};
}
