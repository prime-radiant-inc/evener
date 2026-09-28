import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render } from "../renderNative.testkit";
import { SelectBar } from "./SelectBar";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-safe-area-context", () => ({
	useSafeAreaInsets: () => ({ top: 47, bottom: 34, left: 0, right: 0 }),
}));

const palette = paletteFor("light");
const LABELS = ["Done", "Archive", "Pin", "Mark as read"];

function bar(counts: { archive: number; pin: number; markRead: number }) {
	const handlers = { onDone: vi.fn(), onArchive: vi.fn(), onPin: vi.fn(), onMarkRead: vi.fn() };
	const tree = render(<SelectBar counts={counts} {...handlers} />);
	const button = (label: string) =>
		tree.root.find((node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === label);
	const ink = (label: string) => button(label).findByType("Text" as never).props.style;
	return { tree, handlers, button, ink };
}

it("shows Done, then Archive, Pin and Mark as read, in 17pt accent ink", () => {
	const { tree, ink } = bar({ archive: 1, pin: 1, markRead: 1 });
	const labels = tree.root
		.findAll((node) => node.type === ("Pressable" as never))
		.map((node) => node.props.accessibilityLabel);
	expect(labels).toEqual(LABELS);
	for (const label of LABELS) expect(ink(label)).toMatchObject({ fontSize: 17, color: palette.accentInk });
});

it("runs each action it has sessions for", () => {
	const { handlers, button } = bar({ archive: 2, pin: 1, markRead: 3 });
	act(() => button("Archive").props.onPress());
	act(() => button("Pin").props.onPress());
	act(() => button("Mark as read").props.onPress());
	act(() => button("Done").props.onPress());
	expect([handlers.onArchive, handlers.onPin, handlers.onMarkRead, handlers.onDone].map((fn) => fn.mock.calls.length)).toEqual([
		1, 1, 1, 1,
	]);
});

it("disables an action with no sessions to act on, and Done always works", () => {
	const { handlers, button, ink } = bar({ archive: 0, pin: 0, markRead: 0 });
	for (const label of ["Archive", "Pin", "Mark as read"]) {
		expect(button(label).props.accessibilityState).toEqual({ disabled: true });
		expect(button(label).props.disabled).toBe(true);
		expect(ink(label)).toMatchObject({ color: palette.inkLow });
		act(() => button(label).props.onPress?.());
	}
	expect([handlers.onArchive, handlers.onPin, handlers.onMarkRead].map((fn) => fn.mock.calls.length)).toEqual([0, 0, 0]);
	expect(button("Done").props.accessibilityState).toEqual({ disabled: false });
	act(() => button("Done").props.onPress());
	expect(handlers.onDone).toHaveBeenCalledTimes(1);
});
