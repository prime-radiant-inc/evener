import { FakeClient, gateSettlements } from "@evener/appwire-client/testing/fakeClient";
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PulseMeter } from "../board/PulseMeter";
import { palettes } from "../design/tokens";
import { pressable, render, renderHook, renderedText } from "../renderNative.testkit";
import { LiveStatusTray, StatusTray, useFrameCounter } from "./StatusTray";
import { FrameCounter, type TraySource } from "./trayLine";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const light = palettes.light;

function rootRead(runningSubagents: number) {
	return { ref: "local:root", minutes: [0, 0, 0, 0, 0, 0, 0], runningSubagents };
}

function tray(overrides: Partial<Parameters<typeof StatusTray>[0]> = {}) {
	const props = {
		line: { text: "Running go test ./agent/... · 42s", attention: false },
		perMinute: undefined,
		connected: true,
		canStop: true,
		stopping: false,
		onStop: vi.fn(),
		onJumpToLive: vi.fn(),
		...overrides,
	};
	return { props, tree: render(<StatusTray {...props} />) };
}

function lineText(tree: ReactTestRenderer): ReactTestInstance {
	const text = tree.root.findAll((node) => String(node.type) === "Text" && node.props.numberOfLines === 1)[0];
	if (!text) throw new Error("no tray line");
	return text;
}

describe("StatusTray", () => {
	it("renders the line and a pulse meter", () => {
		const { tree } = tray({ perMinute: [1, 2, 3, 4, 5, 6, 7] });
		expect(renderedText(tree)).toContain("Running go test ./agent/... · 42s");
		const meter = tree.root.findByType(PulseMeter);
		expect(meter.props.perMinute).toEqual([1, 2, 3, 4, 5, 6, 7]);
		expect(meter.props.tone).toBe("alive");
		expect(lineText(tree).props.style).toMatchObject({
			color: light.inkMid,
			fontSize: 15,
			lineHeight: 20,
			fontVariant: ["tabular-nums"],
		});
		expect(lineText(tree).props.ellipsizeMode).toBe("tail");
	});

	it("has no Stop when the turn cannot be stopped", () => {
		const { tree } = tray({ canStop: false });
		expect(pressable(tree, "Stop")).toBeUndefined();
		expect(renderedText(tree)).not.toContain("Stop");
	});

	it("labels Stop and calls onStop when it is pressed", () => {
		const { props, tree } = tray();
		const button = pressable(tree, "Stop");
		expect(button?.props.accessibilityState).toMatchObject({ disabled: false });
		expect(button?.findByType("SymbolView" as never).props).toMatchObject({
			name: "stop.fill",
			tintColor: light.inkHi,
		});
		act(() => button?.props.onPress());
		expect(props.onStop).toHaveBeenCalledTimes(1);
		expect(props.onJumpToLive).not.toHaveBeenCalled();
	});

	it("hides Stop while disconnected, since it can't act offline", () => {
		const { tree } = tray({ connected: false });
		expect(pressable(tree, "Stop")).toBeUndefined();
		expect(renderedText(tree)).toContain("Running go test ./agent/... · 42s");
	});

	it("disables Stop while a Stop is in flight", () => {
		const { tree } = tray({ stopping: true });
		expect(pressable(tree, "Stop")?.props.disabled).toBe(true);
		expect(pressable(tree, "Stop")?.props.accessibilityState).toMatchObject({ disabled: true });
	});

	it("jumps to the live end when the line is pressed", () => {
		const { props, tree } = tray();
		const row = pressable(tree, "Running go test ./agent/... · 42s");
		act(() => row?.props.onPress());
		expect(props.onJumpToLive).toHaveBeenCalledTimes(1);
		expect(props.onStop).not.toHaveBeenCalled();
	});

	it("renders nothing without a line", () => {
		const { tree } = tray({ line: null });
		expect(tree.toJSON()).toBeNull();
	});

	it("turns the line amber and the meter to attention when the agent may be stuck", () => {
		const { tree } = tray({ line: { text: "May be stuck · no updates for 12m", attention: true } });
		expect(lineText(tree).props.style).toMatchObject({ color: light.attentionInk });
		expect(tree.root.findByType(PulseMeter).props.tone).toBe("attention");
	});

	it("grays the meter while disconnected", () => {
		const { tree } = tray({ connected: false });
		expect(tree.root.findByType(PulseMeter).props.tone).toBe("gray");
	});
});

function session(active: boolean, lastFrameAt: number): TraySource {
	return {
		status: active ? { type: "active" } : { type: "idle" },
		turns: [],
		activeTurnId: undefined,
		delegates: [],
		modelRetry: undefined,
		lastFrameAt,
	} as unknown as TraySource;
}

