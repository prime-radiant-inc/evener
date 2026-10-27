import { describe, expect, it, vi } from "vitest";
import { pressable, render } from "../renderNative.testkit";
import { SearchField } from "./SearchField";

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));

const symbols = (tree: ReturnType<typeof render>) =>
	tree.root.findAll((node) => node.type === ("SymbolView" as never)).map((node) => node.props.name);

describe("a search field", () => {
	it("names the field for VoiceOver and its placeholder, and reports what is typed as typed", () => {
		const changes: string[] = [];
		const tree = render(<SearchField label="Search models" value="" onChangeText={(text) => changes.push(text)} />);
		const input = tree.root.findByType("TextInput" as never);
		expect(input.props.accessibilityLabel).toBe("Search models");
		expect(input.props.placeholder).toBe("Search models");
		expect(input.props.autoCapitalize).toBe("none");
		expect(input.props.autoCorrect).toBe(false);
		input.props.onChangeText("haiku");
		expect(changes).toEqual(["haiku"]);
	});

	it("leads with a magnifying glass and clears through an explicit button once something is typed", () => {
		const onClear = vi.fn();
		const empty = render(<SearchField label="Search models" value="" onChangeText={onClear} />);
		expect(symbols(empty)).toEqual(["magnifyingglass"]);
		expect(pressable(empty, "Clear filter")).toBeUndefined();

		const filled = render(<SearchField label="Search models" value="haiku" onChangeText={onClear} />);
		expect(symbols(filled)).toEqual(["magnifyingglass", "xmark.circle.fill"]);
		pressable(filled, "Clear filter")?.props.onPress();
		expect(onClear).toHaveBeenCalledWith("");
	});

	it("gives the input and clear action separate 44-point targets inside their parent", () => {
		const tree = render(<SearchField label="Search models" value="haiku" onChangeText={() => {}} />);
		const input = tree.root.findByType("TextInput" as never);
		const clear = tree.root.findByProps({ accessibilityRole: "button" });
		expect(input.props.style.minHeight).toBeGreaterThanOrEqual(44);
		expect(clear.props.style.minHeight).toBeGreaterThanOrEqual(44);
		expect(clear.props.style.minWidth).toBeGreaterThanOrEqual(44);
		expect(clear.parent?.props.style.minHeight).toBeGreaterThanOrEqual(44);
	});
});
