import type { NavigationSessionSummary } from "@evener/appwire-client";
import { manifest } from "@evener/appwire-client/testing/navigation";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
	answer,
	answerNotices,
	boundary,
	type Hub,
	hubNotice,
	invalidate,
	noticesChanged,
	requestsFor,
	session,
	tick,
} from "../board/navigationHubTestUtils";
import { render } from "../renderNative.testkit";
import { type AlertSnapshot, DEFAULT_ALERT_PREFERENCES, RELEASE_MS, sessionRef } from "./alertCenter";
import { alertPreferences } from "./nativeAlertPreferences";

const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

import { AlertsProvider } from "./AlertsProvider";
import { useAlertSnapshot, useHeldAlertCount, useHoldAlerts, useOfferAlert, useReportRoutes } from "./alertsContext";

type Routes = Parameters<ReturnType<typeof useReportRoutes>>[0];
const probe = {
	snapshot: null as AlertSnapshot | null,
	held: 0,
	report: null as null | ((routes: Routes) => void),
	offer: null as null | ReturnType<typeof useOfferAlert>,
};
function Probe({ quiet }: { quiet: boolean }) {
	probe.snapshot = useAlertSnapshot();
	probe.held = useHeldAlertCount();
	probe.report = useReportRoutes();
	probe.offer = useOfferAlert();
	useHoldAlerts(quiet, "quiet");
	return null;
}

const asking = (ref: string): NavigationSessionSummary =>
	({ ...session(ref), state: "awaiting", ask_pending: true }) as NavigationSessionSummary;
const failed = (ref: string): NavigationSessionSummary =>
	({ ...session(ref), state: "errored" }) as NavigationSessionSummary;
const sources = [{ id: "laptop", label: "Laptop", kind: "local", online: true }];

/** The phone's saved hubs, unless a test removes one. */
const savedHubs = () => [
	{ id: "hub-1", name: "hub-1" },
	{ id: "hub-2", name: "hub-2" },
];
function connect(client: Hub["client"] | null, state = "ready", hubId = "hub-1", profiles = savedHubs()) {
	harness.connection = {
		client,
		state,
		activeProfile: client || hubId ? { id: hubId, name: hubId } : null,
		profiles,
	};
}
let mounted: ReturnType<typeof render> | null = null;
function mount() {
	mounted = render(
		<AlertsProvider>
			<Probe quiet={false} />
		</AlertsProvider>,
	);
	return {
		rerender(quiet = false) {
			act(() =>
				mounted?.update(
					<AlertsProvider>
						<Probe quiet={quiet} />
					</AlertsProvider>,
				),
			);
		},
	};
}
/** A client's first reads: Live, Needs you, the manifest and the notices. */
async function firstReads(
	hub: Hub,
	{ live = [] as NavigationSessionSummary[], needsYou = [] as NavigationSessionSummary[] } = {},
) {
	await act(async () => {
		answer(hub, "live", { sessions: live, remaining: 0 });
		answer(hub, "needs_you", { sessions: needsYou, remaining: 0 });
		answer(hub, "manifest", manifest({ sources }));
		answerNotices(hub, []);
		await tick();
	});
}
async function needsYouNow(hub: Hub, sequence: number, rows: NavigationSessionSummary[]) {
	await act(async () => {
		invalidate(hub, sequence, [{ kind: "section", section: "needs_you", revision: sequence + 1 }]);
		answer(hub, "needs_you", { sessions: rows, remaining: 0 }, sequence + 1);
		await tick();
	});
}
const shownRefs = () =>
	probe.snapshot?.banner?.alerts.map(
		(alert) => sessionRef(alert) ?? (alert.kind === "notice" ? alert.key : alert.kind),
	) ?? [];

beforeEach(() => {
	alertPreferences().set(DEFAULT_ALERT_PREFERENCES);
});
afterEach(() => {
	act(() => mounted?.unmount());
	mounted = null;
	vi.useRealTimers();
});

