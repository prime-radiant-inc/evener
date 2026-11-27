import type { NativeStackNavigationOptions } from "@react-navigation/native-stack";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { expect, it, onTestFinished, vi } from "vitest";
import { DEFAULT_ALERT_PREFERENCES } from "../alerts/alertCenter";
import { AlertCenter, type HoldKind } from "../alerts/alertCenter";
import { AlertsContext } from "../alerts/alertsContext";
import { alertPreferences } from "../alerts/nativeAlertPreferences";
import { paletteFor } from "../design/tokens";
import { alertRequests, render, settle, systemGlass } from "../renderNative.testkit";
import { createShareIntake } from "../share/shareIntake";
import { pendingShares } from "../share/sharePayload";
import { share as payload, sharePort } from "../share/shareTestkit";
import { HubWebView } from "./HubWebView";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
const native = vi.hoisted(() => ({
	focused: true,
	share: vi.fn(),
	insets: { left: 0, right: 0, top: 0, bottom: 34 },
	navigation: { setOptions: vi.fn<(options: NativeStackNavigationOptions) => void>() },
}));

function evaluateHubLoadProbe(
	tree: ReactTestRenderer,
	script: unknown,
	profile: { origin: string },
	rootMarker: boolean | null,
): void {
	if (typeof script !== "string") throw new Error("hub page probe missing");
	const view = tree.root.findByType("WebView" as never);
	const evaluate = new Function("document", "window", script) as (
		document: { getElementById(id: string): { hasAttribute(name: string): boolean } | null },
		window: { ReactNativeWebView: { postMessage(json: string): void } },
	) => void;
	evaluate(
		{
			getElementById: () => (rootMarker === null ? null : { hasAttribute: () => rootMarker }),
		},
		{
			ReactNativeWebView: {
				postMessage: (raw) => {
					const data = JSON.parse(raw);
					act(() =>
						view.props.onMessage({
							nativeEvent: { url: profile.origin + "/", data: JSON.stringify(data) },
						}),
					);
				},
			},
		},
	);
}

it.each([false, true])("accepts the delivered launcher share once (WebKit has navigated: %s)", async (webNavigated) => {
	const profile = { id: "desk", name: "Desk", origin: "https://desk.example" };
	const share = { id: "file-id", path: "/new", hubId: profile.id, text: "shared-sentinel", createdAt: "" };
	const postMessage = vi.fn();
	const other = payload("other-file", 1);
	const port = sharePort([other, { ...share, createdAt: new Date(2).toISOString() }]);
	const offered = vi.fn();
	const intake = createShareIntake({ port, onShare: offered });
	const onSharePlaced = vi.fn(async (id: string) => {
		intake.forget(id);
		await intake.advance(id);
	});
	const workspace = (hubId?: string, initialPath = "/new") => (
		<HubWebView
			profile={profile}
			token=""
			initialPath={initialPath}
			onPath={vi.fn()}
			onHubMenu={vi.fn()}
			share={hubId ? { ...share, hubId } : undefined}
			onSharePlaced={onSharePlaced}
		/>
	);
	const tree = render(workspace("other", "/new?dir=%2Fwork"), {
		createNodeMock: (node) => (node.type === "WebView" ? { postMessage } : null),
	});
	const message = (payload: object, url = profile.origin + "/new") =>
		act(() =>
			tree.root.findByType("WebView" as never).props.onMessage({ nativeEvent: { url, data: JSON.stringify(payload) } }),
		);
	const target = { type: "shareTarget", path: share.path };
	const receipt = { type: "sharePlaced", path: share.path, id: share.id };
	const sent = () => postMessage.mock.calls.map(([raw]) => JSON.parse(raw)).filter((data) => data.type === "shareText");
	message({ type: "ready", capabilities: ["incomingShare", "needsYouNavigation"] });
	message(target, profile.origin + "/new?dir=%2Fwork");
	expect(sent()).toEqual([]);
	message({ type: "covered", active: true });
	act(() => tree.update(workspace(profile.id)));
	message(receipt);
	expect(onSharePlaced).not.toHaveBeenCalled();
	expect(sent()).toEqual([]);
	message({ type: "covered", active: false });
	expect(sent()).toEqual([{ type: "shareText", path: share.path, id: share.id, text: share.text }]);
	message(receipt, "https://other.example/new");
	message(receipt, profile.origin + "/s/local%3Astale");
	message({ ...receipt, id: "stale" });
	expect(onSharePlaced).not.toHaveBeenCalled();
	// The launcher can finish after another pane opens, with the intake
	// already holding a different file for the picker.
	const nextPath = "/s/local%3Aother";
	if (webNavigated) message({ type: "route", path: nextPath }, profile.origin + nextPath);
	native.focused = false;
	onTestFinished(() => {
		native.focused = true;
	});
	act(() => tree.update(workspace(undefined, nextPath)));
	await intake.offer();
	expect(offered).toHaveBeenCalledWith(other);
	message(receipt, profile.origin + (webNavigated ? nextPath : share.path));
	message(receipt, profile.origin + (webNavigated ? nextPath : share.path));
	expect(onSharePlaced.mock.calls).toEqual([[share.id]]);
	await onSharePlaced.mock.results[0]?.value;
	expect(await pendingShares(port)).toEqual([other]);
	expect(intake.busy).toBe(true);
	expect(offered).toHaveBeenCalledTimes(1);
});

