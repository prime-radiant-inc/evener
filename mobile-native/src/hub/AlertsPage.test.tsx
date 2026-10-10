import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { DEFAULT_ALERT_PREFERENCES } from "../alerts/alertCenter";
import { alertPreferences } from "../alerts/nativeAlertPreferences";
import { render, renderedText } from "../renderNative.testkit";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

import { AlertsPage } from "./AlertsPage";

const LABELS = [
	"A session fails",
	"A session asks a question, needs approval, or needs your reply",
	"Hold alerts while reading or typing",
	"Haptics",
];
const switchOf = (tree: ReturnType<typeof render>, label: string) =>
	tree.root.find((node) => String(node.type) === "Switch" && node.props.accessibilityLabel === label);

beforeEach(() => {
	alertPreferences().set(DEFAULT_ALERT_PREFERENCES);
});

it("shows spec 12's switches, their second lines and the footer", () => {
	const tree = render(<AlertsPage />);
	const text = renderedText(tree);
	for (const words of [
		"Show a banner when",
		...LABELS,
		"They show when you leave the document or send",
		"Lock-screen notifications are coming later. Until then, alerts show while Evener is open.",
	])
		expect(text).toContain(words);
	expect(LABELS.map((label) => switchOf(tree, label).props.value)).toEqual([true, true, true, true]);
	expect(text).not.toContain("A session finishes");
	// It needs no connection, so nothing on it is ever off.
	expect(LABELS.map((label) => switchOf(tree, label).props.disabled)).toEqual([false, false, false, false]);
});

it("stores a change, and shows one made elsewhere", () => {
	const tree = render(<AlertsPage />);
	act(() => switchOf(tree, "A session fails").props.onValueChange(false));
	expect(alertPreferences().getSnapshot().failures).toBe(false);
	act(() => alertPreferences().set({ haptics: false }));
	expect(switchOf(tree, "Haptics").props.value).toBe(false);
});
