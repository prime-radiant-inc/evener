import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { Row } from "../sheet/Grouped";
import { HubsPage } from "./HubsPage";
import type { HubRoutes } from "./hubSheetContext";

// What useConnection answers with. vi.hoisted because vi.mock's factory is
// hoisted above every module import and may not close over a module-level let.
const connection = vi.hoisted(() => ({
	current: {
		profiles: [
			{ id: "home", name: "magic-kingdom", origin: "http://100.113.28.18:9180" },
			{ id: "work", name: "paradise-park", origin: "http://10.0.0.7:9180" },
		],
		activeProfile: { id: "home" },
		selectHub: () => {},
		state: "ready",
		fatal: false,
		downSince: null as number | null,
		lastLiveAt: null as number | null,
	},
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => connection.current }));
vi.mock("./hubSheetContext", () => ({ useHubSheet: () => ({ ready: connection.current.state === "ready" }) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

function subOf(tree: ReturnType<typeof render>, name: string): string | undefined {
	return tree.root.findAll((node) => node.type === Row && node.props.label === name)[0]?.props.sub;
}

function mount() {
	const navigation = { navigate: vi.fn() };
	return render(
		<HubsPage
			navigation={navigation as unknown as NativeStackScreenProps<HubRoutes, "Hubs">["navigation"]}
			route={{ key: "Hubs", name: "Hubs", params: undefined } as NativeStackScreenProps<HubRoutes, "Hubs">["route"]}
		/>,
	);
}

it("says the selected hub is connected beside its address, and gives the others their address alone", () => {
	const tree = mount();
	expect(subOf(tree, "magic-kingdom")).toBe("http://100.113.28.18:9180 · Connected");
	expect(subOf(tree, "paradise-park")).toBe("http://10.0.0.7:9180");
});

it("says the selected hub's connection state in the words the Hub's header uses", () => {
	vi.useFakeTimers();
	try {
		const now = Date.now();
		connection.current = { ...connection.current, state: "reconnecting", downSince: now - 5_000, lastLiveAt: now };
		const tree = mount();
		expect(subOf(tree, "magic-kingdom")).toBe("http://100.113.28.18:9180 · Reconnecting…");
		act(() => tree.unmount());
	} finally {
		vi.useRealTimers();
	}
});
