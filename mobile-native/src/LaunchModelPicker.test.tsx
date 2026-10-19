// The launch model picker's failure state (C07's owned recovery): no button
// asks a person to retry what the app retries on its own, and the glyphs a
// person reads are SF Symbols, never text characters.
import { createElement } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { LaunchModelPicker } from "./LaunchModelPicker";
import { render } from "./renderNative.testkit";

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

function picker(client: ConversationClientLike | null, value = "", onChange = () => {}) {
	return render(
		createElement(LaunchModelPicker, {
			client,
			value,
			onChange,
			header: null,
		}),
	);
}

it("says the list will load again on its own, with no pressable Retry", async () => {
	const failing = {
		request: async () => {
			throw new Error("unreachable");
		},
	} as unknown as ConversationClientLike;
	const tree = picker(failing);
	await act(async () => {});
	// The calm line owns the retry: no button can be pressed.
	// No pressable offers to retry, in any wording.
	expect(
		tree.root.findAll(
			(node) => String(node.type) === "Pressable" && /retry/i.test(String(node.props.accessibilityLabel ?? "")),
		),
	).toEqual([]);
	expect(tree.root.findAll((node) => node.props.testID === "model-list-status")).toHaveLength(1);
});

it("draws its warning and selection as SF Symbols, not text glyphs", async () => {
	const catalog = {
		request: async () => ({
			data: [{ provider: "local", model: "a", supportsTools: true, warnings: ["served from one region"] }],
			recent: [],
			diagnostics: [],
		}),
	} as unknown as ConversationClientLike;
	const tree = picker(catalog, "local/a", () => {});
	await act(async () => {});
	const symbols = tree.root.findAll((node) => String(node.type) === "SymbolView").map((node) => node.props.name);
	// A provider's diagnostic warns with the triangle symbol, and the chosen
	// row's check is the same mark the Grouped kit draws.
	expect(symbols).toContain("exclamationmark.triangle.fill");
	expect(symbols).toContain("checkmark");
});
