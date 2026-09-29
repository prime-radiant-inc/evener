// In-app alerts (spec 13.3) as a small state machine with no React and no
// native modules: which banner shows, which alerts wait while you read or
// type, and which sessions alerted you most recently, the order Next serves
// first (spec 8.3). The timing and coalescing are the prototype's
// (docs/design/mobile/redesign/prototype/core.js, EV.alert and releaseHeld),
// which the usability rounds tested (findings.md: round 1 problems 2 and 3,
// round 3 problem 1, round 4 problem 2).
import type { WhyLine } from "../board/attention";

/** The Board states that put a session in Needs you (spec 13.2). */
export type NeedsYouKind = "failed" | "question" | "approval" | "warning" | "restartNeeded";

export interface SessionAlert {
	/** "started": a session you started opened while you were somewhere else
	 * (New session's start landing after you left the sheet), so it could be
	 * started twice without it. */
	kind: NeedsYouKind | "finished" | "started";
	ref: string;
	title: string;
	why: WhyLine | null;
}

export interface NoticeAlert {
	kind: "notice";
	/** The notice's own key (board/notices.ts), which says what it opens. */
	key: string;
	title: string;
}

/** A start New session sent failed, or couldn't be confirmed, after its sheet
 * closed (#3104): nothing else would say so, and the draft waits in New
 * session. */
export interface StartFailedAlert {
	kind: "startFailed";
	/** The hub it was started on, whichever hub is selected when it lands. */
	hubId: string;
	hubName: string;
	/** The start reached the hub, so the session may exist. */
	uncertain: boolean;
}

export type Alert = SessionAlert | NoticeAlert | StartFailedAlert;

/** Hub > In-app alerts (spec 12). Read-only, since the defaults and each
 * snapshot are shared by reference: the store, the center and the page all
 * hold the same object. */
export interface AlertPreferences {
	readonly failures: boolean;
	/** Questions and approvals. */
	readonly questions: boolean;
	readonly finished: boolean;
	/** Hold alerts while reading or typing. */
	readonly hold: boolean;
	readonly haptics: boolean;
}

export const DEFAULT_ALERT_PREFERENCES: AlertPreferences = {
	failures: true,
	questions: true,
	finished: false,
	hold: true,
	haptics: true,
};

export interface Banner {
	/** New for each banner that drops in; kept while a banner updates in place. */
	id: number;
	/** One alert, or two or more about sessions that need you ("3 sessions need you"). */
	alerts: readonly Alert[];
}

export interface AlertSnapshot {
	banner: Banner | null;
	/** Alerts waiting while you read or type: one per session or notice. */
	held: number;
	/** Sessions that alerted you, most recent first. */
	recent: readonly string[];
}

/** What is on screen, as far as alerts care. */
export type AlertScreen = { kind: "board" } | { kind: "session"; ref: string } | { kind: "other" };

/** Where a tapped banner goes. */
export type BannerTarget =
	| { kind: "session"; ref: string; title: string }
	| { kind: "needsYou" }
	| { kind: "notice"; key: string }
	| { kind: "newSession"; hubId: string; hubName: string };

export type Haptic = "warning" | "light";

/** Why banners wait (ruling 7). "quiet" is the Reader or typing, which Hub >
 * In-app alerts' Hold switch governs; "covered" is a sheet or modal on top,
 * which always holds, since nothing shows above one. */
export type HoldKind = "quiet" | "covered";

/** The clock the center runs on: the real one in the app, a fake in tests. */
export interface AlertTimer {
	now(): number;
	setTimeout(callback: () => void, ms: number): unknown;
	clearTimeout(handle: unknown): void;
}

/** A banner stays 8 seconds (spec 13.3). */
export const BANNER_MS = 8_000;
/** Alerts within 5 seconds combine (spec 13.3). */
export const COALESCE_MS = 5_000;
/** Held banners show this long after the last hold ends, so a quick return
 * holds again and the screen you land on counts first (ruling 7). */
