// The form, Access and More options read the hub's launch defaults from the
// sheet: one evener/launch/resolve for the host and project, shown in all
// three at once, and one more when the hub's launch settings change.
import type { AnyNotification } from "@evener/appwire-client";
import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { createNewSessionStore } from "../newSession";
import { render, renderedText, settle } from "../renderNative.testkit";
import { AccessPicker } from "./AccessPicker";
import { MoreOptions } from "./MoreOptions";
import { NewSessionForm } from "./NewSessionForm";
import type { NewSessionRoutes } from "./newSessionContext";
import { sheetContext, TestSheet } from "./newSessionTestUtils";

vi.mock("../board/connectionStatus", async (importOriginal) => ({
	...(await importOriginal<typeof import("../board/connectionStatus")>()),
	useConnectionStatusText: () => null,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => undefined | (() => void)) => useEffect(effect, [effect]),
		StackActions: { replace: () => ({}) },
	};
});
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("../nativeImagePicker", () => ({
	nativeImagePicker: {
		pick: async () => [],
		capture: async () => [],
		encode: async () => ({ data: "", mediaType: "image/jpeg" }),
		id: () => "image-1",
	},
}));

it("reads the defaults once for the form and both pages, and again when the hub's launch settings change", async () => {
	const resolves: unknown[] = [];
	let defaults: Record<string, unknown> = { sandbox: "read-only", maxSubagentDepth: 2 };
	let notify: (notification: AnyNotification) => void = () => {};
	const client = {
		request: async (method: string, params: unknown) => {
			const forwarded = method === "evener/host/request" ? (params as { method: string }).method : method;
			if (forwarded === "evener/launch/resolve") {
				resolves.push(params);
				return { effective: defaults, layers: {}, provenance: {} };
			}
			if (forwarded === "evener/host/list") return { hosts: [] };
			if (forwarded === "evener/git/head") return { head: "" };
			if (forwarded === "evener/plugin/preview") return { plugins: [] };
			return { data: [] };
		},
		onNotification: (handler: (notification: AnyNotification) => void) => {
			notify = handler;
			return () => {};
		},
	};
	const store = createNewSessionStore("hub-1");
	store.setState({ source: "paradise-park", cwd: "/Users/jesse/git/evener" });
	const navigation = { setOptions: () => {}, navigate: () => {}, isFocused: () => true, getParent: () => undefined };
	const tree = render(
		<TestSheet value={sheetContext(store, { client: client as never })}>
			<NewSessionForm
				navigation={navigation as unknown as NativeStackScreenProps<NewSessionRoutes, "Form">["navigation"]}
				route={{ key: "Form", name: "Form", params: undefined }}
			/>
			<AccessPicker />
			<MoreOptions />
		</TestSheet>,
	);
	await settle();
	expect(resolves).toEqual([
		{ host: "paradise-park", method: "evener/launch/resolve", params: { cwd: "/Users/jesse/git/evener" } },
	]);
	const text = () => renderedText(tree);
	const accessValues = () =>
		tree.root
			.findAll((node) => node.props.accessibilityLabel?.startsWith?.("Access, "))
			.map((node) => node.props.accessibilityLabel);
	expect(accessValues()).toContain("Access, Writes nothing; reads anywhere but secrets, Read-only");
	expect(
		tree.root.findAll(
			(node) => node.props.accessibilityLabel === "Read-only, Writes nothing; reads anywhere but secrets",
		)[0]?.props.accessibilityState.selected,
	).toBe(true);
	expect(text()).toContain("The hub's default is 2.");

	defaults = { sandbox: "restricted", maxSubagentDepth: 3 };
	await act(async () => notify({ method: "evener/launch/updated", params: {} } as never));
	await settle();
	expect(resolves).toHaveLength(2);
	expect(accessValues()).toContain("Access, Reads and writes only in the project, Restricted");
	expect(text()).toContain("The hub's default is 3.");
});
