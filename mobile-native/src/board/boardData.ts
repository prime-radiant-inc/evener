import type {
	AuthStatusResponse,
	NavigationInvalidatedPayload,
	NavigationManifest,
	NavigationPinSectionDescriptor,
	NavigationSessionSummary,
	PluginEntry,
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
	/** True while any of the Board's reads (Live, Needs you, the pin catalog
	 * or the manifest) is out. */
	reading: boolean;
	error: string | null;
	/** The hub's providers, for the sign-in notices. */
	auth: AuthStatusResponse[];
	/** The hub's plugins, for the broken-plugin notices. */
	plugins: PluginEntry[];
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
/** evener/plugin/updated fires only on mutations, so a plugin that breaks on
 * its own shows up on this poll (ruling 8). */
const PLUGIN_POLL = 5 * 60_000;

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
		if (!this.owed || this.state.loading || this.paused || this.disposed) return;
		this.owed = false;
		void this.fetch();
	}
	private async fetch() {
		const request = ++this.request;
		const seen = this.invalidations;
		this.publish({ loading: true });
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
				loading: false,
				error: null,
			});
		} catch (cause) {
			if (request !== this.request) return;
			this.publish({
				loading: false,
				error:
					cause instanceof Error
						? cause.message
						: "Could not read the hub's navigation.",
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

interface Readers {
	client: ConversationClientLike;
	live: NavigationPages<NavigationSessionSummary>;
	needsYou: NavigationPages<NavigationSessionSummary>;
	pins: NavigationPages<NavigationPinSectionDescriptor>;
	manifest: ManifestReader;
	stop: Array<() => void>;
	/** The latest read of each notice list, so an older answer that lands
	 * after a newer one is dropped. */
	noticeReads: { auth: number; plugins: number };
	pluginPoll: ReturnType<typeof setInterval> | null;
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
	// The notice lists outlive a connection: each keeps what it last read
	// until the current connection's read of it lands.
	let auth: AuthStatusResponse[] = [];
	let plugins: PluginEntry[] = [];

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
		reading: readers
			? readers.live.getSnapshot().loading ||
				readers.needsYou.getSnapshot().loading ||
				readers.pins.getSnapshot().loading ||
				readers.manifest.state.loading
			: false,
		error: readers
			? (readers.live.getSnapshot().error ??
				readers.needsYou.getSnapshot().error ??
				readers.pins.getSnapshot().error ??
				readers.manifest.state.error)
			: null,
		auth,
		plugins,
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

	/** Needs you and the pin catalog must be complete (every session that
	 * needs you, and every category's row), so keep paging each until the
	 * hub has no more rows. */
	const fill = <T,>(page: NavigationPages<T>) => {
		if (paused) return;
		const state = page.getSnapshot();
		if (
			state.loaded &&
			!state.loading &&
			!state.error &&
			!state.stale &&
			state.remaining > 0
		)
			void page.more();
	};

	const pages = (bound: Readers) => [bound.live, bound.needsYou, bound.pins];

	/** Reads one of the lists the notices come from. These reads stand apart
	 * from the Board's navigation reads: a failure keeps the last list and
	 * says nothing, so it never counts as the Board's error, holds up first
	 * run or starts the rebind retry. The next focus, auth update or poll
	 * tries it again. */
	const readNoticeList = <T,>(
		bound: Readers,
		list: keyof Readers["noticeReads"],
		read: () => Promise<T>,
		land: (value: T) => void,
	) => {
		if (paused) return;
		const request = ++bound.noticeReads[list];
		read().then(
			(value) => {
				if (readers !== bound || request !== bound.noticeReads[list]) return;
				land(value);
				publish();
			},
			() => {},
		);
	};
	const readAuth = (bound: Readers) =>
		readNoticeList(
			bound,
			"auth",
			() => bound.client.request("evener/auth/list", {}),
			(result) => {
				auth = result.providers;
			},
		);
	const readPlugins = (bound: Readers) =>
		readNoticeList(
			bound,
			"plugins",
			() => bound.client.request("evener/plugin/list", {}),
			(result) => {
				plugins = result.plugins;
			},
		);
	const stopPluginPoll = (bound: Readers) => {
		if (bound.pluginPoll !== null) clearInterval(bound.pluginPoll);
		bound.pluginPoll = null;
	};
	/** Reads both notice lists and polls the plugins from now on. */
	const readNoticeLists = (bound: Readers) => {
		readAuth(bound);
		readPlugins(bound);
		stopPluginPoll(bound);
		bound.pluginPoll = setInterval(() => readPlugins(bound), PLUGIN_POLL);
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
			client,
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
			noticeReads: { auth: 0, plugins: 0 },
			pluginPoll: null,
		};
		bound.stop.push(
			bound.live.subscribe(publish),
			bound.needsYou.subscribe(() => {
				publish();
				fill(bound.needsYou);
			}),
			bound.pins.subscribe(() => {
				publish();
				fill(bound.pins);
			}),
		);
		for (const page of pages(bound)) bound.stop.push(page.watch());
		bound.stop.push(
			bound.manifest.watch(),
			client.onNotification((event) => {
				if (event.method === "evener/auth/updated") readAuth(bound);
			}),
		);
		if (paused) {
			for (const page of pages(bound)) page.cancel();
			bound.manifest.pause();
		} else {
			for (const page of pages(bound)) void page.refresh();
			bound.manifest.read();
			readNoticeLists(bound);
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
		stopPluginPoll(bound);
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
			// A paused Board reads nothing new, as the Needs you and pin
			// catalog paging doesn't.
			if (paused) return;
			await readers?.live.more();
		},
		pause() {
			paused = true;
			if (!readers) return;
			for (const page of pages(readers)) page.cancel();
			readers.manifest.pause();
			stopPluginPoll(readers);
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
			readNoticeLists(readers);
		},
		dispose() {
			if (disposed) return;
			disconnect();
			disposed = true;
			listeners.clear();
		},
	};
}
