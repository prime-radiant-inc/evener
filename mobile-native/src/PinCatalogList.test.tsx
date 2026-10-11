// The pin catalog's blocking reason line makes the same split as the pin
// editor: a checkpoint means a previous pin change needs confirming, while a
// failed first read must say the catalog could not be loaded.
import { createElement } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { PinCatalogList } from "./PinCatalogList";
import { pressable, render, renderedText } from "./renderNative.testkit";

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());

function props(previousChange: boolean) {
	return {
		sections: [],
		hubName: "Hub",
		connected: true,
		loaded: true,
		loading: false,
		stale: false,
		remaining: 0,
		pending: false,
		uncertain: true,
		previousChange,
		error: null,
		refresh: () => {},
		more: () => {},
		open: () => {},
	};
}

it("blames the failed load when there is no previous pin change", () => {
	const text = renderedText(render(createElement(PinCatalogList, props(false))));
	expect(text).toContain("Check the pinned sections again.");
	expect(text).not.toContain("previous pin change");
});

it("keeps the previous-change wording when a checkpoint is present", () => {
	const text = renderedText(render(createElement(PinCatalogList, props(true))));
	expect(text).toContain("Check the previous pin change.");
});

// A section's row shows its name and opens it; VoiceOver hears what it does.
it("opens a section from its row, which VoiceOver names Open and the section", () => {
	const open = vi.fn();
	const section = { id: "s1", name: "Releases", count: 2 };
	const tree = render(createElement(PinCatalogList, { ...props(false), uncertain: false, sections: [section], open }));
	const row = pressable(tree, "Open Releases");
	expect(row).toBeDefined();
	expect(renderedText(tree)).toContain("Releases");
	act(() => row?.props.onPress());
	expect(open).toHaveBeenCalledWith(section);
});
