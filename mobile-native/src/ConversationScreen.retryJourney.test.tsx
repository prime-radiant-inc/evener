import "./conversationPlatform.testkit";
import { readFileSync } from "node:fs";
import type { ComponentProps } from "react";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import WebSocket from "ws";
import { installRetryWireFault } from "../../cmd/evener-hub/frontend/scripts/retryguard/wireFault.mjs";
import { ConnectionProvider, useConnection } from "./ConnectionProvider";
import { NativePreferencesProvider } from "./NativePreferencesProvider";
import { ConversationScreen } from "./screens";
import { drafts } from "./nativeDrafts";
import { pressable, render, unmountMountedTrees, untilRendered } from "./renderNative.testkit";

const nav = vi.hoisted(() => ({ state: { index: 0, routes: [{ key: "retry", name: "Conversation" }] } }));
vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, []),
		useNavigationState: <T,>(select: (state: typeof nav.state) => T) => select(nav.state),
		usePreventRemove: () => {},
	};
});

// This file is run by TestRetryContinuationJourney, which owns the real hub,
// daemon and external scripted provider. Default unit runs need no servers.
it.skipIf(!process.env.EVENER_RETRY_JOURNEY)(
	"continues a real failed turn through the native error-row action",
	async () => {
		const fixture = JSON.parse(readFileSync(process.env.EVENER_RETRY_JOURNEY ?? "", "utf8")) as {
			origin: string;
			token: string;
			scenarios: { ref: string; mode: string }[];
		};
		const scenario = fixture.scenarios[0];
		if (!scenario) throw new Error("missing journey scenario");
		// React Native's WebSocket accepts upgrade headers as its third argument.
		class NativeSocket extends WebSocket {
			constructor(url: string, _protocols: unknown, options: WebSocket.ClientOptions) {
				super(url, options);
			}
		}
		vi.stubGlobal("WebSocket", NativeSocket);
		const wire = installRetryWireFault();
		let connection: ReturnType<typeof useConnection> | undefined;
		function Probe() {
			connection = useConnection();
			return null;
		}
		const navigation = {
			isFocused: () => true,
			getState: () => nav.state,
			navigate: vi.fn(),
			push: vi.fn(),
			replace: vi.fn(),
			pop: vi.fn(),
			goBack: vi.fn(),
			setParams: vi.fn(),
			setOptions: vi.fn(),
		} as unknown as ComponentProps<typeof ConversationScreen>["navigation"];
		function App({ show }: { show: boolean }) {
			return (
				<ConnectionProvider>
					<NativePreferencesProvider>
						<Probe />
						{show ? (
							<ConversationScreen
								navigation={navigation}
								route={{
									key: "retry",
									name: "Conversation",
									params: { hubId: connection?.activeProfile?.id ?? "", ref: scenario.ref, title: "Session" },
								}}
							/>
						) : null}
					</NativePreferencesProvider>
				</ConnectionProvider>
			);
		}
		try {
			let tree!: ReactTestRenderer;
			await act(async () => {
				tree = render(<App show={false} />);
			});
			const composer = () =>
				tree.root.find((node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Message");
			const press = async (label: string) => {
				const button = pressable(tree, label);
				if (!button) throw new Error(`missing ${label} action`);
				expect(button.props.accessibilityState?.disabled).not.toBe(true);
				await act(async () => button.props.onPress());
			};
			await untilRendered(() => expect(connection?.loading).toBe(false));
			await act(async () => {
				await connection?.saveHub({ name: "retry fixture", origin: fixture.origin, token: fixture.token });
			});
			await untilRendered(() => expect(connection?.state).toBe("ready"));
			const hubId = connection?.activeProfile?.id;
			if (!hubId) throw new Error("hub profile was not selected");
			const draft = drafts.open({ hubId, sessionRef: scenario.ref });
			await act(async () => {
				tree.update(<App show />);
			});
			await untilRendered(() => expect(pressable(tree, "Retry")).toBeDefined());
			await act(async () => composer().props.onChangeText("RETRY_UNSENT_DRAFT"));
			expect(composer().props.value).toBe("RETRY_UNSENT_DRAFT");
			wire.mode = scenario.mode;
			wire.armed = true;
			await press("Retry");
			await untilRendered(() => expect(wire.requests.length).toBeGreaterThanOrEqual(1));
			expect(wire.requests[0]?.params.input).toEqual([{ type: "text", text: "continue" }]);
			expect(draft.getSnapshot().record.draft).toBe("RETRY_UNSENT_DRAFT");
			if (scenario.mode === "refused") {
				await untilRendered(() => expect(pressable(tree, "Edit")).toBeDefined());
				expect(wire.requests).toHaveLength(1);
				expect(draft.getSnapshot().record.draft).toBe("RETRY_UNSENT_DRAFT");
				// Edit protects an occupied composer. Clear it deliberately, then
				// restore the refused continuation rather than sending that draft.
				await act(async () => composer().props.onChangeText(""));
				await press("Edit");
				expect(composer().props.value).toBe("continue");
				await press("Send");
			}
			if (scenario.mode === "lost-reply") {
				await untilRendered(() => {
					expect(wire.dropped).toBe(1);
					expect(wire.connections).toBeGreaterThanOrEqual(2);
					expect(connection?.state).toBe("ready");
				});
			}
			// The native Markdown host receives its text in a prop, not children.
			await untilRendered(() =>
				expect(tree.root.findAllByType("EnrichedMarkdownText" as never).map((node) => node.props.markdown)).toContain(
					`RETRY_DONE_${scenario.mode}`,
				),
			);
			expect(draft.getSnapshot().record.draft).toBe(scenario.mode === "refused" ? "" : "RETRY_UNSENT_DRAFT");
			for (const request of wire.requests) {
				expect(request.params.input).toEqual([{ type: "text", text: "continue" }]);
				expect(request.params.clientMutationId).toEqual(expect.any(String));
			}
			if (scenario.mode === "lost-reply") {
				expect(new Set(wire.requests.map((request) => request.params.clientMutationId)).size).toBe(1);
			} else {
				expect(wire.requests).toHaveLength(scenario.mode === "refused" ? 2 : 1);
			}
		} finally {
			await unmountMountedTrees();
			vi.unstubAllGlobals();
		}
	},
	30000,
);