export const RELEASE_MS = 200;
const RECENT_LIMIT = 20;

/** Which kinds are sessions that need you, every kind named so a new one
 * has to be decided here. */
const NEEDS_YOU: Record<Alert["kind"], boolean> = {
	failed: true,
	question: true,
	approval: true,
	warning: true,
	restartNeeded: true,
	finished: false,
	started: false,
	notice: false,
	startFailed: false,
};

export function needsYou(alert: Alert): alert is SessionAlert & { kind: NeedsYouKind } {
	return NEEDS_YOU[alert.kind];
}

/** The session an alert is about, or null for a notice or a failed start. */
export function sessionRef(alert: Alert): string | null {
	return alert.kind === "notice" || alert.kind === "startFailed" ? null : alert.ref;
}

/** An alert about what you did yourself in New session: it never joins or
 * replaces a banner that is up, and it is never lost. It follows that banner,
 * or waits out a hold, since you may otherwise start it again. */
function followsInTurn(alert: Alert): boolean {
	return alert.kind === "started" || alert.kind === "startFailed";
}

/** An alert that brings news rather than asks for you: no haptic, and it
 * never joins, replaces or waits behind another banner. */
export function quiet(alert: Alert): boolean {
	return alert.kind === "finished" || alert.kind === "started";
}

function subject(alert: Alert): string {
	if (alert.kind === "notice") return `notice:${alert.key}`;
	// One failed start per hub: the newest on a hub says it.
	if (alert.kind === "startFailed") return `startFailed:${alert.hubId}`;
	return `session:${alert.ref}`;
}

export class AlertCenter {
	private banner: Banner | null = null;
	private shownAt = 0;
	private expiry: unknown = null;
	private releasing: unknown = null;
	private touching = false;
	private held: Alert[] = [];
	/** Sessions you started that waited out a hold behind sessions needing
	 * you, oldest first, and a held notice that came with them: each shows in
	 * turn once the banner ahead of it goes, so none is lost. */
	private afterBanner: Alert[] = [];
	/** Alerts that have buzzed once: one shown again (a failed start a hub
	 * switch keeps) doesn't buzz again. */
	private buzzed = new WeakSet<Alert>();
	private recent: string[] = [];
	private holds = new Map<symbol, HoldKind>();
	private screen: AlertScreen = { kind: "other" };
	private preferences: AlertPreferences = DEFAULT_ALERT_PREFERENCES;
	private nextId = 1;
	private snapshot: AlertSnapshot = { banner: null, held: 0, recent: [] };
	private listeners = new Set<() => void>();

	constructor(
		private readonly timer: AlertTimer,
		private readonly haptic: (kind: Haptic) => void = () => {},
	) {}

	getSnapshot = (): AlertSnapshot => this.snapshot;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	offer(alert: Alert): void {
		if (!this.wanted(alert)) return;
		if (needsYou(alert)) this.remember(alert.ref);
		// A finished result is the quietest alert: it never joins or replaces a
		// banner that is up, a notice's included, and never waits (spec 13.3;
		// the prototype's EV.alert drops it behind any banner).
		if (alert.kind === "finished" && (this.banner !== null || this.holding())) return;
		// A session you started, or a start that failed, never joins or replaces
		// a banner that is up either, but it is never lost: it follows that
		// banner, or waits out a hold. It lands while you're elsewhere, often
		// reading or typing, and without it you may start it again.
		if (followsInTurn(alert) && this.banner !== null && !this.holding()) {
			this.queueAfterBanner([alert]);
			this.publish();
			return;
		}
		if (this.holding()) {
			this.held = [...this.held.filter((waiting) => subject(waiting) !== subject(alert)), alert];
			this.publish();
			return;
		}
		this.show(alert);
	}