it("reopens the selected session when an explicit share targets the current workspace URL", () => {
	const profile = { id: "desk", name: "Desk", origin: "https://desk.example" };
	const share = {
		id: "same-session-share",
		hubId: profile.id,
		path: "/s/local%3Aone",
		text: "shared-sentinel",
		createdAt: "",
	};
	const postMessage = vi.fn();
	const tree = render(
		<HubWebView
			profile={profile}
			token=""
			initialPath={share.path}
			onPath={vi.fn()}
			onHubMenu={vi.fn()}
			share={share}
		/>,
		{ createNodeMock: (node) => (node.type === "WebView" ? { postMessage } : null) },
	);
	const message = (data: object, url = profile.origin + share.path) =>
		act(() =>
			tree.root.findByType("WebView" as never).props.onMessage({ nativeEvent: { url, data: JSON.stringify(data) } }),
		);
	message({ type: "ready", capabilities: ["incomingShare", "needsYouNavigation"] });
	message({ type: "shareTarget", path: share.path });
	const sent = postMessage.mock.calls.map(([raw]) => JSON.parse(raw));
	expect(sent).toContainEqual({ type: "navigate", path: share.path });
	expect(sent).toContainEqual({ type: "shareText", path: share.path, id: share.id, text: share.text });
});

it("accepts a late conversation share receipt from its document only", () => {
	const profile = { id: "desk", name: "Desk", origin: "https://desk.example" };
	const share = {
		id: "late-session-share",
		hubId: profile.id,
		path: "/s/local%3Aone",
		text: "shared-sentinel",
		createdAt: "",
	};
	const onSharePlaced = vi.fn();
	const tree = render(
		<HubWebView
			profile={profile}
			token=""
			initialPath={share.path}
			onPath={vi.fn()}
			onHubMenu={vi.fn()}
			share={share}
			onSharePlaced={onSharePlaced}
		/>,
		{ createNodeMock: (node) => (node.type === "WebView" ? { postMessage: vi.fn() } : null) },
	);
	const view = () => tree.root.findByType("WebView" as never);
	const message = (data: object, path = share.path) =>
		act(() => view().props.onMessage({ nativeEvent: { url: profile.origin + path, data: JSON.stringify(data) } }));
	message({ type: "ready", capabilities: ["incomingShare"] });
	message({ type: "shareTarget", path: share.path });
	message({ type: "route", path: "/s/local%3Atwo" }, "/s/local%3Atwo");
	message({ type: "sharePlaced", path: share.path, id: "unrelated-id" }, "/s/local%3Atwo");
	message({ type: "sharePlaced", path: "/new", id: share.id }, "/s/local%3Atwo");
	expect(onSharePlaced).not.toHaveBeenCalled();
	message({ type: "sharePlaced", path: share.path, id: share.id }, "/s/local%3Atwo");
	expect(onSharePlaced).toHaveBeenCalledExactlyOnceWith(share.id);

	act(() => view().props.onLoadStart());
	message({ type: "sharePlaced", path: share.path, id: share.id }, "/s/local%3Atwo");
	expect(onSharePlaced).toHaveBeenCalledExactlyOnceWith(share.id);
	act(() => tree.unmount());
});

