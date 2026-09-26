import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { ConnectionState } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { HubUpgradeSection } from "./HubUpgradeSection";
import {
	createHubUpgradeController,
	type UpgradeCheckpoint,
	type UpgradeStorage,
} from "./hubUpgrade";
import { useLiveReadiness, whenReady } from "./connectionDisplay";
import { render } from "./renderNative.testkit";

const alerts = vi.hoisted(() => ({
	buttons: [] as Array<Array<{ text: string; onPress?: () => void }>>,
}));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	Alert: {
		alert: (_title: string, _message: string, buttons: typeof alerts.buttons[number]) =>
			alerts.buttons.push(buttons),
	},
}));

const response = {
	release: "v1",
	channel: "stable",
	url: "https://example.test/update",
	archive: "archive",
	prefix: "prefix",
	binDir: "bin",
	shareBinDir: "share",
	installed: ["evener"],
	restartMessage: "restart",
};
const overview = { hub: { version: "running", commit: "abc" } };

function fixture() {
	let checkpoint: UpgradeCheckpoint | null = null;
	const requests: string[] = [];
	const storage: UpgradeStorage = {
		read: () => checkpoint,
		write: (next) => {
			checkpoint = next;
		},
		remove: (_hubId, attemptId) => {
			if (checkpoint?.attemptId === attemptId) checkpoint = null;
		},
	};
	const client = {
		request: vi.fn(async (method: string) => {
			requests.push(method);
			return method === "evener/upgrade" ? response : overview;
		}),
	} as unknown as ConversationClientLike;
	return { client, requests, storage, checkpoint: () => checkpoint };
}

function renderUpgrade(
	clientRef: { current: ConversationClientLike },
	stateRef: { current: ConnectionState },
	controller: ReturnType<typeof createHubUpgradeController>,
) {
	function Subject() {
		const canUseConnection = useLiveReadiness(
			"hub-a",
			clientRef.current,
			stateRef.current,
		);
		return (
			<HubUpgradeSection
				hubName="Work hub"
				state={controller.getSnapshot()}
				disabled={!canUseConnection()}
				onStart={whenReady(canUseConnection, () => {
					void controller.start();
				})}
				onRefresh={() => {}}
				onReviewAnother={() => {}}
			/>
		);
	}
	const tree = render(<Subject />);
	return { tree, rerender: () => tree.update(<Subject />) };
}

it("does not confirm an upgrade opened on a replaced client", async () => {
	alerts.buttons = [];
	const first = fixture();
	const second = fixture();
	const clientRef = { current: first.client };
	const stateRef = { current: "ready" as ConnectionState };
	const controller = createHubUpgradeController(
		"hub-a",
		first.client,
		first.storage,
		() => "attempt-a",
	);
	const rendered = renderUpgrade(clientRef, stateRef, controller);
	await act(async () => {
		rendered.tree.root.findByProps({ accessibilityLabel: "Upgrade hub" }).props.onPress();
	});
	stateRef.current = "reconnecting";
	await act(async () => {
		rendered.rerender();
	});
	clientRef.current = second.client;
	stateRef.current = "ready";
	await act(async () => {
		rendered.rerender();
	});
	await act(async () => {
		alerts.buttons[0]?.[1]?.onPress?.();
	});

	expect(first.requests).toEqual([]);
	expect(second.requests).toEqual([]);
	expect(first.checkpoint()).toBeNull();
});

it("starts an upgrade while the original client remains ready", async () => {
	alerts.buttons = [];
	const first = fixture();
	const clientRef = { current: first.client };
	const stateRef = { current: "ready" as ConnectionState };
	const controller = createHubUpgradeController(
		"hub-a",
		first.client,
		first.storage,
		() => "attempt-a",
	);
	const rendered = renderUpgrade(clientRef, stateRef, controller);
	await act(async () => {
		rendered.tree.root.findByProps({ accessibilityLabel: "Upgrade hub" }).props.onPress();
		await Promise.resolve();
	});
	await act(async () => {
		alerts.buttons[0]?.[1]?.onPress?.();
		await Promise.resolve();
	});

	expect(first.requests).toEqual(["evener/upgrade", "evener/settings/overview"]);
	expect(first.checkpoint()?.response).toEqual(response);
});
