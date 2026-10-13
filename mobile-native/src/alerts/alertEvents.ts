// Turns what the hub says into alerts (spec 13.3): a session that becomes
// Failed, Question, Needs you, Approval, Warning or Restart needed (from any other
// state, another needs-you state included), and a hub notice that appears.
// A turn that ends otherwise says nothing: the session rests Idle with the
// Board's blue dot (#4093). A session that stays in one
// needs-you state says nothing more, whatever it now asks: one alert is
// great (Jesse, question 3). Each source's first read on a connection is
// its baseline and alerts nothing (ruling 2), so opening the app never drops
// a pile of banners on what the Board already shows.
import { type BoardState, bandOf, type LiveBands, liveRows, whyLine } from "../board/attention";
import type { Notice } from "../board/notices";
import type { AlertCenter, NeedsYouKind, NoticeAlert, SessionAlert } from "./alertCenter";

export type SessionStates = ReadonlyMap<string, BoardState>;

// The Board's Needs you band decides which states alert, so the two never disagree.
const needsYou = (state: BoardState): boolean => bandOf(state) === "needsYou";

export function detectSessionAlerts(
	previous: SessionStates | null,
	bands: LiveBands,
	offlineRefs: ReadonlySet<string>,
): { alerts: SessionAlert[]; resolved: string[]; states: Map<string, BoardState> } {
	const states = new Map<string, BoardState>();
	// A row on an unreachable host keeps what it last said, so the host coming
	// back is not news about the session (ruling 3).
	for (const ref of offlineRefs) {
		const state = previous?.get(ref);
		if (state !== undefined) states.set(ref, state);
	}
	const alerts: SessionAlert[] = [];
	// liveBands holds each session once, from Live or the Needs you section,
	// and leaves offline rows out (boardState calls them shutDown), so an
	// offline ref keeps the state seeded above. Each ref is diffed once even
	// if that ever changes: the first band it appears in, Needs you first,
	// decides.
	const diffed = new Set<string>();
	for (const item of liveRows(bands)) {
		const { ref, title } = item.row;
		if (diffed.has(ref)) continue;
		diffed.add(ref);
		const before = previous?.get(ref);
		states.set(ref, item.state);
		if (previous === null) continue;
		if (needsYou(item.state) && item.state !== before)
			alerts.push({ kind: item.state as NeedsYouKind, ref, title, why: whyLine(item) });
	}
	// A session that needed you and no longer does, answered here or
	// elsewhere, has nothing left to alert about. The Needs you section is
	// complete, so one missing from every band stopped needing you too; an
	// offline row kept its state above.
	const resolved =
		previous === null
			? []
			: [...previous]
					.filter(([ref, before]) => needsYou(before) && !needsYou(states.get(ref) ?? "idle"))
					.map(([ref]) => ref);
	return { alerts, resolved, states };
}

// A broken plugin never alerts: the Board's notice row shows it (ruling 5).
const ALERTING_NOTICES: ReadonlySet<Notice["kind"]> = new Set(["signIn", "host"]);

export function detectNoticeAlerts(
	previous: ReadonlySet<string> | null,
	notices: readonly Notice[],
): { alerts: NoticeAlert[]; keys: Set<string> } {
	const keys = new Set(notices.map((notice) => notice.key));
	if (previous === null) return { alerts: [], keys };
	const alerts = notices
		.filter((notice) => ALERTING_NOTICES.has(notice.kind) && !previous.has(notice.key))
		.map((notice): NoticeAlert => ({ kind: "notice", key: notice.key, title: notice.text }));
	return { alerts, keys };
}

/** Feeds the alert center from successive reads, and retracts a session's
 * alert once it stops needing you. Sessions and notices keep separate
 * baselines because they load separately. */
export class AlertFeed {
	private sessions: SessionStates | null = null;
	private notices: ReadonlySet<string> | null = null;

	constructor(private readonly center: Pick<AlertCenter, "offer" | "retract">) {}

	/** A new client (ruling 2): its first reads are baselines again. */
	rebaseline(): void {
		this.sessions = null;
		this.notices = null;
	}

	observeSessions(bands: LiveBands, offlineRefs: ReadonlySet<string>): void {
		const detected = detectSessionAlerts(this.sessions, bands, offlineRefs);
		this.sessions = detected.states;
		for (const ref of detected.resolved) this.center.retract(ref);
		for (const alert of detected.alerts) this.center.offer(alert);
	}

	observeNotices(notices: readonly Notice[]): void {
		const detected = detectNoticeAlerts(this.notices, notices);
		this.notices = detected.keys;
		for (const alert of detected.alerts) this.center.offer(alert);
	}
}