it("keeps a share pending for an older hub and retries delivery after reload discovers support", async () => {
	const profile = { id: "desk", name: "Desk", origin: "https://desk.example" };
	const share = {
		id: "pending-share",
		hubId: profile.id,
		path: "/new",
		text: "shared-sentinel",
		createdAt: new Date(1).toISOString(),
	};
	const port = sharePort([share]);
	const onSharePlaced = vi.fn();
	const postMessage = vi.fn();
	alertRequests.length = 0;
	const tree = render(
		<HubWebView
			profile={profile}
			token=""
			initialPath={share.path}
			onPath={vi.fn()}
			onHubMenu={vi.fn()}
			share={share}
			onSharePlaced={onSharePlaced}
		/>,
		{ createNodeMock: (node) => (node.type === "WebView" ? { postMessage, reload: vi.fn() } : null) },
	);
	const view = () => tree.root.findByType("WebView" as never);
	const message = (data: object) =>
		act(() =>
			view().props.onMessage({ nativeEvent: { url: profile.origin + share.path, data: JSON.stringify(data) } }),
		);
	message({ type: "ready" });
	expect(alertRequests).toHaveLength(1);
	alertRequests[0]?.buttons?.find((button) => button.style === "cancel")?.onPress?.();
	expect(onSharePlaced).not.toHaveBeenCalled();
	expect(postMessage.mock.calls.map(([raw]) => JSON.parse(raw))).not.toContainEqual(
		expect.objectContaining({ type: "shareText" }),
	);
	expect(await pendingShares(port)).toEqual([{ id: share.id, text: share.text, createdAt: share.createdAt }]);

	act(() => view().props.onLoadStart());
	message({ type: "ready", capabilities: ["incomingShare", "needsYouNavigation"] });
	message({ type: "shareTarget", path: share.path });
	expect(postMessage.mock.calls.map(([raw]) => JSON.parse(raw))).toContainEqual({
		type: "shareText",
		path: share.path,
		id: share.id,
		text: share.text,
	});
	act(() => tree.unmount());
});

it("identifies an unmarked workspace root after document load without consuming its share", async () => {
	const profile = { id: "desk", name: "Desk", origin: "https://desk.example" };
	const share = {
		id: "legacy-share",
		hubId: profile.id,
		path: "/new",
		text: "shared-sentinel",
		createdAt: new Date(1).toISOString(),
	};
	const port = sharePort([share]);
	const onSharePlaced = vi.fn();
	alertRequests.length = 0;
	const injectJavaScript = vi.fn();
	const postMessage = vi.fn();
	const tree = render(
		<HubWebView
			profile={profile}
			token=""
			initialPath={share.path}
			onPath={vi.fn()}
			onHubMenu={vi.fn()}
			share={share}
			onSharePlaced={onSharePlaced}
		/>,
		{ createNodeMock: (node) => (node.type === "WebView" ? { injectJavaScript, postMessage } : null) },
	);
	const view = () => tree.root.findByType("WebView" as never);
	act(() => view().props.onLoad({ nativeEvent: { url: profile.origin + "/" } }));
	expect(alertRequests).toHaveLength(0);
	evaluateHubLoadProbe(tree, injectJavaScript.mock.calls[0]?.[0], profile, false);
	expect(alertRequests).toHaveLength(1);
	act(() =>
		view().props.onMessage({ nativeEvent: { url: profile.origin + "/", data: JSON.stringify({ type: "ready" }) } }),
	);
	expect(alertRequests).toHaveLength(1);
	expect(onSharePlaced).not.toHaveBeenCalled();
	expect(await pendingShares(port)).toEqual([{ id: share.id, text: share.text, createdAt: share.createdAt }]);
	act(() => tree.unmount());
});