it("takes the first reads as a baseline, and alerts a question a later read finds", async () => {
	const hub = boundary();
	connect(hub.client);
	mount();
	await firstReads(hub, { needsYou: [failed("old")] });
	expect(probe.snapshot?.banner).toBeNull();
	await needsYouNow(hub, 1, [failed("old"), asking("new")]);
	expect(shownRefs()).toEqual(["new"]);
});

it("reads Live's first page only, where a session moving up is no news unless it needs you", async () => {
	const hub = boundary();
	connect(hub.client);
	mount();
	await act(async () => {
		answer(hub, "live", { sessions: [session("top")], remaining: 40 });
		answer(hub, "needs_you", { sessions: [], remaining: 0 });
		answer(hub, "manifest", manifest({ sources }));
		answerNotices(hub, []);
		await tick();
	});
	expect(requestsFor(hub, "live")).toHaveLength(1);
	// A session from a later page, never read, moves onto the first one.
	await act(async () => {
		invalidate(hub, 1, [{ kind: "section", section: "live", revision: 2 }]);
		answer(hub, "live", { sessions: [session("moved-up"), session("top")], remaining: 40 }, 2);
		await tick();
	});
	expect(probe.snapshot?.banner).toBeNull();
	expect(requestsFor(hub, "live")).toHaveLength(2);
});

it("waits for every Needs you page before its baseline, so a second page alerts nothing", async () => {
	const hub = boundary();
	connect(hub.client);
	mount();
	await act(async () => {
		answer(hub, "live", { sessions: [], remaining: 0 });
		answer(hub, "needs_you", { sessions: [failed("first")], remaining: 1 });
		answer(hub, "manifest", manifest({ sources }));
		answerNotices(hub, []);
		await tick();
		answer(hub, "needs_you", { sessions: [asking("second")], remaining: 0 });
		await tick();
	});
	expect(probe.snapshot?.banner).toBeNull();
});

it("takes a new client's first reads as a new baseline, and drops the replaced client's late answers", async () => {
	const first = boundary();
	connect(first.client);
	const { rerender } = mount();
	await firstReads(first);
	// A read the first client started is still out when the app swaps in a
	// new client (a return from the background, say).
	act(() => invalidate(first, 1, [{ kind: "section", section: "needs_you", revision: 2 }]));
	const second = boundary();
	connect(second.client);
	rerender();
	// The replaced client's controller is gone, listeners and all.
	expect(first.listeners.size).toBe(0);
	await act(async () => {
		answer(first, "needs_you", { sessions: [failed("late")], remaining: 0 }, 2);
		await tick();
	});
	expect(probe.snapshot?.banner).toBeNull();
	await firstReads(second, { needsYou: [failed("meanwhile")] });
	expect(probe.snapshot?.banner).toBeNull();
	await needsYouNow(second, 1, [failed("meanwhile"), asking("new")]);
	expect(shownRefs()).toEqual(["new"]);
});

it("tells you what changed during a drop the same client recovers from (ruling 2)", async () => {
	const hub = boundary();
	connect(hub.client);
	const { rerender } = mount();
	await firstReads(hub);
	connect(hub.client, "reconnecting");
	rerender();
	connect(hub.client, "ready");
	rerender();
	await firstReads(hub, { needsYou: [asking("meanwhile")] });
	expect(shownRefs()).toEqual(["meanwhile"]);
});

it("says nothing when a host goes offline and comes back (ruling 3)", async () => {
	const hub = boundary();
	connect(hub.client);
	mount();
	await firstReads(hub, { needsYou: [asking("away")] });
	await needsYouNow(hub, 1, [{ ...asking("away"), offline: true }]);
	await needsYouNow(hub, 2, [asking("away")]);
	expect(probe.snapshot?.banner).toBeNull();
});

