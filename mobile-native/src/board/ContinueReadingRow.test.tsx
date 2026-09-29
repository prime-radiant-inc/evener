// The Board's way back into a document you left half read (spec 7.1).
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { pressable, render, renderedText } from "../renderNative.testkit";
import { ContinueReadingRow } from "./ContinueReadingRow";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const trail = {
	sessionRef: "local:fix",
	path: "docs/superpowers/plans/settle-race.md",
	title: "Fix the settle/drain race",
	sessionTitle: "Get PR 2138 Test Clean",
	progress: 0.617,
	leftAt: 0,
	updatedAt: "2026-09-26T11:39:00.000Z",
};

it("says how far you read and what, and reads that way to VoiceOver", () => {
	const onOpen = vi.fn();
	const tree = render(<ContinueReadingRow trail={trail} onOpen={onOpen} />);
	expect(renderedText(tree)).toContain("Continue reading · 62%");
	expect(renderedText(tree)).toContain("Fix the settle/drain race");
	const row = pressable(tree, "Continue reading, 62 percent, Fix the settle/drain race");
	if (!row) throw new Error("no row");
	act(() => row.props.onPress());
	expect(onOpen).toHaveBeenCalledWith(trail);
});