	/** What is on screen. Looking at a session answers its alerts: it leaves
	 * the banner, the held alerts and the recent order. The Board lists every
	 * notice, so going there answers the notices the same way. */
	setScreen(screen: AlertScreen): void {
		this.screen = screen;
		if (screen.kind === "other") return;
		const about = (alert: Alert) =>
			screen.kind === "board" ? alert.kind === "notice" : sessionRef(alert) === screen.ref;
		const recent = screen.kind === "session" ? this.recent.filter((ref) => ref !== screen.ref) : this.recent;
		const dropped = this.dropAlerts(about);
		if (!dropped && recent.length === this.recent.length) return;
		this.recent = recent;
		this.publish();
	}

	/** A session stopped needing you (the feed saw it leave Needs you): an
	 * alert about it that waits or still shows would be stale news, so it
	 * goes. The recent order stays; Next reads who needs you now. */
	retract(ref: string): void {
		if (this.dropAlerts((alert) => sessionRef(alert) === ref)) this.publish();
	}

	/** A hub's failed start needs no alert any more: its New session is open
	 * and shows why itself, or the hub was removed with its draft. Another
	 * hub's stays. */
	startFailureSeen(hubId: string): void {
		if (this.dropAlerts((alert) => alert.kind === "startFailed" && alert.hubId === hubId)) this.publish();
	}

	setPreferences(preferences: AlertPreferences): void {
		const wasHolding = this.holding();
		this.preferences = preferences;
		if (wasHolding && !this.holding()) this.scheduleRelease();
	}

	/** Holds banners until the returned function runs: the Reader and typing
	 * each hold a "quiet" one, and a sheet a "covered" one (ruling 7). A
	 * release already on its way is left alone: release() checks again when
	 * it fires, so only a hold that holds keeps it back, and a "quiet" one
	 * with the Hold switch off doesn't. */
	hold(kind: HoldKind): () => void {
		const token = Symbol("hold");
		this.holds.set(token, kind);
		return () => {
			if (this.holds.delete(token) && !this.holding()) this.scheduleRelease();
		};
	}

	/** A finger on the banner keeps it up (spec 13.3). */
	touch(down: boolean): void {
		this.touching = down;
	}

	/** Swiped up. */
	dismiss(): void {
		if (this.banner === null) return;
		this.stopBanner();
		this.showAfterBanner();
	}

	/** Tapped: the banner goes, and the caller opens where it points. */
	tap(): BannerTarget | null {
		const banner = this.banner;
		if (banner === null) return null;
		this.stopBanner();
		this.showAfterBanner();
		const [only] = banner.alerts;
		if (only === undefined || banner.alerts.length > 1) return { kind: "needsYou" };
		if (only.kind === "notice") return { kind: "notice", key: only.key };
		if (only.kind === "startFailed") return { kind: "newSession", hubId: only.hubId, hubName: only.hubName };
		return { kind: "session", ref: only.ref, title: only.title };
	}

	/** Next took you on: it serves the held sessions itself now, so they
	 * don't drop in later (the prototype's goNext). */
	nextUsed(): void {
		// Next serves sessions that need you; a session you started, or a start
		// that failed, isn't one, so it keeps waiting.
		const kept = this.held.filter(followsInTurn);
		if (kept.length === this.held.length) return;
		this.held = kept;
		this.publish();
	}

	/** Another hub, or none: nothing carries over, not even what is on screen,
	 * whose ref named the old hub's session, except a failed start, which
	 * names its own hub and would otherwise be lost (#3104). The provider
	 * reports the screen again after a reset. */
	reset(): void {
		const failedStarts = [...(this.banner?.alerts ?? []), ...this.afterBanner, ...this.held].filter(
			(alert) => alert.kind === "startFailed",
		);
		this.stopBanner();
		this.cancelRelease();
		this.held = [];
		this.afterBanner = [];
		this.recent = [];
		this.screen = { kind: "other" };
		this.queueAfterBanner(failedStarts);
		this.showAfterBanner();
	}