it("alerts a notice that appears later, never one there at first, never a count change, and never a plugin (ruling 5)", async () => {
	const hub = boundary();
	connect(hub.client);
	mount();
	// Live and the manifest land before the notices do.
	await act(async () => {
		answer(hub, "live", { sessions: [], remaining: 0 });
		answer(hub, "needs_you", { sessions: [], remaining: 0 });
		answer(hub, "manifest", manifest({ sources }));
		await tick();
		answerNotices(hub, [hubNotice("signInRequired", "openai", 1)]);
		await tick();
	});
	expect(probe.snapshot?.banner).toBeNull();
	await act(async () => {
		noticesChanged(hub, [
			hubNotice("signInRequired", "openai", 2),
			hubNotice("signInRequired", "anthropic"),
			hubNotice("pluginBroken", "go"),
		]);
		await tick();
	});
	expect(shownRefs()).toEqual(["signInRequired:anthropic"]);
});

it("holds a banner while you read or type, and counts what waits", async () => {
	const hub = boundary();
	connect(hub.client);
	const { rerender } = mount();
	await firstReads(hub);
	rerender(true);
	await needsYouNow(hub, 1, [asking("a")]);
	expect(probe.snapshot?.banner).toBeNull();
	expect(probe.held).toBe(1);
});

it("holds banners under a sheet or the Hub whatever the Hold switch says, and shows them once it closes", async () => {
	alertPreferences().set({ hold: false });
	const hub = boundary();
	connect(hub.client);
	mount();
	await firstReads(hub);
	act(() => probe.report?.([{ name: "Sessions" }, { name: "Hub", params: { screen: "HubHome" } }]));
	await needsYouNow(hub, 1, [asking("a")]);
	expect(probe.snapshot?.banner).toBeNull();
	act(() => probe.report?.([{ name: "Sessions" }]));
	expect(probe.snapshot?.banner).toBeNull();
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, RELEASE_MS + 20));
	});
	expect(shownRefs()).toEqual(["a"]);
});

it("starts over for another hub, and reports the screen again for it", async () => {
	const first = boundary();
	connect(first.client, "ready", "hub-1");
	const { rerender } = mount();
	await firstReads(first);
	const onScreen = { name: "Conversation", params: { hubId: "hub-1", ref: "local:a", title: "a" } };
	act(() => probe.report?.([{ name: "Sessions" }, onScreen]));
	const second = boundary();
	connect(second.client, "ready", "hub-2");
	rerender();
	await firstReads(second);
	// hub-1's session screen still on the stack doesn't quiet hub-2's
	// session with the same ref.
	await needsYouNow(second, 1, [asking("local:a")]);
	expect(shownRefs()).toEqual(["local:a"]);
});

it("drops a removed hub's failed start, so no banner is left that can open nothing (#3104)", async () => {
	const hub = boundary();
	connect(hub.client, "ready", "hub-1");
	const { rerender } = mount();
	await firstReads(hub);
	act(() => {
		probe.offer?.({ kind: "startFailed", hubId: "hub-1", hubName: "hub-1", uncertain: false });
		probe.offer?.({ kind: "startFailed", hubId: "hub-2", hubName: "hub-2", uncertain: false });
	});
	// The active hub's shows; the other follows it.
	expect(probe.snapshot?.banner?.alerts).toEqual([
		{ kind: "startFailed", hubId: "hub-1", hubName: "hub-1", uncertain: false },
	]);
	// The active hub is removed; the phone moves to the other one.
	const other = boundary();
	connect(other.client, "ready", "hub-2", [{ id: "hub-2", name: "hub-2" }]);
	rerender();
	await firstReads(other);
	expect(probe.snapshot?.banner?.alerts).toEqual([
		{ kind: "startFailed", hubId: "hub-2", hubName: "hub-2", uncertain: false },
	]);
	// And the other one, not active, is removed too.
	connect(other.client, "ready", "hub-2", []);
	rerender();
	expect(probe.snapshot).toMatchObject({ banner: null });
	expect(probe.held).toBe(0);
});
