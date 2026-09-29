import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { pressable, render, renderedText } from "../renderNative.testkit";
import { NewSessionProvider, type NewSessionRoutes } from "./newSessionContext";
import { sheetContext } from "./newSessionTestUtils";
import { ProjectPicker } from "./ProjectPicker";

vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => null,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

function mount(cwd: string) {
	const store = createNewSessionStore("hub-1");
	store.setState({
		source: "paradise-park",
		cwd,
		projects: ["/Users/jesse/git/evener", "/Users/jesse/git/docs"],
	});
	const navigation = { goBack: vi.fn(), navigate: vi.fn() };
	const tree = render(
		<NewSessionProvider value={sheetContext(store)}>
			<ProjectPicker
				navigation={navigation as unknown as NativeStackScreenProps<NewSessionRoutes, "Project">["navigation"]}
				route={{ key: "Project", name: "Project", params: undefined }}
			/>
		</NewSessionProvider>,
	);
	const project = (name: string) =>
		tree.root.findAll(
			(node) => node.props.accessibilityRole === "button" && node.props.accessibilityLabel?.startsWith(`${name}, `),
		)[0];
	const search = () => tree.root.findByProps({ accessibilityLabel: "Projects on paradise-park" });
	return { tree, store, navigation, project, search };
}

it("lists the host's recent projects by name and path, the chosen one checked", () => {
	const picker = mount("/Users/jesse/git/evener");
	expect(renderedText(picker.tree)).toContain("Recent on paradise-park");
	expect(picker.project("evener")?.props).toMatchObject({
		accessibilityLabel: "evener, /Users/jesse/git/evener",
		accessibilityState: { disabled: false, selected: true },
	});
	expect(picker.project("docs")?.props.accessibilityState).toEqual({ disabled: false, selected: false });
});

it("filters by name and by path", () => {
	const picker = mount("");
	act(() => picker.search().props.onChangeText("DOC"));
	expect(picker.project("docs")).toBeDefined();
	expect(picker.project("evener")).toBeUndefined();
	act(() => picker.search().props.onChangeText("jesse/git/ev"));
	expect(picker.project("evener")).toBeDefined();
	expect(picker.project("docs")).toBeUndefined();
});

it("sets the project and goes back to the form when one is chosen", async () => {
	const picker = mount("/Users/jesse/git/evener");
	await act(async () => picker.project("docs")?.props.onPress());
	expect(picker.store.getState().cwd).toBe("/Users/jesse/git/docs");
	expect(picker.navigation.goBack).toHaveBeenCalledTimes(1);
});

it("browses from the chosen project's folder, or from home with none", () => {
	const chosen = mount("/Users/jesse/git/evener");
	act(() => pressable(chosen.tree, "Browse folders on paradise-park…")?.props.onPress());
	expect(chosen.navigation.navigate).toHaveBeenCalledWith("Browse", { dir: "/Users/jesse/git" });
	const none = mount("");
	act(() => pressable(none.tree, "Browse folders on paradise-park…")?.props.onPress());
	expect(none.navigation.navigate).toHaveBeenCalledWith("Browse", { dir: "" });
});
