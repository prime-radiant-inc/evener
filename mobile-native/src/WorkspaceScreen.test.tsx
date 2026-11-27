import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import * as SecureStore from "expo-secure-store";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { HubProfile } from "./connection";
import { HubWebView } from "./hubWeb/HubWebView";
import { pressable, render, settle } from "./renderNative.testkit";
import type { Routes } from "./screens";
import { WorkspaceScreen } from "./WorkspaceScreen";

const connection = vi.hoisted(() => ({ activeProfile: null as HubProfile | null }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => connection }));
vi.mock("expo-secure-store", () => ({ getItemAsync: vi.fn() }));
vi.mock("./hubWeb/HubWebView", () => ({ HubWebView: () => null }));
vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());

const STUDIO = { id: "studio", name: "Studio", origin: "https://studio.example" };
const DESK = { id: "desk", name: "Desk", origin: "https://desk.example" };
const share = { id: "share1", text: "Read this", createdAt: "2026-11-24T12:00:00Z", hubId: STUDIO.id, path: "/" };

beforeEach(() => {
	connection.activeProfile = STUDIO;
	vi.mocked(SecureStore.getItemAsync).mockImplementation(async (key) => {
		const profile = [STUDIO, DESK].find((candidate) => key === `evener.hub.${candidate.id}`);
		return profile ? JSON.stringify({ ...profile, token: profile.id }) : null;
	});
});

function mount() {
	const navigate = vi.fn();
	const navigation = { navigate } as unknown as NativeStackScreenProps<Routes, "Workspace">["navigation"];
	const workspace = () => (
		<WorkspaceScreen navigation={navigation} route={{ key: "workspace", name: "Workspace" }} share={share} />
	);
	const tree = render(workspace());
	return { tree, navigate, rerender: () => act(() => tree.update(workspace())) };
}

it("keeps saved Hubs reachable through a credential read failure and opens the workspace after retry", async () => {
	vi.mocked(SecureStore.getItemAsync).mockRejectedValueOnce(new Error("locked"));
	const { tree, navigate } = mount();
	await settle();
	act(() => pressable(tree, "Hubs on this iPhone")?.props.onPress());
	expect(navigate).toHaveBeenCalledWith("Hub", { screen: "Hubs" });
	act(() => pressable(tree, "Try again")?.props.onPress());
	await settle();
	const view = tree.root.findByType(HubWebView);
	expect(view.props.token).toBe(STUDIO.id);
	navigate.mockClear();
	act(() => view.props.onHubMenu());
	expect(navigate).toHaveBeenCalledWith("Hub", { screen: "Hubs" });
});

it("withdraws a pending share when the selected Hub changes", async () => {
	const { tree, rerender } = mount();
	await settle();
	expect(tree.root.findByType(HubWebView).props.share).toEqual(share);
	connection.activeProfile = DESK;
	rerender();
	await settle();
	expect(tree.root.findByType(HubWebView).props).toMatchObject({ profile: DESK, share: undefined });
});
