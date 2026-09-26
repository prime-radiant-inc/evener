import type {
	NavigationInvalidatedPayload,
	NavigationManifest,
	NavigationPinSectionDescriptor,
	NavigationSessionSummary,
} from "@evener/appwire-client";
import {
	decodeNavigationResponse,
	materializeSnapshot,
	navigationParamsToResourceKey,
} from "@evener/appwire-client/state/navigation";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { NavigationPages } from "../navigationPages";
import { singleFlight } from "../singleFlight";

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
/** Needs you is read in full, up to this many rows. */
const NEEDS_YOU_LIMIT = 200;
const PIN_CATALOG_LIMIT = 100;
const MANIFEST_PARAMS = { resource: "manifest", representationVersion: 2 };
const MANIFEST_KEY = navigationParamsToResourceKey(MANIFEST_PARAMS);

interface ManifestState {
	manifest: NavigationManifest | null;
	loaded: boolean;
	error: string | null;
}

/** Reads the hub's manifest, and re-reads it whenever an invalidation names
 * it. Invalidations that arrive during a read coalesce into one trailing
 * read, which is dropped when the read already returned the revision they
 * announced. */
class ManifestReader {
	state: ManifestState = { manifest: null, loaded: false, error: null };
	private paused = false;
	private disposed = false;
	private generation = "";
	private required = 0;
	private flight = singleFlight(
		() => this.fetch(),
		() => !this.paused && !this.disposed,
	);
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
		this.flight.request();
	}
	pause() {
		this.paused = true;
	}
	/** Catch up on invalidations held while paused, and retry a first read
	 * that never landed. */
	resume() {
		this.paused = false;
		if (!this.state.loaded && !this.flight.running) this.flight.request();
		else this.flight.drain();
	}
	dispose() {
		this.disposed = true;
	}
	private invalidate(payload: NavigationInvalidatedPayload) {
		const targets = payload.targets.filter(
			(target) => target.kind === "manifest",
		);
		if (!targets.length) return;
		if (payload.generationId !== this.generation) {
			this.generation = payload.generationId;
			this.required = 0;
		}
		// A target without a revision can't be proven read, so it always
		// earns a read of its own.
		this.required = Math.max(
			this.required,
			...targets.map((target) => target.revision ?? Number.POSITIVE_INFINITY),
		);
		this.flight.request();
	}
	private async fetch() {
		try {
			const wire = await this.client.request(
				"evener/navigation/read",
				MANIFEST_PARAMS,
			);
			if (this.disposed) return;
			const decoded = decodeNavigationResponse(MANIFEST_KEY, undefined, wire);
			if (decoded.status !== "snapshot")
				throw new Error("Hub navigation is unavailable.");
			if (
				decoded.version.generationId === this.generation &&
				decoded.version.revision >= this.required
			)
				this.flight.settle();
			this.publish({
				manifest: materializeSnapshot(
					MANIFEST_KEY,
					decoded,
				) as unknown as NavigationManifest,
				loaded: true,
				error: null,
			});
		} catch (cause) {
			if (this.disposed) return;
			this.publish({
				error:
					cause instanceof Error
						? cause.message
						: "Could not read the hub's navigation.",
			});
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
/** The fresh reader's page once it has loaded, the retained copy until then. */
function shown<T>(
	reader: NavigationPages<T> | undefined,
	kept: Page<T> | null,
): Page<T> {
	const fresh = reader?.getSnapshot();
	if (fresh?.loaded) return fresh;
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
			retained.manifest !== null,
		error: readers
			? (readers.live.getSnapshot().error ??
				readers.needsYou.getSnapshot().error ??
				readers.pins.getSnapshot().error ??
				readers.manifest.state.error)
			: null,
	});
	let snapshot = build();
	const unchanged = (next: BoardSnapshot) =>
		(Object.keys(next) as Array<keyof BoardSnapshot>).every(
			(field) => next[field] === snapshot[field],
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
	 * rows or the Board holds its cap. */
	const fillNeedsYou = () => {
		if (!readers || paused) return;
		const page = readers.needsYou.getSnapshot();
		if (
			page.loaded &&
			!page.loading &&
			!page.error &&
			!page.stale &&
			page.remaining > 0 &&
			page.rows.length < NEEDS_YOU_LIMIT
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