it("recognizes an unmarked workspace root for cold grouped alerts while keeping ordinary work quiet", () => {
	const profile = { id: "desk", name: "Desk", origin: "https://desk.example" };
	const postMessage = vi.fn();
	const injectJavaScript = vi.fn();
	const onPath = vi.fn();
	alertRequests.length = 0;
	const tree = render(
		<HubWebView profile={profile} token="" initialPath="/?attention=needs-you" onPath={onPath} onHubMenu={vi.fn()} />,
		{ createNodeMock: (node) => (node.type === "WebView" ? { postMessage, injectJavaScript } : null) },
	);
	const view = () => tree.root.findByType("WebView" as never);
	act(() => view().props.onLoad({ nativeEvent: { url: profile.origin + "/" } }));
	expect(alertRequests).toHaveLength(0);
	evaluateHubLoadProbe(tree, injectJavaScript.mock.calls[0]?.[0], profile, false);
	expect(alertRequests).toHaveLength(1);
	act(() =>
		view().props.onMessage({ nativeEvent: { url: profile.origin + "/", data: JSON.stringify({ type: "ready" }) } }),
	);
	expect(alertRequests).toHaveLength(1);
	expect(onPath).not.toHaveBeenCalled();
	expect(postMessage.mock.calls.map(([raw]) => JSON.parse(raw))).not.toContainEqual(
		expect.objectContaining({ type: "navigate", path: "/?attention=needs-you" }),
	);
	act(() => tree.unmount());

	alertRequests.length = 0;
	const ordinaryProbe = vi.fn();
	const ordinaryTree = render(
		<HubWebView profile={profile} token="" initialPath="/" onPath={vi.fn()} onHubMenu={vi.fn()} />,
		{ createNodeMock: (node) => (node.type === "WebView" ? { injectJavaScript: ordinaryProbe } : null) },
	);
	act(() =>
		ordinaryTree.root.findByType("WebView" as never).props.onLoad({ nativeEvent: { url: profile.origin + "/" } }),
	);
	evaluateHubLoadProbe(ordinaryTree, ordinaryProbe.mock.calls[0]?.[0], profile, false);
	expect(alertRequests).toHaveLength(0);
	act(() => ordinaryTree.unmount());
});

it("does not prompt a marked workspace while its capability handshake is delayed", () => {
	const profile = { id: "desk", name: "Desk", origin: "https://desk.example" };
	const share = {
		id: "slow-share",
		hubId: profile.id,
		path: "/new",
		text: "shared-sentinel",
		createdAt: new Date(1).toISOString(),
	};
	for (const rootMarker of [true, null]) {
		for (const initialPath of [share.path, "/?attention=needs-you"]) {
			alertRequests.length = 0;
			const injectJavaScript = vi.fn();
			const tree = render(
				<HubWebView
					profile={profile}
					token=""
					initialPath={initialPath}
					onPath={vi.fn()}
					onHubMenu={vi.fn()}
					share={initialPath === share.path ? share : undefined}
				/>,
				{ createNodeMock: (node) => (node.type === "WebView" ? { injectJavaScript } : null) },
			);
			act(() => tree.root.findByType("WebView" as never).props.onLoad({ nativeEvent: { url: profile.origin + "/" } }));
			evaluateHubLoadProbe(tree, injectJavaScript.mock.calls[0]?.[0], profile, rootMarker);
			expect(alertRequests).toHaveLength(0);
			act(() => tree.unmount());
		}
	}
});

