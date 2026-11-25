import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { render } from "../renderNative.testkit";
import { HubWebView } from "./HubWebView";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("@react-navigation/native", () => ({ useIsFocused: () => true }));

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
