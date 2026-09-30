import type {
	HubNotice,
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
import { isMethodNotFound } from "../wireErrors";

type Page<T> = ReturnType<NavigationPages<T>["getSnapshot"]>;
export interface BoardSnapshot {
	/** Sources (hosts, online or not) and section and catalog counts. */
	manifest: NavigationManifest | null;
	live: Page<NavigationSessionSummary>;
	needsYou: Page<NavigationSessionSummary>;
	pins: Page<NavigationPinSectionDescriptor>;
	/** Each category's sessions, keyed by section id in the catalog's order:
	 * one entry for every category the catalog shown lists. */
	pinSections: Record<string, Page<NavigationSessionSummary>>;
	/** True from the first time Live loaded for this hub, and never false again. */
	loaded: boolean;
	/** True while the rows shown were read over an earlier connection and the
	 * current one hasn't replaced them yet. */
	retained: boolean;
	/** True while any of the Board's reads (Live, Needs you, the pin catalog,
	 * a category or the manifest) is out. */
	reading: boolean;
	error: string | null;
	/** The hub's notices (S11): evener/notices/list, then each
	 * evener/notices/changed. A hub without the method has none. */
	notices: HubNotice[];
	/** True once this controller's own first notice read has answered, even
	 * with the list the snapshot already held: the in-app alerts' notice
	 * baseline waits for it. */
	noticesRead: boolean;
}

export interface BoardControllerOptions {
	/** "board" (the default) reads everything the Board shows. "attention"
	 * reads only what the in-app alerts need: Live, Needs you, the manifest
	 * and the notices, and never the pin catalog or its categories. */
	scope?: "board" | "attention";
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
const MANIFEST_PARAMS = { resource: "manifest", representationVersion: 3 };
const MANIFEST_KEY = navigationParamsToResourceKey(MANIFEST_PARAMS);

interface ManifestState {
	manifest: NavigationManifest | null;
	loaded: boolean;
	loading: boolean;
	error: string | null;
}

/** Reads the hub's manifest, and re-reads it whenever an invalidation names
 * it. Invalidations that arrive during a read coalesce into one follow-up
 * read, which is dropped when the read already returned every revision they
 * announced. */
class ManifestReader {
	state: ManifestState = { manifest: null, loaded: false, loading: false, error: null };
	/** Bumped by every read and by pause, so an abandoned read's late answer
	 * is dropped. */
	private request = 0;
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
			if (event.method === "evener/navigation/invalidated") this.invalidate(event.params);
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
		if (!this.state.loading) return;
		this.request++;
		this.owed = true;
		this.publish({ loading: false });
	}
	/** Catch up on invalidations held while paused, and retry a read that
	 * never landed or failed. A failed read doesn't re-arm itself, which
	 * would loop while the link is down; resuming is its retry. */
	resume() {
		this.paused = false;
		if (!this.state.loaded || this.state.error !== null) this.owed = true;
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
		if (gap || targets.some((target) => target.revision === undefined)) this.unversionedAt = this.invalidations;
		this.required = Math.max(this.required, requiredRevision(MANIFEST_KEY, payload.targets));
		this.owed = true;
		this.drain();
	}
	private drain() {
		if (!this.owed || this.state.loading || this.paused || this.disposed) return;
		this.owed = false;
		void this.fetch();
	}
	private async fetch() {
		const request = ++this.request;
		const seen = this.invalidations;
		this.publish({ loading: true });
		try {
			const wire = await this.client.request("evener/navigation/read", MANIFEST_PARAMS);
			if (request !== this.request) return;
			const decoded = decodeNavigationResponse(MANIFEST_KEY, undefined, wire);
			if (decoded.status !== "snapshot") throw new Error("Hub navigation is unavailable.");
			if (
				seen >= this.unversionedAt &&
				(this.generation === "" ||
					(decoded.version.generationId === this.generation && decoded.version.revision >= this.required))
			)
				this.owed = false;
			this.publish({
				manifest: materializeSnapshot(MANIFEST_KEY, decoded) as unknown as NavigationManifest,
				loaded: true,
				loading: false,
				error: null,
			});
		} catch (cause) {
			if (request !== this.request) return;
			this.publish({
				loading: false,
				error: cause instanceof Error ? cause.message : "Could not read the hub's navigation.",
			});
		} finally {
			if (request === this.request) this.drain();
		}
	}
	private publish(change: Partial<ManifestState>) {
		this.state = { ...this.state, ...change };
		this.onChange();
	}
}

/** One category's sessions, and what stops following them. */
interface CategoryReader {
	pages: NavigationPages<NavigationSessionSummary>;
	stop: Array<() => void>;
}
interface Readers {
	live: NavigationPages<NavigationSessionSummary>;
	needsYou: NavigationPages<NavigationSessionSummary>;
	pins: NavigationPages<NavigationPinSectionDescriptor>;
	manifest: ManifestReader;
	/** One reader per category the catalog shown lists, keyed by id. */
	categories: Map<string, CategoryReader>;
	client: ConversationClientLike;
	stop: Array<() => void>;
	/** The latest notice read, so an older answer that lands after a newer
	 * one is dropped. */
	noticeRead: number;
}
/** What the Board showed when its last connection went away, per reader,
 * until the current connection's read of that reader lands. */
interface Retained {
	live: Page<NavigationSessionSummary> | null;
	needsYou: Page<NavigationSessionSummary> | null;
	pins: Page<NavigationPinSectionDescriptor> | null;
	categories: Record<string, Page<NavigationSessionSummary>>;
	manifest: NavigationManifest | null;
}
const nothingRetained: Retained = {
	live: null,
	needsYou: null,
	pins: null,
	categories: {},
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
const categoryPagesOf = (bound: Readers) => [...bound.categories.values()].map((category) => category.pages);

/** A page worth keeping across a reconnect: one that loaded. Its read died
 * with the connection, so it no longer says it is loading. */
function keep<T>(page: Page<T>): Page<T> | null {
	return page.loaded ? { ...page, loading: false } : null;
}
/** The fresh reader's page once it has loaded, the retained copy until then.
 * The retained rows carry the fresh reader's loading and error, so a screen
 * reading them sees what the current connection's read is doing. */
function shown<T>(reader: NavigationPages<T> | undefined, kept: Page<T> | null): Page<T> {
	const fresh = reader?.getSnapshot();
	if (fresh?.loaded) return fresh;
	if (kept && fresh) return { ...kept, loading: fresh.loading, error: fresh.error };
	return kept ?? fresh ?? emptyPage;
}

export function createBoardController({ scope = "board" }: BoardControllerOptions = {}): BoardController {
	const attention = scope === "attention";
	const listeners = new Set<() => void>();
	let readers: Readers | null = null;
	let retained: Retained = nothingRetained;
	let loaded = false;
	let paused = false;
	let disposed = false;
	// The notices outlive a connection: they keep what was last read until
	// the current connection's read lands.
	let notices: HubNotice[] = [];
	let noticesRead = false;

	const build = (): BoardSnapshot => {
		const pins = shown(readers?.pins, retained.pins);
		const categoryPages = readers ? categoryPagesOf(readers) : [];
		return {
			manifest: readers?.manifest.state.loaded ? readers.manifest.state.manifest : retained.manifest,
			live: shown(readers?.live, retained.live),
			needsYou: shown(readers?.needsYou, retained.needsYou),
			pins,
			pinSections: Object.fromEntries(
				pins.rows.map((row) => [
					row.id,
					shown(readers?.categories.get(row.id)?.pages, retained.categories[row.id] ?? null),
				]),
			),
			loaded,
			retained:
				retained.live !== null ||
				retained.needsYou !== null ||
				retained.pins !== null ||
				Object.keys(retained.categories).length > 0 ||
				retained.manifest !== null,
			reading: readers
				? readers.live.getSnapshot().loading ||
					readers.needsYou.getSnapshot().loading ||
					readers.pins.getSnapshot().loading ||
					categoryPages.some((page) => page.getSnapshot().loading) ||
					readers.manifest.state.loading
				: false,
			error: readers
				? (readers.live.getSnapshot().error ??
					readers.needsYou.getSnapshot().error ??
					readers.pins.getSnapshot().error ??
					categoryPages.map((page) => page.getSnapshot().error).find((error) => error !== null) ??
					readers.manifest.state.error)
				: null,
			notices,
			noticesRead,
		};
	};
	let snapshot = build();
	// A reader republishes on cancel() with nothing changed, and every fresh
	// reader starts with its own empty rows, so pages compare by content.
	const samePage = (a: Page<unknown>, b: Page<unknown>) =>
		(Object.keys(a) as Array<keyof Page<unknown>>).every((field) =>
			field === "rows" ? a.rows === b.rows || (!a.rows.length && !b.rows.length) : a[field] === b[field],
		);
	// The same categories on the same pages. Their order needs no check: it
	// follows pins.rows, which unchanged() compares on its own.
	const sameCategories = (a: BoardSnapshot["pinSections"], b: BoardSnapshot["pinSections"]) => {
		const ids = Object.keys(a);
		return ids.length === Object.keys(b).length && ids.every((id) => Object.hasOwn(b, id) && samePage(a[id], b[id]));
	};
	const unchanged = (next: BoardSnapshot) =>
		(Object.keys(next) as Array<keyof BoardSnapshot>).every((field) =>
			field === "live" || field === "needsYou" || field === "pins"
				? samePage(next[field], snapshot[field])
				: field === "pinSections"
					? sameCategories(next.pinSections, snapshot.pinSections)
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
			if (readers.needsYou.getSnapshot().loaded) retained = { ...retained, needsYou: null };
			if (readers.pins.getSnapshot().loaded) retained = { ...retained, pins: null };
			if (readers.manifest.state.loaded) retained = { ...retained, manifest: null };
			followCatalog(readers);
		}
		const next = build();
		if (unchanged(next)) return;
		snapshot = next;
		for (const listener of listeners) listener();
	};

	/** Needs you, the pin catalog and each category must be complete (every
	 * session that needs you, every category's row, and every session a
	 * category counts), so keep paging each until the hub has no more rows. */
	const fill = <T>(page: NavigationPages<T>) => {
		if (paused) return;
		const state = page.getSnapshot();
		if (state.loaded && !state.loading && !state.error && !state.stale && state.remaining > 0) void page.more();
	};

	/** Follow a reader that must be complete: publish each of its changes,
	 * then page on. */
	const followInFull = <T>(page: NavigationPages<T>) =>
		page.subscribe(() => {
			publish();
			fill(page);
		});

	/** Every paged reader: Live, Needs you, the pin catalog and the
	 * categories. */
	const pages = (bound: Readers) =>
		attention ? [bound.live, bound.needsYou] : [bound.live, bound.needsYou, bound.pins, ...categoryPagesOf(bound)];

	/** Keep one reader per category the catalog shown lists (the fresh
	 * catalog once it loaded, the retained one until then), and forget the
	 * retained rows of a category that has left it or whose fresh read has
	 * landed. A category is read whatever its fold, and to completion, since
	 * its header shows the hub's full count. */
	const followCatalog = (bound: Readers) => {
		const listed = new Set(shown(bound.pins, retained.pins).rows.map((row) => row.id));
		for (const [id, category] of bound.categories) {
			if (listed.has(id)) continue;
			bound.categories.delete(id);
			for (const stop of category.stop) stop();
			category.pages.cancel();
		}
		for (const id of listed) {
			if (bound.categories.has(id)) continue;
			const pages = new NavigationPages<NavigationSessionSummary>(
				bound.client,
				{ resource: "pin_section", sectionId: id },
				"sessions",
				sessionKey,
				PAGE_LIMIT,
			);
			// A new reader reads before the Board subscribes to it, so its
			// loading publish reaches no one; the publish() running this
			// builds the reader's state into the snapshot next.
			if (paused) pages.cancel();
			else void pages.refresh();
			bound.categories.set(id, {
				pages,
				stop: [followInFull(pages), pages.watch()],
			});
		}
		const kept = Object.entries(retained.categories).filter(
			([id]) => listed.has(id) && !bound.categories.get(id)?.pages.getSnapshot().loaded,
		);
		if (kept.length !== Object.keys(retained.categories).length)
			retained = { ...retained, categories: Object.fromEntries(kept) };
	};

	/** The notice list a read or a notification carried, or the current one
	 * when nothing in it changed: the snapshot keeps its identity, so a
	 * notification that repeats the list re-renders nothing. Go sends an
	 * empty (nil) slice as null. */
	const takeNotices = (next: HubNotice[] | null | undefined) => {
		const list = next ?? [];
		if (JSON.stringify(list) === JSON.stringify(notices)) return false;
		notices = list;
		return true;
	};
	/** Reads the hub's notices. The read stands apart from the Board's
	 * navigation reads: a failure keeps the last list and says nothing, so it
	 * never counts as the Board's error, holds up first run or starts the
	 * rebind retry; the next resume tries again. A hub without the method
	 * (MethodNotFound) has no notices to show: there is no client-side
	 * fallback (Jesse, 2026-09-29). */
	const readNotices = (bound: Readers) => {
		if (paused) return;
		const request = ++bound.noticeRead;
		bound.client.request("evener/notices/list", {}).then(
			(result) => {
				if (readers !== bound || request !== bound.noticeRead) return;
				const changed = takeNotices(result.notices);
				if (!changed && noticesRead) return;
				noticesRead = true;
				publish();
			},
			(error: unknown) => {
				if (readers !== bound || request !== bound.noticeRead || !isMethodNotFound(error)) return;
				takeNotices([]);
				noticesRead = true;
				publish();
			},
		);
	};

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
			categories: new Map(),
			client,
			stop: [],
			noticeRead: 0,
		};
		bound.stop.push(bound.live.subscribe(publish), followInFull(bound.needsYou));
		if (!attention) bound.stop.push(followInFull(bound.pins));
		for (const page of pages(bound)) bound.stop.push(page.watch());
		bound.stop.push(
			bound.manifest.watch(),
			client.onNotification((event) => {
				if (event.method !== "evener/notices/changed" || readers !== bound) return;
				// The hub orders a list response against a broadcast only while
				// it derives, and never re-broadcasts a list it announced: a read
				// still out is older than this list, so it is dropped.
				bound.noticeRead += 1;
				// A changed list is a baseline too, so the alerts stay live
				// when the read was dropped or failed; a notice that first
				// appears in this list counts as baseline and never alerts.
				const changed = takeNotices(event.params.notices);
				if (!changed && noticesRead) return;
				noticesRead = true;
				publish();
			}),
		);
		if (paused) {
			for (const page of pages(bound)) page.cancel();
			bound.manifest.pause();
		} else {
			for (const page of pages(bound)) void page.refresh();
			bound.manifest.read();
			readNotices(bound);
		}
		return bound;
	};

	const disconnect = () => {
		if (!readers) return;
		const bound = readers;
		readers = null;
		for (const stop of bound.stop) stop();
		for (const category of bound.categories.values()) for (const stop of category.stop) stop();
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
					categories: Object.fromEntries(
						Object.entries(snapshot.pinSections).flatMap(([id, page]) => {
							const kept = keep(page);
							return kept ? [[id, kept]] : [];
						}),
					),
					manifest: snapshot.manifest,
				};
			disconnect();
			if (client) readers = connect(client);
			publish();
		},
		async loadMoreLive() {
			// A paused Board reads nothing new, as the Needs you, pin catalog
			// and category paging doesn't.
			if (paused) return;
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
			// Resuming retries every read that failed, since a failed read
			// doesn't retry itself: a first read the pause dropped (or that
			// failed) starts over, a loaded page catches up on the re-reads it
			// owes, and a later page that failed is read again. A loaded page
			// resumes first either way: more() doesn't lift the pause, and a
			// page left paused would hold every later re-read.
			for (const page of pages(readers)) {
				const state = page.getSnapshot();
				if (!state.loaded && !state.loading) {
					void page.refresh();
					continue;
				}
				page.resume();
				if (state.error && !state.stale && !state.loading && state.remaining > 0) void page.more();
			}
			readers.manifest.resume();
			fill(readers.needsYou);
			fill(readers.pins);
			for (const page of categoryPagesOf(readers)) fill(page);
			readNotices(readers);
		},
		dispose() {
			if (disposed) return;
			disconnect();
			disposed = true;
			listeners.clear();
		},
	};
}