	/** Takes the alerts `about` matches out of the held ones and the banner,
	 * keeping the banner's id while anything is left on it; says whether
	 * anything went. */
	private dropAlerts(about: (alert: Alert) => boolean): boolean {
		const held = this.held.filter((alert) => !about(alert));
		const shown = this.banner?.alerts ?? [];
		const kept = shown.filter((alert) => !about(alert));
		const afterBanner = this.afterBanner.filter((alert) => !about(alert));
		if (
			held.length === this.held.length &&
			kept.length === shown.length &&
			afterBanner.length === this.afterBanner.length
		)
			return false;
		this.held = held;
		this.afterBanner = afterBanner;
		if (this.banner !== null) {
			if (kept.length > 0) this.banner = { id: this.banner.id, alerts: kept };
			else {
				// The banner was answered: what waited behind it is next.
				this.stopBanner();
				this.showAfterBanner();
			}
		}
		return true;
	}

	private wanted(alert: Alert): boolean {
		// A start that failed is about what you just did, whatever the settings.
		if (alert.kind === "startFailed") return true;
		const { failures, questions, finished } = this.preferences;
		if (alert.kind === "failed" && !failures) return false;
		if ((alert.kind === "question" || alert.kind === "approval") && !questions) return false;
		if (alert.kind === "finished" && !finished) return false;
		// Nothing alerts about what is on screen: the session you're looking
		// at, or a notice while the Board, which lists it, is up.
		if (alert.kind === "notice") return this.screen.kind !== "board";
		return !(this.screen.kind === "session" && this.screen.ref === alert.ref);
	}

	private holding(): boolean {
		for (const kind of this.holds.values()) if (kind === "covered" || this.preferences.hold) return true;
		return false;
	}

	private remember(ref: string): void {
		this.recent = [ref, ...this.recent.filter((other) => other !== ref)].slice(0, RECENT_LIMIT);
	}

	private show(alert: Alert): void {
		const now = this.timer.now();
		const current = this.banner;
		if (current !== null && needsYou(alert) && current.alerts.every(needsYou) && now - this.shownAt < COALESCE_MS) {
			// Within 5 seconds, alerts about sessions that need you combine into
			// one banner, each session once (ruling 6).
			const joined = current.alerts.some((other) => subject(other) === subject(alert));
			this.banner = {
				id: current.id,
				alerts: joined
					? current.alerts.map((other) => (subject(other) === subject(alert) ? alert : other))
					: [...current.alerts, alert],
			};
		} else {
			this.replaceBanner([alert]);
			if (!quiet(alert) && !this.buzzed.has(alert)) {
				this.buzzed.add(alert);
				this.buzz(alert.kind === "failed" || alert.kind === "startFailed" ? "warning" : "light");
			}
		}
		this.shownAt = now;
		this.armExpiry();
		this.publish();
	}

	private release(): void {
		this.releasing = null;
		// A hold that began after the release was scheduled keeps it all back.
		if (this.holding()) return;
		const waiting = this.held.filter((alert) => this.wanted(alert));
		this.held = [];
		const current = this.banner;
		const showing = current?.alerts.every(needsYou) ? current.alerts : [];
		const sessions = waiting.filter(needsYou);
		// Sessions you started, and failed starts, always follow in turn, never
		// dropped.
		this.queueAfterBanner(waiting.filter(followsInTurn));
		if (sessions.length === 0) {
			// With no session waiting and no banner up, the oldest session you
			// started shows first; else a held notice shows, only the latest. The
			// Board lists both either way (the prototype's releaseHeld).
			if (showing.length > 0) {
				this.publish();
				return;
			}
			const notice = [...waiting].reverse().find((alert) => alert.kind === "notice");
			if (this.afterBanner.length > 0) {
				// Sessions you started go first; a held notice follows them rather
				// than being lost. They wait for any banner that is up to end.
				if (notice !== undefined) this.queueAfterBanner([notice]);
				if (current !== null) this.publish();
				else this.showAfterBanner();
				return;
			}
			if (notice === undefined) this.publish();
			else this.show(notice);
			return;
		}
		// Sessions that need you come first; the sessions you started follow
		// their banner.
		// Held banners show when you leave, combined (spec 13.3). A banner about
		// sessions that need you that is still up takes them in, as a burst
		// does, so nothing on it drops out.
		const alerts = [
			...showing.filter((alert) => !sessions.some((held) => subject(held) === subject(alert))),
			...sessions,
		];
		const [only] = alerts;
		if (current !== null && showing.length > 0) {
			this.banner = { id: current.id, alerts };
		} else if (alerts.length === 1 && only !== undefined) {
			this.show(only);
			return;
		} else {
			this.replaceBanner(alerts);
			this.buzz(alerts.some((alert) => alert.kind === "failed") ? "warning" : "light");
		}
		this.shownAt = this.timer.now();
		this.armExpiry();
		this.publish();
	}

