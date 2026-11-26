import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import { act } from "react-test-renderer";
import { expect, it, onTestFinished, vi } from "vitest";
import { AlertCenter, type HoldKind } from "../alerts/alertCenter";
import { AlertsContext } from "../alerts/alertsContext";
import { paletteFor } from "../design/tokens";
import { render, settle, systemGlass } from "../renderNative.testkit";
import { HubWebView } from "./HubWebView";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
const native = vi.hoisted(() => ({
	focused: true,
	share: vi.fn(),
	insets: { left: 0, right: 0, top: 0, bottom: 34 },
	navigation: { setOptions: vi.fn<(options: NativeStackNavigationOptions) => void>() },
}));
vi.mock("@react-navigation/native", () => ({
	useIsFocused: () => native.focused,
	useNavigation: () => native.navigation,
}));
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
	useSafeAreaInsets: () => native.insets,
}));
vi.mock("../reader/shareDocumentNative", () => ({ shareSessionDocument: native.share }));
vi.mock("expo-status-bar", () => ({ StatusBar: "StatusBar" }));

it("preserves a document's share name and releases workspace alert holds with their owners", async () => {
	const center = new AlertCenter({
		now: Date.now,
		setTimeout,
		clearTimeout: (timer) => clearTimeout(timer as ReturnType<typeof setTimeout>),
	});
	const held = new Set<HoldKind>();
	vi.spyOn(center, "hold").mockImplementation((kind) => {
		held.add(kind);
		return () => {
			held.delete(kind);
		};
	});
	const profile = { id: "desk", name: "Desk", origin: "https://desk.example" };
	const workspace = () => (
		<AlertsContext.Provider value={{ center, reportRoutes: vi.fn(), noticeFor: () => undefined }}>
			<HubWebView
				profile={profile}
				token="saved"
				initialPath="/"
				onPath={vi.fn()}
				onHubMenu={vi.fn()}
				onDeviceWork={vi.fn()}
			/>
		</AlertsContext.Provider>
	);
	const tree = render(workspace());
	onTestFinished(() => {
		native.focused = true;
	});
	const view = () => tree.root.findByType("WebView" as never);
	const message = (data: unknown) =>
		act(() => view().props.onMessage({ nativeEvent: { url: profile.origin + "/", data: JSON.stringify(data) } }));
	message({ type: "ready" });
	const target = { ref: "local:one", path: "/private/run/notes.txt", namePath: "notes.txt" };
	message({ type: "shareDocument", ...target });
	await settle();
	expect(native.share).toHaveBeenCalledWith({
		origin: profile.origin,
		token: "saved",
		sessionRef: target.ref,
		path: target.path,
		namePath: target.namePath,
	});
	message({ type: "readingDocument", active: true });
	message({ type: "covered", active: true });
	expect(held).toEqual(new Set(["quiet", "covered"]));
	message({ type: "covered", active: false });
	expect(held).toEqual(new Set(["quiet"]));
	native.focused = false;
	act(() => tree.update(workspace()));
	expect(held.size).toBe(0);
	native.focused = true;
	act(() => tree.update(workspace()));
	expect(held).toEqual(new Set(["quiet"]));
	message({ type: "editing", active: true });
	message({ type: "readingDocument", active: false });
	expect(held).toEqual(new Set(["quiet"]));
	message({ type: "editing", active: false });
	expect(held.size).toBe(0);
	message({ type: "covered", active: true });
	act(() => view().props.onLoadStart());
	message({ type: "ready" });
	expect(held.size).toBe(0);
});

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
	message({ type: "ready" });
	expect(postMessage.mock.calls.map(([value]) => JSON.parse(value))).toEqual([
		{ type: "material", glass: true, edgeToolbar: false },
	]);
	postMessage.mockClear();
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
	expect(JSON.parse(postMessage.mock.lastCall?.[0])).toEqual({ type: "material", glass: false, edgeToolbar: false });
	publish(null);
	expect(tree.root.findAllByProps({ accessibilityLabel: "Back" })).toHaveLength(0);
	const frame = { x: 100, y: 360, width: 44, height: 44 };
	message({ type: "createButton", id: "board", frame });
	expect(JSON.parse(postMessage.mock.lastCall?.[0])).toEqual({ type: "createButtonShown", id: "board" });
	const create = tree.root.findByProps({ testID: "workspace-new-session" });
	act(() => create.props.onPress());
	expect(JSON.parse(postMessage.mock.lastCall?.[0])).toEqual({ type: "createSession", id: "board" });
	message({ type: "createButton", id: "old-drawer", frame: null });
	expect(tree.root.findByProps({ testID: "workspace-new-session" })).toBeTruthy();
	message({ type: "createButton", id: "board", frame: null });
	expect(tree.root.findAllByProps({ testID: "workspace-new-session" })).toHaveLength(0);
	const statusBar = () => tree.root.findByType("StatusBar" as never).props.style;
	const inset = () => tree.root.findByType("SafeAreaView" as never).props.style.backgroundColor;
	expect(statusBar()).toBe("light");
	expect(inset()).toBe(paletteFor("dark").surface);
	message({ type: "appearance", dark: false });
	expect(statusBar()).toBe("dark");
	expect(inset()).toBe(paletteFor("light").surface);
	native.insets.right = 84;
	onTestFinished(() => {
		native.insets.left = 0;
		native.insets.right = 0;
	});
	publish({ ...header, leading: null });
	expect(JSON.parse(postMessage.mock.lastCall?.[0])).toEqual({ type: "material", glass: false, edgeToolbar: true });
	const toolbar = () => native.navigation.setOptions.mock.lastCall?.[0];
	expect(toolbar()?.headerShown).toBe(true);
	expect(toolbar()?.unstable_headerLeftItems?.({})).toEqual([]);
	expect(tree.root.findAllByProps({ accessibilityRole: "header" })).toHaveLength(0);
	for (const item of toolbar()?.unstable_headerRightItems?.({}) ?? []) {
		if (item.type === "button") act(item.onPress);
	}
	expect(postMessage.mock.calls.slice(-2).map(([value]) => JSON.parse(value))).toEqual([
		{ type: "headerAction", id: header.id, action: "find" },
		{ type: "headerAction", id: header.id, action: "overview" },
	]);
	publish(header);
	expect(toolbar()?.unstable_headerLeftItems?.({})[0]).toMatchObject({
		type: "button",
		badge: { value: header.needsYou },
	});
	native.insets.left = native.insets.right;
	publish({ ...header, leading: "back" });
	expect(JSON.parse(postMessage.mock.lastCall?.[0])).toEqual({ type: "material", glass: false, edgeToolbar: false });
	expect(toolbar()?.headerShown).toBe(false);
	expect(tree.root.findByProps({ accessibilityRole: "header" }).props.children).toBe(header.title);
	act(() => view.props.onLoadStart());
	expect(toolbar()?.headerShown).toBe(false);
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

