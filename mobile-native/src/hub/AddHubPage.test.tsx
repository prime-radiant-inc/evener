import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { AddHubPage } from "./AddHubPage";
import type { HubRoutes } from "./hubSheetContext";

const mocks = vi.hoisted(() => ({
	saveHub: vi.fn(),
	getStringAsync: vi.fn(),
	permission: null as { granted: boolean; canAskAgain: boolean; status: string } | null,
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ saveHub: mocks.saveHub }) }));
vi.mock("expo-camera", () => ({
	CameraView: "CameraView",
	useCameraPermissions: () => [mocks.permission, vi.fn(), vi.fn()],
}));
vi.mock("expo-clipboard", () => ({ getStringAsync: mocks.getStringAsync }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

it("adds the hub the route names the way to, and leaves closing the sheet to the new selection", async () => {
	mocks.getStringAsync.mockResolvedValue("https://magic-kingdom:9180/auth/s3cret");
	mocks.saveHub.mockResolvedValue(true);
	const navigation = { navigate: vi.fn(), goBack: vi.fn(), getParent: vi.fn() };
	const route = { key: "AddHub", name: "AddHub", params: { how: "paste" } } as const;
	const tree = render(
		<AddHubPage
			navigation={navigation as unknown as NativeStackScreenProps<HubRoutes, "AddHub">["navigation"]}
			route={route}
		/>,
	);
	const press = (label: string) =>
		act(async () => {
			tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: label }).props.onPress();
		});
	await press("Paste");
	await press("Connect");
	expect(mocks.saveHub).toHaveBeenCalledTimes(1);
	expect(navigation.navigate).not.toHaveBeenCalled();
	expect(navigation.goBack).not.toHaveBeenCalled();
	expect(navigation.getParent).not.toHaveBeenCalled();
});

it("retitles the page when scanning gives way to pasting", async () => {
	mocks.permission = { granted: false, canAskAgain: false, status: "denied" };
	const navigation = { navigate: vi.fn(), goBack: vi.fn(), getParent: vi.fn(), setParams: vi.fn() };
	const route = { key: "AddHub", name: "AddHub", params: { how: "scan" } } as const;
	const tree = render(
		<AddHubPage
			navigation={navigation as unknown as NativeStackScreenProps<HubRoutes, "AddHub">["navigation"]}
			route={route}
		/>,
	);
	await act(async () => {
		tree.root
			.findByProps({ accessibilityRole: "button", accessibilityLabel: "Paste the link instead" })
			.props.onPress();
	});
	expect(navigation.setParams).toHaveBeenCalledWith({ how: "paste" });
});
