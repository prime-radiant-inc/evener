import type { LaunchOption } from "@evener/appwire-client";
import { FlatList } from "react-native";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { LaunchFieldEditor } from "./LaunchFieldEditor";
import { pressable, render } from "./renderNative.testkit";

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));

const option = (kind: LaunchOption["kind"], field: string): LaunchOption => ({
	field,
	perLaunch: true,
	wireField: field,
	label: field,
	group: "Launch",
	kind,
	defaultableLayers: ["global"],
});

it.each([
	["a path list", option("pathList", "addDirs")],
	["an environment map", option("envMap", "env")],
	["a text field", option("text", "systemPrompt")],
])("lets a drag of %s's editor put the keyboard away", (_name, edited) => {
	const tree = render(
		<LaunchFieldEditor option={edited} value={{}} effective={{}} client={null} close={() => {}} apply={() => {}} />,
	);
	expect(tree.root.findAllByType("ScrollView" as never)[0]?.props.keyboardDismissMode).toBe("on-drag");
	act(() => tree.unmount());
});

it("lets a drag of the model picker put the keyboard away", () => {
	const tree = render(
		<LaunchFieldEditor
			option={option("modelPicker", "model")}
			value={{}}
			effective={{}}
			client={null}
			close={() => {}}
			apply={() => {}}
		/>,
	);
	expect(tree.root.findByType(FlatList).props.keyboardDismissMode).toBe("on-drag");
	act(() => tree.unmount());
});

it("opens a path list's effective values from a toggle that says whether they're shown", () => {
	const tree = render(
		<LaunchFieldEditor
			option={option("pathList", "addDirs")}
			value={{}}
			effective={{}}
			client={null}
			close={() => {}}
			apply={() => {}}
		/>,
	);
	const toggle = () => pressable(tree, "Effective values (0)");
	expect(toggle()?.props.accessibilityState).toMatchObject({ expanded: false });
	act(() => toggle()?.props.onPress());
	expect(toggle()?.props.accessibilityState).toMatchObject({ expanded: true });
	act(() => tree.unmount());
});
