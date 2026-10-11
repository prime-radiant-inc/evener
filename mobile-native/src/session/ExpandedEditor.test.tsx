import { act } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { pressable, render } from "../renderNative.testkit";
import { ExpandedEditor } from "./ExpandedEditor";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));

describe("the composer's full editor (spec 8.5)", () => {
	it("frames the editor in the shared modal sheet and closes with Done", () => {
		const onDone = vi.fn();
		const tree = render(
			<ExpandedEditor
				visible
				value="a draft that has outgrown the composer"
				onChangeText={() => {}}
				placeholder="Message"
				editable
				onDone={onDone}
			/>,
		);
		expect(tree.root.findByType("Modal" as never).props.presentationStyle).toBe("pageSheet");
		expect(tree.root.findByProps({ accessibilityLabel: "Message" }).props.value).toBe(
			"a draft that has outgrown the composer",
		);
		act(() => {
			pressable(tree, "Done")?.props.onPress();
		});
		expect(onDone).toHaveBeenCalledOnce();
	});
});
