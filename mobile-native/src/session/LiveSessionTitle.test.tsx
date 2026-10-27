// The live nav bar title: its state line is built on its own clock, one second
// while the agent works and thirty at rest, so the transcript under it never
// re-renders per second. It carries the tray's line and meter now (spec 8.1,
// 16.4).
import { act } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PulseMeter } from "../board/PulseMeter";
import { render, renderedText, unmountMountedTrees } from "../renderNative.testkit";
import { LiveSessionTitle } from "./LiveSessionTitle";

import type { MobileConversation } from "../projectedRows";

type LiveSession = MobileConversation;

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-gesture-handler", async () =>
	(await import("../renderNative.testkit")).gestureDetectorModuleMock(),
);

function running(lastFrameAt: number): LiveSession {
	return {
		status: { type: "active" },
		turns: [],
		activeTurnId: undefined,
		runningTurnId: undefined,
		delegates: [],
		modelRetry: undefined,
		lastFrameAt,
		askPending: false,
		pendingEscalations: [],
	} as unknown as LiveSession;
}

/** A store whose conversation a test sets, and whose lastFrameAt it moves to
 * count frames. */
function frameStore(conversation: LiveSession | null) {
	const listeners = new Set<() => void>();
	return {
		getState: () => ({ conversation }),
		subscribe(listener: () => void) {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		frame(at: number) {
			conversation = conversation ? { ...conversation, lastFrameAt: at } : null;
			for (const listener of listeners) listener();
		},
		open(next: LiveSession | null) {
			conversation = next;
			for (const listener of listeners) listener();
		},
	};
}

function liveTitle(
	store: ReturnType<typeof frameStore>,
	overrides: Partial<Parameters<typeof LiveSessionTitle>[0]> = {},
) {
	return (
		<LiveSessionTitle
			title="S"
			onPress={() => {}}
			onSwipe={() => {}}
			neighbors={{ previous: false, next: false }}
			runMs={() => null}
			store={store}
			{...overrides}
		/>
	);
}

describe("the live nav bar title (spec 8.1, 16.4)", () => {
	beforeEach(() => vi.useFakeTimers());
	afterEach(() => {
		unmountMountedTrees();
		vi.useRealTimers();
	});

	it("ticks the live line's seconds once a second while the agent works, and no timer at rest", () => {
		vi.setSystemTime(new Date("2026-09-26T12:00:00Z"));
		const store = frameStore(running(Date.now()));
		const tree = render(liveTitle(store));
		const first = renderedText(tree);
		expect(vi.getTimerCount()).toBe(1);
		act(() => {
			vi.advanceTimersByTime(21_000);
		});
		// Gone quiet, the line says for how long.
		const later = renderedText(tree);
		expect(later).not.toBe(first);
		expect(later).toContain("21s");

		const idle = { ...running(Date.now()), status: { type: "idle" } } as unknown as LiveSession;
		act(() => store.open(idle));
		expect(tree.root.findAllByType(PulseMeter)).toEqual([]);
	});

	it("shows the meter from this phone's frame counts while working", () => {
		vi.setSystemTime(new Date("2026-09-26T12:00:00Z"));
		const store = frameStore(running(Date.now()));
		const tree = render(liveTitle(store));
		// No frame yet: the meter shows its still fallback.
		expect(tree.root.findByType(PulseMeter).props.perMinute).toBeUndefined();
		act(() => {
			vi.advanceTimersByTime(1000);
			store.frame(Date.now());
		});
		expect(tree.root.findByType(PulseMeter).props.perMinute).toEqual([0, 0, 0, 0, 0, 0, 1]);
	});

	it("reads Idle with the elapsed age once the turn ends", () => {
		vi.setSystemTime(new Date("2026-09-26T12:00:00Z"));
		const idle = { ...running(0), status: { type: "idle" } } as unknown as LiveSession;
		const tree = render(liveTitle(frameStore(idle)));
		expect(renderedText(tree)).toContain("Idle");
	});

	it("renders nothing before the session is read", () => {
		const tree = render(liveTitle(frameStore(null)));
		expect(tree.toJSON()).toBeNull();
	});
});