describe("LiveStatusTray", () => {
	beforeEach(() => {
		vi.useFakeTimers();
		vi.setSystemTime(new Date("2026-09-26T12:00:00Z"));
	});
	afterEach(() => vi.useRealTimers());

	function live(source: TraySource | null, frames = new FrameCounter(), client: FakeClient | null = null) {
		return (
			<LiveStatusTray
				session={source}
				frames={frames}
				client={client}
				sessionRef="local:root"
				inFront
				connected
				canStop
				stopping={false}
				onStop={() => {}}
				onJumpToLive={() => {}}
			/>
		);
	}

	it("ticks once a second while the agent works, and runs no timer at rest", () => {
		const start = Date.now();
		const tree = render(live(session(true, start)));
		expect(renderedText(tree)).toContain("Working");
		expect(vi.getTimerCount()).toBe(1);
		act(() => {
			vi.advanceTimersByTime(21_000);
		});
		expect(renderedText(tree)).toContain("Quiet 21s");

		act(() => tree.update(live(session(false, start))));
		expect(tree.toJSON()).toBeNull();
		expect(vi.getTimerCount()).toBe(0);

		act(() => tree.update(live(null)));
		expect(vi.getTimerCount()).toBe(0);
	});

	it("polls the hub's activity for its own session only while the agent works, and names its count", async () => {
		const client = new FakeClient("ready");
		client.on("evener/activity/read", () => ({
			sessions: [rootRead(3)],
		}));
		const tree = render(live(session(true, Date.now()), new FrameCounter(), client));
		await act(async () => {});
		expect(client.calls).toEqual([{ method: "evener/activity/read", params: { refs: ["local:root"] } }]);
		expect(renderedText(tree)).toContain("Waiting on 3 subagents");

		act(() => tree.update(live(session(false, Date.now()), new FrameCounter(), client)));
		act(() => {
			vi.advanceTimersByTime(30_000);
		});
		expect(client.calls).toHaveLength(1);
		expect(vi.getTimerCount()).toBe(0);
	});

	it("never shows the count from before an idle spell once the agent works again", async () => {
		const client = new FakeClient("ready");
		const reads = gateSettlements(client, "evener/activity/read");
		const tree = render(live(session(true, Date.now()), new FrameCounter(), client));
		await act(async () => {});
		await act(async () => {
			reads[0]?.resolve({ sessions: [rootRead(3)] });
		});
		expect(renderedText(tree)).toContain("Waiting on 3 subagents");

		act(() => tree.update(live(session(false, Date.now()), new FrameCounter(), client)));
		await act(async () => tree.update(live(session(true, Date.now()), new FrameCounter(), client)));
		expect(reads).toHaveLength(2);
		expect(renderedText(tree)).not.toContain("Waiting on 3 subagents");

		await act(async () => {
			reads[1]?.resolve({ sessions: [rootRead(5)] });
		});
		expect(renderedText(tree)).toContain("Waiting on 5 subagents");
	});

	it("shows the one-bar fallback until a frame arrives, then this phone's counts", () => {
		const frames = new FrameCounter();
		const tree = render(live(session(true, Date.now()), frames));
		expect(tree.root.findByType(PulseMeter).props.perMinute).toBeUndefined();

		frames.record(Date.now());
		act(() => {
			vi.advanceTimersByTime(1000);
		});
		expect(tree.root.findByType(PulseMeter).props.perMinute).toEqual([0, 0, 0, 0, 0, 0, 1]);
	});
});

describe("useFrameCounter", () => {
	function frameStore(lastFrameAt: number | null) {
		let state = { conversation: lastFrameAt === null ? null : { lastFrameAt } };
		const listeners = new Set<() => void>();
		return {
			getState: () => state,
			subscribe: (listener: () => void) => {
				listeners.add(listener);
				return () => listeners.delete(listener);
			},
			frame(at: number | null) {
				state = { conversation: at === null ? null : { lastFrameAt: at } };
				for (const listener of listeners) listener();
			},
			listeners,
		};
	}

	it("records this phone's clock each time the session's last frame moves", () => {
		vi.useFakeTimers();
		vi.setSystemTime(new Date("2026-09-26T12:00:30Z"));
		try {
			const store = frameStore(null);
			const hook = renderHook(() => useFrameCounter(store));
			expect(hook.result.current.hasFrames()).toBe(false);
			store.frame(100);
			store.frame(100);
			store.frame(200);
			expect(hook.result.current.perMinute(Date.now())).toEqual([0, 0, 0, 0, 0, 0, 2]);
			hook.unmount();
			expect(store.listeners.size).toBe(0);
		} finally {
			vi.useRealTimers();
		}
	});

	it("starts a new count for a new session binding", () => {
		const first = frameStore(1);
		const second = frameStore(1);
		let store = first;
		const hook = renderHook(() => useFrameCounter(store));
		first.frame(2);
		const counter = hook.result.current;
		expect(counter.hasFrames()).toBe(true);
		store = second;
		hook.rerender();
		expect(hook.result.current).not.toBe(counter);
		expect(hook.result.current.hasFrames()).toBe(false);
		expect(first.listeners.size).toBe(0);
		hook.unmount();
	});
});