it("preserves a cold Needs You request while an older hub reports its safe root route", () => {
	const profile = { id: "desk", name: "Desk", origin: "https://desk.example" };
	const onPath = vi.fn();
	const postMessage = vi.fn();
	alertRequests.length = 0;
	const tree = render(
		<HubWebView profile={profile} token="" initialPath="/?attention=needs-you" onPath={onPath} onHubMenu={vi.fn()} />,
		{ createNodeMock: (node) => (node.type === "WebView" ? { postMessage } : null) },
	);
	const view = () => tree.root.findByType("WebView" as never);
	const message = (data: object, url = profile.origin + "/") =>
		act(() => view().props.onMessage({ nativeEvent: { url, data: JSON.stringify(data) } }));
	message({ type: "route", path: "/" });
	message({ type: "ready" });
	expect(onPath).not.toHaveBeenCalled();
	expect(alertRequests).toHaveLength(1);
	alertRequests[0]?.buttons?.find((button) => button.style === "cancel")?.onPress?.();
	act(() => tree.update(<HubWebView profile={profile} token="" initialPath="/" onPath={onPath} onHubMenu={vi.fn()} />));
	act(() =>
		tree.update(
			<HubWebView profile={profile} token="" initialPath="/?attention=needs-you" onPath={onPath} onHubMenu={vi.fn()} />,
		),
	);
	expect(alertRequests).toHaveLength(2);
	expect(postMessage.mock.calls.map(([raw]) => JSON.parse(raw))).not.toContainEqual(
		expect.objectContaining({ type: "navigate", path: "/?attention=needs-you" }),
	);
	act(() => tree.unmount());
});

it("opens the cold Needs You request only after the hub handshake confirms support", () => {
	const profile = { id: "desk", name: "Desk", origin: "https://desk.example" };
	const postMessage = vi.fn();
	const tree = render(
		<HubWebView profile={profile} token="" initialPath="/?attention=needs-you" onPath={vi.fn()} onHubMenu={vi.fn()} />,
		{ createNodeMock: (node) => (node.type === "WebView" ? { postMessage } : null) },
	);
	const view = () => tree.root.findByType("WebView" as never);
	act(() =>
		view().props.onMessage({
			nativeEvent: {
				url: profile.origin + "/",
				data: JSON.stringify({ type: "ready", capabilities: ["needsYouNavigation"] }),
			},
		}),
	);
	expect(postMessage.mock.calls.map(([raw]) => JSON.parse(raw))).toContainEqual({
		type: "navigate",
		path: "/?attention=needs-you",
	});
	act(() => tree.unmount());
});
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

