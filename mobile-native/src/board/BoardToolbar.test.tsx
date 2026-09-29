import type { ConnectionState } from "@evener/appwire-client";
import { AccessibilityInfo } from "react-native";
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
// The status reads the provider's connection and its clock.
const connection = vi.hoisted(() => ({
	value: { state: "ready", fatal: false, downSince: null as number | null, lastLiveAt: null as number | null },
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => connection.value }));

const palette = paletteFor("light");

// The status clock arms timers whenever the connection isn't live; fake ones
// never fire after a test ends.
beforeEach(() => {
	vi.useFakeTimers();
});
afterEach(() => {
	vi.useRealTimers();
});

function toolbar(
	state: ConnectionState,
	over: { fatal?: boolean; onNewSession?: () => void; disabled?: boolean } = {},
) {
	const now = Date.now();
	connection.value = {
		state,
		fatal: over.fatal ?? false,
		downSince: state === "ready" ? null : now,
		lastLiveAt: state === "ready" ? null : now,
	};
	return <BoardToolbar newSessionDisabled={over.disabled ?? false} onNewSession={over.onNewSession ?? (() => {})} />;
}
it("shows the status in the middle ink", () => {
	const tree = render(toolbar("closed", { fatal: true }));
	const status = tree.root.findAll(
		(node) => node.type === ("Text" as never) && node.props.children === "Update needed",
	)[0];
	expect(status.props.style).toMatchObject({ fontSize: 13, color: palette.inkMid });
});

it("leaves the connection status unannounced: it is ambient and changes often (#2903)", () => {
	const announce = vi.mocked(AccessibilityInfo.announceForAccessibility);
	announce.mockClear();
	const tree = render(toolbar("closed", { fatal: true }));
	act(() => tree.update(toolbar("ready")));
	expect(announce).not.toHaveBeenCalled();
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

it("leads with Select while the Board shows a session, and not otherwise", () => {
	const onSelect = vi.fn();
	const select = (tree: ReturnType<typeof render>) =>
		tree.root.findAll((node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === "Select");
	const tree = render(<BoardToolbar newSessionDisabled={false} onNewSession={() => {}} onSelect={onSelect} />);
	const [button] = select(tree);
	expect(button.findByType("Text" as never).props.style).toMatchObject({ fontSize: 17, color: palette.accentInk });
	act(() => button.props.onPress());
	expect(onSelect).toHaveBeenCalledTimes(1);
	expect(select(render(toolbar("ready")))).toHaveLength(0);
});