it.each([
	{ sessionRef: "local:one", documentPath: "/s/local%3Aone", delivered: true },
	{ sessionRef: undefined, documentPath: "/s/local%3Aone", delivered: true },
	{ sessionRef: "local:one", documentPath: "/s/local%3Aother", delivered: false },
])(
	"fences a startup edge action to its document and pane ($documentPath, $sessionRef)",
	({ sessionRef, documentPath, delivered }) => {
		native.insets.right = 84;
		onTestFinished(() => {
			native.insets.right = 0;
		});
		const profile = { id: "desk", name: "Desk", origin: "https://desk.example" };
		const postMessage = vi.fn();
		const workspace = (path: string) => (
			<HubWebView
				profile={profile}
				initialPath={path}
				token="saved"
				onPath={vi.fn()}
				onHubMenu={vi.fn()}
				onDeviceWork={vi.fn()}
			/>
		);
		const first = "/s/local%3Aone";
		const tree = render(workspace(first), { createNodeMock: (e) => (e.type === "WebView" ? { postMessage } : null) });
		const item = native.navigation.setOptions.mock.lastCall?.[0].unstable_headerRightItems?.({})[0];
		if (item?.type !== "button") throw new Error("Missing restore action");
		act(item.onPress);
		expect(postMessage).not.toHaveBeenCalled();
		act(() =>
			tree.root.findByType("WebView" as never).props.onMessage({
				nativeEvent: { url: profile.origin + first, data: JSON.stringify({ type: "ready" }) },
			}),
		);
		const publish = () =>
			act(() =>
				tree.root.findByType("WebView" as never).props.onMessage({
					nativeEvent: {
						url: profile.origin + documentPath,
						data: JSON.stringify({
							type: "sessionHeader",
							header: {
								id: "pane",
								sessionRef,
								leading: null,
								needsYou: 0,
								title: "Trail",
								height: 47,
								collapsed: false,
							},
						}),
					},
				}),
			);
		publish();
		publish();
		const actions = () =>
			postMessage.mock.calls.map(([data]) => JSON.parse(data)).filter(({ type }) => type === "headerAction");
		expect(actions()).toEqual(delivered ? [{ type: "headerAction", action: "find", id: "pane" }] : []);
		act(() => tree.unmount());
		const second = render(workspace(first), { createNodeMock: (e) => (e.type === "WebView" ? { postMessage } : null) });
		const early = native.navigation.setOptions.mock.lastCall?.[0].unstable_headerRightItems?.({})[1];
		if (early?.type !== "button") throw new Error("Missing restore action");
		act(early.onPress);
		act(() => second.update(workspace("/s/local%3Atwo")));
		expect(native.navigation.setOptions.mock.lastCall?.[0].headerShown).toBe(false);
		act(() => second.unmount());
		expect(actions()).toHaveLength(delivered ? 1 : 0);
	},
);