it("reads and updates alert preferences through the trusted hub bridge", () => {
	const profile = { id: "desk", name: "Desk", origin: "https://desk.example" };
	const postMessage = vi.fn();
	alertPreferences().set(DEFAULT_ALERT_PREFERENCES);
	onTestFinished(() => alertPreferences().set(DEFAULT_ALERT_PREFERENCES));
	const tree = render(<HubWebView profile={profile} token="" initialPath="/" onPath={vi.fn()} onHubMenu={vi.fn()} />, {
		createNodeMock: (node) => (node.type === "WebView" ? { postMessage } : null),
	});
	const message = (data: object, url = profile.origin + "/") =>
		act(() =>
			tree.root.findByType("WebView" as never).props.onMessage({ nativeEvent: { url, data: JSON.stringify(data) } }),
		);
	const preferences = () => alertPreferences().getSnapshot();
	const replies = () => postMessage.mock.calls.map(([raw]) => JSON.parse(raw));

	message({ type: "getAlertPreferences" });
	expect(replies()).toEqual([{ type: "alertPreferences", preferences: DEFAULT_ALERT_PREFERENCES }]);

	message({ type: "setAlertPreference", key: "haptics", value: false });
	expect(preferences().haptics).toBe(false);
	expect(replies().at(-1)).toEqual({
		type: "alertPreferences",
		preferences: { ...DEFAULT_ALERT_PREFERENCES, haptics: false },
	});

	const current = preferences();
	const replyCount = replies().length;
	message({ type: "setAlertPreference", key: "unknown", value: false });
	message({ type: "setAlertPreference", key: "failures", value: "false" });
	message({ type: "getAlertPreferences" }, "https://untrusted.example/");
	expect(preferences()).toBe(current);
	expect(replies()).toHaveLength(replyCount);
	act(() => tree.unmount());
});

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
			<HubWebView profile={profile} token="saved" initialPath="/" onPath={vi.fn()} onHubMenu={vi.fn()} />
		</AlertsContext.Provider>
	);
	const tree = render(workspace());
	onTestFinished(() => {
		native.focused = true;
	});
	const view = () => tree.root.findByType("WebView" as never);
	const message = (data: unknown) =>
		act(() => view().props.onMessage({ nativeEvent: { url: profile.origin + "/", data: JSON.stringify(data) } }));
	message({ type: "ready", capabilities: ["incomingShare", "needsYouNavigation"] });
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
		<HubWebView profile={profile} token="hello" initialPath="/" onPath={vi.fn()} onHubMenu={vi.fn()} />,
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
	postMessage.mockClear();
	message({ type: "createButton", id: "board", frame: { ...frame, x: frame.x + 3 } });
	expect(postMessage.mock.calls.map(([value]) => JSON.parse(value))).toEqual([
		{ type: "createButtonShown", id: "board" },
	]);
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
	publish(null);
	message({ type: "sessionsToolbar", id: "list", mode: "list" });
	message({ type: "sessionsToolbar", id: "previous-list", mode: null });
	expect(toolbar()?.headerShown).toBe(true);
	postMessage.mockClear();
	for (const item of [
		...(toolbar()?.unstable_headerLeftItems?.({}) ?? []),
		...(toolbar()?.unstable_headerRightItems?.({}) ?? []),
	]) {
		if (item.type === "button") act(item.onPress);
	}
	expect(postMessage.mock.calls.map(([value]) => JSON.parse(value))).toEqual([
		{ type: "sessionsAction", id: "list", action: "close" },
		{ type: "sessionsAction", id: "list", action: "hubs" },
		{ type: "sessionsAction", id: "list", action: "settings" },
	]);
	message({ type: "sessionsToolbar", id: "list", mode: "launcher" });
	const open = toolbar()?.unstable_headerLeftItems?.({})[0];
	if (open?.type === "button") act(open.onPress);
	expect(JSON.parse(postMessage.mock.lastCall?.[0])).toEqual({ type: "sessionsAction", id: "list", action: "open" });
	expect(toolbar()?.unstable_headerRightItems?.({})).toEqual([]);
	for (const destination of ["open", "board", null]) {
		message({ type: "sessionsToolbar", id: "pane", mode: "navigation", back: true, destination, needsYou: 2 });
		if (destination) expect(toolbar()?.unstable_headerRightItems?.({})[0]).toMatchObject({ badge: { value: 2 } });
		postMessage.mockClear();
		for (const item of [
			...(toolbar()?.unstable_headerLeftItems?.({}) ?? []),
			...(toolbar()?.unstable_headerRightItems?.({}) ?? []),
		]) {
			if (item.type === "button") act(item.onPress);
		}
		expect(postMessage.mock.calls.map(([value]) => JSON.parse(value))).toEqual(
			["back", ...(destination ? [destination] : [])].map((action) => ({ type: "sessionsAction", id: "pane", action })),
		);
	}
	message({ type: "sessionsToolbar", id: "pane", mode: null });
	expect(toolbar()?.headerShown).toBe(false);
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
		<HubWebView profile={profile} token="hello" initialPath="/" onPath={onPath} onHubMenu={onHubMenu} />,
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
			<HubWebView profile={profile} token="hello" initialPath={alertPath} onPath={onPath} onHubMenu={onHubMenu} />,
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
			<HubWebView profile={profile} initialPath={path} token="saved" onPath={vi.fn()} onHubMenu={vi.fn()} />
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