	private scheduleRelease(): void {
		this.cancelRelease();
		if (this.held.length > 0) this.releasing = this.timer.setTimeout(() => this.release(), RELEASE_MS);
	}

	private cancelRelease(): void {
		if (this.releasing !== null) this.timer.clearTimeout(this.releasing);
		this.releasing = null;
	}

	private armExpiry(): void {
		if (this.expiry !== null) this.timer.clearTimeout(this.expiry);
		this.expiry = this.timer.setTimeout(() => this.expire(), BANNER_MS);
	}

	private expire(): void {
		this.expiry = null;
		// A banner never goes away while a finger is on it.
		if (this.touching) {
			this.armExpiry();
			return;
		}
		this.banner = null;
		this.showAfterBanner();
	}

	/** Puts up a new banner in place of any that is up. A session you started
	 * leaves only when its own banner ends or you look at it, so one the new
	 * banner replaces goes back to the front of the queue, to show again when
	 * this banner ends. */
	private replaceBanner(alerts: readonly Alert[]): void {
		const interrupted = (this.banner?.alerts ?? []).filter(
			(other) => followsInTurn(other) && !alerts.some((alert) => subject(alert) === subject(other)),
		);
		if (interrupted.length > 0)
			this.afterBanner = [
				...interrupted,
				...this.afterBanner.filter((waiting) => !interrupted.some((other) => subject(other) === subject(waiting))),
			];
		this.stopBanner();
		this.banner = { id: this.nextId++, alerts };
	}

	private queueAfterBanner(alerts: readonly Alert[]): void {
		for (const alert of alerts)
			this.afterBanner = [...this.afterBanner.filter((waiting) => subject(waiting) !== subject(alert)), alert];
	}

	/** A banner went: the oldest session you started that waited behind it
	 * shows now, unless a hold began meanwhile (it then waits out that hold,
	 * with the rest). */
	private showAfterBanner(): void {
		this.afterBanner = this.afterBanner.filter((alert) => this.wanted(alert));
		const [next] = this.afterBanner;
		if (next === undefined) {
			this.publish();
			return;
		}
		if (this.holding()) {
			const waiting = this.afterBanner;
			this.afterBanner = [];
			this.held = [...this.held.filter((alert) => !waiting.some((w) => subject(w) === subject(alert))), ...waiting];
			this.publish();
			return;
		}
		this.afterBanner = this.afterBanner.slice(1);
		this.show(next);
	}

	private stopBanner(): void {
		if (this.expiry !== null) this.timer.clearTimeout(this.expiry);
		this.expiry = null;
		this.touching = false;
		this.banner = null;
	}

	private buzz(kind: Haptic): void {
		if (this.preferences.haptics) this.haptic(kind);
	}

	private publish(): void {
		this.snapshot = { banner: this.banner, held: this.held.length, recent: this.recent };
		for (const listener of [...this.listeners]) listener();
	}
}
