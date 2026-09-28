import type { ConnectionState } from "@evener/appwire-client";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render } from "../renderNative.testkit";
import { BoardToolbar } from "./BoardToolbar";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-safe-area-context", () => ({
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));

const palette = paletteFor("light");

// The status clock arms timers whenever the connection isn't live; fake ones
// never fire after a test ends.
beforeEach(() => {
	vi.useFakeTimers();
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
