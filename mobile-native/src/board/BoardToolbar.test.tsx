import type { ConnectionState } from "@evener/appwire-client";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render, renderedText } from "../renderNative.testkit";
import { BoardToolbar } from "./BoardToolbar";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-safe-area-context", () => ({
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));

const palette = paletteFor("light");
const NOW = Date.UTC(2026, 8, 26, 12, 0);

beforeEach(() => {
	vi.useFakeTimers();
	vi.setSystemTime(NOW);
});
afterEach(() => {
	vi.useRealTimers();
});

function toolbar(state: ConnectionState, over: { fatal?: boolean; onNewSession?: () => void; disabled?: boolean } = {}) {
	return (
		<BoardToolbar
			state={state}
			fatal={over.fatal ?? false}
			newSessionDisabled={over.disabled ?? false}
			onNewSession={over.onNewSession ?? (() => {})}
		/>
	);
}
function advance(tree: ReactTestRenderer, ms: number) {
	act(() => {
		vi.advanceTimersByTime(ms);
	});
	return renderedText(tree);
}

it("says nothing at 1 second down, Reconnecting… at 2 and Offline at 30, then keeps the age current", () => {
	const tree = render(toolbar("ready"));
	expect(renderedText(tree)).toBe("");
	act(() => tree.update(toolbar("reconnecting")));
	expect(renderedText(tree)).toBe("");
	expect(advance(tree, 1000)).toBe("");
	expect(advance(tree, 1000)).toBe("Reconnecting…");
	expect(advance(tree, 27_999)).toBe("Reconnecting…");
	expect(advance(tree, 1)).toBe("Offline · updated 1m ago");
	// The minute timer (ticking from the 30-second mark) moves the age on with
	// nothing else changing: 3m30s after the drop it says 3m.
	expect(advance(tree, 180_000)).toBe("Offline · updated 3m ago");
});

it("says Offline with no age when the hub was never reached this launch", () => {
	const tree = render(toolbar("connecting"));
	expect(advance(tree, 30_000)).toBe("Offline");
});

it("runs no clock while live, and stops its clock when the connection is live again", () => {
	const tree = render(toolbar("ready"));
	expect(vi.getTimerCount()).toBe(0);
	act(() => tree.update(toolbar("reconnecting")));
	expect(vi.getTimerCount()).toBeGreaterThan(0);
	expect(advance(tree, 2000)).toBe("Reconnecting…");
	act(() => tree.update(toolbar("ready")));
	expect(vi.getTimerCount()).toBe(0);
	expect(renderedText(tree)).toBe("");
});

it("says Update needed at once when no retry can fix the close", () => {
	const tree = render(toolbar("closed", { fatal: true }));
	expect(renderedText(tree)).toBe("Update needed");
});

it("shows the status in the middle ink", () => {
	const tree = render(toolbar("closed", { fatal: true }));
	const status = tree.root.findAll(
		(node) => node.type === ("Text" as never) && node.props.children === "Update needed",
	)[0];
	expect(status.props.style).toMatchObject({ fontSize: 13, color: palette.inkMid });
});

it("opens a new session from its trailing button, which is disabled when it can't act", () => {
	const onNewSession = vi.fn();
	const tree = render(toolbar("ready", { onNewSession }));
	const button = tree.root.find(
		(node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === "New session",
	);
	expect(button.findByType("SymbolView" as never).props).toMatchObject({
		name: "square.and.pencil",
		tintColor: palette.accentInk,
	});
	act(() => button.props.onPress());
	expect(onNewSession).toHaveBeenCalledTimes(1);
	act(() => tree.update(toolbar("reconnecting", { onNewSession, disabled: true })));
	const disabled = tree.root.find(
		(node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === "New session",
	);
	expect(disabled.props.disabled).toBe(true);
});
