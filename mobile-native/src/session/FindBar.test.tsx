// The find bar (spec 8.7; ruling 29): the field, where you are among the
// matches, the two step buttons and Done. Only native edges are mocked.
import type { ComponentProps } from "react";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { pressable, render, textOf } from "../renderNative.testkit";
import { FindBar } from "./FindBar";

const announce = vi.hoisted(() => vi.fn());

vi.mock("react-native", async () => {
	const mock = (await import("../renderNative.testkit")).nativeModuleMock();
	return { ...mock, AccessibilityInfo: { ...mock.AccessibilityInfo, announceForAccessibility: announce } };
});
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const palette = paletteFor("light");

function bar(over: Partial<ComponentProps<typeof FindBar>> = {}) {
	const props = {
		query: "settle",
		label: "2 of 3",
		searchingOlder: false,
		settled: true,
		onQuery: vi.fn(),
		onStep: vi.fn(),
		onDone: vi.fn(),
		...over,
	};
	return { props, tree: render(<FindBar {...props} />) };
}

function field(tree: ReactTestRenderer): ReactTestInstance {
	return tree.root.find((node) => String(node.type) === "TextInput");
}

function text(tree: ReactTestRenderer, words: string): ReactTestInstance | undefined {
	return tree.root.findAll((node) => String(node.type) === "Text" && textOf(node) === words)[0];
}

describe("the find bar", () => {
	// Under the nav bar's glass the bar draws clear on it; elsewhere on the page.
	it("draws clear on the header's glass, and on the page off it", () => {
		const fill = (tree: ReactTestRenderer) =>
			tree.root.findAll((node) => String(node.type) === "View")[0]?.props.style.backgroundColor;
		expect(fill(bar().tree)).toBe(palette.page);
		expect(fill(bar({ onGlass: true }).tree)).toBe("transparent");
	});

	it("holds a focused field that finds in the session", () => {
		const { tree } = bar();
		expect(field(tree).props).toMatchObject({
			placeholder: "Find in session",
			accessibilityLabel: "Find in session",
			autoFocus: true,
			value: "settle",
		});
	});

	it("says where you are among the matches, in tabular ink-mid captions", () => {
		const { tree } = bar();
		expect(text(tree, "2 of 3")?.props.style).toMatchObject({
			fontSize: 13,
			lineHeight: 18,
			color: palette.inkMid,
			fontVariant: ["tabular-nums"],
		});
	});

	it("says so while it searches older messages", () => {
		const { tree } = bar({ searchingOlder: true });
		expect(text(tree, "Searching older messages…")).toBeDefined();
		expect(text(tree, "2 of 3")).toBeUndefined();
	});

	it("steps to the older and the newer match", () => {
		const { props, tree } = bar();
		const older = pressable(tree, "Older match");
		const newer = pressable(tree, "Newer match");
		expect(older?.findByType("SymbolView" as never).props.name).toBe("chevron.up");
		expect(newer?.findByType("SymbolView" as never).props.name).toBe("chevron.down");
		act(() => older?.props.onPress());
		act(() => newer?.props.onPress());
		expect(vi.mocked(props.onStep).mock.calls).toEqual([[-1], [1]]);
	});

	it("offers no steps with nothing to find", () => {
		const { tree } = bar({ query: "  ", label: "No matches" });
		expect(pressable(tree, "Older match")?.props.accessibilityState).toMatchObject({ disabled: true });
		expect(pressable(tree, "Newer match")?.props.accessibilityState).toMatchObject({ disabled: true });
	});

	it("passes what you type", () => {
		const { props, tree } = bar();
		act(() => field(tree).props.onChangeText("flaky"));
		expect(props.onQuery).toHaveBeenCalledWith("flaky");
	});

	it("tells VoiceOver where you are once it settles, and while older messages load", () => {
		announce.mockClear();
		const { props, tree } = bar({ query: "", label: "" });
		expect(announce).not.toHaveBeenCalled();
		act(() => tree.update(<FindBar {...props} query="settle" label="2 of 3" />));
		act(() => tree.update(<FindBar {...props} query="settle" label="2 of 3" />));
		// Between a keystroke and its result the label is passing through.
		act(() => tree.update(<FindBar {...props} query="settles" label="No matches" settled={false} />));
		act(() => tree.update(<FindBar {...props} query="settles" label="No matches" settled={false} searchingOlder />));
		act(() => tree.update(<FindBar {...props} query="settles" label="1 of 1" />));
		act(() => tree.update(<FindBar {...props} query="settles" label="1 of 1" />));
		expect(announce.mock.calls).toEqual([["2 of 3"], ["Searching older messages…"], ["1 of 1"]]);
	});

	it("closes with Done", () => {
		const { props, tree } = bar();
		act(() => pressable(tree, "Done")?.props.onPress());
		expect(props.onDone).toHaveBeenCalledTimes(1);
	});
});
