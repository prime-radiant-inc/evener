import { act } from "react-test-renderer";
import { expect, it, onTestFinished, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import { render, settle, systemGlass } from "../renderNative.testkit";
import { HubWebView } from "./HubWebView";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("@react-navigation/native", () => ({ useIsFocused: () => true }));
vi.mock("expo-status-bar", () => ({ StatusBar: "StatusBar" }));

it("routes the active native session controls back to their pane and gives the header back when it closes", async () => {
	const profile = { id: "e983b944-6ad8-408a-b80a-f004b6a00cf8", name: "Desk", origin: "https://desk.example" };
	const postMessage = vi.fn();
	systemGlass.available = true;
	onTestFinished(() => systemGlass.reset());
	const tree = render(
		<HubWebView
			profile={profile}
			token="hello"
			initialPath="/"
			onPath={vi.fn()}
			onHubMenu={vi.fn()}
			onDeviceWork={vi.fn()}
		/>,
		{ createNodeMock: (element) => (element.type === "WebView" ? { postMessage } : null) },
	);
	const view = tree.root.findByType("WebView" as never);
	const message = (payload: unknown) =>
		act(() =>
			view.props.onMessage({
				nativeEvent: { url: profile.origin + "/", data: JSON.stringify(payload) },
			}),
		);
	const publish = (header: unknown) => message({ type: "sessionHeader", header });
	const header = {
		id: "pane1",
		title: "Build notes",
		leading: "sessions",
		needsYou: 3,
		collapsed: false,
		height: 61,
	};
	publish(header);
	message({ type: "appearance", dark: true });
	await settle();
	expect(tree.root.findAllByType("GlassView" as never)).toHaveLength(2);
	expect(tree.root.findByProps({ accessibilityRole: "header" }).props.children).toBe(header.title);
	const press = (label: string) => act(() => tree.root.findByProps({ accessibilityLabel: label }).props.onPress());
	const sessions = tree.root
		.findAllByType("Pressable" as never)
		.find((button) => button.props.accessibilityLabel.includes(String(header.needsYou)));
	expect(sessions).toBeDefined();
	act(() => sessions?.props.onPress());
	press("Find in session");
	press("Overview");
	publish({ ...header, leading: "back", collapsed: true, id: "pane2" });
	expect(tree.root.findAllByProps({ accessibilityRole: "header" })).toHaveLength(0);
	press("Back");
	expect(postMessage.mock.calls.map(([message]) => JSON.parse(message))).toEqual([
		...["sessions", "find", "overview"].map((action) => ({ type: "headerAction", id: header.id, action })),
		{ type: "headerAction", id: "pane2", action: "back" },
	]);
	act(() => systemGlass.setReduceTransparency(true));
	expect(tree.root.findAllByType("GlassView" as never)).toHaveLength(0);
	publish(null);
	expect(tree.root.findAllByProps({ accessibilityLabel: "Back" })).toHaveLength(0);
	const statusBar = () => tree.root.findByType("StatusBar" as never).props.style;
	const inset = () => tree.root.findByType("SafeAreaView" as never).props.style.backgroundColor;
	expect(statusBar()).toBe("light");
	expect(inset()).toBe(paletteFor("dark").surface);
	message({ type: "appearance", dark: false });
	expect(statusBar()).toBe("dark");
	expect(inset()).toBe(paletteFor("light").surface);
	publish(header);
	act(() => view.props.onLoadStart());
	expect(tree.root.findAllByProps({ accessibilityRole: "header" })).toHaveLength(0);
	expect(tree.root.findAllByType("StatusBar" as never)).toHaveLength(0);
});

it("opens native controls only from the paired app and returns to its session after a failed load", () => {
	const profile = { id: "e983b944-6ad8-408a-b80a-f004b6a00cf8", name: "Desk", origin: "https://desk.example" };
	const onHubMenu = vi.fn();
	const onPath = vi.fn();
	const path = "/s/desk%3Aone";
	const tree = render(
		<HubWebView
			profile={profile}
			token="hello"
			initialPath="/"
			onPath={onPath}
			onHubMenu={onHubMenu}
			onDeviceWork={vi.fn()}
		/>,
	);
	const view = () => tree.root.findByType("WebView" as never);
	const message = (url: string, value: unknown) => {
		act(() => view().props.onMessage({ nativeEvent: { url, data: JSON.stringify(value) } }));
	};
	message(profile.origin + "/doc/file", { type: "hubMenu" });
	message("https://elsewhere.example/", { type: "hubMenu" });
	expect(onHubMenu).not.toHaveBeenCalled();
	message(profile.origin + "/", { type: "hubMenu" });
	expect(onHubMenu).toHaveBeenCalledOnce();
	message(profile.origin + "/", { type: "route", path });
	expect(onPath).toHaveBeenCalledWith(path);
	act(() => view().props.onError());
	expect(tree.root.findAllByType("WebView" as never)).toHaveLength(0);
	act(() => tree.root.findByProps({ accessibilityLabel: "Try again" }).props.onPress());
	expect(view().props.source).toMatchObject({ uri: profile.origin + path, websiteDataStoreIdentifier: profile.id });
	act(() => view().props.onError());
	const alertPath = "/s/desk%3Atwo";
	act(() =>
		tree.update(
			<HubWebView
				profile={profile}
				token="hello"
				initialPath={alertPath}
				onPath={onPath}
				onHubMenu={onHubMenu}
				onDeviceWork={vi.fn()}
			/>,
		),
	);
	act(() => tree.root.findByProps({ accessibilityLabel: "Try again" }).props.onPress());
	expect(view().props.source.uri).toBe(profile.origin + alertPath);
	act(() => tree.unmount());
});
